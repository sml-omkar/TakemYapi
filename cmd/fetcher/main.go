// Command fetcher pulls every ticket (plus full conversation history) from a
// Freshservice account and exports it as JSON and CSV for migration into a
// target ITSM tool.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"takemyapi/internal/config"
	"takemyapi/internal/exporter"
	"takemyapi/internal/freshservice"
)

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := freshservice.New(cfg.Domain, cfg.APIKey,
		freshservice.WithLogger(func(format string, args ...any) {
			logger.Printf(format, args...)
		}),
	)

	exp, err := exporter.New(cfg.OutputDir, cfg.SplitJSON)
	if err != nil {
		logger.Fatalf("exporter: %v", err)
	}

	start := time.Now()

	if cfg.Ticket > 0 {
		handleSingleTicket(ctx, logger, client, exp, cfg)
	} else {
		handleAllTickets(ctx, logger, client, exp, cfg)
	}

	meta := exporter.Meta{
		FetchedAt:    time.Now().UTC().Format(time.RFC3339),
		Requests:     client.Requests(),
		UpdatedSince: cfg.UpdatedSince,
		Status:       cfg.Status,
	}
	if err := exp.Close(meta); err != nil {
		logger.Fatalf("close exporter: %v", err)
	}

	ticketsN, threadsN := exp.Counts()
	logger.Printf("done in %s: %d tickets, %d conversations, %d API requests",
		time.Since(start).Round(time.Second), ticketsN, threadsN, client.Requests())
	logger.Printf("output written to %s", cfg.OutputDir)
}

// handleSingleTicket fetches one ticket by ID (with attachments + full
// conversation history) and writes it to the exporter.
func handleSingleTicket(ctx context.Context, logger *log.Logger, client *freshservice.Client, exp *exporter.Exporter, cfg *config.Config) {
	logger.Printf("fetching ticket %d", cfg.Ticket)
	t, err := client.GetTicket(ctx, cfg.Ticket)
	if err != nil {
		logger.Fatalf("get ticket %d: %v", cfg.Ticket, err)
	}

	batch := []freshservice.Ticket{*t}
	if err := enrichConversations(ctx, client, batch, cfg.Concurrency, cfg.Attachments, logger); err != nil {
		logger.Fatalf("conversations: %v", err)
	}
	if cfg.Attachments {
		if err := downloadAttachments(ctx, client, exp, batch, cfg.Concurrency, logger); err != nil {
			logger.Fatalf("attachments: %v", err)
		}
	}
	if err := exp.WriteTicket(batch[0]); err != nil {
		logger.Fatalf("write ticket %d: %v", t.ID, err)
	}
}

// handleAllTickets lists, enriches, and exports every matching ticket.
func handleAllTickets(ctx context.Context, logger *log.Logger, client *freshservice.Client, exp *exporter.Exporter, cfg *config.Config) {
	logger.Printf("fetching tickets (updated_since=%q status=%q concurrency=%d)", cfg.UpdatedSince, cfg.Status, cfg.Concurrency)

	params := freshservice.TicketsParams{UpdatedSince: cfg.UpdatedSince, Status: cfg.Status}
	tickets, err := client.ListTickets(ctx, params)
	if err != nil {
		logger.Fatalf("list tickets: %v", err)
	}
	if cfg.Limit > 0 && len(tickets) > cfg.Limit {
		tickets = tickets[:cfg.Limit]
	}
	logger.Printf("fetched %d tickets", len(tickets))

	// Split into already-fetched (cached on disk) and new tickets. Cached
	// tickets reload their full data from the per-ticket JSON files, so a
	// re-run does NOT re-query conversations or re-download attachments for
	// them; it only picks up new tickets. Tickets cached before the
	// requested-item/inline export existed (RequestedItems == nil) go to a
	// backfill list: only requested items are re-fetched and only missing
	// files downloaded, without re-querying conversations.
	var cached, fresh, backfill []freshservice.Ticket
	if cfg.SplitJSON {
		known, err := exp.KnownTicketIDs()
		if err != nil {
			logger.Fatalf("scan already-fetched tickets: %v", err)
		}
		for i := range tickets {
			if known[tickets[i].ID] {
				cachedT, err := exp.LoadTicket(tickets[i].ID)
				if err != nil {
					logger.Printf("warning: cannot reload cached ticket %d (%v), re-fetching", tickets[i].ID, err)
					fresh = append(fresh, tickets[i])
					continue
				}
				if needsBackfill(cachedT, cfg.Attachments) {
					backfill = append(backfill, *cachedT)
					continue
				}
				// Only skip when the cached data is complete: JSON present and
				// every referenced attachment file is on disk. If any file is
				// missing (e.g. an earlier download failed), re-process the
				// ticket so the gap gets filled.
				if exp.TicketComplete(*cachedT) {
					cached = append(cached, *cachedT)
					continue
				}
				logger.Printf("ticket %d cached but missing attachment files, re-fetching", tickets[i].ID)
			}
			fresh = append(fresh, tickets[i])
		}
	} else {
		fresh = tickets
	}
	logger.Printf("%d already fetched (skipping), %d need requested-item/inline backfill, %d new", len(cached), len(backfill), len(fresh))

	// Rebuild cached tickets into the combined JSON / CSVs first.
	for _, t := range cached {
		if err := exp.WriteTicket(t); err != nil {
			logger.Fatalf("write cached ticket %d: %v", t.ID, err)
		}
	}

	// Backfill cached tickets that predate the requested-item/inline export.
	if len(backfill) > 0 {
		if err := backfillRequestedItems(ctx, client, backfill, cfg.Concurrency, logger); err != nil {
			logger.Fatalf("backfill requested items: %v", err)
		}
		if cfg.Attachments {
			if err := downloadAttachments(ctx, client, exp, backfill, cfg.Concurrency, logger); err != nil {
				logger.Fatalf("backfill attachments: %v", err)
			}
		}
		for _, t := range backfill {
			if err := exp.WriteTicket(t); err != nil {
				logger.Fatalf("write backfilled ticket %d: %v", t.ID, err)
			}
		}
		logger.Printf("backfilled %d cached tickets with requested items + inline attachments", len(backfill))
	}

	const batchSize = 32
	for i := 0; i < len(fresh); i += batchSize {
		select {
		case <-ctx.Done():
			logger.Fatalf("interrupted: %v", ctx.Err())
		default:
		}

		end := min(i+batchSize, len(fresh))
		batch := fresh[i:end]

		if err := enrichConversations(ctx, client, batch, cfg.Concurrency, cfg.Attachments, logger); err != nil {
			logger.Fatalf("conversations: %v", err)
		}
		if cfg.Attachments {
			if err := downloadAttachments(ctx, client, exp, batch, cfg.Concurrency, logger); err != nil {
				logger.Fatalf("attachments: %v", err)
			}
		}
		for _, t := range batch {
			if err := exp.WriteTicket(t); err != nil {
				logger.Fatalf("write ticket %d: %v", t.ID, err)
			}
		}
		logger.Printf("processed %d/%d tickets", len(cached)+len(backfill)+end, len(tickets))
	}
}

// enrichConversations fetches the full conversation history (and optionally
// ticket-level attachments + requested items, which the list endpoint does
// not return) for the given tickets using a bounded worker pool, mutating the
// tickets in place. Inline <img> attachments embedded in the description and
// conversation bodies are always extracted (no extra API cost) so the
// downloader can fetch them like regular attachments.
func enrichConversations(ctx context.Context, client *freshservice.Client, tickets []freshservice.Ticket, workers int, withAttachments bool, logger *log.Logger) error {
	var wg sync.WaitGroup
	jobs := make(chan *freshservice.Ticket)
	errCh := make(chan error, 1)

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				if withAttachments {
					detail, err := client.GetTicket(ctx, t.ID)
					if err != nil {
						logger.Printf("warning: cannot load ticket %d attachments: %v", t.ID, err)
					} else {
						t.Attachments = detail.Attachments
						if detail.Description != "" {
							t.Description = detail.Description
						}
					}
					items, err := client.ListRequestedItems(ctx, t.ID)
					if err != nil {
						logger.Printf("warning: cannot load ticket %d requested items: %v", t.ID, err)
					} else {
						t.RequestedItems = items
					}
				}
				convs, err := client.ListConversations(ctx, t.ID)
				if err != nil {
					select {
					case errCh <- fmt.Errorf("ticket %d: %w", t.ID, err):
					default:
					}
					return
				}
				t.Conversations = convs
				freshservice.CollectInlineAttachments(t)
			}
		}()
	}

	for i := range tickets {
		jobs <- &tickets[i]
	}
	close(jobs)
	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

// needsBackfill reports whether a cached ticket predates the
// requested-item/inline export (RequestedItems == nil means it was stored
// before that fetch existed) and must be topped up on re-runs. Tickets
// fetched with -attachments=false also qualify once attachments are enabled.
func needsBackfill(t *freshservice.Ticket, attachments bool) bool {
	return attachments && t.RequestedItems == nil
}

// backfillRequestedItems fetches requested items for tickets cached before
// that export existed, without re-querying their conversations (already on
// disk). Inline attachments are re-extracted from the stored description and
// conversation bodies — CollectInlineAttachments keeps already-recorded
// download paths. Incident tickets answer requested_items with 404, which the
// client maps to an empty list, so no warning is logged for them.
func backfillRequestedItems(ctx context.Context, client *freshservice.Client, tickets []freshservice.Ticket, workers int, logger *log.Logger) error {
	var wg sync.WaitGroup
	jobs := make(chan *freshservice.Ticket)
	errCh := make(chan error, 1)

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				items, err := client.ListRequestedItems(ctx, t.ID)
				if err != nil {
					select {
					case errCh <- fmt.Errorf("ticket %d: %w", t.ID, err):
					default:
					}
					return
				}
				t.RequestedItems = items
				freshservice.CollectInlineAttachments(t)
			}
		}()
	}

	for i := range tickets {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- &tickets[i]:
		}
	}
	close(jobs)
	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

// attTask couples an attachment to the ticket (and optionally the
// conversation) it belongs to, so it can be downloaded and referenced back.
// Exactly one of attachment / inline is set.
type attTask struct {
	ticketID   int64
	convID     int64
	source     string
	attachment *freshservice.Attachment
	inline     *freshservice.InlineAttachment
}

// downloadAttachments saves every attachment of the given tickets (ticket
// level, conversation level, requested-item level, plus inline <img> files
// embedded in descriptions/bodies) into output/attachments/<ticket_id>/ and
// records the local path on the attachment object so it shows up in the
// exported ticket data. Already-downloaded files are skipped, so re-runs are
// resume-friendly. A single failing file is logged and skipped rather than
// aborting the whole run.
func downloadAttachments(ctx context.Context, client *freshservice.Client, exp *exporter.Exporter, tickets []freshservice.Ticket, workers int, logger *log.Logger) error {
	var tasks []attTask
	for i := range tickets {
		t := &tickets[i]
		// Backfill inline lists for tickets loaded from an older cache that
		// predates inline extraction (nil lists only, so existing
		// DownloadedPaths are never wiped).
		if needsInlineBackfill(t) {
			freshservice.CollectInlineAttachments(t)
		}
		for a := range t.Attachments {
			tasks = append(tasks, attTask{ticketID: t.ID, source: "ticket", attachment: &t.Attachments[a]})
		}
		for c := range t.Conversations {
			for a := range t.Conversations[c].Attachments {
				tasks = append(tasks, attTask{ticketID: t.ID, convID: t.Conversations[c].ID, source: "conversation", attachment: &t.Conversations[c].Attachments[a]})
			}
		}
		for r := range t.RequestedItems {
			for a := range t.RequestedItems[r].Attachments {
				tasks = append(tasks, attTask{ticketID: t.ID, source: "requested_item", attachment: &t.RequestedItems[r].Attachments[a]})
			}
		}
		for a := range t.InlineAttachments {
			tasks = append(tasks, attTask{ticketID: t.ID, source: "inline_ticket", inline: &t.InlineAttachments[a]})
		}
		for c := range t.Conversations {
			for a := range t.Conversations[c].InlineAttachments {
				tasks = append(tasks, attTask{ticketID: t.ID, convID: t.Conversations[c].ID, source: "inline_conversation", inline: &t.Conversations[c].InlineAttachments[a]})
			}
		}
	}

	var wg sync.WaitGroup
	jobs := make(chan attTask)
	var (
		mu                  sync.Mutex
		muErr               error
		downloaded, skipped int
	)

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				if task.inline != nil {
					downloadInline(ctx, client, exp, task, logger, &mu, &muErr, &downloaded, &skipped)
					continue
				}
				path := exp.AttachmentPath(task.ticketID, *task.attachment)
				if _, err := os.Stat(path); err == nil {
					task.attachment.DownloadedPath = path
					mu.Lock()
					skipped++
					mu.Unlock()
					continue
				}

				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					mu.Lock()
					if muErr == nil {
						muErr = fmt.Errorf("ticket %d attachment %d: %w", task.ticketID, task.attachment.ID, err)
					}
					mu.Unlock()
					continue
				}

				f, err := os.Create(path)
				if err != nil {
					mu.Lock()
					if muErr == nil {
						muErr = fmt.Errorf("ticket %d attachment %d: %w", task.ticketID, task.attachment.ID, err)
					}
					mu.Unlock()
					continue
				}

				if task.attachment.AttachmentURL == "" {
					f.Close()
					os.Remove(path)
					logger.Printf("warning: ticket %d attachment %d (%s) has no download URL, skipping", task.ticketID, task.attachment.ID, task.attachment.Name)
					continue
				}
				if err := client.DownloadAttachment(ctx, task.attachment.AttachmentURL, f); err != nil {
					f.Close()
					os.Remove(path)
					logger.Printf("warning: failed to download ticket %d attachment %d (%s): %v", task.ticketID, task.attachment.ID, task.attachment.Name, err)
					continue
				}
				f.Close()

				task.attachment.DownloadedPath = path
				if err := exp.RecordAttachment(task.ticketID, *task.attachment, task.convID, path, task.source); err != nil {
					mu.Lock()
					if muErr == nil {
						muErr = err
					}
					mu.Unlock()
				}
				mu.Lock()
				downloaded++
				mu.Unlock()
			}
		}()
	}

	for _, t := range tasks {
		jobs <- t
	}
	close(jobs)
	wg.Wait()

	logger.Printf("attachments: %d downloaded, %d already present", downloaded, skipped)
	return muErr
}

// downloadInline saves one inline <img> file. It mirrors the regular
// attachment worker but uses the tokenised inline URL and the inline_
// filename namespace.
func downloadInline(ctx context.Context, client *freshservice.Client, exp *exporter.Exporter, task attTask, logger *log.Logger, mu *sync.Mutex, muErr *error, downloaded, skipped *int) {
	in := task.inline
	path := exp.InlineAttachmentPath(task.ticketID, *in)
	if _, err := os.Stat(path); err == nil {
		in.DownloadedPath = path
		mu.Lock()
		*skipped++
		mu.Unlock()
		return
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		mu.Lock()
		if *muErr == nil {
			*muErr = fmt.Errorf("ticket %d inline attachment %d: %w", task.ticketID, in.ID, err)
		}
		mu.Unlock()
		return
	}

	if in.URL == "" {
		logger.Printf("warning: ticket %d inline attachment %d (%s) has no download URL, skipping", task.ticketID, in.ID, in.Name)
		return
	}

	f, err := os.Create(path)
	if err != nil {
		mu.Lock()
		if *muErr == nil {
			*muErr = fmt.Errorf("ticket %d inline attachment %d: %w", task.ticketID, in.ID, err)
		}
		mu.Unlock()
		return
	}

	if err := client.DownloadAttachment(ctx, in.URL, f); err != nil {
		f.Close()
		os.Remove(path)
		logger.Printf("warning: failed to download ticket %d inline attachment %d (%s): %v", task.ticketID, in.ID, in.Name, err)
		return
	}
	f.Close()

	in.DownloadedPath = path
	if err := exp.RecordInlineAttachment(task.ticketID, *in, path, task.source); err != nil {
		mu.Lock()
		if *muErr == nil {
			*muErr = err
		}
		mu.Unlock()
	}
	mu.Lock()
	*downloaded++
	mu.Unlock()
}

// needsInlineBackfill reports whether a ticket predates inline extraction
// (nil inline lists with HTML bodies that may contain <img> tags). Only nil
// lists trigger a backfill so existing DownloadedPaths are never wiped.
func needsInlineBackfill(t *freshservice.Ticket) bool {
	if t.InlineAttachments == nil && t.Description != "" {
		return true
	}
	for _, c := range t.Conversations {
		if c.InlineAttachments == nil && c.Body != "" {
			return true
		}
	}
	return false
}

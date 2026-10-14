// Package exporter writes fetched Freshservice data to disk as JSON and CSV.
package exporter

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"takemyapi/internal/freshservice"
)

// Meta describes a single fetch run and is written to meta.json.
type Meta struct {
	FetchedAt     string `json:"fetched_at"`
	TicketCount   int    `json:"ticket_count"`
	Conversations int    `json:"conversation_count"`
	Attachments   int    `json:"attachment_count"`
	Requests      int    `json:"api_requests"`
	UpdatedSince  string `json:"updated_since,omitempty"`
	Status        string `json:"status,omitempty"`
}

// Exporter accumulates tickets and conversations and writes JSON + CSV output.
type Exporter struct {
	dir       string
	splitJSON bool

	mu          sync.Mutex
	jsonFile    *os.File
	jsonEnc     *json.Encoder
	jsonStarted bool

	ticketsCSV *csv.Writer
	convCSV    *csv.Writer
	attCSV     *csv.Writer

	ticketCount       int
	conversationCount int
	attachmentCount   int
}

// New creates the output layout and returns a ready exporter.
func New(dir string, splitJSON bool) (*Exporter, error) {
	if err := os.MkdirAll(filepath.Join(dir, "json", "tickets"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "csv"), 0o755); err != nil {
		return nil, err
	}

	e := &Exporter{dir: dir, splitJSON: splitJSON}

	jsonPath := filepath.Join(dir, "json", "tickets.json")
	f, err := os.Create(jsonPath)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString("["); err != nil {
		f.Close()
		return nil, err
	}
	e.jsonFile = f
	e.jsonEnc = json.NewEncoder(f)

	ticketsPath := filepath.Join(dir, "csv", "tickets.csv")
	tf, err := os.Create(ticketsPath)
	if err != nil {
		return nil, err
	}
	e.ticketsCSV = csv.NewWriter(tf)
	if err := e.ticketsCSV.Write(ticketHeaders()); err != nil {
		return nil, err
	}

	convPath := filepath.Join(dir, "csv", "conversations.csv")
	cf, err := os.Create(convPath)
	if err != nil {
		return nil, err
	}
	e.convCSV = csv.NewWriter(cf)
	if err := e.convCSV.Write(conversationHeaders()); err != nil {
		return nil, err
	}

	attPath := filepath.Join(dir, "csv", "attachments.csv")
	af, err := os.Create(attPath)
	if err != nil {
		return nil, err
	}
	e.attCSV = csv.NewWriter(af)
	if err := e.attCSV.Write(attachmentHeaders()); err != nil {
		return nil, err
	}

	return e, nil
}

// WriteTicket writes a single ticket (with embedded conversations) to all
// configured outputs. It is safe for concurrent use.
func (e *Exporter) WriteTicket(t freshservice.Ticket) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.jsonStarted {
		// Insert a comma before every ticket after the first one so the file
		// stays valid JSON: [ {...},{...} ]
		if _, err := e.jsonFile.WriteString(","); err != nil {
			return err
		}
	}
	if err := e.jsonEnc.Encode(t); err != nil {
		return err
	}
	e.jsonStarted = true

	if e.splitJSON {
		path := filepath.Join(e.dir, "json", "tickets", fmt.Sprintf("%d.json", t.ID))
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(f).Encode(t); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}

	if err := e.ticketsCSV.Write(ticketRow(t)); err != nil {
		return err
	}
	for _, c := range t.Conversations {
		if err := e.convCSV.Write(conversationRow(c)); err != nil {
			return err
		}
	}

	e.ticketCount++
	e.conversationCount += len(t.Conversations)
	return nil
}

// KnownTicketIDs returns the set of ticket IDs already exported as per-ticket
// JSON files. It requires splitJSON mode (the default).
func (e *Exporter) KnownTicketIDs() (map[int64]bool, error) {
	ids := map[int64]bool{}
	dir := filepath.Join(e.dir, "json", "tickets")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, en := range entries {
		if en.IsDir() || !strings.HasSuffix(en.Name(), ".json") {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSuffix(en.Name(), ".json"), 10, 64)
		if err != nil {
			continue
		}
		ids[id] = true
	}
	return ids, nil
}

// LoadTicket reads a previously exported per-ticket JSON file back into a
// Ticket struct (including its embedded conversations and attachment paths).
func (e *Exporter) LoadTicket(id int64) (*freshservice.Ticket, error) {
	path := filepath.Join(e.dir, "json", "tickets", strconv.FormatInt(id, 10)+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t freshservice.Ticket
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("decode cached ticket %d: %w", id, err)
	}
	return &t, nil
}

// AttachmentComplete reports whether a ticket's attachment file is present on
// disk under the expected output/attachments path.
func (e *Exporter) AttachmentComplete(ticketID int64, a freshservice.Attachment) bool {
	_, err := os.Stat(e.AttachmentPath(ticketID, a))
	return err == nil
}

// TicketComplete reports whether a ticket has been fully exported: the
// per-ticket JSON exists and every referenced attachment file is on disk
// (regular + requested-item + inline attachments).
func (e *Exporter) TicketComplete(t freshservice.Ticket) bool {
	if !e.AttachmentCompleteList(t.Attachments, t.ID) {
		return false
	}
	for _, c := range t.Conversations {
		if !e.AttachmentCompleteList(c.Attachments, t.ID) {
			return false
		}
	}
	for _, r := range t.RequestedItems {
		if !e.AttachmentCompleteList(r.Attachments, t.ID) {
			return false
		}
	}
	for _, in := range t.InlineAttachments {
		if !e.InlineComplete(t.ID, in) {
			return false
		}
	}
	for _, c := range t.Conversations {
		for _, in := range c.InlineAttachments {
			if !e.InlineComplete(t.ID, in) {
				return false
			}
		}
	}
	return true
}

// AttachmentCompleteList is TicketComplete's inner loop.
func (e *Exporter) AttachmentCompleteList(atts []freshservice.Attachment, ticketID int64) bool {
	for _, a := range atts {
		if !e.AttachmentComplete(ticketID, a) {
			return false
		}
	}
	return true
}

// InlineComplete reports whether an inline <img> file is present on disk.
func (e *Exporter) InlineComplete(ticketID int64, a freshservice.InlineAttachment) bool {
	_, err := os.Stat(e.InlineAttachmentPath(ticketID, a))
	return err == nil
}

// AttachmentPath returns the on-disk path where a ticket's attachment should
// be saved. The filename prefix with the attachment ID guarantees uniqueness.
func (e *Exporter) AttachmentPath(ticketID int64, a freshservice.Attachment) string {
	dir := filepath.Join(e.dir, "attachments", strconv.FormatInt(ticketID, 10))
	return filepath.Join(dir, fmt.Sprintf("%d_%s", a.ID, sanitizeFilename(a.Name)))
}

// InlineAttachmentPath returns the on-disk path for an inline <img>
// attachment. The inline_ prefix keeps these apart from regular attachments
// even when IDs overlap.
func (e *Exporter) InlineAttachmentPath(ticketID int64, a freshservice.InlineAttachment) string {
	dir := filepath.Join(e.dir, "attachments", strconv.FormatInt(ticketID, 10))
	return filepath.Join(dir, fmt.Sprintf("inline_%d_%s", a.ID, sanitizeFilename(a.Name)))
}

// RecordAttachment writes a row to attachments.csv after a file is saved.
// source is one of: ticket, conversation, requested_item.
func (e *Exporter) RecordAttachment(ticketID int64, a freshservice.Attachment, convID int64, savedPath string, source string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.attCSV.Write(attachmentRow(ticketID, convID, a, savedPath, source)); err != nil {
		return err
	}
	e.attachmentCount++
	return nil
}

// RecordInlineAttachment writes a row to attachments.csv after an inline
// <img> file is saved. source is inline_ticket or inline_conversation.
func (e *Exporter) RecordInlineAttachment(ticketID int64, a freshservice.InlineAttachment, savedPath string, source string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.attCSV.Write(inlineAttachmentRow(ticketID, a, savedPath, source)); err != nil {
		return err
	}
	e.attachmentCount++
	return nil
}

// Close finalizes the combined JSON array and flushes CSV writers.
func (e *Exporter) Close(meta Meta) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	meta.TicketCount = e.ticketCount
	meta.Conversations = e.conversationCount
	meta.Attachments = e.attachmentCount

	if _, err := e.jsonFile.WriteString("]"); err != nil {
		return err
	}
	if err := e.jsonFile.Close(); err != nil {
		return err
	}
	e.ticketsCSV.Flush()
	if err := e.ticketsCSV.Error(); err != nil {
		return err
	}
	e.convCSV.Flush()
	if err := e.convCSV.Error(); err != nil {
		return err
	}
	e.attCSV.Flush()
	if err := e.attCSV.Error(); err != nil {
		return err
	}

	metaPath := filepath.Join(e.dir, "meta.json")
	mb, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath, mb, 0o644)
}

// Counts returns the current ticket and conversation counts.
func (e *Exporter) Counts() (tickets, conversations int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ticketCount, e.conversationCount
}

// sanitizeFilename strips characters that are unsafe in filenames, empty it,
// or would allow path traversal.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "file"
	}
	return s
}

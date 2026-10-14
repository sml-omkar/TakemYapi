package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"takemyapi/internal/exporter"
	"takemyapi/internal/freshservice"
)

func TestNeedsBackfill(t *testing.T) {
	old := &freshservice.Ticket{ID: 1} // stored before the export existed
	if !needsBackfill(old, true) {
		t.Fatalf("expected old cache to need backfill")
	}
	if needsBackfill(old, false) {
		t.Fatalf("backfill must stay off when -attachments=false")
	}
	fetchedEmpty := &freshservice.Ticket{ID: 2, RequestedItems: []freshservice.RequestedItem{}}
	if needsBackfill(fetchedEmpty, true) {
		t.Fatalf("ticket with fetched (empty) requested items must not backfill again")
	}
}

// TestBackfillRequestedItemsAndInline exercises the whole top-up path for a
// ticket cached before the requested-item/inline export: requested items are
// fetched (404-safe), inline images extracted, and every file downloaded and
// indexed — without touching conversations.
func TestBackfillRequestedItemsAndInline(t *testing.T) {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/tickets/42/requested_items", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"requested_items":[{"id":7,"service_item_id":50,"service_item_name":"Consulting","stage":1,"quantity":1,"custom_fields":{},"attachments":[{"id":99,"name":"sr-file.pdf","content_type":"application/pdf","size":4,"attachment_url":"http://%s/files/sr"}]}]}`, r.Host)
	})
	mux.HandleFunc("/files/sr", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("DATA"))
	})
	mux.HandleFunc("/inline/attachment", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("token") {
		case "pic2":
			_, _ = w.Write([]byte("IMG2"))
		default:
			_, _ = w.Write([]byte("IMG"))
		}
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	client := freshservice.New("example", "key", freshservice.WithBaseURL(srv.URL+"/api/v2"))
	logger := log.New(io.Discard, "", 0)
	ctx := context.Background()

	// Old-cache shape: no requested items, no inline lists, conversations
	// already on disk.
	tickets := []freshservice.Ticket{{
		ID:          42,
		Description: `<div><img src="` + srv.URL + `/inline/attachment?token=pic" data-id="5" alt="pic.png"></div>`,
		Conversations: []freshservice.Conversation{
			{ID: 9, TicketID: 42, Body: `<div><img src="` + srv.URL + `/inline/attachment?token=pic2" data-id="6"></div>`},
		},
	}}
	if !needsBackfill(&tickets[0], true) {
		t.Fatalf("fixture should qualify for backfill")
	}

	if err := backfillRequestedItems(ctx, client, tickets, 2, logger); err != nil {
		t.Fatalf("backfillRequestedItems: %v", err)
	}
	if len(tickets[0].RequestedItems) != 1 {
		t.Fatalf("expected 1 requested item, got %d", len(tickets[0].RequestedItems))
	}
	if len(tickets[0].InlineAttachments) != 1 {
		t.Fatalf("expected 1 ticket inline, got %+v", tickets[0].InlineAttachments)
	}
	if len(tickets[0].Conversations[0].InlineAttachments) != 1 {
		t.Fatalf("expected 1 conversation inline, got %+v", tickets[0].Conversations[0].InlineAttachments)
	}

	exp, err := exporter.New(t.TempDir(), false)
	if err != nil {
		t.Fatalf("exporter: %v", err)
	}
	if err := downloadAttachments(ctx, client, exp, tickets, 2, logger); err != nil {
		t.Fatalf("downloadAttachments: %v", err)
	}

	// Requested-item attachment downloaded and linked back.
	got := tickets[0].RequestedItems[0].Attachments[0]
	if got.DownloadedPath == "" {
		t.Fatalf("requested-item attachment has no downloaded path")
	}
	if b, err := os.ReadFile(got.DownloadedPath); err != nil || string(b) != "DATA" {
		t.Fatalf("requested-item file wrong: %q %v", b, err)
	}
	// Inline attachments downloaded and linked back.
	for _, in := range tickets[0].InlineAttachments {
		if in.DownloadedPath == "" {
			t.Fatalf("ticket inline %d has no downloaded path", in.ID)
		}
	}
	for _, in := range tickets[0].Conversations[0].InlineAttachments {
		if in.DownloadedPath == "" {
			t.Fatalf("conv inline %d has no downloaded path", in.ID)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(got.DownloadedPath), "inline_5_pic.png")); err != nil {
		t.Fatalf("expected inline file on disk: %v", err)
	}

	if err := exp.Close(exporter.Meta{}); err != nil {
		t.Fatalf("close: %v", err)
	}
}

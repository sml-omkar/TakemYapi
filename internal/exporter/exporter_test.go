package exporter

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"takemyapi/internal/freshservice"
)

func TestExporterWritesValidJSONAndCSV(t *testing.T) {
	dir := t.TempDir()
	e, err := New(dir, true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t1 := freshservice.Ticket{
		ID:             100,
		Subject:        "Password reset",
		Status:         4,
		Tags:           []string{"ops", "priority"},
		RequesterEmail: "a@example.com",
		Conversations: []freshservice.Conversation{
			{ID: 5, BodyText: "hello, world", TicketID: 100},
		},
	}
	if err := e.WriteTicket(t1); err != nil {
		t.Fatalf("WriteTicket: %v", err)
	}

	meta := Meta{FetchedAt: "2026-01-01T00:00:00Z", Requests: 42}
	if err := e.Close(meta); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Combined JSON must be a valid array with one ticket.
	raw, err := os.ReadFile(filepath.Join(dir, "json", "tickets.json"))
	if err != nil {
		t.Fatalf("read combined json: %v", err)
	}
	var decoded []freshservice.Ticket
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("combined json not valid: %v", err)
	}
	if len(decoded) != 1 || decoded[0].ID != 100 {
		t.Fatalf("unexpected combined json: %+v", decoded)
	}
	if len(decoded[0].Conversations) != 1 {
		t.Fatalf("expected embedded conversation")
	}

	// Per-ticket JSON file.
	if _, err := os.Stat(filepath.Join(dir, "json", "tickets", "100.json")); err != nil {
		t.Fatalf("per-ticket json missing: %v", err)
	}

	// Tickets CSV has header + 1 row.
	ticketsCSV := openCSV(t, filepath.Join(dir, "csv", "tickets.csv"))
	if len(ticketsCSV) != 2 || ticketsCSV[1][0] != "100" {
		t.Fatalf("unexpected tickets.csv: %v", ticketsCSV)
	}

	// Conversations CSV has header + 1 row.
	convCSV := openCSV(t, filepath.Join(dir, "csv", "conversations.csv"))
	if len(convCSV) != 2 || convCSV[1][1] != "100" {
		t.Fatalf("unexpected conversations.csv: %v", convCSV)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"spec final v2.pdf":     "spec final v2.pdf",
		"../../etc/passwd":      ".._.._etc_passwd",
		"a/b\\c:d*e?f\"g<h>i|j": "a_b_c_d_e_f_g_h_i_j",
		"":                      "file",
		"   ":                   "file",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAttachmentRecordedWithPath(t *testing.T) {
	dir := t.TempDir()
	e, err := New(dir, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	att := freshservice.Attachment{ID: 9, Name: "invoice.pdf", ContentType: "application/pdf", Size: 1234}
	path := e.AttachmentPath(77, att)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := e.RecordAttachment(77, att, 0, path, "ticket"); err != nil {
		t.Fatalf("RecordAttachment: %v", err)
	}
	if err := e.Close(Meta{}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows := openCSV(t, filepath.Join(dir, "csv", "attachments.csv"))
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %v", rows)
	}
	if rows[1][1] != "77" || rows[1][3] != "invoice.pdf" {
		t.Fatalf("unexpected attachments row: %v", rows[1])
	}
	if rows[1][6] != path {
		t.Fatalf("saved_path mismatch: got %q want %q", rows[1][6], path)
	}
}

func TestKnownIDsAndReload(t *testing.T) {
	dir := t.TempDir()
	e, err := New(dir, true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t1 := freshservice.Ticket{
		ID:            201,
		Subject:       "VPN",
		Conversations: []freshservice.Conversation{{ID: 7, TicketID: 201, BodyText: "hi"}},
	}
	if err := e.WriteTicket(t1); err != nil {
		t.Fatalf("WriteTicket: %v", err)
	}
	if err := e.Close(Meta{}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A fresh exporter over the same dir should detect ticket 201 and reload it.
	e2, err := New(dir, true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	known, err := e2.KnownTicketIDs()
	if err != nil {
		t.Fatalf("KnownTicketIDs: %v", err)
	}
	if !known[201] {
		t.Fatalf("expected ticket 201 to be known, got %v", known)
	}
	loaded, err := e2.LoadTicket(201)
	if err != nil {
		t.Fatalf("LoadTicket: %v", err)
	}
	if loaded.Subject != "VPN" || len(loaded.Conversations) != 1 {
		t.Fatalf("reloaded ticket wrong: %+v", loaded)
	}
	if err := e2.Close(Meta{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func openCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return rows
}

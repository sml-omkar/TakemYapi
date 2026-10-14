package freshservice

import "testing"

func TestExtractInlineAttachments(t *testing.T) {
	html := `<div><img src="https://auattachment.freshservice.com/inline/attachment?token=AAA" class="inline-image" data-id="76027509951" alt="image.png">` +
		`<img src="https://example.com/logo.png" alt="logo">` +
		`<img src="https://auattachment.freshservice.com/inline/attachment?token=AAA" data-id="76027509951" alt="image.png"></div>`
	got := ExtractInlineAttachments(html, 0)
	if len(got) != 1 {
		t.Fatalf("expected 1 unique inline, got %d: %+v", len(got), got)
	}
	if got[0].ID != 76027509951 {
		t.Errorf("expected ID 76027509951, got %d", got[0].ID)
	}
	if got[0].Name != "image.png" {
		t.Errorf("expected name image.png, got %q", got[0].Name)
	}
	if got[0].ConversationID != 0 {
		t.Errorf("expected conv 0, got %d", got[0].ConversationID)
	}
}

func TestExtractInlineAttachmentsNoAlt(t *testing.T) {
	html := `<img src="https://auattachment.freshservice.com/inline/attachment?token=BBB" data-id="123">`
	got := ExtractInlineAttachments(html, 7)
	if len(got) != 1 {
		t.Fatalf("expected 1 inline, got %d", len(got))
	}
	if got[0].Name != "inline_123.png" {
		t.Errorf("expected fallback name inline_123.png, got %q", got[0].Name)
	}
	if got[0].ConversationID != 7 {
		t.Errorf("expected conv 7, got %d", got[0].ConversationID)
	}
}

func TestExtractInlineAttachmentsNone(t *testing.T) {
	if got := ExtractInlineAttachments(`<div>no images here</div>`, 0); len(got) != 0 {
		t.Fatalf("expected none, got %+v", got)
	}
	if got := ExtractInlineAttachments(`<img src="https://example.com/a.png">`, 0); len(got) != 0 {
		t.Fatalf("expected non-inline img to be skipped, got %+v", got)
	}
}

func TestCollectInlineAttachments(t *testing.T) {
	ticket := &Ticket{
		ID:          1,
		Description: `<img src="https://x/inline/attachment?token=A" data-id="11" alt="a.png">`,
		Conversations: []Conversation{
			{ID: 5, Body: `<img src="https://x/inline/attachment?token=B" data-id="22" alt="b.png">`},
			{ID: 6, Body: `<div>plain</div>`},
		},
	}
	if n := CollectInlineAttachments(ticket); n != 2 {
		t.Fatalf("expected 2 total, got %d", n)
	}
	if len(ticket.InlineAttachments) != 1 || ticket.InlineAttachments[0].ID != 11 {
		t.Fatalf("bad ticket inline: %+v", ticket.InlineAttachments)
	}
	if len(ticket.Conversations[0].InlineAttachments) != 1 || ticket.Conversations[0].InlineAttachments[0].ConversationID != 5 {
		t.Fatalf("bad conv inline: %+v", ticket.Conversations[0].InlineAttachments)
	}
	if len(ticket.Conversations[1].InlineAttachments) != 0 {
		t.Fatalf("expected no inline on conv 6, got %+v", ticket.Conversations[1].InlineAttachments)
	}
}

func TestCollectInlineAttachmentsKeepsDownloadedPaths(t *testing.T) {
	ticket := &Ticket{
		ID:          1,
		Description: `<img src="https://x/inline/attachment?token=A" data-id="11" alt="a.png">`,
		InlineAttachments: []InlineAttachment{
			{ID: 11, Name: "a.png", URL: "https://x/inline/attachment?token=A", DownloadedPath: "/out/1/inline_11_a.png"},
		},
	}
	if n := CollectInlineAttachments(ticket); n != 1 {
		t.Fatalf("expected 1, got %d", n)
	}
	if got := ticket.InlineAttachments[0].DownloadedPath; got != "/out/1/inline_11_a.png" {
		t.Fatalf("downloaded path was wiped, got %q", got)
	}
}

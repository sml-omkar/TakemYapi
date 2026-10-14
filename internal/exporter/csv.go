package exporter

import (
	"strconv"
	"strings"
	"time"

	"takemyapi/internal/freshservice"
)

func ticketHeaders() []string {
	return []string{
		"id", "subject", "status", "priority", "source", "deleted", "spam",
		"is_escalated", "ticket_type", "category", "sub_category",
		"item_category", "requester_id", "requester_email", "responder_id",
		"group_id", "created_at", "updated_at", "due_by", "fr_due_by",
		"cc_emails", "fwd_emails", "reply_cc_emails", "to_emails", "tags",
		"conversation_count",
	}
}

func conversationHeaders() []string {
	return []string{
		"id", "ticket_id", "source", "from_email", "to_emails", "cc_emails",
		"bcc_emails", "fwd_emails", "user_id", "support_email",
		"replied_to_email", "created_at", "updated_at", "body_text",
	}
}

func attachmentHeaders() []string {
	return []string{
		"id", "ticket_id", "conversation_id", "name", "content_type", "size",
		"saved_path", "source",
	}
}

func str(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case *string:
		if s == nil {
			return ""
		}
		return *s
	default:
		return ""
	}
}

func timeStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func timePtrStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return timeStr(*t)
}

func intPtrStr(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}

func intStr(v int) string {
	return strconv.Itoa(v)
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func join(vals []string) string {
	return strings.Join(vals, ";")
}

func ticketRow(t freshservice.Ticket) []string {
	return []string{
		intStr(int(t.ID)),
		t.Subject,
		intStr(t.Status),
		intStr(t.Priority),
		intStr(t.Source),
		boolStr(t.Deleted),
		boolStr(t.Spam),
		boolStr(t.IsEscalated),
		str(t.TicketType),
		t.Category,
		t.SubCategory,
		t.ItemCategory,
		intStr(int(t.RequesterID)),
		t.RequesterEmail,
		intPtrStr(t.ResponderID),
		intPtrStr(t.GroupID),
		timeStr(t.CreatedAt),
		timeStr(t.UpdatedAt),
		timePtrStr(t.DueBy),
		timePtrStr(t.FRDueBy),
		join(t.CCEmails),
		join(t.FwdEmails),
		join(t.ReplyCCEmails),
		join(t.ToEmails),
		join(t.Tags),
		intStr(len(t.Conversations)),
	}
}

func conversationRow(c freshservice.Conversation) []string {
	return []string{
		intStr(int(c.ID)),
		intStr(int(c.TicketID)),
		intStr(c.Source),
		str(c.FromEmail),
		join(c.ToEmails),
		join(c.CCEmails),
		join(c.BCCEmails),
		join(c.FwdEmails),
		intPtrStr(c.UserID),
		str(c.SupportEmail),
		str(c.RepliedToEmail),
		timeStr(c.CreatedAt),
		timeStr(c.UpdatedAt),
		c.BodyText,
	}
}

func attachmentRow(ticketID int64, convID int64, a freshservice.Attachment, savedPath string, source string) []string {
	return []string{
		intStr(int(a.ID)),
		intStr(int(ticketID)),
		intStr(int(convID)),
		a.Name,
		a.ContentType,
		intStr(a.Size),
		savedPath,
		source,
	}
}

// inlineAttachmentRow writes an inline <img> download to attachments.csv.
// Columns match attachmentRow so both kinds share one index.
func inlineAttachmentRow(ticketID int64, a freshservice.InlineAttachment, savedPath string, source string) []string {
	return []string{
		intStr(int(a.ID)),
		intStr(int(ticketID)),
		intStr(int(a.ConversationID)),
		a.Name,
		"",
		"",
		savedPath,
		source,
	}
}

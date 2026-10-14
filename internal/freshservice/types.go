package freshservice

import "time"

// Ticket mirrors the ticket object returned by the Freshservice v2 API.
// Fields that are commonly absent are pointers so marshalling stays clean.
type Ticket struct {
	ID             int64          `json:"id"`
	Subject        string         `json:"subject"`
	Description    string         `json:"description"`
	Status         int            `json:"status"`
	Priority       int            `json:"priority"`
	RequesterID    int64          `json:"requester_id"`
	RequesterEmail string         `json:"requester_email"`
	ResponderID    *int64         `json:"responder_id"`
	GroupID        *int64         `json:"group_id"`
	Category       string         `json:"category"`
	SubCategory    string         `json:"sub_category"`
	ItemCategory   string         `json:"item_category"`
	TicketType     *string        `json:"ticket_type"`
	Source         int            `json:"source"`
	Deleted        bool           `json:"deleted"`
	Spam           bool           `json:"spam"`
	IsEscalated    bool           `json:"is_escalated"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DueBy          *time.Time     `json:"due_by"`
	FRDueBy        *time.Time     `json:"fr_due_by"`
	CCEmails       []string       `json:"cc_emails"`
	FwdEmails      []string       `json:"fwd_emails"`
	ReplyCCEmails  []string       `json:"reply_cc_emails"`
	ToEmails       []string       `json:"to_emails"`
	Tags           []string       `json:"tags"`
	CustomFields   map[string]any `json:"custom_fields"`
	Attachments    []Attachment   `json:"attachments"`
	Conversations  []Conversation `json:"conversations,omitempty"`
	// RequestedItems holds the service-catalog items for Service Request
	// tickets (GET /tickets/{id}/requested_items). No omitempty: a fetched
	// ticket always persists [] (possibly empty) so re-runs can tell
	// "fetched, none" apart from an older cache that never fetched them
	// (nil) and backfill the gap.
	RequestedItems []RequestedItem `json:"requested_items"`
	// InlineAttachments are <img src=".../inline/attachment?token=..."> files
	// embedded in the ticket Description. Like conversation inline files,
	// they are NOT listed in Attachments by the API.
	InlineAttachments []InlineAttachment `json:"inline_attachments,omitempty"`
}

// Conversation mirrors a reply or note on a ticket.
type Conversation struct {
	ID             int64        `json:"id"`
	Body           string       `json:"body"`
	BodyText       string       `json:"body_text"`
	ToEmails       []string     `json:"to_emails"`
	FromEmail      *string      `json:"from_email"`
	UserID         *int64       `json:"user_id"`
	SupportEmail   *string      `json:"support_email"`
	TicketID       int64        `json:"ticket_id"`
	Source         int          `json:"source"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	CCEmails       []string     `json:"cc_emails"`
	BCCEmails      []string     `json:"bcc_emails"`
	FwdEmails      []string     `json:"fwd_emails"`
	RepliedToEmail *string      `json:"replied_to_email"`
	Attachments    []Attachment `json:"attachments"`
	// InlineAttachments are <img src=".../inline/attachment?token=..."> files
	// embedded in the conversation Body. They are NOT listed in Attachments
	// by the API, so the fetcher extracts and downloads them separately.
	InlineAttachments []InlineAttachment `json:"inline_attachments,omitempty"`
}

// Attachment mirrors the attachment object; DownloadedPath is populated by
// the fetcher with the local file location under the output folder.
type Attachment struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	ContentType    string    `json:"content_type"`
	Size           int       `json:"size"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	AttachmentURL  string    `json:"attachment_url"`
	DownloadedPath string    `json:"downloaded_path,omitempty"`
}

// RequestedItem mirrors one entry of GET /tickets/{id}/requested_items.
// Service Request tickets carry their catalog answers + file uploads here;
// its Attachments use the same shape (attachment_url) as ticket attachments.
type RequestedItem struct {
	ID              int64          `json:"id"`
	ServiceItemID   int64          `json:"service_item_id"`
	ServiceItemName string         `json:"service_item_name"`
	Stage           int            `json:"stage"`
	Quantity        int            `json:"quantity"`
	CustomFields    map[string]any `json:"custom_fields"`
	Attachments     []Attachment   `json:"attachments"`
}

// InlineAttachment is an embedded <img src=".../inline/attachment?token=...">
// found in a ticket description or conversation body. The tokenised URL
// downloads without API auth, like a regular attachment_url.
type InlineAttachment struct {
	// ID is the data-id attribute on the <img> tag (0 when absent).
	ID int64 `json:"id"`
	// Name comes from the alt attribute, falling back to inline_<id>.
	Name string `json:"name"`
	// URL is the full tokenised inline download URL.
	URL string `json:"attachment_url"`
	// ConversationID is 0 for ticket-description images, else the owning
	// conversation ID.
	ConversationID int64  `json:"conversation_id,omitempty"`
	DownloadedPath string `json:"downloaded_path,omitempty"`
}

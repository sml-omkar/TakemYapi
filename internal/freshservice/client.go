// Package freshservice implements a minimal client for the Freshservice v2
// REST API focused on reading tickets and their full conversation history.
package freshservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Defaults mirroring the Freshservice v2 API limits.
const (
	MaxPerPage     = 100
	defaultTimeout = 30 * time.Second
	apiVersion     = "v2"
)

// linkPattern matches a pagination Link header entry like:
// <https://.../api/v2/tickets?page=2>; rel="next"
var linkPattern = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// Client talks to a single Freshservice account.
type Client struct {
	baseURL  string
	apiKey   string
	http     *http.Client
	limiter  *rate.Limiter
	retries  int
	logf     func(format string, args ...any)
	mu       sync.Mutex
	requests int
}

// New creates a Client for the given account domain and API key.
// "domain" may be either a bare account slug (e.g. "acme", resolved to
// https://acme.freshservice.com) or a full custom portal hostname such as
// "servicedesk.example.com", which already serves the v2 API.
func New(domain, apiKey string, opts ...Option) *Client {
	host := domain
	if !strings.Contains(domain, ".") {
		host = domain + ".freshservice.com"
	}
	c := &Client{
		baseURL: fmt.Sprintf("https://%s/api/%s", host, apiVersion),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: defaultTimeout},
		// Freshservice v2 allows 50 requests/minute.
		limiter: rate.NewLimiter(rate.Every(60*time.Second/50), 50),
		retries: 5,
		logf:    func(string, ...any) {},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Option customizes a Client.
type Option func(*Client)

// WithLogger sets a logging function on the client.
func WithLogger(f func(format string, args ...any)) Option {
	return func(c *Client) { c.logf = f }
}

// WithBaseURL overrides the API base URL (useful for tests pointing at a
// local HTTP server).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// WithRetries sets the number of retry attempts for failed requests.
func WithRetries(n int) Option {
	return func(c *Client) {
		if n >= 0 {
			c.retries = n
		}
	}
}

// Requests returns the total number of API requests issued so far.
func (c *Client) Requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

// NotFoundError reports an HTTP 404 from the API. Callers that treat a
// missing resource as an empty result (e.g. requested items on Incident
// tickets) match on this type instead of string-matching the message.
type NotFoundError struct {
	Path string
	Body string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("api error 404 from %s: %s", e.Path, strings.TrimSpace(e.Body))
}

// do performs a request with rate limiting, auth, and retry-on-failure.
func (c *Client) do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			c.logf("retrying request (attempt %d/%d): %s", attempt, c.retries, path)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}

		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(c.apiKey, "X")
		req.Header.Set("Accept", "application/json")

		c.mu.Lock()
		c.requests++
		c.mu.Unlock()

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request to %s: %w", path, err)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("read response from %s: %w", path, readErr)
			continue
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("rate limited (429): %s", string(body))
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("server error %d from %s: %s", resp.StatusCode, path, strings.TrimSpace(string(body)))
		case resp.StatusCode == http.StatusNotFound:
			return nil, &NotFoundError{Path: path, Body: string(body)}
		default:
			return nil, fmt.Errorf("api error %d from %s: %s", resp.StatusCode, path, strings.TrimSpace(string(body)))
		}
	}
	return nil, fmt.Errorf("giving up on %s after %d attempts: %w", path, c.retries, lastErr)
}

func backoff(attempt int) time.Duration {
	return time.Duration(1<<uint(attempt)) * 500 * time.Millisecond
}

// TicketsParams controls which tickets are listed.
type TicketsParams struct {
	UpdatedSince string // RFC3339 timestamp; only tickets updated at/after this are returned.
	Status       string // Numeric status code as a string.
}

// ListTickets returns all tickets matching params, paging through until the
// API reports no further pages.
func (c *Client) ListTickets(ctx context.Context, p TicketsParams) ([]Ticket, error) {
	var all []Ticket
	page := 1

	for {
		params := url.Values{}
		params.Set("page", strconv.Itoa(page))
		params.Set("per_page", strconv.Itoa(MaxPerPage))
		if p.UpdatedSince != "" {
			params.Set("updated_since", p.UpdatedSince)
		}
		if p.Status != "" {
			params.Set("status", p.Status)
		}

		nextURL, body, err := c.doWithHeaders(ctx, http.MethodGet, "/tickets", params)
		if err != nil {
			return nil, err
		}

		var envelope struct {
			Tickets []Ticket `json:"tickets"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, fmt.Errorf("decode tickets page %d: %w", page, err)
		}

		all = append(all, envelope.Tickets...)
		c.logf("page %d: %d tickets (total %d)", page, len(envelope.Tickets), len(all))

		if nextURL == nil || len(envelope.Tickets) == 0 {
			return all, nil
		}

		q := nextURL.Query()
		nextPage, err := strconv.Atoi(q.Get("page"))
		if err != nil || nextPage <= page {
			return nil, fmt.Errorf("invalid next page link on page %d: %q", page, nextURL)
		}
		page = nextPage
	}
}

// doWithHeaders is like do but also parses the Link header for pagination.
func (c *Client) doWithHeaders(ctx context.Context, method, path string, params url.Values) (*url.URL, []byte, error) {
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			c.logf("retrying request (attempt %d/%d): %s", attempt, c.retries, path)
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}

		if err := c.limiter.Wait(ctx); err != nil {
			return nil, nil, err
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
		if err != nil {
			return nil, nil, err
		}
		req.SetBasicAuth(c.apiKey, "X")
		req.Header.Set("Accept", "application/json")

		c.mu.Lock()
		c.requests++
		c.mu.Unlock()

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request to %s: %w", path, err)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("read response from %s: %w", path, readErr)
			continue
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			var nextURL *url.URL
			if link := resp.Header.Get("Link"); link != "" {
				if m := linkPattern.FindStringSubmatch(link); len(m) == 2 {
					if u, err := url.Parse(m[1]); err == nil {
						nextURL = u
					}
				}
			}
			return nextURL, body, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("rate limited (429): %s", string(body))
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("server error %d from %s: %s", resp.StatusCode, path, strings.TrimSpace(string(body)))
		case resp.StatusCode == http.StatusNotFound:
			return nil, nil, &NotFoundError{Path: path, Body: string(body)}
		default:
			return nil, nil, fmt.Errorf("api error %d from %s: %s", resp.StatusCode, path, strings.TrimSpace(string(body)))
		}
	}
	return nil, nil, fmt.Errorf("giving up on %s after %d attempts: %w", path, c.retries, lastErr)
}

// GetTicket fetches a single ticket by ID, including its attachment objects
// with their attachment_url download links.
func (c *Client) GetTicket(ctx context.Context, id int64) (*Ticket, error) {
	path := fmt.Sprintf("/tickets/%d", id)
	q := url.Values{}
	q.Set("include", "ticket_attachments")

	_, body, err := c.doWithHeaders(ctx, http.MethodGet, path, q)
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Ticket *Ticket `json:"ticket"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode ticket %d: %w", id, err)
	}
	if envelope.Ticket == nil {
		return nil, fmt.Errorf("ticket %d not found", id)
	}
	return envelope.Ticket, nil
}

// ListConversations returns the full conversation history for a ticket.
// The Freshservice API returns both replies and notes in this list.
func (c *Client) ListConversations(ctx context.Context, ticketID int64) ([]Conversation, error) {
	body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/tickets/%d/conversations", ticketID), nil)
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Conversations []Conversation `json:"conversations"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode conversations for ticket %d: %w", ticketID, err)
	}
	return envelope.Conversations, nil
}

// ListRequestedItems returns the service-catalog requested items for a
// ticket (GET /tickets/{id}/requested_items). Incidents and plain email
// tickets have none — the API answers those with 404, which is mapped to an
// empty list (not an error). Each item may carry its own Attachments
// (same attachment_url shape as ticket attachments), which the fetcher
// downloads like any other attachment.
func (c *Client) ListRequestedItems(ctx context.Context, ticketID int64) ([]RequestedItem, error) {
	body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/tickets/%d/requested_items", ticketID), nil)
	if err != nil {
		var nf *NotFoundError
		if errors.As(err, &nf) {
			return []RequestedItem{}, nil
		}
		return nil, err
	}

	var envelope struct {
		RequestedItems []RequestedItem `json:"requested_items"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode requested items for ticket %d: %w", ticketID, err)
	}
	if envelope.RequestedItems == nil {
		return []RequestedItem{}, nil
	}
	return envelope.RequestedItems, nil
}

// DownloadAttachment downloads an attachment file from its pre-signed URL.
// The attachment_url is a direct link to the file on Freshservice's CDN and
// requires no auth — the URL itself authorises the download.
func (c *Client) DownloadAttachment(ctx context.Context, attachmentURL string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, attachmentURL, nil)
	if err != nil {
		return fmt.Errorf("build attachment request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("download attachment %s: %w", attachmentURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download attachment %s: HTTP %d: %s", attachmentURL, resp.StatusCode, strings.TrimSpace(string(b)))
	}

	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("write attachment from %s: %w", attachmentURL, err)
	}
	return nil
}

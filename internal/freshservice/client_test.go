package freshservice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"golang.org/x/time/rate"
)

func TestLinkPatternFindsNextPage(t *testing.T) {
	raw := `<https://acme.freshservice.com/api/v2/tickets?per_page=100&page=2>; rel="next", <https://acme.freshservice.com/api/v2/tickets?per_page=100&page=1>; rel="first"`
	m := linkPattern.FindStringSubmatch(raw)
	if len(m) != 2 {
		t.Fatalf("expected a match, got %v", m)
	}
	u, err := url.Parse(m[1])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := u.Query().Get("page"); got != "2" {
		t.Fatalf("expected page=2, got %q", got)
	}
}

func TestLinkPatternNoNext(t *testing.T) {
	raw := `<https://acme.freshservice.com/api/v2/tickets?per_page=100&page=1>; rel="first"`
	if m := linkPattern.FindStringSubmatch(raw); len(m) != 0 {
		t.Fatalf("expected no match, got %v", m)
	}
}

func TestListRequestedItemsNotFoundMeansEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/tickets/12844/requested_items" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &Client{
		baseURL: srv.URL + "/api/v2",
		http:    srv.Client(),
		limiter: rate.NewLimiter(rate.Inf, 1),
		logf:    func(string, ...any) {},
	}
	items, err := c.ListRequestedItems(context.Background(), 12844)
	if err != nil {
		t.Fatalf("expected 404 to map to empty, got error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty items, got %+v", items)
	}
}

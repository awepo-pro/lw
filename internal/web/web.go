// Package web provides the search-provider seam behind the web.search
// tool: the SearchProvider interface and the one built-in implementation,
// Tavily. It stands alone — no logging, config or tools imports (contract
// §2) — and never touches the vault.
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// defaultEndpoint is the production Tavily search URL, used when
// Tavily.Endpoint is empty.
const defaultEndpoint = "https://api.tavily.com/search"

// defaultMaxResults is the hit cap applied when a caller passes max <= 0.
const defaultMaxResults = 5

// SearchHit is one web search result. Snippet carries the provider's
// content field: a short extract of the page, not the page itself.
type SearchHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchProvider queries the web on behalf of the web.search tool. A nil
// provider means the tool is not offered at all (tools registry rule).
type SearchProvider interface {
	Search(ctx context.Context, query string, max int) ([]SearchHit, error)
}

// Tavily is the built-in SearchProvider, backed by api.tavily.com.
type Tavily struct {
	// Endpoint is the search URL; empty means defaultEndpoint.
	Endpoint string
	// APIKey is sent as the Bearer credential on every search.
	APIKey string
	// HTTP makes the request; nil means http.DefaultClient.
	HTTP *http.Client
}

// NewTavily returns a Tavily provider speaking to the production endpoint.
func NewTavily(apiKey string, hc *http.Client) *Tavily {
	return &Tavily{APIKey: apiKey, HTTP: hc}
}

// WithEndpoint points the provider at e and returns it — a test seam, so
// tests can dial an httptest server instead of the network.
func (t *Tavily) WithEndpoint(e string) *Tavily {
	t.Endpoint = e
	return t
}

// tavilyRequest is the JSON body the Tavily search endpoint accepts.
type tavilyRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

// tavilyResponse is the part of Tavily's reply we need; the decoder
// ignores every other field (score, answer, raw_content, …).
type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

// The classified failure kinds (017): a non-200 reply unwraps to one of
// these sentinels — or to nil, for statuses nobody has a plain-spoken
// story for. Callers branch with errors.Is, never by parsing text.
var (
	// ErrAuth is unwrapped from a 401/403: Tavily rejected the credential.
	ErrAuth = errors.New("web: tavily rejected the API key")
	// ErrRateLimited is unwrapped from a 429; RetryAfter carries Retry-After.
	ErrRateLimited = errors.New("web: tavily rate limit reached")
	// ErrQuota is unwrapped from a 432, Tavily's plan-limit-exceeded code.
	ErrQuota = errors.New("web: tavily plan limit reached")
)

// maxDetailRead bounds how much of an error body is read for Detail, and
// maxDetailRunes caps what Detail may hold: the body is untrusted provider
// text, read only a bounded once and never surfaced unbounded.
const (
	maxDetailRead  = 4096
	maxDetailRunes = 200
)

// SearchError is one non-200 reply from the provider, classified.
type SearchError struct {
	Status     int           // the HTTP status
	Detail     string        // bounded reason from the body; classified kinds only
	RetryAfter time.Duration // from Retry-After on 429; 0 when absent/invalid
}

// Error renders the contract's `web: tavily search failed: <status>` bytes,
// plus `: <Detail>` for a classified error that extracted one (017 F-A5).
func (e *SearchError) Error() string {
	msg := "web: tavily search failed: " + strconv.Itoa(e.Status)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Unwrap returns the sentinel for the classified kinds — 401/403 → ErrAuth,
// 429 → ErrRateLimited, 432 → ErrQuota — and nil for every other status,
// so callers classify with errors.Is.
func (e *SearchError) Unwrap() error {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case 432:
		return ErrQuota
	default:
		return nil
	}
}

// detailFromBody extracts Detail from one classified error body: the
// string `detail` field when the trimmed body parses as a JSON object, else
// its first line, capped at maxDetailRunes; an empty body stays empty. A
// short read is fine — Detail is best-effort context, not contract text.
func detailFromBody(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, maxDetailRead))
	s := strings.TrimSpace(string(raw))
	detail := firstLine(s)
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err == nil {
		if d, ok := obj["detail"].(string); ok {
			detail = d
		}
	}
	return capRunes(detail, maxDetailRunes)
}

// firstLine is s up to its first newline, whitespace trimmed.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// capRunes cuts s to at most n runes.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Search runs one Tavily query and returns at most max hits, in the order
// the provider returned them; max <= 0 means defaultMaxResults. Snippet
// is mapped from the provider's content field. Errors carry the `web:`
// prefix (contract §2); a non-200 reply is a *SearchError, classified by
// status (017 F-A1).
func (t *Tavily) Search(ctx context.Context, query string, max int) ([]SearchHit, error) {
	if max <= 0 {
		max = defaultMaxResults
	}
	ep := t.Endpoint
	if ep == "" {
		ep = defaultEndpoint
	}
	body, err := json.Marshal(tavilyRequest{Query: query, MaxResults: max})
	if err != nil {
		return nil, fmt.Errorf("web: tavily request: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("web: tavily request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	hc := t.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web: tavily request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		se := &SearchError{Status: resp.StatusCode}
		if resp.StatusCode == http.StatusTooManyRequests {
			// Retry-After is parsed only on a 429, only as integer seconds
			// (017 F-A4); absent, non-integer or negative stays 0. Tavily
			// sends no reset date, so none is invented.
			if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
				se.RetryAfter = time.Duration(secs) * time.Second
			}
		}
		if se.Unwrap() != nil {
			// Detail is extracted only for the classified kinds: a generic
			// 5xx body — often someone else's HTML error page — is never
			// read into the error at all (017 F-A3).
			se.Detail = detailFromBody(resp.Body)
		}
		return nil, se
	}
	var tr tavilyResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("web: tavily decode: %v", err)
	}
	hits := make([]SearchHit, 0, len(tr.Results))
	for _, r := range tr.Results {
		hits = append(hits, SearchHit{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	if len(hits) > max {
		hits = hits[:max]
	}
	return hits, nil
}

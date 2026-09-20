package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tavily must always satisfy the seam the web.search tool consumes.
var _ SearchProvider = (*Tavily)(nil)

// recordedRequest is what the fake Tavily saw on its last request.
type recordedRequest struct {
	Method        string
	Path          string
	Authorization string
	ContentType   string
	Body          []byte
}

// newFakeTavilyWithHeaders starts an httptest server answering every POST
// with status, response headers and body, and returns a provider dialing it
// via WithEndpoint plus a getter for the recorded request. All traffic
// stays on loopback.
func newFakeTavilyWithHeaders(t *testing.T, status int, respBody string, headers map[string]string) (*Tavily, func() *recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	rec := &recordedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		*rec = recordedRequest{
			Method:        r.Method,
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			ContentType:   r.Header.Get("Content-Type"),
			Body:          b,
		}
		mu.Unlock()
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	prov := NewTavily("test-key", srv.Client()).WithEndpoint(srv.URL + "/search")
	return prov, func() *recordedRequest { mu.Lock(); defer mu.Unlock(); return rec }
}

// newFakeTavily is newFakeTavilyWithHeaders with no response headers — the
// shape the provider suite (and every pre-017 test) needs.
func newFakeTavily(t *testing.T, status int, respBody string) (*Tavily, func() *recordedRequest) {
	t.Helper()
	return newFakeTavilyWithHeaders(t, status, respBody, nil)
}

// TestTavilyProvider is the permanent regression suite for the Tavily
// provider (D-10C): request shape, hit mapping, the max cap and the exact
// error texts of contract §2, all against a fake Tavily server.
func TestTavilyProvider(t *testing.T) {
	t.Run("bad_json_is_error", func(t *testing.T) {
		prov, _ := newFakeTavily(t, http.StatusOK, `this is not json`)
		_, err := prov.Search(context.Background(), "q", 3)
		if err == nil {
			t.Fatal("Search on a garbage body = nil error, want decode error")
		}
		if !strings.HasPrefix(err.Error(), "web: tavily decode: ") {
			t.Fatalf("error = %q, want prefix %q", err.Error(), "web: tavily decode: ")
		}
	})

	t.Run("http_error_is_error", func(t *testing.T) {
		// A Tavily 5xx body is an HTML error page, not JSON: the body is
		// deliberately undecodable so this pins status-checked-BEFORE-decode.
		prov, _ := newFakeTavily(t, http.StatusInternalServerError, `gateway timeout html`)
		_, err := prov.Search(context.Background(), "q", 3)
		if err == nil {
			t.Fatal("Search on a 500 = nil error, want status error")
		}
		if got := err.Error(); got != "web: tavily search failed: 500" {
			t.Fatalf("error = %q, want exactly %q", got, "web: tavily search failed: 500")
		}
	})

	t.Run("maps_hits_in_order", func(t *testing.T) {
		body := `{"results":[` +
			`{"title":"Alpha","url":"https://example.com/a","content":"alpha content","score":0.9},` +
			`{"title":"Beta","url":"https://example.com/b","content":"beta content"}` +
			`],"response_time":0.1}`
		prov, _ := newFakeTavily(t, http.StatusOK, body)
		hits, err := prov.Search(context.Background(), "q", 0)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		want := []SearchHit{
			{Title: "Alpha", URL: "https://example.com/a", Snippet: "alpha content"},
			{Title: "Beta", URL: "https://example.com/b", Snippet: "beta content"},
		}
		if !reflect.DeepEqual(hits, want) {
			t.Fatalf("hits = %+v, want %+v (order and content→Snippet mapping)", hits, want)
		}
	})

	t.Run("max_caps_results", func(t *testing.T) {
		entries := make([]string, 5)
		for i := range entries {
			entries[i] = `{"title":"t` + strconv.Itoa(i) +
				`","url":"https://example.com/` + strconv.Itoa(i) +
				`","content":"c` + strconv.Itoa(i) + `"}`
		}
		prov, _ := newFakeTavily(t, http.StatusOK, `{"results":[`+strings.Join(entries, ",")+`]}`)
		hits, err := prov.Search(context.Background(), "q", 3)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(hits) != 3 {
			t.Fatalf("len(hits) = %d, want 3 (max caps the server's 5)", len(hits))
		}
		for i, h := range hits {
			if h.Title != "t"+strconv.Itoa(i) {
				t.Errorf("hits[%d].Title = %q, want t%d — the first three, in order", i, h.Title, i)
			}
		}
	})

	t.Run("sends_bearer_key_and_query", func(t *testing.T) {
		prov, rec := newFakeTavily(t, http.StatusOK, `{"results":[]}`)
		hits, err := prov.Search(context.Background(), "glm 5.3 release", 7)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		r := rec()
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Path != "/search" {
			t.Errorf("path = %s, want /search (WithEndpoint's endpoint dialed verbatim)", r.Path)
		}
		if r.Authorization != "Bearer test-key" {
			t.Errorf("Authorization = %q, want %q", r.Authorization, "Bearer test-key")
		}
		if r.ContentType != "application/json" {
			t.Errorf("Content-Type = %q, want %q", r.ContentType, "application/json")
		}
		var sent struct {
			Query      string `json:"query"`
			MaxResults int    `json:"max_results"`
		}
		if err := json.Unmarshal(r.Body, &sent); err != nil {
			t.Fatalf("decode request body %s: %v", r.Body, err)
		}
		if sent.Query != "glm 5.3 release" || sent.MaxResults != 7 {
			t.Errorf("body = {query:%q, max_results:%d}, want {query:%q, max_results:7}",
				sent.Query, sent.MaxResults, "glm 5.3 release")
		}
		if len(hits) != 0 {
			t.Errorf("len(hits) = %d, want 0", len(hits))
		}
	})
}

// TestTavilyClassifiedErrors pins the 017 classification (MASTER F-A2/F-A3/
// F-A5): 401/403 → ErrAuth, 429 → ErrRateLimited, 432 (Tavily's
// plan-limit-exceeded code) → ErrQuota, each a *SearchError whose Detail is
// a bounded reason from the body — the string "detail" field of a JSON
// object, else the body's first line, capped at 200 runes — and whose
// Error() appends ": <Detail>" only when one was extracted. Permanent
// regression tests (D-10C).
func TestTavilyClassifiedErrors(t *testing.T) {
	oversized := strings.Repeat("x", 500) // a detail far past the 200-rune cap
	tests := []struct {
		name         string
		status       int
		body         string
		wantSentinel error
		wantStatus   int
		wantDetail   string
		wantErr      string
	}{
		{
			name:         "401_json_detail",
			status:       http.StatusUnauthorized,
			body:         `{"detail":"Invalid API key"}`,
			wantSentinel: ErrAuth,
			wantStatus:   401,
			wantDetail:   "Invalid API key",
			wantErr:      "web: tavily search failed: 401: Invalid API key",
		},
		{
			name:         "403_plain_detail",
			status:       http.StatusForbidden,
			body:         "forbidden\nsecond line never read",
			wantSentinel: ErrAuth,
			wantStatus:   403,
			wantDetail:   "forbidden",
			wantErr:      "web: tavily search failed: 403: forbidden",
		},
		{
			name:         "432_json_detail",
			status:       432,
			body:         `{"detail":"Plan limit exceeded"}`,
			wantSentinel: ErrQuota,
			wantStatus:   432,
			wantDetail:   "Plan limit exceeded",
			wantErr:      "web: tavily search failed: 432: Plan limit exceeded",
		},
		{
			name:         "429_empty_body",
			status:       http.StatusTooManyRequests,
			body:         "",
			wantSentinel: ErrRateLimited,
			wantStatus:   429,
			wantDetail:   "",
			wantErr:      "web: tavily search failed: 429",
		},
		{
			name:         "401_oversized_detail_capped",
			status:       http.StatusUnauthorized,
			body:         `{"detail":"` + oversized + `"}`,
			wantSentinel: ErrAuth,
			wantStatus:   401,
			wantDetail:   oversized[:200],
			wantErr:      "web: tavily search failed: 401: " + oversized[:200],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prov, _ := newFakeTavilyWithHeaders(t, tt.status, tt.body, nil)
			_, err := prov.Search(context.Background(), "q", 3)
			if err == nil {
				t.Fatalf("Search on %d = nil error, want a classified *SearchError", tt.status)
			}
			var se *SearchError
			if !errors.As(err, &se) {
				t.Fatalf("error = %T (%v), want *SearchError", err, err)
			}
			if !errors.Is(err, tt.wantSentinel) {
				t.Fatalf("errors.Is(err, %v) = false (Unwrap = %v)", tt.wantSentinel, se.Unwrap())
			}
			if se.Status != tt.wantStatus {
				t.Errorf("Status = %d, want %d", se.Status, tt.wantStatus)
			}
			if se.Detail != tt.wantDetail {
				t.Errorf("Detail = %d runes %q…, want %d runes %q…",
					len([]rune(se.Detail)), se.Detail, len([]rune(tt.wantDetail)), tt.wantDetail)
			}
			if got := err.Error(); got != tt.wantErr {
				t.Errorf("Error() = %q, want exactly %q", got, tt.wantErr)
			}
		})
	}
}

// TestTavilyRateLimitRetryAfter pins Retry-After parsing (MASTER F-A4):
// read only on a 429, only as integer seconds; absent, non-integer or
// negative stays 0. Permanent regression tests (D-10C).
func TestTavilyRateLimitRetryAfter(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    time.Duration
	}{
		{"integer_seconds", map[string]string{"Retry-After": "30"}, 30 * time.Second},
		{"garbage", map[string]string{"Retry-After": "soon"}, 0},
		{"absent", nil, 0},
		{"negative", map[string]string{"Retry-After": "-5"}, 0},
		{"past_int64_seconds", map[string]string{"Retry-After": "10000000000"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prov, _ := newFakeTavilyWithHeaders(t, http.StatusTooManyRequests, `{"detail":"slow down"}`, tt.headers)
			_, err := prov.Search(context.Background(), "q", 3)
			var se *SearchError
			if !errors.As(err, &se) {
				t.Fatalf("error = %T (%v), want *SearchError", err, err)
			}
			if se.RetryAfter != tt.want {
				t.Errorf("RetryAfter = %v, want %v", se.RetryAfter, tt.want)
			}
		})
	}
}

// TestTavilyGenericErrorUnchanged pins that an unclassified status keeps
// the exact pre-017 bytes (MASTER F-A2) and never leaks body text, even
// when the body is a large HTML error page (MASTER F-A3, correction 3).
// Permanent regression tests (D-10C).
func TestTavilyGenericErrorUnchanged(t *testing.T) {
	html := "<html>" + strings.Repeat("<p>gateway noise</p>", 400) + "</html>" // > 4 KiB
	prov, _ := newFakeTavily(t, http.StatusInternalServerError, html)
	_, err := prov.Search(context.Background(), "q", 3)
	if err == nil {
		t.Fatal("Search on a 500 = nil error, want status error")
	}
	var se *SearchError
	if !errors.As(err, &se) {
		t.Fatalf("error = %T (%v), want *SearchError", err, err)
	}
	if se.Detail != "" {
		t.Errorf("Detail = %q, want empty — a generic error never reads body text into the error", se.Detail)
	}
	if se.Unwrap() != nil {
		t.Errorf("Unwrap() = %v, want nil for an unclassified status", se.Unwrap())
	}
	for _, s := range []error{ErrAuth, ErrRateLimited, ErrQuota} {
		if errors.Is(err, s) {
			t.Errorf("errors.Is(err, %v) = true, want false for a 500", s)
		}
	}
	if got := err.Error(); got != "web: tavily search failed: 500" {
		t.Fatalf("error = %q, want exactly the pre-017 bytes %q", got, "web: tavily search failed: 500")
	}
}

// TestSearchErrorUnwrap pins the sentinel matrix (MASTER F-A1/F-A2) and the
// classified Error() bytes with and without Detail (MASTER F-A5) — built
// directly, no server: 401/403 → ErrAuth, 429 → ErrRateLimited, 432 →
// ErrQuota, anything else → nil; RetryAfter never appears in Error().
// Permanent regression tests (D-10C).
func TestSearchErrorUnwrap(t *testing.T) {
	tests := []struct {
		name    string
		se      *SearchError
		wantIs  error
		wantErr string
	}{
		{"401_unwraps_auth", &SearchError{Status: 401}, ErrAuth, "web: tavily search failed: 401"},
		{"403_unwraps_auth", &SearchError{Status: 403}, ErrAuth, "web: tavily search failed: 403"},
		{"429_unwraps_rate_limited", &SearchError{Status: 429}, ErrRateLimited, "web: tavily search failed: 429"},
		{"432_unwraps_quota", &SearchError{Status: 432}, ErrQuota, "web: tavily search failed: 432"},
		{"500_unwraps_nil", &SearchError{Status: 500}, nil, "web: tavily search failed: 500"},
		{"401_with_detail", &SearchError{Status: 401, Detail: "Invalid API key"}, ErrAuth,
			"web: tavily search failed: 401: Invalid API key"},
		{"429_detail_and_retry_never_in_error", &SearchError{Status: 429, Detail: "slow down", RetryAfter: 30 * time.Second},
			ErrRateLimited, "web: tavily search failed: 429: slow down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range []error{ErrAuth, ErrRateLimited, ErrQuota} {
				if is := errors.Is(tt.se, s); is != (s == tt.wantIs) {
					t.Errorf("errors.Is(%d, %v) = %v, want %v", tt.se.Status, s, is, s == tt.wantIs)
				}
			}
			if tt.wantIs == nil && tt.se.Unwrap() != nil {
				t.Errorf("Unwrap() = %v, want nil for status %d", tt.se.Unwrap(), tt.se.Status)
			}
			if got := tt.se.Error(); got != tt.wantErr {
				t.Errorf("Error() = %q, want exactly %q", got, tt.wantErr)
			}
		})
	}
}

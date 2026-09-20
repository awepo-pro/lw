package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
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

// newFakeTavily starts an httptest server answering every POST with status
// and body, and returns a provider dialing it via WithEndpoint plus a
// getter for the recorded request. All traffic stays on loopback.
func newFakeTavily(t *testing.T, status int, respBody string) (*Tavily, func() *recordedRequest) {
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
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	prov := NewTavily("test-key", srv.Client()).WithEndpoint(srv.URL + "/search")
	return prov, func() *recordedRequest { mu.Lock(); defer mu.Unlock(); return rec }
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

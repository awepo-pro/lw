package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/web"
)

// fakeSearchProvider is the tiny web.SearchProvider TestWebSearchTool runs
// behind the real registry: fixed hits (or a fixed error), with the query
// and max of the last call recorded so the clamp and the pass-through are
// observable. The Tavily provider's own wire behavior is T-B's suite; here
// the provider is a seam.
type fakeSearchProvider struct {
	hits     []web.SearchHit
	err      error
	gotQuery string
	gotMax   int
}

func (f *fakeSearchProvider) Search(_ context.Context, query string, max int) ([]web.SearchHit, error) {
	f.gotQuery, f.gotMax = query, max
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

// webSearchRegistry is engineRegistry's harness plus Deps.Search: a real
// engine over the minimal fixture, real vault and index, with p wired as
// the provider — the same construction the agent loop's deps builder makes
// once a key is configured (010 contract §4).
func webSearchRegistry(t *testing.T, p web.SearchProvider) *Registry {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v := e.Vault()
	return NewRegistry(Deps{
		Vault:  v,
		Index:  index.Build(v),
		Engine: e,
		Author: stage.Author{Kind: "agent", Model: "test"},
		Search: p,
	})
}

// TestWebSearchTool drives the web.search verb through the real registry
// (Registry.Call) the way the agent loop does — 010 contract §3, MASTER
// block T-C. Permanent regression tests (D-10C).
func TestWebSearchTool(t *testing.T) {
	t.Run("not_registered_without_provider", func(t *testing.T) {
		reg := minimalRegistry(t) // Deps.Search nil — the default build's shape
		if _, ok := reg.Get("web.search"); ok {
			t.Fatal("web.search registered without a provider; an unbacked verb is not offered, not offered-and-failing")
		}
		for _, d := range reg.Definitions() {
			if d.Name == "web_search" {
				t.Fatal("Definitions() advertises web_search without a provider")
			}
		}
	})

	t.Run("registered_with_provider", func(t *testing.T) {
		reg := webSearchRegistry(t, &fakeSearchProvider{})
		tool, ok := reg.Get("web.search")
		if !ok {
			t.Fatal("web.search not registered although a provider is wired")
		}
		if !tool.ReadOnly {
			t.Error("web.search ReadOnly = false, want true — it reads the web and writes nothing")
		}
		found := false
		for _, d := range reg.Definitions() {
			if d.Name == "web_search" {
				found = true
			}
		}
		if !found {
			t.Error("Definitions() does not advertise web_search for the wired provider")
		}
	})

	t.Run("happy_path_lists_hits", func(t *testing.T) {
		p := &fakeSearchProvider{hits: []web.SearchHit{
			{Title: "GLM-5.3 announcement", URL: "https://example.com/a", Snippet: "GLM-5.3 ships today"},
			{Title: "Second source", URL: "https://example.com/b", Snippet: ""},
		}}
		reg := webSearchRegistry(t, p)
		res, err := reg.Call(context.Background(), "web.search", json.RawMessage(`{"query":"glm 5.3 release"}`))
		if err != nil {
			t.Fatalf("web.search: %v", err)
		}
		if res.IsError {
			t.Fatalf("web.search failed: %s", res.Content)
		}
		// Contract §3.4, byte for byte — header line, then three lines per
		// hit, the empty snippet still printing its (three-space) line.
		want := "2 results for \"glm 5.3 release\"\n" +
			"1. GLM-5.3 announcement\n" +
			"   https://example.com/a\n" +
			"   GLM-5.3 ships today\n" +
			"2. Second source\n" +
			"   https://example.com/b\n" +
			"   "
		if res.Content != want {
			t.Fatalf("Content =\n%q\nwant\n%q", res.Content, want)
		}
		hits, ok := res.Data.([]web.SearchHit)
		if !ok {
			t.Fatalf("Data = %T, want []web.SearchHit", res.Data)
		}
		if !reflect.DeepEqual(hits, p.hits) {
			t.Errorf("Data hits = %+v, want %+v", hits, p.hits)
		}
	})

	t.Run("max_results_clamped", func(t *testing.T) {
		p := &fakeSearchProvider{}
		reg := webSearchRegistry(t, p)
		res, err := reg.Call(context.Background(), "web.search", json.RawMessage(`{"query":"x","max_results":50}`))
		if err != nil || res.IsError {
			t.Fatalf("web.search: %+v %v", res, err)
		}
		if p.gotMax != 10 {
			t.Errorf("provider called with max = %d, want the clamp 10", p.gotMax)
		}
		// An absent max_results passes 0: the provider's own default (5)
		// is the provider's decision, not the handler's.
		if _, err := reg.Call(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`)); err != nil {
			t.Fatalf("web.search without max_results: %v", err)
		}
		if p.gotMax != 0 {
			t.Errorf("provider called with max = %d for an absent max_results, want 0 (provider default)", p.gotMax)
		}
	})

	t.Run("provider_error_is_error_result", func(t *testing.T) {
		reg := webSearchRegistry(t, &fakeSearchProvider{err: errors.New("boom")})
		res, err := reg.Call(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`))
		if err != nil {
			t.Fatalf("provider failure returned a Go error %v; it must not abort the turn", err)
		}
		if !res.IsError {
			t.Fatalf("provider failure result = %+v, want IsError", res)
		}
		if !strings.HasPrefix(res.Content, "web.search failed: ") {
			t.Errorf("Content = %q, want the \"web.search failed: \" prefix", res.Content)
		}
	})

	t.Run("strict_args_reject_unknown_field", func(t *testing.T) {
		reg := webSearchRegistry(t, &fakeSearchProvider{})
		res, err := reg.Call(context.Background(), "web.search", json.RawMessage(`{"query":"x","nom":1}`))
		if err != nil {
			t.Fatalf("unknown field returned a Go error %v; bad args are an IsError result", err)
		}
		if !res.IsError {
			t.Fatalf("unknown-field result = %+v, want the strict decoder's refusal (C-906)", res)
		}
	})
}

// TestWebSearchErrorMessages pins the plain-language mapping from a
// classified *web.SearchError to the message the model narrates (017
// MASTER F-A6), byte for byte, each still an IsError result with a nil Go
// error; an unclassified SearchError keeps today's passthrough bytes.
// Permanent regression tests (D-10C).
func TestWebSearchErrorMessages(t *testing.T) {
	tests := []struct {
		name string
		err  *web.SearchError
		want string
	}{
		{
			name: "auth_401",
			err:  &web.SearchError{Status: 401},
			want: "web.search failed: Tavily rejected the web API key (401). The user can check it with: lw config get web.api_key",
		},
		{
			name: "rate_limited_429_no_retry_after",
			err:  &web.SearchError{Status: 429},
			want: "web.search failed: Tavily is rate limiting this key (429)",
		},
		{
			name: "rate_limited_429_retry_after",
			err:  &web.SearchError{Status: 429, RetryAfter: 30 * time.Second},
			want: "web.search failed: Tavily is rate limiting this key (429); retry after 30s",
		},
		{
			name: "quota_432",
			err:  &web.SearchError{Status: 432},
			want: "web.search failed: the monthly web-search budget is exhausted (Tavily 432); it resets with the Tavily billing cycle",
		},
		{
			name: "unclassified_503_passthrough",
			err:  &web.SearchError{Status: 503},
			want: "web.search failed: web: tavily search failed: 503",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := webSearchRegistry(t, &fakeSearchProvider{err: tt.err})
			res, err := reg.Call(context.Background(), "web.search", json.RawMessage(`{"query":"x"}`))
			if err != nil {
				t.Fatalf("provider failure returned a Go error %v; it must not abort the turn", err)
			}
			if !res.IsError {
				t.Fatalf("result = %+v, want IsError", res)
			}
			if res.Content != tt.want {
				t.Fatalf("Content =\n%q\nwant\n%q", res.Content, tt.want)
			}
		})
	}
}

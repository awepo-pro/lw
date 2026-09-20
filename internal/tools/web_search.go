package tools

// web_search.go is 010 contract §3's web.search verb: the registry's one
// outward-facing read. It is registered only when a SearchProvider is
// wired (Deps.Search != nil) — not offered at all is how the registry
// treats a capability with no backing service, the same rule that keeps a
// filesystem verb out. The handler never touches the vault and never
// fetches a page: it returns the provider's hits (title, URL, snippet)
// and leaves fetching to stage.ingest_source, which stages the page
// through the normal reviewable raw/ path.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/web"
)

// webMaxResultsCap is the hard ceiling on one web.search call, applied in
// the handler before the provider runs (contract §3: max_results > 10
// clamps to 10). The schema's own "maximum": 10 tells the model the same
// thing; the clamp is what a schema-ignoring caller meets.
const webMaxResultsCap = 10

const webSearchSchema = `{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "minLength": 1,
      "description": "the web search query"
    },
    "max_results": {
      "type": "integer",
      "minimum": 1,
      "maximum": 10,
      "description": "maximum hits to return; defaults to the provider's own default (5)"
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`

type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

// webSearchFailureMessage renders one classified provider failure as the
// plain-language message for its kind (017 F-A6) — status interpolated,
// never the provider's body text. An unclassified SearchError keeps
// today's `web.search failed: web: tavily search failed: <status>`
// passthrough.
func webSearchFailureMessage(se *web.SearchError) string {
	switch {
	case errors.Is(se, web.ErrAuth):
		return fmt.Sprintf("Tavily rejected the web API key (%d). The user can check it with: lw config get web.api_key", se.Status)
	case errors.Is(se, web.ErrRateLimited):
		if se.RetryAfter > 0 {
			return fmt.Sprintf("Tavily is rate limiting this key (%d); retry after %s", se.Status, se.RetryAfter)
		}
		return fmt.Sprintf("Tavily is rate limiting this key (%d)", se.Status)
	case errors.Is(se, web.ErrQuota):
		return fmt.Sprintf("the monthly web-search budget is exhausted (Tavily %d); it resets with the Tavily billing cycle", se.Status)
	default:
		return se.Error()
	}
}

// webSearchFailureKind names the failure class for the log line (017 F-A7);
// "" for an unclassified error, which gets no Warn at all.
func webSearchFailureKind(se *web.SearchError) string {
	switch {
	case errors.Is(se, web.ErrAuth):
		return "auth"
	case errors.Is(se, web.ErrRateLimited):
		return "rate_limited"
	case errors.Is(se, web.ErrQuota):
		return "quota"
	default:
		return ""
	}
}

func webSearchTool(d Deps) Tool {
	return Tool{
		Name:        "web.search",
		Description: "Search the public web when the vault lacks the answer or its facts may have changed. Returns ranked hits, each a title, URL and a short snippet — never a page body. Ingest the best hit with stage.ingest_source to work from the full page.",
		Schema:      json.RawMessage(webSearchSchema),
		ReadOnly:    true,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			var a webSearchArgs
			if err := decodeArgsStrict(args, &a); err != nil {
				return badArgs("web.search", err, `{"query":"glm 5.3 release notes","max_results":5}`), nil
			}
			q := strings.TrimSpace(a.Query)
			if q == "" {
				return Result{IsError: true, Content: `query is required: provide the search query, e.g. {"query":"glm 5.3 release notes"}`}, nil
			}
			max := a.MaxResults
			if max > webMaxResultsCap {
				max = webMaxResultsCap
			}
			start := time.Now()
			hits, err := d.Search.Search(ctx, q, max)
			ms := time.Since(start).Milliseconds()
			n := 0
			if err == nil {
				n = len(hits)
			}
			slog.Info("web search", "query", q, "hits", n, "ms", ms)
			if err != nil {
				// A provider failure is recoverable — the model can retry,
				// reword, or answer from the vault — so it is an IsError
				// result with a nil Go error, never a turn abort. A
				// classified *web.SearchError becomes the plain-language
				// message for its kind (017 F-A6), with a Warn that names
				// only the class — no secrets, no body content (F-A7).
				var se *web.SearchError
				if errors.As(err, &se) {
					if kind := webSearchFailureKind(se); kind != "" {
						slog.Warn("web search failed", "status", se.Status, "kind", kind, "ms", ms)
					}
					return Result{IsError: true, Content: "web.search failed: " + webSearchFailureMessage(se)}, nil
				}
				return Result{IsError: true, Content: "web.search failed: " + err.Error()}, nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d results for %q", n, q)
			for i, h := range hits {
				// The empty snippet still prints its — empty — line, so hit
				// blocks stay aligned at three lines each (contract §3.4).
				fmt.Fprintf(&b, "\n%d. %s\n   %s\n   %s", i+1, h.Title, h.URL, h.Snippet)
			}
			return Result{Content: b.String(), Data: hits}, nil
		},
	}
}

package main

// ingest_partial_loop_test.go runs 061's keep-the-ops contract through the
// REAL stack: cmdIngest, the real newIngestAgent (agent.Loop over llm.Client),
// and an httptest server standing in for the provider. The fake-agent tests in
// ingest_partial_test.go hand-build the error a failed turn returns; this is
// the proof that the error the real loop and client actually produce is the
// one providerFailure recognises — a wrap that dropped the chain (%v for %w)
// would leave every fake-agent test green and the feature dead.
//
// Round 1 stages the raw source and a page: the server reads the scratch path
// out of the real request's user message, as a model would, and names the
// tools the way the wire does (tools.WireName). Round 2 is the
// provider failing, in the two shapes that need no wait to give up on.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
)

// sseToolCalls renders one SSE response whose single delta carries the given
// complete tool calls, then finish_reason tool_calls and [DONE].
func sseToolCalls(t *testing.T, calls []map[string]any) string {
	t.Helper()
	var deltas []map[string]any
	for i, c := range calls {
		args, err := json.Marshal(c["args"])
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		deltas = append(deltas, map[string]any{
			"index": i, "id": fmt.Sprintf("call_%d", i+1), "type": "function",
			"function": map[string]any{"name": c["name"], "arguments": string(args)},
		})
	}
	first, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": deltas}}}})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	return fmt.Sprintf("data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", first,
		`{"choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}]}`)
}

// lastUserMessage is the content of the last user-role message of a request.
func lastUserMessage(t *testing.T, body []byte) string {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return req.Messages[i].Content
		}
	}
	t.Fatalf("request has no user message: %s", body)
	return ""
}

func TestIngestProviderFailureThroughRealLoop(t *testing.T) {
	cases := []struct {
		name    string
		round2  func(w http.ResponseWriter)
		wantErr string // the provider's error text inside the turn's error
		cause   string // the partial note's cause
	}{
		{
			name: "http_429_with_a_long_retry_after",
			round2: func(w http.ResponseWriter) {
				w.Header().Set("Retry-After", "3600")
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, `{"error":{"message":"daily limit","code":429}}`)
			},
			wantErr: `llm: unexpected status 429: {"error":{"message":"daily limit","code":429}}`,
			cause:   "provider 429",
		},
		{
			name: "in_stream_error_event",
			round2: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, `data: {"error":{"code":422,"message":"Provider returned error","metadata":{"provider_name":"Nvidia"}}}`+"\n\n")
			},
			wantErr: "llm: provider error in stream (422): Provider returned error",
			cause:   "provider 422",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				requests++
				n := requests
				mu.Unlock()
				if n >= 2 {
					c.round2(w)
					return
				}
				paths := parseIngestPaths(lastUserMessage(t, body))
				if len(paths) != 1 {
					t.Errorf("the ingest message named %d scratch path(s), want 1", len(paths))
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sseToolCalls(t, []map[string]any{
					{"name": tools.WireName("stage.ingest_source"), "args": map[string]any{"uri": paths[0]}},
					{"name": tools.WireName("stage.create_page"), "args": map[string]any{
						"path": "wiki/concepts/real-loop-partial.md", "title": "Real loop partial", "type": "concept",
						"tags": []string{"inference", "decoding"}, "sources": []string{"raw/articles/kv-cache-explained.md"},
						"confidence": "high", "contested": false,
						"body":      "A page staged before the provider failed, linking [[kv-cache]] and [[flash-attention]].\n\nProvenance: ^[raw/articles/kv-cache-explained.md]\n",
						"rationale": "round 1 of a turn the provider fails in round 2",
					}},
				}))
			}))
			defer srv.Close()

			dir := filepath.Join(t.TempDir(), "xdg-config")
			t.Setenv("XDG_CONFIG_HOME", dir)
			writeConfigFile(t, dir, fmt.Sprintf("[llm]\nbase_url = %q\napi_key = \"test-key\"\nmax_tokens = 8192\n", srv.URL))
			root := testutil.CopyFixture(t, "minimal")
			src := shortSource(t)

			_, err := ingestOnce(t, root, src)
			if err == nil {
				t.Fatal("cmdIngest returned nil for a failed turn, want exit 1")
			}

			mu.Lock()
			got := requests
			mu.Unlock()
			if got != 2 {
				t.Errorf("the provider saw %d request(s), want 2 — a refusal that is not worth waiting out is not retried", got)
			}
			cs := currentOf(t, root)
			if n := len(cs.Live()); n != 2 {
				t.Fatalf("live ops = %d, want the 2 staged in round 1; error: %v", n, err)
			}
			wantSuffix := " [partial: the agent turn failed (" + c.cause + ") after staging 2 op(s)]"
			if !strings.HasSuffix(cs.Intent, wantSuffix) {
				t.Errorf("Intent = %q, want it to end with %q", cs.Intent, wantSuffix)
			}
			if countChangesets(t, root, "rejected") != 0 {
				t.Errorf("a changeset was rejected; the failed turn's work must be kept")
			}
			for _, part := range []string{
				"agent turn: agent: stream: " + c.wantErr,
				"; kept the 2 op(s) this ingest staged in changeset " + cs.ID + " (partial: the turn did not finish) — review them in lw tui",
			} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q does not contain %q", err, part)
				}
			}
		})
	}
}

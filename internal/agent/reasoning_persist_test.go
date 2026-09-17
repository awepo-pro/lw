package agent

// reasoning_persist_test.go is 005's persistence half of contract §3: the
// checked-in pre-005 fixture round-trips byte-identically (D-5E — no
// migration exists or is needed), and a resolved provider key — in the
// process environment and in the llm.Config a turn runs under — never
// reaches a session.ndjson Record (note 4). The key test drives a REAL
// *llm.Client over a loopback httptest SSE server, so the key is present
// on the wire's Authorization header the way production would put it, and
// the assertion is that the loop still writes it nowhere.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
)

// fixtureBytes reads the checked-in pre-005 transcript.
func fixtureBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "pre005-session.ndjson"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

func TestPre005FixtureRoundTrip(t *testing.T) {
	t.Run("records_decode_unchanged", func(t *testing.T) {
		raw := fixtureBytes(t)
		if bytes.Contains(raw, []byte("reasoning")) {
			t.Fatalf("the pre-005 fixture carries a %q key — it must be exactly what the v1 code wrote", "reasoning")
		}

		recs, err := decodeRecords(raw)
		if err != nil {
			t.Fatalf("decodeRecords: %v", err)
		}

		ts := time.Date(2026, 9, 16, 9, 15, 4, 0, time.UTC)
		want := []Record{
			{TS: ts, Role: "user", Content: "Rewrite the intro of kv-cache.md so it mentions the new invalidation section."},
			{TS: ts.Add(5 * time.Second), Role: "assistant", Content: "I'll stage the rewrite first, then re-read the current intro for comparison."},
			{
				TS:     ts.Add(9 * time.Second),
				Role:   "tool",
				Tool:   "stage.create_page",
				Args:   `{"path":"wiki/kv-cache.md","title":"KV cache","type":"concept","confidence":"low","contested":false,"body":"# KV cache\n\nA KV cache keeps reuse-friendly data close to the compute that needs it. This rewrite adds the invalidation section.","rationale":"mention the new invalidation section"}`,
				Result: "proposed op op-3f9c2a",
				Staged: true,
			},
			{
				TS:     ts.Add(11 * time.Second),
				Role:   "tool",
				Tool:   "wiki.get",
				Args:   `{"page":"kv-cache.md"}`,
				Result: "# KV cache\n\nA KV cache keeps reuse-friendly data close to the compute that needs it.",
			},
			{TS: ts.Add(18 * time.Second), Role: "assistant", Content: "Staged the rewrite as op-3f9c2a; the current intro is quoted above for the diff review."},
		}

		if len(recs) != len(want) {
			t.Fatalf("fixture decodes to %d records, want %d", len(recs), len(want))
		}
		for i := range want {
			if !reflect.DeepEqual(recs[i], want[i]) {
				t.Errorf("record %d = %+v, want %+v", i, recs[i], want[i])
			}
			if recs[i].Reasoning != "" {
				t.Errorf("record %d decoded with Reasoning %q, want empty — a pre-005 file has none", i, recs[i].Reasoning)
			}
		}
	})

	t.Run("reencode_is_byte_identical", func(t *testing.T) {
		raw := fixtureBytes(t)
		if !bytes.HasSuffix(raw, []byte("\n")) {
			t.Fatalf("fixture does not end in a newline; Append always writes one")
		}

		// Re-marshal every decoded record — exactly what Append does on a
		// replayed session — and the joined lines must reproduce the file
		// byte for byte. omitempty on Reasoning is what makes this hold:
		// add a non-omitempty field or reorder the struct and this fails.
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		re := make([]string, 0, len(lines))
		for i, line := range lines {
			var r Record
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				t.Fatalf("line %d: decode: %v", i+1, err)
			}
			enc, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("line %d: re-encode: %v", i+1, err)
			}
			re = append(re, string(enc))
		}
		got := strings.Join(re, "\n") + "\n"
		if got != string(raw) {
			t.Fatalf("re-encoding the decoded fixture changed the bytes:\n got  %q\n want %q", got, string(raw))
		}
	})
}

// sseLine renders one SSE data line the way an OpenAI-compatible provider
// frames a chat-completions chunk — the same shapes internal/llm's own
// stream tests replay.
func sseLine(delta string, finish string) []byte {
	if finish == "" {
		finish = "null"
	} else {
		finish = `"` + finish + `"`
	}
	return []byte(`data: {"choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}` + "\n\n")
}

func TestNoAPIKeyInSession(t *testing.T) {
	t.Run("key_never_appears_in_ndjson", func(t *testing.T) {
		// A literal test key — never a real one, never read from the
		// user's config. It sits in the process environment under the
		// names a provider integration would plausibly consult, and in
		// the llm.Config the turn runs under, where it lands on the wire
		// as the Authorization header (internal/llm client.go).
		const testKey = "sk-lw-test-do-not-use-0000"
		t.Setenv("LW_TEST_API_KEY", testKey)
		t.Setenv("OPENAI_API_KEY", testKey)
		t.Setenv("DEEPSEEK_API_KEY", testKey)

		round1 := bytes.Join([][]byte{
			sseLine(`{"reasoning_content":"Let me think it through. "}`, ""),
			sseLine(`{"reasoning_content":"Close the changeset."}`, ""),
			sseLine(`{"content":"Closing the changeset now."}`, ""),
			sseLine(`{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"stage.close","arguments":"{}"}}]}`, ""),
			sseLine(`{}`, "tool_calls"),
			[]byte("data: [DONE]\n\n"),
		}, nil)
		round2 := bytes.Join([][]byte{
			sseLine(`{"content":"Closed."}`, ""),
			sseLine(`{}`, "stop"),
			[]byte("data: [DONE]\n\n"),
		}, nil)

		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			n := calls.Add(1)
			if n == 1 {
				_, _ = w.Write(round1)
				return
			}
			if n == 2 {
				_, _ = w.Write(round2)
				return
			}
			t.Errorf("unexpected Stream call #%d", n)
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}))
		defer srv.Close()

		// The real client, config carrying the key — not the fake seam —
		// so the turn runs under the exact conditions production runs
		// under, minus the network.
		client := llm.New(llm.Config{BaseURL: srv.URL, APIKey: testKey})
		fx := newTestLoopFixture(t)
		l := newLoop(client, fx.reg, fx.store, fx.engine, LoopConfig{})

		out := make(chan Event, 64)
		if err := l.Send(context.Background(), fx.csID, "one full turn under a keyed config", out); err != nil {
			t.Fatalf("Send: %v", err)
		}
		drain(out)

		if got := calls.Load(); got != 2 {
			t.Fatalf("the loop made %d Stream calls, want 2 (a real two-round turn must have run)", got)
		}

		raw, err := os.ReadFile(filepath.Join(fx.engine.Vault().Root(), ".llmwiki", "changesets", "open", fx.csID, "session.ndjson"))
		if err != nil {
			t.Fatalf("read session.ndjson: %v", err)
		}

		// The assertion must not pass vacuously: the turn really recorded
		// reasoning and a tool call before we claim the key is absent.
		for _, mustContain := range []string{"Closing the changeset now.", "Let me think it through.", `"role":"tool"`} {
			if !bytes.Contains(raw, []byte(mustContain)) {
				t.Fatalf("session.ndjson is missing %q — the turn did not record what this test guards: %s", mustContain, raw)
			}
		}
		if bytes.Contains(raw, []byte(testKey)) {
			t.Fatalf("the provider key leaked into session.ndjson (contract §3 note 4):\n%s", raw)
		}
	})
}

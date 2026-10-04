package agent

// history_wire_test.go pins 046: a tool record carried in session history
// reaches the provider as a spec-valid assistant tool_calls + tool result
// pair, never as a bare role:"tool" message with no tool_call_id. The live
// failure that motivated it: DeepSeek answered every turn that resumed a
// changeset with `422 … messages[6]: missing field tool_call_id`, because
// Build replayed history as {"role":"tool","content":"raw.list({…}) -> …"}.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
)

// histNameRE mirrors the pattern every OpenAI-compatible endpoint enforces on
// function names (internal/tools/names.go), so a history pair whose name
// would be rejected fails here and not on the provider.
var histNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// validChat reports the first way msgs breaks the OpenAI chat-completions
// tool-calling contract, or nil. It is the regression net 046 hangs on: a
// strict provider validates exactly this, and z.ai's tolerance is what hid
// the bug for as long as z.ai was the only provider in use.
//
//   - every tool message has a non-empty tool_call_id and non-empty content;
//   - that id is in the tool_calls of the nearest preceding assistant message
//     that has any, with only tool messages in between;
//   - every announced id is answered exactly once, and announced once;
//   - every tool call has type "function", a wire-legal name and an
//     arguments string that is a JSON object.
func validChat(msgs []llm.Message) error {
	pending := map[string]bool{} // ids of the nearest assistant's tool_calls not yet answered
	announced := map[string]bool{}
	for i, m := range msgs {
		if m.Role == "tool" {
			if m.ToolCallID == "" {
				return fmt.Errorf("messages[%d]: tool message has no tool_call_id", i)
			}
			if m.Content == "" {
				return fmt.Errorf("messages[%d]: tool message %q has empty content", i, m.ToolCallID)
			}
			if !pending[m.ToolCallID] {
				return fmt.Errorf("messages[%d]: tool message answers %q, which is not an unanswered id of the nearest preceding assistant tool_calls", i, m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
			continue
		}
		if len(pending) > 0 {
			return fmt.Errorf("messages[%d] (%s) arrives while tool_calls %v are still unanswered", i, m.Role, sortedKeys(pending))
		}
		if len(m.ToolCalls) > 0 && m.Role != "assistant" {
			return fmt.Errorf("messages[%d]: role %q carries tool_calls", i, m.Role)
		}
		for _, tc := range m.ToolCalls {
			switch {
			case tc.ID == "":
				return fmt.Errorf("messages[%d]: tool call with empty id", i)
			case announced[tc.ID]:
				return fmt.Errorf("messages[%d]: tool call id %q announced twice", i, tc.ID)
			case tc.Type != "function":
				return fmt.Errorf("messages[%d]: tool call %q has type %q, want function", i, tc.ID, tc.Type)
			case !histNameRE.MatchString(tc.Function.Name):
				return fmt.Errorf("messages[%d]: tool call %q has wire-illegal name %q", i, tc.ID, tc.Function.Name)
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &obj); err != nil || obj == nil {
				return fmt.Errorf("messages[%d]: tool call %q arguments %q are not a JSON object", i, tc.ID, tc.Function.Arguments)
			}
			announced[tc.ID] = true
			pending[tc.ID] = true
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("tool_calls %v never answered", sortedKeys(pending))
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Two or three keys at most; an insertion sort keeps this file free of a
	// sort import for a diagnostic string.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// wireCall is the assistant half of one history pair, as 046 specifies it:
// no content, one tool call.
func wireCall(id, name, args string) llm.Message {
	tc := llm.ToolCall{ID: id, Type: "function"}
	tc.Function.Name = name
	tc.Function.Arguments = args
	return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{tc}}
}

// wireResult is the tool half of one history pair.
func wireResult(id, content string) llm.Message {
	return llm.Message{Role: "tool", ToolCallID: id, Content: content}
}

// toolRec builds a tool record the way loop.go writes one: role tool, the
// canonical dotted name, the call's arguments and its result.
func toolRec(ts time.Time, tool, args, result string) Record {
	return Record{TS: ts, Role: "tool", Tool: tool, Args: args, Result: result}
}

// buildHistory runs Build over recs against a fresh fixture vault and returns
// the whole message list plus its history slice (everything between the three
// system messages and the closing user message). budget is the builder's
// token budget; the fixture preamble alone exceeds a few dozen tokens, so a
// small budget leaves Compact nothing to spend.
func buildHistory(t *testing.T, budget int, recs []Record, userMsg string) (all, history []llm.Message) {
	t.Helper()
	v, _ := newTestVault(t)
	reg := tools.NewRegistry(tools.Deps{Vault: v})
	b := NewContextBuilder(v, reg, budget)
	s := &Session{ID: "cs-histwire", ChangesetID: "cs-histwire", Records: recs}

	msgs, err := b.Build(s, userMsg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(msgs) < 4 {
		t.Fatalf("Build returned %d messages, want at least the 3 system messages and the user message", len(msgs))
	}
	for i := 0; i < 3; i++ {
		if msgs[i].Role != "system" {
			t.Fatalf("msgs[%d].Role = %q, want system", i, msgs[i].Role)
		}
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Content != userMsg {
		t.Fatalf("last message = %+v, want the user message %q", last, userMsg)
	}
	return msgs, msgs[3 : len(msgs)-1]
}

func requireHistory(t *testing.T, got, want []llm.Message) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	g, _ := json.MarshalIndent(got, "", " ")
	w, _ := json.MarshalIndent(want, "", " ")
	t.Fatalf("history messages differ\n got: %s\nwant: %s", g, w)
}

var histTS = time.Date(2026, 10, 5, 0, 30, 0, 0, time.UTC)

func TestBuildToolRecordsArePairs(t *testing.T) {
	ts := histTS
	recs := []Record{
		rec(ts, "user", "Q1"),
		toolRec(ts.Add(1*time.Second), "raw.list", `{"query":"x"}`, "none"),
		toolRec(ts.Add(2*time.Second), "wiki.get", `{"page":"kv-cache"}`, "BODY"),
		rec(ts.Add(3*time.Second), "assistant", "A1"),
	}
	all, got := buildHistory(t, 100_000, recs, "Q2")

	want := []llm.Message{
		{Role: "user", Content: "Q1"},
		wireCall("hist_1", "raw_list", `{"query":"x"}`),
		wireResult("hist_1", "none"),
		wireCall("hist_2", "wiki_get", `{"page":"kv-cache"}`),
		wireResult("hist_2", "BODY"),
		{Role: "assistant", Content: "A1"},
	}
	requireHistory(t, got, want)

	// The bytes on the wire, not just the struct: the assistant half carries
	// no content key at all, and the tool half carries tool_call_id.
	const wantCall = `{"role":"assistant","tool_calls":[{"id":"hist_1","type":"function","function":{"name":"raw_list","arguments":"{\"query\":\"x\"}"}}]}`
	const wantRes = `{"role":"tool","content":"none","tool_call_id":"hist_1"}`
	if b, _ := json.Marshal(got[1]); string(b) != wantCall {
		t.Errorf("assistant half on the wire:\n got %s\nwant %s", b, wantCall)
	}
	if b, _ := json.Marshal(got[2]); string(b) != wantRes {
		t.Errorf("tool half on the wire:\n got %s\nwant %s", b, wantRes)
	}

	if err := validChat(all); err != nil {
		t.Errorf("Build output is not a valid chat: %v", err)
	}
}

func TestBuildSplitToolRecordMerges(t *testing.T) {
	ts := histTS
	const result = `[{"path":"wiki/concepts/kv-cache.md"}]`

	t.Run("merges_into_one_pair", func(t *testing.T) {
		recs := []Record{
			{TS: ts, Role: "assistant", Tool: "wiki.search", Args: `{"q":"kv"}`},
			{TS: ts.Add(time.Second), Role: "tool", Tool: "wiki.search", Result: result},
		}
		all, got := buildHistory(t, 100_000, recs, "next")
		requireHistory(t, got, []llm.Message{
			wireCall("hist_1", "wiki_search", `{"q":"kv"}`),
			wireResult("hist_1", result),
		})
		if err := validChat(all); err != nil {
			t.Errorf("Build output is not a valid chat: %v", err)
		}
	})

	// Each of these looks a little like the split shape and must NOT merge:
	// folding two real calls into one would invent a result for the first
	// and hide the second.
	notMerged := []struct {
		name string
		recs []Record
		want []llm.Message
	}{
		{
			name: "second record has its own args",
			recs: []Record{
				{TS: ts, Role: "assistant", Tool: "wiki.search", Args: `{"q":"a"}`},
				{TS: ts.Add(time.Second), Role: "tool", Tool: "wiki.search", Args: `{"q":"b"}`, Result: "RB"},
			},
			want: []llm.Message{
				wireCall("hist_1", "wiki_search", `{"q":"a"}`), wireResult("hist_1", "(no result recorded)"),
				wireCall("hist_2", "wiki_search", `{"q":"b"}`), wireResult("hist_2", "RB"),
			},
		},
		{
			name: "different tool",
			recs: []Record{
				{TS: ts, Role: "assistant", Tool: "wiki.search", Args: `{"q":"a"}`},
				{TS: ts.Add(time.Second), Role: "tool", Tool: "wiki.get", Result: "RB"},
			},
			want: []llm.Message{
				wireCall("hist_1", "wiki_search", `{"q":"a"}`), wireResult("hist_1", "(no result recorded)"),
				wireCall("hist_2", "wiki_get", `{}`), wireResult("hist_2", "RB"),
			},
		},
		{
			name: "first record already has a result",
			recs: []Record{
				{TS: ts, Role: "assistant", Tool: "wiki.search", Args: `{"q":"a"}`, Result: "RA"},
				{TS: ts.Add(time.Second), Role: "tool", Tool: "wiki.search", Result: "RB"},
			},
			want: []llm.Message{
				wireCall("hist_1", "wiki_search", `{"q":"a"}`), wireResult("hist_1", "RA"),
				wireCall("hist_2", "wiki_search", `{}`), wireResult("hist_2", "RB"),
			},
		},
		{
			name: "first record is a tool-role record",
			recs: []Record{
				{TS: ts, Role: "tool", Tool: "wiki.search", Args: `{"q":"a"}`},
				{TS: ts.Add(time.Second), Role: "tool", Tool: "wiki.search", Result: "RB"},
			},
			want: []llm.Message{
				wireCall("hist_1", "wiki_search", `{"q":"a"}`), wireResult("hist_1", "(no result recorded)"),
				wireCall("hist_2", "wiki_search", `{}`), wireResult("hist_2", "RB"),
			},
		},
	}
	for _, c := range notMerged {
		t.Run("not_merged/"+c.name, func(t *testing.T) {
			all, got := buildHistory(t, 100_000, c.recs, "next")
			requireHistory(t, got, c.want)
			if err := validChat(all); err != nil {
				t.Errorf("Build output is not a valid chat: %v", err)
			}
		})
	}

	t.Run("merge_consumes_the_second_record_only", func(t *testing.T) {
		// A merged pair uses one id; the record after it starts the next.
		recs := []Record{
			{TS: ts, Role: "assistant", Tool: "wiki.search", Args: `{"q":"kv"}`},
			{TS: ts.Add(time.Second), Role: "tool", Tool: "wiki.search", Result: "R1"},
			toolRec(ts.Add(2*time.Second), "wiki.get", `{"page":"p"}`, "R2"),
		}
		_, got := buildHistory(t, 100_000, recs, "next")
		requireHistory(t, got, []llm.Message{
			wireCall("hist_1", "wiki_search", `{"q":"kv"}`), wireResult("hist_1", "R1"),
			wireCall("hist_2", "wiki_get", `{"page":"p"}`), wireResult("hist_2", "R2"),
		})
	})
}

func TestBuildToolRecordEdgeCases(t *testing.T) {
	ts := histTS
	cases := []struct {
		name string
		recs []Record
		want []llm.Message
	}{
		{
			name: "empty result",
			recs: []Record{toolRec(ts, "wiki.get", `{"page":"p"}`, "")},
			want: []llm.Message{wireCall("hist_1", "wiki_get", `{"page":"p"}`), wireResult("hist_1", "(no result recorded)")},
		},
		{
			name: "empty args",
			recs: []Record{toolRec(ts, "vault.orient", "", "digest")},
			want: []llm.Message{wireCall("hist_1", "vault_orient", `{}`), wireResult("hist_1", "digest")},
		},
		{
			// A-046-3: blank args are as empty as "" — {"raw":"  "} would be
			// valid JSON but says nothing.
			name: "whitespace-only args",
			recs: []Record{
				toolRec(ts, "vault.orient", "  ", "d1"),
				toolRec(ts.Add(time.Second), "vault.orient", "\n\t ", "d2"),
			},
			want: []llm.Message{
				wireCall("hist_1", "vault_orient", `{}`), wireResult("hist_1", "d1"),
				wireCall("hist_2", "vault_orient", `{}`), wireResult("hist_2", "d2"),
			},
		},
		{
			name: "args that are not JSON",
			recs: []Record{toolRec(ts, "stage.create_page", "op-1", "ok")},
			want: []llm.Message{wireCall("hist_1", "stage_create_page", `{"raw":"op-1"}`), wireResult("hist_1", "ok")},
		},
		{
			name: "args that are JSON but not an object",
			recs: []Record{
				toolRec(ts, "wiki.get", `["a","b"]`, "r1"),
				toolRec(ts.Add(time.Second), "wiki.get", `null`, "r2"),
				toolRec(ts.Add(2*time.Second), "wiki.get", `"page"`, "r3"),
				toolRec(ts.Add(3*time.Second), "wiki.get", `42`, "r4"),
			},
			want: []llm.Message{
				wireCall("hist_1", "wiki_get", `{"raw":"[\"a\",\"b\"]"}`), wireResult("hist_1", "r1"),
				wireCall("hist_2", "wiki_get", `{"raw":"null"}`), wireResult("hist_2", "r2"),
				wireCall("hist_3", "wiki_get", `{"raw":"\"page\""}`), wireResult("hist_3", "r3"),
				wireCall("hist_4", "wiki_get", `{"raw":"42"}`), wireResult("hist_4", "r4"),
			},
		},
		{
			name: "truncated JSON object",
			recs: []Record{toolRec(ts, "wiki.get", `{"page":"p`, "r")},
			want: []llm.Message{wireCall("hist_1", "wiki_get", `{"raw":"{\"page\":\"p"}`), wireResult("hist_1", "r")},
		},
		{
			name: "valid object args pass through byte for byte",
			recs: []Record{toolRec(ts, "wiki.get", `{ "page" : "p" }`, "r")},
			want: []llm.Message{wireCall("hist_1", "wiki_get", `{ "page" : "p" }`), wireResult("hist_1", "r")},
		},
		{
			name: "assistant tool record with no following result",
			recs: []Record{{TS: ts, Role: "assistant", Tool: "wiki.search", Args: `{"q":"kv"}`}},
			want: []llm.Message{wireCall("hist_1", "wiki_search", `{"q":"kv"}`), wireResult("hist_1", "(no result recorded)")},
		},
		{
			name: "assistant tool record followed by prose",
			recs: []Record{
				{TS: ts, Role: "assistant", Tool: "wiki.search", Args: `{"q":"kv"}`},
				rec(ts.Add(time.Second), "user", "hello"),
			},
			want: []llm.Message{
				wireCall("hist_1", "wiki_search", `{"q":"kv"}`), wireResult("hist_1", "(no result recorded)"),
				{Role: "user", Content: "hello"},
			},
		},
		{
			// Content on a tool record never reached the old text shape
			// either; the pair carries none, as 046 specifies.
			name: "stray content on a tool record is not sent",
			recs: []Record{{TS: ts, Role: "assistant", Tool: "wiki.get", Args: `{"page":"p"}`, Result: "R", Content: "stray prose"}},
			want: []llm.Message{wireCall("hist_1", "wiki_get", `{"page":"p"}`), wireResult("hist_1", "R")},
		},
		{
			name: "tool name with no wire table entry",
			recs: []Record{toolRec(ts, "foo.bar", `{}`, "r")},
			want: []llm.Message{wireCall("hist_1", "foo_bar", `{}`), wireResult("hist_1", "r")},
		},
		{
			name: "result that looks like the old text shape is sent as is",
			recs: []Record{toolRec(ts, "wiki.get", `{"page":"p"}`, "wiki.get({}) -> x")},
			want: []llm.Message{wireCall("hist_1", "wiki_get", `{"page":"p"}`), wireResult("hist_1", "wiki.get({}) -> x")},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			all, got := buildHistory(t, 100_000, c.recs, "next")
			requireHistory(t, got, c.want)
			if err := validChat(all); err != nil {
				t.Errorf("Build output is not a valid chat: %v", err)
			}
		})
	}
}

// TestBuildToolRecordSkippedGuardUnchanged keeps D-5G honest next to the new
// pair path: a record with neither tool nor content is still dropped, and a
// tool record with every other field empty is NOT (its Tool is non-empty),
// so a staged op's audit trail still reaches the wire.
func TestBuildToolRecordSkippedGuardUnchanged(t *testing.T) {
	ts := histTS
	recs := []Record{
		{TS: ts, Role: "assistant", Reasoning: "thought, wrote nothing"},
		{TS: ts.Add(time.Second), Role: "tool", Tool: "stage.close"},
	}
	_, got := buildHistory(t, 100_000, recs, "next")
	requireHistory(t, got, []llm.Message{
		wireCall("hist_1", "stage_close", `{}`),
		wireResult("hist_1", "(no result recorded)"),
	})
}

// mixedHistory is 40 records in 8 cycles of 5: two prose turns Compact may
// collapse, one read or staged op, and one split-shape call. Every third
// cycle's read is a staged stage.create_page, which Compact must keep.
func mixedHistory(ts time.Time) []Record {
	long := strings.Repeat("prose that a small budget collapses into a placeholder. ", 6)
	var recs []Record
	at := func() time.Time { return ts.Add(time.Duration(len(recs)) * time.Second) }
	for c := 0; c < 8; c++ {
		recs = append(recs, rec(at(), "user", fmt.Sprintf("Q%d %s", c, long)))
		recs = append(recs, rec(at(), "assistant", fmt.Sprintf("A%d %s", c, long)))
		if c%3 == 2 {
			recs = append(recs, Record{TS: at(), Role: "tool", Tool: "stage.create_page", Args: fmt.Sprintf("op-%d", c), Result: "proposed", Staged: true})
		} else {
			recs = append(recs, toolRec(at(), "wiki.get", fmt.Sprintf(`{"page":"p%d"}`, c), fmt.Sprintf("BODY%d", c)))
		}
		recs = append(recs, Record{TS: at(), Role: "assistant", Tool: "wiki.search", Args: fmt.Sprintf(`{"q":"s%d"}`, c)})
		recs = append(recs, Record{TS: at(), Role: "tool", Tool: "wiki.search", Result: fmt.Sprintf("[hit%d]", c)})
	}
	return recs
}

func countCalls(msgs []llm.Message, wireName string) int {
	n := 0
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == wireName {
				n++
			}
		}
	}
	return n
}

func TestBuildWireValid(t *testing.T) {
	ts := histTS

	t.Run("cases_above", func(t *testing.T) {
		sets := map[string][]Record{
			"pairs": {
				rec(ts, "user", "Q1"),
				toolRec(ts.Add(time.Second), "raw.list", `{"query":"x"}`, "none"),
				toolRec(ts.Add(2*time.Second), "wiki.get", `{"page":"kv-cache"}`, "BODY"),
				rec(ts.Add(3*time.Second), "assistant", "A1"),
			},
			"split": {
				{TS: ts, Role: "assistant", Tool: "wiki.search", Args: `{"q":"kv"}`},
				{TS: ts.Add(time.Second), Role: "tool", Tool: "wiki.search", Result: "[…]"},
			},
			"edges": {
				toolRec(ts, "wiki.get", `{"page":"p"}`, ""),
				toolRec(ts.Add(time.Second), "vault.orient", "", "digest"),
				toolRec(ts.Add(2*time.Second), "stage.create_page", "op-1", "ok"),
				{TS: ts.Add(3 * time.Second), Role: "assistant", Tool: "wiki.search", Args: `{"q":"kv"}`},
			},
			"consecutive_tool_records": {
				toolRec(ts, "wiki.get", `{"page":"a"}`, "A"),
				toolRec(ts.Add(time.Second), "wiki.get", `{"page":"b"}`, "B"),
				toolRec(ts.Add(2*time.Second), "wiki.get", `{"page":"c"}`, "C"),
			},
		}
		for name, recs := range sets {
			all, _ := buildHistory(t, 100_000, recs, "Q2")
			if err := validChat(all); err != nil {
				t.Errorf("%s: Build output is not a valid chat: %v", name, err)
			}
		}
	})

	t.Run("forty_record_mixed_history_compacted", func(t *testing.T) {
		recs := mixedHistory(ts)
		if len(recs) != 40 {
			t.Fatalf("test setup: %d records, want 40", len(recs))
		}
		// 50 tokens is less than the fixture preamble, so Build hands
		// Compact a zero budget and every collapsible prose run goes.
		all, history := buildHistory(t, 50, recs, "Q2")

		placeholders := 0
		for _, m := range history {
			if m.Role == "system" && strings.Contains(m.Content, "omitted to fit the context budget") {
				placeholders++
			}
		}
		if placeholders == 0 {
			t.Fatalf("budget 50 collapsed nothing; the case would prove nothing about compaction: %d history messages", len(history))
		}
		// 8 cycles x (one read or staged op + one merged split call).
		if got := len(history) - placeholders; got != 8*4 {
			t.Errorf("history has %d non-placeholder messages, want %d (16 pairs)", got, 8*4)
		}
		if got := countCalls(history, "stage_create_page"); got != 2 {
			t.Errorf("%d staged stage_create_page calls survived compaction, want 2", got)
		}
		if got := countCalls(history, "wiki_search"); got != 8 {
			t.Errorf("%d wiki_search calls, want 8 (each split shape merged to one)", got)
		}
		if err := validChat(all); err != nil {
			t.Errorf("Build output is not a valid chat: %v", err)
		}

		// And the same 40 records under a budget that compacts nothing.
		whole, _ := buildHistory(t, 1_000_000, recs, "Q2")
		if err := validChat(whole); err != nil {
			t.Errorf("uncompacted Build output is not a valid chat: %v", err)
		}
	})

	t.Run("carried_records", func(t *testing.T) {
		recs := []Record{
			{TS: ts, Role: "user", Content: "Q1", Carried: true},
			{TS: ts.Add(time.Second), Role: "assistant", Content: "A1", Carried: true},
			{TS: ts.Add(2 * time.Second), Role: "tool", Tool: "wiki.get", Args: `{"page":"p"}`, Result: "carried body", Carried: true},
			{TS: ts.Add(3 * time.Second), Role: "assistant", Tool: "wiki.search", Args: `{"q":"c"}`, Carried: true},
			{TS: ts.Add(4 * time.Second), Role: "tool", Tool: "wiki.search", Result: "carried hits", Carried: true},
			toolRec(ts.Add(5*time.Second), "wiki.get", `{"page":"q"}`, "fresh body"),
		}
		all, history := buildHistory(t, 100_000, recs, "Q2")
		if err := validChat(all); err != nil {
			t.Errorf("Build output is not a valid chat: %v", err)
		}
		// Carried never reaches the wire as a field of its own: the carried
		// and fresh pairs are indistinguishable but for their ids' order.
		if got := countCalls(history, "wiki_get") + countCalls(history, "wiki_search"); got != 3 {
			t.Errorf("%d tool calls in history, want 3", got)
		}
	})

	t.Run("staged_record_kept_by_compact", func(t *testing.T) {
		long := strings.Repeat("a long turn that a zero budget collapses. ", 8)
		recs := []Record{
			rec(ts, "user", long),
			rec(ts.Add(time.Second), "assistant", long),
			{TS: ts.Add(2 * time.Second), Role: "tool", Tool: "stage.create_page", Args: `{"path":"wiki/concepts/x.md"}`, Result: "proposed op1", Staged: true},
			rec(ts.Add(3*time.Second), "user", long),
			rec(ts.Add(4*time.Second), "assistant", long),
		}
		all, history := buildHistory(t, 50, recs, "Q2")
		requireHistory(t, history, []llm.Message{
			{Role: "system", Content: "[2 earlier message(s) omitted to fit the context budget]"},
			wireCall("hist_1", "stage_create_page", `{"path":"wiki/concepts/x.md"}`),
			wireResult("hist_1", "proposed op1"),
			{Role: "system", Content: "[2 earlier message(s) omitted to fit the context budget]"},
		})
		if err := validChat(all); err != nil {
			t.Errorf("Build output is not a valid chat: %v", err)
		}
	})
}

// TestValidChatRejectsBadShapes keeps the regression net itself honest: a
// validator that accepted the pre-046 shape would let every Build test above
// pass whatever Build did.
func TestValidChatRejectsBadShapes(t *testing.T) {
	user := llm.Message{Role: "user", Content: "hi"}
	cases := []struct {
		name string
		msgs []llm.Message
		ok   bool
	}{
		{"empty list", nil, true},
		{"plain conversation", []llm.Message{user, {Role: "assistant", Content: "yo"}}, true},
		{"one pair", []llm.Message{user, wireCall("a", "wiki_get", `{}`), wireResult("a", "r"), user}, true},
		{"two calls in one assistant message", []llm.Message{
			{Role: "assistant", ToolCalls: append(wireCall("a", "wiki_get", `{}`).ToolCalls, wireCall("b", "wiki_get", `{}`).ToolCalls...)},
			wireResult("a", "r"), wireResult("b", "r"), user,
		}, true},
		{"the pre-046 text shape", []llm.Message{user, {Role: "tool", Content: "raw.list({}) -> none"}, user}, false},
		{"tool message with no preceding assistant", []llm.Message{user, wireResult("a", "r")}, false},
		{"tool message answers an unknown id", []llm.Message{wireCall("a", "wiki_get", `{}`), wireResult("zzz", "r")}, false},
		{"tool message with empty content", []llm.Message{wireCall("a", "wiki_get", `{}`), wireResult("a", "")}, false},
		{"id answered twice", []llm.Message{wireCall("a", "wiki_get", `{}`), wireResult("a", "r"), wireResult("a", "r")}, false},
		{"call never answered", []llm.Message{wireCall("a", "wiki_get", `{}`), user}, false},
		{"call never answered at the end", []llm.Message{user, wireCall("a", "wiki_get", `{}`)}, false},
		{"message between call and result", []llm.Message{wireCall("a", "wiki_get", `{}`), user, wireResult("a", "r")}, false},
		{"result answers an older assistant message", []llm.Message{
			wireCall("a", "wiki_get", `{}`), wireResult("a", "r"),
			wireCall("b", "wiki_get", `{}`), wireResult("b", "r"),
			wireResult("a", "r"),
		}, false},
		{"id announced twice", []llm.Message{
			wireCall("a", "wiki_get", `{}`), wireResult("a", "r"),
			wireCall("a", "wiki_get", `{}`), wireResult("a", "r"),
		}, false},
		{"empty id", []llm.Message{wireCall("", "wiki_get", `{}`), wireResult("", "r")}, false},
		{"dotted name", []llm.Message{wireCall("a", "wiki.get", `{}`), wireResult("a", "r")}, false},
		{"arguments not an object", []llm.Message{wireCall("a", "wiki_get", `op-1`), wireResult("a", "r")}, false},
		{"empty arguments", []llm.Message{wireCall("a", "wiki_get", ``), wireResult("a", "r")}, false},
		{"tool_calls on a user message", []llm.Message{{Role: "user", ToolCalls: wireCall("a", "wiki_get", `{}`).ToolCalls}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validChat(c.msgs)
			if c.ok && err != nil {
				t.Fatalf("validChat = %v, want nil", err)
			}
			if !c.ok && err == nil {
				t.Fatalf("validChat accepted an invalid chat: %+v", c.msgs)
			}
		})
	}
}

// TestBuildHistoryIDsDeterministic pins the cache-stability property the id
// scheme exists for: the same history yields the same ids on every Build,
// and the count restarts at hist_1 each call — never a session-wide or
// builder-wide counter, never random.
func TestBuildHistoryIDsDeterministic(t *testing.T) {
	ts := histTS
	v, _ := newTestVault(t)
	reg := tools.NewRegistry(tools.Deps{Vault: v})
	b := NewContextBuilder(v, reg, 100_000)
	s := &Session{ID: "cs-ids", ChangesetID: "cs-ids", Records: []Record{
		toolRec(ts, "wiki.get", `{"page":"a"}`, "A"),
		toolRec(ts.Add(time.Second), "wiki.get", `{"page":"b"}`, "B"),
	}}

	first, err := b.Build(s, "one")
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	// A turn appends records between Builds; the next Build sees more of them.
	s.Records = append(s.Records, toolRec(ts.Add(2*time.Second), "wiki.get", `{"page":"c"}`, "C"))
	second, err := b.Build(s, "two")
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}

	ids := func(msgs []llm.Message) []string {
		var out []string
		for _, m := range msgs {
			for _, tc := range m.ToolCalls {
				out = append(out, tc.ID)
			}
		}
		return out
	}
	if got, want := ids(first), []string{"hist_1", "hist_2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("first Build ids = %v, want %v", got, want)
	}
	if got, want := ids(second), []string{"hist_1", "hist_2", "hist_3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("second Build ids = %v, want %v (the shared prefix keeps its ids, so the provider's prefix cache survives)", got, want)
	}
	// The shared prefix is byte-identical between the two requests.
	pa, _ := json.Marshal(first[:len(first)-1])
	pb, _ := json.Marshal(second[:len(first)-1])
	if string(pa) != string(pb) {
		t.Errorf("the shared prefix of two Builds differs:\n%s\n%s", pa, pb)
	}
}

// TestBuildWireBytesNoToolRecords pins that 046 changes nothing for a history
// with no tool records: json.Marshal of Build's output is byte-identical to
// what the pre-046 code sent, which is a Message{Role, Content} per record.
// The expected history JSON below was captured from the pre-046 Build.
func TestBuildWireBytesNoToolRecords(t *testing.T) {
	ts := histTS
	recs := []Record{
		{TS: ts, Role: "user", Content: "Q1", Carried: true},
		{TS: ts.Add(1 * time.Second), Role: "assistant", Content: "A1", Carried: true, Reasoning: "never replayed"},
		{TS: ts.Add(2 * time.Second), Role: "assistant", Reasoning: "reasoning only; skipped by D-5G"},
		{TS: ts.Add(3 * time.Second), Role: "user", Content: "Q2 with \"quotes\" <and> & unicode é"},
		{TS: ts.Add(4 * time.Second), Role: "assistant", Content: "A2, cut off", Finish: "length", Turn: "20261005T003000Z-ab12"},
		{TS: ts.Add(5 * time.Second), Role: "system", Content: "[3 earlier message(s) omitted to fit the context budget]"},
	}
	all, history := buildHistory(t, 100_000, recs, "Q3")

	const wantHistory = `[` +
		`{"role":"user","content":"Q1"},` +
		`{"role":"assistant","content":"A1"},` +
		`{"role":"user","content":"Q2 with \"quotes\" ` + "\\u003cand\\u003e \\u0026" + ` unicode é"},` +
		`{"role":"assistant","content":"A2, cut off"},` +
		`{"role":"system","content":"[3 earlier message(s) omitted to fit the context budget]"}` +
		`]`
	gotHistory, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("marshal history: %v", err)
	}
	if string(gotHistory) != wantHistory {
		t.Errorf("history bytes changed:\n got %s\nwant %s", gotHistory, wantHistory)
	}

	// The whole request: the three system messages are the unchanged
	// preamble, and the user message closes it.
	v, _ := newTestVault(t)
	memory, err := v.Read("curator-memory.md")
	if err != nil {
		t.Fatalf("read curator-memory.md: %v", err)
	}
	var oldShape []llm.Message
	for _, m := range all[:3] {
		oldShape = append(oldShape, llm.Message{Role: m.Role, Content: m.Content})
	}
	if all[0].Content != systemPromptFor(false) || all[1].Content != string(memory) {
		t.Fatalf("preamble changed: system prompt or curator-memory.md is not what Build used to send")
	}
	for _, r := range recs {
		if r.Content == "" {
			continue
		}
		oldShape = append(oldShape, llm.Message{Role: r.Role, Content: r.Content})
	}
	oldShape = append(oldShape, llm.Message{Role: "user", Content: "Q3"})

	gotAll, err := json.Marshal(all)
	if err != nil {
		t.Fatalf("marshal all: %v", err)
	}
	wantAll, err := json.Marshal(oldShape)
	if err != nil {
		t.Fatalf("marshal old shape: %v", err)
	}
	if string(gotAll) != string(wantAll) {
		t.Errorf("request bytes changed for a history with no tool records:\n got %s\nwant %s", gotAll, wantAll)
	}
}

// TestResumedSessionRequestsAreValidChats is the live failure end to end: a
// real Loop runs one turn that calls tools (including an unknown tool, whose
// correctable error is a tool record like any other), then a second turn on
// the same session. Every request either turn sent must be a valid chat — the
// second turn's first request is the one DeepSeek answered with `422 …
// messages[6]: missing field tool_call_id` — and it must replay the first
// turn's three calls as hist_ pairs, in order.
func TestResumedSessionRequestsAreValidChats(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-0", "no_such_tool", `{}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-1", "raw_list", `{"query":"kv"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-2", "vault_orient", `{}`), {Finish: "tool_calls"}},
		{{Text: "first turn done"}, {Finish: "stop"}},
		{{Text: "second turn"}, {Finish: "stop"}},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	for _, msg := range []string{"look around", "and again"} {
		out := make(chan Event, 256)
		if err := l.Send(context.Background(), fx.csID, msg, out); err != nil {
			t.Fatalf("Send(%q): %v", msg, err)
		}
		drain(out)
	}

	reqs := fake.Requests()
	if len(reqs) != 5 {
		t.Fatalf("Stream called %d times, want 5 (4 rounds in turn 1, 1 in turn 2)", len(reqs))
	}
	for i, req := range reqs {
		if err := validChat(req.Messages); err != nil {
			t.Errorf("request %d is not a valid chat: %v", i+1, err)
		}
	}

	var gotNames, gotIDs []string
	for _, m := range reqs[4].Messages {
		for _, tc := range m.ToolCalls {
			if strings.HasPrefix(tc.ID, "hist_") {
				gotNames = append(gotNames, tc.Function.Name)
				gotIDs = append(gotIDs, tc.ID)
			}
		}
	}
	if want := []string{"no_such_tool", "raw_list", "vault_orient"}; !reflect.DeepEqual(gotNames, want) {
		t.Errorf("turn 2 replays history calls %v, want %v", gotNames, want)
	}
	if want := []string{"hist_1", "hist_2", "hist_3"}; !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("turn 2 replays history ids %v, want %v", gotIDs, want)
	}
}

// TestBuildSanitizesIllegalToolNames pins A-046-3. The unknown-tool path
// records the model's own spelling of a tool it invented, and WireName only
// maps dots, so a name like "Bad Name!" would reach the wire as written and
// every strict provider would refuse the replayed tool_calls entry — on this
// turn and on every later one that resumes the session, since the record is
// permanent. The name a pair carries is therefore made legal
// ([a-zA-Z0-9_-]+, internal/tools/names.go) rune by rune, after WireName.
func TestBuildSanitizesIllegalToolNames(t *testing.T) {
	ts := histTS
	cases := []struct {
		tool, want string
	}{
		{"Bad Name!", "Bad_Name_"},
		{"foo bar.baz", "foo_bar_baz"},
		{"a/b:c", "a_b_c"},
		{"é-x", "_-x"},    // one multi-byte rune is one underscore
		{"x\xffy", "x_y"}, // so is one invalid UTF-8 byte
		{"A-b_9", "A-b_9"},
		{"wiki.get", "wiki_get"},
		{"stage.create_page", "stage_create_page"},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			recs := []Record{toolRec(ts, c.tool, `{"k":"v"}`, "R")}
			all, got := buildHistory(t, 100_000, recs, "next")
			requireHistory(t, got, []llm.Message{
				wireCall("hist_1", c.want, `{"k":"v"}`),
				wireResult("hist_1", "R"),
			})
			if err := validChat(all); err != nil {
				t.Errorf("Build output is not a valid chat: %v", err)
			}
		})
	}

	t.Run("empty name becomes unknown_tool", func(t *testing.T) {
		// Build never reaches this (a pair needs Tool != ""), so the rule is
		// pinned on the helper itself.
		if got := sanitizeWireName(""); got != "unknown_tool" {
			t.Errorf("sanitizeWireName(\"\") = %q, want unknown_tool", got)
		}
		if got := sanitizeWireName("ok_name"); got != "ok_name" {
			t.Errorf("sanitizeWireName(ok_name) = %q, want it unchanged", got)
		}
	})
}

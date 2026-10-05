package agent

// deadweight_test.go pins 041: a staged page body is dead weight on the wire
// once the model has emitted it, because it is already in the open changeset
// and wiki.get reads it back. Two seams carry the same stub:
//
//   - Build replays a staged tool record's arguments with the big page text
//     replaced by a deterministic stub (history, always on, cache-safe);
//   - boundContext's phase 0 stubs this turn's older stage calls before it
//     elides any read result (within the turn, only when over budget).
//
// The stub text is written out literally here — never read back from
// production — so a drift in the format fails these tests rather than agreeing
// with itself. 041's scope is cost and context room, never latency.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// dwStub is the frozen stub for n bytes of staged content.
func dwStub(n int) string {
	return fmt.Sprintf("[elided: %d bytes of staged content — it is in the open changeset; read it back with wiki.get]", n)
}

// dwObj marshals kv the way the stub re-encodes: keys sorted. It builds
// expectations for cases whose exact bytes are not the point of the test.
func dwObj(t *testing.T, kv map[string]any) string {
	t.Helper()
	b, err := json.Marshal(kv)
	if err != nil {
		t.Fatalf("marshal %v: %v", kv, err)
	}
	return string(b)
}

// TestBuildStubsStagedContent is the frozen history pin: a replayed
// stage.create_page / stage.patch_page record whose page text is longer than
// 512 bytes goes on the wire with that value replaced by the stub, every other
// key kept; anything else — short text, another tool, arguments that are not a
// JSON object — is replayed exactly as 046 replays it.
//
// A-041-1: the frozen block says stage.create_page carries `content`. It does
// not: the schema's page text is `body` (stage_page.go stageCreatePageSchema,
// and every recorded session), `content` belongs to stage.patch_page. A stub
// keyed on `content` alone would leave every real create_page body on the wire,
// which is the exact weight 041 exists to remove. The frozen literal case
// (create_page + `content`) is kept and still stubs; `body` is added beside it.
func TestBuildStubsStagedContent(t *testing.T) {
	ts := histTS
	c := func(n int) string { return strings.Repeat("c", n) }

	// The frozen case, byte for byte: keys come out sorted, the stub names the
	// byte count, and the rest of the object is untouched.
	const frozenWant = `{"content":"[elided: 2000 bytes of staged content — it is in the open changeset; read it back with wiki.get]","path":"wiki/concepts/x.md","rationale":"r","title":"X"}`

	createBody := func(n int) string {
		return `{"path":"wiki/concepts/x.md","title":"X","type":"concept","tags":["inference"],"sources":["raw/papers/leviathan-2023.md"],"confidence":"low","contested":false,"body":"` + c(n) + `","rationale":"r"}`
	}
	createBodyWant := func(t *testing.T, n int) string {
		return dwObj(t, map[string]any{
			"path": "wiki/concepts/x.md", "title": "X", "type": "concept", "tags": []string{"inference"},
			"sources": []string{"raw/papers/leviathan-2023.md"}, "confidence": "low", "contested": false,
			"body": dwStub(n), "rationale": "r",
		})
	}
	patch := func(n int) string {
		return `{"path":"wiki/concepts/x.md","section":"## Related","op":"replace_section","content":"` + c(n) + `","rationale":"r"}`
	}
	patchWant := func(t *testing.T, n int) string {
		return dwObj(t, map[string]any{
			"path": "wiki/concepts/x.md", "section": "## Related", "op": "replace_section",
			"content": dwStub(n), "rationale": "r",
		})
	}
	plain := func(key string, n int) string {
		return `{"path":"wiki/concepts/x.md","title":"X","` + key + `":"` + c(n) + `","rationale":"r"}`
	}
	huge := strings.Repeat("x", 2000)

	cases := []struct {
		name string
		tool string
		args string
		want string         // the exact arguments string the pair replays
		stub map[string]int // keys that must come out as dwStub(n); everything else must equal the input
	}{
		{"create_page_content_2000_frozen", "stage.create_page", plain("content", 2000), frozenWant, map[string]int{"content": 2000}},
		{"create_page_real_body_2000", "stage.create_page", createBody(2000), createBodyWant(t, 2000), map[string]int{"body": 2000}},
		{"patch_page_content_2000", "stage.patch_page", patch(2000), patchWant(t, 2000), map[string]int{"content": 2000}},
		{"content_300_unchanged", "stage.create_page", plain("content", 300), plain("content", 300), nil},
		{"body_300_unchanged", "stage.create_page", createBody(300), createBody(300), nil},
		{"content_exactly_512_unchanged", "stage.patch_page", patch(512), patch(512), nil},
		{"content_513_stubbed", "stage.patch_page", patch(513), patchWant(t, 513), map[string]int{"content": 513}},
		{
			"n_counts_bytes_not_runes", "stage.patch_page",
			`{"path":"wiki/concepts/x.md","section":"## S","op":"append_section","content":"` + strings.Repeat("é", 300) + `","rationale":"r"}`,
			dwObj(t, map[string]any{"path": "wiki/concepts/x.md", "section": "## S", "op": "append_section", "content": dwStub(600), "rationale": "r"}),
			map[string]int{"content": 600},
		},
		{
			"body_and_content_both_stubbed", "stage.create_page",
			`{"path":"p","body":"` + c(700) + `","content":"` + c(900) + `"}`,
			dwObj(t, map[string]any{"path": "p", "body": dwStub(700), "content": dwStub(900)}),
			map[string]int{"body": 700, "content": 900},
		},
		{"wiki_get_record_unchanged", "wiki.get", plain("content", 2000), plain("content", 2000), nil},
		{"other_stage_tool_unchanged", "stage.rename_page", plain("content", 2000), plain("content", 2000), nil},
		{"non_string_content_unchanged", "stage.patch_page", `{"path":"p","content":12345678901234567890,"rationale":"r"}`, `{"path":"p","content":12345678901234567890,"rationale":"r"}`, nil},
		{"null_content_unchanged", "stage.patch_page", `{"path":"p","content":null}`, `{"path":"p","content":null}`, nil},
		{"non_json_args_keep_raw_wrapper", "stage.create_page", huge, dwObj(t, map[string]any{"raw": huge}), nil},
		{"json_array_args_keep_raw_wrapper", "stage.create_page", `["` + huge + `"]`, dwObj(t, map[string]any{"raw": `["` + huge + `"]`}), nil},
		{"empty_args_stay_empty_object", "stage.create_page", "", "{}", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs := []Record{
				rec(ts, "user", "Q1"),
				toolRec(ts.Add(time.Second), tc.tool, tc.args, "proposed op1"),
			}
			all, got := buildHistory(t, 1_000_000, recs, "Q2")

			requireHistory(t, got, []llm.Message{
				{Role: "user", Content: "Q1"},
				wireCall("hist_1", tools.WireName(tc.tool), tc.want),
				wireResult("hist_1", "proposed op1"),
			})
			if err := validChat(all); err != nil {
				t.Errorf("Build output is not a valid chat: %v", err)
			}
			if recs[1].Args != tc.args {
				t.Errorf("the record's own Args changed: got %d bytes, want the original %d — only the wire copy may be stubbed", len(recs[1].Args), len(tc.args))
			}

			// Decoded, independently of the exact bytes: the same keys as the
			// input, the stubbed ones equal to the frozen text, the rest equal
			// to the input's own values.
			if tc.stub == nil {
				return
			}
			var in, out map[string]any
			if err := json.Unmarshal([]byte(tc.args), &in); err != nil {
				t.Fatalf("input args are not an object: %v", err)
			}
			if err := json.Unmarshal([]byte(got[1].ToolCalls[0].Function.Arguments), &out); err != nil {
				t.Fatalf("replayed arguments are not an object: %v", err)
			}
			if len(in) != len(out) {
				t.Errorf("replayed keys differ: input %d keys, output %d keys", len(in), len(out))
			}
			for k, v := range in {
				if n, stubbed := tc.stub[k]; stubbed {
					if out[k] != dwStub(n) {
						t.Errorf("key %q = %v, want the stub for %d bytes", k, out[k], n)
					}
					continue
				}
				if !reflect.DeepEqual(out[k], v) {
					t.Errorf("key %q changed: %v -> %v; only the page text may be stubbed", k, v, out[k])
				}
			}
		})
	}

	// The split shape (046): the call logged as an assistant record with the
	// args, then a tool record with the result. It is still ONE pair, and the
	// stub applies to the args half.
	t.Run("split_record_shape", func(t *testing.T) {
		recs := []Record{
			rec(ts, "user", "Q1"),
			{TS: ts.Add(time.Second), Role: "assistant", Tool: "stage.patch_page", Args: patch(2000)},
			{TS: ts.Add(2 * time.Second), Role: "tool", Tool: "stage.patch_page", Result: "patched"},
		}
		_, got := buildHistory(t, 1_000_000, recs, "Q2")
		requireHistory(t, got, []llm.Message{
			{Role: "user", Content: "Q1"},
			wireCall("hist_1", "stage_patch_page", patchWant(t, 2000)),
			wireResult("hist_1", "patched"),
		})
	})
}

// TestStubKeysMatchRegistrySchema ties the stubbed key to the schema the model
// is actually shown: the property that carries the page text of each tool is a
// string property of that tool's real schema, and a 2000-byte value under it
// is stubbed. This is the guard A-041-1 lacked — the frozen block named a key
// the schema does not have, and nothing noticed because every fixture was
// written from the same wrong premise.
func TestStubKeysMatchRegistrySchema(t *testing.T) {
	fx := newTestLoopFixture(t)
	for _, tc := range []struct{ tool, key string }{
		{"stage.create_page", "body"},
		{"stage.patch_page", "content"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			tool, ok := fx.reg.Get(tc.tool)
			if !ok {
				t.Fatalf("registry has no %s", tc.tool)
			}
			var schema struct {
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(tool.Schema, &schema); err != nil {
				t.Fatalf("schema: %v", err)
			}
			if schema.Properties[tc.key].Type != "string" {
				t.Fatalf("%s schema has no string property %q: %v", tc.tool, tc.key, schema.Properties)
			}

			args := dwObj(t, map[string]any{"path": "wiki/concepts/x.md", tc.key: strings.Repeat("p", 2000)})
			_, got := buildHistory(t, 1_000_000, []Record{
				toolRec(histTS, tc.tool, args, "proposed"),
			}, "Q")
			var out map[string]any
			if err := json.Unmarshal([]byte(got[0].ToolCalls[0].Function.Arguments), &out); err != nil {
				t.Fatalf("replayed arguments: %v", err)
			}
			if out[tc.key] != dwStub(2000) {
				t.Errorf("%s.%s = %v, want the 2000-byte stub — the real page text is still on the wire", tc.tool, tc.key, out[tc.key])
			}
		})
	}
}

// TestBuildStubDeterministic: two Builds of the same session are
// byte-identical once marshaled, so the provider's prefix cache keeps hitting
// across turns. The arguments are given in an unsorted key order and in two
// different orders, so a re-encoding that follows input order — or Go's map
// iteration order — cannot pass.
func TestBuildStubDeterministic(t *testing.T) {
	ts := histTS
	big := strings.Repeat("d", 4000)
	argsA := `{"rationale":"r","path":"wiki/concepts/x.md","title":"X","body":"` + big + `","type":"concept"}`
	argsB := `{"type":"concept","body":"` + big + `","title":"X","path":"wiki/concepts/x.md","rationale":"r"}`
	recs := []Record{
		rec(ts, "user", "Q1"),
		toolRec(ts.Add(1*time.Second), "stage.create_page", argsA, "proposed op1"),
		toolRec(ts.Add(2*time.Second), "stage.create_page", argsB, "proposed op2"),
		toolRec(ts.Add(3*time.Second), "stage.patch_page", `{"section":"## S","path":"p","op":"replace_section","content":"`+big+`"}`, "patched"),
		rec(ts.Add(4*time.Second), "assistant", "A1"),
	}

	v, _ := newTestVault(t)
	b := NewContextBuilder(v, tools.NewRegistry(tools.Deps{Vault: v}), 1_000_000)
	s := &Session{ID: "cs-dw", ChangesetID: "cs-dw", Records: recs}

	first, err := b.Build(s, "Q2")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := b.Build(s, "Q2")
	if err != nil {
		t.Fatalf("Build (second): %v", err)
	}
	a, _ := json.Marshal(first)
	z, _ := json.Marshal(second)
	if !bytes.Equal(a, z) {
		t.Fatalf("two Builds of the same session differ:\n first %s\nsecond %s", a, z)
	}

	// Non-vacuous: the stub really is on the wire, with sorted keys, whatever
	// order the model wrote them in.
	const wantCreate = `{"body":"[elided: 4000 bytes of staged content — it is in the open changeset; read it back with wiki.get]","path":"wiki/concepts/x.md","rationale":"r","title":"X","type":"concept"}`
	history := first[3 : len(first)-1]
	for _, i := range []int{1, 3} {
		if got := history[i].ToolCalls[0].Function.Arguments; got != wantCreate {
			t.Errorf("history[%d] arguments:\n got %s\nwant %s", i, got, wantCreate)
		}
	}
	if strings.Contains(string(a), big) {
		t.Errorf("the 4000-byte page text is still somewhere in the marshaled request")
	}
}

// TestBuildStubLeavesRecordsAlone: Build stubs the wire copy only. The session
// it was handed — the durable store's own view of every staged call — is
// byte-for-byte what it was, and a second Build still sees the full text to
// stub it again.
func TestBuildStubLeavesRecordsAlone(t *testing.T) {
	ts := histTS
	args := `{"path":"p","content":"` + strings.Repeat("k", 3000) + `"}`
	recs := []Record{
		rec(ts, "user", "Q1"),
		toolRec(ts.Add(time.Second), "stage.patch_page", args, "patched"),
		{TS: ts.Add(2 * time.Second), Role: "assistant", Tool: "stage.patch_page", Args: args},
		{TS: ts.Add(3 * time.Second), Role: "tool", Tool: "stage.patch_page", Result: "patched again"},
	}
	snapshot := append([]Record(nil), recs...)

	v, _ := newTestVault(t)
	b := NewContextBuilder(v, tools.NewRegistry(tools.Deps{Vault: v}), 1_000_000)
	s := &Session{ID: "cs-dw", ChangesetID: "cs-dw", Records: recs}
	for i := 0; i < 2; i++ {
		msgs, err := b.Build(s, "Q2")
		if err != nil {
			t.Fatalf("Build %d: %v", i, err)
		}
		if err := validChat(msgs); err != nil {
			t.Errorf("Build %d is not a valid chat: %v", i, err)
		}
		if !reflect.DeepEqual(s.Records, snapshot) {
			t.Fatalf("Build %d changed the session's records", i)
		}
	}
}

// ---- boundContext phase 0 ----

// dwBase is the fixed part of a request: what Build returns before the turn
// starts. turnStart for every message list built on it is len(dwBase()).
func dwBase() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "go"},
	}
}

// dwRes is a tool-result message the way dispatchToolCall builds one: the wire
// spelling in Name, which boundContext canonicalizes.
func dwRes(id, wire, content string) llm.Message {
	return llm.Message{Role: "tool", ToolCallID: id, Name: wire, Content: content}
}

// dwStageArgs is a stage call's argument object with n bytes of page text
// under key.
func dwStageArgs(key string, n int) string {
	return `{"path":"wiki/concepts/x.md","title":"X","` + key + `":"` + strings.Repeat("s", n) + `","rationale":"r"}`
}

// dwBound runs boundContext with fresh per-turn state as round 3.
func dwBound(msgs []llm.Message, budget int) []llm.Message {
	return boundContext(context.Background(), msgs, len(dwBase()), map[int]bool{}, map[string]bool{}, 3, budget)
}

// dwStubbed is the test's own derivation of what msgs look like once the named
// tool calls' page text is stubbed: a deep copy, never touching msgs. Budgets
// are sized from it, independently of production's estimator wiring.
func dwStubbed(t *testing.T, msgs []llm.Message, ids ...string) []llm.Message {
	t.Helper()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		out[i].ToolCalls = nil
		for _, tc := range m.ToolCalls {
			if want[tc.ID] {
				var obj map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &obj); err != nil {
					t.Fatalf("call %s arguments are not an object: %v", tc.ID, err)
				}
				for _, key := range []string{"body", "content"} {
					if s, ok := obj[key].(string); ok && len(s) > 512 {
						obj[key] = dwStub(len(s))
					}
				}
				b, err := json.Marshal(obj)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				tc.Function.Arguments = string(b)
			}
			out[i].ToolCalls = append(out[i].ToolCalls, tc)
		}
	}
	return out
}

// dwArgsOf decodes the arguments of the tool call with id from msgs.
func dwArgsOf(t *testing.T, msgs []llm.Message, id string) map[string]any {
	t.Helper()
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.ID == id {
				var obj map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &obj); err != nil {
					t.Fatalf("call %s arguments are not an object: %v", id, err)
				}
				return obj
			}
		}
	}
	t.Fatalf("no tool call %q in the request", id)
	return nil
}

// dwSnapshot is the wire bytes of msgs, deep: a later in-place edit of any
// message, tool call or argument shows up as a difference.
func dwSnapshot(t *testing.T, msgs []llm.Message) string {
	t.Helper()
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestBoundContextStubsStagedArgsFirst is the frozen phase-0 pin: round 1 is a
// stage_create_page carrying 6 KB of page text, round 2 an older raw_get with a
// 6 KB result, round 3 the live round. With a budget that fits once the create
// call's text is stubbed, the create call is stubbed and the raw_get result —
// the evidence — is NOT elided. Before 041 the stage args were untouchable, so
// the read result was the only thing that could give, and source text was
// pushed out first to keep a page the model had already written.
func TestBoundContextStubsStagedArgsFirst(t *testing.T) {
	msgs := append(dwBase(),
		wireCall("c1", "stage_create_page", dwStageArgs("content", 6000)),
		dwRes("c1", "stage_create_page", "proposed op1"),
		wireCall("r1", "raw_get", `{"path":"raw/a.md"}`),
		dwRes("r1", "raw_get", strings.Repeat("a", 6000)),
		wireCall("r2", "raw_get", `{"path":"raw/b.md"}`),
		dwRes("r2", "raw_get", "tiny"),
	)
	original := dwSnapshot(t, msgs)
	budget := wireEstimate(dwStubbed(t, msgs, "c1"))

	t.Run("stub_alone_fits", func(t *testing.T) {
		logPath := installFileLog(t)
		got := dwBound(msgs, budget)

		if args := dwArgsOf(t, got, "c1"); args["content"] != dwStub(6000) {
			t.Errorf("create call content = %v, want the 6000-byte stub", args["content"])
		}
		if m := toolMsgByID(t, got, "r1"); m.Content != strings.Repeat("a", 6000) {
			t.Errorf("the older raw_get result was elided though stubbing the create args alone fits: %q", m.Content)
		}
		if m := toolMsgByID(t, got, "r2"); m.Content != "tiny" {
			t.Errorf("the most recent round's result changed: %q", m.Content)
		}
		if len(got) != len(msgs) {
			t.Errorf("request has %d messages, want the original %d — a stub replaces text, it drops nothing", len(got), len(msgs))
		}
		if est := wireEstimate(got); est > budget {
			t.Errorf("request still estimates %d over the %d budget after the stub", est-budget, budget)
		}
		if dwSnapshot(t, msgs) != original {
			t.Errorf("the input slice was mutated")
		}

		log := readLog(t, logPath)
		for _, want := range []string{`msg="context elided"`, "round=3", "messages=1", "bytes=6000"} {
			if !strings.Contains(log, want) {
				t.Errorf("log missing %q:\n%s", want, log)
			}
		}
		if strings.Contains(log, "context over budget") {
			t.Errorf("the stub reached the budget; want no over-budget warn:\n%s", log)
		}
	})

	// Phase 0 does not replace the read-result phase, it runs first: when the
	// stub is not enough, the oldest eligible read result is elided as before,
	// and both are counted in the one log line.
	t.Run("read_elision_still_runs_after_the_stub", func(t *testing.T) {
		logPath := installFileLog(t)
		got := dwBound(msgs, budget-1000)

		if args := dwArgsOf(t, got, "c1"); args["content"] != dwStub(6000) {
			t.Errorf("create call content = %v, want the stub", args["content"])
		}
		wantElided := "[elided to fit the context budget: raw.get result, 6000 bytes — call raw.get again if you still need it]"
		if m := toolMsgByID(t, got, "r1"); m.Content != wantElided {
			t.Errorf("older raw_get result = %q, want the F.C2 string %q", m.Content, wantElided)
		}
		if m := toolMsgByID(t, got, "r2"); m.Content != "tiny" {
			t.Errorf("the most recent round's result changed: %q", m.Content)
		}
		log := readLog(t, logPath)
		for _, want := range []string{"messages=2", "bytes=12000"} {
			if !strings.Contains(log, want) {
				t.Errorf("log missing %q — stubbed calls count in the same line as elided results:\n%s", want, log)
			}
		}
	})
}

// TestBoundContextStubsOldestFirstAndStopsWhenFits: three create rounds; the
// stub walks oldest first and stops at the first point the request fits, so a
// budget that one stub satisfies leaves the later pages whole.
func TestBoundContextStubsOldestFirstAndStopsWhenFits(t *testing.T) {
	msgs := append(dwBase(),
		wireCall("c1", "stage_create_page", dwStageArgs("body", 6000)), dwRes("c1", "stage_create_page", "ok1"),
		wireCall("c2", "stage_create_page", dwStageArgs("body", 6000)), dwRes("c2", "stage_create_page", "ok2"),
		wireCall("c3", "stage_patch_page", dwStageArgs("content", 6000)), dwRes("c3", "stage_patch_page", "ok3"),
	)

	for _, tc := range []struct {
		name     string
		stubbed  []string // ids whose budget this is: the request fits once exactly these are stubbed
		wantFull []string
	}{
		{"one_stub_fits", []string{"c1"}, []string{"c2", "c3"}},
		{"two_stubs_fit", []string{"c1", "c2"}, []string{"c3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dwBound(msgs, wireEstimate(dwStubbed(t, msgs, tc.stubbed...)))
			for _, id := range tc.stubbed {
				args := dwArgsOf(t, got, id)
				key := "body"
				if id == "c3" {
					key = "content"
				}
				if args[key] != dwStub(6000) {
					t.Errorf("%s %s = %.40v…, want the stub", id, key, args[key])
				}
			}
			for _, id := range tc.wantFull {
				args := dwArgsOf(t, got, id)
				for _, key := range []string{"body", "content"} {
					if s, ok := args[key].(string); ok && len(s) != 6000 {
						t.Errorf("%s %s was stubbed (%d bytes left) though the request already fit", id, key, len(s))
					}
				}
			}
		})
	}
}

// TestBoundContextStubsEachCallOfOneRound: a round may carry several stage
// calls in one assistant message. Each is its own stub step — the walk stops
// after the first when that fits — and the message's ToolCalls array is cloned
// before any call is touched.
func TestBoundContextStubsEachCallOfOneRound(t *testing.T) {
	two := llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		wireCall("c1", "stage_create_page", dwStageArgs("body", 6000)).ToolCalls[0],
		wireCall("c2", "stage_create_page", dwStageArgs("body", 6000)).ToolCalls[0],
	}}
	msgs := append(dwBase(),
		two, dwRes("c1", "stage_create_page", "ok1"), dwRes("c2", "stage_create_page", "ok2"),
		wireCall("r1", "raw_get", `{"path":"raw/a.md"}`), dwRes("r1", "raw_get", "tiny"),
	)
	before := dwSnapshot(t, msgs)

	got := dwBound(msgs, wireEstimate(dwStubbed(t, msgs, "c1")))
	if args := dwArgsOf(t, got, "c1"); args["body"] != dwStub(6000) {
		t.Errorf("first call of the round not stubbed: %.40v…", args["body"])
	}
	if args := dwArgsOf(t, got, "c2"); len(args["body"].(string)) != 6000 {
		t.Errorf("second call of the round was stubbed though the first alone fit")
	}
	if dwSnapshot(t, msgs) != before {
		t.Errorf("the input messages were mutated — the round's ToolCalls array must be cloned before an argument is rewritten")
	}

	both := dwBound(msgs, wireEstimate(dwStubbed(t, msgs, "c1", "c2")))
	for _, id := range []string{"c1", "c2"} {
		if args := dwArgsOf(t, both, id); args["body"] != dwStub(6000) {
			t.Errorf("%s not stubbed under a budget that needs both", id)
		}
	}
}

// TestBoundContextSmallContentNotStubbed: the >512 rule holds inside the turn
// too — a stage call whose page text is at most 512 bytes is left alone even
// when the request is over budget.
func TestBoundContextSmallContentNotStubbed(t *testing.T) {
	msgs := append(dwBase(),
		wireCall("c1", "stage_patch_page", dwStageArgs("content", 512)), dwRes("c1", "stage_patch_page", "ok1"),
		wireCall("c2", "stage_patch_page", dwStageArgs("content", 513)), dwRes("c2", "stage_patch_page", "ok2"),
		wireCall("c3", "stage_patch_page", dwStageArgs("content", 600)), dwRes("c3", "stage_patch_page", "ok3"),
	)
	got := dwBound(msgs, 1) // hopelessly over budget: everything eligible gives
	if args := dwArgsOf(t, got, "c1"); len(args["content"].(string)) != 512 {
		t.Errorf("a 512-byte content was stubbed")
	}
	if args := dwArgsOf(t, got, "c2"); args["content"] != dwStub(513) {
		t.Errorf("a 513-byte content was not stubbed: %.40v…", args["content"])
	}
	if args := dwArgsOf(t, got, "c3"); len(args["content"].(string)) != 600 {
		t.Errorf("the most recent round's content was stubbed")
	}
}

// TestBoundContextStubCountsBytesNotRunes: the stub's N and the "bytes" the
// log and the trace report are bytes of text, as the read-result elision's are
// (len of the content), not runes — a page in a multi-byte language is the case
// where the two differ by a factor of three.
func TestBoundContextStubCountsBytesNotRunes(t *testing.T) {
	logPath := installFileLog(t)
	text := strings.Repeat("é", 300) // 300 runes, 600 bytes
	msgs := append(dwBase(),
		wireCall("c1", "stage_patch_page", `{"path":"p","section":"## S","op":"append_section","content":"`+text+`","rationale":"r"}`),
		dwRes("c1", "stage_patch_page", "ok1"),
		wireCall("r1", "raw_get", `{"path":"raw/a.md"}`), dwRes("r1", "raw_get", "tiny"),
	)
	got := dwBound(msgs, 1)
	if args := dwArgsOf(t, got, "c1"); args["content"] != dwStub(600) {
		t.Errorf("content = %v, want the 600-byte stub", args["content"])
	}
	log := readLog(t, logPath)
	for _, want := range []string{"messages=1", "bytes=600"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
}

// TestBoundContextUnderBudgetIdentical: under budget the bound is a no-op that
// returns the very slice it was given — pointer equality, as before 041 — even
// when that slice holds 6 KB page bodies. History stubs are Build's business;
// a request that fits is never rewritten within the turn.
func TestBoundContextUnderBudgetIdentical(t *testing.T) {
	msgs := append(dwBase(),
		wireCall("c1", "stage_create_page", dwStageArgs("body", 6000)), dwRes("c1", "stage_create_page", "ok1"),
		wireCall("c2", "stage_create_page", dwStageArgs("body", 6000)), dwRes("c2", "stage_create_page", "ok2"),
	)
	before := dwSnapshot(t, msgs)

	for _, budget := range []int{1_000_000, wireEstimate(msgs)} { // well under, and exactly at the limit
		got := dwBound(msgs, budget)
		if len(got) != len(msgs) || &got[0] != &msgs[0] {
			t.Errorf("budget %d: bound returned a different slice than the input — under budget must be the input itself", budget)
		}
		if dwSnapshot(t, got) != before {
			t.Errorf("budget %d: the under-budget request changed", budget)
		}
	}
}

// TestBoundContextMostRecentRoundUntouched: the live round is the model's own
// just-produced output; its stage args are never stubbed, however far over
// budget the request is.
func TestBoundContextMostRecentRoundUntouched(t *testing.T) {
	logPath := installFileLog(t)
	two := llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		wireCall("c2", "stage_create_page", dwStageArgs("body", 6000)).ToolCalls[0],
		wireCall("c3", "stage_patch_page", dwStageArgs("content", 6000)).ToolCalls[0],
	}}
	msgs := append(dwBase(),
		wireCall("c1", "stage_create_page", dwStageArgs("body", 6000)), dwRes("c1", "stage_create_page", "ok1"),
		two, dwRes("c2", "stage_create_page", "ok2"), dwRes("c3", "stage_patch_page", "ok3"),
	)

	got := dwBound(msgs, 1)
	if args := dwArgsOf(t, got, "c1"); args["body"] != dwStub(6000) {
		t.Errorf("the older round's create call was not stubbed: %.40v…", args["body"])
	}
	if args := dwArgsOf(t, got, "c2"); len(args["body"].(string)) != 6000 {
		t.Errorf("the most recent round's create call was stubbed")
	}
	if args := dwArgsOf(t, got, "c3"); len(args["content"].(string)) != 6000 {
		t.Errorf("the most recent round's patch call was stubbed")
	}
	if log := readLog(t, logPath); !strings.Contains(log, "context over budget") {
		t.Errorf("a request left over budget must still warn (F.C3):\n%s", log)
	}

	// A turn whose only round is the live one has nothing to stub at all: the
	// request goes out as it is.
	only := append(dwBase(), wireCall("c1", "stage_create_page", dwStageArgs("body", 6000)), dwRes("c1", "stage_create_page", "ok1"))
	if got := dwBound(only, 1); &got[0] != &only[0] || dwArgsOf(t, got, "c1")["body"] == dwStub(6000) {
		t.Errorf("a single-round turn was rewritten; the live round is never touched")
	}
}

// dwCreateArgs is a valid, real stage.create_page payload whose body is
// exactly n bytes — the shape the live model sends, links and all, so the call
// stages for real through the registry.
func dwCreateArgs(t *testing.T, n int) string {
	t.Helper()
	body := "# Dead Weight\n\nSee [[kv-cache]] and [[gpt-4]].\n\n"
	for i := 0; len(body) < n; i++ {
		body += fmt.Sprintf("Sentence %d about staged page bodies that ride every round.\n", i)
	}
	return dwObj(t, map[string]any{
		"path": "wiki/concepts/dead-weight.md", "title": "Dead Weight", "type": "concept",
		"tags": []string{"inference"}, "sources": []string{"raw/papers/leviathan-2023.md"},
		"confidence": "low", "contested": false, "body": body[:n], "rationale": "pin 041",
	})
}

// dwBodyBytes is the size of the page body every loop-driven pin stages.
const dwBodyBytes = 9000

// dwRun is one scripted turn driven through the real Loop.
type dwRun struct {
	fake    *fakeStreamer
	fx      *testLoopFixture
	events  []Event
	traces  string
	logPath string
}

// dwLoop sends the 041 script at budget: round 1 stages a real 9 KB page,
// round 2 reads the 20000-byte big page, round 3 reads a small one, round 4
// answers. Requests 3 and 4 are where the staged call is an older round and the
// big read is eligible too.
func dwLoop(t *testing.T, budget int) dwRun {
	t.Helper()
	logPath := installFileLog(t)
	fx, _, _ := newBudgetFixture(t, bigResultBytes)
	fake := &fakeStreamer{rounds: [][]llm.Chunk{
		{toolCallChunk("call-s1", "stage_create_page", dwCreateArgs(t, dwBodyBytes)), {Finish: "tool_calls"}},
		{toolCallChunk("call-w1", "wiki_get", bigWikiArgs), {Finish: "tool_calls"}},
		{toolCallChunk("call-k1", "wiki_get", `{"page":"kv-cache"}`), {Finish: "tool_calls"}},
		{{Text: "done"}, {Finish: "stop"}},
	}}
	dir := filepath.Join(t.TempDir(), "traces")
	l := newLoop(&observingStreamer{fake: fake}, fx.reg, fx.store, fx.engine, LoopConfig{ContextTokens: budget, TraceDir: dir})
	out := make(chan Event, 256)
	if err := l.Send(context.Background(), fx.csID, "stage a page, then read around", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return dwRun{fake: fake, fx: fx, events: drain(out), traces: dir, logPath: logPath}
}

// TestBoundContextNoMutation: requests the loop already sent stay exactly what
// they were. A stubbing round builds a fresh message list AND a fresh ToolCalls
// array for every message it rewrites — a plain copy of the message slice still
// shares each message's ToolCalls backing array, and rewriting an argument
// through it would quietly turn an earlier request, which the fake streamer (and
// a retry, and the trace) still hold, into the stubbed one.
func TestBoundContextNoMutation(t *testing.T) {
	// The seam itself: the input slice, its messages and their ToolCalls
	// arrays are what they were after a call that stubs, and the result is a
	// different slice. A loop-driven check alone cannot tell an in-place write
	// through a shared message slice from a fresh copy, because append may
	// have moved the earlier request's array by then.
	t.Run("input_slice_and_tool_calls", func(t *testing.T) {
		msgs := append(dwBase(),
			wireCall("c1", "stage_create_page", dwStageArgs("body", 6000)), dwRes("c1", "stage_create_page", "ok1"),
			wireCall("c2", "stage_patch_page", dwStageArgs("content", 6000)), dwRes("c2", "stage_patch_page", "ok2"),
			wireCall("r1", "raw_get", `{"path":"raw/a.md"}`), dwRes("r1", "raw_get", "tiny"),
		)
		before := dwSnapshot(t, msgs)
		got := dwBound(msgs, 1)
		if args := dwArgsOf(t, got, "c1"); args["body"] != dwStub(6000) {
			t.Fatalf("nothing was stubbed — the pin is vacuous: %.40v…", args["body"])
		}
		if &got[0] == &msgs[0] {
			t.Errorf("a stubbing round returned the input slice itself, not a copy")
		}
		if after := dwSnapshot(t, msgs); after != before {
			t.Errorf("a stubbing round rewrote the input messages in place")
		}
	})

	t.Run("fake_streamer_requests", func(t *testing.T) { dwNoMutationLoop(t) })
}

// dwNoMutationLoop is TestBoundContextNoMutation's loop-driven half: the
// requests the fake streamer already holds.
func dwNoMutationLoop(t *testing.T) {
	t.Helper()
	ctl := dwLoop(t, 1_000_000)
	ctlReqs := ctl.fake.Requests()
	if len(ctlReqs) != 4 {
		t.Fatalf("control run made %d requests, want 4", len(ctlReqs))
	}
	// The budget is exactly what round 4's request needs once the staged page
	// text is a stub: round 3's request (the same plus nothing) is over it by
	// the stub's saving less the small read, so the stub happens in round 3.
	budget := wireEstimate(dwStubbed(t, ctlReqs[3].Messages, "call-s1"))

	run := dwLoop(t, budget)
	reqs := run.fake.Requests()
	if len(reqs) != 4 {
		t.Fatalf("sized run made %d requests, want 4", len(reqs))
	}

	// Rounds 1 and 2 were sent before anything was stubbed, and must still be
	// the control's bytes now that round 3 has stubbed — they were held by
	// reference all along.
	for i := 0; i < 2; i++ {
		if got, want := dwSnapshot(t, reqs[i].Messages), dwSnapshot(t, ctlReqs[i].Messages); got != want {
			t.Errorf("request %d (sent before the stub) was rewritten afterwards:\n got %.300s…\nwant %.300s…", i+1, got, want)
		}
	}
	if body, _ := dwArgsOf(t, reqs[1].Messages, "call-s1")["body"].(string); len(body) != dwBodyBytes {
		t.Errorf("request 2 carries %d bytes of the page body now, want the full %d it was sent with", len(body), dwBodyBytes)
	}

	// The stub itself landed, in round 3, and stayed for round 4: the request
	// is the control's with only the page text replaced.
	for i := 2; i < 4; i++ {
		want := dwSnapshot(t, dwStubbed(t, ctlReqs[i].Messages, "call-s1"))
		if got := dwSnapshot(t, reqs[i].Messages); got != want {
			t.Errorf("request %d is not the control's request with the page text stubbed:\n got %.300s…\nwant %.300s…", i+1, got, want)
		}
	}
	if args := dwArgsOf(t, reqs[2].Messages, "call-s1"); args["body"] != dwStub(dwBodyBytes) {
		t.Errorf("round 3's create call body = %.60v…, want the %d-byte stub", args["body"], dwBodyBytes)
	}

	// The evidence the stub made room for is whole.
	big := resultByCall(t, run.events, "call-w1")
	for i := 2; i < 4; i++ {
		if m := toolMsgByID(t, reqs[i].Messages, "call-w1"); m.Content != big {
			t.Errorf("request %d: the big wiki.get result was elided (%d bytes left of %d) though the stub made room", i+1, len(m.Content), len(big))
		}
	}

	// Records on disk are never touched: the staged call keeps its full args.
	sess, err := run.fx.store.Get(run.fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	full := dwCreateArgs(t, dwBodyBytes)
	seen := false
	for _, r := range sess.Records {
		if r.Tool == "stage.create_page" {
			seen = true
			if r.Args != full {
				t.Errorf("the session record's Args changed: %d bytes, want the original %d", len(r.Args), len(full))
			}
		}
	}
	if !seen {
		t.Fatalf("no stage.create_page record in the session")
	}

	// The log line and the trace event carry the same facts.
	log := readLog(t, run.logPath)
	if n := strings.Count(log, `msg="context elided"`); n != 1 {
		t.Errorf("want exactly one context-elided line (round 3's stub; round 4 fits), got %d:\n%s", n, log)
	}
	for _, want := range []string{"round=3", "messages=1", fmt.Sprintf("bytes=%d", dwBodyBytes)} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "context over budget") {
		t.Errorf("the stub reached the budget; want no over-budget warn:\n%s", log)
	}
	turn, err := trace.Load(run.traces, turnID(t, run.fx))
	if err != nil {
		t.Fatalf("trace.Load: %v", err)
	}
	if len(turn.Elisions) != 1 {
		t.Fatalf("elisions = %+v, want exactly one", turn.Elisions)
	}
	if el := turn.Elisions[0]; el.Round != 3 || el.Count != 1 || el.Bytes != dwBodyBytes {
		t.Errorf("elide event = %+v, want {round 3, count 1, bytes %d}", el, dwBodyBytes)
	}
}

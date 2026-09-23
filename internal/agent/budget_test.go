package agent

// budget_test.go pins 004 T0a's within-turn context bound (F.C1–F.C5).
// Every pin drives the real Loop — real registry, real stage engine, real
// session store over a private fixture copy; no fakes below the LLM seam
// (D-CS) — and asserts on the llm.Requests fakeStreamer recorded, the way
// the workflow's P1–P6 are written. The big wiki.get results the pins need
// are real tool results: newBudgetFixture seeds a page and calibrates it
// through the registry until wiki.get returns exactly 20000 bytes, because
// P2's pin names the literal F.C2 string "wiki.get result, 20000 bytes".

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
)

// bigResultBytes is the exact Result.Content size every big wiki.get result
// in these pins carries — P2's pin quotes the elision string as
// "wiki.get result, 20000 bytes", so the seeded page is calibrated to hit
// it exactly, not approximately.
const bigResultBytes = 20000

// bigWikiArgs is the argument payload the scripted rounds send to wiki_get.
const bigWikiArgs = `{"page":"big"}`

// wireEstimate re-derives F.C1's estimate independently of budget.go's
// requestTokens: the pins size their budgets from it, so if the production
// estimator ever drifts from the frozen formula the pins fail on their
// sizing instead of silently testing the wrong threshold.
func wireEstimate(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content) / 4
		total += len(m.ReasoningContent) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Arguments) / 4
		}
	}
	return total
}

// writeNamedBigPage writes wiki/concepts/<name>.md with a body of exactly
// n bytes. The body is multi-byte ("éa" repeats: 3 bytes per 2 runes)
// because T0b caps wiki.get at wikiGetMaxRunes — 16000 RUNES — while P2's
// frozen elision placeholder counts BYTES ("wiki.get result, 20000
// bytes"); an ASCII page calibrated to 20000 bytes is 20000 runes and
// would trip the cap, so the body runs 2 bytes per rune to stay well under
// it (004 seam G1). Serialize appends the body verbatim and neither 'é'
// nor 'a' is transformed by a JSON encoder, so len(wiki.get's
// Result.Content) stays linear in n and one calibration correction below
// lands exactly.
func writeNamedBigPage(t *testing.T, root, name string, n int) {
	t.Helper()
	const unit = "éa" // 3 bytes, 2 runes
	body := strings.Repeat(unit, n/len(unit))
	switch n % len(unit) {
	case 1:
		body += "x" // 1 byte, 1 rune
	case 2:
		body += "é" // 2 bytes, 1 rune
	}
	if len(body) != n {
		t.Fatalf("body construction: %d bytes, want %d", len(body), n)
	}
	page := "---\ntitle: " + name + "\ncreated: 2026-08-20\nupdated: 2026-08-20\ntype: concept" +
		"\ntags: [inference]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: high\n---\n\n# " + name + "\n\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(root, "wiki", "concepts", name+".md"), []byte(page), 0o644); err != nil {
		t.Fatalf("write %s.md: %v", name, err)
	}
}

// calibrateBigPage writes wiki/concepts/<name>.md and rewrites it until
// the real wiki.get — called through reg with {"page":"<name>"} — returns
// exactly resultBytes bytes: the serialize-measure-correct loop the
// fixture has always used, extracted so the A-004-2 pins can calibrate
// further distinct pages ("a", "b") to the same exact size. Serialize
// appends the body verbatim, so len(Content) is linear in the body length:
// one delta correction must land exactly. A third miss fails the test
// instead of papering over a format drift. The vault serves pages from an
// immutable snapshot (snapshot.go), so every rewrite is followed by
// Vault.Reload before the next calibration read — the same registry call
// the scripted rounds make. Returns the final body length and result.
func calibrateBigPage(t *testing.T, root string, reg *tools.Registry, e *stage.Engine, name string, resultBytes int) (int, string) {
	t.Helper()
	args := fmt.Sprintf(`{"page":%q}`, name)
	read := func() string {
		t.Helper()
		res, err := reg.Call(context.Background(), "wiki.get", json.RawMessage(args))
		if err != nil {
			t.Fatalf("wiki.get: %v", err)
		}
		if res.IsError {
			t.Fatalf("wiki.get %s failed: %s", name, res.Content)
		}
		return res.Content
	}

	writeNamedBigPage(t, root, name, 4096)
	bodyLen := 4096
	if err := e.Vault().Reload(); err != nil {
		t.Fatalf("Vault.Reload: %v", err)
	}
	got := read()
	for pass := 0; pass < 2 && len(got) != resultBytes; pass++ {
		bodyLen += resultBytes - len(got)
		writeNamedBigPage(t, root, name, bodyLen)
		if err := e.Vault().Reload(); err != nil {
			t.Fatalf("Vault.Reload: %v", err)
		}
		got = read()
	}
	if len(got) != resultBytes {
		t.Fatalf("wiki.get %s = %d bytes after calibration, want exactly %d — the page format drifted", name, len(got), resultBytes)
	}
	if strings.Contains(got, "[truncated:") {
		t.Fatalf("calibrated fixture result hit wiki.get's 16000-rune cap — the page must stay under it in runes while hitting %d bytes", resultBytes)
	}
	return bodyLen, got
}

// newBudgetFixture is newTestLoopFixture plus one synthetic page,
// wiki/concepts/big.md, calibrated until the real wiki.get returns exactly
// resultBytes bytes. It also returns the calibrated body length, so the
// wide fixtures below can clone big under further names.
func newBudgetFixture(t *testing.T, resultBytes int) (*testLoopFixture, string, int) {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("stage.OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	author := stage.Author{Kind: "agent", Model: "test-model"}
	reg := tools.NewRegistry(tools.Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author})

	bodyLen, got := calibrateBigPage(t, root, reg, e, "big", resultBytes)

	cs, err := e.OpenChangeset("budget test", author)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	store := NewFileSessions(root)
	if _, err := store.Create(cs.ID); err != nil {
		t.Fatalf("Create session: %v", err)
	}
	return &testLoopFixture{engine: e, reg: reg, store: store, csID: cs.ID}, got, bodyLen
}

// newBudgetLoop wires a Loop with cfg over newBudgetFixture and returns the
// fixture plus the calibrated 20000-byte page content.
func newBudgetLoop(t *testing.T, rounds [][]llm.Chunk, cfg LoopConfig) (*Loop, *testLoopFixture, *fakeStreamer, string) {
	t.Helper()
	fx, big, _ := newBudgetFixture(t, bigResultBytes)
	fake := &fakeStreamer{rounds: rounds}
	l := newLoop(fake, fx.reg, fx.store, fx.engine, cfg)
	return l, fx, fake, big
}

// widePageNames returns n distinct 3-char page basenames (aaz, abz, …).
// Three chars so every clone's front matter is exactly as wide as big's,
// and the cloned pages' wiki.get results stay the same size.
func widePageNames(n int) []string {
	names := make([]string, 0, n)
	for _, first := range []string{"a", "b", "c"} {
		for second := 'a'; second <= 'z' && len(names) < n; second++ {
			names = append(names, string(first)+string(second)+"z")
		}
	}
	if len(names) < n {
		panic(fmt.Sprintf("widePageNames: %d exceeds the 78 3-char names", n))
	}
	return names
}

// addBigPages clones the calibrated big page under the given names — same
// body length and title width, so each clone's wiki.get result is the same
// 20000 bytes — with one Vault.Reload at the end.
func addBigPages(t *testing.T, fx *testLoopFixture, names []string, bodyLen int) {
	t.Helper()
	root := fx.engine.Vault().Root()
	for _, name := range names {
		writeNamedBigPage(t, root, name, bodyLen)
	}
	if err := fx.engine.Vault().Reload(); err != nil {
		t.Fatalf("Vault.Reload: %v", err)
	}
}

// newWideLoop is newBudgetLoop plus distinct page clones under names: the
// A-004-2 pressure probes script many elidable results, and a shared
// signature is ONE read under second-chance pinning, so every scripted
// call needs its own page.
func newWideLoop(t *testing.T, rounds [][]llm.Chunk, cfg LoopConfig, names []string) (*Loop, *testLoopFixture, *fakeStreamer) {
	t.Helper()
	fx, _, bodyLen := newBudgetFixture(t, bigResultBytes)
	addBigPages(t, fx, names, bodyLen)
	fake := &fakeStreamer{rounds: rounds}
	l := newLoop(fake, fx.reg, fx.store, fx.engine, cfg)
	return l, fx, fake
}

// newRereadFixture is newBudgetFixture plus two further calibrated pages,
// wiki/concepts/a.md and b.md, each returning the same exact result size
// as big — P7 scripts the live livelock's alternating re-reads of two
// distinct big reads (A-004-2), and its budget sizing needs both results
// known-big.
func newRereadFixture(t *testing.T) *testLoopFixture {
	t.Helper()
	fx, _, _ := newBudgetFixture(t, bigResultBytes)
	root := fx.engine.Vault().Root()
	for _, name := range []string{"a", "b"} {
		_, _ = calibrateBigPage(t, root, fx.reg, fx.engine, name, bigResultBytes)
	}
	return fx
}

// bigWikiRounds scripts the three-round shape P1/P2/P4/P5 pin: two rounds
// each calling wiki_get on the big page (a 20000-byte Result.Content both
// times), then a prose stop. The wire spelling "wiki_get" is what
// Definitions advertised (D-CY) — the provider echoes it back.
func bigWikiRounds() [][]llm.Chunk {
	return [][]llm.Chunk{
		{toolCallChunk("call-w1", "wiki_get", bigWikiArgs), {Finish: "tool_calls"}},
		{toolCallChunk("call-w2", "wiki_get", bigWikiArgs), {Finish: "tool_calls"}},
		{{Text: "done"}, {Finish: "stop"}},
	}
}

// runBigTurn Sends one three-round turn and returns the recorded requests,
// the drained events and the fixture. The events matter as much as the
// requests: ToolResEv carries each result's original bytes independently of
// any wire mutation, so every "intact" comparison below reads its expected
// value from the same run's events, never from the request under test.
func runBigTurn(t *testing.T, cfg LoopConfig) (*fakeStreamer, []Event, *testLoopFixture) {
	t.Helper()
	l, fx, fake, _ := newBudgetLoop(t, bigWikiRounds(), cfg)
	out := make(chan Event, 256)
	if err := l.Send(context.Background(), fx.csID, "read the big page", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return fake, drain(out), fx
}

// resultByCall extracts one ToolResEv's content by its tool-call id.
func resultByCall(t *testing.T, events []Event, id string) string {
	t.Helper()
	for _, ev := range events {
		if res, ok := ev.(ToolResEv); ok && res.ID == id {
			return res.Content
		}
	}
	t.Fatalf("no ToolResEv for call %q in %#v", id, events)
	return ""
}

// toolMsgByID returns the tool-result message with ToolCallID id.
func toolMsgByID(t *testing.T, msgs []llm.Message, id string) llm.Message {
	t.Helper()
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID == id {
			return m
		}
	}
	t.Fatalf("no tool-result message with tool_call_id %q in %+v", id, msgs)
	return llm.Message{}
}

// wireBytes marshals v the way the outbound request body would, so
// byte-identical requests are compared as bytes, not structurally.
func wireBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// scriptedBigCall rebuilds the literal llm.ToolCall the script streamed for
// one big wiki_get call.
func scriptedBigCall(id string) llm.ToolCall {
	return *toolCallChunk(id, "wiki_get", bigWikiArgs).ToolCall
}

// TestBudgetUnderBudgetSendsUnchanged is P1: at budget 1 000 000 a
// three-round script with two 20000-byte wiki.get results must send
// requests 2 and 3 byte-identical to a run with no budget logic at all —
// compared against the literal messages the script produced, not against
// the run itself. The baseline is independent of the bound: request 1 is
// the loop's own Build output (nothing in the turn region exists yet, so
// even a broken bound could only have corrupted it by touching history,
// which P6 pins separately), and each later request is that prefix plus the
// round's one assistant message and its tool result, with the result bytes
// taken from the ToolResEv events the wire mutation cannot reach.
func TestBudgetUnderBudgetSendsUnchanged(t *testing.T) {
	logPath := installFileLog(t)
	fake, events, _ := runBigTurn(t, LoopConfig{ContextTokens: 1000000})
	reqs := fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("Stream called %d times, want 3", len(reqs))
	}
	if len(reqs[0].Messages) != 4 {
		t.Fatalf("round 1 request carries %d messages, want 4 (3 system + user) — baseline poisoned", len(reqs[0].Messages))
	}

	res1 := resultByCall(t, events, "call-w1")
	res2 := resultByCall(t, events, "call-w2")

	exp2 := append(append([]llm.Message{}, reqs[0].Messages...),
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{scriptedBigCall("call-w1")}},
		llm.Message{Role: "tool", ToolCallID: "call-w1", Name: "wiki_get", Content: res1},
	)
	exp3 := append(append([]llm.Message{}, exp2...),
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{scriptedBigCall("call-w2")}},
		llm.Message{Role: "tool", ToolCallID: "call-w2", Name: "wiki_get", Content: res2},
	)

	for i, exp := range [][]llm.Message{exp2, exp3} {
		got, want := wireBytes(t, reqs[i+1].Messages), wireBytes(t, exp)
		if !bytes.Equal(got, want) {
			t.Errorf("round %d's request is not byte-identical to the literal script messages:\n got %s\nwant %s", i+2, got, want)
		}
	}

	if log := readLog(t, logPath); strings.Contains(log, "context elided") {
		t.Errorf("budget 1 000 000 logged an elision; F.C1 wants an under-budget request sent unchanged:\n%s", log)
	}
}

// TestBudgetElidesOldestEligibleResultOnly is P2: the budget is sized so
// round 3's request sits over it by less than one result, so exactly one
// elision — round 1's wiki.get result, the oldest eligible one — must
// reach it. That result is replaced with the exact F.C2 string naming
// "wiki.get result, 20000 bytes"; round 2's result (the most recent round,
// skipped per F.C2a) stays intact, and the elided message keeps its
// ToolCallID and Role so the wire shape survives.
func TestBudgetElidesOldestEligibleResultOnly(t *testing.T) {
	logPath := installFileLog(t)

	// Size off a control run at 1 000 000: budget = round 3's over-budget
	// estimate minus 2000 tokens, well inside the ~5000 tokens one
	// 20000-byte elision buys, so one elision must reach the budget.
	ctlFake, _, _ := runBigTurn(t, LoopConfig{ContextTokens: 1000000})
	budget := wireEstimate(ctlFake.Requests()[2].Messages) - 2000

	fake, events, _ := runBigTurn(t, LoopConfig{ContextTokens: budget})
	reqs := fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("Stream called %d times, want 3", len(reqs))
	}
	req3 := reqs[2].Messages
	if len(req3) != len(ctlFake.Requests()[2].Messages) {
		t.Fatalf("round 3 request carries %d messages, control carried %d — elision must replace content, never drop messages (F.C2)",
			len(req3), len(ctlFake.Requests()[2].Messages))
	}

	res1 := resultByCall(t, events, "call-w1")
	if len(res1) != bigResultBytes {
		t.Fatalf("calibration broke: round-1 result is %d bytes, want exactly %d", len(res1), bigResultBytes)
	}
	wantElided := fmt.Sprintf("[elided to fit the context budget: wiki.get result, %d bytes — call wiki.get again if you still need it]", len(res1))

	elided := toolMsgByID(t, req3, "call-w1")
	if elided.Content != wantElided {
		t.Errorf("round-1 result Content = %q, want the exact F.C2 string %q", elided.Content, wantElided)
	}
	if elided.Role != "tool" || elided.ToolCallID != "call-w1" || elided.Name != "wiki_get" {
		t.Errorf("elided message lost its wire identity: role=%q tool_call_id=%q name=%q, want tool/call-w1/wiki_get", elided.Role, elided.ToolCallID, elided.Name)
	}

	recent := toolMsgByID(t, req3, "call-w2")
	if want := resultByCall(t, events, "call-w2"); recent.Content != want {
		t.Errorf("round-2 (most recent) result was touched: got %d bytes, want the original %d", len(recent.Content), len(want))
	}

	if est := wireEstimate(req3); est > budget {
		t.Errorf("round 3's request still estimates %d over the %d-token budget after elision", est-budget, budget)
	}

	log := readLog(t, logPath)
	for _, want := range []string{
		`msg="context elided"`,
		"round=3",
		"messages=1",
		fmt.Sprintf("bytes=%d", len(res1)),
		fmt.Sprintf("budget=%d", budget),
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q (F.C5):\n%s", want, log)
		}
	}
	if strings.Contains(log, "context over budget") {
		t.Errorf("elision reached the budget; want no F.C3 warn:\n%s", log)
	}
}

// TestBudgetNeverElidesStagedResults is P3: a stage.create_page result in
// round 1 is never elided, even when protecting it leaves the request over
// budget — the budget is sized past what the one eligible elision (round
// 1's wiki.get result) can recover, so F.C3's warn must fire and the send
// must happen anyway. The staged result is also walked over and skipped
// mid-walk, not merely absent: round 1 stages the page BEFORE the big
// wiki.get, so the walk visits it first and must pass it by.
func TestBudgetNeverElidesStagedResults(t *testing.T) {
	logPath := installFileLog(t)

	rounds := [][]llm.Chunk{
		{
			toolCallChunk("call-s1", "stage_create_page", wireCreatePageArgs),
			toolCallChunk("call-w1", "wiki_get", bigWikiArgs),
			{Finish: "tool_calls"},
		},
		{toolCallChunk("call-w2", "wiki_get", bigWikiArgs), {Finish: "tool_calls"}},
		{{Text: "done"}, {Finish: "stop"}},
	}

	run := func(cfg LoopConfig) (*fakeStreamer, []Event) {
		t.Helper()
		l, fx, fake, _ := newBudgetLoop(t, rounds, cfg)
		out := make(chan Event, 256)
		if err := l.Send(context.Background(), fx.csID, "stage a page and read the big one", out); err != nil {
			t.Fatalf("Send: %v", err)
		}
		return fake, drain(out)
	}

	ctlFake, _ := run(LoopConfig{ContextTokens: 1000000})
	// 6000 tokens in exceeds the ~4973 one 20000-byte elision recovers, so
	// after the only eligible result is gone the request is still over.
	budget := wireEstimate(ctlFake.Requests()[2].Messages) - 6000

	fake, events := run(LoopConfig{ContextTokens: budget})
	reqs := fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("Stream called %d times, want 3", len(reqs))
	}
	req3 := reqs[2].Messages
	if len(req3) != len(ctlFake.Requests()[2].Messages) {
		t.Fatalf("round 3 request carries %d messages, control carried %d", len(req3), len(ctlFake.Requests()[2].Messages))
	}

	staged := toolMsgByID(t, req3, "call-s1")
	if want := resultByCall(t, events, "call-s1"); staged.Content != want {
		t.Errorf("stage.create_page result was elided: got %q, want the original %q", staged.Content, want)
	}
	if elided := toolMsgByID(t, req3, "call-w1"); !strings.HasPrefix(elided.Content, "[elided to fit the context budget:") {
		t.Errorf("round-1 wiki.get result (the one eligible message) was not elided: %q", elided.Content)
	}
	if recent := toolMsgByID(t, req3, "call-w2"); recent.Content != resultByCall(t, events, "call-w2") {
		t.Errorf("round-2 (most recent) result was touched: %q", recent.Content)
	}

	log := readLog(t, logPath)
	for _, want := range []string{`msg="context over budget"`, "round=3"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q (F.C3):\n%s", want, log)
		}
	}
}

// TestBudgetNeverElidesMostRecentRound is P4. Two pressure shapes in one
// turn: with the budget set ten tokens under round 2's estimate, round 2's
// request — whose only tool results ARE the most recent round's — is over
// with nothing eligible at all, and must still go out byte-identical to the
// control run's; round 3's request then has an eligible older result, but
// even after eliding it the request stays over, and round 2's result — the
// bytes that alone keep it over — must still be intact.
func TestBudgetNeverElidesMostRecentRound(t *testing.T) {
	logPath := installFileLog(t)

	ctlFake, _, _ := runBigTurn(t, LoopConfig{ContextTokens: 1000000})
	budget := wireEstimate(ctlFake.Requests()[1].Messages) - 10

	fake, events, _ := runBigTurn(t, LoopConfig{ContextTokens: budget})
	reqs := fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("Stream called %d times, want 3", len(reqs))
	}

	// Round 2: over budget, nothing eligible, sent unchanged (F.C3).
	if got, want := wireBytes(t, reqs[1].Messages), wireBytes(t, ctlFake.Requests()[1].Messages); !bytes.Equal(got, want) {
		t.Errorf("round 2's request changed while its only results were the most recent round's:\n got %s\nwant %s", got, want)
	}

	// Round 3: the older result is elided (and not enough), the most
	// recent one is never touched.
	req3 := reqs[2].Messages
	if elided := toolMsgByID(t, req3, "call-w1"); !strings.HasPrefix(elided.Content, "[elided to fit the context budget:") {
		t.Errorf("round-1 result should have been elided at round 3: %q", elided.Content)
	}
	if want := resultByCall(t, events, "call-w2"); toolMsgByID(t, req3, "call-w2").Content != want {
		t.Errorf("round-2 (most recent) result was elided though it alone kept the request over budget")
	}

	log := readLog(t, logPath)
	if n := strings.Count(log, `msg="context over budget"`); n != 2 {
		t.Errorf("want exactly 2 context-over-budget warns (rounds 2 and 3), got %d:\n%s", n, log)
	}
}

// TestBudgetLeavesRecordsByteIdentical is P5: a P2-shaped turn (its budget
// sized exactly as P2 sizes it, so an elision genuinely happens) must leave
// session Records equal to the same script run at budget 1 000 000 (F.C4) —
// compared field by field, timestamps apart, since TS is wall-clock.
func TestBudgetLeavesRecordsByteIdentical(t *testing.T) {
	ctlFake, _, ctlFx := runBigTurn(t, LoopConfig{ContextTokens: 1000000})
	budget := wireEstimate(ctlFake.Requests()[2].Messages) - 2000

	// The sized run must really elide, or the comparison below is vacuous.
	sizedFake, _, sizedFx := runBigTurn(t, LoopConfig{ContextTokens: budget})
	req3 := sizedFake.Requests()[2].Messages
	if elided := toolMsgByID(t, req3, "call-w1"); !strings.HasPrefix(elided.Content, "[elided to fit the context budget:") {
		t.Fatalf("sized run did not elide — pin is vacuous: %q", elided.Content)
	}

	ctlSess, err := ctlFx.store.Get(ctlFx.csID)
	if err != nil {
		t.Fatalf("Get control session: %v", err)
	}
	sizedSess, err := sizedFx.store.Get(sizedFx.csID)
	if err != nil {
		t.Fatalf("Get sized session: %v", err)
	}
	if len(ctlSess.Records) != len(sizedSess.Records) {
		t.Fatalf("records: sized run wrote %d, control wrote %d", len(sizedSess.Records), len(ctlSess.Records))
	}
	for i := range ctlSess.Records {
		a, b := ctlSess.Records[i], sizedSess.Records[i]
		a.TS, b.TS = time.Time{}, time.Time{} // wall-clock, not content
		if a != b {
			t.Errorf("record %d differs after elision:\n control %+v\n sized  %+v", i, a, b)
		}
	}
}

// TestBudgetNeverElidesPriorHistory is P6: one session, two turns — turn 1
// reads the big page, so turn 2's request carries it as rendered history.
// The budget is far below the fixed parts alone, so if the bound could
// touch history at all, that 20000-byte history message would be its first
// victim; it must arrive untouched, and nothing in the request may carry
// the elision marker.
//
// Turn 2 runs TWO rounds, and the second round's assertions are the load-
// bearing ones: in any turn's ROUND 1 nothing is elidable anyway (the most
// recent round is undefined, so every result is protected whatever the
// turn boundary does), but by round 2 the walk has a live region — and a
// bound that started at index 0 instead of the turn's first message would
// elide the prior-turn history messages OLDEST FIRST, before anything in
// the current turn. A single-round turn 2 cannot tell those apart.
func TestBudgetNeverElidesPriorHistory(t *testing.T) {
	turn2 := [][]llm.Chunk{
		{toolCallChunk("call-w3", "wiki_get", bigWikiArgs), {Finish: "tool_calls"}},
		{{Text: "second turn"}, {Finish: "stop"}},
	}
	rounds := append(append([][]llm.Chunk{}, bigWikiRounds()...), turn2...)
	l, fx, fake, big := newBudgetLoop(t, rounds, LoopConfig{ContextTokens: 2000})

	out := make(chan Event, 256)
	if err := l.Send(context.Background(), fx.csID, "read the big page", out); err != nil {
		t.Fatalf("Send turn 1: %v", err)
	}
	drain(out)

	out2 := make(chan Event, 256)
	if err := l.Send(context.Background(), fx.csID, "and once more, briefly", out2); err != nil {
		t.Fatalf("Send turn 2: %v", err)
	}
	drain(out2)

	reqs := fake.Requests()
	if len(reqs) != 5 {
		t.Fatalf("Stream called %d times, want 5 (3 rounds + turn 2's two rounds)", len(reqs))
	}

	// Turn 2, round 1: nothing is elidable yet — history must be whole.
	history := 0
	for _, m := range reqs[3].Messages {
		if strings.Contains(m.Content, "wiki.get(") {
			history++
			if !strings.Contains(m.Content, big) {
				t.Errorf("prior-turn history message was truncated or elided: %d bytes, want it to still carry the full %d-byte result", len(m.Content), len(big))
			}
		}
		if strings.Contains(m.Content, "[elided to fit the context budget") {
			t.Errorf("elision marker reached prior-session history: %+v", m)
		}
	}
	if history != 2 {
		t.Errorf("turn 2's request carries %d rendered wiki.get history messages, want 2 (one per turn-1 call)", history)
	}
	if last := reqs[3].Messages[len(reqs[3].Messages)-1]; last.Role != "user" || last.Content != "and once more, briefly" {
		t.Errorf("turn 2's own user message = %+v, want it last and untouched", last)
	}

	// Turn 2, round 2: the walk now has a live region. On the wire, round
	// 1 of turn 2 is the MOST RECENT round — its result is protected like
	// any other (F.C2a), and the region before it inside this turn is
	// empty, so nothing is elidable at all. The load-bearing assertion is
	// that nothing OUTSIDE this turn was touched: a bound that started at
	// index 0 would walk the prior-turn history messages oldest-first and
	// replace them with markers before ever reaching this turn.
	req5 := reqs[4].Messages
	history = 0
	sawTurn2Result := false
	for _, m := range req5 {
		if m.Role == "tool" && m.ToolCallID == "call-w3" {
			sawTurn2Result = true
			if m.Content != big {
				t.Errorf("turn 2's own round-1 result (the most recent round) was elided: %q", m.Content)
			}
			continue
		}
		if strings.Contains(m.Content, "[elided to fit the context budget") {
			t.Errorf("elision marker reached a message outside this turn's own rounds: %+v", m)
		}
		if strings.Contains(m.Content, "wiki.get(") {
			history++
			if !strings.Contains(m.Content, big) {
				t.Errorf("prior-turn history message was elided once the turn had a second round: %q", m.Content)
			}
		}
	}
	if !sawTurn2Result {
		t.Fatalf("turn 2 round 2's request lost the call-w3 tool result entirely")
	}
	if history != 2 {
		t.Errorf("turn 2 round 2's request carries %d rendered wiki.get history messages, want 2", history)
	}
}

// ---- A-004-2 second-chance pinning (live-found livelock repair) ----

// pinSignature is the test-side identity of one distinct read under
// A-004-2: the canonical tool name plus the call's arguments after
// json.Compact — the exact frozen rule — falling back to the raw string
// when compacting fails. Deliberately re-derived here instead of read back
// from production's own signature function: if the implementation's
// signature ever drifted from the frozen rule, this test must fail, not
// silently agree.
func pinSignature(wireName, args string) string {
	canonical := tools.CanonicalName(wireName)
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(args)); err != nil {
		return canonical + "\x00" + args
	}
	return canonical + "\x00" + buf.String()
}

// newRereadTurn scripts the live defect's shape (A-004-2, acceptance step
// 4): round 1 reads two big pages, then rounds 2–7 the model re-requests,
// alternating, whatever was just elided — six re-read rounds, page a's
// third read re-spaced (`{ "page": "a" }`) so the whitespace case rides
// along — and only then answers. Every call id is mapped to its argument
// payload, so assertions below can attribute each wire placeholder to the
// distinct read it stands for.
func newRereadTurn() ([][]llm.Chunk, map[string]string) {
	rounds := [][]llm.Chunk{
		{
			toolCallChunk("call-a1", "wiki_get", `{"page":"a"}`),
			toolCallChunk("call-b1", "wiki_get", `{"page":"b"}`),
			{Finish: "tool_calls"},
		},
		{toolCallChunk("call-a2", "wiki_get", `{"page":"a"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-b2", "wiki_get", `{"page":"b"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-a3", "wiki_get", `{ "page": "a" }`), {Finish: "tool_calls"}},
		{toolCallChunk("call-b3", "wiki_get", `{"page":"b"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-a4", "wiki_get", `{"page":"a"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-b4", "wiki_get", `{"page":"b"}`), {Finish: "tool_calls"}},
		{{Text: "I have both pages now"}, {Finish: "stop"}},
	}
	script := map[string]string{
		"call-a1": `{"page":"a"}`,
		"call-b1": `{"page":"b"}`,
		"call-a2": `{"page":"a"}`,
		"call-b2": `{"page":"b"}`,
		"call-a3": `{ "page": "a" }`,
		"call-b3": `{"page":"b"}`,
		"call-a4": `{"page":"a"}`,
		"call-b4": `{"page":"b"}`,
	}
	return rounds, script
}

// runRereadTurn Sends one scripted turn over a fresh newRereadFixture at
// cfg and returns the recorded requests and drained events.
func runRereadTurn(t *testing.T, rounds [][]llm.Chunk, cfg LoopConfig) (*fakeStreamer, []Event) {
	t.Helper()
	fx := newRereadFixture(t)
	fake := &fakeStreamer{rounds: rounds}
	l := newLoop(fake, fx.reg, fx.store, fx.engine, cfg)
	out := make(chan Event, 512)
	if err := l.Send(context.Background(), fx.csID, "read a and b until they elide", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return fake, drain(out)
}

// assertStopTurn asserts the turn ended the scripted way: exactly one
// terminal DoneEv{reason, rounds} and no ErrorEv anywhere.
func assertStopTurn(t *testing.T, events []Event, wantRounds int) {
	t.Helper()
	dones := 0
	for _, ev := range events {
		switch e := ev.(type) {
		case DoneEv:
			dones++
			if e.Reason != "stop" || e.Rounds != wantRounds {
				t.Errorf("DoneEv = {reason %q rounds %d}, want {stop %d}", e.Reason, e.Rounds, wantRounds)
			}
		case ErrorEv:
			t.Errorf("unexpected ErrorEv: %v", e.Err)
		}
	}
	if dones != 1 {
		t.Errorf("want exactly one DoneEv, got %d", dones)
	}
}

// TestBudgetPinsRereadOfElidedCall is P7 (A-004-2): the live livelock —
// each round the model re-requests what the last round just elided, the
// placeholder's own invitation — must be unable to form. At a budget that
// fits roughly ONE big result, every round from 2 on is over budget, so
// the walk has real work every round. Assertions: across ALL requests,
// each distinct read (canonical tool + json.Compact'ed arguments) is
// elided on at most one tool call; every re-read's result is intact in
// every request that carries it — including the re-spaced-JSON read,
// which is the SAME read as its compact twin; and the turn still ends
// with DoneEv{stop}. The live run this repair answers ended
// reason=max_rounds after 24 rounds of exactly this shape, never answering.
func TestBudgetPinsRereadOfElidedCall(t *testing.T) {
	rounds, script := newRereadTurn()

	// Size off a control run at 1 000 000: budget = the fixed parts (round
	// 1's request) plus a little over ONE big result.
	ctlFake, _ := runRereadTurn(t, rounds, LoopConfig{ContextTokens: 1000000})
	base := wireEstimate(ctlFake.Requests()[0].Messages)
	budget := base + 5500

	fake, events := runRereadTurn(t, rounds, LoopConfig{ContextTokens: budget})
	reqs := fake.Requests()
	if len(reqs) != 8 {
		t.Fatalf("Stream called %d times, want 8", len(reqs))
	}
	assertStopTurn(t, events, 8)

	originals := map[string]string{}
	for _, ev := range events {
		if res, ok := ev.(ToolResEv); ok {
			originals[res.ID] = res.Content
		}
	}
	rereads := []string{"call-a2", "call-b2", "call-a3", "call-b3", "call-a4", "call-b4"}

	// Per request: the wire must stay valid (checkWireShape), every
	// placeholder is attributed to its read's signature, and every
	// re-read must arrive intact.
	elidedReads := map[string]map[string]bool{}
	elidedAny := false
	for k, req := range reqs {
		for _, i := range checkWireShape(t, req, k+1, originals) {
			elidedAny = true
			id := req.Messages[i].ToolCallID
			sig := pinSignature("wiki_get", script[id])
			if elidedReads[sig] == nil {
				elidedReads[sig] = map[string]bool{}
			}
			elidedReads[sig][id] = true
		}
		for _, m := range req.Messages {
			if m.Role != "tool" {
				continue
			}
			for _, id := range rereads {
				if m.ToolCallID == id && m.Content != originals[id] {
					t.Errorf("round %d: re-read %q (args %q) is not intact on the wire: %q", k+1, id, script[id], m.Content)
				}
			}
		}
	}
	if !elidedAny {
		t.Fatalf("nothing was ever elided (base %d, budget %d) — sizing broke, pin vacuous", base, budget)
	}

	// THE pin: one elision per distinct read, whole turn. The placeholder
	// persists on the call it elided (that is F.C2's own rule); what may
	// never happen is a SECOND call — a re-read — losing its result too.
	sigA := pinSignature("wiki_get", `{"page":"a"}`)
	sigB := pinSignature("wiki_get", `{"page":"b"}`)
	if ids := elidedReads[sigA]; len(ids) != 1 || !ids["call-a1"] {
		t.Errorf("page a was elided on calls %v — A-004-2 wants exactly {call-a1}: the re-reads (call-a2, call-a3 with re-spaced JSON, call-a4) are the same read and must stay intact", ids)
	}
	if ids := elidedReads[sigB]; len(ids) != 1 || !ids["call-b1"] {
		t.Errorf("page b was elided on calls %v — A-004-2 wants exactly {call-b1}", ids)
	}
}

// TestBudgetPinRereadMatchesAcrossJSONSpacing is P7's focused whitespace
// case: the re-request differs from the elided call ONLY in JSON spacing
// (`{"page":"a"}` vs `{ "page":  "a" }`). Three tool rounds, so that at
// round 4's request the spaced re-read is no longer the most recent
// round's result (F.C2a would shield it there and the pin would be
// vacuous) and the walk genuinely reaches it. The budget sits under one
// result, so eliding round 1's result is never enough: the pin must
// recognize the re-read as the same read across the spacing and skip it —
// leaving the request over budget with F.C3's warn — rather than elide it
// and re-create the cycle.
func TestBudgetPinRereadMatchesAcrossJSONSpacing(t *testing.T) {
	logPath := installFileLog(t)
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "wiki_get", `{"page":"a"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-2", "wiki_get", `{ "page":  "a" }`), {Finish: "tool_calls"}},
		{toolCallChunk("call-3", "wiki_get", `{"page":"b"}`), {Finish: "tool_calls"}},
		{{Text: "got it"}, {Finish: "stop"}},
	}

	ctlFake, _ := runRereadTurn(t, rounds, LoopConfig{ContextTokens: 1000000})
	base := wireEstimate(ctlFake.Requests()[0].Messages)
	budget := base + 3000 // under ONE result: the walk must reach the re-read

	fake, events := runRereadTurn(t, rounds, LoopConfig{ContextTokens: budget})
	reqs := fake.Requests()
	if len(reqs) != 4 {
		t.Fatalf("Stream called %d times, want 4", len(reqs))
	}
	assertStopTurn(t, events, 4)

	originals := map[string]string{}
	for _, ev := range events {
		if res, ok := ev.(ToolResEv); ok {
			originals[res.ID] = res.Content
		}
	}

	// Non-vacuous: round 1's result really was elided at round 3.
	if msg := toolMsgByID(t, reqs[2].Messages, "call-1"); msg.Content != probePlaceholder("wiki.get", len(originals["call-1"])) {
		t.Fatalf("call-1's result was not elided — sizing broke, pin vacuous: %q", msg.Content)
	}
	// THE pin: the spaced re-read is the same read — intact at round 4,
	// where the walk visits it and pinning is the only thing protecting it.
	if msg := toolMsgByID(t, reqs[3].Messages, "call-2"); msg.Content != originals["call-2"] {
		t.Errorf("re-read with re-spaced JSON ({ \"page\":  \"a\" }) was elided too — the cycle is back: %q", msg.Content)
	}

	log := readLog(t, logPath)
	if n := strings.Count(log, `msg="context elided"`); n != 1 {
		t.Errorf("want exactly 1 elision log line (round 3, one message), got %d:\n%s", n, log)
	}
	if !strings.Contains(log, `msg="context over budget"`) {
		t.Errorf("skipping the pinned re-read must leave round 4 over budget with an F.C3 warn:\n%s", log)
	}
}

// ---- fresh-eyes review probes (004 T0a review, 2026-09-23) ----

// probePlaceholder builds the exact F.C2 placeholder for a tool result whose
// original content was n bytes under the canonical dotted name canonical.
func probePlaceholder(canonical string, n int) string {
	return fmt.Sprintf(elidedResultFormat, canonical, n, canonical)
}

// probeCanonical returns the canonical dotted name a placeholder carries for
// a tool-result message with wire Name name — the same CanonicalName call
// budget.go makes on the message's Name field.
func probeCanonical(name string) string { return tools.CanonicalName(name) }

// checkWireShape asserts one recorded request's wire validity (the probe-1
// contract): every assistant ToolCall id is answered by exactly one tool
// message that appears after it, in call order; no tool message content is
// empty (an assistant message's Content may legally be empty when the round
// streamed only tool calls); and every elided message keeps its wire
// identity. originals maps tool-call id → the result's original bytes, read
// from the same run's ToolResEv events. It returns the indices of elided
// (placeholder) tool messages, oldest first.
func checkWireShape(t *testing.T, req llm.Request, round int, originals map[string]string) []int {
	t.Helper()
	pending := []string{} // tool-call ids awaiting their result, in order
	answered := map[string]int{}
	var elidedIdx []int
	for i, m := range req.Messages {
		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				pending = append(pending, tc.ID)
			}
		case "tool":
			if len(pending) == 0 {
				t.Fatalf("round %d: tool message for %q at index %d with no unanswered tool call before it", round, m.ToolCallID, i)
			}
			if got := pending[0]; m.ToolCallID != got {
				t.Fatalf("round %d: tool message at index %d answers %q, want %q (results out of call order)", round, i, m.ToolCallID, got)
			}
			pending = pending[1:]
			answered[m.ToolCallID]++
			if answered[m.ToolCallID] > 1 {
				t.Fatalf("round %d: tool call %q answered by %d tool messages, want exactly 1", round, m.ToolCallID, answered[m.ToolCallID])
			}
			if m.Content == "" {
				t.Errorf("round %d: tool message for %q has empty content at index %d", round, m.ToolCallID, i)
			}
			orig, ok := originals[m.ToolCallID]
			if !ok {
				t.Fatalf("round %d: no original result recorded for call %q", round, m.ToolCallID)
			}
			canonical := probeCanonical(m.Name)
			switch m.Content {
			case orig:
				// intact
			case probePlaceholder(canonical, len(orig)):
				elidedIdx = append(elidedIdx, i)
				if m.Role != "tool" {
					t.Errorf("round %d: elided message at %d lost its role: %q", round, i, m.Role)
				}
				if canonical == "wiki.get" && strings.Contains(m.Content, "wiki_get") {
					t.Errorf("round %d: placeholder at %d carries the wire spelling: %q", round, i, m.Content)
				}
			default:
				// Neither original nor the exact single-elision placeholder —
				// a placeholder of a placeholder (double elision) lands here,
				// because re-eliding would name len(placeholder), not len(orig).
				t.Errorf("round %d: tool message for %q at index %d is neither the original (%d bytes) nor its exact F.C2 placeholder: %q", round, m.ToolCallID, i, len(orig), m.Content)
			}
		}
	}
	if len(pending) > 0 {
		t.Fatalf("round %d: tool call(s) %v never answered by a tool message", round, pending)
	}
	return elidedIdx
}

// TestProbeWireHoldsAcrossMaxRounds is probe 1: the full MaxToolRounds cap —
// 24 rounds, each with two parallel 20 KB wiki.get calls, under a budget
// sized so every round from the third on must elide — must keep every
// recorded request wire-valid: pairing, order, non-empty contents, no double
// elision, canonical names in placeholders. It also pins F.C2's early stop
// ("stopping at the first point the estimate is ≤ budget"): in any round
// whose elisions DID reach the budget, restoring all but the last elided
// message must push the estimate back over — otherwise the walk elided more
// than it had to.
func TestProbeWireHoldsAcrossMaxRounds(t *testing.T) {
	logPath := installFileLog(t)

	// Control run sizes the budget off the real fixed parts (its round-1
	// request is the same Build output the sized run's round 1 carries).
	ctlFake, _, _ := runBigTurn(t, LoopConfig{ContextTokens: 1000000})
	base := wireEstimate(ctlFake.Requests()[0].Messages)
	budget := base + 18000 // per-round fresh pressure is 2×~5000 tokens

	// Two fresh reads per round over 48 DISTINCT pages: under A-004-2 a
	// repeated page is one read, elidable once — a same-page script would
	// pin every round-4+ result and starve the walk this probe exists to
	// observe (its purpose is wire-shape validity under max-rounds
	// pressure, and pressure means fresh reads).
	names := widePageNames(48)
	rounds := make([][]llm.Chunk, 24)
	for r := range rounds {
		id1, id2 := fmt.Sprintf("call-w%02da", r+1), fmt.Sprintf("call-w%02db", r+1)
		rounds[r] = []llm.Chunk{
			toolCallChunk(id1, "wiki_get", fmt.Sprintf(`{"page":%q}`, names[2*r])),
			toolCallChunk(id2, "wiki_get", fmt.Sprintf(`{"page":%q}`, names[2*r+1])),
			{Finish: "tool_calls"},
		}
	}

	l, fx, fake := newWideLoop(t, rounds, LoopConfig{ContextTokens: budget}, names)
	out := make(chan Event, 512)
	if err := l.Send(context.Background(), fx.csID, "read the big page, a lot", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	reqs := fake.Requests()
	if len(reqs) != 24 {
		t.Fatalf("Stream called %d times, want 24", len(reqs))
	}

	// Every round must have elided something from round 3 on, or the
	// sizing failed and the pins below are vacuous.
	elidedAny := false

	for k, req := range reqs {
		round := k + 1
		// Elision replaces content, never drops messages (F.C2): every
		// round appends exactly one assistant message plus one tool result
		// per call — three messages per scripted round.
		if got, want := len(req.Messages), len(reqs[0].Messages)+3*k; got != want {
			t.Fatalf("round %d: request carries %d messages, want %d — elision must never drop messages", round, got, want)
		}

		originals := map[string]string{}
		for _, ev := range events {
			if res, ok := ev.(ToolResEv); ok {
				originals[res.ID] = res.Content
			}
		}
		elidedIdx := checkWireShape(t, req, round, originals)

		// F.C2a: the most recent round's results are never elided. The
		// last assistant message in the request is the round just
		// completed; everything after it is its results.
		for i := len(req.Messages) - 1; i >= 0; i-- {
			m := req.Messages[i]
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					msg := toolMsgByID(t, req.Messages, tc.ID)
					if msg.Content != originals[tc.ID] {
						t.Errorf("round %d: most recent round's result for %q was elided (F.C2a)", round, tc.ID)
					}
				}
				break
			}
		}

		// F.C2's early stop: if this round's elisions reached the budget,
		// they were minimal — the LAST elision was taken only because the
		// estimate was still over before it, so restoring exactly that one
		// must push the estimate back over budget. (Rounds where even all
		// eligible elisions can't reach the budget are exempt: there the
		// walk ran out, not stopped early.)
		if len(elidedIdx) > 0 {
			elidedAny = true
			if est := wireEstimate(req.Messages); est <= budget {
				last := elidedIdx[len(elidedIdx)-1]
				sim := append([]llm.Message{}, req.Messages...)
				sim[last] = llm.Message{Role: "tool", ToolCallID: sim[last].ToolCallID, Name: sim[last].Name, Content: originals[sim[last].ToolCallID]}
				if rest := wireEstimate(sim); rest <= budget {
					t.Errorf("round %d: estimate %d still fits with the last elision restored (%d ≤ %d) — the walk elided past the first point it fit (F.C2)", round, est, rest, budget)
				}
			}
		}
	}
	if !elidedAny {
		t.Fatalf("no round ever elided anything (base estimate %d, budget %d) — sizing broke, pins vacuous", base, budget)
	}

	log := readLog(t, logPath)
	if strings.Contains(log, "context over budget") {
		t.Errorf("every round from 3 on should fit after elision at this budget; got F.C3 warns:\n%s", log)
	}
	if n := strings.Count(log, `msg="context elided"`); n < 20 {
		t.Errorf("want an elision log line for nearly every round from 3 on (22 rounds), got %d:\n%s", n, log)
	}
}

// TestProbeCorrectableResultsElideWithCanonicalNames is probe 2: tool-result
// messages produced by the correctable paths — malformed JSON (Name =
// "wiki_get"), a call with an EMPTY function name (Name = ""), and an
// unknown tool (Name = "bogus_tool") — all carry the wire Name, and the
// elision walk canonicalizes each: the malformed call's placeholder names
// "wiki.get" (not skipped, not named wiki_get), the unknown tool's names
// "bogus_tool" and is NOT skipped (a bogus name gains no stage.*
// protection), and the empty name's placeholder is still a non-empty,
// correctly paired message. The most recent round is never touched.
func TestProbeCorrectableResultsElideWithCanonicalNames(t *testing.T) {
	logPath := installFileLog(t)

	ctlFake, _, _ := runBigTurn(t, LoopConfig{ContextTokens: 1000000})
	base := wireEstimate(ctlFake.Requests()[0].Messages)
	budget := base + 8000 // one 20 KB result over what two rounds of results leave room for

	// The four big reads are four DISTINCT pages: under A-004-2 a repeated
	// page is one read, so g2–g4 sharing g1's args would pin them after
	// g1's elision and this probe's later rounds would go over budget.
	names := widePageNames(4)
	rounds := [][]llm.Chunk{
		{
			toolCallChunk("call-bad", "wiki_get", "{oops"), // malformed JSON → correctable, Name "wiki_get"
			toolCallChunk("call-g1", "wiki_get", fmt.Sprintf(`{"page":%q}`, names[0])),
			{Finish: "tool_calls"},
		},
		{
			toolCallChunk("call-empty", "", `{"page":"big"}`), // empty wire name → unknown tool → correctable, Name ""
			toolCallChunk("call-g2", "wiki_get", fmt.Sprintf(`{"page":%q}`, names[1])),
			{Finish: "tool_calls"},
		},
		{
			toolCallChunk("call-unk", "bogus_tool", `{"x":1}`), // unknown tool → correctable, Name "bogus_tool"
			toolCallChunk("call-g3", "wiki_get", fmt.Sprintf(`{"page":%q}`, names[2])),
			{Finish: "tool_calls"},
		},
		{toolCallChunk("call-g4", "wiki_get", fmt.Sprintf(`{"page":%q}`, names[3])), {Finish: "tool_calls"}},
		{{Text: "done"}, {Finish: "stop"}},
	}

	l, fx, fake := newWideLoop(t, rounds, LoopConfig{ContextTokens: budget}, names)
	out := make(chan Event, 256)
	if err := l.Send(context.Background(), fx.csID, "fumble some calls", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	reqs := fake.Requests()
	if len(reqs) != 5 {
		t.Fatalf("Stream called %d times, want 5 (the one-retry path must have kept the turn alive)", len(reqs))
	}
	originals := map[string]string{}
	for _, ev := range events {
		if res, ok := ev.(ToolResEv); ok {
			originals[res.ID] = res.Content
		}
	}
	for _, id := range []string{"call-bad", "call-empty", "call-unk"} {
		if originals[id] == "" {
			t.Fatalf("correctable call %q produced an empty error result — events: %#v", id, events)
		}
	}

	// The correctable tool messages carry the WIRE Name on the way out —
	// pinned on the first request that contains each (round N's own results
	// first appear in round N+1's request).
	if msg := toolMsgByID(t, reqs[1].Messages, "call-bad"); msg.Name != "wiki_get" {
		t.Errorf("malformed-call tool message Name = %q, want the wire spelling %q", msg.Name, "wiki_get")
	}
	if msg := toolMsgByID(t, reqs[2].Messages, "call-empty"); msg.Name != "" {
		t.Errorf("empty-name call tool message Name = %q, want %q", msg.Name, "")
	}
	if msg := toolMsgByID(t, reqs[3].Messages, "call-unk"); msg.Name != "bogus_tool" {
		t.Errorf("unknown-call tool message Name = %q, want the bogus wire spelling %q", msg.Name, "bogus_tool")
	}

	for k, req := range reqs {
		checkWireShape(t, req, k+1, originals)
	}

	// Round 3's request elides round 1: the malformed call's result must be
	// replaced by a placeholder naming the CANONICAL name, not skipped and
	// not named wiki_get.
	elidedBad := toolMsgByID(t, reqs[2].Messages, "call-bad")
	if want := probePlaceholder("wiki.get", len(originals["call-bad"])); elidedBad.Content != want {
		t.Errorf("malformed call's elided content = %q, want %q (canonical name, exact F.C2 string)", elidedBad.Content, want)
	}
	if elidedBad.ToolCallID != "call-bad" || elidedBad.Name != "wiki_get" {
		t.Errorf("elided malformed-call message lost its wire identity: %+v", elidedBad)
	}

	// Round 4 elides round 2: the EMPTY-name call is still elided — not
	// skipped — with a non-empty placeholder built from canonical("") = "".
	elidedEmpty := toolMsgByID(t, reqs[3].Messages, "call-empty")
	if want := probePlaceholder("", len(originals["call-empty"])); elidedEmpty.Content != want {
		t.Errorf("empty-name call's elided content = %q, want %q", elidedEmpty.Content, want)
	}
	if elidedEmpty.Content == "" {
		t.Errorf("empty-name call's placeholder is empty content on the wire")
	}

	// Round 5 elides round 3: the unknown tool's result is NOT skipped (a
	// bogus name gains no stage.* protection) and names the canonicalized
	// bogus name — whatever CanonicalName makes of it ("bogus.tool": the
	// switch's default maps unknown _ to .).
	elidedUnk := toolMsgByID(t, reqs[4].Messages, "call-unk")
	if want := probePlaceholder(probeCanonical("bogus_tool"), len(originals["call-unk"])); elidedUnk.Content != want {
		t.Errorf("unknown tool's elided content = %q, want %q", elidedUnk.Content, want)
	}
	if !strings.HasPrefix(elidedUnk.Content, "[elided to fit the context budget: bogus.") {
		t.Errorf("unknown tool's placeholder should carry the canonicalized name, got %q", elidedUnk.Content)
	}

	// Most recent round intact in every request (F.C2a).
	for k := 1; k < len(reqs); k++ {
		id := []string{"call-g1", "call-g2", "call-g3", "call-g4"}[k-1]
		if msg := toolMsgByID(t, reqs[k].Messages, id); msg.Content != originals[id] {
			t.Errorf("round %d: most recent round's result for %q was elided (F.C2a)", k+1, id)
		}
	}

	if log := readLog(t, logPath); strings.Contains(log, "context over budget") {
		t.Errorf("this sizing should always fit after elision; got F.C3 warns:\n%s", log)
	}
}

// TestProbeElidedMapTracksIndicesAcrossGrowth is probe 3, pinned directly:
// msgs only ever grows by runRound's append (order- and index-preserving,
// whether or not the append reallocates) and boundContext returns either
// msgs itself or a same-length copy — so the index keys in elided cannot
// point at a different message between rounds. The test forces the exact
// reallocation boundContext's own fresh copy makes likely (its copy has
// cap == len, so the very next append reallocates) and asserts the map
// still names the right messages: the earlier placeholder is NOT re-elided,
// the newly eligible one is, and the most recent round stays intact. The
// three synthetic reads carry DISTINCT pages: under A-004-2 a shared
// signature would pin the later reads (their result would then be intact
// by the pin, not by index tracking), which would moot this probe's own
// question.
func TestProbeElidedMapTracksIndicesAcrossGrowth(t *testing.T) {
	big := strings.Repeat("x", 400) // 100 estimated tokens
	toolCall := func(id, page string) llm.ToolCall {
		return *toolCallChunk(id, "wiki_get", `{"page":"`+page+`"}`).ToolCall
	}
	msgs := []llm.Message{
		{Role: "system", Content: strings.Repeat("s", 40)},
		{Role: "user", Content: strings.Repeat("u", 40)},
		{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("id-a", "pa")}},
		{Role: "tool", ToolCallID: "id-a", Name: "wiki_get", Content: big},
		{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("id-b", "pb")}},
		{Role: "tool", ToolCallID: "id-b", Name: "wiki_get", Content: big},
	}

	turnStart := 2
	elided := map[int]bool{}
	pinned := map[string]bool{}                                  // the same maps Send passes across rounds
	out1 := boundContext(msgs, turnStart, elided, pinned, 2, 10) // budget 10: far over

	// Round 2's result (index 5) is the most recent round — only index 3
	// (round 1's result) may be elided.
	if got := out1[3].Content; got != probePlaceholder("wiki.get", len(big)) {
		t.Fatalf("round-1 result not elided: %q", got)
	}
	if got := out1[5].Content; got != big {
		t.Fatalf("most recent round's result was elided: %q", got)
	}
	if len(elided) != 1 || !elided[3] {
		t.Fatalf("elided = %v, want only index 3", elided)
	}

	// Grow the way runRound does — append a third round. out1's cap == len
	// (boundContext's fresh copy), so this append MUST reallocate: the
	// slice base changes out from under the map's index keys.
	msgs2 := append(out1,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("id-c", "pc")}},
		llm.Message{Role: "tool", ToolCallID: "id-c", Name: "wiki_get", Content: big},
	)
	if &msgs2[0] == &out1[0] {
		t.Fatalf("append did not reallocate — the test no longer exercises index stability across a new backing array")
	}

	out2 := boundContext(msgs2, turnStart, elided, pinned, 3, 10)

	// Index 3 still holds round-1's placeholder — NOT a re-elision of it
	// (which would name len(placeholder), ~95 bytes, not 400).
	if got := out2[3].Content; got != probePlaceholder("wiki.get", len(big)) {
		t.Errorf("index 3 changed after growth — double elision: %q", got)
	}
	// Index 5 is now the newly eligible round-2 result, elided exactly once.
	if got := out2[5].Content; got != probePlaceholder("wiki.get", len(big)) {
		t.Errorf("round-2 result should have been elided at round 3: %q", got)
	}
	// Index 7 is the most recent round's result — intact.
	if got := out2[7].Content; got != big {
		t.Errorf("most recent round's result was elided: %q", got)
	}
	if len(elided) != 2 || !elided[3] || !elided[5] {
		t.Errorf("elided = %v, want exactly indices 3 and 5", elided)
	}
}

// TestProbeSessionNDJSONByteIdenticalOnDisk is probe 5 — F.C4 against the
// durable artifact, not the in-memory view: a P2-shaped turn at an eliding
// budget and the same script at budget 1 000 000 must leave the session
// ndjson ON DISK equal, line for line, once the wall-clock ts field is
// normalized away. ts is the only nondeterministic field; everything else —
// including the FULL un-elided tool results — must match byte for byte.
func TestProbeSessionNDJSONByteIdenticalOnDisk(t *testing.T) {
	ctlFake, _, ctlFx := runBigTurn(t, LoopConfig{ContextTokens: 1000000})
	budget := wireEstimate(ctlFake.Requests()[2].Messages) - 2000

	sizedFake, _, sizedFx := runBigTurn(t, LoopConfig{ContextTokens: budget})

	// Non-vacuous: the sized run really elided on the wire.
	req3 := sizedFake.Requests()[2].Messages
	if elided := toolMsgByID(t, req3, "call-w1"); !strings.HasPrefix(elided.Content, "[elided to fit the context budget:") {
		t.Fatalf("sized run did not elide — pin is vacuous: %q", elided.Content)
	}

	readNDJSON := func(t *testing.T, fx *testLoopFixture) [][]byte {
		t.Helper()
		path := filepath.Join(fx.engine.Vault().Root(), ".llmwiki", "changesets", "open", fx.csID, "session.ndjson")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var out [][]byte
		for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("parse ndjson line: %v", err)
			}
			delete(m, "ts") // wall clock, not content
			norm, err := json.Marshal(m)
			if err != nil {
				t.Fatalf("re-marshal ndjson line: %v", err)
			}
			out = append(out, norm)
		}
		return out
	}

	ctlLines := readNDJSON(t, ctlFx)
	sizedLines := readNDJSON(t, sizedFx)
	if len(ctlLines) != len(sizedLines) {
		t.Fatalf("session.ndjson: sized run wrote %d records, control wrote %d", len(sizedLines), len(ctlLines))
	}
	sawBigResult := false
	for i := range ctlLines {
		if !bytes.Equal(ctlLines[i], sizedLines[i]) {
			t.Errorf("record %d differs on disk after elision:\n control %s\n sized  %s", i, ctlLines[i], sizedLines[i])
		}
		if len(ctlLines[i]) > bigResultBytes {
			sawBigResult = true
		}
	}
	if !sawBigResult {
		t.Errorf("neither session recorded a full %d-byte tool result — the fixture stopped producing them", bigResultBytes)
	}
}

// BenchmarkBoundContextRealisticWorstCase is probe 4: the cost of one
// boundContext call at the loop's realistic worst case — a full
// MaxToolRounds turn (24 rounds × two 20 KB results on the wire, 48 tool
// messages) with a budget forcing several elisions per call, so the walk
// re-estimates the whole request after each elision, on top of the initial
// estimate. Fix only if this exceeds ~5 ms per round.
func BenchmarkBoundContextRealisticWorstCase(b *testing.B) {
	// Silence F.C5's per-call Info line: the benchmark runs it millions of
	// times.
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(prev) })

	big := strings.Repeat("x", 20000)
	// Distinct pages per call: under A-004-2 a shared signature would pin
	// every later result after the first elision, and the walk would have
	// nothing to do — the worst case for the walk is many distinct reads.
	toolCall := func(id, args string) llm.ToolCall {
		return *toolCallChunk(id, "wiki_get", args).ToolCall
	}
	msgs := make([]llm.Message, 0, 4+2*24)
	msgs = append(msgs,
		llm.Message{Role: "system", Content: strings.Repeat("s", 4000)},
		llm.Message{Role: "system", Content: strings.Repeat("m", 2000)},
		llm.Message{Role: "system", Content: strings.Repeat("d", 1000)},
		llm.Message{Role: "user", Content: "read the big page, a lot"},
	)
	for r := 0; r < 24; r++ {
		msgs = append(msgs, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
			toolCall(fmt.Sprintf("a%d", r), fmt.Sprintf(`{"page":"big-a%d"}`, r)),
			toolCall(fmt.Sprintf("b%d", r), fmt.Sprintf(`{"page":"big-b%d"}`, r)),
		}})
		msgs = append(msgs,
			llm.Message{Role: "tool", ToolCallID: fmt.Sprintf("a%d", r), Name: "wiki_get", Content: big},
			llm.Message{Role: "tool", ToolCallID: fmt.Sprintf("b%d", r), Name: "wiki_get", Content: big},
		)
	}

	// Over by ~2.5 elisions' worth: the walk elides three results,
	// re-estimating after each, before it fits — the worst steady-state
	// shape the loop can produce.
	budget := requestTokens(msgs) - 3*4976 - 2000

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		boundContext(msgs, 4, make(map[int]bool), make(map[string]bool), 24, budget)
	}
}

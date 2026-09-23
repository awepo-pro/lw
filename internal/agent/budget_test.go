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

// writeBigPage writes wiki/concepts/big.md with a body of exactly n plain
// ASCII bytes — no character the frontmatter encoder or a JSON encoder
// would transform, so len(wiki.get's Result.Content) is linear in n and
// one calibration correction below lands exactly.
func writeBigPage(t *testing.T, root string, n int) {
	t.Helper()
	filler := "The key value cache stores decoded attention states so decoding stays cheap. "
	body := strings.Repeat(filler, n/len(filler)+1)[:n]
	page := "---\ntitle: Big\ncreated: 2026-08-20\nupdated: 2026-08-20\ntype: concept" +
		"\ntags: [inference]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: high\n---\n\n# Big\n\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(root, "wiki", "concepts", "big.md"), []byte(page), 0o644); err != nil {
		t.Fatalf("write big.md: %v", err)
	}
}

// newBudgetFixture is newTestLoopFixture plus one synthetic page,
// wiki/concepts/big.md, calibrated until the real wiki.get returns exactly
// resultBytes bytes. The vault serves pages from an immutable snapshot
// (snapshot.go), so every rewrite is followed by Vault.Reload before the
// next calibration read — the same registry call the scripted rounds make.
func newBudgetFixture(t *testing.T, resultBytes int) (*testLoopFixture, string) {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	writeBigPage(t, root, 4096)

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("stage.OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	author := stage.Author{Kind: "agent", Model: "test-model"}
	reg := tools.NewRegistry(tools.Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author})

	read := func() string {
		t.Helper()
		res, err := reg.Call(context.Background(), "wiki.get", json.RawMessage(bigWikiArgs))
		if err != nil {
			t.Fatalf("wiki.get: %v", err)
		}
		if res.IsError {
			t.Fatalf("wiki.get failed: %s", res.Content)
		}
		return res.Content
	}

	// Serialize appends the body verbatim, so len(Content) is linear in
	// the body length: one delta correction must land exactly. A third
	// miss fails the test instead of papering over a format drift.
	bodyLen := 4096
	got := read()
	for pass := 0; pass < 2 && len(got) != resultBytes; pass++ {
		bodyLen += resultBytes - len(got)
		writeBigPage(t, root, bodyLen)
		if err := e.Vault().Reload(); err != nil {
			t.Fatalf("Vault.Reload: %v", err)
		}
		got = read()
	}
	if len(got) != resultBytes {
		t.Fatalf("wiki.get result = %d bytes after calibration, want exactly %d — the page format drifted", len(got), resultBytes)
	}

	cs, err := e.OpenChangeset("budget test", author)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	store := NewFileSessions(root)
	if _, err := store.Create(cs.ID); err != nil {
		t.Fatalf("Create session: %v", err)
	}
	return &testLoopFixture{engine: e, reg: reg, store: store, csID: cs.ID}, got
}

// newBudgetLoop wires a Loop with cfg over newBudgetFixture and returns the
// fixture plus the calibrated 20000-byte page content.
func newBudgetLoop(t *testing.T, rounds [][]llm.Chunk, cfg LoopConfig) (*Loop, *testLoopFixture, *fakeStreamer, string) {
	t.Helper()
	fx, big := newBudgetFixture(t, bigResultBytes)
	fake := &fakeStreamer{rounds: rounds}
	l := newLoop(fake, fx.reg, fx.store, fx.engine, cfg)
	return l, fx, fake, big
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
func TestBudgetNeverElidesPriorHistory(t *testing.T) {
	rounds := append(bigWikiRounds(), [][]llm.Chunk{{{Text: "second turn"}, {Finish: "stop"}}}...)
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
	if len(reqs) != 4 {
		t.Fatalf("Stream called %d times, want 4 (3 rounds + turn 2's one round)", len(reqs))
	}

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
}

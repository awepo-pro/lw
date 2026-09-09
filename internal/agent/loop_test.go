package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
)

// fakeStreamer replays a scripted sequence of rounds — one []llm.Chunk per
// Stream call — with no network at all (backbone §9's "Contract — the
// fake-client seam", D-CS). Each round is delivered on its own channel by a
// goroutine that mirrors internal/llm's own consumeStream: every send is
// guarded by ctx, and production stops for good — deterministically,
// without racing a further send against ctx.Done() — the moment ctx ends,
// so a canceled turn never depends on how many more chunks happen to still
// be scripted.
//
// It also records every llm.Request it is handed (repair-1: the previous
// version discarded req entirely, which is the "test blindness" that let a
// duplicated user message ship — no test in this suite could ever have
// asserted on what Send actually sent over the wire). TestFirstRequestFol-
// lowsContextOrder reads requests back through Requests.
type fakeStreamer struct {
	mu       sync.Mutex
	rounds   [][]llm.Chunk
	calls    int
	requests []llm.Request

	// afterFirstChunk, if set, runs synchronously in the producing
	// goroutine right after the first chunk of every round is delivered.
	// TestContextCancel uses it to fire cancel() at a fixed point in the
	// stream instead of guessing a sleep.
	afterFirstChunk func()
}

// Requests returns every llm.Request Stream has been called with, in call
// order.
func (f *fakeStreamer) Requests() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]llm.Request, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *fakeStreamer) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	if f.calls >= len(f.rounds) {
		got := f.calls + 1
		f.mu.Unlock()
		return nil, fmt.Errorf("fakeStreamer: Stream called %d times, only %d round(s) scripted", got, len(f.rounds))
	}
	round := f.rounds[f.calls]
	f.calls++
	f.mu.Unlock()

	out := make(chan llm.Chunk)
	go func() {
		defer close(out)
		for i, c := range round {
			select {
			case out <- c:
			case <-ctx.Done():
				return
			}
			if i == 0 && f.afterFirstChunk != nil {
				f.afterFirstChunk()
			}
			if ctx.Err() != nil {
				return
			}
		}
	}()
	return out, nil
}

// toolCallChunk builds the one Chunk shape a complete tool call ever
// arrives as (backbone §8: ToolCall is "emitted once, complete").
func toolCallChunk(id, name, args string) llm.Chunk {
	tc := &llm.ToolCall{ID: id, Type: "function"}
	tc.Function.Name = name
	tc.Function.Arguments = args
	return llm.Chunk{ToolCall: tc}
}

// testLoopFixture bundles the real stage.Engine, tools.Registry and
// SessionStore a Loop needs (00-conventions.md §6: no fakes below the LLM
// seam), over a private copy of spec/fixtures/minimal, with one changeset
// already open — csID doubles as the session id, exactly as fileSessions
// expects (session.go: "this store uses the changeset id as the session id
// directly").
type testLoopFixture struct {
	engine *stage.Engine
	reg    *tools.Registry
	store  SessionStore
	csID   string
}

func newTestLoopFixture(t *testing.T) *testLoopFixture {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("stage.OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	author := stage.Author{Kind: "agent", Model: "test-model"}
	reg := tools.NewRegistry(tools.Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author})

	cs, err := e.OpenChangeset("loop test", author)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	store := NewFileSessions(root)
	if _, err := store.Create(cs.ID); err != nil {
		t.Fatalf("Create session: %v", err)
	}

	return &testLoopFixture{engine: e, reg: reg, store: store, csID: cs.ID}
}

// newTestLoop wires a Loop over newTestLoopFixture through the unexported
// newLoop constructor and a fakeStreamer scripted with rounds (backbone
// §9's fake-client seam, D-CS — NewLoop's exported *llm.Client parameter is
// never exercised by this suite).
func newTestLoop(t *testing.T, rounds [][]llm.Chunk, cfg LoopConfig) (*Loop, *testLoopFixture, *fakeStreamer) {
	t.Helper()
	fx := newTestLoopFixture(t)
	fake := &fakeStreamer{rounds: rounds}
	l := newLoop(fake, fx.reg, fx.store, fx.engine, cfg)
	return l, fx, fake
}

// drain collects every Event sent on out, in order, until it closes.
func drain(out <-chan Event) []Event {
	var events []Event
	for ev := range out {
		events = append(events, ev)
	}
	return events
}

// TestLoopTwoRounds is one of the five PASS-by-name tests the stage file
// requires. Round one calls the read-only stage.close tool (backbone §9
// item 6); round two answers in prose with no further tool call, ending
// the turn. It asserts the ToolCallEv/ToolResEv ordering, the StageEv the
// stage.* call fires, the terminal DoneEv{stop, 2} landing last, and that
// every turn element was appended to the session.
func TestLoopTwoRounds(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "stage.close", ""), {Finish: "tool_calls"}},
		{{Text: "All done."}, {Finish: "stop"}},
	}
	l, fx, _ := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "please summarize", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	toolCallIdx, toolResIdx := -1, -1
	var sawStage bool
	for i, ev := range events {
		switch e := ev.(type) {
		case ToolCallEv:
			if toolCallIdx == -1 {
				toolCallIdx = i
			}
		case ToolResEv:
			if toolResIdx == -1 {
				toolResIdx = i
			}
		case StageEv:
			sawStage = true
			if e.ChangesetID != fx.csID || e.Ops != 0 {
				t.Errorf("StageEv = %+v, want {%s 0} (a freshly opened changeset)", e, fx.csID)
			}
		}
	}
	if toolCallIdx == -1 || toolResIdx == -1 {
		t.Fatalf("want both a ToolCallEv and a ToolResEv, got %#v", events)
	}
	if toolCallIdx >= toolResIdx {
		t.Fatalf("ToolCallEv at %d, ToolResEv at %d; want the call before its result", toolCallIdx, toolResIdx)
	}
	if !sawStage {
		t.Fatalf("no StageEv observed for the stage.close call, got %#v", events)
	}

	last := events[len(events)-1]
	done, ok := last.(DoneEv)
	if !ok || done.Reason != "stop" || done.Rounds != 2 {
		t.Fatalf("last event = %#v, want DoneEv{Reason:stop, Rounds:2} last", last)
	}

	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	if len(sess.Records) != 3 {
		t.Fatalf("session records = %+v, want 3 (user, tool, assistant)", sess.Records)
	}
	if sess.Records[0].Role != "user" || sess.Records[0].Content != "please summarize" {
		t.Errorf("record 0 = %+v, want the user message", sess.Records[0])
	}
	if sess.Records[1].Role != "tool" || sess.Records[1].Tool != "stage.close" || !sess.Records[1].Staged {
		t.Errorf("record 1 = %+v, want a staged stage.close call", sess.Records[1])
	}
	if sess.Records[2].Role != "assistant" || sess.Records[2].Content != "All done." {
		t.Errorf("record 2 = %+v, want the final assistant text", sess.Records[2])
	}
}

// TestFirstRequestFollowsContextOrder is repair-1's regression test
// (orchestrator probe, 2026-09-06): Send used to fold the user Record into
// sess.Records before calling ContextBuilder.Build(sess, msg), so Build's
// part 4 (history) and part 5 (the new user message) rendered the same
// text twice in the very first wire request of every turn. fakeStreamer
// now records every llm.Request it receives (the "test blindness" half of
// the repair — no test here could previously see the wire shape at all),
// so this asserts directly on that: three system messages (prompt,
// curator-memory, orientation digest), then any history (none, for a
// session's first turn), then exactly one final user message equal to the
// turn's text — counted explicitly so the duplication cannot come back
// unnoticed.
func TestFirstRequestFollowsContextOrder(t *testing.T) {
	const turnMsg = "summarise kv-cache"
	rounds := [][]llm.Chunk{
		{{Text: "here is a summary"}, {Finish: "stop"}},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, turnMsg, out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Stream called %d times, want exactly 1 for a single-round turn", len(reqs))
	}
	req := reqs[0]

	if len(req.Tools) == 0 {
		t.Fatalf("Tools = %d, want the registry's definitions to reach the model", len(req.Tools))
	}

	msgs := req.Messages
	// A brand-new session has no history yet, so the first request is
	// exactly system x3 + the one user message: this pins the probe's own
	// reproduction (5 messages, the user one duplicated) as impossible.
	if len(msgs) != 4 {
		t.Fatalf("len(msgs) = %d, want 4 (3 system + 1 user); got %+v", len(msgs), msgs)
	}
	for i := 0; i < 3; i++ {
		if msgs[i].Role != "system" {
			t.Errorf("msgs[%d].Role = %q, want %q (part %d of the five-part order)", i, msgs[i].Role, "system", i+1)
		}
	}

	userCount := 0
	for _, m := range msgs {
		if m.Role == "user" && m.Content == turnMsg {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("turn message %q appears %d times in the first request, want exactly 1 — %+v", turnMsg, userCount, msgs)
	}

	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Content != turnMsg {
		t.Fatalf("last message = %+v, want the turn's own user message last (part 5)", last)
	}
}

// TestMaxRounds is one of the five PASS-by-name tests. It scripts a small
// MaxToolRounds and a tool call every round, so the cap fires instead of
// scripting all the way to the real default of 24 (backbone §9 item 5).
func TestMaxRounds(t *testing.T) {
	const cap = 2
	rounds := [][]llm.Chunk{
		{toolCallChunk("c1", "stage.close", ""), {Finish: "tool_calls"}},
		{toolCallChunk("c2", "stage.close", ""), {Finish: "tool_calls"}},
	}
	l, fx, _ := newTestLoop(t, rounds, LoopConfig{MaxToolRounds: cap})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "loop forever", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	last := events[len(events)-1]
	done, ok := last.(DoneEv)
	if !ok || done.Reason != "max_rounds" || done.Rounds != cap {
		t.Fatalf("last event = %#v, want DoneEv{Reason:max_rounds, Rounds:%d}", last, cap)
	}
}

// TestMalformedToolJSONRetriesOnce is one of the five PASS-by-name tests
// (backbone §9 item 7). The first malformed call is fed back as an
// IsError tool result and the turn continues; the second consecutive one
// ends the turn with ErrorEv, and Send returns that exact error.
func TestMalformedToolJSONRetriesOnce(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("c1", "wiki.get", "{not valid json"), {Finish: "tool_calls"}},
		{toolCallChunk("c2", "wiki.get", "{also not valid"), {Finish: "tool_calls"}},
	}
	l, fx, _ := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	err := l.Send(context.Background(), fx.csID, "call a tool badly", out)
	if err == nil {
		t.Fatalf("Send: want a non-nil error after two consecutive malformed tool calls")
	}
	events := drain(out)

	badResults := 0
	for _, ev := range events {
		if res, ok := ev.(ToolResEv); ok && res.IsError {
			badResults++
		}
	}
	if badResults != 2 {
		t.Fatalf("want 2 IsError ToolResEv (one retry, then abort), got %d in %#v", badResults, events)
	}

	last := events[len(events)-1]
	errEv, ok := last.(ErrorEv)
	if !ok {
		t.Fatalf("last event = %#v, want ErrorEv", last)
	}
	if errEv.Err != err {
		t.Fatalf("ErrorEv.Err = %v, Send returned %v; want the exact same error (backbone §9, C-105)", errEv.Err, err)
	}

	sess, gerr := fx.store.Get(fx.csID)
	if gerr != nil {
		t.Fatalf("Get session: %v", gerr)
	}
	badRecords := 0
	for _, r := range sess.Records {
		if r.Role == "tool" && r.Tool == "wiki.get" {
			badRecords++
		}
	}
	if badRecords != 2 {
		t.Fatalf("want 2 tool records for the two malformed attempts, got %d in %+v", badRecords, sess.Records)
	}
}

// TestUnknownToolNameRetriesOnce is not one of the five names the stage
// file requires, but backbone §9 item 8 (D-CT) is its own amendment with
// its own paragraph: a hallucinated tool name is model-correctable and
// shares the malformed-JSON retry budget, not "every other non-nil error
// aborts" (backbone §6). One call to a name the registry never registered
// retries; a second consecutive one still aborts, exactly like malformed
// JSON.
func TestUnknownToolNameRetriesOnce(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("c1", "wiki.frobnicate", "{}"), {Finish: "tool_calls"}},
		{toolCallChunk("c2", "wiki.frobnicate", "{}"), {Finish: "tool_calls"}},
	}
	l, fx, _ := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	err := l.Send(context.Background(), fx.csID, "call a tool that does not exist", out)
	if err == nil {
		t.Fatalf("Send: want a non-nil error after two consecutive unknown-tool calls")
	}
	events := drain(out)

	badResults := 0
	for _, ev := range events {
		if res, ok := ev.(ToolResEv); ok && res.IsError {
			badResults++
		}
	}
	if badResults != 2 {
		t.Fatalf("want 2 IsError ToolResEv (one retry, then abort), got %d in %#v", badResults, events)
	}

	last := events[len(events)-1]
	if _, ok := last.(ErrorEv); !ok {
		t.Fatalf("last event = %#v, want ErrorEv", last)
	}
}

// TestToolErrorDoesNotAbort is one of the five PASS-by-name tests (backbone
// §9 item 8's closing sentence: "A Result with IsError: true and a nil
// error is an ordinary result and never aborts the loop"). wiki.get on a
// page that does not exist returns exactly that shape.
func TestToolErrorDoesNotAbort(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("c1", "wiki.get", `{"page":"does-not-exist"}`), {Finish: "tool_calls"}},
		{{Text: "ok, continuing"}, {Finish: "stop"}},
	}
	l, fx, _ := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "look up a missing page", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	sawIsError, sawStage := false, false
	for _, ev := range events {
		switch e := ev.(type) {
		case ToolResEv:
			if e.IsError {
				sawIsError = true
			}
		case StageEv:
			sawStage = true
		}
	}
	if !sawIsError {
		t.Fatalf("want a ToolResEv{IsError:true} for the missing page, got %#v", events)
	}
	if sawStage {
		t.Fatalf("wiki.get is not a stage.* tool; want no StageEv, got %#v", events)
	}

	last := events[len(events)-1]
	done, ok := last.(DoneEv)
	if !ok || done.Reason != "stop" || done.Rounds != 2 {
		t.Fatalf("last event = %#v, want DoneEv{Reason:stop, Rounds:2} — the tool error must not abort the turn", last)
	}
}

// TestLoopReplaysReasoningOnRoundTwo is the S5-T7 regression test for
// C-114: no prior test in this suite ever ran a second round against a
// thinking-mode provider, which is exactly what hid the defect — round 1
// discarded the model's own reasoning, and round 2's request never carried
// it back, so a real thinking provider rejects it with a 400. Round 1
// scripts reasoning deltas immediately followed by a tool call with no
// preceding text, so dispatchToolCall — not flush — is the one that must
// attach the accumulated reasoning to the assistant message it builds; this
// asserts that message reaches round 2's actual llm.Request.
func TestLoopReplaysReasoningOnRoundTwo(t *testing.T) {
	const wantReasoning = "Let me think about it."
	rounds := [][]llm.Chunk{
		{
			{Reasoning: "Let me "},
			{Reasoning: "think about it."},
			toolCallChunk("call-1", "stage.close", ""),
			{Finish: "tool_calls"},
		},
		{
			{Text: "done"},
			{Finish: "stop"},
		},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "trigger reasoning replay", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Stream called %d times, want exactly 2 (a second round must actually run)", len(reqs))
	}

	var found bool
	for _, m := range reqs[1].Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			found = true
			if m.ToolCalls[0].ID != "call-1" {
				continue
			}
			if m.ReasoningContent != wantReasoning {
				t.Errorf("round 2's assistant tool-call message ReasoningContent = %q, want %q", m.ReasoningContent, wantReasoning)
			}
		}
	}
	if !found {
		t.Fatalf("round 2 request carried no assistant tool-call message: %+v", reqs[1].Messages)
	}
}

// TestLoopReasoningOnEveryAssistantMessageInRound is the reset test the
// stage file requires, corrected by C-115: a live wire probe against the
// real provider showed that attaching a round's reasoning to only its first
// assistant message (the rule frozen at dispatch) still 400s — the provider
// accepts a follow-up request only when EVERY assistant message of the
// round repeats the same reasoning string, because it can emit several
// parallel tool calls in one round. This round produces three: a text
// flush and two tool-call messages, all three from the one accumulated
// roundReasoning builder — assert all three carry it, unchanged.
func TestLoopReasoningOnEveryAssistantMessageInRound(t *testing.T) {
	const reasoning = "shared reasoning for the whole round"
	rounds := [][]llm.Chunk{
		{
			{Reasoning: reasoning},
			{Text: "here's what I found"},
			toolCallChunk("call-1", "stage.close", ""),
			toolCallChunk("call-2", "stage.close", ""),
			{Finish: "tool_calls"},
		},
		{
			{Text: "done"},
			{Finish: "stop"},
		},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "one round, three assistant messages", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Stream called %d times, want exactly 2", len(reqs))
	}

	var textMsg *llm.Message
	var toolCallMsgs []llm.Message
	for i := range reqs[1].Messages {
		m := reqs[1].Messages[i]
		if m.Role != "assistant" {
			continue
		}
		switch {
		case len(m.ToolCalls) > 0:
			toolCallMsgs = append(toolCallMsgs, m)
		case m.Content == "here's what I found":
			textMsg = &reqs[1].Messages[i]
		}
	}
	if textMsg == nil {
		t.Fatalf("no text-flush assistant message found in round 2's request: %+v", reqs[1].Messages)
	}
	if textMsg.ReasoningContent != reasoning {
		t.Errorf("text-flush assistant message ReasoningContent = %q, want %q", textMsg.ReasoningContent, reasoning)
	}
	if len(toolCallMsgs) != 2 {
		t.Fatalf("got %d assistant tool-call messages, want 2: %+v", len(toolCallMsgs), toolCallMsgs)
	}
	for i, m := range toolCallMsgs {
		if m.ReasoningContent != reasoning {
			t.Errorf("tool-call assistant message %d ReasoningContent = %q, want %q (every assistant message in the round must carry it, C-115)", i, m.ReasoningContent, reasoning)
		}
	}
}

// TestLoopReasoningDoesNotLeakAcrossRounds proves the other half of C-115:
// roundReasoning resets when the NEXT ROUND starts, not mid-round — round
// 1's reasoning must never appear on round 2's assistant message, and vice
// versa, even though by round 3's request both messages sit side by side in
// the same history.
func TestLoopReasoningDoesNotLeakAcrossRounds(t *testing.T) {
	const round1Reasoning = "round one reasoning"
	const round2Reasoning = "round two reasoning"
	rounds := [][]llm.Chunk{
		{
			{Reasoning: round1Reasoning},
			toolCallChunk("call-1", "stage.close", ""),
			{Finish: "tool_calls"},
		},
		{
			{Reasoning: round2Reasoning},
			toolCallChunk("call-2", "stage.close", ""),
			{Finish: "tool_calls"},
		},
		{
			{Text: "done"},
			{Finish: "stop"},
		},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "three rounds, isolated reasoning", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	reqs := fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("Stream called %d times, want exactly 3", len(reqs))
	}

	var msg1, msg2 *llm.Message
	for i := range reqs[2].Messages {
		m := reqs[2].Messages[i]
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		switch m.ToolCalls[0].ID {
		case "call-1":
			msg1 = &reqs[2].Messages[i]
		case "call-2":
			msg2 = &reqs[2].Messages[i]
		}
	}
	if msg1 == nil || msg2 == nil {
		t.Fatalf("round 3's request is missing one of round 1/2's tool-call messages: %+v", reqs[2].Messages)
	}
	if msg1.ReasoningContent != round1Reasoning {
		t.Errorf("round 1's assistant message ReasoningContent = %q, want %q (its own round's reasoning)", msg1.ReasoningContent, round1Reasoning)
	}
	if msg2.ReasoningContent != round2Reasoning {
		t.Errorf("round 2's assistant message ReasoningContent = %q, want %q (its own round's reasoning, not round 1's)", msg2.ReasoningContent, round2Reasoning)
	}
}

// TestContextCancel is one of the five PASS-by-name tests. Canceling ctx
// partway through a round must return promptly, close out, and leak no
// goroutine — asserted here by waiting, with a timeout, for both Send and
// the goroutine draining out to finish.
func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chunks := make([]llm.Chunk, 0, 5)
	for i := 0; i < 5; i++ {
		chunks = append(chunks, llm.Chunk{Text: fmt.Sprintf("chunk-%d ", i)})
	}
	fake := &fakeStreamer{rounds: [][]llm.Chunk{chunks}, afterFirstChunk: cancel}

	fx := newTestLoopFixture(t)
	l := newLoop(fake, fx.reg, fx.store, fx.engine, LoopConfig{})

	out := make(chan Event) // unbuffered: forces every send through the ctx guard
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- l.Send(ctx, fx.csID, "hang please", out)
	}()

	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range out {
		}
	}()

	select {
	case err := <-sendDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return within 2s of ctx cancellation — goroutine leak?")
	}

	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
		t.Fatal("out was never closed — Send must close it on every exit path")
	}
}

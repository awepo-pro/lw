package agent

// stream_cut_test.go holds 035's frozen contract for a provider stream that
// ends before [DONE] or a finish_reason (llm.ErrStreamTruncated): the agent
// recovers — never a silent clean stop. The three shapes are (A) a tool call
// was already dispatched mid-stream, so the round continues as if finished;
// (B) nothing was dispatched, so the identical request is re-sent once; (C)
// the re-send was also cut, so the turn fails. The hazard the (A)/(B) split
// exists for: a retry that re-dispatched a call the cut round already ran
// would stage the same op twice.
//
// Every test here scripts the cut as Chunk{Err} wrapping
// llm.ErrStreamTruncated through fakeStreamer — the same shape internal/llm
// (035 T1) emits — and depends on nothing from internal/llm's own code.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
)

// cutChunk is the one shape a truncated stream ever reaches this package
// as: a terminal Chunk whose Err wraps llm.ErrStreamTruncated (035).
func cutChunk() llm.Chunk {
	return llm.Chunk{Err: fmt.Errorf("%w: x", llm.ErrStreamTruncated)}
}

// assertEventOrder asserts events contains each element of want, in order —
// a subsequence match, so unrelated events between them (the ReasoningDelta
// a cut round streamed first, say) do not break it, while a missing,
// reordered or duplicated element of want does.
func assertEventOrder(t *testing.T, events []Event, want ...Event) {
	t.Helper()
	i := 0
	for _, got := range events {
		if i >= len(want) {
			break
		}
		if eventEqual(got, want[i]) {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("events = %#v\nwant them to contain, in order: %#v", events, want)
	}
}

// eventEqual reports whether got matches want: same concrete type and equal
// salient fields.
func eventEqual(got, want Event) bool {
	switch w := want.(type) {
	case TextDelta:
		g, ok := got.(TextDelta)
		return ok && g.Text == w.Text
	case RetryEv:
		g, ok := got.(RetryEv)
		return ok && g == w
	case DoneEv:
		g, ok := got.(DoneEv)
		return ok && g == w
	case ErrorEv:
		_, ok := got.(ErrorEv)
		return ok
	default:
		return false
	}
}

// TestCutAfterToolCallContinues is 035 case (A): the cut lands after a tool
// call was already dispatched mid-stream, so the round ends as if it had
// finished — the call runs exactly once, its assistant message and result
// reach round 2's request, and no retry is attempted (a retry would
// re-dispatch the call the cut round already ran).
func TestCutAfterToolCallContinues(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "wiki.search", `{}`), cutChunk()},
		{{Text: "done"}, {Finish: "stop"}},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "search, then get cut", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	toolCalls := 0
	for _, ev := range events {
		switch ev.(type) {
		case ToolCallEv:
			toolCalls++
		case RetryEv, ErrorEv:
			t.Fatalf("want no RetryEv and no ErrorEv in case (A), got %#v", events)
		}
	}
	if toolCalls != 1 {
		t.Fatalf("tool dispatched %d times, want exactly once: %#v", toolCalls, events)
	}

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Stream called %d times, want exactly 2", len(reqs))
	}
	var assistants []int
	for i, m := range reqs[1].Messages {
		if m.Role == "assistant" {
			assistants = append(assistants, i)
		}
	}
	if len(assistants) != 1 {
		t.Fatalf("request 2 carries %d assistant messages, want exactly 1: %+v", len(assistants), reqs[1].Messages)
	}
	m := reqs[1].Messages[assistants[0]]
	if len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != "call-1" {
		t.Fatalf("assistant message ToolCalls = %+v, want exactly [call-1]", m.ToolCalls)
	}
	next := assistants[0] + 1
	if next >= len(reqs[1].Messages) || reqs[1].Messages[next].Role != "tool" || reqs[1].Messages[next].ToolCallID != "call-1" {
		t.Fatalf("the assistant message must be followed by its own tool result: %+v", reqs[1].Messages)
	}

	last := events[len(events)-1]
	done, ok := last.(DoneEv)
	if !ok || done.Reason != "stop" || done.Rounds != 2 {
		t.Fatalf("last event = %#v, want DoneEv{Reason:stop, Rounds:2}", last)
	}
}

// TestCutWithoutToolCallRetriesOnce is 035 case (B): the cut lands with no
// tool call dispatched, so nothing observable happened — the round's partial
// text and reasoning are discarded, RetryEv tells consumers to drop what
// they streamed, and the identical request is re-sent. The retried stream is
// handled as a fresh stream of the same round, so the turn ends DoneEv{stop, 1}:
// a retry is not a round.
func TestCutWithoutToolCallRetriesOnce(t *testing.T) {
	rounds := [][]llm.Chunk{
		{{Reasoning: "r"}, {Text: "partial ZEBRA"}, cutChunk()},
		{{Text: "full answer"}, {Finish: "stop"}},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "get cut mid-sentence", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Stream called %d times, want exactly 2", len(reqs))
	}
	if !reflect.DeepEqual(reqs[0], reqs[1]) {
		t.Fatalf("the retry's request must be deep-equal to the cut one:\ncut:  %+v\nsent: %+v", reqs[0], reqs[1])
	}

	assertEventOrder(t, events,
		TextDelta{Text: "partial ZEBRA"},
		RetryEv{Round: 1, Attempt: 1, Reason: "stream ended early"},
		TextDelta{Text: "full answer"},
		DoneEv{Reason: "stop", Rounds: 1},
	)

	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	var assistant *Record
	for i := range sess.Records {
		if strings.Contains(sess.Records[i].Content, "ZEBRA") || strings.Contains(sess.Records[i].Reasoning, "ZEBRA") {
			t.Errorf("record %d leaked the cut round's partial text: %+v", i, sess.Records[i])
		}
		if sess.Records[i].Role == "assistant" {
			assistant = &sess.Records[i]
		}
	}
	if assistant == nil || assistant.Content != "full answer" {
		t.Fatalf("assistant record = %+v, want Content %q", assistant, "full answer")
	}
}

// TestSecondCutFailsTurn is 035 case (C): the re-sent request was cut too,
// so the provider is failing the same request twice in a row — retrying
// again would loop forever. The turn fails through the ordinary ErrorEv
// path, with an error that still wraps llm.ErrStreamTruncated.
func TestSecondCutFailsTurn(t *testing.T) {
	rounds := [][]llm.Chunk{
		{{Text: "a"}, cutChunk()},
		{{Text: "b"}, cutChunk()},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	sendErr := l.Send(context.Background(), fx.csID, "cut twice", out)
	if sendErr == nil || !errors.Is(sendErr, llm.ErrStreamTruncated) {
		t.Fatalf("Send error = %v, want one wrapping llm.ErrStreamTruncated", sendErr)
	}
	events := drain(out)

	retries, fails := 0, 0
	for _, ev := range events {
		switch ev.(type) {
		case RetryEv:
			retries++
		case ErrorEv:
			fails++
		}
	}
	if retries != 1 {
		t.Fatalf("got %d RetryEv, want exactly 1 (the first cut's): %#v", retries, events)
	}
	if fails != 1 {
		t.Fatalf("got %d ErrorEv, want exactly 1: %#v", fails, events)
	}
	if len(fake.Requests()) != 2 {
		t.Fatalf("Stream called %d times, want exactly 2 (one retry, then fail)", len(fake.Requests()))
	}
}

// TestRetryBudgetIsPerRound pins the budget's scope: one cut retry per
// round, not per turn — round 2's own cut gets its own retry, and both
// retries are invisible to DoneEv.Rounds.
func TestRetryBudgetIsPerRound(t *testing.T) {
	rounds := [][]llm.Chunk{
		{cutChunk()},
		{toolCallChunk("call-1", "wiki.search", `{}`), {Finish: "tool_calls"}},
		{cutChunk()},
		{{Text: "ok"}, {Finish: "stop"}},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "get cut in two different rounds", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	var retryRounds []int
	for _, ev := range events {
		if r, ok := ev.(RetryEv); ok {
			retryRounds = append(retryRounds, r.Round)
		}
	}
	if len(retryRounds) != 2 || retryRounds[0] != 1 || retryRounds[1] != 2 {
		t.Fatalf("RetryEv rounds = %v, want [1 2] — one retry each for rounds 1 and 2", retryRounds)
	}
	if len(fake.Requests()) != 4 {
		t.Fatalf("Stream called %d times, want exactly 4", len(fake.Requests()))
	}
	last := events[len(events)-1]
	done, ok := last.(DoneEv)
	if !ok || done.Reason != "stop" || done.Rounds != 2 {
		t.Fatalf("last event = %#v, want DoneEv{Reason:stop, Rounds:2} — a retry is not a round", last)
	}
}

// TestRetryThenCutAfterToolContinues pins the handoff between the two
// recovery shapes within one round: the round's first attempt is cut with
// nothing dispatched (retry), and the retried stream is handled as a fresh
// stream of the same round — so a cut after ITS tool call is case (A)
// continue, not a second retry.
func TestRetryThenCutAfterToolContinues(t *testing.T) {
	rounds := [][]llm.Chunk{
		{cutChunk()},
		{toolCallChunk("call-1", "wiki.search", `{}`), cutChunk()},
		{{Text: "ok"}, {Finish: "stop"}},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "cut, then cut after a tool call", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	retries, calls := 0, 0
	for _, ev := range events {
		switch ev.(type) {
		case RetryEv:
			retries++
		case ToolCallEv:
			calls++
		}
	}
	if retries != 1 {
		t.Fatalf("got %d RetryEv, want exactly 1: %#v", retries, events)
	}
	if calls != 1 {
		t.Fatalf("tool dispatched %d times, want exactly once: %#v", calls, events)
	}
	if len(fake.Requests()) != 3 {
		t.Fatalf("Stream called %d times, want exactly 3", len(fake.Requests()))
	}
	last := events[len(events)-1]
	if _, ok := last.(DoneEv); !ok {
		t.Fatalf("last event = %#v, want DoneEv — the turn must succeed", last)
	}
}

// TestOtherStreamErrorsNeverRetry pins the boundary: only
// llm.ErrStreamTruncated recovers. A stall, the parse error — anything else
// a Chunk.Err can carry — keeps today's fail-the-turn behavior, with no
// RetryEv and no second Stream call.
func TestOtherStreamErrorsNeverRetry(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"stall", fmt.Errorf("%w: x", llm.ErrStalled)},
		{"parse", errors.New("llm: parse stream chunk: x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rounds := [][]llm.Chunk{{{Text: "x"}, {Err: tc.err}}}
			l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

			out := make(chan Event, 64)
			sendErr := l.Send(context.Background(), fx.csID, "fail, do not retry", out)
			if sendErr == nil {
				t.Fatalf("Send: want a non-nil error for %v", tc.err)
			}
			events := drain(out)

			for _, ev := range events {
				if _, ok := ev.(RetryEv); ok {
					t.Fatalf("got a RetryEv for %v — only ErrStreamTruncated may retry: %#v", tc.err, events)
				}
			}
			if len(fake.Requests()) != 1 {
				t.Fatalf("Stream called %d times, want exactly 1 — other errors never retry", len(fake.Requests()))
			}
			last := events[len(events)-1]
			if _, ok := last.(ErrorEv); !ok {
				t.Fatalf("last event = %#v, want ErrorEv", last)
			}
		})
	}
}

// TestPlanCut is the (A)/(B)/(C) table itself, one row per combination the
// two per-round facts — was a tool call dispatched, has this round already
// retried — can take. It pins the invariant the whole design exists for: no
// combination ever plans a retry once a tool call is out, because that call
// already ran.
func TestPlanCut(t *testing.T) {
	cases := []struct {
		name       string
		toolCalled bool
		retried    bool
		want       cutPlan
	}{
		{"call dispatched, first cut", true, false, cutContinue},
		{"call dispatched, after a retry", true, true, cutContinue},
		{"nothing dispatched, first cut", false, false, cutRetry},
		{"nothing dispatched, second cut", false, true, cutFail},
	}
	for _, tc := range cases {
		if got := planCut(tc.toolCalled, tc.retried); got != tc.want {
			t.Errorf("%s: planCut(%t, %t) = %v, want %v", tc.name, tc.toolCalled, tc.retried, got, tc.want)
		}
	}
}

// TestCutRetryDiscardsRoundReasoning pins the half of case (B)'s discard the
// other frozen tests cannot see: the round-wide accumulators roundText and
// roundReasoning, which become the wire message's Content and
// ReasoningContent. A provider cut before its tool call leaves stale text
// and reasoning behind; if the retry does not reset them, the (A) continue
// after the retried attempt's call sends the dead stream's words back to the
// provider as if the model had said and thought them.
func TestCutRetryDiscardsRoundReasoning(t *testing.T) {
	rounds := [][]llm.Chunk{
		{{Reasoning: "stale thinking"}, {Text: "stale prose"}, cutChunk()},
		{toolCallChunk("call-1", "wiki.search", `{}`), cutChunk()},
		{{Text: "ok"}, {Finish: "stop"}},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "cut mid-answer, then cut after the call", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	reqs := fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("Stream called %d times, want exactly 3", len(reqs))
	}
	for i, m := range reqs[2].Messages {
		if strings.Contains(m.ReasoningContent, "stale") || strings.Contains(m.Content, "stale") {
			t.Errorf("request 3's message %d (%s) carries the cut stream's output: %+v", i, m.Role, m)
		}
	}

	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	for i, r := range sess.Records {
		if strings.Contains(r.Reasoning, "stale") || strings.Contains(r.Content, "stale") {
			t.Errorf("record %d leaked the cut stream's output: %+v", i, r)
		}
	}

	retries := 0
	for _, ev := range events {
		if _, ok := ev.(RetryEv); ok {
			retries++
		}
	}
	if retries != 1 {
		t.Fatalf("got %d RetryEv, want exactly 1: %#v", retries, events)
	}
}

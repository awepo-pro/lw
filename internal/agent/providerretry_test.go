package agent

// providerretry_test.go holds 061's frozen contract for a provider that
// refuses a round: a transient refusal (429, 5xx, an in-stream error event)
// is waited out and the identical request re-sent — three times at most, per
// round, with the provider's own Retry-After when it names a sane one — and
// everything else fails the turn with the *llm.ProviderError intact in the
// chain. The hazard the "tool call dispatched" split exists for is 035's:
// re-sending a round that already ran a call would run it twice.
//
// Every test scripts the provider through scriptedStreamer (a Stream that can
// fail outright OR stream chunks, which fakeStreamer cannot) and the wait
// through sleepLog — no test sleeps for real.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/trace"
)

// providerStep is one scripted Stream call: Stream itself fails with err
// (the HTTP-refusal shape), or it returns a channel that carries chunks and
// then closes (the streamed shapes, including an in-stream error event).
type providerStep struct {
	err    error
	chunks []llm.Chunk
}

// scriptedStreamer replays steps, one per Stream call, and records every
// request it is handed. observe makes it call the trace Observer the way the
// real client does, so a traced turn writes its request files.
type scriptedStreamer struct {
	mu       sync.Mutex
	steps    []providerStep
	calls    int
	requests []llm.Request
	observe  bool
}

func (s *scriptedStreamer) Requests() []llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llm.Request(nil), s.requests...)
}

func (s *scriptedStreamer) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *scriptedStreamer) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	if s.calls >= len(s.steps) {
		n := s.calls + 1
		s.mu.Unlock()
		return nil, fmt.Errorf("scriptedStreamer: Stream called %d times, only %d step(s) scripted", n, len(s.steps))
	}
	step := s.steps[s.calls]
	s.calls++
	s.mu.Unlock()

	if s.observe {
		if b, err := json.Marshal(req); err == nil {
			trace.Observer{}.OnRequest(ctx, b)
		}
	}
	if step.err != nil {
		return nil, step.err
	}
	out := make(chan llm.Chunk)
	go func() {
		defer close(out)
		for _, c := range step.chunks {
			select {
			case out <- c:
			case <-ctx.Done():
				return
			}
			if ctx.Err() != nil {
				return
			}
		}
	}()
	return out, nil
}

// sleepLog is the injected wait: it records every duration the loop asked
// for and returns at once. hook, when set, runs inside the wait — a test
// cancels the turn's ctx there.
type sleepLog struct {
	mu   sync.Mutex
	got  []time.Duration
	hook func(ctx context.Context) error
}

func (s *sleepLog) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.got = append(s.got, d)
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return nil
}

func (s *sleepLog) Sleeps() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.got...)
}

// newProviderLoop is newTestLoop over a scriptedStreamer with the wait
// injected.
func newProviderLoop(t *testing.T, steps []providerStep, cfg LoopConfig) (*Loop, *testLoopFixture, *scriptedStreamer, *sleepLog) {
	t.Helper()
	fx := newTestLoopFixture(t)
	fake := &scriptedStreamer{steps: steps}
	sl := &sleepLog{}
	l := newLoop(fake, fx.reg, fx.store, fx.engine, cfg)
	l.sleep = sl.sleep
	return l, fx, fake, sl
}

// sendTurn runs one turn and returns what it emitted and returned.
func sendTurn(t *testing.T, l *Loop, fx *testLoopFixture, msg string) ([]Event, error) {
	t.Helper()
	out := make(chan Event, 256)
	err := l.Send(context.Background(), fx.csID, msg, out)
	return drain(out), err
}

// inStreamErr is the chunk an in-stream provider error event reaches the
// loop as: a terminal Chunk whose Err is the typed error (061 D2).
func inStreamErr(status int, msg string) llm.Chunk {
	return llm.Chunk{Err: &llm.ProviderError{Status: status, Message: msg, InStream: true}}
}

func okRound(text string) providerStep {
	return providerStep{chunks: []llm.Chunk{{Text: text}, {Finish: "stop"}}}
}

func retryEvents(events []Event) []RetryEv {
	var got []RetryEv
	for _, ev := range events {
		if r, ok := ev.(RetryEv); ok {
			got = append(got, r)
		}
	}
	return got
}

func errorEvents(events []Event) int {
	n := 0
	for _, ev := range events {
		if _, ok := ev.(ErrorEv); ok {
			n++
		}
	}
	return n
}

// TestProviderRetryThenSucceeds: a 429 that names Retry-After 3 s is waited
// out for exactly that long and the identical request is re-sent; the turn
// then succeeds as if nothing had happened, with one RetryEv saying why.
func TestProviderRetryThenSucceeds(t *testing.T) {
	steps := []providerStep{
		{err: &llm.ProviderError{Status: 429, Message: "slow down", RetryAfter: 3 * time.Second}},
		okRound("here you go"),
	}
	l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})

	events, err := sendTurn(t, l, fx, "ask something")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	want := []RetryEv{{Round: 1, Attempt: 1, Reason: "provider 429: slow down; retrying in 3s"}}
	if got := retryEvents(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("RetryEv = %#v, want %#v", got, want)
	}
	if got := sl.Sleeps(); !reflect.DeepEqual(got, []time.Duration{3 * time.Second}) {
		t.Fatalf("slept %v, want exactly [3s]", got)
	}
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Stream called %d times, want 2", len(reqs))
	}
	if !reflect.DeepEqual(reqs[0], reqs[1]) {
		t.Fatalf("the retry's request must be deep-equal to the refused one:\nfirst: %+v\nsent:  %+v", reqs[0], reqs[1])
	}
	assertEventOrder(t, events,
		RetryEv{Round: 1, Attempt: 1, Reason: "provider 429: slow down; retrying in 3s"},
		TextDelta{Text: "here you go"},
		DoneEv{Reason: "stop", Rounds: 1},
	)
	if n := errorEvents(events); n != 0 {
		t.Errorf("got %d ErrorEv, want none: %#v", n, events)
	}
}

// TestProviderRetryExponential: with no Retry-After to honour (an in-stream
// error carries none) the waits are 2 s, 4 s, 8 s; each failed attempt's
// partial reasoning and text are thrown away whole, so the answer that
// finally lands is the retry's own and the transcript carries none of the
// abandoned ones. Retries are not rounds.
func TestProviderRetryExponential(t *testing.T) {
	failing := func() providerStep {
		return providerStep{chunks: []llm.Chunk{{Reasoning: "thinking ZEBRA"}, {Text: "partial ZEBRA"}, inStreamErr(502, "overloaded")}}
	}
	steps := []providerStep{failing(), failing(), failing(), okRound("full answer")}
	l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})

	events, err := sendTurn(t, l, fx, "retry me three times")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := sl.Sleeps(); !reflect.DeepEqual(got, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}) {
		t.Fatalf("slept %v, want [2s 4s 8s]", got)
	}
	want := []RetryEv{
		{Round: 1, Attempt: 1, Reason: "provider 502: overloaded; retrying in 2s"},
		{Round: 1, Attempt: 2, Reason: "provider 502: overloaded; retrying in 4s"},
		{Round: 1, Attempt: 3, Reason: "provider 502: overloaded; retrying in 8s"},
	}
	if got := retryEvents(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("RetryEv = %#v, want %#v", got, want)
	}
	reqs := fake.Requests()
	if len(reqs) != 4 {
		t.Fatalf("Stream called %d times, want 4", len(reqs))
	}
	for i := 1; i < len(reqs); i++ {
		if !reflect.DeepEqual(reqs[0], reqs[i]) {
			t.Fatalf("request %d is not deep-equal to request 1:\nfirst: %+v\nsent:  %+v", i+1, reqs[0], reqs[i])
		}
	}
	last := events[len(events)-1]
	if done, ok := last.(DoneEv); !ok || done.Reason != "stop" || done.Rounds != 1 {
		t.Fatalf("last event = %#v, want DoneEv{stop, 1} — a retry is not a round", last)
	}

	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	var assistant *Record
	for i := range sess.Records {
		r := sess.Records[i]
		if strings.Contains(r.Content, "ZEBRA") || strings.Contains(r.Reasoning, "ZEBRA") {
			t.Errorf("record %d leaked an abandoned attempt's output: %+v", i, r)
		}
		if r.Role == "assistant" {
			assistant = &sess.Records[i]
		}
	}
	if assistant == nil || assistant.Content != "full answer" {
		t.Fatalf("assistant record = %+v, want Content %q", assistant, "full answer")
	}
}

// TestProviderRetryGivesUp: three retries is the budget. The fourth refusal
// fails the turn, and the provider's own error — status, message — is still
// in the chain for the caller to classify.
func TestProviderRetryGivesUp(t *testing.T) {
	steps := []providerStep{
		{chunks: []llm.Chunk{inStreamErr(502, "overloaded")}},
		{chunks: []llm.Chunk{inStreamErr(502, "overloaded")}},
		{chunks: []llm.Chunk{inStreamErr(502, "overloaded")}},
		{chunks: []llm.Chunk{inStreamErr(502, "overloaded")}},
	}
	l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})

	events, err := sendTurn(t, l, fx, "never works")
	var pe *llm.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("Send error = %v, want one with a *llm.ProviderError in its chain", err)
	}
	if pe.Status != 502 || !pe.InStream {
		t.Errorf("ProviderError = %+v, want Status 502, InStream", pe)
	}
	if got := fake.Calls(); got != 4 {
		t.Errorf("Stream called %d times, want exactly 4 (the first try and three retries)", got)
	}
	if got := sl.Sleeps(); !reflect.DeepEqual(got, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}) {
		t.Errorf("slept %v, want [2s 4s 8s]", got)
	}
	if got := len(retryEvents(events)); got != 3 {
		t.Errorf("got %d RetryEv, want 3", got)
	}
	if n := errorEvents(events); n != 1 {
		t.Errorf("got %d ErrorEv, want exactly 1: %#v", n, events)
	}
}

// TestProviderNotRetryable: a refusal that re-sending cannot fix fails the
// turn on the first call — no RetryEv, no wait. The 402 case is the
// out-of-credit one: only a 402 that names a wait is a retry.
func TestProviderNotRetryable(t *testing.T) {
	cases := []struct {
		name string
		step providerStep
	}{
		{"http_401", providerStep{err: &llm.ProviderError{Status: 401, Message: "bad key"}}},
		{"http_402_without_retry_after", providerStep{err: &llm.ProviderError{Status: 402, Message: "out of credit"}}},
		{"http_404", providerStep{err: &llm.ProviderError{Status: 404, Message: "Not Found"}}},
		{"in_stream_401", providerStep{chunks: []llm.Chunk{inStreamErr(401, "bad key")}}},
		{"plain_error", providerStep{err: errors.New("dial tcp: connection refused")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, fx, fake, sl := newProviderLoop(t, []providerStep{c.step}, LoopConfig{})

			events, err := sendTurn(t, l, fx, "refused")
			if err == nil {
				t.Fatalf("Send returned nil, want the refusal")
			}
			if got := fake.Calls(); got != 1 {
				t.Errorf("Stream called %d times, want 1", got)
			}
			if got := len(retryEvents(events)); got != 0 {
				t.Errorf("got %d RetryEv, want 0", got)
			}
			if got := sl.Sleeps(); len(got) != 0 {
				t.Errorf("slept %v, want no wait", got)
			}
			if n := errorEvents(events); n != 1 {
				t.Errorf("got %d ErrorEv, want exactly 1", n)
			}
		})
	}
}

// TestProviderLongRetryAfterNotRetried: a Retry-After past a minute is a
// quota reset, not a wait — the turn fails at once instead of sitting in a
// wait nobody asked for.
func TestProviderLongRetryAfterNotRetried(t *testing.T) {
	steps := []providerStep{
		{err: &llm.ProviderError{Status: 429, Message: "daily quota", RetryAfter: 3600 * time.Second}},
	}
	l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})

	events, err := sendTurn(t, l, fx, "over quota")
	var pe *llm.ProviderError
	if !errors.As(err, &pe) || pe.Status != 429 {
		t.Fatalf("Send error = %v, want the 429 ProviderError", err)
	}
	if got := fake.Calls(); got != 1 {
		t.Errorf("Stream called %d times, want 1", got)
	}
	if got := sl.Sleeps(); len(got) != 0 {
		t.Errorf("slept %v, want no wait", got)
	}
	if got := len(retryEvents(events)); got != 0 {
		t.Errorf("got %d RetryEv, want 0", got)
	}
}

// TestProviderRetryAfterBounds pins the edges of the Retry-After rule: a
// minute exactly is waited out, a sub-second wait is honoured as given (and
// shown rounded up, so the line never says "0s"), and a second past the
// minute is not waited out.
func TestProviderRetryAfterBounds(t *testing.T) {
	t.Run("sixty_seconds_is_waited", func(t *testing.T) {
		steps := []providerStep{
			{err: &llm.ProviderError{Status: 429, Message: "wait", RetryAfter: 60 * time.Second}},
			okRound("ok"),
		}
		l, fx, _, sl := newProviderLoop(t, steps, LoopConfig{})
		events, err := sendTurn(t, l, fx, "x")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got := sl.Sleeps(); !reflect.DeepEqual(got, []time.Duration{60 * time.Second}) {
			t.Errorf("slept %v, want [1m0s]", got)
		}
		if got := retryEvents(events); len(got) != 1 || got[0].Reason != "provider 429: wait; retrying in 60s" {
			t.Errorf("RetryEv = %#v", got)
		}
	})

	t.Run("sub_second_is_honoured_and_rounded_up_in_the_line", func(t *testing.T) {
		steps := []providerStep{
			{err: &llm.ProviderError{Status: 503, Message: "busy", RetryAfter: 1500 * time.Millisecond}},
			okRound("ok"),
		}
		l, fx, _, sl := newProviderLoop(t, steps, LoopConfig{})
		events, err := sendTurn(t, l, fx, "x")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got := sl.Sleeps(); !reflect.DeepEqual(got, []time.Duration{1500 * time.Millisecond}) {
			t.Errorf("slept %v, want [1.5s]", got)
		}
		if got := retryEvents(events); len(got) != 1 || got[0].Reason != "provider 503: busy; retrying in 2s" {
			t.Errorf("RetryEv = %#v", got)
		}
	})

	t.Run("over_sixty_seconds_is_not_waited", func(t *testing.T) {
		steps := []providerStep{
			{err: &llm.ProviderError{Status: 429, Message: "wait", RetryAfter: 60*time.Second + time.Millisecond}},
		}
		l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})
		if _, err := sendTurn(t, l, fx, "x"); err == nil {
			t.Fatalf("Send returned nil, want the refusal")
		}
		if fake.Calls() != 1 || len(sl.Sleeps()) != 0 {
			t.Errorf("calls=%d sleeps=%v, want 1 call and no wait", fake.Calls(), sl.Sleeps())
		}
	})

	t.Run("a_402_that_names_a_wait_is_retried", func(t *testing.T) {
		steps := []providerStep{
			{err: &llm.ProviderError{Status: 402, Message: "in flight budget", RetryAfter: 5 * time.Second}},
			okRound("ok"),
		}
		l, fx, _, sl := newProviderLoop(t, steps, LoopConfig{})
		if _, err := sendTurn(t, l, fx, "x"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got := sl.Sleeps(); !reflect.DeepEqual(got, []time.Duration{5 * time.Second}) {
			t.Errorf("slept %v, want [5s]", got)
		}
	})

	t.Run("retry_after_spent_budget_still_applies", func(t *testing.T) {
		// Three waits the provider named are three retries: the fourth
		// refusal fails the turn, however short its Retry-After.
		refuse := providerStep{err: &llm.ProviderError{Status: 429, Message: "wait", RetryAfter: time.Second}}
		l, fx, fake, sl := newProviderLoop(t, []providerStep{refuse, refuse, refuse, refuse}, LoopConfig{})
		if _, err := sendTurn(t, l, fx, "x"); err == nil {
			t.Fatalf("Send returned nil, want the refusal")
		}
		if fake.Calls() != 4 || len(sl.Sleeps()) != 3 {
			t.Errorf("calls=%d sleeps=%v, want 4 calls and 3 waits", fake.Calls(), sl.Sleeps())
		}
	})
}

// TestProviderRetryReason pins the line a consumer is shown: the code in
// D1's form (status, else the string code, else "unknown"), the provider's
// message cut to its first 120 runes (never mid-rune), and the wait.
func TestProviderRetryReason(t *testing.T) {
	long := strings.Repeat("é", 300)
	cases := []struct {
		name string
		pe   *llm.ProviderError
		want string
	}{
		{"status", &llm.ProviderError{Status: 502, Message: "overloaded", InStream: true},
			"provider 502: overloaded; retrying in 2s"},
		{"string_code", &llm.ProviderError{Code: "server_error", Message: "Provider disconnected", InStream: true},
			"provider server_error: Provider disconnected; retrying in 2s"},
		{"no_code", &llm.ProviderError{Message: "the provider ended the stream", InStream: true},
			"provider unknown: the provider ended the stream; retrying in 2s"},
		{"message_cut_at_120_runes", &llm.ProviderError{Status: 502, Message: long, InStream: true},
			"provider 502: " + strings.Repeat("é", 120) + "; retrying in 2s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			steps := []providerStep{{chunks: []llm.Chunk{{Err: c.pe}}}, okRound("ok")}
			l, fx, _, _ := newProviderLoop(t, steps, LoopConfig{})
			events, err := sendTurn(t, l, fx, "x")
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			got := retryEvents(events)
			if len(got) != 1 || got[0].Reason != c.want {
				t.Fatalf("RetryEv = %#v, want Reason %q", got, c.want)
			}
		})
	}
}

// TestProviderErrorAfterToolCallContinues is 035 case (A) for a provider
// error: the round dispatched a tool call and THEN the provider said it
// failed. The call already ran, so re-sending would run it twice — the round
// ends as if it had finished and the turn goes on to round 2, with no wait
// and no RetryEv.
func TestProviderErrorAfterToolCallContinues(t *testing.T) {
	steps := []providerStep{
		{chunks: []llm.Chunk{toolCallChunk("call-1", "wiki.search", `{}`), inStreamErr(502, "overloaded")}},
		okRound("done"),
	}
	l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})

	events, err := sendTurn(t, l, fx, "search, then the provider fails")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	toolCalls := 0
	for _, ev := range events {
		switch ev.(type) {
		case ToolCallEv:
			toolCalls++
		case RetryEv, ErrorEv:
			t.Fatalf("want no RetryEv and no ErrorEv when a call was already dispatched, got %#v", events)
		}
	}
	if toolCalls != 1 {
		t.Fatalf("tool dispatched %d times, want exactly once: %#v", toolCalls, events)
	}
	if got := sl.Sleeps(); len(got) != 0 {
		t.Errorf("slept %v, want no wait", got)
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
	if done, ok := last.(DoneEv); !ok || done.Reason != "stop" || done.Rounds != 2 {
		t.Fatalf("last event = %#v, want DoneEv{stop, 2}", last)
	}
}

// TestProviderRetryCancelDuringWait: the user's cancel lands while the loop
// is waiting out a rate limit. Send returns context.Canceled at once and
// never streams again; a canceled turn has no terminal event of its own.
func TestProviderRetryCancelDuringWait(t *testing.T) {
	steps := []providerStep{
		{err: &llm.ProviderError{Status: 429, Message: "slow down", RetryAfter: 30 * time.Second}},
		okRound("must never be asked for"),
	}
	l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sl.hook = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}

	out := make(chan Event, 256)
	err := l.Send(ctx, fx.csID, "cancel while waiting", out)
	events := drain(out)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send error = %v, want context.Canceled", err)
	}
	if got := fake.Calls(); got != 1 {
		t.Errorf("Stream called %d times, want 1 — no call after the cancel", got)
	}
	if got := sl.Sleeps(); !reflect.DeepEqual(got, []time.Duration{30 * time.Second}) {
		t.Errorf("slept %v, want [30s]", got)
	}
	for _, ev := range events {
		switch ev.(type) {
		case DoneEv, ErrorEv:
			t.Errorf("a canceled turn emitted a terminal event: %#v", events)
		}
	}
}

// TestTruncationPolicyUnchanged: 035's own tests (stream_cut_test.go) pass
// unmodified; this adds the one thing they cannot see from outside the new
// code — a cut stream is NOT a provider error, so it is retried once with no
// wait and fails with ErrStreamTruncated, never with a ProviderError; and the
// two budgets are separate, so a provider retry does not spend the cut's.
func TestTruncationPolicyUnchanged(t *testing.T) {
	t.Run("cut_retries_once_without_a_wait", func(t *testing.T) {
		steps := []providerStep{
			{chunks: []llm.Chunk{{Text: "partial"}, cutChunk()}},
			okRound("full"),
		}
		l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})
		events, err := sendTurn(t, l, fx, "x")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		want := []RetryEv{{Round: 1, Attempt: 1, Reason: "stream ended early"}}
		if got := retryEvents(events); !reflect.DeepEqual(got, want) {
			t.Errorf("RetryEv = %#v, want %#v", got, want)
		}
		if got := sl.Sleeps(); len(got) != 0 {
			t.Errorf("slept %v, want no wait for a cut stream", got)
		}
		if fake.Calls() != 2 {
			t.Errorf("Stream called %d times, want 2", fake.Calls())
		}
	})

	t.Run("second_cut_fails_with_the_sentinel_not_a_provider_error", func(t *testing.T) {
		steps := []providerStep{
			{chunks: []llm.Chunk{cutChunk()}},
			{chunks: []llm.Chunk{cutChunk()}},
		}
		l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})
		_, err := sendTurn(t, l, fx, "x")
		var pe *llm.ProviderError
		if !errors.Is(err, llm.ErrStreamTruncated) || errors.As(err, &pe) {
			t.Fatalf("Send error = %v, want ErrStreamTruncated and no ProviderError", err)
		}
		if fake.Calls() != 2 || len(sl.Sleeps()) != 0 {
			t.Errorf("calls=%d sleeps=%v, want 2 calls and no wait", fake.Calls(), sl.Sleeps())
		}
	})

	t.Run("a_provider_retry_does_not_spend_the_cut_retry", func(t *testing.T) {
		steps := []providerStep{
			{err: &llm.ProviderError{Status: 503, Message: "busy"}},
			{chunks: []llm.Chunk{{Text: "partial"}, cutChunk()}},
			okRound("full"),
		}
		l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})
		events, err := sendTurn(t, l, fx, "x")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		want := []RetryEv{
			{Round: 1, Attempt: 1, Reason: "provider 503: busy; retrying in 2s"},
			{Round: 1, Attempt: 1, Reason: "stream ended early"},
		}
		if got := retryEvents(events); !reflect.DeepEqual(got, want) {
			t.Errorf("RetryEv = %#v, want %#v", got, want)
		}
		if got := sl.Sleeps(); !reflect.DeepEqual(got, []time.Duration{2 * time.Second}) {
			t.Errorf("slept %v, want [2s] (the cut retry waits for nothing)", got)
		}
		if fake.Calls() != 3 {
			t.Errorf("Stream called %d times, want 3", fake.Calls())
		}
	})
}

// TestProviderRetryBudgetIsPerRound: the three retries belong to a round, not
// to the turn — round 2 starts with a full budget of its own.
func TestProviderRetryBudgetIsPerRound(t *testing.T) {
	refuse := func() providerStep { return providerStep{chunks: []llm.Chunk{inStreamErr(503, "busy")}} }
	steps := []providerStep{
		refuse(), refuse(), refuse(),
		{chunks: []llm.Chunk{toolCallChunk("call-1", "wiki.search", `{}`), {Finish: "tool_calls"}}},
		refuse(), refuse(), refuse(),
		okRound("ok"),
	}
	l, fx, _, sl := newProviderLoop(t, steps, LoopConfig{})

	events, err := sendTurn(t, l, fx, "two rough rounds")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := len(sl.Sleeps()); got != 6 {
		t.Errorf("waited %d times, want 6 (three in each round)", got)
	}
	var rounds []int
	for _, r := range retryEvents(events) {
		rounds = append(rounds, r.Round)
	}
	if !reflect.DeepEqual(rounds, []int{1, 1, 1, 2, 2, 2}) {
		t.Errorf("RetryEv rounds = %v, want [1 1 1 2 2 2]", rounds)
	}
	last := events[len(events)-1]
	if done, ok := last.(DoneEv); !ok || done.Reason != "stop" || done.Rounds != 2 {
		t.Errorf("last event = %#v, want DoneEv{stop, 2}", last)
	}
}

// TestProviderRetryTrace: the trace keeps every attempt visible, as 035's
// does — the refused attempt's response carries the error, each retry event
// names the attempt that will be sent, and the request files are numbered
// 1, 2, 3. The second retry waits the SECOND backoff step (4 s) even though
// the first used the provider's own Retry-After: the steps count retries, not
// retries that fell back to them.
func TestProviderRetryTrace(t *testing.T) {
	fx := newTestLoopFixture(t)
	fake := &scriptedStreamer{
		observe: true,
		steps: []providerStep{
			{err: &llm.ProviderError{Status: 429, Message: "slow down", RetryAfter: time.Second}},
			{chunks: []llm.Chunk{{Text: "partial "}, inStreamErr(502, "overloaded")}},
			okRound("recovered"),
		},
	}
	sl := &sleepLog{}
	dir := t.TempDir()
	l := newLoop(fake, fx.reg, fx.store, fx.engine, LoopConfig{TraceDir: dir})
	l.sleep = sl.sleep

	if _, err := sendTurn(t, l, fx, "trace the retries"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	turn, err := trace.Load(dir, turnID(t, fx))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantRetries := []trace.RetryInfo{
		{Round: 1, Attempt: 2, Reason: "provider 429: slow down; retrying in 1s"},
		{Round: 1, Attempt: 3, Reason: "provider 502: overloaded; retrying in 4s"},
	}
	if !reflect.DeepEqual(turn.Retries, wantRetries) {
		t.Errorf("retries = %+v, want %+v", turn.Retries, wantRetries)
	}
	if len(turn.Attempts) != 3 {
		t.Fatalf("Load gave %d attempts, want 3: %+v", len(turn.Attempts), turn.Attempts)
	}
	for i, a := range turn.Attempts {
		if a.Attempt != i+1 {
			t.Errorf("attempt %d numbered %d", i+1, a.Attempt)
		}
	}
	if r := turn.Attempts[0].Response; r == nil || !strings.Contains(r.Error, "429") {
		t.Errorf("attempt 1 response = %+v, want the 429 error recorded", r)
	}
	if r := turn.Attempts[1].Response; r == nil || r.Text != "partial " || !strings.Contains(r.Error, "overloaded") {
		t.Errorf("attempt 2 response = %+v, want the partial text and the 502 error kept", r)
	}
	if turn.Done == nil || turn.Done.Reason != "stop" || turn.Done.Rounds != 1 {
		t.Errorf("done = %+v, want {stop, 1}", turn.Done)
	}
}

// TestProviderRetryDoesNotInheritFinish: a refusal can follow a finish_reason
// (llm emits the error chunk even after one), and the re-sent attempt starts
// clean — a stale "length" from the refused attempt must not turn the retry's
// ordinary answer into an output-cap failure.
func TestProviderRetryDoesNotInheritFinish(t *testing.T) {
	steps := []providerStep{
		{chunks: []llm.Chunk{{Text: "cut off"}, {Finish: "length"}, inStreamErr(502, "overloaded")}},
		{chunks: []llm.Chunk{{Text: "the answer"}}}, // ends with no finish_reason of its own
	}
	l, fx, _, _ := newProviderLoop(t, steps, LoopConfig{})

	events, err := sendTurn(t, l, fx, "x")
	if err != nil {
		t.Fatalf("Send: %v — the refused attempt's finish_reason leaked into the retry", err)
	}
	last := events[len(events)-1]
	if done, ok := last.(DoneEv); !ok || done.Reason != "stop" {
		t.Fatalf("last event = %#v, want DoneEv{stop}", last)
	}
}

// TestProviderRetryDiscardsAbandonedAttempt: what the refused attempt had
// streamed — thinking and text — is gone from the round's one assistant
// message, not just from the transcript. The message is only observable on
// the wire, in the NEXT round's request, so the retried attempt calls a tool.
func TestProviderRetryDiscardsAbandonedAttempt(t *testing.T) {
	steps := []providerStep{
		{chunks: []llm.Chunk{{Reasoning: "stale thinking"}, {Text: "stale text"}, inStreamErr(503, "busy")}},
		{chunks: []llm.Chunk{{Reasoning: "plan"}, {Text: "searching"}, toolCallChunk("call-1", "wiki.search", `{}`), {Finish: "tool_calls"}}},
		okRound("done"),
	}
	l, fx, fake, _ := newProviderLoop(t, steps, LoopConfig{})

	if _, err := sendTurn(t, l, fx, "x"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	reqs := fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("Stream called %d times, want 3", len(reqs))
	}
	var assistant *llm.Message
	for i, m := range reqs[2].Messages {
		if m.Role == "assistant" {
			assistant = &reqs[2].Messages[i]
		}
	}
	if assistant == nil {
		t.Fatalf("round 2's request has no assistant message: %+v", reqs[2].Messages)
	}
	if assistant.Content != "searching" || assistant.ReasoningContent != "plan" {
		t.Errorf("assistant message = content %q reasoning %q, want %q and %q — the refused attempt's output leaked",
			assistant.Content, assistant.ReasoningContent, "searching", "plan")
	}
}

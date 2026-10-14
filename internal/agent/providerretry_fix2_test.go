package agent

// providerretry_fix2_test.go pins 061 FIX2's agent half, from the branch review:
//
//   R2  a transport failure (the request got no response) is retried like a
//       503: with the real llm.Client against a closed port, the loop waits
//       2 s, 4 s, 8 s and re-sends — and when the port comes back, succeeds.
//   R10 two things the first suites could not see (surviving mutants): an
//       in-stream refusal the provider does NOT call transient still fails the
//       turn after a dispatched call, and a canceled ctx wins over a refusal
//       that arrives in the same instant.
//   R7/R4/R1 through the whole stack: the retry line carries no control
//       character, a permanent in-stream code is not retried, a healthy stream
//       stamped with an empty "error" is not a failure.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/awepo-pro/lw/internal/llm"
)

// okSSE is a complete, healthy one-message answer in the chat-completions wire
// shape.
const okSSE = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{}}]}\n\ndata: [DONE]\n\n"

// closedPort is a loopback address nobody listens on, plus a function that
// brings an SSE server up there — the "provider comes back" half of a transport
// test, called from the injected wait.
func closedPort(t *testing.T) (addr string, bringUp func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr = l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var once sync.Once
	bringUp = func() {
		once.Do(func() {
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, okSSE)
			}))
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				t.Errorf("re-listen on %s: %v", addr, err)
				return
			}
			srv.Listener.Close()
			srv.Listener = ln
			srv.Start()
			t.Cleanup(srv.Close)
		})
	}
	return addr, bringUp
}

// newWireLoop is a Loop over the real llm.Client pointed at baseURL, its wait
// injected.
func newWireLoop(t *testing.T, baseURL string) (*Loop, *testLoopFixture, *sleepLog) {
	t.Helper()
	fx := newTestLoopFixture(t)
	sl := &sleepLog{}
	l := newLoop(llm.New(llm.Config{BaseURL: baseURL, Model: "test-model"}), fx.reg, fx.store, fx.engine, LoopConfig{})
	l.sleep = sl.sleep
	return l, fx, sl
}

// TestProviderTransportRetryThenSucceeds: attempt 1 is refused (nothing is
// listening), the injected wait brings the provider up, attempt 2 answers.
func TestProviderTransportRetryThenSucceeds(t *testing.T) {
	addr, bringUp := closedPort(t)
	l, fx, sl := newWireLoop(t, "http://"+addr)
	sl.hook = func(ctx context.Context) error { bringUp(); return nil }

	events, err := sendTurn(t, l, fx, "x")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := retryEvents(events)
	if len(got) != 1 || got[0].Round != 1 || got[0].Attempt != 1 ||
		!strings.HasPrefix(got[0].Reason, "provider network: ") ||
		!strings.Contains(got[0].Reason, "connection refused") ||
		!strings.HasSuffix(got[0].Reason, "; retrying in 2s") {
		t.Fatalf("RetryEv = %#v, want one \"provider network: …connection refused…; retrying in 2s\"", got)
	}
	if want := []time.Duration{2 * time.Second}; !reflect.DeepEqual(sl.Sleeps(), want) {
		t.Errorf("slept %v, want %v", sl.Sleeps(), want)
	}
	last := events[len(events)-1]
	if done, ok := last.(DoneEv); !ok || done.Reason != "stop" {
		t.Errorf("last event = %#v, want DoneEv{stop}", last)
	}
}

// TestProviderTransportGivesUp: a provider that never comes back fails the turn
// after three waits, the transport ProviderError intact in the chain (so `lw
// ingest` can keep what it staged).
func TestProviderTransportGivesUp(t *testing.T) {
	addr, _ := closedPort(t)
	l, fx, sl := newWireLoop(t, "http://"+addr)

	events, err := sendTurn(t, l, fx, "x")
	var pe *llm.ProviderError
	if !errors.As(err, &pe) || !pe.Transport {
		t.Fatalf("Send error = %v, want a Transport *llm.ProviderError in its chain", err)
	}
	if want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}; !reflect.DeepEqual(sl.Sleeps(), want) {
		t.Errorf("slept %v, want %v", sl.Sleeps(), want)
	}
	if got := len(retryEvents(events)); got != 3 {
		t.Errorf("got %d RetryEv, want 3", got)
	}
	if !strings.HasPrefix(err.Error(), "agent: stream: llm: request: ") {
		t.Errorf("error = %q, want the plain `llm: request:` text under agent: stream:", err)
	}
}

// TestProviderTransportCancelIsNotRetried: the user's cancel while a request is
// in flight is a canceled turn, not a network failure to wait out.
func TestProviderTransportCancelIsNotRetried(t *testing.T) {
	addr, _ := closedPort(t)
	l, fx, sl := newWireLoop(t, "http://"+addr)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := make(chan Event, 64)
	err := l.Send(ctx, fx.csID, "x", out)
	events := drain(out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send error = %v, want context.Canceled", err)
	}
	if len(sl.Sleeps()) != 0 || len(retryEvents(events)) != 0 {
		t.Errorf("a canceled turn waited/retried: sleeps %v, events %#v", sl.Sleeps(), events)
	}
}

// TestProviderStallIsNotRetried: a stall already waited its whole stall_timeout
// (and stays a failure that keeps `lw ingest`'s ops, as before); the loop never
// waits it out again.
func TestProviderStallIsNotRetried(t *testing.T) {
	stalled := fmt.Errorf("llm: read stream: %w", llm.ErrStalled)
	for name, step := range map[string]providerStep{
		"at the request": {err: fmt.Errorf("llm: request: %w", llm.ErrStalled)},
		"mid stream":     {chunks: []llm.Chunk{{Text: "partial"}, {Err: stalled}}},
	} {
		t.Run(name, func(t *testing.T) {
			l, fx, fake, sl := newProviderLoop(t, []providerStep{step, okRound("never")}, LoopConfig{})
			events, err := sendTurn(t, l, fx, "x")
			if !errors.Is(err, llm.ErrStalled) {
				t.Fatalf("Send error = %v, want ErrStalled in the chain", err)
			}
			if fake.Calls() != 1 || len(sl.Sleeps()) != 0 || len(retryEvents(events)) != 0 {
				t.Errorf("calls=%d sleeps=%v retries=%d, want 1 call and no retry", fake.Calls(), sl.Sleeps(), len(retryEvents(events)))
			}
		})
	}
}

// TestProviderNonRetryableAfterToolCallFailsTurn (R10): after a dispatched call
// an in-stream refusal the provider does not call transient is not "carry on" —
// that is only for ones a retry could have fixed. A 401 behind a call fails the
// turn, so a bad key is reported, not looped on round after round.
func TestProviderNonRetryableAfterToolCallFailsTurn(t *testing.T) {
	steps := []providerStep{
		{chunks: []llm.Chunk{toolCallChunk("call-1", "wiki.search", `{}`), inStreamErr(401, "bad key")}},
		okRound("must never be asked for"),
	}
	l, fx, fake, sl := newProviderLoop(t, steps, LoopConfig{})

	events, err := sendTurn(t, l, fx, "search, then the key is refused")
	var pe *llm.ProviderError
	if !errors.As(err, &pe) || pe.Status != 401 {
		t.Fatalf("Send error = %v, want the 401 ProviderError", err)
	}
	if fake.Calls() != 1 {
		t.Errorf("Stream called %d times, want 1 — the round must not continue", fake.Calls())
	}
	tools := 0
	for _, ev := range events {
		if _, ok := ev.(ToolCallEv); ok {
			tools++
		}
	}
	if tools != 1 || len(sl.Sleeps()) != 0 || len(retryEvents(events)) != 0 || errorEvents(events) != 1 {
		t.Errorf("tools=%d sleeps=%v retries=%d errors=%d, want 1 tool, no wait/retry, 1 ErrorEv", tools, sl.Sleeps(), len(retryEvents(events)), errorEvents(events))
	}
}

// cancelOnToolRecord is a SessionStore that cancels the turn's ctx when the
// loop appends a tool record — the last thing dispatchToolCall does, so the
// cancel lands after the call ran and before the loop's next select.
type cancelOnToolRecord struct {
	SessionStore
	cancel context.CancelFunc
}

func (s *cancelOnToolRecord) Append(id string, r Record) error {
	err := s.SessionStore.Append(id, r)
	if r.Role == "tool" {
		s.cancel()
	}
	return err
}

// TestProviderCancelAfterToolCallReturnsCtxErr (R10): the user's cancel and the
// provider's refusal are both ready when the loop selects. Go picks between two
// ready cases at random, so the turn is run many times: every one must end as
// a canceled turn — ctx.Err(), no further request, no terminal event — never as
// a round that carries on or a ProviderError (which `lw ingest` would keep the
// ops for, against a user who aborted). A mutant that drops the ctx check fails
// in about half the runs; 64 runs make surviving it a 1 in 2^64 chance.
func TestProviderCancelAfterToolCallReturnsCtxErr(t *testing.T) {
	for i := 0; i < 64; i++ {
		fx := newTestLoopFixture(t)
		steps := []providerStep{
			{preload: true, chunks: []llm.Chunk{toolCallChunk("call-1", "wiki.search", `{}`), inStreamErr(502, "overloaded")}},
			okRound("must never be asked for"),
		}
		fake := &scriptedStreamer{steps: steps}
		ctx, cancel := context.WithCancel(context.Background())
		store := &cancelOnToolRecord{SessionStore: fx.store, cancel: cancel}
		l := newLoop(fake, fx.reg, store, fx.engine, LoopConfig{})
		l.sleep = (&sleepLog{}).sleep

		out := make(chan Event, 256)
		err := l.Send(ctx, fx.csID, "x", out)
		events := drain(out)
		cancel()

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: Send error = %v, want context.Canceled", i, err)
		}
		if fake.Calls() != 1 {
			t.Fatalf("run %d: Stream called %d times, want 1 — the round carried on after the cancel", i, fake.Calls())
		}
		for _, ev := range events {
			switch ev.(type) {
			case ErrorEv, DoneEv:
				t.Fatalf("run %d: a canceled turn emitted a terminal event: %#v", i, events)
			}
		}
	}
}

// wireTurn runs a turn against an SSE server whose every response is body (or,
// when handler is set, whatever it writes) and returns what the loop did.
func wireTurn(t *testing.T, handler http.HandlerFunc) (events []Event, err error, sl *sleepLog, hits func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	l, fx, sl := newWireLoop(t, srv.URL)
	events, err = sendTurn(t, l, fx, "x")
	return events, err, sl, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// TestProviderRetryReasonHasNoControlCharacters (R7, whole stack): the retry
// line goes to a terminal, and the provider's message is its own bytes.
func TestProviderRetryReasonHasNoControlCharacters(t *testing.T) {
	var calls int
	var mu sync.Mutex
	events, err, sl, _ := wireTurn(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if first {
			fmt.Fprint(w, "data: {\"error\":{\"code\":502,\"message\":\"\\u001b[31mX\\ny\"}}\n\n")
			return
		}
		fmt.Fprint(w, okSSE)
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := retryEvents(events)
	if len(got) != 1 {
		t.Fatalf("RetryEv = %#v, want one", got)
	}
	if want := "provider 502: [31mX y; retrying in 2s"; got[0].Reason != want {
		t.Errorf("Reason = %q, want %q", got[0].Reason, want)
	}
	for _, r := range got[0].Reason {
		if unicode.IsControl(r) {
			t.Errorf("Reason carries the control character %U: %q", r, got[0].Reason)
		}
	}
	if len(sl.Sleeps()) != 1 {
		t.Errorf("slept %v, want one wait", sl.Sleeps())
	}
}

// TestProviderPermanentInStreamCodeIsNotRetried (R4, whole stack): an in-stream
// error whose string code says the request is the problem fails the turn at
// once — no wait, no second request.
func TestProviderPermanentInStreamCodeIsNotRetried(t *testing.T) {
	_, err, sl, hits := wireTurn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"This model's maximum context length is 8192 tokens\",\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\"}}\n\n")
	})
	var pe *llm.ProviderError
	if !errors.As(err, &pe) || pe.Code != "context_length_exceeded" {
		t.Fatalf("Send error = %v, want the context_length_exceeded ProviderError", err)
	}
	if hits() != 1 || len(sl.Sleeps()) != 0 {
		t.Errorf("provider saw %d request(s), slept %v — want one request and no wait", hits(), sl.Sleeps())
	}
}

// TestProviderHealthyStreamWithEmptyErrorIsNotRetried (R1, whole stack): a
// gateway that stamps "error":{"message":"","code":null} on every chunk is
// still a healthy provider — the turn finishes on the first request.
func TestProviderHealthyStreamWithEmptyErrorIsNotRetried(t *testing.T) {
	events, err, sl, hits := wireTurn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"\",\"code\":null},\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n"+
			"data: {\"error\":{\"message\":\"\",\"code\":null},\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{}}]}\n\ndata: [DONE]\n\n")
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if hits() != 1 || len(sl.Sleeps()) != 0 || len(retryEvents(events)) != 0 {
		t.Errorf("requests=%d sleeps=%v retries=%d, want a single clean request", hits(), sl.Sleeps(), len(retryEvents(events)))
	}
}

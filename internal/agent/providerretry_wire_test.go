package agent

// providerretry_wire_test.go runs 061 FIX1 A-061-3 through the real client:
// the retry line is built from llm.ProviderError.Message, and Message is the
// sentence inside an HTTP error body — so a real 429 reads
// `provider 429: Rate limit exceeded: free-models-per-min; retrying in 2s`,
// not the JSON body around it (the s47 live run printed the body). The
// providerretry_test.go suite scripts typed errors; this is the one test that
// lets llm.Client build the error from bytes on a socket.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
)

func TestProviderRetryReasonUsesUnwrappedMessage(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"Rate limit exceeded: free-models-per-min","code":429}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n"+
			"data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	fx := newTestLoopFixture(t)
	sl := &sleepLog{}
	l := newLoop(llm.New(llm.Config{BaseURL: srv.URL, Model: "test-model"}), fx.reg, fx.store, fx.engine, LoopConfig{})
	l.sleep = sl.sleep

	events, err := sendTurn(t, l, fx, "x")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	want := []RetryEv{{Round: 1, Attempt: 1, Reason: "provider 429: Rate limit exceeded: free-models-per-min; retrying in 2s"}}
	if got := retryEvents(events); !reflect.DeepEqual(got, want) {
		t.Errorf("RetryEv = %#v, want %#v", got, want)
	}
	if got := sl.Sleeps(); !reflect.DeepEqual(got, []time.Duration{2 * time.Second}) {
		t.Errorf("slept %v, want [2s] (the Retry-After)", got)
	}
}

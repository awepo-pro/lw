// carry_loop_test.go is TestConversationCarry's model_sees_previous_answer
// subtest, split out by the file-size rule: it is the one carry scenario
// that runs the REAL agent.Loop (MASTER §5 T-C, TestConversationCarry) —
// over a loopback httptest SSE server that records each request's decoded
// messages, the pattern internal/agent's own reasoning_persist_test.go
// established — and asserts the second provider request carries turn 1's
// answer, which is what seeding is for.
package ask

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
)

// sseRecorder is the fake provider: every /chat/completions POST is decoded
// far enough to keep the request's messages, and answered with the next
// scripted answer as one content delta and a clean stop — one round, no
// tool calls, so each Loop.Send ends in DoneEv{stop} having staged nothing.
type sseRecorder struct {
	mu      sync.Mutex
	answers []string
	reqs    [][]llm.Message
}

// serve is the handler; it is a method so the recorder owns its own lock.
func (rec *sseRecorder) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var wire struct {
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		http.Error(w, "decode request", http.StatusBadRequest)
		return
	}

	rec.mu.Lock()
	n := len(rec.reqs)
	answer := ""
	if n < len(rec.answers) {
		answer = rec.answers[n]
	}
	rec.reqs = append(rec.reqs, wire.Messages)
	rec.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", answer)
	fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// recorded returns the decoded messages of the n'th Stream call.
func (rec *sseRecorder) recorded(n int) []llm.Message {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if n >= len(rec.reqs) {
		return nil
	}
	return rec.reqs[n]
}

// count returns how many Stream calls have arrived.
func (rec *sseRecorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.reqs)
}

// TestConversationCarry/model_sees_previous_answer lives here (its t.Run is
// in carry_test.go's parent): the real Loop must put turn 1's answer on the
// wire in turn 2's request — the carried record rendered back as history.
func modelSeesPreviousAnswer(t *testing.T) {
	root, engine := carryVault(t)
	sessions := agent.NewFileSessions(root)
	reg := tools.NewRegistry(tools.Deps{
		Vault:  engine.Vault(),
		Index:  engine.Index(),
		Engine: engine,
		Author: stage.Author{Kind: "agent"},
	})

	rec := &sseRecorder{answers: []string{"A1", "A2"}}
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)
	client := llm.New(llm.Config{BaseURL: srv.URL, Model: "lw-test"})
	loop := agent.NewLoop(client, reg, sessions, engine, agent.LoopConfig{})

	m := New(liveDeps(t, engine, loop)).(*Model)
	m = submitAndDrain(t, m, "q1")
	m = submitAndDrain(t, m, "q2")

	if got := rec.count(); got != 2 {
		t.Fatalf("the provider saw %d requests, want one per turn (2)", got)
	}

	// Non-vacuous guards: turn 2's request must carry the turn's own
	// question (the loop really ran) before we believe it also carries the
	// previous answer.
	second := rec.recorded(1)
	var sawQ2, sawA1 bool
	for _, msg := range second {
		switch {
		case msg.Role == "user" && msg.Content == "q2":
			sawQ2 = true
		case msg.Role == "assistant" && msg.Content == "A1":
			sawA1 = true
		}
	}
	if !sawQ2 {
		t.Fatalf("the second request does not carry turn 2's own question:\n%+v", second)
	}
	if !sawA1 {
		t.Fatalf("the second request never carries turn 1's answer A1 — the model sees a stranger:\n%+v", second)
	}
}

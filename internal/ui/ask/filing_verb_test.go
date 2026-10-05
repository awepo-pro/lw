package ask

// filing_verb_test.go is 039's pin on the verb the pane tags a turn with. The
// agent picks a turn's prompt and tool set from the ctx verb: "ask" is a
// read-only question, so a ctrl+s filing turn — which must stage a query page
// — cannot ride "ask" any more. It runs under "file", which the agent treats
// as a curator turn. Permanent regression test (D-10C).

import (
	"context"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/trace"
)

// verbRecordingAgent is fakeTurnAgent that also records the verb on each
// Send's ctx — the one thing the pane tells the loop about what kind of turn
// this is.
type verbRecordingAgent struct {
	*fakeTurnAgent

	mu    sync.Mutex
	verbs []string
}

func (v *verbRecordingAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	v.mu.Lock()
	v.verbs = append(v.verbs, trace.VerbFrom(ctx))
	v.mu.Unlock()
	return v.fakeTurnAgent.Send(ctx, sessionID, msg, out)
}

func (v *verbRecordingAgent) sentVerbs() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.verbs...)
}

// TestFilingTurnVerbIsFile drives a question and then ctrl+s through the real
// pane: the question's Send ctx carries verb "ask", the filing turn's carries
// verb "file".
func TestFilingTurnVerbIsFile(t *testing.T) {
	_, engine, _ := queryVault(t)
	ag := &verbRecordingAgent{fakeTurnAgent: &fakeTurnAgent{
		sessions: agent.NewFileSessions(engine.Vault().Root()),
		script: []agent.Event{
			agent.TextDelta{Text: fileKeyAnswer},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		},
	}}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m = submitAndDrain(t, m, fileKeyQuestion)
	if got := ag.sentVerbs(); len(got) != 1 || got[0] != "ask" {
		t.Fatalf("a normal question's Send verbs = %q, want [ask]", got)
	}

	pane, cmd := pressCtrlS(m)
	m = pane.(*Model)
	if cmd == nil {
		t.Fatal("ctrl+s produced no command")
	}
	var seen []tea.Msg
	runCmd(t, m, cmd, &seen)

	got := ag.sentVerbs()
	if len(got) != 2 || got[0] != "ask" || got[1] != "file" {
		t.Fatalf("Send verbs = %q, want [ask file]: the filing turn must not run as an ask turn", got)
	}
}

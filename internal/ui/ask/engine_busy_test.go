// engine_busy_test.go is 008 A-801's ask-side evidence (MASTER R-802): the
// pane reports EngineBusy from the moment a turn starts (submit) until its
// terminal event — DoneEv, ErrorEv, or the stream's end — has been
// processed by Update, then reports idle. The state it reads is the
// turnActive flag submitInput, endTurn, endTurnError and StreamClosedMsg
// already maintain (G5 review I-1: the shell must not reload under a live
// turn's goroutine).
package ask

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
)

func TestAskEngineBusy(t *testing.T) {
	t.Run("busy_during_turn", func(t *testing.T) {
		root, engine, _ := liveVault(t)
		release := make(chan struct{})
		entered := make(chan struct{})
		ag := &fakeTurnAgent{
			sessions: agent.NewFileSessions(root),
			release:  release,
			entered:  entered,
			script:   []agent.Event{agent.TextDelta{Text: "working on it"}, agent.DoneEv{Reason: "stop", Rounds: 1}},
		}
		m := New(liveDeps(t, engine, ag)).(*Model)
		if m.EngineBusy() {
			t.Fatal("a pane that never submitted reports EngineBusy")
		}

		m, cmd := typeAndSubmit(t, m, "what is a kv cache?")
		if cmd == nil {
			t.Fatal("submit produced no command")
		}
		if !m.EngineBusy() {
			t.Fatal("EngineBusy = false at submit, want true")
		}

		<-entered // the turn goroutine is inside Send right now
		if !m.EngineBusy() {
			t.Fatal("EngineBusy = false while the turn is live, want true")
		}

		close(release)
		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model)
		if m.EngineBusy() {
			t.Fatal("EngineBusy = true after DoneEv was processed, want false")
		}

		// The other terminal event ends the busy window the same way: a
		// turn whose script ends in ErrorEv is busy from its submit until
		// Update has processed the error.
		agErr := &fakeTurnAgent{
			sessions: agent.NewFileSessions(root),
			script:   []agent.Event{agent.ErrorEv{Err: errors.New("the provider died mid-turn")}},
		}
		m = New(liveDeps(t, engine, agErr)).(*Model)
		m, cmd = typeAndSubmit(t, m, "and if it errors?")
		if cmd == nil {
			t.Fatal("the error turn's submit produced no command")
		}
		if !m.EngineBusy() {
			t.Fatal("EngineBusy = false at the error turn's submit, want true")
		}
		m = runCmd(t, m, cmd, &seen).(*Model)
		if m.EngineBusy() {
			t.Fatal("EngineBusy = true after ErrorEv was processed, want false")
		}
	})

	t.Run("idle_after_turn", func(t *testing.T) {
		root, engine, _ := liveVault(t)
		ag := &fakeTurnAgent{
			sessions: agent.NewFileSessions(root),
			script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
		}
		m := New(liveDeps(t, engine, ag)).(*Model)

		m, cmd := typeAndSubmit(t, m, "what is a kv cache?")
		if !m.EngineBusy() {
			t.Fatal("EngineBusy = false at submit, want true")
		}
		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model) // drains to DoneEv AND the stream's end

		if m.EngineBusy() {
			t.Fatal("EngineBusy = true after the turn's terminal event and StreamClosedMsg, want false")
		}
		// And a turn-free key keeps it idle: busy tracks turns, not keys.
		pane, _ := m.Update(keyPress('j'))
		m = pane.(*Model)
		if m.EngineBusy() {
			t.Fatal("EngineBusy = true after a key with no turn, want false")
		}
	})
}

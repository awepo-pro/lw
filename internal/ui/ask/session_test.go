// session_test.go pins the S5-T5 session lifecycle: the empty
// ui.StageChangedMsg is how review reports "no changeset any more", and
// that is when this pane archives the session its turn ran under — once,
// only its own, through a real commit as well as a reject — and cancels a
// turn still running when its changeset goes.
package ask

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestSessionClosesWhenReviewRejects pins the S5-T5 session lifecycle: the
// empty ui.StageChangedMsg is how review reports "no changeset any more"
// (Commit and Reject both emit it), and that is when this pane archives the
// session its turn ran under — once, and only its own.
func TestSessionClosesWhenReviewRejects(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewFileSessions(root)}
	ag := &fakeTurnAgent{
		sessions: sessions,
		script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "stage something")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)
	if m.sessionID != csID {
		t.Fatalf("sessionID = %q, want %q", m.sessionID, csID)
	}
	if got := sessions.closedIDs(); len(got) != 0 {
		t.Fatalf("session closed %v before any commit/reject, want none", got)
	}

	// A populated StageChangedMsg is a change, not a removal: it must leave
	// the session alone.
	if _, cmd := m.Update(ui.StageChangedMsg{ChangesetID: csID, Ops: 2}); cmd != nil {
		t.Fatalf("populated StageChangedMsg produced a command (%#v), want nil", cmd)
	}
	if got := sessions.closedIDs(); len(got) != 0 {
		t.Fatalf("populated StageChangedMsg closed %v, want none", got)
	}

	// Engine.Reject is what review's X runs; the message it broadcasts
	// afterwards is the empty one. Closing must succeed against the real
	// store even though the changeset's directory has moved to rejected/.
	if err := engine.Reject("rejected in test"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	pane, closeCmd := m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	if closeCmd == nil {
		t.Fatal("empty StageChangedMsg produced no Close command")
	}
	outcome := closeCmd()
	sc, ok := outcome.(sessionClosedMsg)
	if !ok {
		t.Fatalf("Close command produced %#v (%T), want sessionClosedMsg", outcome, outcome)
	}
	if sc.err != nil {
		t.Fatalf("Close failed after Reject: %v", sc.err)
	}
	if got := sessions.closedIDs(); len(got) != 1 || got[0] != csID {
		t.Fatalf("closed = %v, want exactly [%s]", got, csID)
	}
	if m.sessionID != "" {
		t.Fatalf("sessionID = %q after the changeset went, want empty", m.sessionID)
	}

	// And a second empty broadcast has nothing of ours left to close.
	if _, cmd := m.Update(ui.StageChangedMsg{}); cmd != nil {
		t.Fatalf("second empty StageChangedMsg produced a command (%#v), want nil", cmd)
	}
	if got := sessions.closedIDs(); len(got) != 1 {
		t.Fatalf("closed = %v after a repeated broadcast, want exactly one close", got)
	}
}

// TestSessionClosesAfterARealCommit is the same lifecycle through
// Engine.Commit, whose directory move lands the session under
// changesets/committed/ before the broadcast arrives.
func TestSessionClosesAfterARealCommit(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewFileSessions(root)}
	ag := &fakeTurnAgent{
		sessions: sessions,
		script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "stage something")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	if _, err := engine.Commit("test commit"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".llmwiki", "changesets", "committed", csID, "session.ndjson")); err != nil {
		t.Fatalf("session did not travel with the committed changeset: %v", err)
	}

	pane, closeCmd := m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	if closeCmd == nil {
		t.Fatal("empty StageChangedMsg produced no Close command")
	}
	if sc, ok := closeCmd().(sessionClosedMsg); !ok {
		t.Fatalf("Close command produced a %#T, want sessionClosedMsg", closeCmd())
	} else if sc.err != nil {
		t.Fatalf("Close failed after Commit: %v", sc.err)
	}
	if got := sessions.closedIDs(); len(got) != 1 || got[0] != csID {
		t.Fatalf("closed = %v, want exactly [%s]", got, csID)
	}
}

// TestChangesetGoneMidTurnCancelsTheTurn pins the other half of the session
// lifecycle: a turn still running when its changeset is committed or
// rejected is cancelled rather than left writing records into an archived
// session, and its stream still closes out cleanly (backbone §9, C-105).
func TestChangesetGoneMidTurnCancelsTheTurn(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewFileSessions(root)}
	release := make(chan struct{})
	entered := make(chan struct{})
	ag := &fakeTurnAgent{
		sessions: sessions,
		release:  release,
		entered:  entered,
		script: []agent.Event{
			agent.TextDelta{Text: "still working"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "long question")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	if m.cancel == nil {
		t.Fatal("submit left the turn's context uncancellable")
	}
	<-entered // the turn is genuinely running, session and all

	// Review rejects while the turn is parked mid-flight.
	if err := engine.Reject("rejected mid-turn"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	pane, closeCmd := m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	if closeCmd == nil {
		t.Fatal("empty StageChangedMsg produced no Close command")
	}
	if m.cancel != nil {
		t.Fatal("changesetGone did not cancel the running turn")
	}
	if sc, ok := closeCmd().(sessionClosedMsg); !ok {
		t.Fatalf("Close command produced a %#T, want sessionClosedMsg", closeCmd())
	} else if sc.err != nil {
		t.Fatalf("Close failed after Reject: %v", sc.err)
	}
	close(release)

	// The channel closes out cleanly and the pane's pump terminates.
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)
	if m.turnActive {
		t.Fatal("cancelled turn left the pane marked active")
	}
	var sawClosed bool
	for _, msg := range seen {
		if _, ok := msg.(StreamClosedMsg); ok {
			sawClosed = true
		}
	}
	if !sawClosed {
		t.Fatalf("StreamClosedMsg never observed; seen = %#v", seen)
	}
	if got := sessions.closedIDs(); len(got) != 1 || got[0] != csID {
		t.Fatalf("closed = %v, want exactly [%s]", got, csID)
	}
}

// session_test.go pins the Ask pane's session lifecycle as 057 left it. The
// empty ui.StageChangedMsg is still how review reports "no changeset any more"
// (Commit and Reject both emit it), but the conversation a turn ran in is no
// longer the changeset's session, so it does not close with it: the pane keeps
// it, and cancels a turn still running only when that turn staged into the
// changeset that went away. (Before 057 the pane archived the session its turn
// ran under at that message, once and only its own; A-057-1 rewrote these three
// tests to the contract that replaced it.)
package ask

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// wantFateOnly fails the test unless cmd is nil or the title's state lookup
// (titleFateMsg) — the one thing the empty broadcast may still owe now that it
// archives no session.
func wantFateOnly(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		if _, ok := msg.(titleFateMsg); !ok {
			t.Fatalf("empty StageChangedMsg produced %#v (%T), want at most the title's state lookup", msg, msg)
		}
	}
}

// TestSessionSurvivesWhenReviewRejects: the empty ui.StageChangedMsg review
// broadcasts after Engine.Reject closes nothing and cancels nothing — the
// conversation outlives the changeset, which is the point of 057.
func TestSessionSurvivesWhenReviewRejects(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewAskSessions(agent.NewFileSessions(root))}
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
	convID := m.convID
	if convID == "" || convID == csID {
		t.Fatalf("convID = %q, want the pane's own ask- conversation", convID)
	}
	if got := sessions.closedIDs(); len(got) != 0 {
		t.Fatalf("session closed %v before any commit/reject, want none", got)
	}

	// A populated StageChangedMsg is a change, not a removal: it must leave the
	// conversation alone.
	if _, cmd := m.Update(ui.StageChangedMsg{ChangesetID: csID, Ops: 2}); cmd != nil {
		t.Fatalf("populated StageChangedMsg produced a command (%#v), want nil", cmd)
	}

	// Engine.Reject is what review's X runs; the message it broadcasts
	// afterwards is the empty one. Nothing is owed to the conversation.
	if err := engine.Reject("rejected in test"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	pane, closeCmd := m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	// The only command the broadcast may owe is the title's state lookup for
	// the changeset id the pane showed (liveVault's, seeded at New); the
	// session archive that used to be returned here is gone.
	wantFateOnly(t, closeCmd)
	if got := sessions.closedIDs(); len(got) != 0 {
		t.Fatalf("closed = %v after the reject, want the conversation kept", got)
	}
	if m.convID != convID {
		t.Fatalf("convID = %q after the changeset went, want the conversation %q kept", m.convID, convID)
	}
	if _, err := sessions.Get(convID); err != nil {
		t.Fatalf("the conversation is gone after the reject: %v", err)
	}

	// And a second empty broadcast has nothing of ours left to do.
	if _, cmd := m.Update(ui.StageChangedMsg{}); cmd != nil {
		t.Fatalf("second empty StageChangedMsg produced a command (%#v), want nil", cmd)
	}
}

// TestSessionSurvivesARealCommit is the same lifecycle through Engine.Commit,
// whose directory move lands the changeset under changesets/committed/ before
// the broadcast arrives. The turn staged nothing into it, so it left no
// session.ndjson there and the commit takes none along.
func TestSessionSurvivesARealCommit(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewAskSessions(agent.NewFileSessions(root))}
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
	convID := m.convID

	// Commit refuses a changeset with no live op (008 ErrNothingToCommit,
	// C-802), so the harness stages one valid page through the public stage
	// API before committing. Every original assertion below stays.
	if _, err := engine.Append(stage.Op{
		Kind: stage.OpCreatePage,
		Path: "wiki/concepts/session-commit-page.md",
		Content: []byte("---\ntitle: Session Commit Page\ncreated: 2026-08-29\nupdated: 2026-08-29\ntype: concept\n" +
			"tags: [inference]\nconfidence: medium\n---\n\n" +
			"# Session Commit Page\n\nSee [[kv-cache]] and [[gpt-4]] for background.\n"),
		Rationale:  "this commit needs one live op",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := engine.Commit("test commit"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	committed := filepath.Join(root, ".llmwiki", "changesets", "committed", csID)
	if _, err := os.Stat(filepath.Join(committed, "changeset.json")); err != nil {
		t.Fatalf("the commit did not land: %v", err)
	}
	if _, err := os.Stat(filepath.Join(committed, "session.ndjson")); !os.IsNotExist(err) {
		t.Fatalf("a session.ndjson travelled with the committed changeset (%v); the turn staged nothing into it", err)
	}

	pane, closeCmd := m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	wantFateOnly(t, closeCmd)
	if got := sessions.closedIDs(); len(got) != 0 {
		t.Fatalf("closed = %v after the commit, want the conversation kept", got)
	}
	if m.convID != convID {
		t.Fatalf("convID = %q after the commit, want %q", m.convID, convID)
	}
}

// TestChangesetGoneMidTurnCancelsTheTurn pins the other half of the lifecycle:
// a turn still running when the changeset it STAGED INTO is committed or
// rejected is cancelled rather than left appending records its adopt step has
// nowhere to put, and its stream still closes out cleanly (backbone §9,
// C-105). (Before 057 any turn was cancelled, staged or not.)
func TestChangesetGoneMidTurnCancelsTheTurn(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewAskSessions(agent.NewFileSessions(root))}
	ag := &gatedAgent{
		sessions: sessions,
		first: []agent.Event{
			agent.TextDelta{Text: "still working"},
			agent.StageEv{ChangesetID: csID, Ops: 1},
		},
		rest:    []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "long question")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	if m.cancel == nil {
		t.Fatal("submit left the turn's context uncancellable")
	}
	m, _ = pump(t, m, cmd) // turnStartedMsg
	<-ag.reached           // the turn is genuinely running, session and all
	m = nextEvent(t, m)    // the TextDelta
	m = nextEvent(t, m)    // the StageEv: the turn staged into csID

	// Review rejects while the turn is parked mid-flight.
	if err := engine.Reject("rejected mid-turn"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	pane, closeCmd := m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	wantFateOnly(t, closeCmd)
	if m.cancel != nil {
		t.Fatal("changesetGone did not cancel the running turn")
	}
	close(ag.release)

	// The channel closes out cleanly and the pane's pump terminates.
	var seen []tea.Msg
	m = runCmd(t, m, Listen(m.ch), &seen).(*Model)
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
	if got := sessions.closedIDs(); len(got) != 0 {
		t.Fatalf("closed = %v, want the conversation kept", got)
	}
}

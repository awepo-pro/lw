// convid_adopt_test.go covers the two edges of 057's pane that askconv_test.go's
// headline scenarios do not: the id the pane's conversation is minted under, and
// what the adopt step does when it cannot or must not run — a session store that
// refuses the write, and a changeset Review committed while the turn was still
// running (the C-118 shape: it must never be resurrected as an open/ directory
// holding only a session.ndjson). Permanent regression tests (D-10C).
package ask

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
)

func TestNewConvID(t *testing.T) {
	got := newConvID(bytes.NewReader([]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xff}))
	if want := "ask-0123456789abcdef"; got != want {
		t.Fatalf("newConvID = %q, want %q", got, want)
	}
	a, b := mintConvID(), mintConvID()
	if a == b {
		t.Fatalf("two minted ids are both %q", a)
	}
	for _, id := range []string{a, b} {
		if len(id) != len("ask-")+16 || !strings.HasPrefix(id, "ask-") {
			t.Fatalf("minted id %q is not \"ask-\" and 16 hex digits", id)
		}
	}
}

// TestAskPaneRunsEveryTurnInOneConversation: the first submit mints the pane's
// conversation and every later turn — a question or the ctrl+s filing turn —
// reuses it; a second pane (a second TUI on the vault) gets its own.
func TestAskPaneRunsEveryTurnInOneConversation(t *testing.T) {
	_, engine := carryVault(t)
	root := engine.Vault().Root()
	ag := &fakeTurnAgent{sessions: askStore(root)}
	m := New(liveDeps(t, engine, ag)).(*Model)
	if m.convID != "" {
		t.Fatalf("convID = %q before any submit, want none minted yet", m.convID)
	}

	ag.script, ag.persist = turnScript("q1", citedAnswer1)
	m = submitAndDrain(t, m, "q1")
	first := ag.gotSess
	if !strings.HasPrefix(first, "ask-") || m.convID != first {
		t.Fatalf("first turn ran in %q (convID %q), want the pane's ask- conversation", first, m.convID)
	}

	ag.script, ag.persist = turnScript("q2", "A2")
	m = submitAndDrain(t, m, "q2")
	if ag.gotSess != first {
		t.Fatalf("second turn ran in %q, want the same conversation %q", ag.gotSess, first)
	}

	// ctrl+s on a sourced answer: the filing turn is the third turn of the
	// same conversation. Turn 2's answer is unsourced, so run a sourced one.
	ag.script, ag.persist = turnScript("q3", citedAnswer2)
	m = submitAndDrain(t, m, "q3")
	ag.script, ag.persist = nil, nil // before ctrl+s: its turn starts reading them at once
	pane, cmd := pressCtrlS(m)
	m = pane.(*Model)
	if cmd == nil {
		t.Fatal("ctrl+s produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)
	if ag.gotSess != first {
		t.Fatalf("the filing turn ran in %q, want the same conversation %q", ag.gotSess, first)
	}

	other := New(liveDeps(t, engine, &fakeTurnAgent{sessions: askStore(root)})).(*Model)
	other.deps.Agent.(*fakeTurnAgent).script = []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}}
	other = submitAndDrain(t, other, "hello")
	if other.convID == "" || other.convID == first {
		t.Fatalf("a second pane's conversation is %q, want its own (first was %q)", other.convID, first)
	}
}

// failingAppendSessions refuses to write any record to a changeset session
// (every id that is not an ask- conversation): the adopt step's write fails,
// the conversation's does not.
type failingAppendSessions struct {
	agent.SessionStore
}

func (f failingAppendSessions) Append(id string, r agent.Record) error {
	if strings.HasPrefix(id, "ask-") {
		return f.SessionStore.Append(id, r)
	}
	return errors.New("disk full")
}

// TestAskTurnAdoptFailureIsAStatusLine: an adopt that fails never fails the
// finished turn. The answer and the done line stand, the changeset the turn
// staged into stays open, and the failure is told once, after the terminal
// line, instead of vanishing into lw.log.
func TestAskTurnAdoptFailureIsAStatusLine(t *testing.T) {
	root, engine := carryVault(t)
	ag := &fakeTurnAgent{sessions: failingAppendSessions{SessionStore: askStore(root)}}
	var csID string
	ag.mid = func() []agent.Event {
		cs, _, err := engine.OpenOrJoin("ask: q1", stage.Author{Kind: "agent"})
		if err != nil {
			t.Errorf("OpenOrJoin: %v", err)
			return nil
		}
		csID = cs.ID
		stageKeepOp(t, engine)
		return []agent.Event{agent.StageEv{ChangesetID: cs.ID, Ops: 1}}
	}
	ag.script, ag.persist = turnScript("q1", "A1")
	m := New(liveDeps(t, engine, ag)).(*Model)

	m = submitAndDrain(t, m, "q1")

	if m.turnActive {
		t.Fatal("the turn is still active")
	}
	if cs, err := engine.Current(); err != nil || cs.ID != csID {
		t.Fatalf("Current = %+v, %v; want the changeset the turn staged into still open", cs, err)
	}
	n := len(m.entries)
	if n < 2 {
		t.Fatalf("scrollback holds %d entries", n)
	}
	done, status := m.entries[n-2], m.entries[n-1]
	if done.kind != kindStatus || done.text != "done · 1 rounds" {
		t.Fatalf("second-to-last entry = %#v, want the done line (the turn finished)", done)
	}
	if status.kind != kindStatus || !strings.HasPrefix(status.text, "could not save this turn into its changeset's session: ") || !strings.Contains(status.text, "disk full") {
		t.Fatalf("last entry = %#v, want the adopt failure as a status line", status)
	}
	if m.turnErrored {
		t.Fatal("an adopt failure marked the finished turn as errored")
	}
}

// TestAskTurnStagedChangesetGoneIsNotAdopted: Review commits the changeset a
// turn staged into before the turn's events are all relayed. The turn is not
// adopted into a changeset that has left changesets/open/, and nothing is
// resurrected there — no open/<id> directory, no session.ndjson in committed/ —
// because a directory in open/ with a session.ndjson and no changeset.json
// makes Engine.Current fail for every verb after it (C-118).
func TestAskTurnStagedChangesetGoneIsNotAdopted(t *testing.T) {
	root, engine := carryVault(t)
	ag := &fakeTurnAgent{sessions: askStore(root)}
	var csID string
	ag.mid = func() []agent.Event {
		cs, _, err := engine.OpenOrJoin("ask: q1", stage.Author{Kind: "agent"})
		if err != nil {
			t.Errorf("OpenOrJoin: %v", err)
			return nil
		}
		csID = cs.ID
		stageKeepOp(t, engine)
		if _, err := engine.Commit("review accepted mid-turn"); err != nil {
			t.Errorf("Commit: %v", err)
		}
		return []agent.Event{agent.StageEv{ChangesetID: cs.ID, Ops: 1}}
	}
	ag.script, ag.persist = turnScript("q1", "A1")
	m := New(liveDeps(t, engine, ag)).(*Model)

	m = submitAndDrain(t, m, "q1")

	if csID == "" {
		t.Fatal("the fake turn never staged")
	}
	state := filepath.Join(root, ".llmwiki", "changesets")
	if _, err := os.Stat(filepath.Join(state, "open", csID)); !os.IsNotExist(err) {
		t.Fatalf("open/%s exists after the commit (%v): the adopt step resurrected a gone changeset", csID, err)
	}
	if _, err := os.Stat(filepath.Join(state, "committed", csID, "session.ndjson")); !os.IsNotExist(err) {
		t.Fatalf("a session.ndjson was appended into committed/%s (%v); a closed changeset's transcript is not adopted into", csID, err)
	}
	if _, err := engine.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Fatalf("Current after the turn = %v, want ErrNoChangeset (the engine is not broken)", err)
	}
	for _, e := range m.entries {
		if strings.Contains(e.text, "could not save") {
			t.Fatalf("entry %#v reports an adopt failure for a changeset that simply went away", e)
		}
	}
	if last := lastEntry(m); last.kind != kindStatus || last.text != "done · 1 rounds" {
		t.Fatalf("last entry = %#v, want the done line", last)
	}
}

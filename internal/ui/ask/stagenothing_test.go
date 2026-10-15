// stagenothing_test.go is 057 FIX1's pane half: a turn whose staging call
// lazily opened a changeset and then staged nothing leaves no changeset behind,
// as the pane's own auto-reject used to guarantee, and the pane neither adopts
// into it nor cancels the turn that was finishing. Real loop, real engine, the
// production store (askconv_test.go's scripted provider). Permanent regression
// tests (D-10C).
package ask

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// refusedCreatePageArgs is valid JSON stage.create_page refuses (no rationale,
// no body): the call runs and comes back IsError, having staged nothing.
const refusedCreatePageArgs = `{"path":"wiki/queries/refused.md"}`

// TestAskTurnFailedStageRejectsItsEmptyChangeset: ctrl+s files an answer, the
// model's one stage.create_page is refused, and it gives up. The changeset the
// loop opened for that call is rejected with the pane's old reason; nothing is
// adopted into it (no session.ndjson in rejected/), nothing is left open, and
// the turn still reads as finished.
func TestAskTurnFailedStageRejectsItsEmptyChangeset(t *testing.T) {
	root, engine := carryVault(t)
	p := newScriptedProvider(t,
		textRound(citedAnswer1),
		toolRound("call-1", "stage_create_page", refusedCreatePageArgs),
		textRound("I could not stage that page."),
	)
	loop := askLoop(t, engine, agent.NewFileSessions(root), p)
	m := New(liveDeps(t, engine, loop)).(*Model)
	m, _ = submitCollect(t, m, "what is a kv cache?")

	pane, cmd := pressCtrlS(m)
	m = pane.(*Model)
	if cmd == nil {
		t.Fatal("ctrl+s produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	if cs, err := engine.Current(); err == nil {
		t.Fatalf("changeset %s is open after a turn that staged nothing", cs.ID)
	}
	dirs := changesetDirNames(t, root)
	if len(dirs) != 1 || !strings.HasPrefix(dirs[0], "rejected/") {
		t.Fatalf("changeset directories = %v, want exactly the one rejected", dirs)
	}
	if _, err := os.Stat(filepath.Join(root, ".llmwiki", "changesets", dirs[0], "session.ndjson")); !os.IsNotExist(err) {
		t.Fatalf("a session.ndjson was adopted into the rejected changeset (%v)", err)
	}
	journal := string(journalBytes(t, root))
	if !strings.Contains(journal, `"changeset_rejected"`) || !strings.Contains(journal, `"message":"ask turn staged nothing"`) {
		t.Fatalf("journal lacks the old reject reason:\n%s", journal)
	}
	if m.turnActive {
		t.Fatal("the turn is still active")
	}
	if got := lastEntry(m); got.kind != kindStatus || !strings.HasPrefix(got.text, "done ·") {
		t.Fatalf("last entry = %#v, want the done line", got)
	}
	for _, e := range m.entries {
		if strings.Contains(e.text, "could not save") {
			t.Fatalf("entry %#v reports an adopt failure for a changeset that was rejected", e)
		}
	}
	if m.stagedID != "" {
		t.Fatalf("stagedID = %q after the reject, want none", m.stagedID)
	}
}

// TestAskTurnLeavesAnotherVerbsEmptyChangesetAlone: an `lw ingest` left a
// changeset open (nothing staged yet) and the ask turn's staging call is
// refused. The turn did not open that changeset, so it does not reject it.
func TestAskTurnLeavesAnotherVerbsEmptyChangesetAlone(t *testing.T) {
	root, engine := carryVault(t)
	p := newScriptedProvider(t,
		textRound(citedAnswer1),
		toolRound("call-1", "stage_create_page", refusedCreatePageArgs),
		textRound("I could not stage that page."),
	)
	loop := askLoop(t, engine, agent.NewFileSessions(root), p)
	m := New(liveDeps(t, engine, loop)).(*Model)
	m, _ = submitCollect(t, m, "what is a kv cache?")

	opened, err := engine.OpenChangeset("lw ingest a.md", stage.Author{Kind: "agent"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	journal := journalBytes(t, root)

	pane, cmd := pressCtrlS(m)
	m = pane.(*Model)
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	cs, err := engine.Current()
	if err != nil || cs.ID != opened.ID || cs.Intent != "lw ingest a.md" {
		t.Fatalf("Current = %+v, %v; want the other verb's changeset untouched", cs, err)
	}
	if dirs := changesetDirNames(t, root); len(dirs) != 1 || dirs[0] != "open/"+opened.ID {
		t.Fatalf("changeset directories = %v, want only open/%s", dirs, opened.ID)
	}
	if after := journalBytes(t, root); !bytes.Equal(after, journal) {
		t.Fatalf("the refused call wrote to the journal:\n before %q\n after  %q", journal, after)
	}
	if sc := stageChangedMsgs(seen); len(sc) != 0 {
		t.Fatalf("a refused call against another verb's changeset broadcast ui.StageChangedMsg %+v", sc)
	}
}

// TestOwnEmptyStageEvClearsStagedID: the empty StageEv a turn streams says the
// changeset it staged into is gone (the loop rejected it, or it was never
// there). The pane must forget stagedID on seeing it — otherwise the
// ui.StageChangedMsg{} that follows, which cannot be told from a Review verdict,
// reads as "the changeset this turn staged into went away" and cancels the turn
// at the very moment it is delivering its terminal event.
func TestOwnEmptyStageEvClearsStagedID(t *testing.T) {
	m := New(newTestDeps(t)).(*Model)
	cancelled := false
	m.cancel = func() { cancelled = true }
	m.turnActive = true

	pane, _ := m.Update(EventMsg{Ev: agent.StageEv{ChangesetID: "cs-x", Ops: 1}})
	m = pane.(*Model)
	if m.stagedID != "cs-x" {
		t.Fatalf("stagedID = %q after a StageEv for cs-x", m.stagedID)
	}
	pane, _ = m.Update(EventMsg{Ev: agent.StageEv{}})
	m = pane.(*Model)
	if m.stagedID != "" {
		t.Fatalf("stagedID = %q after the turn's own empty StageEv, want it forgotten", m.stagedID)
	}
	pane, _ = m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	if cancelled {
		t.Fatal("the empty StageChangedMsg that follows the turn's own empty StageEv cancelled the turn")
	}
}

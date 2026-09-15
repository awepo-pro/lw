// revert_test.go pins `r` (pinned item 5): on a row carrying a commit id
// it calls Engine.Revert and emits ui.StageChangedMsg then
// ui.SwitchScreenMsg{ScreenReview}; on a row without one it is a visible
// refusal that makes no engine call; a failing Revert renders, never
// panics.
package logview

import (
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestRevertOnCommitRowProducesNewChangesetAndSwitches is pinned item 5:
// `r` on a row carrying a commit id calls Engine.Revert and emits
// ui.StageChangedMsg then ui.SwitchScreenMsg{ScreenReview}.
func TestRevertOnCommitRowProducesNewChangesetAndSwitches(t *testing.T) {
	d, e := newTestDeps(t, "minimal")

	if _, err := e.OpenChangeset("add a page", stage.Author{Kind: "agent", Model: "test-model"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/seed-page.md",
		Content:    conceptPageContent("Seed Page"),
		Rationale:  "test seed",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append create_page: %v", err)
	}
	commitID, err := e.Commit("add a page")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	p := initModel(t, d)
	m := p.(*Model)

	idx := -1
	for i, ev := range m.events {
		if ev.Kind == stage.EvCommitEnd && ev.Commit == commitID {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no commit_end row for %s in %+v", commitID, m.events)
	}
	m.cursor = idx

	_, cmd := m.handleKey(keyMsg("r"))
	msgs := collectMsgs(t, cmd)

	var gotStageChanged *ui.StageChangedMsg
	var gotSwitch *ui.SwitchScreenMsg
	for _, msg := range msgs {
		switch v := msg.(type) {
		case eventsMsg:
			// queryCmd's own reload, batched alongside the two shell
			// messages (pinned item 5) — expected, not asserted on here.
		case ui.StageChangedMsg:
			gotStageChanged = &v
		case ui.SwitchScreenMsg:
			gotSwitch = &v
		}
	}
	if gotStageChanged == nil {
		t.Fatalf("no ui.StageChangedMsg among %#v", msgs)
	}
	if gotStageChanged.ChangesetID == "" {
		t.Fatal("ui.StageChangedMsg.ChangesetID is empty; want the new reverted changeset's id")
	}
	if gotStageChanged.ChangesetID == commitID {
		t.Fatal("ui.StageChangedMsg.ChangesetID equals the reverted commit id; want a new changeset id")
	}
	if gotStageChanged.Ops == 0 {
		t.Fatal("ui.StageChangedMsg.Ops is 0; the revert should have proposed at least one op")
	}
	if gotSwitch == nil {
		t.Fatalf("no ui.SwitchScreenMsg among %#v", msgs)
	}
	if gotSwitch.To != ui.ScreenReview {
		t.Fatalf("SwitchScreenMsg.To = %v, want ui.ScreenReview", gotSwitch.To)
	}

	// The engine really does carry a new open changeset now.
	cur, err := e.Current()
	if err != nil {
		t.Fatalf("Current after revert: %v", err)
	}
	if cur.ID != gotStageChanged.ChangesetID {
		t.Fatalf("Current().ID = %q, want %q", cur.ID, gotStageChanged.ChangesetID)
	}
}

// TestRevertOnNonCommitRowRefusesAndMakesNoEngineCall is pinned item 5's
// other half: a row with no commit id (Event.Commit == "") is refused
// visibly and never calls Engine.Revert.
func TestRevertOnNonCommitRowRefusesAndMakesNoEngineCall(t *testing.T) {
	d, e := newTestDeps(t, "minimal")

	if _, err := e.OpenChangeset("add a page", stage.Author{Kind: "agent", Model: "test-model"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	p := initModel(t, d)
	m := p.(*Model)

	idx := -1
	for i, ev := range m.events {
		if ev.Kind == stage.EvChangesetOpened {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no changeset_opened row in %+v", m.events)
	}
	if m.events[idx].Commit != "" {
		t.Fatalf("changeset_opened row unexpectedly carries a commit id: %+v", m.events[idx])
	}
	m.cursor = idx

	before, err := e.Current()
	if err != nil {
		t.Fatalf("Current before `r`: %v", err)
	}

	_, cmd := m.handleKey(keyMsg("r"))
	if cmd != nil {
		t.Fatal("`r` on a non-commit row returned a non-nil Cmd; it must make no engine call")
	}
	if m.status == "" {
		t.Fatal("`r` on a non-commit row set no status; want a one-line refusal")
	}

	after, err := e.Current()
	if err != nil {
		t.Fatalf("Current after `r`: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("the open changeset changed (%q -> %q) after a refused revert", before.ID, after.ID)
	}
}

// TestRevertErrorRendersVisiblyNeverPanics asserts a failing
// Engine.Revert (an id with no such commit) is caught and rendered, never
// panics.
func TestRevertErrorRendersVisiblyNeverPanics(t *testing.T) {
	d, e := newTestDeps(t, "minimal")
	_ = e

	m := &Model{
		deps:    d,
		hasLoad: true,
		events:  []stage.Event{{Kind: stage.EvCommitEnd, Commit: "999999"}},
	}

	_, cmd := m.handleKey(keyMsg("r"))
	if cmd != nil {
		t.Fatal("a failing Revert returned a non-nil Cmd; want status only")
	}
	if m.status == "" {
		t.Fatal("a failing Revert set no status")
	}
}

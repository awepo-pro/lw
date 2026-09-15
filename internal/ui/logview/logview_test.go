// logview_test.go covers the model's lifecycle away from `r`: headless
// construction, the five filter states' subsets, View's never-panic
// contract, the theme-copy rule and the reload triggers.
package logview

import (
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestNewConstructibleWithNilEngine matches S4-T2's own "constructible
// headless" contract: a pane built with no Engine at all must not panic,
// and must still answer Title/Help/View.
func TestNewConstructibleWithNilEngine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}

	p := New(ui.Deps{Theme: theme, Keys: keys})
	if p == nil {
		t.Fatal("New returned nil")
	}
	if got := p.Title(); got != "Log" {
		t.Fatalf("Title() = %q, want %q", got, "Log")
	}
	if len(p.Help()) == 0 {
		t.Fatal("Help() returned no bindings")
	}

	p = runCmd(t, p, p.Init())
	if got := p.View(80, 24); got == "" {
		t.Fatal("View with no engine returned an empty string")
	}
}

// TestFiveFiltersSelectRightSubset seeds a mixed history (agent commit,
// human revert-then-reject) and drives `f` through all five states
// (pinned item 4), asserting each selects exactly the subset backbone
// §5.7's Filter contract promises.
func TestFiveFiltersSelectRightSubset(t *testing.T) {
	d, e := newTestDeps(t, "minimal")
	commitID := seedHistory(t, e)

	p := initModel(t, d)
	m := p.(*Model)

	if m.filter != filterAll {
		t.Fatalf("initial filter = %v, want filterAll", m.filter)
	}
	allEvents := append([]stage.Event(nil), m.events...)
	if len(allEvents) == 0 {
		t.Fatal("filterAll returned no events; seedHistory produced nothing")
	}
	for _, ev := range allEvents {
		if ev.Actor.Kind != "agent" && ev.Actor.Kind != "human" {
			t.Fatalf("event %s has unexpected Actor.Kind %q", ev.Kind, ev.Actor.Kind)
		}
	}

	// accepted: only EvOpAccepted (no writer in v0.1) or EvCommitEnd.
	_, cmd := m.handleKey(keyMsg("f"))
	p = runCmd(t, m, cmd)
	m = p.(*Model)
	if m.filter != filterAccepted {
		t.Fatalf("filter after one `f` = %v, want filterAccepted", m.filter)
	}
	if len(m.events) == 0 {
		t.Fatal("filterAccepted returned no events; want at least commit_end")
	}
	for _, ev := range m.events {
		if ev.Kind != stage.EvOpAccepted && ev.Kind != stage.EvCommitEnd {
			t.Fatalf("filterAccepted returned a %s event", ev.Kind)
		}
	}
	sawCommitEnd := false
	for _, ev := range m.events {
		if ev.Kind == stage.EvCommitEnd && ev.Commit == commitID {
			sawCommitEnd = true
		}
	}
	if !sawCommitEnd {
		t.Fatal("filterAccepted is missing the seeded commit_end")
	}

	// rejected: EvOpDropped, EvHunkDropped or EvChangesetRejected.
	_, cmd = m.handleKey(keyMsg("f"))
	p = runCmd(t, m, cmd)
	m = p.(*Model)
	if m.filter != filterRejected {
		t.Fatalf("filter after two `f` = %v, want filterRejected", m.filter)
	}
	if len(m.events) == 0 {
		t.Fatal("filterRejected returned no events; want at least changeset_rejected")
	}
	for _, ev := range m.events {
		switch ev.Kind {
		case stage.EvOpDropped, stage.EvHunkDropped, stage.EvChangesetRejected:
		default:
			t.Fatalf("filterRejected returned a %s event", ev.Kind)
		}
	}

	// agent: every seeded agent-authored event (changeset_opened,
	// op_proposed, commit_begin, commit_end).
	_, cmd = m.handleKey(keyMsg("f"))
	p = runCmd(t, m, cmd)
	m = p.(*Model)
	if m.filter != filterAgent {
		t.Fatalf("filter after three `f` = %v, want filterAgent", m.filter)
	}
	if len(m.events) == 0 {
		t.Fatal("filterAgent returned no events")
	}
	for _, ev := range m.events {
		if ev.Actor.Kind != "agent" {
			t.Fatalf("filterAgent returned an event with Actor.Kind %q", ev.Actor.Kind)
		}
	}
	agentCount := len(m.events)

	// human: the revert's changeset_opened/op_proposed/reverted plus the
	// reject's changeset_rejected.
	_, cmd = m.handleKey(keyMsg("f"))
	p = runCmd(t, m, cmd)
	m = p.(*Model)
	if m.filter != filterHuman {
		t.Fatalf("filter after four `f` = %v, want filterHuman", m.filter)
	}
	if len(m.events) == 0 {
		t.Fatal("filterHuman returned no events")
	}
	for _, ev := range m.events {
		if ev.Actor.Kind != "human" {
			t.Fatalf("filterHuman returned an event with Actor.Kind %q", ev.Actor.Kind)
		}
	}
	humanCount := len(m.events)

	if agentCount+humanCount != len(allEvents) {
		t.Fatalf("agent(%d) + human(%d) = %d, want len(all) = %d",
			agentCount, humanCount, agentCount+humanCount, len(allEvents))
	}

	sawReverted := false
	for _, ev := range m.events {
		if ev.Kind == stage.EvReverted && ev.Commit == commitID {
			sawReverted = true
		}
	}
	if !sawReverted {
		t.Fatal("filterHuman is missing the seeded reverted event")
	}

	// f a fifth time returns to all.
	_, cmd = m.handleKey(keyMsg("f"))
	p = runCmd(t, m, cmd)
	m = p.(*Model)
	if m.filter != filterAll {
		t.Fatalf("filter after five `f` = %v, want filterAll (wraps around)", m.filter)
	}
	if len(m.events) != len(allEvents) {
		t.Fatalf("filterAll after wrap = %d events, want %d", len(m.events), len(allEvents))
	}
}

// TestViewNeverPanics drives View across a spread of sizes, including
// degenerate ones, both before and after the journal loads, and with an
// empty event list.
func TestViewNeverPanics(t *testing.T) {
	d, _ := newTestDeps(t, "minimal")
	m := New(d)

	sizes := [][2]int{{0, 0}, {-1, -1}, {1, 1}, {40, 3}, {120, 40}, {200, 200}}
	for _, s := range sizes {
		_ = m.View(s[0], s[1])
	}

	m = runCmd(t, m, m.Init())
	for _, s := range sizes {
		_ = m.View(s[0], s[1])
	}
}

// TestBackgroundColorMsgRebuildsThemeCopy is pinned item 6 (C-81): this
// pane holds its own copy of Theme and rebuilds it locally on
// tea.BackgroundColorMsg, never touching the shell's.
func TestBackgroundColorMsgRebuildsThemeCopy(t *testing.T) {
	d, _ := newTestDeps(t, "minimal")
	p := initModel(t, d)
	m := p.(*Model)

	wasDark := m.theme.IsDark
	newColor := color.White
	if !wasDark {
		newColor = color.Black
	}
	next, _ := m.Update(tea.BackgroundColorMsg{Color: newColor})
	m2 := next.(*Model)
	if m2.theme.IsDark == wasDark {
		t.Fatalf("theme.IsDark unchanged (%v) after a polarity-flipping BackgroundColorMsg", wasDark)
	}
	if d.Theme.IsDark != m.deps.Theme.IsDark {
		t.Fatal("d.Theme was mutated by construction; New must take a copy")
	}
}

// TestVaultReloadedTriggersReload asserts ui.VaultReloadedMsg schedules a
// fresh journal query rather than being ignored (pinned item 6/TD-4).
func TestVaultReloadedTriggersReload(t *testing.T) {
	d, _ := newTestDeps(t, "minimal")
	p := initModel(t, d)
	m := p.(*Model)

	next, cmd := m.Update(ui.VaultReloadedMsg{})
	if cmd == nil {
		t.Fatal("VaultReloadedMsg did not schedule a reload Cmd")
	}
	p = runCmd(t, next, cmd)
	m2 := p.(*Model)
	if !m2.hasLoad {
		t.Fatalf("journal failed to reload after VaultReloadedMsg: %v", m2.loadErr)
	}
}

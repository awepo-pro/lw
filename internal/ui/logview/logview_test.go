package logview

import (
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

// newTestDeps builds ui.Deps with a real Engine over a private copy of
// fixture, and lw's compiled-in Theme/KeyMap. XDG_CONFIG_HOME is pointed
// at an empty temp dir so these tests never pick up a real user config
// (mirrors internal/ui/review's and internal/ui/browse's own helper of the
// same name, in a different package).
func newTestDeps(t *testing.T, fixture string) (ui.Deps, *stage.Engine) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := testutil.CopyFixture(t, fixture)
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}

	return ui.Deps{Engine: e, Theme: theme, Keys: keys}, e
}

// keyMsg builds a tea.KeyPressMsg for a literal key name this screen
// matches on, or a bare printable rune — the same shape
// internal/ui/browse's own (unexported, different-package) helper uses.
// Never tea.KeyMsg (C-80).
func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	default:
		r := []rune(s)
		return tea.KeyPressMsg{Code: r[0], Text: s}
	}
}

// runCmd executes cmd, if non-nil, against m, expanding any tea.BatchMsg it
// returns into its constituent commands and feeding every resulting
// message back through m.Update, until nothing is left to run. Headless
// tests drive Update/View directly; this is the harness's stand-in for
// what tea.Program's event loop would otherwise do (mirrors
// internal/ui/review's own helper of the same name, in a different
// package — never Program.Run(), which does not return on EOF, C-83).
func runCmd(t *testing.T, m ui.Pane, cmd tea.Cmd) ui.Pane {
	t.Helper()
	if cmd == nil {
		return m
	}
	return feedMsg(t, m, cmd())
}

func feedMsg(t *testing.T, m ui.Pane, msg tea.Msg) ui.Pane {
	t.Helper()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = runCmd(t, m, c)
		}
		return m
	}
	updated, cmd := m.Update(msg)
	return runCmd(t, updated, cmd)
}

// collectMsgs runs cmd (which may be a tea.Batch of several commands) and
// returns every tea.Msg it produces, in the order Batch's own slice holds
// them — without feeding any of them back through Update. Used where a
// test must assert exactly which messages a key press emits, rather than
// their effect once applied.
func collectMsgs(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectMsgs(t, c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// initModel constructs a logview.Model over d and drives its Init() to
// completion (running the first journal query, if an engine is present).
func initModel(t *testing.T, d ui.Deps) ui.Pane {
	t.Helper()
	m := New(d)
	return runCmd(t, m, m.Init())
}

// conceptPageContent returns a well-formed wiki/concepts page body, valid
// against the minimal fixture's SCHEMA.md taxonomy and its own
// validateCreatePage rule (>= 2 outbound wikilinks to pages the minimal
// fixture already ships) — the same shape internal/stage's own
// (unexported, different-package) newConceptPageContent test helper
// builds.
func conceptPageContent(title string) []byte {
	return []byte("---\n" +
		"title: " + title + "\n" +
		"created: 2026-08-29\n" +
		"updated: 2026-08-29\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n# " + title + "\n\n" +
		"See [[kv-cache]] and [[gpt-4]] for background.\n")
}

// seedHistory drives e through one full commit-then-revert-then-reject
// cycle, producing at least one event of every kind the five filters
// (pinned item 4) need to distinguish:
//
//   - changeset_opened, op_proposed, commit_begin, commit_end — all
//     Actor.Kind == "agent", the commit_begin/commit_end pair carrying the
//     returned commitID.
//   - changeset_opened, op_proposed (the revert's own inverse op),
//     reverted, changeset_rejected — all Actor.Kind == "human" (Revert's
//     own OpenChangeset call, backbone §5.8), the reverted event also
//     carrying commitID.
//
// Returns the commit id so a test can address its commit_begin/commit_end
// rows directly.
func seedHistory(t *testing.T, e *stage.Engine) (commitID string) {
	t.Helper()

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

	if _, err := e.Revert(commitID); err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}
	if err := e.Reject("test reject"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	return commitID
}

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

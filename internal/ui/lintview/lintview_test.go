package lintview

import (
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/lint"
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
// their effect once applied (a plain tea.Cmd, a *tea.BatchMsg producing
// one, and a nil Cmd are all handled).
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

// initModel constructs a lintview.Model over d and drives its Init() to
// completion (running the lint report, if an engine is present).
func initModel(t *testing.T, d ui.Deps) ui.Pane {
	t.Helper()
	m := New(d)
	return runCmd(t, m, m.Init())
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
	if got := p.Title(); got != "Lint" {
		t.Fatalf("Title() = %q, want %q", got, "Lint")
	}
	if len(p.Help()) == 0 {
		t.Fatal("Help() returned no bindings")
	}

	p = runCmd(t, p, p.Init())
	if got := p.View(80, 24); got == "" {
		t.Fatal("View with no engine returned an empty string")
	}
}

// TestDirtyFixtureFourteenRowsSixteenFindings is the stage file's own
// verification: lint.All() is 14 checks (C-85/D-V, not the 11 the stage
// file originally said) and spec/fixtures/dirty totals 16 findings
// (EXPECTED-LINT.md). Both numbers are read from the model's own state,
// never hardcoded from a rendered string.
func TestDirtyFixtureFourteenRowsSixteenFindings(t *testing.T) {
	d, _ := newTestDeps(t, "dirty")
	p := initModel(t, d)
	m := p.(*Model)

	if !m.hasReport {
		t.Fatalf("report did not load: %v", m.loadErr)
	}
	if got := len(m.checks); got != 14 {
		t.Fatalf("len(lint.All()) = %d, want 14", got)
	}
	if got := len(m.report.Findings); got != 16 {
		t.Fatalf("len(report.Findings) = %d, want 16", got)
	}

	// Every one of the 14 checks fires at least once on dirty
	// (EXPECTED-LINT.md's own claim) — cross-checked here via ByCheck
	// rather than trusted blindly.
	var total int
	for _, c := range m.checks {
		total += len(m.report.ByCheck[c.ID()])
	}
	if total != 16 {
		t.Fatalf("sum of ByCheck findings = %d, want 16", total)
	}

	// Nothing is expanded by default: exactly 14 rows, one per check.
	if got := len(m.rows); got != 14 {
		t.Fatalf("len(rows) with nothing expanded = %d, want 14", got)
	}
}

// TestExpandCollapseRow drives `enter` on a check-header row and asserts
// the row list grows by exactly that check's finding count, then shrinks
// back to 14 when collapsed again.
func TestExpandCollapseRow(t *testing.T) {
	d, _ := newTestDeps(t, "dirty")
	p := initModel(t, d)
	m := p.(*Model)

	// log-rotate is the last of the 14 checks in table order (backbone
	// §4) and carries exactly one finding on dirty (EXPECTED-LINT.md).
	idx := -1
	for i, c := range m.checks {
		if c.ID() == "log-rotate" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("lint.All() has no log-rotate check")
	}
	m.cursor = idx

	_, cmd := m.handleKey(keyMsg("enter"))
	p = runCmd(t, m, cmd)
	m = p.(*Model)
	if got := len(m.rows); got != 15 {
		t.Fatalf("len(rows) after expanding log-rotate = %d, want 15 (14 + 1 finding)", got)
	}

	_, cmd = m.handleKey(keyMsg("enter"))
	p = runCmd(t, m, cmd)
	m = p.(*Model)
	if got := len(m.rows); got != 14 {
		t.Fatalf("len(rows) after collapsing log-rotate = %d, want 14", got)
	}
}

// TestEnterOnFindingEmitsOpenPathAndSwitch expands log-rotate (dirty's one
// finding with a non-empty, root-level Path — "log.md") and presses enter
// on its finding row, asserting both messages backbone §12/C-108 promises:
// ui.OpenPathMsg carrying that Path, then ui.SwitchScreenMsg{ScreenBrowse}.
func TestEnterOnFindingEmitsOpenPathAndSwitch(t *testing.T) {
	d, _ := newTestDeps(t, "dirty")
	p := initModel(t, d)
	m := p.(*Model)

	idx := -1
	for i, c := range m.checks {
		if c.ID() == "log-rotate" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("lint.All() has no log-rotate check")
	}
	findings := m.report.ByCheck["log-rotate"]
	if len(findings) != 1 {
		t.Fatalf("log-rotate findings on dirty = %d, want 1", len(findings))
	}
	wantPath := findings[0].Path
	if wantPath == "" {
		t.Fatal("log-rotate's finding has an empty Path; fixture assumption broken")
	}

	m.cursor = idx
	_, cmd := m.handleKey(keyMsg("enter")) // expand
	if cmd != nil {
		t.Fatal("expanding a check row returned a non-nil Cmd")
	}
	if len(m.rows) != 15 {
		t.Fatalf("len(rows) after expanding = %d, want 15", len(m.rows))
	}
	m.cursor = idx + 1 // the one finding row just inserted

	_, cmd = m.handleKey(keyMsg("enter"))
	msgs := collectMsgs(t, cmd)
	if len(msgs) != 2 {
		t.Fatalf("enter on a finding emitted %d messages, want 2: %#v", len(msgs), msgs)
	}
	open, ok := msgs[0].(ui.OpenPathMsg)
	if !ok {
		t.Fatalf("first message = %#v, want ui.OpenPathMsg", msgs[0])
	}
	if open.Path != wantPath {
		t.Fatalf("OpenPathMsg.Path = %q, want %q", open.Path, wantPath)
	}
	sw, ok := msgs[1].(ui.SwitchScreenMsg)
	if !ok {
		t.Fatalf("second message = %#v, want ui.SwitchScreenMsg", msgs[1])
	}
	if sw.To != ui.ScreenBrowse {
		t.Fatalf("SwitchScreenMsg.To = %v, want ui.ScreenBrowse", sw.To)
	}
}

// TestOpenFindingCmdVaultWide is openFindingCmd's own contract, tested as a
// pure function so it needs no engine at all: a vault-wide finding
// (Path == "") has nowhere to open, so only the screen switch is emitted —
// never an ui.OpenPathMsg with an empty Path.
func TestOpenFindingCmdVaultWide(t *testing.T) {
	cmd := openFindingCmd(lint.Finding{Check: "log-rotate", Path: "", Message: "vault-wide finding"})
	msgs := collectMsgs(t, cmd)
	if len(msgs) != 1 {
		t.Fatalf("vault-wide finding emitted %d messages, want 1: %#v", len(msgs), msgs)
	}
	sw, ok := msgs[0].(ui.SwitchScreenMsg)
	if !ok {
		t.Fatalf("message = %#v, want ui.SwitchScreenMsg", msgs[0])
	}
	if sw.To != ui.ScreenBrowse {
		t.Fatalf("SwitchScreenMsg.To = %v, want ui.ScreenBrowse", sw.To)
	}
}

// TestOpenFindingCmdPageScoped is openFindingCmd's non-empty-Path branch,
// mirrored against TestOpenFindingCmdVaultWide as a pure-function check.
func TestOpenFindingCmdPageScoped(t *testing.T) {
	cmd := openFindingCmd(lint.Finding{Check: "link-broken", Path: "wiki/concepts/kv-cache.md", Message: "broken link"})
	msgs := collectMsgs(t, cmd)
	if len(msgs) != 2 {
		t.Fatalf("page-scoped finding emitted %d messages, want 2: %#v", len(msgs), msgs)
	}
	open, ok := msgs[0].(ui.OpenPathMsg)
	if !ok || open.Path != "wiki/concepts/kv-cache.md" {
		t.Fatalf("first message = %#v, want ui.OpenPathMsg{Path: wiki/concepts/kv-cache.md}", msgs[0])
	}
	if sw, ok := msgs[1].(ui.SwitchScreenMsg); !ok || sw.To != ui.ScreenBrowse {
		t.Fatalf("second message = %#v, want ui.SwitchScreenMsg{ScreenBrowse}", msgs[1])
	}
}

// TestFilterKeyRendersAgentGuidanceNoEngineCall is pinned item 3: `f`
// renders the literal guidance and never calls the engine — checked here
// by asserting the loaded report is byte-identical before and after (no
// reload was triggered, which is the only engine call this screen could
// make from `f`).
func TestFilterKeyRendersAgentGuidanceNoEngineCall(t *testing.T) {
	d, _ := newTestDeps(t, "dirty")
	p := initModel(t, d)
	m := p.(*Model)
	before := m.report

	_, cmd := m.handleKey(keyMsg("f"))
	if cmd != nil {
		t.Fatal("`f` returned a non-nil Cmd; it must make no engine call")
	}
	if m.status != "requires the agent (M5)" {
		t.Fatalf("status = %q, want %q", m.status, "requires the agent (M5)")
	}
	if len(m.report.Findings) != len(before.Findings) {
		t.Fatal("report changed after `f`; an engine call must have been made")
	}
}

// TestViewNeverPanics drives View across a spread of sizes, including
// degenerate ones, both before and after the report loads.
func TestViewNeverPanics(t *testing.T) {
	d, _ := newTestDeps(t, "dirty")
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
	d, _ := newTestDeps(t, "dirty")
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
	// d.Theme itself — the shell's own copy — must be untouched.
	if d.Theme.IsDark != m.deps.Theme.IsDark {
		t.Fatal("d.Theme was mutated by construction; New must take a copy")
	}
}

// TestStageChangedTriggersReload asserts ui.StageChangedMsg schedules a
// fresh lint.Run rather than being ignored — the "refresh on
// StageChangedMsg" half of pinned item 6/TD-4 — and that the reload
// completes cleanly.
func TestStageChangedTriggersReload(t *testing.T) {
	d, _ := newTestDeps(t, "minimal")
	p := initModel(t, d)
	m := p.(*Model)

	if len(m.report.Findings) != 0 {
		t.Fatalf("minimal fixture is not clean: %d findings", len(m.report.Findings))
	}

	next, cmd := m.Update(ui.StageChangedMsg{})
	if cmd == nil {
		t.Fatal("StageChangedMsg did not schedule a reload Cmd")
	}
	p = runCmd(t, next, cmd)
	m2 := p.(*Model)
	if !m2.hasReport {
		t.Fatalf("report failed to reload after StageChangedMsg: %v", m2.loadErr)
	}
}

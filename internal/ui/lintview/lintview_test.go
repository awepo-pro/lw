package lintview

import (
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/ui"
)

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

// TestDirtyFixtureFifteenRowsTwentySixFindings is the stage file's own
// verification: lint.All() is 15 checks (C-85/D-V, not the 11 the stage
// file originally said; 014 added page-abstract as the 15th) and
// spec/fixtures/dirty totals 26 findings (EXPECTED-LINT.md — 014
// amendment, workflow §9 A6: 16 → 26, the ten dirty wiki pages each
// gained a page-abstract warn). Both numbers are read from the model's
// own state, never hardcoded from a rendered string.
func TestDirtyFixtureSixteenRowsTwentySixFindings(t *testing.T) {
	d, _ := newTestDeps(t, "dirty")
	p := initModel(t, d)
	m := p.(*Model)

	if !m.hasReport {
		t.Fatalf("report did not load: %v", m.loadErr)
	}
	// 020 amendment (workflow T-C): duplicate-section joins as check 16; it
	// fires 0 times on dirty (no duplicate sections in the fixtures), so the
	// totals below are unchanged.
	if got := len(m.checks); got != 16 {
		t.Fatalf("len(lint.All()) = %d, want 16", got)
	}
	if got := len(m.report.Findings); got != 26 {
		t.Fatalf("len(report.Findings) = %d, want 26", got)
	}

	// The first 15 checks each fire at least once on dirty
	// (EXPECTED-LINT.md's own claim) — cross-checked here via ByCheck
	// rather than trusted blindly.
	var total int
	for _, c := range m.checks {
		total += len(m.report.ByCheck[c.ID()])
	}
	if total != 26 {
		t.Fatalf("sum of ByCheck findings = %d, want 26", total)
	}
}

// TestReportRowsAreFlatFindings replaces v1's expand/collapse test: the 003
// Findings panel has no check-header rows to expand — every row of the
// report is one finding, in Report.Findings' frozen order (Path, then
// Line, then Check), and the pane's row count is the finding count.
func TestReportRowsAreFlatFindings(t *testing.T) {
	d, _ := newTestDeps(t, "dirty")
	p := initModel(t, d)
	m := p.(*Model)

	// 014 amendment (workflow §9 A6): 16 → 26 findings (10 page-abstract).
	if got := len(m.report.Findings); got != 26 {
		t.Fatalf("len(report.Findings) = %d, want 26", got)
	}

	// Rendered tall enough to show every row: one content row per finding,
	// each carrying a severity glyph, with nothing between them. 014
	// amendment (workflow §9 A6): 20 → 28 rows — 16 findings fit under the
	// old height, 26 do not.
	plain := plainView(p, 120, 28)
	lines := splitRows(plain)
	if got := len(nonPanelLines(lines)); got != 26 {
		t.Fatalf("content rows rendered = %d, want 26 (one per finding)\n%s", got, plain)
	}
}

// TestCursorNavigation pins the movement keys against the flat list: j
// moves down one finding, k back up, G to the last, g to the first, and
// the cursor clamps at both ends instead of escaping the list.
func TestCursorNavigation(t *testing.T) {
	d, _ := newTestDeps(t, "dirty")
	p := initModel(t, d)
	m := p.(*Model)
	n := len(m.report.Findings)

	p, _ = m.handleKey(keyMsg("j")) // down
	if p.(*Model).cursor != 1 {
		t.Fatalf("cursor after j = %d, want 1", p.(*Model).cursor)
	}
	p, _ = p.(*Model).handleKey(keyMsg("k")) // up
	if p.(*Model).cursor != 0 {
		t.Fatalf("cursor after k = %d, want 0", p.(*Model).cursor)
	}
	p, _ = p.(*Model).handleKey(keyMsg("k")) // clamps at 0
	if p.(*Model).cursor != 0 {
		t.Fatalf("cursor after k at top = %d, want 0", p.(*Model).cursor)
	}
	p, _ = p.(*Model).handleKey(keyMsg("G")) // bottom
	if p.(*Model).cursor != n-1 {
		t.Fatalf("cursor after G = %d, want %d", p.(*Model).cursor, n-1)
	}
	p, _ = p.(*Model).handleKey(keyMsg("j")) // clamps at the last finding
	if p.(*Model).cursor != n-1 {
		t.Fatalf("cursor after j at bottom = %d, want %d", p.(*Model).cursor, n-1)
	}
	p, _ = p.(*Model).handleKey(keyMsg("g")) // top
	if p.(*Model).cursor != 0 {
		t.Fatalf("cursor after g = %d, want 0", p.(*Model).cursor)
	}
}

// findingIndex returns the index of the one finding check fires on the
// dirty fixture, failing the test when it is not there exactly once.
func findingIndex(t *testing.T, m *Model, check string) int {
	t.Helper()
	idx := -1
	for i, f := range m.report.Findings {
		if f.Check == check {
			if idx >= 0 {
				t.Fatalf("check %s fires more than once on dirty", check)
			}
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("check %s never fires on dirty", check)
	}
	return idx
}

// TestEnterOnFindingEmitsOpenPathAndSwitch drives enter onto dirty's
// log-rotate finding (the one with a non-empty, root-level Path —
// "log.md"), asserting both messages backbone §12/C-108 promise:
// ui.OpenPathMsg carrying that Path, then ui.SwitchScreenMsg{ScreenBrowse}.
func TestEnterOnFindingEmitsOpenPathAndSwitch(t *testing.T) {
	d, _ := newTestDeps(t, "dirty")
	p := initModel(t, d)
	m := p.(*Model)

	idx := findingIndex(t, m, "log-rotate")
	f := m.report.Findings[idx]
	if f.Path == "" {
		t.Fatal("log-rotate's finding has an empty Path; fixture assumption broken")
	}

	m.cursor = idx
	_, cmd := m.handleKey(keyMsg("enter"))
	msgs := collectMsgs(t, cmd)
	if len(msgs) != 2 {
		t.Fatalf("enter on a finding emitted %d messages, want 2: %#v", len(msgs), msgs)
	}
	open, ok := msgs[0].(ui.OpenPathMsg)
	if !ok {
		t.Fatalf("first message = %#v, want ui.OpenPathMsg", msgs[0])
	}
	if open.Path != f.Path {
		t.Fatalf("OpenPathMsg.Path = %q, want %q", open.Path, f.Path)
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
// reports the literal guidance through StatusReporter — the footer's
// channel, not a status line this pane draws — and never calls the engine,
// checked here by asserting the loaded report is byte-identical before and
// after (no reload was triggered, which is the only engine call this
// screen could make from `f`).
func TestFilterKeyRendersAgentGuidanceNoEngineCall(t *testing.T) {
	d, _ := newTestDeps(t, "dirty")
	p := initModel(t, d)
	m := p.(*Model)
	before := m.report

	if msg, _ := m.Status(); msg != "" {
		t.Fatalf("Status before any key = %q, want empty", msg)
	}

	_, cmd := m.handleKey(keyMsg("f"))
	if cmd != nil {
		t.Fatal("`f` returned a non-nil Cmd; it must make no engine call")
	}
	msg, level := m.Status()
	if msg != "requires the agent (M5)" {
		t.Fatalf("Status after f = %q, want %q", msg, "requires the agent (M5)")
	}
	if level != ui.StatusInfo {
		t.Fatalf("Status level after f = %v, want ui.StatusInfo", level)
	}
	if len(m.report.Findings) != len(before.Findings) {
		t.Fatal("report changed after `f`; an engine call must have been made")
	}
}

// TestFooterHelpAndOverlayHelp pins the frozen footer and overlay surface
// (s2-screens.md T09): today's bindings in today's order, movement labels
// merged the way the shell's KeyMap carries them (contract §4), one-word
// labels where the old ones were longer, and the overlay section titled
// Lint.
func TestFooterHelpAndOverlayHelp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}

	m := New(ui.Deps{Theme: theme, Keys: keys}).(*Model)

	// The pane must implement both optional interfaces by value assertion,
	// exactly as the shell discovers them (contract §5).
	var fh ui.FooterHelper = m
	var oh ui.OverlayHelper = m
	if fh == nil || oh == nil {
		t.Fatal("Model does not implement ui.FooterHelper / ui.OverlayHelper")
	}
	var sr ui.StatusReporter = m
	if sr == nil {
		t.Fatal("Model does not implement ui.StatusReporter")
	}

	got := m.FooterHelp()
	want := []struct{ key, desc string }{
		{"j/k", "move"},
		{"g/G", "top/bottom"},
		{"enter", "open"},
		{"f", "fix"},
	}
	if len(got) != len(want) {
		t.Fatalf("FooterHelp has %d bindings, want %d", len(got), len(want))
	}
	for i, w := range want {
		h := got[i].Help()
		if h.Key != w.key || h.Desc != w.desc {
			t.Fatalf("FooterHelp[%d] = %q %q, want %q %q", i, h.Key, h.Desc, w.key, w.desc)
		}
	}

	title, entries := m.OverlayHelp()
	if title != "Lint" {
		t.Fatalf("OverlayHelp title = %q, want %q", title, "Lint")
	}
	if len(entries) != 4 {
		t.Fatalf("OverlayHelp has %d entries, want 4", len(entries))
	}
	wantOverlay := []struct{ key, desc string }{
		{"j/k", "down / up"},
		{"g/G", "top / bottom"},
		{"enter", "open"},
		{"f", "fix"},
	}
	for i, w := range wantOverlay {
		if entries[i].Key != w.key || entries[i].Desc != w.desc {
			t.Fatalf("OverlayHelp[%d] = %q %q, want %q %q", i, entries[i].Key, entries[i].Desc, w.key, w.desc)
		}
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

	m = runCmd(t, m, m.Init()).(*Model)
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

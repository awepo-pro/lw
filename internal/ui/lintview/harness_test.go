// harness_test.go is the package's headless driver: ui.Deps over a fixture
// engine, key messages, and the Update/View loop that stands in for what
// tea.Program delivers (never Program.Run itself, C-83). The model, view
// and golden test files share it.
package lintview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
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

// plainView renders m at w×h and returns the plain (ANSI-stripped) text
// through the harness's PaneScreen — the same render path the goldens take,
// so column arithmetic in these tests runs on cells, not escape bytes.
func plainView(m ui.Pane, w, h int) string {
	_, plain := uitest.PaneScreen(m, w, h)
	return plain
}

// syntheticDeps builds ui.Deps with no engine at all: the view tests below
// install a synthetic lint.Report straight onto the model (reportMsg is
// this package's own message), so alignment and glyphs are asserted
// independently of any fixture vault's lint state.
func syntheticDeps(t *testing.T) ui.Deps {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	return ui.Deps{Theme: theme, Keys: keys}
}

// withReport returns a loaded model showing findings.
func withReport(t *testing.T, d ui.Deps, findings []lint.Finding) *Model {
	t.Helper()
	p := feedMsg(t, New(d), reportMsg{report: lint.Report{
		Findings: findings,
		ByCheck:  groupByCheck(findings),
	}})
	return p.(*Model)
}

// groupByCheck rebuilds the ByCheck view a real lint.Run produces.
func groupByCheck(findings []lint.Finding) map[string][]lint.Finding {
	by := map[string][]lint.Finding{}
	for _, f := range findings {
		by[f.Check] = append(by[f.Check], f)
	}
	return by
}

// splitRows splits a View render into its rows.
func splitRows(plain string) []string { return strings.Split(plain, "\n") }

// nonPanelLines counts the panel's content rows: the rows between the top
// and bottom borders that are not blank padding.
func nonPanelLines(lines []string) []string {
	if len(lines) < 3 {
		return nil
	}
	var out []string
	for _, l := range lines[1 : len(lines)-1] {
		trimmed := strings.Trim(l, "│ ")
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// rowCells decodes one rendered row into runes — column arithmetic is in
// cells, and the glyphs (`✗`, `!`, `·`) are multi-byte.
func rowCells(row string) []rune { return []rune(row) }

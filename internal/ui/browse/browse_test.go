package browse

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

// newTestDeps builds a ui.Deps around a real vault copied from
// spec/fixtures/<fixture>, with lw's compiled-in theme and key defaults.
// XDG_CONFIG_HOME points at a fresh empty temp dir for the duration of the
// test, so LoadTheme/LoadKeys never read a real user config
// (00-conventions.md §3: tests must be deterministic). The caller must Close
// the returned *stage.Engine.
func newTestDeps(t *testing.T, fixture string) (ui.Deps, *stage.Engine) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := testutil.CopyFixture(t, fixture)
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}

	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}

	return ui.Deps{Engine: engine, Theme: theme, Keys: keys}, engine
}

// keyMsg builds a tea.KeyPressMsg for s, one of the literal strings this
// screen matches on, a bare printable rune, or a KeyMap-bound letter like
// "j"/"k"/"g"/"G". Never tea.KeyMsg (C-80).
func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	default:
		r := []rune(s)
		return tea.KeyPressMsg{Code: r[0], Text: s}
	}
}

// TestNewConstructibleWithNilEngine matches S4-T2's own "constructible
// headless" contract: a Pane with no vault at all must not panic, and must
// still render something.
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
	if got := p.Title(); got != "Browse" {
		t.Fatalf("Title() = %q, want %q", got, "Browse")
	}
	if len(p.Help()) == 0 {
		t.Fatal("Help() returned no bindings")
	}
	if out := p.View(80, 24); out == "" {
		t.Fatal("View(80, 24) is empty with no engine")
	}
}

// TestCursorOpensOnFirstFileRow is C8: on open the cursor sits on the first
// FILE row of the tree — a page or raw source, never the raw root or any
// directory — so the preview has content from the first frame.
func TestCursorOpensOnFirstFileRow(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	m := New(d).(*Model)
	n := m.selectedNode()
	if n == nil || n.IsDir() {
		t.Fatalf("selectedNode() at construction = %+v, want the first file row", n)
	}
	if want := "raw/articles/kv-cache-explained.md"; n.Path != want {
		t.Fatalf("selectedNode().Path = %q, want %q (the tree's first file)", n.Path, want)
	}
}

// TestViewFitsWidthAt80x24And200x50 is s4-tui.md S4-T4's own contract:
// "View(w, h int) string renders at exactly that size — no line wider than
// w, never assume 80×24".
func TestViewFitsWidthAt80x24And200x50(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	for _, sz := range []struct{ w, h int }{{80, 24}, {200, 50}} {
		p := New(d)
		out := p.View(sz.w, sz.h)
		if out == "" {
			t.Fatalf("View(%d, %d) is empty", sz.w, sz.h)
		}
		for i, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w > sz.w {
				t.Errorf("View(%d, %d) line %d is %d columns wide, want <= %d: %q", sz.w, sz.h, i, w, sz.w, line)
			}
		}
	}
}

// TestTreeNavigationMovesCursor exercises MoveDown/MoveUp/Top/Bottom, all
// matched through d.Keys (s4-tui.md S4-T4 item 4).
func TestTreeNavigationMovesCursor(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	if len(m.visible) < 2 {
		t.Fatalf("only %d visible nodes, want at least 2 to test cursor movement", len(m.visible))
	}
	start := m.cursor

	p, _ = p.Update(keyMsg("j"))
	m = p.(*Model)
	if m.cursor != start+1 {
		t.Fatalf("after j, cursor = %d, want %d", m.cursor, start+1)
	}

	p, _ = p.Update(keyMsg("k"))
	m = p.(*Model)
	if m.cursor != start {
		t.Fatalf("after k, cursor = %d, want %d", m.cursor, start)
	}

	p, _ = p.Update(keyMsg("G"))
	m = p.(*Model)
	if m.cursor != len(m.visible)-1 {
		t.Fatalf("after G, cursor = %d, want last index %d", m.cursor, len(m.visible)-1)
	}

	p, _ = p.Update(keyMsg("g"))
	m = p.(*Model)
	if m.cursor != 0 {
		t.Fatalf("after g, cursor = %d, want 0", m.cursor)
	}
}

// TestCollapseAndExpandDirectory is "h"/"l" (s4-tui.md S4-T4 item 4), from
// the C8 open state: the cursor starts on the first file, so the first h
// moves it up to the parent directory, the second collapses that directory
// (hiding its descendants), and l re-expands it.
func TestCollapseAndExpandDirectory(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	if n := m.selectedNode(); n == nil || n.IsDir() {
		t.Fatalf("expected the C8 open state (cursor on a file), got %+v", n)
	}

	p, _ = p.Update(keyMsg("h"))
	m = p.(*Model)
	if n := m.selectedNode(); n == nil || n.Path != "raw/articles" {
		t.Fatalf("after h on a file, selectedNode() = %+v, want its parent raw/articles", n)
	}
	before := len(m.visible)

	p, _ = p.Update(keyMsg("h"))
	m = p.(*Model)
	if len(m.visible) >= before {
		t.Fatalf("after collapsing raw/articles, visible count = %d, want fewer than %d", len(m.visible), before)
	}
	if m.expanded["raw/articles"] {
		t.Fatal("expanded[\"raw/articles\"] is still true after h")
	}

	p, _ = p.Update(keyMsg("l"))
	m = p.(*Model)
	if len(m.visible) != before {
		t.Fatalf("after re-expanding raw/articles, visible count = %d, want %d", len(m.visible), before)
	}
}

// TestEnterTogglesDirectory covers "enter" on a directory (s4-tui.md S4-T4
// item 4: "toggle-a-directory"), after moving the cursor onto one.
func TestEnterTogglesDirectory(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	if !m.selectPath("raw") {
		t.Fatal("selectPath: raw not found in tree")
	}
	before := len(m.visible)

	p, _ = p.Update(keyMsg("enter"))
	m = p.(*Model)
	if len(m.visible) >= before {
		t.Fatalf("after enter on an expanded directory, visible count = %d, want fewer than %d", len(m.visible), before)
	}

	p, _ = p.Update(keyMsg("enter"))
	m = p.(*Model)
	if len(m.visible) != before {
		t.Fatalf("after enter twice, visible count = %d, want back to %d", len(m.visible), before)
	}
}

// TestPagesFootNoteCountsFiles: the Pages panel's foot note numbers the
// selected file among the tree's files (pages + raw sources) and reads
// `– of M` while the cursor is on a directory.
func TestPagesFootNoteCountsFiles(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	m := New(d).(*Model)
	files := m.fileNodes()
	if len(files) != 6 {
		t.Fatalf("fileNodes() = %d files, want 6 (2 raw + 4 wiki pages)", len(files))
	}
	if got := m.pagesFootNote(); got != "1 of 6" {
		t.Fatalf("pagesFootNote() at the first file = %q, want %q", got, "1 of 6")
	}

	if !m.selectPath("wiki/concepts/kv-cache.md") {
		t.Fatal("selectPath: kv-cache.md not found in tree")
	}
	if got := m.pagesFootNote(); got != "4 of 6" {
		t.Fatalf("pagesFootNote() on kv-cache.md = %q, want %q", got, "4 of 6")
	}

	if !m.selectPath("raw") {
		t.Fatal("selectPath: raw not found in tree")
	}
	if got := m.pagesFootNote(); got != "– of 6" {
		t.Fatalf("pagesFootNote() on a directory = %q, want %q", got, "– of 6")
	}
}

// TestSelectPathShowsPageInPreview: moving the cursor onto a page makes its
// rendered body appear in View's output — the shared renderer draws the
// heading without its "#" mark.
func TestSelectPathShowsPageInPreview(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)

	if !m.selectPath("wiki/concepts/kv-cache.md") {
		t.Fatal("selectPath: wiki/concepts/kv-cache.md not found in tree")
	}
	out := p.View(100, 40)
	if !strings.Contains(out, "KV Cache") {
		t.Fatalf("View() after selecting kv-cache.md does not show its heading:\n%s", out)
	}
	if strings.Contains(out, "# KV Cache") {
		t.Fatalf("View() shows the raw # mark in the heading:\n%s", out)
	}
}

// TestSelectPathOnRawSourceOpensItToo: the tree's raw/ side previews too,
// not just wiki/ pages.
func TestSelectPathOnRawSourceOpensItToo(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)

	if !m.selectPath("raw/papers/leviathan-2023.md") {
		t.Fatal("selectPath: raw/papers/leviathan-2023.md not found in tree")
	}
	src, ok := m.sourceOf(m.selectedNode())
	if !ok || len(src) == 0 {
		t.Fatalf("sourceOf(raw/papers/leviathan-2023.md) = (%d bytes, %v), want non-empty content", len(src), ok)
	}
	out := p.View(100, 40)
	if !strings.Contains(out, "leviathan-2023.md") {
		t.Fatalf("View() after selecting the raw source does not title the preview with it:\n%s", out)
	}
}

// TestVaultReloadedMsgRebuildsTree: a page written to disk after

// TestOpenPathMsgSelectsAndExpandsAncestors is C-108/D-CU's Browse-side
// half: the shell delivers ui.OpenPathMsg to this pane so Lint's `enter`
// lands on the right page. The ancestor directory is collapsed first, so
// the test also proves the handler expands it rather than merely matching a

// TestOpenPathMsgUnknownPathIsNoOp: a path the vault does not hold (a stale
// finding, or one from before a revert) must leave the tree cursor exactly

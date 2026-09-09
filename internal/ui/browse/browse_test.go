package browse

import (
	"os"
	"path/filepath"
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
// screen matches on (s4-tui.md S4-T4 item 4), a bare printable rune, or a
// KeyMap-bound letter like "j"/"k"/"g"/"G". Never tea.KeyMsg (C-80).
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

// TestCollapseAndExpandDirectory is "h"/"l" (s4-tui.md S4-T4 item 4): the
// tree starts fully expanded, so "h" on the first node ("raw", a directory)
// collapses it, hiding its descendants; "l" re-expands it.
func TestCollapseAndExpandDirectory(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	if m.cursor != 0 || m.visible[0].Path != "raw" {
		t.Fatalf("expected cursor 0 on the raw root at construction, got node %+v", m.selectedNode())
	}
	before := len(m.visible)

	p, _ = p.Update(keyMsg("h"))
	m = p.(*Model)
	if len(m.visible) >= before {
		t.Fatalf("after collapsing raw, visible count = %d, want fewer than %d", len(m.visible), before)
	}
	if m.expanded["raw"] {
		t.Fatal("expanded[\"raw\"] is still true after h")
	}

	p, _ = p.Update(keyMsg("l"))
	m = p.(*Model)
	if len(m.visible) != before {
		t.Fatalf("after re-expanding raw, visible count = %d, want %d", len(m.visible), before)
	}
}

// TestEnterTogglesDirectory covers "enter" on a directory (s4-tui.md S4-T4
// item 4: "toggle-a-directory").
func TestEnterTogglesDirectory(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	before := len(m.visible)

	p, _ = p.Update(keyMsg("enter")) // cursor is on "raw"
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

// TestSelectPathOpensPageForPreview: moving the cursor onto a page makes its
// rendered body appear in View's output.
func TestSelectPathOpensPageForPreview(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)

	if !m.selectPath("wiki/concepts/kv-cache.md") {
		t.Fatal("selectPath: wiki/concepts/kv-cache.md not found in tree")
	}
	n := m.selectedNode()
	if n == nil || n.Path != "wiki/concepts/kv-cache.md" {
		t.Fatalf("selectedNode() = %+v, want wiki/concepts/kv-cache.md", n)
	}

	// glamour re-styles each word of a heading individually, so ANSI escapes
	// can land between "KV" and "Cache" — check the words independently
	// rather than the exact contiguous, unstyled substring.
	out := m.View(100, 40)
	if !strings.Contains(out, "KV") || !strings.Contains(out, "Cache") {
		t.Fatalf("View() after selecting kv-cache.md does not show its heading:\n%s", out)
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
	body, ok := m.selectedBody(m.selectedNode())
	if !ok || body == "" {
		t.Fatalf("selectedBody(raw/papers/leviathan-2023.md) = (%q, %v), want non-empty content", body, ok)
	}
}

// TestBacklinksShowForKVCache: kv-cache.md is linked from flash-attention.md
// and speculative-decoding.md in the minimal fixture, so the backlinks strip
// must name at least one of them.
func TestBacklinksShowForKVCache(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	if !m.selectPath("wiki/concepts/kv-cache.md") {
		t.Fatal("selectPath failed")
	}

	lines := m.renderBacklinks("wiki/concepts/kv-cache.md")
	if len(lines) == 0 {
		t.Fatal("renderBacklinks(kv-cache.md) is empty, want at least the header plus one backlink")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Backlinks:") {
		t.Fatalf("renderBacklinks output has no header:\n%s", joined)
	}
}

// TestBacklinksEmptyForOrphanIsNotAnError: an orphan page (no inbound
// wikilinks) is a legitimate state — nil, not an error, not a placeholder
// line (s4-tui.md S4-T4 item 9).
func TestBacklinksEmptyForOrphanIsNotAnError(t *testing.T) {
	d, engine := newTestDeps(t, "dirty")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)

	if got := m.renderBacklinks("wiki/concepts/orphan-page.md"); got != nil {
		t.Fatalf("renderBacklinks(orphan-page.md) = %v, want nil", got)
	}
}

// TestFuzzyFinderOpenTypeCommitOpensMatch drives the whole `/` flow through
// Model.Update: open, type a query, commit with enter, and confirm the tree
// cursor landed on the top-ranked match (s4-tui.md S4-T4 item 10).
func TestFuzzyFinderOpenTypeCommitOpensMatch(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)

	p, _ = p.Update(keyMsg("/"))
	m := p.(*Model)
	if !m.finder.open {
		t.Fatal("finder did not open on /")
	}

	for _, r := range "kv" {
		p, _ = p.Update(keyMsg(string(r)))
	}
	m = p.(*Model)
	if len(m.finder.matches) == 0 {
		t.Fatal("no finder matches for \"kv\"")
	}
	if m.finder.matches[0].path != "wiki/concepts/kv-cache.md" {
		t.Fatalf("finder.matches[0] = %q, want wiki/concepts/kv-cache.md", m.finder.matches[0].path)
	}

	p, _ = p.Update(keyMsg("enter"))
	m = p.(*Model)
	if m.finder.open {
		t.Fatal("finder still open after enter")
	}
	n := m.selectedNode()
	if n == nil || n.Path != "wiki/concepts/kv-cache.md" {
		t.Fatalf("selectedNode() after finder commit = %+v, want wiki/concepts/kv-cache.md", n)
	}
}

// TestFuzzyFinderEscRestoresCursor: closing the finder without committing
// leaves the tree cursor exactly where it was before / was pressed
// (s4-tui.md S4-T4 item 10: "esc closes the finder leaving the cursor where
// it was").
func TestFuzzyFinderEscRestoresCursor(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	m.cursor = 1
	origCursor := m.cursor

	p, _ = p.Update(keyMsg("/"))
	for _, r := range "kv" {
		p, _ = p.Update(keyMsg(string(r)))
	}
	p, _ = p.Update(keyMsg("esc"))
	m = p.(*Model)

	if m.finder.open {
		t.Fatal("finder still open after esc")
	}
	if m.cursor != origCursor {
		t.Fatalf("cursor after esc = %d, want restored to %d", m.cursor, origCursor)
	}
}

// TestFuzzyFinderBackspaceEditsQuery: backspace removes the last rune and
// re-runs the search.
func TestFuzzyFinderBackspaceEditsQuery(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	p, _ = p.Update(keyMsg("/"))
	for _, r := range "kx" {
		p, _ = p.Update(keyMsg(string(r)))
	}
	m := p.(*Model)
	if m.finder.query != "kx" {
		t.Fatalf("finder.query = %q, want %q", m.finder.query, "kx")
	}

	p, _ = p.Update(keyMsg("backspace"))
	m = p.(*Model)
	if m.finder.query != "k" {
		t.Fatalf("finder.query after backspace = %q, want %q", m.finder.query, "k")
	}
}

// TestVaultReloadedMsgRebuildsTree: a page written to disk after
// construction appears in the tree once ui.VaultReloadedMsg arrives
// (s4-tui.md S4-T4 item 8).
func TestVaultReloadedMsgRebuildsTree(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	for _, pth := range allPaths(m.tree) {
		if pth == "wiki/concepts/new-page.md" {
			t.Fatal("new-page.md unexpectedly already present before it was written")
		}
	}

	const newPage = "---\n" +
		"title: New Page\n" +
		"created: 2026-08-20\n" +
		"updated: 2026-08-20\n" +
		"type: concept\n" +
		"---\n" +
		"\n" +
		"# New Page\n" +
		"\n" +
		"Body.\n"
	dst := filepath.Join(d.Engine.Vault().Root(), "wiki", "concepts", "new-page.md")
	if err := os.WriteFile(dst, []byte(newPage), 0o644); err != nil {
		t.Fatalf("write new page: %v", err)
	}
	if err := d.Engine.Vault().Reload(); err != nil {
		t.Fatalf("Vault.Reload: %v", err)
	}

	next, _ := m.Update(ui.VaultReloadedMsg{})
	m2 := next.(*Model)

	found := false
	for _, pth := range allPaths(m2.tree) {
		if pth == "wiki/concepts/new-page.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tree after VaultReloadedMsg does not contain the new page: %v", allPaths(m2.tree))
	}
}

// TestOpenPathMsgSelectsAndExpandsAncestors is C-108/D-CU's Browse-side
// half: the shell delivers ui.OpenPathMsg to this pane so Lint's `enter`
// lands on the right page (s4-tui.md S4-T8). The ancestor directory is
// collapsed first, so the test also proves the handler expands it rather
// than merely matching a node already visible.
func TestOpenPathMsgSelectsAndExpandsAncestors(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	m.expanded["wiki/concepts"] = false
	m.refreshVisible()
	for _, n := range m.visible {
		if n.Path == "wiki/concepts/kv-cache.md" {
			t.Fatal("kv-cache.md unexpectedly visible while its parent is collapsed")
		}
	}

	next, cmd := m.Update(ui.OpenPathMsg{Path: "wiki/concepts/kv-cache.md"})
	if cmd != nil {
		t.Fatalf("Update(OpenPathMsg) returned a non-nil Cmd: %v", cmd())
	}
	m2 := next.(*Model)

	if !m2.expanded["wiki/concepts"] {
		t.Fatal("wiki/concepts was not expanded after OpenPathMsg")
	}
	n := m2.selectedNode()
	if n == nil || n.Path != "wiki/concepts/kv-cache.md" {
		t.Fatalf("selectedNode() after OpenPathMsg = %+v, want wiki/concepts/kv-cache.md", n)
	}
}

// TestOpenPathMsgUnknownPathIsNoOp: a path the vault does not hold (a stale
// finding, or one from before a revert) must leave the tree cursor exactly
// where it was, never panic.
func TestOpenPathMsgUnknownPathIsNoOp(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	if !m.selectPath("wiki/concepts/kv-cache.md") {
		t.Fatal("selectPath: wiki/concepts/kv-cache.md not found in tree")
	}
	before := m.selectedNode().Path

	next, _ := m.Update(ui.OpenPathMsg{Path: "wiki/concepts/does-not-exist.md"})
	m2 := next.(*Model)

	got := m2.selectedNode()
	if got == nil || got.Path != before {
		t.Fatalf("selectedNode() after OpenPathMsg(unknown path) = %+v, want unchanged %q", got, before)
	}
}

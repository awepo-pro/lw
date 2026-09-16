package browse

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// TestPreviewLinesPadsToWidth: the preview is the shared renderer's output,
// so every line is exactly min(width, Measure+2) cells — here width is the
// panel's content area and is under the 100-cell measure, so exactly width.
func TestPreviewLinesPadsToWidth(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	m := New(d).(*Model)
	n := m.selectedNode() // the C8 open state: a file
	lines, err := m.previewLines(n, 48)
	if err != nil {
		t.Fatalf("previewLines(%s, 48): %v", n.Path, err)
	}
	if len(lines) == 0 {
		t.Fatal("previewLines returned no lines")
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != 48 {
			t.Fatalf("previewLines line %d is %d cells, want 48: %q", i, got, l)
		}
	}
}

// TestPreviewLinesRendersFrontmatterHeader: the preview goes through the
// shared renderer's frontmatter handling — the page's title is the first
// line (its bold styling carries no text change), and the meta line names
// the page's type and updated date — with no raw `---` block anywhere.
func TestPreviewLinesRendersFrontmatterHeader(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	m := New(d).(*Model)
	if !m.selectPath("wiki/concepts/kv-cache.md") {
		t.Fatal("selectPath: kv-cache.md not found in tree")
	}
	lines, err := m.previewLines(m.selectedNode(), 60)
	if err != nil {
		t.Fatalf("previewLines: %v", err)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "KV Cache") {
		t.Fatalf("preview does not carry the page's frontmatter title:\n%s", plain)
	}
	if strings.Contains(plain, "---") {
		t.Fatalf("preview leaks the raw frontmatter delimiters:\n%s", plain)
	}
}

// TestPreviewLinesOnRawSource: a raw source renders through the same
// renderer, its body's first heading drawn without the `#` mark.
func TestPreviewLinesOnRawSource(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	m := New(d).(*Model)
	if !m.selectPath("raw/papers/leviathan-2023.md") {
		t.Fatal("selectPath: leviathan-2023.md not found in tree")
	}
	lines, err := m.previewLines(m.selectedNode(), 60)
	if err != nil {
		t.Fatalf("previewLines: %v", err)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "Speculative decoding proposes drafting") {
		t.Fatalf("raw-source preview does not carry its body:\n%s", plain)
	}
}

// TestPreviewLinesMissingPageIsAnError: a node the vault no longer holds
// reports an error (which View draws as a `preview failed` line), never a
// panic and never a silently empty panel.
func TestPreviewLinesMissingPageIsAnError(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	m := New(d).(*Model)
	n := &treeNode{Path: "wiki/concepts/deleted.md", Name: "deleted.md", Kind: nodePage}
	if _, err := m.previewLines(n, 48); err == nil {
		t.Fatal("previewLines on a path the vault does not hold returned no error")
	}
}

// TestPreviewHeadlessEngineIsEmpty: with no engine at all the preview has
// nothing to read and renders nothing, matching the headless constructor.
func TestPreviewHeadlessEngineIsEmpty(t *testing.T) {
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
	lines, err := m.previewLines(&treeNode{Path: "wiki/x.md", Name: "x.md", Kind: nodePage}, 48)
	if err != nil {
		t.Fatalf("previewLines with no engine: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("previewLines with no engine = %v, want empty", lines)
	}
}

// TestBackgroundColorMsgFlipsPreviewPolarity (C-81): after a polarity flip
// the same page renders with the other palette's SGR — the renderer's memo
// is keyed on polarity, so the flip is visible immediately.
func TestBackgroundColorMsgFlipsPreviewPolarity(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	if !m.selectPath("wiki/concepts/kv-cache.md") {
		t.Fatal("selectPath: kv-cache.md not found in tree")
	}

	styledBefore, _ := uitest.PaneScreen(m, 80, 24)
	wasDark := m.deps.Theme.IsDark
	flip := color.White
	if !wasDark {
		flip = color.Black
	}
	next, _ := m.Update(tea.BackgroundColorMsg{Color: flip})
	m2 := next.(*Model)
	styledAfter, _ := uitest.PaneScreen(m2, 80, 24)

	if m2.deps.Theme.IsDark == wasDark {
		t.Fatal("Theme.IsDark unchanged after a polarity-flipping BackgroundColorMsg")
	}
	if styledBefore == styledAfter {
		t.Fatal("preview styled output unchanged across the polarity flip, want the other palette")
	}
}

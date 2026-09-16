// view.go lays the browse screen out (s2-screens.md T07, mockgen.browse):
// a focused Pages tree panel on the left at clamp(28%, 28, 40) columns, a
// preview panel rendered by the shared markdown renderer beside it, and —
// only at w >= 180 — a Links panel on the right. While the finder is open
// the pane is a blank canvas with the finder panel centred on it (finder.go).
package browse

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

const (
	// linksMinWidth is the pane width at which the Links panel appears
	// (mockgen.browse: `lk = 40 if w >= 180 else 0`).
	linksMinWidth = 180
	// linksWidth is the Links panel's width, when it is shown at all.
	linksWidth = 40
)

// View renders the screen at exactly w columns by h rows: the Pages panel,
// the preview panel and — at w >= 180 — the Links panel side by side, or the
// centred finder panel while it is open.
func (m *Model) View(w, h int) string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	if m.finder.open {
		return m.renderFinder(w, h)
	}

	tw := treeWidth(w)
	lk := 0
	if w >= linksMinWidth {
		lk = linksWidth
	}
	pw := w - tw - lk

	rows := make([]string, h)
	pages := ui.Panel(m.deps.Theme, m.pagesSpec(tw, h), tw, h)
	preview := ui.Panel(m.deps.Theme, m.previewSpec(pw, h), pw, h)
	var links []string
	if lk > 0 {
		links = ui.Panel(m.deps.Theme, m.linksSpec(lk, h), lk, h)
	}
	for i := 0; i < h; i++ {
		line := pages[i] + preview[i]
		if links != nil {
			line += links[i]
		}
		rows[i] = line
	}
	return strings.Join(rows, "\n")
}

// treeWidth is the Pages panel's width: clamp((28*w+50)/100, 28, 40)
// (mockgen.browse: `max(28, min(40, round(w * 0.28)))`; (28*w+50)/100 is the
// same rounding in integer arithmetic), never wider than the pane itself.
func treeWidth(w int) int {
	tw := (28*w + 50) / 100
	if tw < 28 {
		tw = 28
	}
	if tw > 40 {
		tw = 40
	}
	if tw > w {
		tw = w
	}
	return tw
}

// pagesSpec builds the focused Pages panel: one row per visible node
// (mockgen.tree_rows), the cursor row marked, and the `i of M` foot note
// counting the tree's files.
func (m *Model) pagesSpec(w, h int) ui.PanelSpec {
	lines := make([]string, len(m.visible))
	for i, n := range m.visible {
		lines[i] = m.rowLabel(n)
	}

	// Scroll so the cursor row stays in view once the tree outgrows the
	// panel (mockgen draws only the top of a tree that always fits; the
	// pane's own scrollWindow keeps the invariant in general).
	cursor := -1
	if 0 <= m.cursor && m.cursor < len(m.visible) {
		cursor = m.cursor
	}
	windowed, cursor := scrollWindow(lines, cursor, h-2)

	return ui.PanelSpec{
		Title:     "Pages",
		Focused:   true,
		Lines:     windowed,
		CursorRow: cursor,
		FootNote:  m.pagesFootNote(),
	}
}

// rowLabel renders one tree row: `  `×depth then `▾ `/`▸ ` and the name for
// a directory (Muted), `  `×depth then two spaces and the name without
// `.md` for a file (Fg). Panel clips it to the content width.
func (m *Model) rowLabel(n *treeNode) string {
	indent := strings.Repeat("  ", strings.Count(n.Path, "/"))
	if n.IsDir() {
		marker := "▸ "
		if m.expanded[n.Path] {
			marker = "▾ "
		}
		return m.deps.Theme.Muted.Render(indent + marker + n.Name)
	}
	return m.deps.Theme.Fg.Render(indent + "  " + strings.TrimSuffix(n.Name, ".md"))
}

// pagesFootNote is the Pages panel's bottom-border note: the selected file's
// 1-based index among the tree's files (pages and raw sources, in display
// order), or `– of M` while the cursor sits on a directory
// (mockgen.browse's foot call).
func (m *Model) pagesFootNote() string {
	files := m.fileNodes()
	n := m.selectedNode()
	if n == nil || n.IsDir() {
		return fmt.Sprintf("– of %d", len(files))
	}
	for i, f := range files {
		if f.Path == n.Path {
			return fmt.Sprintf("%d of %d", i+1, len(files))
		}
	}
	return fmt.Sprintf("– of %d", len(files))
}

// fileNodes returns every file node in the tree, in display order — the
// numbering the foot note uses, stable whether or not directories are
// collapsed.
func (m *Model) fileNodes() []*treeNode {
	var out []*treeNode
	var walk func(n *treeNode)
	walk = func(n *treeNode) {
		if !n.IsDir() {
			out = append(out, n)
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range m.tree {
		walk(r)
	}
	return out
}

// previewSpec builds the preview panel (mockgen.browse's second box): titled
// with the selected file's base name, noted with its directory, its lines
// the shared markdown renderer's output at the panel's content width, and
// the Overflow foot note when the page is longer than the panel.
func (m *Model) previewSpec(w, h int) ui.PanelSpec {
	// CursorRow -1: the preview has no cursor of its own (the tree holds it).
	spec := ui.PanelSpec{Overflow: true, CursorRow: -1}

	var lines []string
	if n := m.selectedNode(); n != nil {
		spec.Title = n.Name
		spec.Note = filepath.Dir(n.Path) + "/"

		if n.IsDir() {
			// A directory has nothing to preview; say so rather than draw a
			// stale page under a directory's name.
			lines = []string{m.deps.Theme.Faint.Render("(select a page to preview)")}
		} else {
			var err error
			if lines, err = m.previewLines(n, w-4); err != nil {
				lines = []string{m.deps.Theme.Bad.Render("preview failed: " + err.Error())}
			}
		}
	}

	// Preview scroll (contract §5 note 10, W5 F2/C36): clamp the offset to
	// this render's geometry — remembered so the key handlers can clamp
	// between frames — then hand the Panel the window it selects. Overflow
	// keeps counting the lines below `↓ N more`; once nothing is below and
	// lines were dropped above, the foot note counts those instead
	// (`↑ N above`, N = off).
	m.previewCount, m.previewInner = len(lines), h-2
	m.clampPreviewOff()
	spec.Lines = lines[m.off:]
	if m.off > 0 && m.off >= m.maxPreviewOff() {
		spec.FootNote = fmt.Sprintf("↑ %d above", m.off)
	}
	return spec
}

// linksSpec builds the Links panel (mockgen.links_lines): backlinks, links
// out and the selected page's frontmatter sources.
func (m *Model) linksSpec(w, h int) ui.PanelSpec {
	return ui.PanelSpec{
		Title:     "Links",
		Lines:     m.linksLines(),
		Overflow:  true,
		CursorRow: -1, // the tree holds the cursor; the Links panel mirrors it
	}
}

// scrollWindow returns at most h consecutive lines from lines, positioned so
// index cursor is inside the window whenever the full list is longer than h.
// It returns the window and cursor's index within it (-1 stays -1).
func scrollWindow(lines []string, cursor, h int) ([]string, int) {
	if h <= 0 || len(lines) == 0 {
		return nil, -1
	}
	start := 0
	if cursor >= h {
		start = cursor - h + 1
	}
	if max := len(lines) - h; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}
	end := start + h
	if end > len(lines) {
		end = len(lines)
	}
	if cursor >= 0 {
		cursor -= start
	}
	return lines[start:end], cursor
}

// mdStyle builds the renderer's palette from the current theme (contract §3:
// a plain struct literal; markdown.Style and ui.Palette share field names on
// purpose). Heading and Code ride along (W5 F3): without them the renderer
// sees an empty hex and draws the preview's headings in the fallback colour
// instead of the palette's.
func (m *Model) mdStyle() markdown.Style {
	p := m.deps.Theme.Palette
	return markdown.Style{
		Dark:    m.deps.Theme.IsDark,
		Fg:      p.Fg,
		Muted:   p.Muted,
		Faint:   p.Faint,
		Border:  p.Border,
		Accent:  p.Accent,
		Good:    p.Good,
		Warn:    p.Warn,
		Bad:     p.Bad,
		Heading: p.Heading,
		Code:    p.Code,
	}
}

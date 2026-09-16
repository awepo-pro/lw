// finder.go is the `/` fuzzy finder: its transient state and its render —
// a centred ui.Panel titled `Find` (s2-screens.md T07) over a blank pane.
// The matching and the key handling are the finder's original behaviour
// (tree.go's fuzzyFind, browse.go's handleFinderKey); only the drawing moved
// to a Panel.
package browse

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui"
)

// finderState holds the `/` fuzzy finder's transient state. It is its own
// struct, rather than fields on Model, so opening and closing the finder is
// one assignment.
type finderState struct {
	open      bool
	query     string
	matches   []findMatch
	cursor    int // index into matches
	preCursor int // Model.cursor to restore on esc, leaving the tree where it was
}

// finderPanelWidth is the finder panel's widest form; narrower panes shrink
// it to fit with two cells of margin on each side (the `?` overlay's 64-wide
// convention, contract §5 frame note 4).
const finderPanelWidth = 64

// openFinder opens the `/` fuzzy finder, remembering the current cursor so
// esc can restore it untouched.
func (m *Model) openFinder() {
	m.finder = finderState{open: true, preCursor: m.cursor}
	m.refreshFinderMatches()
}

// handleFinderKey handles one key press while the finder is open. Every key
// not named here is treated as typed text appended to the query.
func (m *Model) handleFinderKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "esc":
		m.cursor = m.finder.preCursor
		m.clampCursor()
		m.finder = finderState{}
		return
	case "enter":
		m.commitFinder()
		return
	case "backspace":
		if r := []rune(m.finder.query); len(r) > 0 {
			m.finder.query = string(r[:len(r)-1])
			m.refreshFinderMatches()
		}
		return
	case "up":
		if m.finder.cursor > 0 {
			m.finder.cursor--
		}
		return
	case "down":
		if m.finder.cursor < len(m.finder.matches)-1 {
			m.finder.cursor++
		}
		return
	}

	// A plain printable key (no ctrl/alt) types into the query.
	if msg.Text != "" && msg.Mod&^tea.ModShift == 0 {
		m.finder.query += msg.Text
		m.refreshFinderMatches()
	}
}

// commitFinder moves the tree cursor to the selected match, then closes the
// finder. A finder that has nothing to commit — no matches, or a match the
// vault no longer holds — is the StatusReporter's error case.
func (m *Model) commitFinder() {
	if len(m.finder.matches) == 0 {
		m.finder = finderState{}
		m.setStatus("finder: no matches", ui.StatusWarn)
		return
	}
	target := m.finder.matches[m.finder.cursor].path
	m.finder = finderState{}
	if !m.selectPath(target) {
		m.setStatus(fmt.Sprintf("finder: %s is not in the vault", target), ui.StatusWarn)
	}
}

// refreshFinderMatches re-runs fuzzyFind against the current query and
// clamps the finder's own cursor back into range.
func (m *Model) refreshFinderMatches() {
	m.finder.matches = fuzzyFind(m.finder.query, m.findItems())
	if m.finder.cursor >= len(m.finder.matches) {
		m.finder.cursor = len(m.finder.matches) - 1
	}
	if m.finder.cursor < 0 {
		m.finder.cursor = 0
	}
}

// findItems returns every page and raw source's (path, display title) pair
// the finder searches over.
func (m *Model) findItems() []findItem {
	if m.deps.Engine == nil {
		return nil
	}
	v := m.deps.Engine.Vault()
	pages := v.Pages()
	rawSources := v.RawSources()

	items := make([]findItem, 0, len(pages)+len(rawSources))
	for _, p := range pages {
		items = append(items, findItem{path: p.Path, title: p.FM.Title})
	}
	for _, r := range rawSources {
		items = append(items, findItem{path: r.Path, title: r.Title})
	}
	return items
}

// renderFinder draws the finder over a blank pane: a centred, focused
// ui.Panel titled `Find` — the query on its first row, then the matches with
// the finder's own cursor row marked, shrinking to the pane and noting the
// remainder through the Panel's Overflow foot note.
func (m *Model) renderFinder(w, h int) string {
	lines := m.finderLines()

	bw := finderPanelWidth
	if bw > w-4 {
		bw = w - 4
	}
	if bw < 6 {
		bw = 6
	}
	bh := len(lines) + 2
	if bh > h-2 {
		bh = h - 2
	}
	if bh < 2 {
		bh = 2
	}

	panel := ui.Panel(m.deps.Theme, m.finderSpec(lines), bw, bh)

	x0 := (w - bw) / 2
	y0 := (h - bh) / 2
	blank := strings.Repeat(" ", w)
	rows := make([]string, h)
	for y := range rows {
		rows[y] = blank
	}
	for i, line := range panel {
		y := y0 + i
		if y < 0 || y >= h {
			continue
		}
		var row string
		if x0 > 0 {
			row += blank[:x0]
		}
		row += line
		if right := x0 + bw; right < w {
			row += blank[:w-right]
		}
		rows[y] = row
	}
	return strings.Join(rows, "\n")
}

// finderSpec is the finder panel's spec: focused (it is the pane's active
// input), the query and matches as its lines, the finder cursor marked one
// row below the query line.
func (m *Model) finderSpec(lines []string) ui.PanelSpec {
	spec := ui.PanelSpec{
		Title:    "Find",
		Focused:  true,
		Lines:    lines,
		Overflow: true,
	}
	if len(m.finder.matches) > 0 {
		spec.CursorRow = 1 + m.finder.cursor
	}
	return spec
}

// finderLines builds the finder panel's content: the query line — `/` plus
// what has been typed so far — then one row per match (`path  (title)`), or
// a single faint hint row when there is nothing to list.
func (m *Model) finderLines() []string {
	out := []string{m.deps.Theme.Bold.Render("/" + m.finder.query)}
	if len(m.finder.matches) == 0 {
		hint := "type to search"
		if m.finder.query != "" {
			hint = "no matches"
		}
		return append(out, m.deps.Theme.Faint.Render(hint))
	}
	for _, match := range m.finder.matches {
		line := m.deps.Theme.Fg.Render(match.path)
		if match.title != "" {
			line += m.deps.Theme.Muted.Render("  (" + match.title + ")")
		}
		out = append(out, line)
	}
	return out
}

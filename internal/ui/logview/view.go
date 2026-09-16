// view.go draws the log screen: one focused Events panel (s2-screens.md
// T10) — the journal's rows from rows.go, the active filter as the panel's
// Note, the cursor row in the gutter, and `i of N` on the bottom border.
// The pane scrolls its rows so the cursor stays visible; ui.Panel draws
// every border.
package logview

import (
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/ui"
)

// View renders the log screen at exactly w by h (backbone §12) — no line
// wider than w, never assuming 80x24, never panicking at small or large
// sizes.
func (m *Model) View(w, h int) string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	// ui.Panel is only defined at w >= 6, h >= 2; below that the frame does
	// not exist either (the shell never composes one), so return blank rows
	// that still honour the exactly-h-lines-of-w-cells invariant.
	if w < 6 || h < 2 {
		blank := strings.Repeat(" ", w)
		lines := make([]string, h)
		for i := range lines {
			lines[i] = blank
		}
		return strings.Join(lines, "\n")
	}

	return strings.Join(m.renderPanel(w, h), "\n")
}

// renderPanel builds the Events panel's rows at w×h.
func (m *Model) renderPanel(w, h int) []string {
	spec := ui.PanelSpec{
		Title:     "Events",
		Note:      m.filter.label(),
		Focused:   true,
		CursorRow: -1,
		Lines:     m.panelLines(w, h),
	}
	if n := len(m.events); n > 0 {
		spec.FootNote = fmt.Sprintf("%d of %d", m.cursor+1, n)
		spec.CursorRow = m.cursor - scrollStart(len(spec.Lines), m.cursor, h-2)
	}
	return ui.Panel(m.theme, spec, w, h)
}

// panelLines produces the panel's content rows: the window of event rows
// that keeps the cursor visible, or the single status row (empty list,
// still loading, failed query) when there is nothing to list.
func (m *Model) panelLines(w, h int) []string {
	innerH := h - 2

	switch {
	case m.loadErr != nil:
		return []string{loadErrRow(m.loadErr, m.theme)}
	case !m.hasLoad:
		return []string{loadingRow(m.theme)}
	case len(m.events) == 0:
		return []string{emptyListRow(m.theme)}
	}

	lines := eventRows(m.events, m.theme, kindWidth(m.events))
	return scrollWindow(lines, m.cursor, innerH)
}

// scrollStart is the index of the first line of the window scrollWindow
// returns — the offset the cursor row's gutter position is computed from.
func scrollStart(n, cursor, h int) int {
	if h <= 0 || n == 0 {
		return 0
	}
	start := 0
	if cursor >= h {
		start = cursor - h + 1
	}
	if max := n - h; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}
	return start
}

// scrollWindow returns at most h consecutive lines from lines, positioned
// so index cursor is inside the window whenever the full list is longer
// than h (the reviewer follows the newest event, the list's tail, on
// open).
func scrollWindow(lines []string, cursor, h int) []string {
	if h <= 0 || len(lines) == 0 {
		return nil
	}
	start := scrollStart(len(lines), cursor, h)
	end := start + h
	if end > len(lines) {
		end = len(lines)
	}
	return lines[start:end]
}

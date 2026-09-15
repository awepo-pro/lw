// view.go renders the lint screen's one panel (003 s2-screens.md T09): a
// single focused Findings panel drawn with ui.Panel — never a hand-drawn
// border — whose rows align glyph, check name, path and message into
// columns, with the severity as a coloured glyph. The pane draws no status
// line of its own (contract §5 StatusReporter): transient messages live in
// the shell's footer.
package lintview

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/ui"
)

// maxCheckColumn caps the check-name column: the longest check ID in the
// report pads the column, but never past 18 cells (s2-screens.md T09).
const maxCheckColumn = 18

// View renders the lint screen at exactly w by h (backbone §12): exactly h
// lines of exactly w cells, at every size, never panicking.
func (m *Model) View(w, h int) string {
	if w < 1 || h < 1 {
		return strings.Join(padGrid(nil, max(w, 0), max(h, 0)), "\n")
	}
	rows := ui.Panel(m.theme, m.panelSpec(w, h), w, h)
	return strings.Join(padGrid(rows, w, h), "\n")
}

// panelSpec builds the Findings panel for a w×h view. The three
// non-report states — load error, still loading, and the empty report —
// each fill the panel's first content row themselves; the empty report
// additionally switches the header note to `clean`.
func (m *Model) panelSpec(w, h int) ui.PanelSpec {
	cw := w - 4 // ui.Panel's content width

	switch {
	case m.loadErr != nil:
		return ui.PanelSpec{
			Title:   "Findings",
			Focused: true,
			Lines:   []string{m.theme.Bad.Render(ui.Clip(fmt.Sprintf("lint: %v", m.loadErr), max(cw, 1)))},
		}
	case !m.hasReport:
		return ui.PanelSpec{
			Title:   "Findings",
			Focused: true,
			Lines:   []string{m.theme.Muted.Render(ui.Clip("loading lint report…", max(cw, 1)))},
		}
	}

	findings := m.report.Findings
	if len(findings) == 0 {
		// Empty report (T09): `no findings` (faint) on the first content
		// row, note `clean` on the top border, no FootNote.
		return ui.PanelSpec{
			Title:   "Findings",
			Note:    "clean",
			Focused: true,
			Lines:   []string{m.theme.Faint.Render(ui.Clip("no findings", max(cw, 1)))},
		}
	}

	lines := make([]string, len(findings))
	nameW := checkColumnWidth(findings)
	pathW := pathColumnWidth(findings, cw)
	for i, f := range findings {
		lines[i] = m.findingRow(f, nameW, pathW, cw)
	}

	// The window scrolls under the cursor; ui.Panel draws the slice it is
	// given from the top, so the cursor row is passed window-relative.
	start, window := scrollWindow(lines, m.cursor, h-2)
	return ui.PanelSpec{
		Title:     "Findings",
		Focused:   true,
		Lines:     window,
		CursorRow: m.cursor - start,
		FootNote:  fmt.Sprintf("%d of %d", m.cursor+1, len(findings)),
	}
}

// findingRow renders one finding as the panel's pre-styled content line
// (s2-screens.md T09): glyph, two spaces, check name padded to the longest
// (max 18, muted), two spaces, path (fg, in the report-wide path column),
// two spaces, message (fg, clipped). nameW and pathW are shared by every
// row of the report, so the message starts at one cell column and takes all
// the width that is left.
func (m *Model) findingRow(f lint.Finding, nameW, pathW, cw int) string {
	glyph, glyphStyle := m.severityGlyph(f.Severity)

	fixed := 1 + 2 + nameW + 2 + pathW + 2 // glyph + gaps + both padded columns
	msgW := max(cw-fixed, 0)

	var b strings.Builder
	b.WriteString(glyphStyle.Render(glyph))
	b.WriteString("  ")
	b.WriteString(m.theme.Muted.Render(ui.Pad(f.Check, nameW)))
	b.WriteString("  ")
	b.WriteString(m.theme.Fg.Render(ui.Pad(location(f), pathW)))
	b.WriteString("  ")
	b.WriteString(m.theme.Fg.Render(ui.Clip(f.Message, msgW)))
	return b.String()
}

// location is a finding's path column: the vault-relative path, with the
// 1-based line appended when the finding carries one (Line 0 means the
// finding is not line-specific). A vault-wide finding (Path == "") has no
// location at all.
func location(f lint.Finding) string {
	if f.Path == "" {
		return "(vault-wide)"
	}
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	return f.Path
}

// checkColumnWidth is the check-name column's width: the longest check ID
// in the report, capped at 18 cells. Computed over the whole report, not
// the visible window, so the column does not re-align while scrolling.
func checkColumnWidth(findings []lint.Finding) int {
	longest := 0
	for _, f := range findings {
		if n := len(f.Check); n > longest {
			longest = n
		}
	}
	return min(longest, maxCheckColumn)
}

// pathColumnWidth is the path column's width: the longest rendered location
// in the report (including any `:line` suffix) capped at 40% of the panel's
// content width, rounded half-up — the same integer ratio style the review
// screen's split uses (s2-screens.md T06). Computed over the whole report,
// not the visible window, so the column does not re-align while scrolling —
// the same rule checkColumnWidth follows. Every path cell is then padded or
// clipped to it, so the message column starts at one cell.
func pathColumnWidth(findings []lint.Finding, cw int) int {
	longest := 0
	for _, f := range findings {
		if n := lipgloss.Width(location(f)); n > longest {
			longest = n
		}
	}
	return min(longest, (40*cw+50)/100)
}

// severityGlyph is a finding's severity glyph and its colour (T09): `✗`
// error in bad, `!` warn in warn, `·` info in muted.
func (m *Model) severityGlyph(sev lint.Severity) (string, lipgloss.Style) {
	switch sev {
	case lint.SevError:
		return "✗", m.theme.Bad
	case lint.SevWarn:
		return "!", m.theme.Warn
	default:
		return "·", m.theme.Muted
	}
}

// scrollWindow returns at most h consecutive lines from lines, positioned
// so index cursor is inside the window whenever the full list is longer
// than h, together with the window's start index.
func scrollWindow(lines []string, cursor, h int) (int, []string) {
	if h <= 0 || len(lines) == 0 {
		return 0, nil
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
	end := min(start+h, len(lines))
	return start, lines[start:end]
}

// padGrid forces rows to exactly h lines of exactly w cells: lines beyond
// h are dropped, missing ones come back blank, and every line is clipped /
// padded to w. ui.Panel already returns exact rows at legal sizes; this is
// the guard that keeps the invariant total at degenerate ones too
// (conventions §4 rule 2).
func padGrid(rows []string, w, h int) []string {
	out := make([]string, max(h, 0))
	for i := range out {
		if i < len(rows) {
			out[i] = ui.Pad(rows[i], w)
		} else {
			out[i] = strings.Repeat(" ", max(w, 0))
		}
	}
	return out
}

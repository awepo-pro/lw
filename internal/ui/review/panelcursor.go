// panelcursor.go extends ui.Panel's cursor treatment from one row to a
// span of rows — locally, and without touching the shell's panel code
// (contract §5 froze PanelSpec.CursorRow as a single index).
//
// The frozen review grids mark EVERY line of the cursor's hunk window
// with the ▌ gutter and the CursorBg tint (mockgen.draw_lines carries a
// per-line cur flag; the window is the unit, not the row), and the review
// grids are compared byte-exact — no mask. So the Detail panel is drawn
// with ui.Panel (borders included, never hand-drawn) and the window's
// rows are then re-rendered through exactly the same construction
// ui.Panel itself uses for its single cursor row: border cell, tinted ▌
// gutter, CursorBg-tinted content, tinted blank, border cell.
package review

import (
	"strings"
	"unicode/utf8"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/ui"
)

// applyCursorRows returns rows with every index in marked re-rendered as
// a cursor row. marked holds indices into the Lines slice the panel was
// drawn from — ui.Panel puts line i at rows[1+i], the top border taking
// row 0 — and rows not marked are reused untouched.
func applyCursorRows(rows []string, t ui.Theme, marked []int) []string {
	if len(marked) == 0 {
		return rows
	}
	out := make([]string, len(rows))
	copy(out, rows)
	for _, i := range marked {
		if i >= 0 && i+1 < len(out) {
			out[i+1] = cursorRow(out[i+1], t)
		}
	}
	return out
}

// cursorRow re-renders one ui.Panel content row as a cursor row. A panel
// content row is exactly: rendered border "│" (cell 0), one plain space
// (cell 1), the content padded to w-4 cells (cells 2..w-3), one plain
// space (cell w-2), rendered border "│" (cell w-1). Those seams are plain
// byte positions — the border runs are the only escapes before the first
// content byte and after the last " \x1b" — so the row can be taken apart
// at cells without splitting an escape sequence, and rebuilt the way
// ui.Panel's own cursor branch builds it.
func cursorRow(row string, t ui.Theme) string {
	// Cell 0: the border's escape runs, its printable rune, and the run's
	// own trailing reset.
	i := 0
	for i < len(row) && row[i] == 0x1b {
		i = skipEscape(row, i)
	}
	if i >= len(row) {
		return row
	}
	_, size := utf8.DecodeRuneInString(row[i:])
	i += size // the border rune: one cell, but several bytes
	for i < len(row) && row[i] == 0x1b {
		i = skipEscape(row, i)
	}
	if i >= len(row) || row[i] != ' ' {
		return row // not the row shape ui.Panel builds; leave it alone
	}
	prefix := row[:i] // border cell complete with its escapes
	contentStart := i + 1

	// The trailing seam: the LAST plain space followed by an escape —
	// cell w-2 plus the right border's first escape run. The right border
	// is always styled (border or accent), and it holds no space, so this
	// is the seam and never a styled run inside the content.
	seam := strings.LastIndex(row, " \x1b")
	if seam < contentStart {
		return row
	}

	gutter := t.Accent.Background(t.CursorBg).Render("▌")
	bg := lipgloss.NewStyle().Background(t.CursorBg)
	return prefix + gutter + bg.Render(row[contentStart:seam]) + bg.Render(" ") + row[seam+1:]
}

// skipEscape returns the index just past the escape sequence at s[i].
// lipgloss emits CSI sequences — "\x1b[" followed by parameter and
// intermediate bytes and one final byte in 0x40–0x7e — and nothing else;
// anything else is stepped over one byte at a time rather than guessed.
func skipEscape(s string, i int) int {
	if !strings.HasPrefix(s[i:], "\x1b[") {
		return i + 1
	}
	j := i + 2
	for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
		j++
	}
	if j < len(s) {
		return j + 1
	}
	return len(s)
}

package conformance

import (
	"strings"
	"testing"
)

// maskRegion is one grid's masked region (contract §9 note 4): the panel's
// inner columns x+1 … x+w-2 on every masked content row, plus the panel's
// bottom border row between its corners. The zero value is no mask; use a
// nil *maskRegion for views without one.
type maskRegion struct {
	rows      [2]int // first..last masked content row, inclusive
	bottomRow int    // the panel's bottom border row
	x, w      int    // the panel's left column and width
}

// maskedRow reports whether row r is a masked row (content or the panel's
// bottom border). A nil region masks nothing.
func (m *maskRegion) maskedRow(r int) bool {
	if m == nil {
		return false
	}
	if r == m.bottomRow {
		return true
	}
	return r >= m.rows[0] && r <= m.rows[1]
}

// masked reports whether the cell at (row, col) is excluded from the exact
// comparison: the panel's inner columns on a masked row. The border columns
// x and x+w-1 themselves stay compared. A nil region masks nothing.
func (m *maskRegion) masked(row, col int) bool {
	if !m.maskedRow(row) {
		return false
	}
	return col > m.x && col < m.x+m.w-1
}

// locateMask computes name's masked region from the frozen grid (contract
// §9 note 4). It is located in the grid — the definitive render — and the
// same cells are excluded on both sides of the comparison: a screen the
// wave has not rewritten yet has no Preview panel to find, and it must
// still compare (and fail as a layout diff) rather than break the harness.
// No view outside review-preview-* and browse-* has a mask.
func locateMask(t *testing.T, name string, grid []string) *maskRegion {
	t.Helper()

	switch {
	case strings.HasPrefix(name, "review-preview-"):
		p, ok := findPanelWithTitle(grid, "Preview")
		if !ok {
			t.Fatalf("setup: frozen grid %s has no Preview panel", name)
		}
		// The op head is the 1st inner row and its blank line the 2nd; the
		// mask starts on the 3rd inner row and runs to the last.
		return &maskRegion{rows: [2]int{p.top + 3, p.bottom - 1}, bottomRow: p.bottom, x: p.x, w: p.w}

	case strings.HasPrefix(name, "browse-"):
		p, ok := findPanelWithTitleSuffix(grid, ".md")
		if !ok {
			t.Fatalf("setup: frozen grid %s has no .md preview panel", name)
		}
		// From the first all-space inner row — after the title and meta
		// lines — to the last inner row.
		first := p.bottom
		for i := p.top + 1; i < p.bottom; i++ {
			if allSpace(grid[i], p.x+1, p.x+p.w-2) {
				first = i
				break
			}
		}
		return &maskRegion{rows: [2]int{first, p.bottom - 1}, bottomRow: p.bottom, x: p.x, w: p.w}
	}
	return nil
}

// panelBox is a panel's frame inside a rendered grid: its top border row,
// left column, width and bottom border row.
type panelBox struct {
	top, x, w, bottom int
}

// findPanelWithTitle returns the first panel whose top border starts
// `╭ <title> ` — how the redesigned screens title their panels ("Ops",
// "Preview", "Pages").
func findPanelWithTitle(rows []string, title string) (panelBox, bool) {
	return findPanel(rows, "╭ "+title+" ", "")
}

// findPanelWithTitleSuffix returns the first panel whose title text ends
// with suffix — the browse preview panel is titled by the selected page's
// file name.
func findPanelWithTitleSuffix(rows []string, suffix string) (panelBox, bool) {
	return findPanel(rows, "╭ ", suffix)
}

// findPanel scans row by row, left to right, for a top border starting with
// prefix; with a non-empty suffix, the title text between the prefix and
// the first `─` must end with it. The box's width runs to the border's `╮`;
// its bottom border row is the next row whose cell at x is `╰`. All indexes
// are cells, not bytes: the frames draw box-drawing runes, and a byte
// offset into one lands mid-rune.
func findPanel(rows []string, prefix, suffix string) (panelBox, bool) {
	for r, row := range rows {
		for x := 0; ; {
			i := runeIndex(row, prefix, x)
			if i < 0 {
				break
			}
			x = i
			if (suffix == "" || panelTitleEndsWith(row, x, suffix)) &&
				panelWidth(row, x) >= 2 {
				return panelBox{top: r, x: x, w: panelWidth(row, x), bottom: panelBottom(rows, r, x)}, true
			}
			x++
		}
	}
	return panelBox{}, false
}

// runeIndex returns the cell index of the first occurrence of substr in
// row at or after cell from, or -1 when there is none.
func runeIndex(row, substr string, from int) int {
	rs, ss := []rune(row), []rune(substr)
	for i := from; i+len(ss) <= len(rs); i++ {
		if string(rs[i:i+len(ss)]) == substr {
			return i
		}
	}
	return -1
}

// panelTitleEndsWith reports whether the title text a `╭ ` box starting at
// cell x carries — up to the border's first `─` — ends with suffix.
func panelTitleEndsWith(row string, x int, suffix string) bool {
	title := []rune(row)[x+2:]
	if i := runeIndex(string(title), "─", 0); i >= 0 {
		title = title[:i]
	}
	return strings.HasSuffix(strings.TrimSpace(string(title)), suffix)
}

// panelWidth returns the width of a panel whose top border starts at cell
// x: the distance to its `╮` plus one, or 0 when the border has no right
// corner.
func panelWidth(topRow string, x int) int {
	i := runeIndex(topRow, "╮", x)
	if i < 0 {
		return 0
	}
	return i + 1 - x
}

// panelBottom returns the row of the bottom border of the panel whose top
// border row is top and whose left column is x — the next row with `╰` at
// x — or len(rows) when the frame has none.
func panelBottom(rows []string, top, x int) int {
	for i := top + 1; i < len(rows); i++ {
		if cellAt(rows[i], x) == '╰' {
			return i
		}
	}
	return len(rows)
}

// allSpace reports whether row's cells in [from, to] are all spaces.
func allSpace(row string, from, to int) bool {
	for c := from; c <= to; c++ {
		if cellAt(row, c) != ' ' {
			return false
		}
	}
	return true
}

// cellAt returns row's cell at col — one cell, one rune: every rune the
// frames draw is a single cell wide. Out of range is a space.
func cellAt(row string, col int) rune {
	r := []rune(row)
	if col < 0 || col >= len(r) {
		return ' '
	}
	return r[col]
}

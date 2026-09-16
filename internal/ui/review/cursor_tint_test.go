// cursor_tint_test.go is T26's W5 colour evidence: the Detail panel's
// cursor window drawn through PanelSpec.CursorRow + CursorSpan tints
// every cell between the panel borders (contract §5 frame note 9, W5
// F1/C35 — the tint that stopped after the first styled run is what the
// G4 run measured), and the profile-aware grey at ANSI256 (contract §3
// note 7). The cell scanner here reads a styled row the way a terminal's
// parser does, SGR additive and resets included; the geometry helpers it
// walks panels with live in scroll_test.go.
package review

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/colorprofile"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// darkCursorBg is the compiled-in dark theme's cursor tint, as the SGR
// parameters lipgloss emits for it (contract §3 note 7; the same constant
// the conformance gate's colour checks pin).
const darkCursorBg = "48;2;26;35;49"

// ansi256CursorBg is the same tint at ANSI256 (dark): the fixed neutral
// grey 236.
const ansi256CursorBg = "48;5;236"

// styledCell is one cell of a styled row: its rune and the background SGR
// parameter string active on it — tracked the way a terminal does, since
// SGR is additive: 0 and 49 clear the background, 48;2;r;g;b and 48;5;n
// set it, and foreground-only sequences leave it alone.
type styledCell struct {
	r  rune
	bg string
}

// styledCells splits a styled line into cells, tracking the background
// state each cell renders with (the same read a terminal's parser makes,
// resets included).
func styledCells(line string) []styledCell {
	var (
		out []styledCell
		bg  string
		i   int
	)
	for i < len(line) {
		if line[i] == 0x1b && strings.HasPrefix(line[i:], "\x1b[") {
			if end := strings.IndexByte(line[i+2:], 'm'); end >= 0 {
				applySGR(&bg, strings.Split(line[i+2:i+2+end], ";"))
				i += 2 + end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if size < 1 {
			size = 1
		}
		out = append(out, styledCell{r: r, bg: bg})
		i += size
	}
	return out
}

// applySGR updates a tracked background parameter string with one SGR
// sequence's parameters — the subset of SGR a Theme render can emit.
func applySGR(bg *string, params []string) {
	for k := 0; k < len(params); k++ {
		switch params[k] {
		case "", "0", "49":
			*bg = ""
		case "48":
			span := 0
			if k+1 < len(params) {
				switch params[k+1] {
				case "2":
					span = 4 // 48;2;r;g;b
				case "5":
					span = 2 // 48;5;n
				}
			}
			if span > 0 && k+span < len(params) {
				*bg = strings.Join(params[k:k+span+1], ";")
				k += span
			}
		}
	}
}

// cursorRowPanels walks one ▌ row of a styled frame to its panel: the ▌ at
// cell c sits one cell in from the left border, so column c-1 runs up the
// panel's border to the ╭ at row r0, and row r0 runs right to the panel's
// own ╮, whose column is returned (s2-screens.md T06 Scroll, the rule-(g)
// walk). ok is false when the row carries no ▌ at all.
func cursorRowPanels(t *testing.T, rows []string, r int) (c, c2 int, ok bool) {
	t.Helper()
	for i, cell := range styledCells(rows[r]) {
		if cell.r == '▌' {
			c = i
			break
		}
	}
	if c == 0 {
		return 0, 0, false
	}
	r0 := -1
	for rr := r - 1; rr >= 0; rr-- {
		if rc := styledCells(rows[rr]); c-1 < len(rc) && rc[c-1].r == '╭' {
			r0 = rr
			break
		}
	}
	if r0 < 0 {
		t.Fatalf("row %d's ▌ at cell %d has no ╭ above it in column %d", r+1, c, c-1)
	}
	top := styledCells(rows[r0])
	for i := c - 1; i < len(top); i++ {
		if top[i].r == '╮' {
			return c, i, true
		}
	}
	t.Fatalf("the panel opened at row %d column %d has no ╮", r0+1, c-1)
	return 0, 0, false
}

// styledRows is the pane's styled View at the tests' size, split — the
// rows the SGR checks walk.
func styledRows(m *Model) []string {
	return strings.Split(m.View(scrollPaneW, scrollPaneH), "\n")
}

// detailRowsWithGutter walks every ▌ row of the styled render to its
// panel and holds each one to the want background: every cell from the
// gutter through the trailing blank — the frozen walk's c…c2-2 plus the
// blank at c2-1 — carries it, and the two border cells (c-1 and c2) carry
// none. It returns the count of Detail-panel rows walked, so a caller can
// tell "no cursor rows in Detail at all" from a walked pass.
func detailRowsWithGutter(t *testing.T, m *Model, want string) int {
	t.Helper()
	styled, plain := styledRows(m), plainRows(m)
	detailRows := 0
	for r, row := range styled {
		if !strings.Contains(row, "▌") {
			continue
		}
		c, c2, ok := cursorRowPanels(t, styled, r)
		if !ok {
			continue
		}
		cells := styledCells(row)
		isDetail := cellIndex(plain[0], "╭ Diff ") == c-1 || cellIndex(plain[0], "╭ Preview ") == c-1
		for i := c; i <= c2-1; i++ {
			if cells[i].bg != want {
				t.Errorf("row %d cell %d (%q) carries background %q, want %s",
					r+1, i, cells[i].r, cells[i].bg, want)
			}
		}
		for _, i := range []int{c - 1, c2} {
			if cells[i].bg != "" {
				t.Errorf("row %d border cell %d (%q) carries background %q, want none",
					r+1, i, cells[i].r, cells[i].bg)
			}
		}
		if isDetail {
			detailRows++
		}
	}
	return detailRows
}

// walkToOp3 moves the cursor from op1's op-level stop to op3's hunk stop,
// the stop the conformance scripts also land on: its hunk window is on
// screen at offset 0 and its lines are the ones the cursor marks.
func walkToOp3(t *testing.T, m *Model) *Model {
	t.Helper()
	return sendM(t, sendM(t, m, uitest.Key("j")), uitest.Key("j"))
}

// TestReviewCursorSpan holds the Detail panel's cursor window to contract
// §5 frame note 9 (W5 F1/C35): the window is drawn through PanelSpec's
// CursorRow + CursorSpan, so every cell a ▌ row carries between the panel
// borders — gutter, content, padding, trailing blank — is tinted, whatever
// SGR the pre-styled line contains.
func TestReviewCursorSpan(t *testing.T) {
	t.Run("window_rows_tinted_full_width", func(t *testing.T) {
		m := walkToOp3(t, newScrollModel(t))

		// The cursor window's marks are one contiguous run — the single
		// hunk window mockgen.draw_lines marks. A second, disjoint run
		// would tint the rows between windows that belong to no window:
		// stop, rather than paper over it.
		lines := detailLines(m, findDetail(t, plainRows(m)))
		var marks []int
		for i, l := range lines {
			if l.cursor {
				marks = append(marks, i)
			}
		}
		if len(marks) == 0 {
			t.Fatal("no cursor-marked Detail lines at op3: nothing to assert the tint on")
		}
		for i := 1; i < len(marks); i++ {
			if marks[i] != marks[i-1]+1 {
				t.Fatalf("cursor marks are not contiguous: %v", marks)
			}
		}
		if marks[0] >= findDetail(t, plainRows(m)).innerH() {
			t.Fatalf("cursor window starts at line %d, below the fold at offset 0", marks[0])
		}

		if detailRowsWithGutter(t, m, darkCursorBg) == 0 {
			t.Fatal("no Detail panel row carries the cursor gutter: the window is not drawn as cursor rows")
		}
	})
}

// TestReviewProfile pins the profile-aware cursor tint (contract §3 note
// 7, W5 F1/C35): on tea.ColorProfileMsg the pane rebuilds its theme with
// WithProfile, so at ANSI256 the ▌ rows carry the fixed neutral grey 236
// and no truecolor background survives anywhere.
func TestReviewProfile(t *testing.T) {
	t.Run("ansi256_cursor_rows_use_grey", func(t *testing.T) {
		m := walkToOp3(t, newScrollModel(t))
		m = sendM(t, m, tea.ColorProfileMsg{Profile: colorprofile.ANSI256})

		if strings.Contains(m.View(scrollPaneW, scrollPaneH), "48;2;") {
			t.Error("the ANSI256 render still carries a truecolor background sequence")
		}
		if detailRowsWithGutter(t, m, ansi256CursorBg) == 0 {
			t.Fatal("no Detail panel row carries the cursor gutter at ANSI256")
		}
	})
}

// scroll_test.go is T26's W5 scroll evidence (contract §5 frame note 10,
// W5 F2/C36): the Detail panel's scroll keys and their steps at the
// frozen geometry, the ↑-above footnote, the offset's reset points, and
// the wheel's hit-testing against the same layout View draws. It renders
// uitest.PublicVault at a 120×40 terminal — the pane's View(120, 38) —
// where the Detail panel sits side by side with Ops at x=40, 80 cells
// wide, with an inner height of 36.
package review

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// scrollPaneW and scrollPaneH are the pane size every scroll test renders
// at: a 120×40 terminal, so View(120, 38).
const (
	scrollPaneW = 120
	scrollPaneH = 38
)

// newScrollModel builds the scroll tests' pane: the public fixture vault's
// four-op changeset, dark theme, with the shell's size message delivered —
// the scroll keys read the Detail panel's geometry from it.
func newScrollModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "review-scroll-tests")
	m := initModel(t, uitest.Deps(v, true, nil)).(*Model)
	return sendM(t, m, tea.WindowSizeMsg{Width: scrollPaneW, Height: scrollPaneH + 2})
}

// sendM drives msg through m and drains every command it produces, keeping
// the concrete Model type.
func sendM(t *testing.T, m *Model, msg tea.Msg) *Model {
	t.Helper()
	return send(t, m, msg).(*Model)
}

// plainRows is the pane's View at the tests' size, stripped and split —
// the rows findDetail and contentRow locate and slice.
func plainRows(m *Model) []string {
	return strings.Split(ansi.Strip(m.View(scrollPaneW, scrollPaneH)), "\n")
}

// detailBox is the Detail panel's frame in a rendered plain grid: its top
// border row, left column, width and bottom border row.
type detailBox struct{ top, x, w, bottom int }

// innerH is the panel's inner height: its rows minus the two borders.
func (b detailBox) innerH() int { return b.bottom - b.top - 1 }

// findDetail locates the Detail panel — titled Diff or Preview, the `p`
// toggle renames it — in a rendered plain frame.
func findDetail(t *testing.T, rows []string) detailBox {
	t.Helper()
	for r, row := range rows {
		for _, title := range []string{"╭ Diff ", "╭ Preview "} {
			x := cellIndex(row, title)
			if x < 0 {
				continue
			}
			cells := []rune(row)
			w := 0
			for c := x; c < len(cells); c++ {
				if cells[c] == '╮' {
					w = c - x + 1
					break
				}
			}
			if w == 0 {
				t.Fatalf("Detail panel's top border at row %d has no ╮: %q", r+1, row)
			}
			for b := r + 1; b < len(rows); b++ {
				if bs := []rune(rows[b]); x < len(bs) && bs[x] == '╰' {
					return detailBox{top: r, x: x, w: w, bottom: b}
				}
			}
			t.Fatalf("Detail panel opened at row %d never closes", r+1)
		}
	}
	t.Fatal("no Diff or Preview panel in the render")
	return detailBox{}
}

// cellIndex is the cell (rune) index of sub in s, -1 when absent.
func cellIndex(s, sub string) int {
	i := strings.Index(s, sub)
	if i < 0 {
		return -1
	}
	return len([]rune(s[:i]))
}

// contentRow is the panel's k-th inner content row with its right padding
// trimmed: cells x+2 … x+w-3 of rows[top+1+k] (ui.Panel draws border,
// gutter, content, blank, border).
func contentRow(rows []string, b detailBox, k int) string {
	cells := []rune(rows[b.top+1+k])
	return strings.TrimRight(string(cells[b.x+2:b.x+b.w-2]), " ")
}

// detailLines is the Detail panel's full, unscrolled content line list at
// the box's content width — what an offset of 0 renders.
func detailLines(m *Model, b detailBox) []panelLine {
	_, _, lines := m.detailContent(b.w - 4)
	return lines
}

// wantRow is line i's plain text, right-trimmed the way contentRow is.
func wantRow(lines []panelLine, i int) string {
	return strings.TrimRight(ansi.Strip(lines[i].text), " ")
}

// assertRows pins the Detail panel's first n content rows to lines[off:],
// after a scroll.
func assertRows(t *testing.T, m *Model, lines []panelLine, off, n int) {
	t.Helper()
	rows := plainRows(m)
	b := findDetail(t, rows)
	for k := 0; k < n && off+k < len(lines); k++ {
		if got, want := contentRow(rows, b, k), wantRow(lines, off+k); got != want {
			t.Errorf("content row %d after scrolling = %q, want unscrolled line %d = %q",
				k+1, got, off+k, want)
		}
	}
}

// firstPlainDiff names the first row whose plain text differs between two
// renders — a one-line hint for the byte-for-byte assertions.
func firstPlainDiff(want, got string) string {
	wantRows, gotRows := strings.Split(ansi.Strip(want), "\n"), strings.Split(ansi.Strip(got), "\n")
	for i := 0; i < len(wantRows) && i < len(gotRows); i++ {
		if wantRows[i] != gotRows[i] {
			return fmt.Sprintf("first differing row %d:\n want %q\n  got %q", i+1, wantRows[i], gotRows[i])
		}
	}
	return fmt.Sprintf("row counts differ: %d vs %d", len(wantRows), len(gotRows))
}

// TestReviewScroll pins the Detail panel's scroll state (contract §5
// frame note 10, W5 F2/C36): the six bindings' steps at the tests' inner
// height of 36, the ↑-above footnote at the bottom, the offset's reset
// points, and the wheel's hit-testing against the layout View draws.
func TestReviewScroll(t *testing.T) {
	t.Run("pgdown_scrolls_detail", func(t *testing.T) {
		m := newScrollModel(t)
		lines := detailLines(m, findDetail(t, plainRows(m)))
		off := max(1, findDetail(t, plainRows(m)).innerH()-1)
		if len(lines) <= off+3 {
			t.Fatalf("fixture too short to scroll: %d content lines", len(lines))
		}
		m = sendM(t, m, uitest.Key("pgdown"))
		assertRows(t, m, lines, off, 4)
	})

	t.Run("ctrl_d_scrolls_half_page", func(t *testing.T) {
		m := newScrollModel(t)
		lines := detailLines(m, findDetail(t, plainRows(m)))
		off := max(1, findDetail(t, plainRows(m)).innerH()/2)
		if len(lines) <= off+3 {
			t.Fatalf("fixture too short to scroll: %d content lines", len(lines))
		}
		m = sendM(t, m, uitest.Key("ctrl+d"))
		assertRows(t, m, lines, off, 4)
	})

	t.Run("end_shows_above_footnote", func(t *testing.T) {
		m := newScrollModel(t)
		b := findDetail(t, plainRows(m))
		lines := detailLines(m, b)
		maxOff := max(0, len(lines)-b.innerH())
		if maxOff == 0 {
			t.Fatalf("fixture does not overflow its Detail panel: %d lines, inner %d",
				len(lines), b.innerH())
		}

		m = sendM(t, m, uitest.Key("end"))
		rows := plainRows(m)
		b = findDetail(t, rows)
		bottom := ansi.Strip(rows[b.bottom])
		if want := fmt.Sprintf("↑ %d above", maxOff); !strings.Contains(bottom, want) {
			t.Errorf("Detail bottom border after end lacks %q: %q", want, bottom)
		}
		if strings.Contains(bottom, "↓ ") {
			t.Errorf("Detail bottom border after end still counts lines below: %q", bottom)
		}
		if got, want := contentRow(rows, b, 0), wantRow(lines, maxOff); got != want {
			t.Errorf("content row 1 after end = %q, want the first line left on screen %q", got, want)
		}
	})

	t.Run("home_returns_to_top", func(t *testing.T) {
		m := newScrollModel(t)
		before := m.View(scrollPaneW, scrollPaneH)

		m = sendM(t, m, uitest.Key("pgdown"))
		m = sendM(t, m, uitest.Key("home"))
		if after := m.View(scrollPaneW, scrollPaneH); after != before {
			t.Errorf("home did not restore the unscrolled render byte for byte:\n%s",
				firstPlainDiff(before, after))
		}
	})

	t.Run("cursor_move_resets_scroll", func(t *testing.T) {
		moved := newScrollModel(t)
		moved = sendM(t, moved, uitest.Key("j"))
		want := moved.View(scrollPaneW, scrollPaneH)

		m := newScrollModel(t)
		m = sendM(t, m, uitest.Key("pgdown"))
		m = sendM(t, m, uitest.Key("j"))
		if got := m.View(scrollPaneW, scrollPaneH); got != want {
			t.Errorf("pgdown then j differs from j alone: the cursor move did not reset the scroll:\n%s",
				firstPlainDiff(want, got))
		}
	})

	t.Run("p_toggle_resets_scroll", func(t *testing.T) {
		toggled := newScrollModel(t)
		toggled = sendM(t, toggled, uitest.Key("p"))
		want := toggled.View(scrollPaneW, scrollPaneH)

		m := newScrollModel(t)
		m = sendM(t, m, uitest.Key("pgdown"))
		m = sendM(t, m, uitest.Key("p"))
		if got := m.View(scrollPaneW, scrollPaneH); got != want {
			t.Errorf("pgdown then p differs from p alone: the toggle did not reset the scroll:\n%s",
				firstPlainDiff(want, got))
		}
	})

	t.Run("wheel_over_detail_scrolls", func(t *testing.T) {
		m := newScrollModel(t)
		lines := detailLines(m, findDetail(t, plainRows(m)))
		top := m.View(scrollPaneW, scrollPaneH)

		// A notch well inside the Detail panel's rectangle (x 40..119,
		// y 0..37 at this size).
		m = sendM(t, m, ui.WheelMsg{X: 60, Y: 10, W: scrollPaneW, H: scrollPaneH, Delta: 1})
		assertRows(t, m, lines, 3, 4)

		// And a notch back up undoes it, byte for byte.
		m = sendM(t, m, ui.WheelMsg{X: 60, Y: 10, W: scrollPaneW, H: scrollPaneH, Delta: -1})
		if got := m.View(scrollPaneW, scrollPaneH); got != top {
			t.Errorf("a wheel-up notch did not undo the wheel-down notch:\n%s",
				firstPlainDiff(top, got))
		}
	})

	t.Run("wheel_over_ops_moves_cursor", func(t *testing.T) {
		pressed := newScrollModel(t)
		pressed = sendM(t, pressed, uitest.Key("j"))
		wantDown := pressed.View(scrollPaneW, scrollPaneH)
		lifted := newScrollModel(t)
		lifted = sendM(t, lifted, uitest.Key("k"))
		wantUp := lifted.View(scrollPaneW, scrollPaneH)

		m := newScrollModel(t)
		m = sendM(t, m, ui.WheelMsg{X: 5, Y: 3, W: scrollPaneW, H: scrollPaneH, Delta: 1})
		if got := m.View(scrollPaneW, scrollPaneH); got != wantDown {
			t.Errorf("a wheel-down notch over Ops differs from j:\n%s", firstPlainDiff(wantDown, got))
		}

		m2 := newScrollModel(t)
		m2 = sendM(t, m2, ui.WheelMsg{X: 5, Y: 3, W: scrollPaneW, H: scrollPaneH, Delta: -1})
		if got := m2.View(scrollPaneW, scrollPaneH); got != wantUp {
			t.Errorf("a wheel-up notch over Ops differs from k:\n%s", firstPlainDiff(wantUp, got))
		}
	})
}

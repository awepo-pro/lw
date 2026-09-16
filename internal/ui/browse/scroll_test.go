// scroll_test.go covers W5 F2/C36's preview scroll (contract §5 note 10,
// s2-screens.md T07 "Preview scroll"): the six scroll keys always targeting
// the preview, the wheel's hit-testing, the reset points — plus the two T27
// theme jobs that ride along: mdStyle carrying the palette's Heading/Code
// tokens and tea.ColorProfileMsg re-resolving the cursor tint.
package browse

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// The pane size the scroll tests run at: 80 columns gives tw = 28 and a
// 52-column preview (no Links panel below 180); 12 rows give the preview
// inner = 10 content rows. The C8 first file,
// raw/articles/the-science-of-a-good-loaf.md, renders to 50 lines at the
// preview's 48-column content width, so a page step (9) and the full max
// offset (40) are both reachable without clamping.
const (
	scrollW = 80
	scrollH = 12
)

// newScrollModel builds a browse model over the public vault in its C8 open
// state: the cursor on the first file row, the loaf page.
func newScrollModel(t *testing.T) *Model {
	t.Helper()
	v := uitest.PublicVault(t, "public")
	return New(uitest.Deps(v, true, nil)).(*Model)
}

// pressKey drives one key press through the model and returns it.
func pressKey(t *testing.T, m *Model, s string) *Model {
	t.Helper()
	next, _ := m.Update(uitest.Key(s))
	return next.(*Model)
}

// updateMsg drives one non-key message through the model and returns it.
func updateMsg(t *testing.T, m *Model, msg tea.Msg) *Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(*Model)
}

// wheel builds a pane-local ui.WheelMsg at x, the shape the shell delivers
// after its own hit-testing pass (contract §5 frame note 7).
func wheel(x, delta int) ui.WheelMsg {
	return ui.WheelMsg{X: x, Y: 1, W: scrollW, H: scrollH, Delta: delta}
}

// previewRows renders m and returns the plain text of the preview panel's
// content rows, each still wearing its `│ … │` chrome, sliced out of the
// pane grid by cell (the plain grid is one width-1 rune per cell).
func previewRows(t *testing.T, m *Model, w, h int) []string {
	t.Helper()
	_, plain := uitest.PaneScreen(m, w, h)
	rows := strings.Split(plain, "\n")
	if len(rows) != h {
		t.Fatalf("View(%d, %d) is %d rows, want %d", w, h, len(rows), h)
	}
	tw := treeWidth(w)
	pw := w - tw // no Links panel below 180 columns
	out := make([]string, h-2)
	for i := range out {
		out[i] = string([]rune(rows[1+i])[tw : tw+pw])
	}
	return out
}

// renderedPreview renders the model's selected page at the test size's
// content width — the same call previewSpec makes — so assertions can name
// rendered lines by index.
func renderedPreview(t *testing.T, m *Model, w int) []string {
	t.Helper()
	pw := w - treeWidth(w)
	lines, err := m.previewLines(m.selectedNode(), pw-4)
	if err != nil {
		t.Fatalf("previewLines(%s): %v", m.selectedNode().Path, err)
	}
	return lines
}

// firstRowIs asserts the preview's first content row shows rendered line i.
// The rendered line is stripped to plain text (previewRows reads the plain
// grid); the renderer pads every line to exactly the content width, so the
// stripped line is what sits between the panel's border and one blank on
// each side.
func firstRowIs(t *testing.T, m *Model, w, h int, rendered []string, i int) {
	t.Helper()
	rows := previewRows(t, m, w, h)
	if want := "│ " + ansi.Strip(rendered[i]) + " │"; rows[0] != want {
		t.Fatalf("preview's first content row = %q, want rendered line %d %q", rows[0], i, want)
	}
}

// TestBrowsePreviewScroll is C36's Browse half: the preview scrolls (the
// keys always target it, j/k stay tree movement), the offset resets when
// the selection changes, the wheel is hit-tested against the pane layout,
// and an open finder ignores every wheel notch.
func TestBrowsePreviewScroll(t *testing.T) {
	t.Run("pgdown_scrolls_preview", func(t *testing.T) {
		m := newScrollModel(t)
		rendered := renderedPreview(t, m, scrollW)
		firstRowIs(t, m, scrollW, scrollH, rendered, 0) // the initial frame

		m = pressKey(t, m, "pgdown")
		step := max(1, (scrollH-2)-1) // max(1, inner-1), contract §5 note 10
		firstRowIs(t, m, scrollW, scrollH, rendered, step)
	})

	t.Run("selection_change_resets_scroll", func(t *testing.T) {
		m := newScrollModel(t)
		before := m.selectedPath()
		previewRows(t, m, scrollW, scrollH) // the first frame establishes the geometry

		m = pressKey(t, m, "pgdown")
		// Step j until the cursor sits on a file: in the public vault the
		// first j from the loaf lands on the wiki root, a directory with no
		// preview to assert on.
		m = pressKey(t, m, "j")
		for i := 0; i < len(m.visible) && m.selectedNode() != nil && m.selectedNode().IsDir(); i++ {
			m = pressKey(t, m, "j")
		}
		if m.selectedNode() == nil || m.selectedNode().Path == before {
			t.Fatalf("j did not move the selection off %q", before)
		}
		// The new page renders from its first line: the offset did not ride
		// over from the old selection.
		firstRowIs(t, m, scrollW, scrollH, renderedPreview(t, m, scrollW), 0)

		m = pressKey(t, m, "pgdown")
		m = updateMsg(t, m, ui.OpenPathMsg{Path: "wiki/concepts/autolyse.md"})
		firstRowIs(t, m, scrollW, scrollH, renderedPreview(t, m, scrollW), 0)
	})

	t.Run("end_shows_above_footnote", func(t *testing.T) {
		m := newScrollModel(t)
		rendered := renderedPreview(t, m, scrollW)
		firstRowIs(t, m, scrollW, scrollH, rendered, 0) // the first frame
		maxOff := len(rendered) - (scrollH - 2)

		m = pressKey(t, m, "end")

		_, plain := uitest.PaneScreen(m, scrollW, scrollH)
		rows := strings.Split(plain, "\n")
		want := fmt.Sprintf("↑ %d above", maxOff)
		if !strings.Contains(rows[scrollH-1], want) {
			t.Fatalf("after end the bottom border = %q, want it to contain %q", rows[scrollH-1], want)
		}
		// And the window really is the tail of the page.
		firstRowIs(t, m, scrollW, scrollH, rendered, maxOff)
	})

	t.Run("wheel_over_preview_scrolls", func(t *testing.T) {
		m := newScrollModel(t)
		rendered := renderedPreview(t, m, scrollW)
		firstRowIs(t, m, scrollW, scrollH, rendered, 0) // the first frame
		x := treeWidth(scrollW) + 2                     // inside the preview, past the Pages panel

		m = updateMsg(t, m, wheel(x, 1))
		firstRowIs(t, m, scrollW, scrollH, rendered, 3)

		m = updateMsg(t, m, wheel(x, 1))
		firstRowIs(t, m, scrollW, scrollH, rendered, 6)

		m = updateMsg(t, m, wheel(x, -1))
		firstRowIs(t, m, scrollW, scrollH, rendered, 3)
	})

	t.Run("wheel_over_pages_moves_cursor", func(t *testing.T) {
		m := newScrollModel(t)
		start := m.pagesFootNote()

		m = updateMsg(t, m, wheel(2, 1)) // inside the Pages panel
		down := m.pagesFootNote()
		if down == start {
			t.Fatalf("a wheel notch over Pages left the cursor on %q, want it to move", start)
		}

		// One notch is exactly one j step: the same row a j from the C8 open
		// state lands on.
		mj := pressKey(t, newScrollModel(t), "j")
		if got := mj.pagesFootNote(); got != down {
			t.Fatalf("one wheel notch over Pages = %q, want the row j lands on (%q)", down, got)
		}

		m = updateMsg(t, m, wheel(2, -1))
		if got := m.pagesFootNote(); got != start {
			t.Fatalf("wheel up over Pages = %q, want back to %q", got, start)
		}
	})

	t.Run("finder_open_ignores_wheel", func(t *testing.T) {
		m := newScrollModel(t)
		m = pressKey(t, m, "/")
		for _, r := range "sour" {
			m = pressKey(t, m, string(r))
		}
		before, _ := uitest.PaneScreen(m, scrollW, scrollH)

		m = updateMsg(t, m, wheel(treeWidth(scrollW)+2, 1)) // over the preview
		after, _ := uitest.PaneScreen(m, scrollW, scrollH)

		if after != before {
			t.Fatalf("a wheel notch changed the render while the finder was open:\nbefore: %q\nafter:  %q",
				ansi.Strip(before), ansi.Strip(after))
		}
	})
}

// TestBrowseStyleTokens pins T27's two theme jobs: the renderer's style is
// built with the palette's Heading/Code tokens (an empty hex renders
// headings unreadable — F3's live bug), and tea.ColorProfileMsg re-resolves
// the cursor tint so a 256-colour terminal gets the neutral grey, never the
// truecolor hex rounding to navy (F1/C35).
func TestBrowseStyleTokens(t *testing.T) {
	t.Run("md_style_carries_heading_and_code", func(t *testing.T) {
		m := newScrollModel(t)
		st := m.mdStyle()
		p := m.deps.Theme.Palette
		if p.Heading == "" || p.Code == "" {
			t.Fatalf("the theme's palette carries empty tokens: heading %q code %q", p.Heading, p.Code)
		}
		if st.Heading != p.Heading {
			t.Fatalf("mdStyle().Heading = %q, want the palette's %q", st.Heading, p.Heading)
		}
		if st.Code != p.Code {
			t.Fatalf("mdStyle().Code = %q, want the palette's %q", st.Code, p.Code)
		}
	})

	t.Run("profile_msg_rethemes_tree_cursor", func(t *testing.T) {
		m := newScrollModel(t)
		m = updateMsg(t, m, tea.ColorProfileMsg{Profile: colorprofile.ANSI256})

		styled, plain := uitest.PaneScreen(m, 80, 22)
		styledRows := strings.Split(styled, "\n")
		for i, row := range strings.Split(plain, "\n") {
			if !strings.Contains(row, "▌") {
				continue
			}
			if !strings.Contains(styledRows[i], "48;5;236") {
				t.Fatalf("the Pages cursor row after ColorProfileMsg{ANSI256} does not carry 48;5;236: %q", styledRows[i])
			}
			if strings.Contains(styledRows[i], "48;2;") {
				t.Fatalf("the Pages cursor row still carries a truecolor background after ColorProfileMsg{ANSI256}: %q", styledRows[i])
			}
			return
		}
		t.Fatal("no ▌ cursor row found in the 80x22 render")
	})
}

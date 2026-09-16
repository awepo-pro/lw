// cursor_tint_test.go pins correction C35's coverage fix: a cursor row is
// tinted cell by cell, from the gutter (column 1) to the last inner cell
// (column w-2), whatever SGR sequences — resets included — the pre-styled
// content line carries. The G4 acceptance run measured the old shape: one
// Background(...).Render around pre-styled content, whose first run's own
// reset ("\x1b[39m\x1b[49m") switched the tint off after the first styled
// run, leaving a lone tinted blank at the right edge.
package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// TestPanelCursorTintFullWidth asserts the cursor tint's reach inside a
// Panel row (contract §5 frame note 9): every cell from column 1 through
// column w-2 carries CursorBg; the two border cells never do.
func TestPanelCursorTintFullWidth(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	t.Run("styled_content_keeps_tint_to_border", func(t *testing.T) {
		// A cursor line assembled from separately rendered styled runs —
		// exactly the shape screens build Lines from — plus the padding
		// Panel adds itself. Each run ends with its own reset, so the old
		// wrap-the-whole-row Render lost the tint after "kept".
		line := th.Good.Render("kept") + th.Muted.Render(" · ") + th.Warn.Render("warn")

		const w, h = 40, 3
		rows := Panel(th, PanelSpec{Lines: []string{line}, CursorRow: 0}, w, h)
		assertCursorRowTint(t, rows[1], w, "48;2;26;35;49")
	})

	t.Run("cursor_span_tints_every_row", func(t *testing.T) {
		// CursorRow 1 with a span of 3: Lines 1–3 are cursor rows (panel
		// rows 2–4), tinted full width; Lines 0 and 4 are ordinary rows.
		const w, h = 30, 7
		spec := PanelSpec{
			Lines:      []string{"zero", "one", "two", "three", "four"},
			CursorRow:  1,
			CursorSpan: 3,
		}
		rows := Panel(th, spec, w, h)
		assertCursorRowTint(t, rows[2], w, "48;2;26;35;49")
		assertCursorRowTint(t, rows[3], w, "48;2;26;35;49")
		assertCursorRowTint(t, rows[4], w, "48;2;26;35;49")
		for _, i := range []int{1, 5} {
			for c, bg := range cellBackgrounds(rows[i]) {
				if bg != "" {
					t.Errorf("non-cursor panel row %d cell %d carries background %q, want none", i, c, bg)
				}
			}
		}
	})

	t.Run("nil_cursor_bg_draws_gutter_only", func(t *testing.T) {
		// A profile below ANSI256 resolves CursorBg to nil: the accent
		// gutter still marks the cursor row, and no cell carries any
		// background (contract §3 note 7).
		const w, h = 24, 3
		rows := Panel(th.WithProfile(colorprofile.ANSI), PanelSpec{Lines: []string{"x"}, CursorRow: 0}, w, h)
		if !strings.Contains(rows[1], "▌") {
			t.Fatalf("cursor row with nil CursorBg = %q, want the accent ▌ gutter", rows[1])
		}
		if strings.Contains(rows[1], "48;") {
			t.Fatalf("cursor row with nil CursorBg = %q, want no background SGR at all", rows[1])
		}
	})
}

// assertCursorRowTint checks one rendered Panel content row of width w:
// the border cells (0 and w-1) carry no background, and every inner cell
// carries exactly the want background SGR parameter string.
func assertCursorRowTint(t *testing.T, row string, w int, want string) {
	t.Helper()

	cells := cellBackgrounds(row)
	if len(cells) != w {
		t.Fatalf("row has %d cells, want %d: %q", len(cells), w, row)
	}
	for i, bg := range cells {
		switch {
		case i == 0 || i == w-1:
			if bg != "" {
				t.Errorf("border cell %d carries background %q, want none", i, bg)
			}
		case bg != want:
			t.Errorf("inner cell %d carries background %q, want %q", i, bg, want)
		}
	}
	if !strings.Contains(row, want) {
		t.Fatalf("row %q never emits %s at all", row, want)
	}
}

// cellBackgrounds returns, for every printable cell of s in order, the
// background SGR parameter string active at that cell ("" when none) —
// the way a terminal tracking SGR state sees each cell, resets included.
func cellBackgrounds(s string) []string {
	var (
		out []string
		bg  string
		i   int
	)
	for i < len(s) {
		if s[i] == 0x1b {
			j := ansiSeqEnd(s, i)
			if params, ok := sgrParams(s[i:j]); ok {
				bg = applySGRState(bg, params)
			}
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size < 1 {
			size = 1
		}
		out = append(out, bg)
		i += size
	}
	return out
}

// applySGRState updates a tracked background parameter string with one
// SGR sequence's parameters, the subset of SGR a Theme render can emit:
// 0 (reset) and 49 (default background) clear it, 48;2;r;g;b and 48;5;n
// set it. Foreground parameters are ignored.
func applySGRState(bg string, params []string) string {
	for k := 0; k < len(params); k++ {
		switch params[k] {
		case "0", "49":
			bg = ""
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
				bg = strings.Join(params[k:k+span+1], ";")
				k += span
			}
		}
	}
	return bg
}

// rowWidth is a rendered row's visible width in cells.
func rowWidth(row string) int {
	return lipgloss.Width(row)
}

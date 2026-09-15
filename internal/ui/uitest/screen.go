// screen.go implements contract §6's frame helpers: Screen and PaneScreen
// render a model or a pane to (styled, plain), AssertGrid holds a rendered
// frame to the exactly-h-lines-of-exactly-w-cells invariant every View must
// satisfy (conventions §4 rule 2), and SweepSizes returns the size sweep
// the invariant is checked over.
package uitest

import (
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui"
)

// Screen renders m's View content, returning (styled, plain) where
// plain = ansi.Strip. It takes the tea.View's Content — the string a
// terminal would draw — never View itself, so a screen test asserts on
// exactly what the shell composes. A pane is rendered through PaneScreen.
func Screen(m tea.Model) (styled, plain string) {
	styled = m.View().Content
	return styled, ansi.Strip(styled)
}

// PaneScreen renders p at w×h, returning (styled, plain) where
// plain = ansi.Strip.
func PaneScreen(p ui.Pane, w, h int) (styled, plain string) {
	styled = p.View(w, h)
	return styled, ansi.Strip(styled)
}

// AssertGrid fails unless plain has exactly h lines of exactly w cells each.
// Widths are cells, not bytes (conventions §4 rule 1): ansi.StringWidth is
// the measure, so a line of "▌" or "…" costs one cell however many bytes it
// encodes as.
func AssertGrid(t *testing.T, plain string, w, h int) {
	t.Helper()
	assertGrid(t, plain, w, h)
}

// assertGrid is AssertGrid's testing.TB-general core. The contract pins the
// exported signature to *testing.T — the screens' only kind of caller — and
// the TB-general form exists so this package's own test can drive the
// failure paths on a recording TB instead of failing a real one.
func assertGrid(tb testing.TB, plain string, w, h int) {
	tb.Helper()

	lines := strings.Split(plain, "\n")
	if len(lines) != h {
		tb.Errorf("grid is %d lines, want %d", len(lines), h)
		return
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != w {
			tb.Errorf("line %d is %d cells, want %d: %q", i+1, got, w, line)
		}
	}
}

// sweepHeights are the heights the size sweep runs at (contract §6).
var sweepHeights = []int{24, 25, 30, 31, 32, 40, 60}

// sweepExtras are the boundary pairs and the below-minimum sizes the sweep
// adds to the 80..220 grid (contract §6).
var sweepExtras = [][2]int{
	{99, 30}, {100, 30}, {179, 40}, {180, 40}, // D1's drop points
	{79, 24}, {80, 23}, {72, 20}, {120, 20}, // the too-small notices
}

// SweepSizes returns the size-sweep set: every width 80..220 × heights
// {24,25,30,31,32,40,60}, plus the boundary pairs (99,30) (100,30) (179,40)
// (180,40) and the below-minimum sizes (79,24) (80,23) (72,20) (120,20),
// de-duplicated and sorted by (W,H) — 991 entries, first (72,20), last
// (220,60).
func SweepSizes() []struct{ W, H int } {
	seen := make(map[[2]int]bool, 991)
	var out []struct{ W, H int }
	add := func(w, h int) {
		if seen[[2]int{w, h}] {
			return
		}
		seen[[2]int{w, h}] = true
		out = append(out, struct{ W, H int }{w, h})
	}
	for w := 80; w <= 220; w++ {
		for _, h := range sweepHeights {
			add(w, h)
		}
	}
	for _, p := range sweepExtras {
		add(p[0], p[1])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].W != out[j].W {
			return out[i].W < out[j].W
		}
		return out[i].H < out[j].H
	})
	return out
}

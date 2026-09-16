// golden_test.go is the subtask's frozen verification pair: TestReviewSweep
// holds the pane to the exactly-h-lines-of-exactly-w-cells invariant over
// the harness's whole size sweep (s2-screens.md "Every screen"), and
// TestReviewGoldens pins the rendered pane at the four checkpoint sizes in
// both polarities (00-conventions.md §5).
package review

import (
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// newVaultModel builds a review pane over uitest.PublicVault (the staged
// four-op fixture changeset, contract §6 note 2) at the given polarity and
// drives its load to completion. The pane renders the Diff mode by
// default, cursor on the first stop.
func newVaultModel(t *testing.T, dark bool) ui.Pane {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "review-tests")
	return initModel(t, uitest.Deps(v, dark, nil))
}

// TestReviewSweep renders the pane at every size of uitest.SweepSizes with
// W >= 80 and H >= 24 — the shell gives a pane the terminal's h-2 — and
// holds each render to exactly h lines of exactly w cells.
func TestReviewSweep(t *testing.T) {
	m := newVaultModel(t, true)

	for _, s := range uitest.SweepSizes() {
		if s.W < ui.MinWidth || s.H < ui.MinHeight {
			continue
		}
		_, got := uitest.PaneScreen(m, s.W, s.H-2)
		if lines := strings.Count(got, "\n") + 1; lines != s.H-2 {
			t.Fatalf("pane at %dx%d (h=%d) returned %d lines", s.W, s.H, s.H-2, lines)
		}
		uitest.AssertGrid(t, got, s.W, s.H-2)
	}
}

// goldenSizes are the frozen checkpoint sizes (s2-screens.md "Every
// screen"): pane height = terminal height - 2.
var goldenSizes = [][2]int{{80, 22}, {100, 28}, {120, 38}, {200, 58}}

// TestReviewGoldens pins the pane's rendered frame at the checkpoint
// sizes: a styled ANSI golden per polarity and one plain golden, which is
// polarity-independent — asserted, not assumed.
func TestReviewGoldens(t *testing.T) {
	dark := newVaultModel(t, true)
	light := newVaultModel(t, false)

	for _, sz := range goldenSizes {
		w, h := sz[0], sz[1]
		styledDark, plainDark := uitest.PaneScreen(dark, w, h)
		styledLight, plainLight := uitest.PaneScreen(light, w, h)
		if plainDark != plainLight {
			t.Errorf("%dx%d: light polarity changed the plain text", w, h)
		}

		base := fmt.Sprintf("testdata/golden/review-%dx%d", w, h)
		// joinLines: goldens end with exactly one trailing newline
		// (00-conventions.md §3), so the payload carries one too.
		join := func(s string) string { return s + "\n" }
		testutil.GoldenString(t, base+"-dark.ansi.golden", join(styledDark))
		testutil.GoldenString(t, base+"-light.ansi.golden", join(styledLight))
		testutil.GoldenString(t, base+".txt.golden", join(plainDark))
	}
}

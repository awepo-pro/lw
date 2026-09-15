// sweep_test.go holds the size-sweep and golden checkpoints the browse
// screen's subtask freezes (s2-screens.md "Every screen" and T07): a grid
// invariant over every uitest.SweepSizes() size at W >= 80, H >= 24 (the
// shell gives panes h-2), and goldens at the four checkpoint sizes over
// uitest.PublicVault, in both polarities, styled and plain.
package browse

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// goldenSizes are the pane-level checkpoint sizes (a 80x24 terminal's pane
// is 80x22, and so on: the shell's header and footer rows are not ours).
var goldenSizes = [][2]int{{80, 22}, {100, 28}, {120, 38}, {200, 58}}

// TestBrowseSweep asserts the exactly-h-lines-of-exactly-w-cells invariant
// (conventions §4 rule 2) at every swept size with W >= 80 and H >= 24, at
// the pane height the shell would hand the pane (H-2), on the public
// fixture vault.
func TestBrowseSweep(t *testing.T) {
	v := uitest.PublicVault(t, "public")
	p := New(uitest.Deps(v, true, nil))

	for _, sz := range uitest.SweepSizes() {
		if sz.W < 80 || sz.H < 24 {
			continue
		}
		_, plain := uitest.PaneScreen(p, sz.W, sz.H-2)
		uitest.AssertGrid(t, plain, sz.W, sz.H-2)
	}
}

// TestBrowseGolden pins the four checkpoint renders, dark and light, styled
// and plain. The plain render is polarity-independent (conventions §5): the
// test asserts that and writes one plain file per size, reviewed by hand
// against the frozen layout rules.
func TestBrowseGolden(t *testing.T) {
	v := uitest.PublicVault(t, "public")

	for _, sz := range goldenSizes {
		var darkPlain string
		for _, dark := range []bool{true, false} {
			p := New(uitest.Deps(v, dark, nil))
			styled, plain := uitest.PaneScreen(p, sz[0], sz[1])

			pol := "light"
			if dark {
				pol, darkPlain = "dark", plain
			}
			testutil.GoldenString(t, goldenPath(fmt.Sprintf("browse-%dx%d-%s.ansi", sz[0], sz[1], pol)), joinLines(styled))

			if !dark && plain != darkPlain {
				t.Fatalf("browse-%dx%d: light polarity changed the plain text", sz[0], sz[1])
			}
		}
		testutil.GoldenString(t, goldenPath(fmt.Sprintf("browse-%dx%d.txt", sz[0], sz[1])), joinLines(darkPlain))
	}
}

// joinLines matches testutil.Golden's own on-disk normalization (exactly one
// trailing newline): Golden only applies that when writing with -update, not
// when comparing, so a caller must pass it consistently itself.
func joinLines(s string) string {
	return s + "\n"
}

// goldenPath is a golden file's path under this package's testdata.
func goldenPath(name string) string {
	return filepath.Join("testdata", "golden", name+".golden")
}

// browseKey drives one key press through the pane.
func browseKey(t *testing.T, p *Model, s string) *Model {
	t.Helper()
	next, _ := p.Update(uitest.Key(s))
	return next.(*Model)
}

// TestBrowseFinderGolden pins the `/` finder as its frozen checkpoint draws
// it: a centred ui.Panel titled `Find`, at 120x38, with a query typed.
func TestBrowseFinderGolden(t *testing.T) {
	v := uitest.PublicVault(t, "public")

	openFinder := func(dark bool) *Model {
		p := New(uitest.Deps(v, dark, nil)).(*Model)
		m := browseKey(t, p, "/")
		for _, r := range "sour" {
			m = browseKey(t, m, string(r))
		}
		return m
	}

	var darkPlain string
	for _, dark := range []bool{true, false} {
		m := openFinder(dark)
		styled, plain := uitest.PaneScreen(m, 120, 38)

		pol := "light"
		if dark {
			pol, darkPlain = "dark", plain
		}
		testutil.GoldenString(t, goldenPath(fmt.Sprintf("finder-120x38-%s.ansi", pol)), joinLines(styled))

		if !dark && plain != darkPlain {
			t.Fatal("finder: light polarity changed the plain text")
		}
	}
	testutil.GoldenString(t, goldenPath("finder-120x38.txt"), joinLines(darkPlain))
}

// TestFinderPanelIsCentredFind: the open finder's pane is a `Find`-titled
// panel (a real ui.Panel, so the grid invariant still holds) centred in an
// otherwise blank pane — the tree is not drawn under it.
func TestFinderPanelIsCentredFind(t *testing.T) {
	v := uitest.PublicVault(t, "public")
	p := New(uitest.Deps(v, true, nil)).(*Model)
	m := p
	for _, k := range []string{"/", "s", "o", "u", "r"} {
		m = browseKey(t, m, k)
	}

	_, plain := uitest.PaneScreen(m, 120, 38)
	uitest.AssertGrid(t, plain, 120, 38)

	if !strings.Contains(plain, "╭ Find ") {
		t.Fatalf("the open finder is not a Find-titled panel:\n%s", plain)
	}
	if strings.Contains(plain, "╭ Pages ") {
		t.Fatalf("the tree is still drawn while the finder is open:\n%s", plain)
	}

	// Centred: bw = min(64, w-4) = 64, bh = content+2 = 4, so the panel's
	// top border starts at cell (120-64)/2 = 28 on row (38-4)/2 = 17.
	rows := strings.Split(plain, "\n")
	top := -1
	for i, row := range rows {
		if strings.Contains(row, "╭ Find ") {
			top = i
			break
		}
	}
	if top < 0 {
		t.Fatal("unreachable: Find panel located then not found")
	}
	if top != 17 || !strings.HasPrefix(rows[top], strings.Repeat(" ", 28)) {
		t.Fatalf("the Find panel's top border is at row %d, want the centred row 17 col 28: %q", top, rows[top])
	}
}

// TestCapturesTextTracksFinderOpen is the pane-side half of C27: the shell
// type-asserts ui.TextCapturer on the active pane and delivers printable
// keys while it returns true.
func TestCapturesTextTracksFinderOpen(t *testing.T) {
	v := uitest.PublicVault(t, "public")
	p := New(uitest.Deps(v, true, nil))

	tc, ok := p.(ui.TextCapturer)
	if !ok {
		t.Fatal("browse does not implement ui.TextCapturer")
	}
	if tc.CapturesText() {
		t.Fatal("CapturesText() is true with the finder closed, want false")
	}

	m := browseKey(t, p.(*Model), "/")
	if !m.CapturesText() {
		t.Fatal("CapturesText() is false while the finder is open, want true")
	}

	m = browseKey(t, m, "esc")
	if m.CapturesText() {
		t.Fatal("CapturesText() is true after esc closed the finder, want false")
	}
}

// TestBrowseCapturesTextInShell drives a real ui.NewApp (C27): with Browse
// active and the finder open, `q` and `?` type into the query instead of
// quitting or opening the help overlay.
func TestBrowseCapturesTextInShell(t *testing.T) {
	v := uitest.PublicVault(t, "public")
	d := uitest.Deps(v, true, nil)

	app := ui.NewApp(ui.Options{
		Deps:  d,
		Panes: map[ui.Screen]ui.Pane{ui.ScreenBrowse: New(d)},
		Start: ui.ScreenBrowse,
	})
	m := uitest.Drive(t, app,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.BackgroundColorMsg{Color: color.Black},
	)
	m = uitest.Drive(t, m, uitest.Key("/"), uitest.Key("q"), uitest.Key("?"))

	_, plain := uitest.Screen(m)
	if !strings.Contains(plain, "/q?") {
		t.Fatalf("q and ? did not type into the finder query:\n%s", plain)
	}
	if strings.Contains(plain, "╭ Keys ") {
		t.Fatal("? opened the help overlay while the finder was open")
	}
}

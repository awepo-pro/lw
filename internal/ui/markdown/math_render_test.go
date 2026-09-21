// math_render_test.go pins §F.2 — the seam: TeX math in a real page renders
// as unicode through the shared renderBlock pipeline (fences and money
// dollars stay literal), the mathpage golden set is frozen, and the page
// and fragment entry points style converted math identically (the mirror
// of ask/render_test.go's heading_matches_a_page_render).
package markdown

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/testutil"
)

// readMathPage loads the frozen §F.2.2 fixture.
func readMathPage(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile("testdata/mathpage.md")
	if err != nil {
		t.Fatalf("read testdata/mathpage.md: %v", err)
	}
	return src
}

// TestMathRendersThroughPipeline renders mathpage.md (dark, 80) and holds
// the §F.2.3 assertions on the ANSI-stripped output: converted math is
// present, raw TeX and unconverted candidates are not.
func TestMathRendersThroughPipeline(t *testing.T) {
	out, err := NewRenderer().Render(readMathPage(t), Options{Width: 80, Style: darkStyle})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	plain := ansi.Strip(strings.Join(out, "\n"))

	for _, want := range []string{"q₀ + q₁", "v′ = qvq⁻¹", "½θ", "$(ip -o link show"} {
		if !strings.Contains(plain, want) {
			t.Errorf("rendered mathpage does not contain %q:\n%s", want, plain)
		}
	}
	for _, bad := range []string{`\mathbf`, "q_0", "$q"} {
		if strings.Contains(plain, bad) {
			t.Errorf("rendered mathpage still contains %q:\n%s", bad, plain)
		}
	}
}

// TestRenderMathGolden freezes mathpage.md at width 80 in both polarities
// plus a plain rendering (§F.2.2), exactly the way page.md's goldens are
// taken. Generated once with -update, then immutable.
func TestRenderMathGolden(t *testing.T) {
	src := readMathPage(t)
	for _, tc := range []struct {
		kind  string
		style Style
	}{
		{"dark", darkStyle},
		{"light", lightStyle},
	} {
		out, err := NewRenderer().Render(src, Options{Width: 80, Style: tc.style})
		if err != nil {
			t.Fatalf("render %s: %v", tc.kind, err)
		}
		testutil.GoldenString(t,
			filepath.Join("testdata", "golden", fmt.Sprintf("mathpage-80-%s.ansi.golden", tc.kind)),
			joinLines(out))
	}

	plain, err := NewRenderer().Render(src, Options{Width: 80, Style: darkStyle, Plain: true})
	if err != nil {
		t.Fatalf("render plain: %v", err)
	}
	testutil.GoldenString(t, filepath.Join("testdata", "golden", "mathpage-80.txt.golden"), joinLines(plain))
}

// TestMathSGRParityPageAndFragment: the same converted-math paragraph
// through Render and through RenderFragment carries identical SGR runs, so
// an Ask answer's math is styled exactly like a page's (§F.2.4).
func TestMathSGRParityPageAndFragment(t *testing.T) {
	const para = "A point is rotated via the sandwich product $v' = qvq^{-1}$."

	page, err := NewRenderer().Render([]byte(para), Options{Width: 80, Style: darkStyle})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	frag, err := NewRenderer().RenderFragment([]byte(para), Options{Width: 80, Style: darkStyle})
	if err != nil {
		t.Fatalf("RenderFragment: %v", err)
	}

	pageLine := lineWithMath(t, page, "qvq⁻¹")
	fragLine := lineWithMath(t, frag, "qvq⁻¹")
	if strings.Join(sgrRuns(cutAtText(pageLine)), "\x1b") != strings.Join(sgrRuns(cutAtText(fragLine)), "\x1b") {
		t.Fatalf("converted math renders with different SGR:\npage     %q\nfragment %q", pageLine, fragLine)
	}
}

// TestVertTableCellSurvives pins A15-2: a table cell holding $\vert v\vert^2$
// renders with the converted ∣v∣² visible in that cell. Before the fix \vert
// emitted ASCII |, glamour's table parser reshaped the row on it, and the
// value cell rendered empty — silent content loss.
func TestVertTableCellSurvives(t *testing.T) {
	const src = "| norm | value |\n" +
		"| ---- | ---- |\n" +
		"| norm | $\\vert v\\vert^2$ |\n"

	out, err := NewRenderer().Render([]byte(src), Options{Width: 80, Style: darkStyle})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Fails with "no line contains ∣v∣²" exactly when the cell was dropped.
	lineWithMath(t, out, "∣v∣²")
}

// cutAtText drops a line's trailing pad — whitespace and the SGR wrappers
// around it — until the line ends at visible text. The page path pads its
// lines to the width inside styled chunks while the fragment path truncates
// that padding away but keeps the (now empty, zero-cell) chunk wrappers —
// fragment.go's documented cut-back — so the runs beyond the text differ by
// design; every run covering the text itself must still be identical, and
// any styling difference inside the text still fails the comparison.
func cutAtText(l string) string {
	for {
		t := strings.TrimRight(l, " \t")
		t = trailingSGRRe.ReplaceAllString(t, "")
		if t == l {
			return l
		}
		l = t
	}
}

// trailingSGRRe matches a run of SGR sequences at the end of a line.
var trailingSGRRe = regexp.MustCompile(`(?:\x1b\[[0-9;]*m)+$`)

// lineWithMath returns the first line whose visible text contains sub,
// failing if conversion never happened (the raw `$v' = …` source never
// strips to converted math).
func lineWithMath(t *testing.T, lines []string, sub string) string {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(ansi.Strip(l), sub) {
			return l
		}
	}
	t.Fatalf("no line contains %q after strip (math not converted?) in:\n%s",
		sub, strings.Join(lines, "\n"))
	return ""
}

// sgrRuns returns every SGR parameter list in s, in order — the run
// sequence two renders of the same line are compared by.
func sgrRuns(s string) []string {
	re := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

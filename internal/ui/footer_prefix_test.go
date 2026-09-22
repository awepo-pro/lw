// footer_prefix_test.go pins the FooterPrefix seam's own rule (workflow
// 016 F.M3): the morsel is chrome — it renders at column 1 with the whole
// binding list keeping its place beside it, and at a width where it cannot,
// it is dropped whole: never clipped, and no binding ever gives way to it.
// A pane without a morsel renders byte-identically through either path.
package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestFooterPrefixChromeRule(t *testing.T) {
	theme, _ := footerTestFixtures(t)
	bindings := []key.Binding{
		helpBinding("enter", "enter", "send"),
		helpBinding("ctrl+r", "ctrl+r", "review"),
	}
	const morsel = "██▀██▀█ "

	// Wide enough: morsel at column 1, the bindings right behind it.
	got := ansi.Strip(prefixedFooterLine(theme, 60, morsel, lipgloss.Style{}, bindings))
	if m := strings.Index(got, morsel); m != 1 {
		t.Fatalf("footer = %q, want the morsel at column 1", got)
	}
	if k := strings.Index(got, "enter send"); k < 0 || k < len(morsel) {
		t.Fatalf("footer = %q, want the bindings after the morsel", got)
	}

	// Too narrow for morsel + whole list: the morsel yields whole and the
	// row is byte-for-byte what footerLine would have drawn — bindings
	// dropping from the end under the usual rule, never for the morsel.
	want := ansi.Strip(footerLine(theme, 40, bindings))
	got = ansi.Strip(prefixedFooterLine(theme, 40, morsel, lipgloss.Style{}, bindings))
	if strings.Contains(got, "█") {
		t.Fatalf("footer at 40 = %q, want the morsel dropped whole", got)
	}
	if got != want {
		t.Fatalf("footer at 40 with a morsel = %q, want the plain footerLine bytes %q", got, want)
	}

	// The yield is decided BEFORE the drop loop, against the WHOLE list:
	// at 34 footerLine itself drops `ctrl+r review`, and morsel + the
	// reduced list would fit — so a morsel checked after the drop would
	// render beside a binding it cost. It must not: same bytes as the
	// plain footer, no morsel.
	want = ansi.Strip(footerLine(theme, 34, bindings))
	got = ansi.Strip(prefixedFooterLine(theme, 34, morsel, lipgloss.Style{}, bindings))
	if strings.Contains(got, "█") {
		t.Fatalf("footer at 34 = %q, the morsel must never cost a binding", got)
	}
	if got != want {
		t.Fatalf("footer at 34 with a morsel = %q, want the plain footerLine bytes %q", got, want)
	}

	// No morsel: both paths agree byte for byte, so every pane without the
	// interface renders exactly as before.
	if a, b := ansi.Strip(footerLine(theme, 90, bindings)),
		ansi.Strip(prefixedFooterLine(theme, 90, "", lipgloss.Style{}, bindings)); a != b {
		t.Fatalf("empty morsel changed the footer:\n%q\n%q", a, b)
	}
}

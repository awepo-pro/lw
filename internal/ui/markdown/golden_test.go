package markdown

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// darkStyle and lightStyle are the built-in adaptive palette (contract §3
// note 1, eleven tokens since W5), reproduced here rather than imported:
// this package cannot import internal/ui, and a caller builds
// markdown.Style from ui.Theme's Palette the same way at runtime
// (contract §3).
var darkStyle = Style{
	Dark: true, Fg: "#D8DDE4", Muted: "#8C95A2", Faint: "#5E6672", Border: "#353C47",
	Accent: "#7AB2F2", Good: "#6BC28E", Warn: "#E2B45A", Bad: "#EF7F76",
	Heading: "#C3A0F0", Code: "#6CC7C9",
}

var lightStyle = Style{
	Dark: false, Fg: "#1C2128", Muted: "#586270", Faint: "#8A929E", Border: "#C6CDD6",
	Accent: "#1D62C2", Good: "#1D7A4B", Warn: "#93660A", Bad: "#B03A33",
	Heading: "#7A45C2", Code: "#17727A",
}

// TestRenderGolden renders testdata/page.md at the two checkpoint widths in
// both polarities, plus a polarity-independent plain rendering, against the
// goldens the T02 frozen block names.
func TestRenderGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/page.md")
	if err != nil {
		t.Fatalf("read testdata/page.md: %v", err)
	}

	for _, width := range []int{80, 120} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			dark, err := NewRenderer().Render(src, Options{Width: width, Style: darkStyle})
			if err != nil {
				t.Fatalf("render dark: %v", err)
			}
			testutil.GoldenString(t, goldenPath(width, "dark.ansi"), joinLines(dark))

			light, err := NewRenderer().Render(src, Options{Width: width, Style: lightStyle})
			if err != nil {
				t.Fatalf("render light: %v", err)
			}
			testutil.GoldenString(t, goldenPath(width, "light.ansi"), joinLines(light))

			plainDark, err := NewRenderer().Render(src, Options{Width: width, Style: darkStyle, Plain: true})
			if err != nil {
				t.Fatalf("render plain (dark style): %v", err)
			}
			testutil.GoldenString(t, filepath.Join("testdata", "golden", fmt.Sprintf("page-%d.txt.golden", width)), joinLines(plainDark))

			plainLight, err := NewRenderer().Render(src, Options{Width: width, Style: lightStyle, Plain: true})
			if err != nil {
				t.Fatalf("render plain (light style): %v", err)
			}
			if joinLines(plainDark) != joinLines(plainLight) {
				t.Errorf("width %d: plain output depends on polarity, want it independent", width)
			}
		})
	}
}

func goldenPath(width int, kind string) string {
	return filepath.Join("testdata", "golden", fmt.Sprintf("page-%d-%s.golden", width, kind))
}

// joinLines matches testutil.Golden's own on-disk normalization (exactly
// one trailing newline): Golden only applies that when writing with
// -update, not when comparing, so a caller must pass it consistently
// itself.
func joinLines(lines []string) string {
	return strings.Join(lines, "\n") + "\n"
}

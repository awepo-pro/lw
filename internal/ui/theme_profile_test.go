// theme_profile_test.go pins the W5 theme changes: CursorBg resolved per
// colour profile (contract §3 note 7, W5 F1/C35 — inside tmux without RGB,
// Bubble Tea reports ANSI256 and #1A2331 must not round to xterm navy), and
// the two new markdown tokens Heading and Code (W5 F3/D-3W, amending D7).
package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
)

// cursorRowOf draws a one-line panel with its only content row as the
// cursor row and returns that row rendered.
func cursorRowOf(t *testing.T, theme Theme) string {
	t.Helper()
	const w, h = 24, 3
	rows := Panel(theme, PanelSpec{Lines: []string{"row"}, CursorRow: 0}, w, h)
	return rows[1]
}

// TestThemeProfile asserts contract §3 note 7: the cursor tint follows the
// terminal's colour profile, nothing else about the theme does.
func TestThemeProfile(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}

	t.Run("truecolor_keeps_palette_tint", func(t *testing.T) {
		// LoadTheme returns a TrueColor theme, and WithProfile(TrueColor)
		// keeps it: the palette's cursor hex, both polarities.
		for _, tc := range []struct {
			theme Theme
			want  string
		}{
			{th.WithDark(true), "48;2;26;35;49"},
			{th.WithDark(false), "48;2;230;238;249"},
			{th.WithDark(true).WithProfile(colorprofile.TrueColor), "48;2;26;35;49"},
			{th.WithDark(false).WithProfile(colorprofile.TrueColor), "48;2;230;238;249"},
		} {
			assertCursorRowTint(t, cursorRowOf(t, tc.theme), 24, tc.want)
		}
	})

	t.Run("ansi256_uses_grey_236_dark_254_light", func(t *testing.T) {
		// The fixed neutral greys — never the saturated xterm navy the old
		// downsampling produced, and no truecolor sequence anywhere.
		dark := cursorRowOf(t, th.WithDark(true).WithProfile(colorprofile.ANSI256))
		assertCursorRowTint(t, dark, 24, "48;5;236")
		if strings.Contains(dark, "48;5;17") {
			t.Error("dark ANSI256 cursor row rounds to xterm 17 (navy): the bug C35 measured")
		}
		if strings.Contains(dark, "48;2;") {
			t.Error("dark ANSI256 cursor row carries a truecolor background")
		}
		assertCursorRowTint(t, cursorRowOf(t, th.WithDark(false).WithProfile(colorprofile.ANSI256)), 24, "48;5;254")
	})

	t.Run("below_256_has_no_background", func(t *testing.T) {
		theme := th.WithDark(true).WithProfile(colorprofile.ANSI)
		if theme.CursorBg != nil {
			t.Fatalf("ANSI CursorBg = %v, want nil", theme.CursorBg)
		}
		row := cursorRowOf(t, theme)
		if !strings.Contains(row, "▌") {
			t.Fatalf("ANSI cursor row = %q, want the accent ▌ gutter", row)
		}
		if strings.Contains(row, "48;") {
			t.Fatalf("ANSI cursor row = %q, want no background sequence", row)
		}
	})

	t.Run("with_dark_keeps_profile", func(t *testing.T) {
		// WithProfile keeps the polarity and WithDark keeps the profile:
		// the two re-resolutions compose in either order.
		got := th.WithProfile(colorprofile.ANSI256).WithDark(false)
		assertCursorRowTint(t, cursorRowOf(t, got), 24, "48;5;254")
	})
}

// TestThemeMarkdownTokens asserts the W5 F3 tokens (D-3W, amending D7):
// default hexes on both polarities, foreground-only styles, and the
// heading_*/code_* theme-file keys.
func TestThemeMarkdownTokens(t *testing.T) {
	t.Run("default_palette", func(t *testing.T) {
		setConfigDir(t)
		th, err := LoadTheme("")
		if err != nil {
			t.Fatalf("LoadTheme: %v", err)
		}

		dark, light := th.WithDark(true).Palette, th.WithDark(false).Palette
		if dark.Heading != "#C3A0F0" || dark.Code != "#6CC7C9" {
			t.Fatalf("dark Heading/Code = %q/%q, want #C3A0F0/#6CC7C9", dark.Heading, dark.Code)
		}
		if light.Heading != "#7A45C2" || light.Code != "#17727A" {
			t.Fatalf("light Heading/Code = %q/%q, want #7A45C2/#17727A", light.Heading, light.Code)
		}

		for _, tc := range []struct {
			name  string
			style lipglossStyle
			fg    string
		}{
			{"dark Heading", th.WithDark(true).Heading, "38;2;195;160;240"},
			{"dark Code", th.WithDark(true).Code, "38;2;108;199;201"},
			{"light Heading", th.WithDark(false).Heading, "38;2;122;69;194"},
			{"light Code", th.WithDark(false).Code, "38;2;23;114;122"},
		} {
			out := tc.style.Render("x")
			if !strings.Contains(out, tc.fg) {
				t.Errorf("%s.Render(x) = %q, want foreground %s", tc.name, out, tc.fg)
			}
			if strings.Contains(out, "48;") {
				t.Errorf("%s.Render(x) = %q, carries a background", tc.name, out)
			}
		}
	})

	t.Run("theme_file_keys", func(t *testing.T) {
		configDir := setConfigDir(t)
		writeConfigFile(t, configDir, "theme.toml", `
heading_dark = "#112233"
code_light = "#445566"
`)
		th, err := LoadTheme("")
		if err != nil {
			t.Fatalf("LoadTheme: %v", err)
		}
		if len(th.Warnings) != 0 {
			t.Fatalf("Warnings = %v, want none — heading_*/code_* are known keys", th.Warnings)
		}
		if got := th.WithDark(true).Palette.Heading; got != "#112233" {
			t.Fatalf("dark Heading = %q, want the heading_dark override", got)
		}
		if got := th.WithDark(false).Palette.Code; got != "#445566" {
			t.Fatalf("light Code = %q, want the code_light override", got)
		}
	})
}

// lipglossStyle is the one method of lipgloss.Style these assertions need.
type lipglossStyle interface {
	Render(...string) string
}

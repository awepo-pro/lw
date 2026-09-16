// theme.go implements contract §3's Theme, Palette, LoadTheme and
// Theme.WithDark — the one adaptive palette (plan §4.4), locked to a
// polarity by the reserved names "dark"/"light", and a user
// themes/<name>.toml loaded on top of the default palette for anything else.
//
// The TOML shape here follows a pattern, not a copy: yorukot/superfile ships
// a flat key = value theme.toml, one file per colour polarity, and a user
// picks the file that matches their terminal. lipgloss v2 has no
// AdaptiveColor — colour is resolved by calling a LightDarkFunc at
// Style-build time instead — so a theme file stays flat and superfile-style
// but pairs every semantic colour with explicit "<name>_light" /
// "<name>_dark" keys, letting any theme render correctly on both
// polarities. See NOTICE for the full attribution and the MIT licence text
// this pattern is credited under. The on-disk side — the themeFile shape
// and the lookup rules — lives in theme_file.go.
package ui

import (
	"fmt"
	"image/color"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// colorPair is one semantic colour's hex value on a light terminal and on a
// dark one. lipgloss.LightDark picks between them when a Theme's styles are
// built.
type colorPair struct {
	Light string
	Dark  string
}

// themeColors is every semantic colour a theme defines. Field names double
// as the "<name>_light" / "<name>_dark" TOML key prefixes a theme file
// names — see themeFile.
type themeColors struct {
	Fg       colorPair
	Muted    colorPair
	Faint    colorPair
	Border   colorPair
	Accent   colorPair
	CursorBg colorPair
	Good     colorPair
	Warn     colorPair
	Bad      colorPair
	Heading  colorPair
	Code     colorPair
}

// defaultThemeColors is lw 2's one compiled-in palette (contract §3 note 1,
// mockgen.PALETTE): one half for a light terminal, one for a dark one. Every
// theme — adaptive, forced-dark, forced-light, or a user file — starts from
// this; nothing else is compiled in.
func defaultThemeColors() themeColors {
	return themeColors{
		Fg:       colorPair{Light: "#1C2128", Dark: "#D8DDE4"},
		Muted:    colorPair{Light: "#586270", Dark: "#8C95A2"},
		Faint:    colorPair{Light: "#8A929E", Dark: "#5E6672"},
		Border:   colorPair{Light: "#C6CDD6", Dark: "#353C47"},
		Accent:   colorPair{Light: "#1D62C2", Dark: "#7AB2F2"},
		CursorBg: colorPair{Light: "#E6EEF9", Dark: "#1A2331"},
		Good:     colorPair{Light: "#1D7A4B", Dark: "#6BC28E"},
		Warn:     colorPair{Light: "#93660A", Dark: "#E2B45A"},
		Bad:      colorPair{Light: "#B03A33", Dark: "#EF7F76"},
		Heading:  colorPair{Light: "#7A45C2", Dark: "#C3A0F0"},
		Code:     colorPair{Light: "#17727A", Dark: "#6CC7C9"},
	}
}

// Palette is one polarity's resolved tokens, as "#RRGGBB" (contract §3;
// Heading and Code added 2026-09-16, W5 F3/D-3W: eleven tokens). It is what
// markdown.Style is built from — the two structs share field names on
// purpose (01-contract.md §3).
type Palette struct {
	Fg, Muted, Faint, Border, Accent, CursorBg, Good, Warn, Bad string
	Heading, Code                                               string
}

// paletteFrom resolves colors to one polarity's Palette.
func paletteFrom(colors themeColors, isDark bool) Palette {
	pick := func(p colorPair) string {
		if isDark {
			return p.Dark
		}
		return p.Light
	}
	return Palette{
		Fg:       pick(colors.Fg),
		Muted:    pick(colors.Muted),
		Faint:    pick(colors.Faint),
		Border:   pick(colors.Border),
		Accent:   pick(colors.Accent),
		CursorBg: pick(colors.CursorBg),
		Good:     pick(colors.Good),
		Warn:     pick(colors.Warn),
		Bad:      pick(colors.Bad),
		Heading:  pick(colors.Heading),
		Code:     pick(colors.Code),
	}
}

// Theme is lw's resolved set of lipgloss v2 styles for one background
// polarity (contract §3). Build one with LoadTheme, then call WithDark
// whenever the terminal's real polarity becomes known — a no-op when the
// theme's polarity was forced by name ("dark"/"light").
type Theme struct {
	// IsDark is the polarity these styles were built for.
	IsDark bool
	// Warnings collects non-fatal problems found while loading a theme — an
	// unknown key in theme.toml or themes/<name>.toml, a background_* key
	// (always ignored), or a name with no themes/<name>.toml file behind it
	// — so a caller can surface them without LoadTheme failing
	// (00-conventions.md §2: no log.Fatal, no printing from a library).
	Warnings []string

	// Fg, Muted, Faint, Border, Accent, Good, Warn and Bad are foreground-only
	// styles: no Theme style ever sets a background (F4).
	Fg     lipgloss.Style
	Muted  lipgloss.Style
	Faint  lipgloss.Style
	Border lipgloss.Style
	Accent lipgloss.Style
	Good   lipgloss.Style
	Warn   lipgloss.Style
	Bad    lipgloss.Style
	// Heading and Code are the markdown role colours (W5 F3/D-3W): H1 and
	// H3+ headings, and inline code. Foreground only, like every other
	// Theme style.
	Heading lipgloss.Style
	Code    lipgloss.Style
	// Bold is Fg with bold set: the vault name, unfocused panel titles, and
	// the keys the footer prints.
	Bold lipgloss.Style
	// CursorBg is the cursor-row tint (plan §4.4) — the one place a
	// background colour appears at all. It is a color.Color, not a style: a
	// caller tints exactly the cursor row with it (ui.Panel's CursorRow).
	// Its value depends on the colour profile (contract §3 note 7): the
	// palette hex at TrueColor, a fixed neutral grey at ANSI256, and nil —
	// no background at all — below that. It starts as the palette hex; the
	// shell re-resolves it through WithProfile when Bubble Tea reports the
	// terminal's real profile.
	CursorBg color.Color
	// Palette is what markdown.Style is built from.
	Palette Palette

	colors  themeColors
	forced  bool                 // polarity forced by theme = "dark"/"light"; WithDark is then a no-op
	profile colorprofile.Profile // the profile CursorBg was resolved for
}

// WithDark rebuilds t's styles for the given polarity and returns the new
// Theme, unless t's polarity was forced by name ("dark"/"light"), in which
// case it is a no-op (contract §3). The colour profile is kept: CursorBg
// stays resolved for whatever WithProfile last set.
func (t Theme) WithDark(isDark bool) Theme {
	if t.forced {
		return t
	}
	return buildTheme(t.colors, isDark, t.forced, t.Warnings, t.profile)
}

// WithProfile re-resolves t's cursor tint for the colour profile p
// (contract §3 note 7, W5 F1/C35) and returns the new Theme: TrueColor
// keeps the palette's cursor hex, ANSI256 gets the fixed neutral grey
// (236 dark / 254 light — a theme file's cursor_* keys set only the
// TrueColor tint), and any lower profile gets nil, meaning no background
// at all. The polarity — forced or not — is kept.
func (t Theme) WithProfile(p colorprofile.Profile) Theme {
	return buildTheme(t.colors, t.IsDark, t.forced, t.Warnings, p)
}

// profileCursorBg resolves the cursor tint for one profile (contract §3
// note 7). below ANSI256 there is no background a terminal could show
// subtly, so the tint is dropped entirely and Panel draws only the accent
// gutter.
func profileCursorBg(colors themeColors, isDark bool, p colorprofile.Profile) color.Color {
	switch p {
	case colorprofile.TrueColor:
		if isDark {
			return lipgloss.Color(colors.CursorBg.Dark)
		}
		return lipgloss.Color(colors.CursorBg.Light)
	case colorprofile.ANSI256:
		if isDark {
			return lipgloss.ANSIColor(236)
		}
		return lipgloss.ANSIColor(254)
	default:
		return nil
	}
}

// buildTheme resolves colors for isDark and constructs every Theme style.
func buildTheme(colors themeColors, isDark, forced bool, warnings []string, profile colorprofile.Profile) Theme {
	ld := lipgloss.LightDark(isDark)
	resolve := func(p colorPair) color.Color {
		return ld(lipgloss.Color(p.Light), lipgloss.Color(p.Dark))
	}

	cursorBg := profileCursorBg(colors, isDark, profile)

	fg := lipgloss.NewStyle().Foreground(resolve(colors.Fg))
	muted := lipgloss.NewStyle().Foreground(resolve(colors.Muted))
	faint := lipgloss.NewStyle().Foreground(resolve(colors.Faint))
	border := lipgloss.NewStyle().Foreground(resolve(colors.Border))
	accent := lipgloss.NewStyle().Foreground(resolve(colors.Accent))
	good := lipgloss.NewStyle().Foreground(resolve(colors.Good))
	warn := lipgloss.NewStyle().Foreground(resolve(colors.Warn))
	bad := lipgloss.NewStyle().Foreground(resolve(colors.Bad))
	heading := lipgloss.NewStyle().Foreground(resolve(colors.Heading))
	code := lipgloss.NewStyle().Foreground(resolve(colors.Code))
	bold := fg.Bold(true)

	return Theme{
		IsDark:   isDark,
		Warnings: warnings,
		Fg:       fg,
		Muted:    muted,
		Faint:    faint,
		Border:   border,
		Accent:   accent,
		Good:     good,
		Warn:     warn,
		Bad:      bad,
		Heading:  heading,
		Code:     code,
		Bold:     bold,
		CursorBg: cursorBg,
		Palette:  paletteFrom(colors, isDark),

		colors:  colors,
		forced:  forced,
		profile: profile,
	}
}

// LoadTheme returns the named theme (contract §3). name comes from
// config.toml's `theme` key, and "" means "default".
//
// Every theme starts from the one compiled-in palette (defaultThemeColors):
// there is no second built-in palette to select any more. "" and "default"
// are adaptive — the terminal's reported polarity (WithDark) picks a half.
// "dark" and "light" are the same palette with the polarity forced, so
// WithDark becomes a no-op. Any other name is looked up as a user theme file
// at <config.ConfigDir()>/themes/<name>.toml, loaded on top of the default
// palette; a name with no such file warns and falls back to the default
// theme untouched (contract §3 note 2) — "nord" no longer names a compiled
// palette, so it takes this path like any unrecognised name.
//
// A theme file is the flat TOML shape documented on themeFile: one optional
// "<colour>_light" / "<colour>_dark" key per semantic colour, every one of
// them optional. A missing file is not an error. A partial file overrides
// only the keys it names; an unknown key is recorded on the returned
// Theme's Warnings field, never fatal and never printed here. theme.toml is
// applied last, on top of whichever theme was selected, as the per-machine
// overlay it has always been.
func LoadTheme(name string) (Theme, error) {
	isDark, forced := true, false
	reserved := true
	switch name {
	case "", "default":
		// Adaptive: isDark is corrected by WithDark once the terminal's real
		// polarity is known; dark is the reasonable default until then.
	case "dark":
		isDark, forced = true, true
	case "light":
		isDark, forced = false, true
	default:
		reserved = false
	}

	colors := defaultThemeColors()
	var warnings []string

	if !reserved {
		found, fileWarnings, err := loadThemeFile(userThemePath(name), &colors)
		if err != nil {
			return Theme{}, err
		}
		warnings = append(warnings, fileWarnings...)
		if !found {
			warnings = append(warnings, fmt.Sprintf(
				"theme: %q is not available in lw 2; using the default theme", name))
		}
	}

	// theme.toml stays the last word: it is the per-machine overlay a user
	// tweaks a single colour with, whichever theme config.toml selects.
	_, overlayWarnings, err := loadThemeFile(themeOverlayPath(), &colors)
	if err != nil {
		return Theme{}, err
	}
	warnings = append(warnings, overlayWarnings...)

	// A loaded theme starts TrueColor — the palette's own tints. The shell
	// re-resolves the cursor tint through WithProfile once Bubble Tea
	// reports the terminal's real colour profile (contract §3 note 7).
	return buildTheme(colors, isDark, forced, warnings, colorprofile.TrueColor), nil
}

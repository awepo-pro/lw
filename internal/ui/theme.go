// theme.go implements backbone §12's Theme, LoadTheme and Theme.WithDark.
//
// The TOML shape here follows a pattern, not a copy: yorukot/superfile ships
// a flat key = value theme.toml, one file per colour polarity, and a user
// picks the file that matches their terminal. lipgloss v2 has no
// AdaptiveColor (C-81) — colour is resolved by calling a LightDarkFunc at
// Style-build time instead — so this theme.toml stays flat and
// superfile-style but pairs every semantic colour with explicit
// "<name>_light" / "<name>_dark" keys, letting the one theme this subtask
// ships still render correctly on both polarities. See NOTICE for the full
// attribution and the MIT licence text this pattern is credited under.
package ui

import (
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"

	"github.com/awepo-pro/lw/internal/config"
)

// colorPair is one semantic colour's hex value on a light terminal and on a
// dark one. lipgloss.LightDarkFunc (backbone §12, C-81) picks between them
// when a Theme's styles are built.
type colorPair struct {
	Light string
	Dark  string
}

// themeColors is every semantic colour the default theme defines. Field
// names double as the "<name>_light" / "<name>_dark" TOML key prefixes an
// override file names — see themeFile.
type themeColors struct {
	Foreground colorPair
	Background colorPair
	Muted      colorPair
	Faint      colorPair
	Border     colorPair
	Accent     colorPair
	Good       colorPair
	Warn       colorPair
	Bad        colorPair
}

// defaultThemeColors is lw's one compiled-in palette (S6-T4 owns adding more
// themes). The hex values are /demo.html's design so the TUI matches the
// mock the project was designed against.
func defaultThemeColors() themeColors {
	return themeColors{
		Foreground: colorPair{Light: "#14171D", Dark: "#E4E8EE"},
		Background: colorPair{Light: "#F5F6F8", Dark: "#0F1217"},
		Muted:      colorPair{Light: "#5B6675", Dark: "#8E98A7"},
		Faint:      colorPair{Light: "#8A93A1", Dark: "#6B7482"},
		Border:     colorPair{Light: "#DCE1E8", Dark: "#252B34"},
		Accent:     colorPair{Light: "#9C6410", Dark: "#DFA344"},
		Good:       colorPair{Light: "#1C6E48", Dark: "#57C48C"},
		Warn:       colorPair{Light: "#8A6410", Dark: "#D9A63F"},
		Bad:        colorPair{Light: "#A63535", Dark: "#EE7B72"},
	}
}

// themeFile is the on-disk shape of <config.ConfigDir()>/theme.toml. Every
// field is a pointer so a partial file overrides only the keys it names —
// nil means "not present in the file, keep the default".
type themeFile struct {
	ForegroundLight *string `toml:"foreground_light"`
	ForegroundDark  *string `toml:"foreground_dark"`
	BackgroundLight *string `toml:"background_light"`
	BackgroundDark  *string `toml:"background_dark"`
	MutedLight      *string `toml:"muted_light"`
	MutedDark       *string `toml:"muted_dark"`
	FaintLight      *string `toml:"faint_light"`
	FaintDark       *string `toml:"faint_dark"`
	BorderLight     *string `toml:"border_light"`
	BorderDark      *string `toml:"border_dark"`
	AccentLight     *string `toml:"accent_light"`
	AccentDark      *string `toml:"accent_dark"`
	GoodLight       *string `toml:"good_light"`
	GoodDark        *string `toml:"good_dark"`
	WarnLight       *string `toml:"warn_light"`
	WarnDark        *string `toml:"warn_dark"`
	BadLight        *string `toml:"bad_light"`
	BadDark         *string `toml:"bad_dark"`
}

// applyOverrides copies every field f names onto colors, leaving the rest of
// colors untouched.
func (f themeFile) applyOverrides(colors *themeColors) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&colors.Foreground.Light, f.ForegroundLight)
	set(&colors.Foreground.Dark, f.ForegroundDark)
	set(&colors.Background.Light, f.BackgroundLight)
	set(&colors.Background.Dark, f.BackgroundDark)
	set(&colors.Muted.Light, f.MutedLight)
	set(&colors.Muted.Dark, f.MutedDark)
	set(&colors.Faint.Light, f.FaintLight)
	set(&colors.Faint.Dark, f.FaintDark)
	set(&colors.Border.Light, f.BorderLight)
	set(&colors.Border.Dark, f.BorderDark)
	set(&colors.Accent.Light, f.AccentLight)
	set(&colors.Accent.Dark, f.AccentDark)
	set(&colors.Good.Light, f.GoodLight)
	set(&colors.Good.Dark, f.GoodDark)
	set(&colors.Warn.Light, f.WarnLight)
	set(&colors.Warn.Dark, f.WarnDark)
	set(&colors.Bad.Light, f.BadLight)
	set(&colors.Bad.Dark, f.BadDark)
}

// Theme is lw's resolved set of lipgloss v2 styles for one background
// polarity (backbone §12). Build one with LoadTheme, then call WithDark
// whenever the terminal's real polarity becomes known.
type Theme struct {
	// IsDark is the polarity these styles were built for.
	IsDark bool
	// Warnings collects non-fatal problems found while loading theme.toml —
	// an unknown key, or an unrecognised theme name — so a caller can
	// surface them without LoadTheme failing (00-conventions.md §2: no
	// log.Fatal, no printing from a library).
	Warnings []string

	// Base is the default foreground-on-background style: plain body text.
	Base lipgloss.Style
	// Title is bold and accent-coloured: pane titles, the app masthead.
	Title lipgloss.Style
	// Muted is secondary text: help lines, timestamps, provenance.
	Muted lipgloss.Style
	// Faint is the least prominent text: disabled entries, placeholders.
	Faint lipgloss.Style
	// Border carries only a foreground colour; a caller adds Border(...)
	// with the border runes it wants (active vs. inactive pane, etc.).
	Border lipgloss.Style
	// Accent highlights the active element: a focused pane, a cursor.
	Accent lipgloss.Style
	// Good, Warn and Bad colour status: lint pass/warn/fail, diff
	// add/context/delete, journal accepted/pending/rejected.
	Good lipgloss.Style
	Warn lipgloss.Style
	Bad  lipgloss.Style
	// StatusBar reverses fg/bg with the accent colour: footer, title bar.
	StatusBar lipgloss.Style
	// Selected reverses fg/bg with the accent colour for a highlighted row.
	Selected lipgloss.Style

	colors themeColors
}

// WithDark rebuilds t's styles for the given polarity and returns the new
// Theme (backbone §12, C-81). The shell calls this when tea.BackgroundColorMsg
// arrives; Theme itself never queries the terminal or handles messages.
func (t Theme) WithDark(isDark bool) Theme {
	return buildTheme(t.colors, isDark, t.Warnings)
}

// buildTheme resolves colors for isDark and constructs every Theme style.
func buildTheme(colors themeColors, isDark bool, warnings []string) Theme {
	ld := lipgloss.LightDark(isDark)
	resolve := func(p colorPair) color.Color {
		return ld(lipgloss.Color(p.Light), lipgloss.Color(p.Dark))
	}

	fg := resolve(colors.Foreground)
	bg := resolve(colors.Background)
	accent := resolve(colors.Accent)
	base := lipgloss.NewStyle().Foreground(fg).Background(bg)

	return Theme{
		IsDark:    isDark,
		Warnings:  warnings,
		Base:      base,
		Title:     base.Bold(true).Foreground(accent),
		Muted:     base.Foreground(resolve(colors.Muted)),
		Faint:     base.Foreground(resolve(colors.Faint)),
		Border:    lipgloss.NewStyle().Foreground(resolve(colors.Border)),
		Accent:    base.Foreground(accent),
		Good:      base.Foreground(resolve(colors.Good)),
		Warn:      base.Foreground(resolve(colors.Warn)),
		Bad:       base.Foreground(resolve(colors.Bad)),
		StatusBar: lipgloss.NewStyle().Foreground(bg).Background(accent).Bold(true),
		Selected:  lipgloss.NewStyle().Foreground(bg).Background(accent),
		colors:    colors,
	}
}

// LoadTheme returns the named theme, with lw's compiled-in defaults
// overlaid by <config.ConfigDir()>/theme.toml when that file is present
// (backbone §12). A missing file is not an error. A partial file overrides
// only the keys it names; an unknown key is recorded on the returned
// Theme's Warnings field, never fatal and never printed here.
//
// v0.1 ships exactly one compiled-in theme (S6-T4 owns adding more): any
// name other than "" or "default" is itself recorded as a warning and the
// default theme is returned regardless, so an unrecognised
// Config.Theme value degrades to something usable instead of failing
// startup.
func LoadTheme(name string) (Theme, error) {
	colors := defaultThemeColors()
	var warnings []string

	if name != "" && name != "default" {
		warnings = append(warnings, fmt.Sprintf(
			"theme: unknown theme %q, only \"default\" is built in — using default", name))
	}

	path := filepath.Join(config.ConfigDir(), "theme.toml")
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// No overlay file: compiled-in defaults only.
	case err != nil:
		return Theme{}, fmt.Errorf("ui: read %s: %w", path, err)
	default:
		var f themeFile
		meta, decErr := toml.Decode(string(b), &f)
		if decErr != nil {
			return Theme{}, fmt.Errorf("ui: parse %s: %w", path, decErr)
		}
		f.applyOverrides(&colors)
		for _, k := range meta.Undecoded() {
			warnings = append(warnings, fmt.Sprintf("theme.toml: unknown key %q ignored", k.String()))
		}
	}

	// The shell does not know the terminal's real polarity until
	// tea.BackgroundColorMsg arrives (backbone §12, C-81); dark is the
	// reasonable default until then; WithDark corrects it either way.
	return buildTheme(colors, true, warnings), nil
}

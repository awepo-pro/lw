// theme.go implements backbone §12's Theme, LoadTheme and Theme.WithDark,
// including S6-T4's four named built-ins ("default", "dark", "light",
// "nord") and user theme files from <config.ConfigDir()>/themes/.
//
// The TOML shape here follows a pattern, not a copy: yorukot/superfile ships
// a flat key = value theme.toml, one file per colour polarity, and a user
// picks the file that matches their terminal. lipgloss v2 has no
// AdaptiveColor (C-81) — colour is resolved by calling a LightDarkFunc at
// Style-build time instead — so a theme file stays flat and superfile-style
// but pairs every semantic colour with explicit "<name>_light" /
// "<name>_dark" keys, letting any theme render correctly on both
// polarities. See NOTICE for the full attribution and the MIT licence text
// this pattern is credited under.
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

// themeColors is every semantic colour a theme defines. Field names double
// as the "<name>_light" / "<name>_dark" TOML key prefixes a theme file
// names — see themeFile.
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

// defaultThemeColors is lw's compiled-in adaptive palette: one half for a
// light terminal, one for a dark one. The hex values are /demo.html's
// design so the TUI matches the mock the project was designed against.
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

// darkThemeColors is the `dark` built-in: a deep neutral-indigo palette for
// someone who wants a dark UI on a light terminal as much as on a dark one.
func darkThemeColors() themeColors {
	return themeColors{
		Foreground: locked("#E8EBF2"),
		Background: locked("#0B0E14"),
		Muted:      locked("#A3ADC2"),
		Faint:      locked("#5C6577"),
		Border:     locked("#1E2430"),
		Accent:     locked("#7AA2F7"),
		Good:       locked("#9ECE6A"),
		Warn:       locked("#E0AF68"),
		Bad:        locked("#F7768E"),
	}
}

// lightThemeColors is the `light` built-in: a warm white palette locked to
// light rendering, for a terminal whose reported polarity a curator does
// not want to trust.
func lightThemeColors() themeColors {
	return themeColors{
		Foreground: locked("#1B1F27"),
		Background: locked("#FBFBFC"),
		Muted:      locked("#4E5765"),
		Faint:      locked("#8B94A3"),
		Border:     locked("#DDE2E9"),
		Accent:     locked("#1F5FBF"),
		Good:       locked("#1C6E48"),
		Warn:       locked("#8A6410"),
		Bad:        locked("#A63535"),
	}
}

// nordThemeColors is the `nord` built-in, from the documented Nord palette
// (nordtheme.com): polar night for the background, snow storm for text,
// frost for the accent and the aurora colours for status. Nord ships no
// light polarity, so it is locked to its own dark one rather than invented
// into one.
func nordThemeColors() themeColors {
	return themeColors{
		Foreground: locked("#ECEFF4"), // nord6  — snow storm
		Background: locked("#2E3440"), // nord0  — polar night
		Muted:      locked("#D8DEE9"), // nord4  — snow storm
		Faint:      locked("#4C566A"), // nord3  — polar night
		Border:     locked("#3B4252"), // nord1  — polar night
		Accent:     locked("#88C0D0"), // nord8  — frost
		Good:       locked("#A3BE8C"), // nord14 — aurora
		Warn:       locked("#EBCB8B"), // nord13 — aurora
		Bad:        locked("#BF616A"), // nord11 — aurora
	}
}

// locked collapses one hex colour into a pair that renders identically on
// both terminal polarities — how a built-in opts out of LightDark
// resolution (C-81) and says "this theme is this theme, whatever the
// terminal reports".
func locked(hex string) colorPair { return colorPair{Light: hex, Dark: hex} }

// builtInThemeColors returns the compiled-in palette for name, and false
// when name is not one lw ships. The names, in stable order, are
// "default", "dark", "light" and "nord".
func builtInThemeColors(name string) (themeColors, bool) {
	switch name {
	case "default":
		return defaultThemeColors(), true
	case "dark":
		return darkThemeColors(), true
	case "light":
		return lightThemeColors(), true
	case "nord":
		return nordThemeColors(), true
	}
	return themeColors{}, false
}

// themeFile is the on-disk shape of a theme file: either
// <config.ConfigDir()>/theme.toml (the per-machine overlay) or
// <config.ConfigDir()>/themes/<name>.toml (a named user theme). Every
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
	// Warnings collects non-fatal problems found while loading a theme —
	// an unknown key in theme.toml or themes/<name>.toml, or an
	// unrecognised theme name with no file behind it — so a caller can
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

// LoadTheme returns the named theme (backbone §12). name comes from
// config.toml's `theme` key, and "" means "default".
//
// Four names are compiled in: "default" (adaptive — a light half and a dark
// half, resolved by the terminal's reported polarity), "dark", "light" and
// "nord". Any other name is looked up as a user theme file at
// <config.ConfigDir()>/themes/<name>.toml, which lets a vault's curator add
// themes — including ones that shadow a built-in name — without
// recompiling lw.
//
// A theme file is the same flat TOML shape <ConfigDir()>/theme.toml has
// always used: one optional "<colour>_light" / "<colour>_dark" key per
// semantic colour (foreground, background, muted, faint, border, accent,
// good, warn, bad), every one of them optional. A user theme file starts
// from the built-in whose name it shares, or from the default palette when
// the name is a new one, and overrides only the keys it names — so
// themes/nord.toml is one tweaked key, not eighteen. theme.toml is applied
// last, on top of whichever theme was selected, as the per-machine overlay
// it has always been.
//
// A missing file is not an error. A partial file overrides only the keys it
// names; an unknown key is recorded on the returned Theme's Warnings field,
// never fatal and never printed here. An unrecognised name with no file
// behind it is a warning too, and the default theme comes back — an
// unrecognised Config.Theme value degrades to something usable instead of
// failing startup.
func LoadTheme(name string) (Theme, error) {
	if name == "" {
		name = "default"
	}
	// Resolution order, most specific first: a user theme file is applied on
	// top of the built-in of the same name when there is one, and on top of
	// the default palette when there is not — so themes/<name>.toml both
	// adds new themes and overrides shipped ones, key by key. A name with
	// neither a file nor a built-in behind it is a warning, not an error.
	colors, isBuiltIn := builtInThemeColors(name)
	if !isBuiltIn {
		colors = defaultThemeColors()
	}
	var warnings []string

	found, fileWarnings, err := loadThemeFile(userThemePath(name), &colors)
	if err != nil {
		return Theme{}, err
	}
	warnings = append(warnings, fileWarnings...)
	if !found && !isBuiltIn {
		warnings = append(warnings, fmt.Sprintf(
			"theme: unknown theme %q, and no themes/%s.toml — using default "+
				"(built in: default, dark, light, nord)", name, name))
	}

	// theme.toml stays the last word: it is the per-machine overlay a user
	// tweaks a single colour with, whichever theme config.toml selects.
	_, overlayWarnings, err := loadThemeFile(filepath.Join(config.ConfigDir(), "theme.toml"), &colors)
	if err != nil {
		return Theme{}, err
	}
	warnings = append(warnings, overlayWarnings...)

	// The shell does not know the terminal's real polarity until
	// tea.BackgroundColorMsg arrives (backbone §12, C-81); dark is the
	// reasonable default until then; WithDark corrects it either way. A
	// locked theme is unaffected: both halves of every pair are the same
	// colour.
	return buildTheme(colors, true, warnings), nil
}

// loadThemeFile reads and applies one theme file onto colors, if path names
// a file that exists (an empty path, or a missing file, applies nothing and
// is not an error). It reports whether a file was applied, plus the warnings
// reading it produced; an existing file that cannot be read or parsed is an
// error naming that file, because a theme a curator wrote and got wrong is
// something they have to be told about, not something to paper over.
func loadThemeFile(path string, colors *themeColors) (applied bool, warnings []string, err error) {
	if path == "" {
		return false, nil, nil
	}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil, nil
	case err != nil:
		return false, nil, fmt.Errorf("ui: read %s: %w", path, err)
	}
	var f themeFile
	meta, decErr := toml.Decode(string(b), &f)
	if decErr != nil {
		return false, nil, fmt.Errorf("ui: parse %s: %w", path, decErr)
	}
	f.applyOverrides(colors)
	for _, k := range meta.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("%s: unknown key %q ignored",
			filepath.Base(path), k.String()))
	}
	return true, warnings, nil
}

// userThemePath returns the file that defines name, if that name can be a
// user theme: <config.ConfigDir()>/themes/<name>.toml. A name that is not a
// single safe path element — one that would climb out of the themes
// directory with "..", or hide a separator — is refused by returning "",
// rather than looked up, so a config.toml theme value can never make lw
// read a theme file from outside its own config directory.
func userThemePath(name string) string {
	if name == "" || name != filepath.Base(filepath.Clean(name)) {
		return ""
	}
	return filepath.Join(config.ConfigDir(), "themes", name+".toml")
}

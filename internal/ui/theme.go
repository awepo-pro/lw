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
	}
}

// themeFile is the on-disk shape of a theme file: either
// <config.ConfigDir()>/theme.toml (the per-machine overlay) or
// <config.ConfigDir()>/themes/<name>.toml (a named user theme). Every field
// is a pointer so a partial file overrides only the keys it names — nil
// means "not present in the file, keep what came before".
type themeFile struct {
	FgLight *string `toml:"fg_light"`
	FgDark  *string `toml:"fg_dark"`
	// ForegroundLight/Dark is the pre-lw-2 key name; it still sets Fg
	// (contract §3 note 3), applied before the new fg_* keys so fg_* wins
	// when a file names both.
	ForegroundLight *string `toml:"foreground_light"`
	ForegroundDark  *string `toml:"foreground_dark"`
	// BackgroundLight/Dark is accepted so a pre-lw-2 file still parses, but
	// it is never applied: lw 2 has no background token (contract §3 note 3).
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
	CursorLight     *string `toml:"cursor_light"`
	CursorDark      *string `toml:"cursor_dark"`
	GoodLight       *string `toml:"good_light"`
	GoodDark        *string `toml:"good_dark"`
	WarnLight       *string `toml:"warn_light"`
	WarnDark        *string `toml:"warn_dark"`
	BadLight        *string `toml:"bad_light"`
	BadDark         *string `toml:"bad_dark"`
}

// applyOverrides copies every field f names onto colors, leaving the rest of
// colors untouched. Background keys are handled separately (backgroundWarnings)
// since they are accepted but never applied.
func (f themeFile) applyOverrides(colors *themeColors) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&colors.Fg.Light, f.ForegroundLight)
	set(&colors.Fg.Dark, f.ForegroundDark)
	set(&colors.Fg.Light, f.FgLight)
	set(&colors.Fg.Dark, f.FgDark)
	set(&colors.Muted.Light, f.MutedLight)
	set(&colors.Muted.Dark, f.MutedDark)
	set(&colors.Faint.Light, f.FaintLight)
	set(&colors.Faint.Dark, f.FaintDark)
	set(&colors.Border.Light, f.BorderLight)
	set(&colors.Border.Dark, f.BorderDark)
	set(&colors.Accent.Light, f.AccentLight)
	set(&colors.Accent.Dark, f.AccentDark)
	set(&colors.CursorBg.Light, f.CursorLight)
	set(&colors.CursorBg.Dark, f.CursorDark)
	set(&colors.Good.Light, f.GoodLight)
	set(&colors.Good.Dark, f.GoodDark)
	set(&colors.Warn.Light, f.WarnLight)
	set(&colors.Warn.Dark, f.WarnDark)
	set(&colors.Bad.Light, f.BadLight)
	set(&colors.Bad.Dark, f.BadDark)
}

// backgroundWarnings reports one warning per background_light/background_dark
// key f's file actually set (contract §3 note 3): lw 2 has no background
// token, so the key is accepted for compatibility and always ignored.
func (f themeFile) backgroundWarnings(file string) []string {
	var warnings []string
	if f.BackgroundLight != nil {
		warnings = append(warnings, fmt.Sprintf("%s: %q is ignored: lw 2 uses the terminal's background", file, "background_light"))
	}
	if f.BackgroundDark != nil {
		warnings = append(warnings, fmt.Sprintf("%s: %q is ignored: lw 2 uses the terminal's background", file, "background_dark"))
	}
	return warnings
}

// Palette is one polarity's nine resolved tokens, as "#RRGGBB" (contract
// §3). It is what markdown.Style is built from — the two structs share field
// names on purpose (01-contract.md §3).
type Palette struct {
	Fg, Muted, Faint, Border, Accent, CursorBg, Good, Warn, Bad string
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
	// Bold is Fg with bold set: the vault name, unfocused panel titles, and
	// the keys the footer prints.
	Bold lipgloss.Style
	// CursorBg is the cursor-row tint (plan §4.4) — the one place a
	// background colour appears at all. It is a color.Color, not a style: a
	// caller tints exactly the cursor row with it (ui.Panel's CursorRow).
	CursorBg color.Color
	// Palette is what markdown.Style is built from.
	Palette Palette

	colors themeColors
	forced bool // polarity forced by theme = "dark"/"light"; WithDark is then a no-op
}

// WithDark rebuilds t's styles for the given polarity and returns the new
// Theme, unless t's polarity was forced by name ("dark"/"light"), in which
// case it is a no-op (contract §3).
func (t Theme) WithDark(isDark bool) Theme {
	if t.forced {
		return t
	}
	return buildTheme(t.colors, isDark, t.forced, t.Warnings)
}

// buildTheme resolves colors for isDark and constructs every Theme style.
func buildTheme(colors themeColors, isDark, forced bool, warnings []string) Theme {
	ld := lipgloss.LightDark(isDark)
	resolve := func(p colorPair) color.Color {
		return ld(lipgloss.Color(p.Light), lipgloss.Color(p.Dark))
	}

	cursorBg := resolve(colors.CursorBg)

	fg := lipgloss.NewStyle().Foreground(resolve(colors.Fg))
	muted := lipgloss.NewStyle().Foreground(resolve(colors.Muted))
	faint := lipgloss.NewStyle().Foreground(resolve(colors.Faint))
	border := lipgloss.NewStyle().Foreground(resolve(colors.Border))
	accent := lipgloss.NewStyle().Foreground(resolve(colors.Accent))
	good := lipgloss.NewStyle().Foreground(resolve(colors.Good))
	warn := lipgloss.NewStyle().Foreground(resolve(colors.Warn))
	bad := lipgloss.NewStyle().Foreground(resolve(colors.Bad))
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
		Bold:     bold,
		CursorBg: cursorBg,
		Palette:  paletteFrom(colors, isDark),

		colors: colors,
		forced: forced,
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
	_, overlayWarnings, err := loadThemeFile(filepath.Join(config.ConfigDir(), "theme.toml"), &colors)
	if err != nil {
		return Theme{}, err
	}
	warnings = append(warnings, overlayWarnings...)

	return buildTheme(colors, isDark, forced, warnings), nil
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
	warnings = append(warnings, f.backgroundWarnings(filepath.Base(path))...)
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

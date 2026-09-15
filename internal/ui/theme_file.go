// theme_file.go holds LoadTheme's on-disk side (contract §3 notes 2-3 and
// 5): the themeFile shape, its overrides onto the compiled-in palette, and
// the path rules a theme name is looked up by. Split from theme.go, which
// keeps the in-memory Theme, Palette and the profile-aware cursor tint.
package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/awepo-pro/lw/internal/config"
)

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
	HeadingLight    *string `toml:"heading_light"`
	HeadingDark     *string `toml:"heading_dark"`
	CodeLight       *string `toml:"code_light"`
	CodeDark        *string `toml:"code_dark"`
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
	set(&colors.Heading.Light, f.HeadingLight)
	set(&colors.Heading.Dark, f.HeadingDark)
	set(&colors.Code.Light, f.CodeLight)
	set(&colors.Code.Dark, f.CodeDark)
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

// themeOverlayPath is the per-machine overlay's path:
// <config.ConfigDir()>/theme.toml.
func themeOverlayPath() string {
	return filepath.Join(config.ConfigDir(), "theme.toml")
}

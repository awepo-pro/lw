// theme_file_test.go covers LoadTheme's file behaviour (contract §3
// notes 2 and 5): a user themes/<name>.toml loads on top of the default
// palette, theme.toml stays the per-machine overlay applied last, a file
// that does not parse fails naming the file, and a theme name that is a
// path never reads outside the config directory. The compiled-in palette,
// the reserved names and the file *keys* are theme_test.go's subject; the
// setConfigDir/writeConfigFile helpers it defines are shared.
package ui

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadThemeMissingConfigDirEntirelyIsNotAnError: a config dir that does
// not exist at all is not an error, and produces no warning.
func TestLoadThemeMissingConfigDirEntirelyIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "does-not-exist"))

	th, err := LoadTheme("default")
	if err != nil {
		t.Fatalf("LoadTheme(\"default\") error = %v, want nil for a missing config dir", err)
	}
	if len(th.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", th.Warnings)
	}
}

// TestLoadThemeUserThemeFile: a themes/<name>.toml file loads on top of the
// default palette (contract §3 note 2 — it no longer shadows a built-in,
// since there is only the one compiled-in palette left).
func TestLoadThemeUserThemeFile(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, filepath.Join(configDir, "themes"), "solarized.toml", `
accent_light = "#268bd2"
accent_dark = "#2aa198"
`)

	th, err := LoadTheme("solarized")
	if err != nil {
		t.Fatalf("LoadTheme(\"solarized\") error = %v", err)
	}
	if len(th.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none for a well-formed user theme", th.Warnings)
	}
	if got, want := th.WithDark(true).Palette.Accent, "#2aa198"; got != want {
		t.Fatalf("dark Accent = %q, want the file's value %q", got, want)
	}
	def := paletteFrom(defaultThemeColors(), true)
	if got := th.WithDark(true).Palette.Muted; got != def.Muted {
		t.Fatalf("Muted = %q, want the default %q (the file names accent only)", got, def.Muted)
	}
}

// TestLoadThemeInvalidUserFileNamesTheFile: a theme file that does not
// parse must fail with an error naming the file.
func TestLoadThemeInvalidUserFileNamesTheFile(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, filepath.Join(configDir, "themes"), "broken.toml", `
accent_light = [
`)

	_, err := LoadTheme("broken")
	if err == nil {
		t.Fatal("LoadTheme(\"broken\") error = nil, want a parse error")
	}
	if !strings.Contains(err.Error(), "broken.toml") {
		t.Fatalf("error = %q, want it to name the offending file", err)
	}
}

// TestLoadThemeInvalidOverlayNamesTheFile is the same guarantee for the
// theme.toml overlay.
func TestLoadThemeInvalidOverlayNamesTheFile(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, configDir, "theme.toml", `not [valid toml`)

	_, err := LoadTheme("dark")
	if err == nil {
		t.Fatal("LoadTheme(\"dark\") error = nil, want a parse error for theme.toml")
	}
	if !strings.Contains(err.Error(), "theme.toml") {
		t.Fatalf("error = %q, want it to name theme.toml", err)
	}
}

// TestLoadThemeOverlayAppliesAfterSelectedTheme: theme.toml stays the last
// word, applied on top of whichever theme was selected (contract §3 note 5).
func TestLoadThemeOverlayAppliesAfterSelectedTheme(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, filepath.Join(configDir, "themes"), "mine.toml", `
accent_dark = "#111111"
`)
	writeConfigFile(t, configDir, "theme.toml", `
accent_dark = "#222222"
`)

	th, err := LoadTheme("mine")
	if err != nil {
		t.Fatalf("LoadTheme(\"mine\") error = %v", err)
	}
	if got, want := th.WithDark(true).Palette.Accent, "#222222"; got != want {
		t.Fatalf("dark Accent = %q, want theme.toml's overriding value %q", got, want)
	}
}

// TestLoadThemeTraversalNameNeverReadsOutsideTheConfigDir: a theme name is
// user-controlled config input, so one carrying a path separator must not
// turn into a read of an arbitrary file.
func TestLoadThemeTraversalNameNeverReadsOutsideTheConfigDir(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, configDir, "escape.toml", `
accent_dark = "#ff00ff"
`)

	th, err := LoadTheme("../escape")
	if err != nil {
		t.Fatalf("LoadTheme(\"../escape\") error = %v", err)
	}
	if len(th.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one (unknown theme)", th.Warnings)
	}
	def := paletteFrom(defaultThemeColors(), true)
	if got := th.WithDark(true).Palette; got != def {
		t.Fatalf("palette = %+v, want the default %+v (the escaped file must not be read)", got, def)
	}
}

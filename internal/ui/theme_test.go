package ui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// setConfigDir points config.ConfigDir() at a fresh, empty temp directory
// for the duration of the test.
func setConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "lw")
}

func writeConfigFile(t *testing.T, configDir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", configDir, err)
	}
	path := filepath.Join(configDir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestThemePalette asserts contract §3 note 1's 18 hex values: the one
// compiled-in palette, both polarities.
func TestThemePalette(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme(\"\") error = %v", err)
	}

	wantDark := Palette{
		Fg: "#D8DDE4", Muted: "#8C95A2", Faint: "#5E6672", Border: "#353C47",
		Accent: "#7AB2F2", CursorBg: "#1A2331", Good: "#6BC28E", Warn: "#E2B45A", Bad: "#EF7F76",
	}
	if got := th.WithDark(true).Palette; got != wantDark {
		t.Fatalf("dark palette = %+v, want %+v", got, wantDark)
	}

	wantLight := Palette{
		Fg: "#1C2128", Muted: "#586270", Faint: "#8A929E", Border: "#C6CDD6",
		Accent: "#1D62C2", CursorBg: "#E6EEF9", Good: "#1D7A4B", Warn: "#93660A", Bad: "#B03A33",
	}
	if got := th.WithDark(false).Palette; got != wantLight {
		t.Fatalf("light palette = %+v, want %+v", got, wantLight)
	}
}

// TestThemeNames covers contract §3 note 2: "" and "default" are adaptive
// with no warning; "dark"/"light" force their polarity, so WithDark becomes
// a no-op; any other name with no themes/<name>.toml file (here, "nord",
// which no longer names a compiled palette) warns with the exact message
// and falls back to the default theme.
func TestThemeNames(t *testing.T) {
	t.Run("empty_and_default_are_adaptive", func(t *testing.T) {
		for _, name := range []string{"", "default"} {
			setConfigDir(t)
			th, err := LoadTheme(name)
			if err != nil {
				t.Fatalf("LoadTheme(%q) error = %v", name, err)
			}
			if len(th.Warnings) != 0 {
				t.Fatalf("LoadTheme(%q).Warnings = %v, want none", name, th.Warnings)
			}
			if got := th.WithDark(true).Accent.Render("x"); got == th.WithDark(false).Accent.Render("x") {
				t.Fatalf("LoadTheme(%q) renders identically at both polarities; want adaptive", name)
			}
		}
	})

	t.Run("dark_stays_dark_after_withdark_false", func(t *testing.T) {
		setConfigDir(t)
		th, err := LoadTheme("dark")
		if err != nil {
			t.Fatalf("LoadTheme(\"dark\") error = %v", err)
		}
		if len(th.Warnings) != 0 {
			t.Fatalf("Warnings = %v, want none", th.Warnings)
		}
		if !th.IsDark {
			t.Fatal(`LoadTheme("dark").IsDark = false`)
		}
		if got := th.WithDark(false); !got.IsDark {
			t.Fatal(`WithDark(false).IsDark = false, want true (forced)`)
		}
	})

	t.Run("light_stays_light_after_withdark_true", func(t *testing.T) {
		setConfigDir(t)
		th, err := LoadTheme("light")
		if err != nil {
			t.Fatalf("LoadTheme(\"light\") error = %v", err)
		}
		if len(th.Warnings) != 0 {
			t.Fatalf("Warnings = %v, want none", th.Warnings)
		}
		if th.IsDark {
			t.Fatal(`LoadTheme("light").IsDark = true`)
		}
		if got := th.WithDark(true); got.IsDark {
			t.Fatal(`WithDark(true).IsDark = true, want false (forced)`)
		}
	})

	t.Run("nord_warns_and_falls_back_to_default", func(t *testing.T) {
		setConfigDir(t)
		th, err := LoadTheme("nord")
		if err != nil {
			t.Fatalf("LoadTheme(\"nord\") error = %v", err)
		}
		want := []string{`theme: "nord" is not available in lw 2; using the default theme`}
		if len(th.Warnings) != 1 || th.Warnings[0] != want[0] {
			t.Fatalf("Warnings = %v, want %v", th.Warnings, want)
		}
		def, err := LoadTheme("default")
		if err != nil {
			t.Fatalf("LoadTheme(\"default\") error = %v", err)
		}
		if got, want := th.WithDark(true).Palette, def.WithDark(true).Palette; got != want {
			t.Fatalf("nord palette = %+v, want the default palette %+v", got, want)
		}
	})
}

// TestThemeFileKeys covers contract §3 note 3: the fg_*/muted_*/faint_*/
// border_*/accent_*/cursor_*/good_*/warn_*/bad_* keys apply; the old
// foreground_* key still sets Fg; background_* is accepted and ignored,
// with the exact warning; and an unrecognised key still warns.
func TestThemeFileKeys(t *testing.T) {
	t.Run("new_keys_apply", func(t *testing.T) {
		configDir := setConfigDir(t)
		writeConfigFile(t, configDir, "theme.toml", `
fg_light = "#111111"
fg_dark = "#222222"
muted_light = "#333333"
muted_dark = "#444444"
faint_light = "#555555"
faint_dark = "#666666"
border_light = "#777777"
border_dark = "#888888"
accent_light = "#999999"
accent_dark = "#aaaaaa"
cursor_light = "#bbbbbb"
cursor_dark = "#cccccc"
good_light = "#dddddd"
good_dark = "#eeeeee"
warn_light = "#f0f0f0"
warn_dark = "#f1f1f1"
bad_light = "#f2f2f2"
bad_dark = "#f3f3f3"
`)
		th, err := LoadTheme("")
		if err != nil {
			t.Fatalf("LoadTheme error = %v", err)
		}
		if len(th.Warnings) != 0 {
			t.Fatalf("Warnings = %v, want none", th.Warnings)
		}
		want := Palette{
			Fg: "#222222", Muted: "#444444", Faint: "#666666", Border: "#888888",
			Accent: "#aaaaaa", CursorBg: "#cccccc", Good: "#eeeeee", Warn: "#f1f1f1", Bad: "#f3f3f3",
		}
		if got := th.WithDark(true).Palette; got != want {
			t.Fatalf("dark palette = %+v, want %+v", got, want)
		}
	})

	t.Run("legacy_foreground_maps_to_fg", func(t *testing.T) {
		configDir := setConfigDir(t)
		writeConfigFile(t, configDir, "theme.toml", `
foreground_light = "#123456"
foreground_dark = "#654321"
`)
		th, err := LoadTheme("")
		if err != nil {
			t.Fatalf("LoadTheme error = %v", err)
		}
		if len(th.Warnings) != 0 {
			t.Fatalf("Warnings = %v, want none", th.Warnings)
		}
		if got, want := th.WithDark(false).Palette.Fg, "#123456"; got != want {
			t.Fatalf("light Fg = %q, want %q", got, want)
		}
		if got, want := th.WithDark(true).Palette.Fg, "#654321"; got != want {
			t.Fatalf("dark Fg = %q, want %q", got, want)
		}
	})

	t.Run("fg_wins_over_legacy_foreground_when_both_set", func(t *testing.T) {
		configDir := setConfigDir(t)
		writeConfigFile(t, configDir, "theme.toml", `
foreground_dark = "#000001"
fg_dark = "#000002"
`)
		th, err := LoadTheme("")
		if err != nil {
			t.Fatalf("LoadTheme error = %v", err)
		}
		if got, want := th.WithDark(true).Palette.Fg, "#000002"; got != want {
			t.Fatalf("dark Fg = %q, want the fg_dark value %q", got, want)
		}
	})

	t.Run("background_ignored_with_warning", func(t *testing.T) {
		configDir := setConfigDir(t)
		writeConfigFile(t, configDir, "theme.toml", `
background_light = "#000000"
background_dark = "#ffffff"
`)
		th, err := LoadTheme("")
		if err != nil {
			t.Fatalf("LoadTheme error = %v", err)
		}
		if len(th.Warnings) != 2 {
			t.Fatalf("Warnings = %v, want exactly two", th.Warnings)
		}
		for _, key := range []string{"background_light", "background_dark"} {
			found := false
			for _, w := range th.Warnings {
				if strings.Contains(w, `"`+key+`"`) && strings.Contains(w, "lw 2 uses the terminal's background") {
					found = true
				}
			}
			if !found {
				t.Fatalf("Warnings = %v, missing the ignored-key warning for %q", th.Warnings, key)
			}
		}
		def, err := LoadTheme("default")
		if err != nil {
			t.Fatalf("LoadTheme error = %v", err)
		}
		if got, want := th.WithDark(true).Palette.CursorBg, def.WithDark(true).Palette.CursorBg; got != want {
			t.Fatalf("CursorBg = %q, want the default %q (background_* must not apply)", got, want)
		}
	})

	t.Run("unknown_key_warns", func(t *testing.T) {
		configDir := setConfigDir(t)
		writeConfigFile(t, configDir, "theme.toml", `
totally_bogus_key = "nope"
`)
		th, err := LoadTheme("")
		if err != nil {
			t.Fatalf("LoadTheme error = %v", err)
		}
		if len(th.Warnings) != 1 || !strings.Contains(th.Warnings[0], "totally_bogus_key") {
			t.Fatalf("Warnings = %v, want one row naming the unknown key", th.Warnings)
		}
	})

	t.Run("partial_override_only_changes_named_keys", func(t *testing.T) {
		configDir := setConfigDir(t)
		writeConfigFile(t, configDir, "theme.toml", `
accent_light = "#ff0000"
accent_dark = "#00ff00"
`)
		th, err := LoadTheme("")
		if err != nil {
			t.Fatalf("LoadTheme error = %v", err)
		}
		if len(th.Warnings) != 0 {
			t.Fatalf("Warnings = %v, want none", th.Warnings)
		}
		if got, want := th.WithDark(true).Palette.Accent, "#00ff00"; got != want {
			t.Fatalf("dark Accent = %q, want %q", got, want)
		}
		want := paletteFrom(defaultThemeColors(), true)
		if got := th.WithDark(true).Palette.Muted; got != want.Muted {
			t.Fatalf("Muted = %q, want the default %q (unrelated key changed)", got, want.Muted)
		}
	})
}

// TestThemeNoBackground adds the check contract §3 note 6 asks for: no
// Theme style but Selected ever sets a background (F4).
func TestThemeNoBackground(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme error = %v", err)
	}
	th = th.WithDark(true)

	styles := map[string]interface{ Render(...string) string }{
		"Fg": th.Fg, "Muted": th.Muted, "Faint": th.Faint, "Border": th.Border,
		"Accent": th.Accent, "Good": th.Good, "Warn": th.Warn, "Bad": th.Bad,
		"Bold": th.Bold, "Base": th.Base, "Title": th.Title, "StatusBar": th.StatusBar,
	}
	names := make([]string, 0, len(styles))
	for name := range styles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if out := styles[name].Render("x"); strings.Contains(out, "48;") {
			t.Errorf("%s.Render(x) = %q, carries a background SGR code", name, out)
		}
	}
	if out := th.Selected.Render("x"); !strings.Contains(out, "48;") {
		t.Fatalf("Selected.Render(x) = %q, has no background; the cursor row would have no tint", out)
	}
}

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

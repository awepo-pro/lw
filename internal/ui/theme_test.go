package ui

import (
	"os"
	"path/filepath"
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

func TestLoadThemeDefaultsWithNoFilePresent(t *testing.T) {
	setConfigDir(t)

	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme(\"\") error = %v", err)
	}
	if len(th.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", th.Warnings)
	}

	want := buildTheme(defaultThemeColors(), true, nil)
	if got, want := th.Accent.Render("x"), want.Accent.Render("x"); got != want {
		t.Fatalf("Accent.Render(x) = %q, want %q (defaults not applied)", got, want)
	}
}

func TestLoadThemePartialOverrideOnlyChangesNamedKeys(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, configDir, "theme.toml", `
accent_light = "#ff0000"
accent_dark = "#00ff00"
`)

	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme(\"\") error = %v", err)
	}
	if len(th.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", th.Warnings)
	}

	// The named key changed...
	wantColors := defaultThemeColors()
	wantColors.Accent = colorPair{Light: "#ff0000", Dark: "#00ff00"}
	wantOverridden := buildTheme(wantColors, true, nil)
	if got, want := th.Accent.Render("x"), wantOverridden.Accent.Render("x"); got != want {
		t.Fatalf("Accent.Render(x) = %q, want %q (override not applied)", got, want)
	}

	// ...and everything else — e.g. Muted — did not.
	wantDefault := buildTheme(defaultThemeColors(), true, nil)
	if got, want := th.Muted.Render("x"), wantDefault.Muted.Render("x"); got != want {
		t.Fatalf("Muted.Render(x) = %q, want %q (unrelated key was changed)", got, want)
	}
}

func TestLoadThemeUnknownKeyWarnsButStillUsable(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, configDir, "theme.toml", `
accent_light = "#ff0000"
totally_bogus_key = "nope"
`)

	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme(\"\") error = %v", err)
	}
	if len(th.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one", th.Warnings)
	}
	if !strings.Contains(th.Warnings[0], "totally_bogus_key") {
		t.Fatalf("Warnings[0] = %q, want it to name the unknown key", th.Warnings[0])
	}

	// The theme is still usable: the named override took effect and the
	// style produces non-empty output.
	if th.Accent.Render("x") == "" {
		t.Fatal("Accent.Render(x) is empty; theme was not usable after a warning")
	}
}

func TestLoadThemeUnknownNameWarnsAndFallsBackToDefault(t *testing.T) {
	setConfigDir(t)

	th, err := LoadTheme("nonexistent-theme")
	if err != nil {
		t.Fatalf("LoadTheme(\"nonexistent-theme\") error = %v", err)
	}
	if len(th.Warnings) != 1 || !strings.Contains(th.Warnings[0], "nonexistent-theme") {
		t.Fatalf("Warnings = %v, want one row naming the unknown theme", th.Warnings)
	}

	want := buildTheme(defaultThemeColors(), true, th.Warnings)
	if got, want := th.Accent.Render("x"), want.Accent.Render("x"); got != want {
		t.Fatalf("Accent.Render(x) = %q, want the default theme's rendering", got)
	}
}

func TestThemeWithDarkRendersDifferently(t *testing.T) {
	setConfigDir(t)

	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme(\"\") error = %v", err)
	}

	light := th.WithDark(false)
	dark := th.WithDark(true)

	lightOut := light.Accent.Render("hello")
	darkOut := dark.Accent.Render("hello")

	if lightOut == "" || darkOut == "" {
		t.Fatalf("rendered output empty: light=%q dark=%q", lightOut, darkOut)
	}
	if lightOut == darkOut {
		t.Fatalf("WithDark(false) and WithDark(true) rendered identically: %q", lightOut)
	}
	if !light.IsDark && dark.IsDark {
		// sanity: polarity flags reflect what was asked for.
		return
	}
	if light.IsDark {
		t.Fatal("WithDark(false).IsDark = true")
	}
	if !dark.IsDark {
		t.Fatal("WithDark(true).IsDark = false")
	}
}

func TestLoadThemeMissingConfigDirEntirelyIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	// Point at a config dir that does not exist at all (not even created).
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "does-not-exist"))

	th, err := LoadTheme("default")
	if err != nil {
		t.Fatalf("LoadTheme(\"default\") error = %v, want nil for a missing config dir", err)
	}
	if len(th.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", th.Warnings)
	}
}

// themeSamples renders every style a Theme exposes, in both polarities and
// in a fixed order, as name/rendered pairs — a fingerprint two themes can
// be compared on without ranging a map.
func themeSamples(th Theme) [][2]string {
	pairs := []struct {
		name  string
		style func(Theme) string
	}{
		{"base", func(t Theme) string { return t.Base.Render("x") }},
		{"title", func(t Theme) string { return t.Title.Render("x") }},
		{"muted", func(t Theme) string { return t.Muted.Render("x") }},
		{"faint", func(t Theme) string { return t.Faint.Render("x") }},
		{"border", func(t Theme) string { return t.Border.Render("x") }},
		{"accent", func(t Theme) string { return t.Accent.Render("x") }},
		{"good", func(t Theme) string { return t.Good.Render("x") }},
		{"warn", func(t Theme) string { return t.Warn.Render("x") }},
		{"bad", func(t Theme) string { return t.Bad.Render("x") }},
		{"status", func(t Theme) string { return t.StatusBar.Render("x") }},
		{"selected", func(t Theme) string { return t.Selected.Render("x") }},
	}

	var out [][2]string
	for _, polarity := range []bool{false, true} {
		p := th.WithDark(polarity)
		for _, pair := range pairs {
			out = append(out, [2]string{pair.name, pair.style(p)})
		}
	}
	return out
}

// themeSignature is themeSamples joined into one string.
func themeSignature(t *testing.T, th Theme) string {
	t.Helper()
	var b strings.Builder
	for _, p := range themeSamples(th) {
		b.WriteString(p[0])
		b.WriteString("=")
		b.WriteString(p[1])
		b.WriteString("\n")
	}
	return b.String()
}

// TestLoadThemeBuiltIns covers S6-T4's four named themes: each loads from
// just its name, with no warning and no error, every style renders
// non-empty at both polarities, and the polarity flag follows what was
// asked for. A theme that fails any of this is not "selectable from
// config" — it is a startup error or an illegible screen waiting to happen.
func TestLoadThemeBuiltIns(t *testing.T) {
	setConfigDir(t)

	for _, name := range []string{"default", "dark", "light", "nord"} {
		t.Run(name, func(t *testing.T) {
			th, err := LoadTheme(name)
			if err != nil {
				t.Fatalf("LoadTheme(%q) error = %v", name, err)
			}
			if len(th.Warnings) != 0 {
				t.Fatalf("Warnings = %v, want none for a built-in theme", th.Warnings)
			}
			for _, polarity := range []bool{false, true} {
				p := th.WithDark(polarity)
				if p.IsDark != polarity {
					t.Errorf("WithDark(%v).IsDark = %v", polarity, p.IsDark)
				}
				for style, out := range map[string]string{
					"Base":      p.Base.Render("x"),
					"Title":     p.Title.Render("x"),
					"Muted":     p.Muted.Render("x"),
					"Faint":     p.Faint.Render("x"),
					"Border":    p.Border.Render("x"),
					"Accent":    p.Accent.Render("x"),
					"Good":      p.Good.Render("x"),
					"Warn":      p.Warn.Render("x"),
					"Bad":       p.Bad.Render("x"),
					"StatusBar": p.StatusBar.Render("x"),
					"Selected":  p.Selected.Render("x"),
				} {
					if out == "" {
						t.Errorf("WithDark(%v).%s.Render(x) is empty", polarity, style)
					}
				}
			}
		})
	}
}

// TestBuiltInThemesAreDistinct pins the point of having four of them: no
// two built-ins may resolve to the same palette, which is what a copy-paste
// palette edit would silently produce.
func TestBuiltInThemesAreDistinct(t *testing.T) {
	setConfigDir(t)

	sigs := map[string]string{}
	for _, name := range []string{"default", "dark", "light", "nord"} {
		th, err := LoadTheme(name)
		if err != nil {
			t.Fatalf("LoadTheme(%q) error = %v", name, err)
		}
		sigs[name] = themeSignature(t, th)
	}

	names := []string{"default", "dark", "light", "nord"}
	for i, a := range names {
		for _, b := range names[i+1:] {
			if sigs[a] == sigs[b] {
				t.Errorf("themes %q and %q rendered identically", a, b)
			}
		}
	}
}

// TestBuiltInNonDefaultThemesAreLocked pins what "dark"/"light"/"nord" mean:
// a curator who asks for one of them gets exactly those colours whether the
// terminal reports dark or light, unlike "default", which adapts.
func TestBuiltInNonDefaultThemesAreLocked(t *testing.T) {
	setConfigDir(t)

	for _, name := range []string{"dark", "light", "nord"} {
		t.Run(name, func(t *testing.T) {
			th, err := LoadTheme(name)
			if err != nil {
				t.Fatalf("LoadTheme(%q) error = %v", name, err)
			}
			if got := th.WithDark(true).Accent.Render("x"); got != th.WithDark(false).Accent.Render("x") {
				t.Fatalf("%s renders differently per polarity; want it locked", name)
			}
		})
	}

	def, err := LoadTheme("default")
	if err != nil {
		t.Fatalf("LoadTheme(\"default\") error = %v", err)
	}
	if def.WithDark(true).Accent.Render("x") == def.WithDark(false).Accent.Render("x") {
		t.Fatal("default renders identically per polarity; want it adaptive")
	}
}

// TestLoadThemeUserThemeFile covers the second half of S6-T4: a theme a
// curator drops into <config>/themes/<name>.toml loads by name, overrides
// only the keys it names, and raises no warning.
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

	wantColors := defaultThemeColors()
	wantColors.Accent = colorPair{Light: "#268bd2", Dark: "#2aa198"}
	want := buildTheme(wantColors, true, nil)
	if got, want := th.Accent.Render("x"), want.Accent.Render("x"); got != want {
		t.Fatalf("Accent.Render(x) = %q, want the file's accent", got)
	}
	// Everything the file did not name stayed at the default palette's value.
	if got, want := th.Muted.Render("x"), buildTheme(defaultThemeColors(), true, nil).Muted.Render("x"); got != want {
		t.Fatalf("Muted.Render(x) = %q, want the default (the file names accent only)", got)
	}
}

// TestLoadThemeUserFileShadowsBuiltIn: a user theme file named after a
// built-in wins, which is how a curator tweaks a shipped theme without
// recompiling lw.
func TestLoadThemeUserFileShadowsBuiltIn(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, filepath.Join(configDir, "themes"), "nord.toml", `
accent_dark = "#b48ead"
`)

	th, err := LoadTheme("nord")
	if err != nil {
		t.Fatalf("LoadTheme(\"nord\") error = %v", err)
	}
	if len(th.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", th.Warnings)
	}

	wantColors := nordThemeColors()
	wantColors.Accent.Dark = "#b48ead"
	want := buildTheme(wantColors, true, nil)
	if got, want := th.Accent.Render("x"), want.Accent.Render("x"); got != want {
		t.Fatalf("Accent.Render(x) = %q, want nord with the file's dark accent", got)
	}
	// The rest of nord is still nord, not the default palette.
	if got, want := th.Base.Render("x"), buildTheme(nordThemeColors(), true, nil).Base.Render("x"); got != want {
		t.Fatalf("Base.Render(x) = %q, want nord's background and foreground", got)
	}
}

// TestLoadThemeInvalidUserFileNamesTheFile: a theme file that does not
// parse must fail with an error naming the file, so the curator can find
// the one they wrote, and must not silently fall back to a palette they did
// not ask for.
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
// pre-existing theme.toml overlay.
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

// TestLoadThemeUserFileUnknownKeyWarns: an unknown key in a user theme is a
// typo a curator wants to hear about, and it must not stop the theme
// loading.
func TestLoadThemeUserFileUnknownKeyWarns(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, filepath.Join(configDir, "themes"), "sloppy.toml", `
accent_dark = "#2aa198"
acccent_light = "#268bd2"
`)

	th, err := LoadTheme("sloppy")
	if err != nil {
		t.Fatalf("LoadTheme(\"sloppy\") error = %v", err)
	}
	if len(th.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one", th.Warnings)
	}
	if !strings.Contains(th.Warnings[0], "acccent_light") || !strings.Contains(th.Warnings[0], "sloppy.toml") {
		t.Fatalf("Warnings[0] = %q, want it to name the key and the file", th.Warnings[0])
	}
	if th.Accent.Render("x") == "" {
		t.Fatal("Accent.Render(x) is empty; theme was not usable after a warning")
	}
}

// TestLoadThemeTraversalNameNeverReadsOutsideTheConfigDir: a theme name is
// user-controlled config input, so one carrying a path separator must not
// turn into a read of an arbitrary file. The decoy written outside the
// themes directory is exactly where a naive filepath.Join would land.
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
	want := buildTheme(defaultThemeColors(), true, th.Warnings)
	if got, want := th.Accent.Render("x"), want.Accent.Render("x"); got != want {
		t.Fatalf("Accent.Render(x) = %q, want the default palette (the escaped file must not be read)", got)
	}
}

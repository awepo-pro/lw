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

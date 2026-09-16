package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/ui"
)

// withTUIThemeSeam swaps loadTUITheme for fn, recording every theme name the
// TUI hands ui.LoadTheme, and restoring the real loader on cleanup.
func withTUIThemeSeam(t *testing.T, fn func(name string) (ui.Theme, error)) *[]string {
	t.Helper()
	requested := new([]string)
	orig := loadTUITheme
	loadTUITheme = func(name string) (ui.Theme, error) {
		*requested = append(*requested, name)
		if fn == nil {
			return orig(name)
		}
		return fn(name)
	}
	t.Cleanup(func() { loadTUITheme = orig })
	return requested
}

// TestTUIThemeUsesConfigThemeName is the S6 wrap-up's cmd/lw half: `theme`
// in config.toml is what reaches ui.LoadTheme. LoadTheme("") — the call this
// replaces — ignores the config, so a curator who set nord got lw's default
// palette and had no way to tell why. "nord" no longer names a compiled
// palette (contract §3 note 2, 003): it degrades to the default theme with
// the exact retirement warning, which is itself the proof the config's name
// reached LoadTheme.
func TestTUIThemeUsesConfigThemeName(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, `theme = "nord"
`)

	requested := withTUIThemeSeam(t, nil)

	theme, keys, err := tuiTheme()
	if err != nil {
		t.Fatalf("tuiTheme: %v", err)
	}
	if len(*requested) != 1 {
		t.Fatalf("LoadTheme called %d time(s) with %v, want exactly one call", len(*requested), *requested)
	}
	if got := (*requested)[0]; got != "nord" {
		t.Fatalf("LoadTheme was given %q, want the config's \"nord\"", got)
	}
	wantWarnings := []string{`theme: "nord" is not available in lw 2; using the default theme`}
	if !reflect.DeepEqual(theme.Warnings, wantWarnings) {
		t.Errorf("warnings = %v, want %v", theme.Warnings, wantWarnings)
	}
	if keys.Warnings != nil {
		t.Errorf("keymap warnings = %v, want none with no hotkeys.toml", keys.Warnings)
	}
}

// TestTUIThemeEmptyConfigFallsBackToDefaultTheme pins the no-theme case: a
// config that does not name a theme asks LoadTheme for "", lw's own "use the
// default" spelling — not for some made-up name.
func TestTUIThemeEmptyConfigFallsBackToDefaultTheme(t *testing.T) {
	configTestEnv(t)

	requested := withTUIThemeSeam(t, nil)

	if _, _, err := tuiTheme(); err != nil {
		t.Fatalf("tuiTheme: %v", err)
	}
	if got := (*requested)[0]; got != "" {
		t.Fatalf("LoadTheme was given %q, want \"\" (lw's default)", got)
	}
}

// TestTUIThemeGarbageNameDegradesWithAWarning: a theme name nothing knows
// about must not abort the launch — LoadTheme falls back to the default
// palette and says so, and tuiTheme prints what it said instead of swallowing
// it.
func TestTUIThemeGarbageNameDegradesWithAWarning(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, `theme = "solar-flare-9000"
`)

	stdout, stderr, code := captureRun(t, func() int {
		theme, _, err := tuiTheme()
		if err != nil {
			t.Errorf("tuiTheme: %v", err)
			return 1
		}
		if len(theme.Warnings) == 0 {
			t.Error("theme carries no warning; the garbage name went unnoticed")
		}
		return 0
	})
	_ = stdout

	if code != 0 {
		t.Fatalf("tuiTheme degraded to exit %d, want 0 — a garbage theme name is never fatal", code)
	}
	if !strings.Contains(stderr, `theme: "solar-flare-9000" is not available in lw 2; using the default theme`) {
		t.Errorf("stderr = %q, want the unknown-theme warning", stderr)
	}
	if !strings.Contains(stderr, "lw tui: warning:") {
		t.Errorf("stderr = %q, want warnings prefixed for stderr", stderr)
	}
}

// TestTUIThemeSurfacesFileWarnings covers the other half of the fix: an
// unknown key in hotkeys.toml (the same shape as one in theme.toml) reaches
// stderr before the TUI starts, once per warning, instead of being silently
// ignored by LoadKeys.
func TestTUIThemeSurfacesFileWarnings(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, `theme = "nord"
`)
	if err := os.WriteFile(filepath.Join(dir, "lw", "hotkeys.toml"), []byte("move_down = [\"n\"]\nbogus_key = true\n"), 0o600); err != nil {
		t.Fatalf("write hotkeys.toml: %v", err)
	}

	_, stderr, code := captureRun(t, func() int {
		if _, _, err := tuiTheme(); err != nil {
			t.Errorf("tuiTheme: %v", err)
			return 1
		}
		return 0
	})

	if code != 0 {
		t.Fatalf("tuiTheme exited %d, want 0", code)
	}
	if !strings.Contains(stderr, "hotkeys.toml: unknown key") {
		t.Errorf("stderr = %q, want the hotkeys.toml warning", stderr)
	}
	if n := strings.Count(stderr, "hotkeys.toml: unknown key"); n != 1 {
		t.Errorf("warning printed %d time(s), want each exactly once:\n%s", n, stderr)
	}
}

// TestTUIThemeSurvivesMalformedConfig is the tolerance contract: the theme
// path reads the config itself, so a broken config.toml must degrade to the
// default theme — and still launch — rather than becoming a launch failure.
func TestTUIThemeSurvivesMalformedConfig(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, "not [valid toml")

	requested := withTUIThemeSeam(t, nil)

	stdout, stderr, code := captureRun(t, func() int {
		_, _, err := tuiTheme()
		if err != nil {
			t.Errorf("tuiTheme: %v", err)
			return 1
		}
		return 0
	})
	_ = stdout

	if code != 0 {
		t.Fatalf("tuiTheme exited %d, want 0 — a config failure must not abort the TUI", code)
	}
	if got := (*requested)[0]; got != "" {
		t.Errorf("LoadTheme was given %q, want \"\" (the default) after the config failed", got)
	}
	if !strings.Contains(stderr, "lw tui: config: parse") {
		t.Errorf("stderr = %q, want the config failure reported", stderr)
	}
}

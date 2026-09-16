package main

import (
	"testing"

	"github.com/awepo-pro/lw/internal/ui"
)

// testTUIDeps builds a ui.Deps around lw's compiled-in theme and key
// defaults, with no *stage.Engine (matching every screen's own
// "constructible headless" contract). XDG_CONFIG_HOME points at a fresh
// empty temp dir for the test's duration so LoadTheme/LoadKeys never read a
// real user config (00-conventions.md §3: tests must be deterministic).
func testTUIDeps(t *testing.T) ui.Deps {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	return ui.Deps{Theme: theme, Keys: keys}
}

// TestBuildTUIOptionsInjectsAllFivePanes is s4-tui.md S4-T8's own goal: lw
// opens with all five screens live, and no "screen not loaded" placeholder
// (C-103). Exercises buildTUIOptions directly rather than cmdTUI, since
// cmdTUI calls tea.Program.Run(), which never returns when its input
// reaches EOF (C-83) — a test must drive the wiring, not the program.
func TestBuildTUIOptionsInjectsAllFivePanes(t *testing.T) {
	opts := buildTUIOptions(testTUIDeps(t))

	want := []ui.Screen{ui.ScreenBrowse, ui.ScreenReview, ui.ScreenAsk, ui.ScreenLint, ui.ScreenLog}
	if len(opts.Panes) != len(want) {
		t.Fatalf("len(Panes) = %d, want %d (%v)", len(opts.Panes), len(want), want)
	}
	for _, s := range want {
		p, ok := opts.Panes[s]
		if !ok || p == nil {
			t.Errorf("Panes[%v] missing or nil", s)
		}
	}
}

// TestBuildTUIOptionsStartsOnReview pins /docs/design.md §13's "ship review before
// anything else" — §9 calls review "the reason this project exists" — to
// Options.Start rather than leaving it to whatever NewApp happens to
// default to.
func TestBuildTUIOptionsStartsOnReview(t *testing.T) {
	opts := buildTUIOptions(testTUIDeps(t))

	if opts.Start != ui.ScreenReview {
		t.Fatalf("Start = %v, want ui.ScreenReview", opts.Start)
	}
}

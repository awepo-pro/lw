package main

import (
	"testing"
	"time"
)

// TestTUIReloadEvery pins 008 contract §6's last line: the ui.Options the
// TUI builds carry ReloadEvery = 2s, so the App's reload tick notices a
// commit made by another process (T-G's VaultReloadedMsg needs a producer
// with a period, not just review's own commit). Driven through
// buildTUIOptions — never cmdTUI, whose tea.Program.Run never returns when
// its input reaches EOF (C-83).
func TestTUIReloadEvery(t *testing.T) {
	t.Run("tui_options_reload_every_two_seconds", func(t *testing.T) {
		opts := buildTUIOptions(testTUIDeps(t))
		if opts.ReloadEvery != 2*time.Second {
			t.Errorf("buildTUIOptions().ReloadEvery = %v, want 2s", opts.ReloadEvery)
		}
	})
}

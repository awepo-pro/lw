// logging_test.go pins 025 T3's shell launch-timing log lines against the
// file-backed handler logging.Init installs for real runs (010 D-10B):
// NewApp's launch refresh logs "tui shell ready" with lint_errors, the
// first WindowSizeMsg logs "tui first frame" exactly once, and a reload
// tick that actually reloads logs "vault reloaded" — once, not per quiet
// tick. The tea.Program-bound lines (tui engine open's caller, and the
// frame tea renders itself) stay with cmd/lw, which never drives Run in a
// test (C-83).
package ui

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/logging"
)

// installFileLog points slog.Default at <tmp>/lw.log through logging.Init
// and restores the previous default when the test ends (the internal/llm
// logging_test idiom).
func installFileLog(t *testing.T) string {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	dir := t.TempDir()
	if err := logging.Init(dir, slog.LevelDebug); err != nil {
		t.Fatalf("logging.Init: %v", err)
	}
	return dir
}

// readLog returns the whole log file installed by installFileLog.
func readLog(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(dir + "/lw.log")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(b)
}

// requireLine fails when log has no line carrying want, and requires every
// matching line to carry dur_ms — the one field every 025 T3 launch line
// must have.
func requireLine(t *testing.T, log, want string) {
	t.Helper()
	var found bool
	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, want) {
			continue
		}
		found = true
		if !strings.Contains(line, "dur_ms=") {
			t.Errorf("line %q is missing dur_ms", line)
		}
	}
	if !found {
		t.Errorf("log has no line for %q:\n%s", want, log)
	}
}

func TestUILogsLaunchTimings(t *testing.T) {
	t.Run("shell_ready_carries_lint_errors", func(t *testing.T) {
		dir := installFileLog(t)

		a, _ := newTickApp(t, 0)

		if a.lintErrors != 0 {
			t.Fatalf("fixture vault carries %d lint errors; this pin assumes a clean one", a.lintErrors)
		}
		log := readLog(t, dir)
		requireLine(t, log, `msg="tui shell ready"`)
		for _, line := range strings.Split(log, "\n") {
			if strings.Contains(line, `msg="tui shell ready"`) && !strings.Contains(line, "lint_errors=0") {
				t.Errorf("shell ready line = %q, want lint_errors=0 for the clean fixture", line)
			}
		}
	})

	t.Run("first_frame_logs_once", func(t *testing.T) {
		dir := installFileLog(t)

		a, _ := newTickApp(t, 0)
		a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
		// A resize re-enters the same case; the line must stay at one.
		a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

		log := readLog(t, dir)
		if got := strings.Count(log, `msg="tui first frame"`); got != 1 {
			t.Errorf("tui first frame lines = %d, want exactly 1:\n%s", got, log)
		}
		requireLine(t, log, `msg="tui first frame"`)
	})

	t.Run("reloaded_line_on_a_foreign_commit_only", func(t *testing.T) {
		dir := installFileLog(t)

		a, _ := newTickApp(t, time.Millisecond)
		foreignCommit(t, a.deps.Engine.Vault().Root())

		_, cmd := a.Update(reloadTickMsg{})
		feedApp(t, a, cmd, 32)
		// The next tick finds the stamp refreshed: a quiet tick logs nothing.
		a.Update(reloadTickMsg{})

		log := readLog(t, dir)
		if got := strings.Count(log, `msg="vault reloaded"`); got != 1 {
			t.Errorf("vault reloaded lines = %d, want exactly 1 (the foreign commit):\n%s", got, log)
		}
		requireLine(t, log, `msg="vault reloaded"`)
	})
}

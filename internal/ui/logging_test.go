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
	"regexp"
	"strconv"
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

	// F.W8 freezes "tui first frame" as dur_ms SINCE PROCESS START, not since
	// NewApp. The distinction is not cosmetic: OpenEngine (vault parse + index
	// load) runs before NewApp exists, and anchoring inside NewApp excluded it
	// — the launch's whole heavy half. The defect announced itself as
	// arithmetic, a first frame reported at 5.887ms while "tui engine open"
	// alone measured 6.219ms, so this pin asserts the span actually reaches
	// back past NewApp.
	t.Run("first_frame_measures_from_process_start", func(t *testing.T) {
		dir := installFileLog(t)

		start := time.Now().Add(-750 * time.Millisecond) // a "process" that began earlier
		a, _ := newTickAppFrom(t, 0, start)
		a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

		log := readLog(t, dir)
		var line string
		for _, l := range strings.Split(log, "\n") {
			if strings.Contains(l, `msg="tui first frame"`) {
				line = l
			}
		}
		if line == "" {
			t.Fatalf("no first frame line:\n%s", log)
		}
		m := regexp.MustCompile(`dur_ms=([0-9.]+)`).FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("first frame line carries no dur_ms: %q", line)
		}
		got, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("dur_ms %q: %v", m[1], err)
		}
		// Anchored at ProcessStart the span is >= the 750ms head start.
		// Anchored at NewApp it would be a fraction of a millisecond.
		if got < 700 {
			t.Errorf("dur_ms = %v, want >= 700 — the span must reach back to "+
				"Options.ProcessStart, not start at NewApp", got)
		}
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

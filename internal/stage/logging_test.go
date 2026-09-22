// logging_test.go pins 025 T3's launch-timing log lines against the same
// file-backed handler logging.Init installs for real runs (010 D-10B): a
// cold OpenEngine over the minimal fixture leaves one vault open, index
// load, index build and index save line — each carrying dur_ms — and a
// warm reopen reports loaded_ok=true and rebuilds nothing. Durations,
// counts and booleans only; no path or content may appear (D-10B).
package stage

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/logging"
	"github.com/awepo-pro/lw/internal/testutil"
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

func TestOpenEngineLogsLaunchTimings(t *testing.T) {
	t.Run("cold_start_logs_open_load_build_save", func(t *testing.T) {
		dir := installFileLog(t)
		root := testutil.CopyFixture(t, "minimal")

		e, err := OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { e.Close() })

		log := readLog(t, dir)
		for _, want := range []string{
			`msg="vault open"`,
			`msg="index load"`,
			`msg="index build"`,
			`msg="index save"`,
		} {
			requireLine(t, log, want)
		}
		// A first open over the fixture has no index.gob yet: the load
		// reports not-ok, and the rebuild reports absent, not stale.
		if !strings.Contains(log, "loaded_ok=false") {
			t.Errorf("log missing loaded_ok=false on the cold path:\n%s", log)
		}
		buildLine := strings.Split(strings.Split(log, `msg="index build"`)[1], "\n")[0]
		if !strings.Contains(buildLine, "stale=false") || !strings.Contains(buildLine, "docs=4") {
			t.Errorf("index build line = %q, want stale=false with the fixture's 4 docs", buildLine)
		}
	})

	t.Run("warm_open_loads_without_rebuilding", func(t *testing.T) {
		dir := installFileLog(t)
		root := testutil.CopyFixture(t, "minimal")

		e, err := OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine (cold): %v", err)
		}
		t.Cleanup(func() { e.Close() })
		e2, err := OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine (warm): %v", err)
		}
		t.Cleanup(func() { e2.Close() })

		log := readLog(t, dir)
		if got := strings.Count(log, `msg="vault open"`); got != 2 {
			t.Errorf("vault open lines = %d, want 2 (both opens):\n%s", got, log)
		}
		// The cold build is the only one; the warm open loads ok instead.
		if got := strings.Count(log, `msg="index build"`); got != 1 {
			t.Errorf("index build lines = %d, want 1 (cold open only):\n%s", got, log)
		}
		if got := strings.Count(log, "loaded_ok=true"); got != 1 {
			t.Errorf("loaded_ok=true lines = %d, want 1 (the warm open):\n%s", got, log)
		}
	})
}

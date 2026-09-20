package logging

// logging_test.go pins 010 contract §0's observable behaviour — the frozen
// TestOpenLogger block (MASTER §5): create-and-append, level filtering,
// secret redaction, file-only output, and rotation at the white-box cap.
// Every subtest installs its logger through Init itself (the exact path
// production takes) and restores the previous slog.Default at cleanup, so
// no subtest poisons its siblings.

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// install points slog.Default at a fresh temp dir via Init and restores
// the previous default when the test ends. It returns the active log
// file's path.
func install(t *testing.T, level slog.Level) string {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	dir := t.TempDir()
	if err := Init(dir, level); err != nil {
		t.Fatalf("Init(%s): %v", dir, err)
	}
	return filepath.Join(dir, fileName)
}

// readAll returns the whole named log file.
func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestOpenLogger(t *testing.T) {
	t.Run("appends_across_opens", func(t *testing.T) {
		dir := t.TempDir()
		prev := slog.Default()
		t.Cleanup(func() { slog.SetDefault(prev) })

		if err := Init(dir, slog.LevelInfo); err != nil {
			t.Fatalf("Init: %v", err)
		}
		slog.Info("one")
		if err := Init(dir, slog.LevelInfo); err != nil {
			t.Fatalf("second Init: %v", err)
		}
		slog.Info("two")

		log := readAll(t, filepath.Join(dir, fileName))
		i, j := strings.Index(log, "one"), strings.Index(log, "two")
		if i == -1 || j == -1 {
			t.Fatalf("log does not carry both records:\n%s", log)
		}
		if i > j {
			t.Errorf("records out of order: %q at %d before %q at %d", "two", j, "one", i)
		}
	})

	t.Run("creates_file_and_dirs", func(t *testing.T) {
		deep := filepath.Join(t.TempDir(), "not-yet", "nested")
		if err := Init(deep, slog.LevelInfo); err != nil {
			t.Fatalf("Init: %v", err)
		}
		path := filepath.Join(deep, fileName)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s not created (parents included): %v", path, err)
		}
		slog.Info("writes through")
		if log := readAll(t, path); !strings.Contains(log, "writes through") {
			t.Errorf("installed logger does not write through:\n%s", log)
		}
	})

	t.Run("level_filters_debug", func(t *testing.T) {
		path := install(t, slog.LevelInfo)
		slog.Debug("hidden-debug")
		slog.Info("shown-info")
		log := readAll(t, path)
		if strings.Contains(log, "hidden-debug") {
			t.Errorf("Debug record leaked at LevelInfo:\n%s", log)
		}
		if !strings.Contains(log, "shown-info") {
			t.Errorf("Info record missing at LevelInfo:\n%s", log)
		}

		path = install(t, slog.LevelDebug)
		slog.Debug("shown-debug")
		if log := readAll(t, path); !strings.Contains(log, "shown-debug") {
			t.Errorf("Debug record missing at LevelDebug:\n%s", log)
		}
	})

	t.Run("no_terminal_output", func(t *testing.T) {
		path := install(t, slog.LevelInfo)

		outR, outW, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		errR, errW, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		prevOut, prevErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = outW, errW
		slog.Info("file only")
		os.Stdout, os.Stderr = prevOut, prevErr
		outW.Close()
		errW.Close()
		outB, _ := io.ReadAll(outR)
		errB, _ := io.ReadAll(errR)

		if len(outB) != 0 {
			t.Errorf("stdout got %q during a file-only Info record", outB)
		}
		if len(errB) != 0 {
			t.Errorf("stderr got %q during a file-only Info record", errB)
		}
		if log := readAll(t, path); !strings.Contains(log, "file only") {
			t.Errorf("record missing from the file:\n%s", log)
		}
	})

	t.Run("redacts_secret_keys", func(t *testing.T) {
		path := install(t, slog.LevelDebug)
		slog.Info("creds",
			"api_key", "sk-secret",
			"authorization", "Bearer x",
			"token", "tok-1",
			"password", "pw-1",
		)

		log := readAll(t, path)
		for _, secret := range []string{"sk-secret", "Bearer x", "tok-1", "pw-1"} {
			if strings.Contains(log, secret) {
				t.Errorf("log leaked %q:\n%s", secret, log)
			}
		}
		if n := strings.Count(log, redacted); n != 4 {
			t.Errorf("want 4 %s values, got %d:\n%s", redacted, n, log)
		}
	})

	t.Run("rotates_at_cap", func(t *testing.T) {
		prevCap := maxLogBytes
		maxLogBytes = 1024
		t.Cleanup(func() { maxLogBytes = prevCap })

		dir := t.TempDir()
		prev := slog.Default()
		t.Cleanup(func() { slog.SetDefault(prev) })
		if err := Init(dir, slog.LevelInfo); err != nil {
			t.Fatalf("Init: %v", err)
		}

		first := strings.Repeat("a", 900)
		slog.Info(first)
		second := strings.Repeat("b", 900) // would push past the 1 KiB cap
		slog.Info(second)

		rotated := readAll(t, filepath.Join(dir, rotatedTo))
		if !strings.Contains(rotated, first) {
			t.Errorf("lw.log.1 does not hold the pre-rotation record:\n%s", rotated)
		}
		if strings.Contains(rotated, second) {
			t.Errorf("lw.log.1 leaked the post-rotation record:\n%s", rotated)
		}
		fresh := readAll(t, filepath.Join(dir, fileName))
		if !strings.Contains(fresh, second) {
			t.Errorf("fresh lw.log does not hold the post-rotation record:\n%s", fresh)
		}
		if strings.Contains(fresh, first) {
			t.Errorf("fresh lw.log carries pre-rotation content:\n%s", fresh)
		}
	})
}

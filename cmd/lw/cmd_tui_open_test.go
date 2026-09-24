// cmd_tui_open_test.go pins cmd/lw's half of the 034 T5 seam: the
// openPDFOpener closure ui.Deps.OpenPDF is wired from. It runs the viewer
// detached (the test's fake writes its argv aside and exits), traces the
// launch in lw.log with the vault-relative file, and returns the unset hint
// while open.pdf is not configured. Drives the closure directly rather than
// cmdTUI, which calls tea.Program.Run and never returns once its input
// reaches EOF (C-83).
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/logging"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

func TestOpenPDFWiring(t *testing.T) {
	// The fake viewer records its argv, one argument per line, so the test
	// can assert the compiled argv without a real PDF viewer installed.
	record := filepath.Join(t.TempDir(), "argv.txt")
	viewer := filepath.Join(t.TempDir(), "fakeviewer")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + record + "; done\n"
	if err := os.WriteFile(viewer, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile fake viewer: %v", err)
	}

	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer engine.Close()

	t.Run("unset_config_returns_the_hint", func(t *testing.T) {
		open := openPDFOpener(config.Default(), engine) // Default().Open is unset
		pdf := filepath.Join(root, "raw", "papers", "x.pdf")
		err := open(pdf, 12)
		if err == nil || err.Error() != ui.OpenPDFUnsetHint {
			t.Fatalf("open with no open.pdf: error = %v, want %q", err, ui.OpenPDFUnsetHint)
		}
	})

	t.Run("configured_viewer_runs_detached_and_is_traced", func(t *testing.T) {
		// The TUI's log install is what puts the trace in lw.log; the test
		// installs it against the fixture vault the same way cmdTUI does.
		initLoggingAt(root)

		cfg := config.Default()
		cfg.Open.PDF = viewer + " -p {page} {file}"
		open := openPDFOpener(cfg, engine)

		pdf := filepath.Join(root, "raw", "papers", "x.pdf")
		if err := open(pdf, 12); err != nil {
			t.Fatalf("open: %v", err)
		}

		// The detached process reaps itself after writing its argv; poll
		// briefly rather than sleeping a fixed wait.
		var argv []string
		for i := 0; i < 200; i++ {
			if b, err := os.ReadFile(record); err == nil {
				argv = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
				if len(argv) == 3 {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		want := []string{"-p", "12", pdf}
		if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("viewer argv = %q, want %q", argv, want)
		}

		// The trace lands in lw.log with the vault-relative file.
		b, err := os.ReadFile(filepath.Join(logging.Dir(root), "lw.log"))
		if err != nil {
			t.Fatalf("read lw.log: %v", err)
		}
		if !strings.Contains(string(b), "open pdf") ||
			!strings.Contains(string(b), `file=raw/papers/x.pdf`) ||
			!strings.Contains(string(b), "page=12") {
			t.Fatalf("lw.log has no open pdf trace:\n%s", b)
		}
	})
}

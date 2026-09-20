package main

// logging_test.go pins cmd/lw's half of 010 contract §0: the helper main.go
// calls resolves <root>/.llmwiki/logs/lw.log and installs it as
// slog.Default, a log dir that cannot be created never fails anything —
// the default logger stays exactly as it was — and a verb's explicit
// --vault wins over the working directory (A-10-2 / C-1009): the record
// lands under the explicit root, never under the cwd's vault.

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/logging"
)

func TestCmdLoggingHelper(t *testing.T) {
	t.Run("resolves_vault_log_dir", func(t *testing.T) {
		root := t.TempDir()
		prev := slog.Default()
		t.Cleanup(func() { slog.SetDefault(prev) })

		wantDir := filepath.Join(root, ".llmwiki", "logs")
		if got := logging.Dir(root); got != wantDir {
			t.Errorf("logging.Dir(%s) = %s, want %s", root, got, wantDir)
		}

		initLoggingAt(root)

		slog.Info("cmd helper writes through")
		b, err := os.ReadFile(filepath.Join(wantDir, "lw.log"))
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Join(wantDir, "lw.log"), err)
		}
		if !strings.Contains(string(b), "cmd helper writes through") {
			t.Errorf("log file does not carry the record:\n%s", b)
		}
	})

	t.Run("unwritable_log_dir_keeps_default_logger", func(t *testing.T) {
		prev := slog.Default()
		t.Cleanup(func() { slog.SetDefault(prev) })

		blocked := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(blocked, []byte("a file, not a parent\n"), 0o644); err != nil {
			t.Fatalf("write blocker: %v", err)
		}

		initLoggingAt(blocked) // must not panic, must not change the default

		if slog.Default() != prev {
			t.Error("a failed Init replaced slog.Default; contract says it must not")
		}
		if _, err := os.Stat(filepath.Join(blocked, ".llmwiki", "logs", "lw.log")); err == nil {
			t.Error("lw.log created under a file-valued path; Init should have failed")
		}
	})

	t.Run("explicit_vault_outside_cwd_logs_under_explicit_root", func(t *testing.T) {
		prev := slog.Default()
		t.Cleanup(func() { slog.SetDefault(prev) })

		// Two disjoint trees: the working directory sits inside vault A
		// (its SCHEMA.md makes the cwd-ancestry fallback resolve there),
		// while the explicit --vault names vault B — the C-1009 shape an
		// MCP caller or any out-of-vault shell produces. The per-verb
		// install must log under B and touch nothing under A.
		vaultA, vaultB := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(vaultA, "SCHEMA.md"), []byte("# schema\n"), 0o644); err != nil {
			t.Fatalf("write SCHEMA.md: %v", err)
		}
		chdir(t, vaultA)

		root, err := findVaultRoot(vaultB) // the way a verb resolves --vault
		if err != nil {
			t.Fatalf("findVaultRoot(%s): %v", vaultB, err)
		}
		if root != vaultB {
			t.Errorf("findVaultRoot(%s) = %s, want the explicit root unchanged", vaultB, root)
		}
		initLoggingAt(root)

		slog.Info("explicit vault wins over cwd")

		b, err := os.ReadFile(filepath.Join(vaultB, ".llmwiki", "logs", "lw.log"))
		if err != nil {
			t.Fatalf("read the explicit root's lw.log: %v", err)
		}
		if !strings.Contains(string(b), "explicit vault wins over cwd") {
			t.Errorf("explicit-root log does not carry the record:\n%s", b)
		}
		if _, err := os.Stat(filepath.Join(vaultA, ".llmwiki", "logs")); !os.IsNotExist(err) {
			t.Errorf("cwd vault A got a log dir (stat err %v); the explicit root must be the only destination", err)
		}
	})

	t.Run("attach_joins_existing_trail_never_creates_one", func(t *testing.T) {
		prev := slog.Default()
		t.Cleanup(func() { slog.SetDefault(prev) })

		// Without a trail, attach creates nothing (005 §4, doctor's
		// fresh-vault check) and lands the discard logger — the T-L review
		// minor folded into T-D: a doctor probe on a trail-less vault must
		// never fall to stderr (D-10B: never a terminal). The previous
		// default is swapped for io.Discard, so a record that arrives
		// anyway reaches nothing at all.
		bare := t.TempDir()
		var sink bytes.Buffer
		sentinel := slog.New(slog.NewTextHandler(&sink, nil))
		slog.SetDefault(sentinel)
		attachLoggingAt(bare)
		if _, err := os.Stat(filepath.Join(bare, ".llmwiki")); !os.IsNotExist(err) {
			t.Fatalf("attachLoggingAt created state under a vault with none (stat err %v)", err)
		}
		slog.Info("doctor probe on a trail-less vault")
		if sink.Len() != 0 {
			t.Errorf("a trail-less attach left records reaching the previous sink:\n%s", sink.String())
		}
		if slog.Default() == sentinel {
			t.Error("a trail-less attach kept the previous default; want the discard logger installed")
		}

		// With a trail, attach installs it and records flow through.
		worn := t.TempDir()
		trail := filepath.Join(worn, ".llmwiki", "logs")
		if err := os.MkdirAll(trail, 0o755); err != nil {
			t.Fatalf("mkdir trail: %v", err)
		}
		attachLoggingAt(worn)
		slog.Info("read-only verb joins the trail")
		b, err := os.ReadFile(filepath.Join(trail, "lw.log"))
		if err != nil {
			t.Fatalf("read the existing trail's lw.log: %v", err)
		}
		if !strings.Contains(string(b), "read-only verb joins the trail") {
			t.Errorf("attached logger does not write through:\n%s", b)
		}
	})
}

// TestCreateVerbsInstallLogging is the per-verb wiring tripwire (A-10-3):
// every verb that can stage — ingest, commit, revert, stage, query, tui,
// mcp, lint — must call initLoggingAt itself, right after its own
// findVaultRoot succeeds, so an explicit --vault logs under the explicit
// root and never under the cwd the fallback resolved (C-1009). The scan
// reads each verb file directly, so a dropped install line fails here
// loudly instead of surfacing as a mis-placed trail in the field.
func TestCreateVerbsInstallLogging(t *testing.T) {
	for _, verb := range []string{
		"cmd_ingest", "cmd_commit", "cmd_revert", "cmd_stage",
		"cmd_query", "cmd_tui", "cmd_mcp", "cmd_lint",
	} {
		t.Run(verb, func(t *testing.T) {
			b, err := os.ReadFile(verb + ".go")
			if err != nil {
				t.Fatalf("read source: %v", err)
			}
			if !strings.Contains(string(b), "initLoggingAt(") {
				t.Errorf("%s.go holds no initLoggingAt( call; its log trail would fall to the cwd fallback instead of the verb's own --vault root", verb)
			}
		})
	}
}

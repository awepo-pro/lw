package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/ledger"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestLedgerFixI_I2_BookkeepingPermissions tests I2 (082 A2):
// the atomic append keeps bookkeeping.md's permission bits (0644 for a new file).
func TestLedgerFixI_I2_BookkeepingPermissions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	// Case 1: New bookkeeping.md file should have 0644 permissions.
	t.Run("new bookkeeping.md permissions", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		notePath := filepath.Join(root, "2026-11-10.md")
		if err := os.WriteFile(notePath, []byte("44 (kfc)\n"), 0o644); err != nil {
			t.Fatalf("write note: %v", err)
		}

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
		})
		if code != 0 {
			t.Fatalf("collect failed code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
		}

		bkPath := filepath.Join(root, ledger.LooseFile)
		st, err := os.Stat(bkPath)
		if err != nil {
			t.Fatalf("stat bookkeeping.md: %v", err)
		}
		if gotPerm := st.Mode().Perm(); gotPerm != 0o644 {
			t.Errorf("new bookkeeping.md perm = 0%o, want 0644", gotPerm)
		}
	})

	// Case 2: Existing bookkeeping.md preserves custom permissions (e.g. 0664).
	t.Run("existing bookkeeping.md keeps permissions", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		bkPath := filepath.Join(root, ledger.LooseFile)
		if err := os.WriteFile(bkPath, []byte("2026-11-09, 10 (coffee)\n"), 0o664); err != nil {
			t.Fatalf("write existing bookkeeping.md: %v", err)
		}
		if err := os.Chmod(bkPath, 0o664); err != nil {
			t.Fatalf("chmod: %v", err)
		}

		notePath := filepath.Join(root, "2026-11-10.md")
		if err := os.WriteFile(notePath, []byte("44 (kfc)\n"), 0o644); err != nil {
			t.Fatalf("write note: %v", err)
		}

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
		})
		if code != 0 {
			t.Fatalf("collect failed code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
		}

		st, err := os.Stat(bkPath)
		if err != nil {
			t.Fatalf("stat bookkeeping.md: %v", err)
		}
		if gotPerm := st.Mode().Perm(); gotPerm != 0o664 {
			t.Errorf("existing bookkeeping.md perm = 0%o, want 0664", gotPerm)
		}
	})
}

// TestLedgerFixI_I3_Symlinks tests I3 (082 C1):
// a day-note candidate must be a regular file (e.Type().IsRegular()); a symlink named 2026-11-10.md is ignored.
// The archive path and ledger/receipts/... are created and written only when no path element under the vault
// is a symlink (else the note is waiting ...: ledger/ holds a symlink).
func TestLedgerFixI_I3_Symlinks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	t.Run("symlink candidate at root is ignored", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		realFile := filepath.Join(t.TempDir(), "target.md")
		if err := os.WriteFile(realFile, []byte("44 (kfc)\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		symlinkPath := filepath.Join(root, "2026-11-10.md")
		if err := os.Symlink(realFile, symlinkPath); err != nil {
			t.Fatal(err)
		}

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
		})
		if code != 0 {
			t.Fatalf("code = %d, want 0", code)
		}
		if !strings.Contains(stdout, "collected 0 day note(s), 0 waiting") {
			t.Errorf("stdout = %q, want collected 0 day note(s), 0 waiting", stdout)
		}
	})

	t.Run("symlink candidate in ledger/ is ignored", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ledgerDir := filepath.Join(root, "ledger")
		if err := os.MkdirAll(ledgerDir, 0o755); err != nil {
			t.Fatal(err)
		}
		realFile := filepath.Join(t.TempDir(), "target.md")
		if err := os.WriteFile(realFile, []byte("44 (kfc)\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		symlinkPath := filepath.Join(ledgerDir, "2026-11-10.md")
		if err := os.Symlink(realFile, symlinkPath); err != nil {
			t.Fatal(err)
		}

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
		})
		if code != 0 {
			t.Fatalf("code = %d, want 0", code)
		}
		if !strings.Contains(stdout, "collected 0 day note(s), 0 waiting") {
			t.Errorf("stdout = %q, want collected 0 day note(s), 0 waiting", stdout)
		}
	})

	t.Run("ledger/ is a symlink: waiting ... holds a symlink", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		// Day note at root
		notePath := filepath.Join(root, "2026-11-10.md")
		if err := os.WriteFile(notePath, []byte("44 (kfc)\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// ledger is a symlink
		targetDir := t.TempDir()
		ledgerSymlink := filepath.Join(root, "ledger")
		if err := os.Symlink(targetDir, ledgerSymlink); err != nil {
			t.Fatal(err)
		}

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
		})
		if code != 0 {
			t.Fatalf("code = %d, want 0", code)
		}
		wantWaiting := "waiting 2026-11-10.md: ledger/ holds a symlink"
		if !strings.Contains(stdout, wantWaiting) {
			t.Errorf("stdout %q does not contain %q", stdout, wantWaiting)
		}
		if !strings.Contains(stdout, "collected 0 day note(s), 1 waiting") {
			t.Errorf("stdout %q does not contain 'collected 0 day note(s), 1 waiting'", stdout)
		}
		// bookkeeping.md not created
		bkPath := filepath.Join(root, ledger.LooseFile)
		if _, err := os.Stat(bkPath); err == nil {
			t.Errorf("bookkeeping.md exists, want not created")
		}
		// note still at root
		if _, err := os.Stat(notePath); err != nil {
			t.Errorf("day note was moved/deleted: %v", err)
		}
	})

	t.Run("ledger/receipts is a symlink: waiting ... holds a symlink", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ledgerDir := filepath.Join(root, "ledger")
		if err := os.MkdirAll(ledgerDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Day note at root referencing an image
		notePath := filepath.Join(root, "2026-11-10.md")
		if err := os.WriteFile(notePath, []byte("![[receipt.jpg]]\n44 (kfc)\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		imagePath := filepath.Join(root, "receipt.jpg")
		if err := os.WriteFile(imagePath, []byte("fake image data"), 0o644); err != nil {
			t.Fatal(err)
		}
		// ledger/receipts is a symlink
		targetDir := t.TempDir()
		receiptsSymlink := filepath.Join(ledgerDir, "receipts")
		if err := os.Symlink(targetDir, receiptsSymlink); err != nil {
			t.Fatal(err)
		}

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
		})
		if code != 0 {
			t.Fatalf("code = %d, want 0", code)
		}
		wantWaiting := "waiting 2026-11-10.md: ledger/receipts/ holds a symlink"
		if !strings.Contains(stdout, wantWaiting) {
			t.Errorf("stdout %q does not contain %q", stdout, wantWaiting)
		}
	})
}

// TestLedgerFixI_I4_InvalidLineFlag tests I4 (082 C7):
// --line containing \n or \r, or a [[, is exit 2.
func TestLedgerFixI_I4_InvalidLineFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	badLines := []struct {
		name string
		line string
	}{
		{name: "newline", line: "44 (kfc)\n55 (taxi)"},
		{name: "carriage return", line: "44 (kfc)\r55 (taxi)"},
		{name: "source link with date", line: "44 (kfc) [[2026-11-10]]"},
		{name: "brackets", line: "44 (kfc) [[other]]"},
	}

	for _, tc := range badLines {
		t.Run(tc.name, func(t *testing.T) {
			root := testutil.CopyFixture(t, "minimal")
			notePath := filepath.Join(root, "2026-11-10.md")
			if err := os.WriteFile(notePath, []byte("44 (kfc)\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			stdout, stderr, code := captureRun(t, func() int {
				return run([]string{"ledger", "collect", "--vault", root, "--day", "2026-11-10", "--line", tc.line})
			})
			if code != 2 {
				t.Fatalf("code = %d, want 2\nstdout=%s\nstderr=%s", code, stdout, stderr)
			}
			// bookkeeping.md should not be created
			bkPath := filepath.Join(root, ledger.LooseFile)
			if _, err := os.Stat(bkPath); err == nil {
				t.Errorf("bookkeeping.md created on invalid --line")
			}
		})
	}
}

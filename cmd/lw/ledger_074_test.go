package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestLedgerInvisibleToHistory pins 074 F3 (modelled on TestNotesInvisibleToHistory):
// every commit's snapshot manifest is built by walking the vault for *.md, and the walk
// skips the vault-root ledger/ directory. A ledger file written between two commits is not
// recorded in any snapshot manifest and `lw revert` of the second commit prints no line
// containing `skipped: ledger/`.
func TestLedgerInvisibleToHistory(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	commit := func(id, msg, path, title string) {
		t.Helper()
		openCreatePageChangeset(t, root, path, title)
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"commit", "--vault", root, "-m", msg})
		})
		if code != 0 || !strings.Contains(stdout, "committed "+id) {
			t.Fatalf("commit %s: exit = %d, stdout=%q stderr=%q", id, code, stdout, stderr)
		}
	}

	commit("000001", "first page", "wiki/concepts/first-page.md", "First Page")

	// The ledger file lands between two commits.
	ledgerPath := filepath.Join(root, "ledger", "2026", "10", "2026-10-31.md")
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledgerPath, []byte("- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) lunch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	commit("000002", "second page", "wiki/concepts/second-page.md", "Second Page")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	for _, id := range []string{"000000", "000001", "000002"} {
		snap, err := e.Snapshot(id)
		if err != nil {
			t.Fatalf("Snapshot(%s): %v", id, err)
		}
		for p := range snap {
			if strings.HasPrefix(p, "ledger/") {
				t.Errorf("snapshot %s records %s; ledger/ is outside the vault's history", id, p)
			}
		}
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"revert", "--vault", root, "000002"})
	})
	if code != 0 {
		t.Fatalf("revert: exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "skipped: ledger/") {
		t.Errorf("revert output mentions skipped: ledger/:\n%s", stdout)
	}
}

// TestNonWikiRootsAgree pins 074 C-4: consumers of the non-wiki root directories
// must agree with vault.NonWikiRoots. noteDirName equals vault.NotesDir; after
// `lw storage add f`, the payload exists under <vault>/vault.StorageDir/; and the
// `notes` and `storage` directory names lw actually writes equal the set members.
func TestNonWikiRootsAgree(t *testing.T) {
	if noteDirName != vault.NotesDir {
		t.Errorf("noteDirName = %q, want vault.NotesDir %q", noteDirName, vault.NotesDir)
	}

	root := testutil.CopyFixture(t, "minimal")
	content := "payload content\n"
	srcFile := writeScratchFile(t, t.TempDir(), "agree.txt", content)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"storage", "add", srcFile, "--name", "agree.txt", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("storage add failed: code=%d, stderr=%q, stdout=%q", code, stderr, stdout)
	}

	payloadPath := filepath.Join(root, vault.StorageDir, "agree.txt")
	got, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatalf("payload does not exist under <vault>/vault.StorageDir/: %v", err)
	}
	if string(got) != content {
		t.Errorf("stored payload = %q, want %q", string(got), content)
	}

	// Verify lw note actually writes under vault.NotesDir
	setNoteClock(t, time.Date(2026, 10, 31, 12, 0, 0, 0, noteZone))
	_, stderr, code = captureRun(t, func() int {
		return run([]string{"note", "-m", "agree note", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("note failed: code=%d, stderr=%q", code, stderr)
	}
	notesEntries, err := os.ReadDir(filepath.Join(root, vault.NotesDir))
	if err != nil || len(notesEntries) == 0 {
		t.Fatalf("no note created under <vault>/vault.NotesDir: %v", err)
	}

	// The directory names lw actually writes equal the set members.
	if !vault.IsNonWikiRoot(noteDirName) {
		t.Errorf("noteDirName %q not in vault.NonWikiRoots", noteDirName)
	}
	if !vault.IsNonWikiRoot(vault.StorageDir) {
		t.Errorf("vault.StorageDir %q not in vault.NonWikiRoots", vault.StorageDir)
	}
}

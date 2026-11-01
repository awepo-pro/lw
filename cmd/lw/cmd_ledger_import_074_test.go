package main

// cmd_ledger_import_074_test.go pins the S2 ledger import verb (074) and the
// C-4 refactor (074 D-8).
//
// Receipts delivered by Syncthing to ledger/receipts/ are immutable local files
// until lw ledger import copies them into storage/ as receipts/<path>, where
// lw sync carries them to other machines. The Syncthing copy is never deleted
// or modified (074 I7).

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/storage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestLedgerImportLifecycle tests the lifecycle of importing receipts:
// fresh vault -> stored x2 + summary + sync hint, storage list has both, receipts untouched;
// second run -> unchanged x2, exit 0;
// modified receipt -> conflict line, exit 1, storage bytes untouched.
func TestLedgerImportLifecycle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	receiptsDir := filepath.Join(root, "ledger", "receipts", "2026", "10")
	if err := os.MkdirAll(receiptsDir, 0o755); err != nil {
		t.Fatalf("mkdir receipts: %v", err)
	}

	content1 := []byte("receipt 1 image bytes\n")
	content2 := []byte("receipt 2 image bytes\n")
	file1 := filepath.Join(receiptsDir, "r-20261031-1230.jpg")
	file2 := filepath.Join(receiptsDir, "r-20261031-1300.png")
	if err := os.WriteFile(file1, content1, 0o644); err != nil {
		t.Fatalf("write file1: %v", err)
	}
	if err := os.WriteFile(file2, content2, 0o644); err != nil {
		t.Fatalf("write file2: %v", err)
	}

	// 1. First run: stores both receipts, prints sync hint, exits 0
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "import", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("run 1: exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}

	wantLines := []string{
		"stored receipts/2026/10/r-20261031-1230.jpg",
		"stored receipts/2026/10/r-20261031-1300.png",
		"2 stored, 0 unchanged, 0 conflict(s), 0 error(s)",
		"run lw sync to carry them to your other PCs",
	}
	for _, want := range wantLines {
		if !strings.Contains(stdout, want) {
			t.Errorf("run 1 stdout missing %q; got:\n%s", want, stdout)
		}
	}

	// Verify storage list shows both
	outList, _, codeList := captureRun(t, func() int {
		return run([]string{"storage", "list", "--vault", root})
	})
	if codeList != 0 {
		t.Fatalf("storage list: exit %d", codeList)
	}
	if !strings.Contains(outList, "receipts/2026/10/r-20261031-1230.jpg") {
		t.Errorf("storage list missing receipt 1:\n%s", outList)
	}
	if !strings.Contains(outList, "receipts/2026/10/r-20261031-1300.png") {
		t.Errorf("storage list missing receipt 2:\n%s", outList)
	}

	// Verify source files still exist byte-identical (074 I7)
	got1, err := os.ReadFile(file1)
	if err != nil || !bytes.Equal(got1, content1) {
		t.Fatalf("source file1 modified or missing: %v", err)
	}
	got2, err := os.ReadFile(file2)
	if err != nil || !bytes.Equal(got2, content2) {
		t.Fatalf("source file2 modified or missing: %v", err)
	}

	// 2. Second run: unchanged x2, exit 0, no sync hint
	stdout2, stderr2, code2 := captureRun(t, func() int {
		return run([]string{"ledger", "import", "--vault", root})
	})
	if code2 != 0 {
		t.Fatalf("run 2: exit code = %d, want 0; stderr=%q stdout=%q", code2, stderr2, stdout2)
	}
	wantLines2 := []string{
		"unchanged receipts/2026/10/r-20261031-1230.jpg",
		"unchanged receipts/2026/10/r-20261031-1300.png",
		"0 stored, 2 unchanged, 0 conflict(s), 0 error(s)",
	}
	for _, want := range wantLines2 {
		if !strings.Contains(stdout2, want) {
			t.Errorf("run 2 stdout missing %q; got:\n%s", want, stdout2)
		}
	}
	if strings.Contains(stdout2, "run lw sync") {
		t.Errorf("run 2 stdout unexpectedly contains sync hint:\n%s", stdout2)
	}

	// 3. Mutate file1: different bytes -> conflict line, exit 1, storage unchanged
	tampered := []byte("different tampered receipt bytes\n")
	if err := os.WriteFile(file1, tampered, 0o644); err != nil {
		t.Fatalf("tamper file1: %v", err)
	}

	stdout3, _, code3 := captureRun(t, func() int {
		return run([]string{"ledger", "import", "--vault", root})
	})
	if code3 != 1 {
		t.Fatalf("run 3: exit code = %d, want 1; stdout=%q", code3, stdout3)
	}
	wantConflict := "conflict receipts/2026/10/r-20261031-1230.jpg: storage holds different bytes (lw storage show receipts/2026/10/r-20261031-1230.jpg)"
	if !strings.Contains(stdout3, wantConflict) {
		t.Errorf("run 3 stdout missing conflict line %q; got:\n%s", wantConflict, stdout3)
	}
	if !strings.Contains(stdout3, "unchanged receipts/2026/10/r-20261031-1300.png") {
		t.Errorf("run 3 stdout missing unchanged for r2; got:\n%s", stdout3)
	}
	if !strings.Contains(stdout3, "0 stored, 1 unchanged, 1 conflict(s), 0 error(s)") {
		t.Errorf("run 3 stdout missing summary; got:\n%s", stdout3)
	}

	// Ensure stored payload in storage/ was NOT overwritten
	st, err := storage.Open(root)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	rdr, _, err := st.Get("receipts/2026/10/r-20261031-1230.jpg")
	if err != nil {
		t.Fatalf("get stored file: %v", err)
	}
	defer rdr.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(rdr)
	if !bytes.Equal(buf.Bytes(), content1) {
		t.Errorf("storage was modified by conflict! got %q, want %q", buf.Bytes(), content1)
	}
}

// TestLedgerImportDryRun verifies that --dry-run reports would store and writes
// nothing to disk (no storage directory, no storage.json created).
func TestLedgerImportDryRun(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	receiptsDir := filepath.Join(root, "ledger", "receipts")
	if err := os.MkdirAll(receiptsDir, 0o755); err != nil {
		t.Fatalf("mkdir receipts: %v", err)
	}
	fileA := filepath.Join(receiptsDir, "a.jpg")
	fileB := filepath.Join(receiptsDir, "b.jpg")
	if err := os.WriteFile(fileA, []byte("aaa"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(fileB, []byte("bbb"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "import", "--dry-run", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("dry-run exit %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	wantLines := []string{
		"would store receipts/a.jpg",
		"would store receipts/b.jpg",
		"2 stored, 0 unchanged, 0 conflict(s), 0 error(s)",
	}
	for _, want := range wantLines {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry-run stdout missing %q; got:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "run lw sync") {
		t.Errorf("dry-run stdout unexpectedly contains sync hint:\n%s", stdout)
	}

	// Verify nothing was written
	storageDir := filepath.Join(root, vault.StorageDir)
	if _, err := os.Stat(storageDir); !os.IsNotExist(err) {
		t.Errorf("storage directory exists after dry-run: %v", err)
	}
	manifestPath := filepath.Join(root, ".llmwiki", "storage.json")
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Errorf("storage.json exists after dry-run: %v", err)
	}
}

// TestLedgerImportSkipsDotfilesAndSyncthing verifies dotfiles and ~syncthing~
// temporary files/dirs are ignored (074 S2).
func TestLedgerImportSkipsDotfilesAndSyncthing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	receiptsDir := filepath.Join(root, "ledger", "receipts")
	if err := os.MkdirAll(filepath.Join(receiptsDir, ".stversions"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(receiptsDir, ".DS_Store"), []byte("ds"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(receiptsDir, ".stversions", "old.jpg"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(receiptsDir, "~syncthing~x.tmp"), []byte("tmp"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(receiptsDir, "real.jpg"), []byte("real"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "import", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if strings.Contains(stdout, ".DS_Store") || strings.Contains(stdout, ".stversions") || strings.Contains(stdout, "~syncthing~") {
		t.Errorf("stdout contains skipped files:\n%s", stdout)
	}
	if !strings.Contains(stdout, "stored receipts/real.jpg") {
		t.Errorf("stdout missing real.jpg:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1 stored, 0 unchanged, 0 conflict(s), 0 error(s)") {
		t.Errorf("stdout missing summary:\n%s", stdout)
	}
}

// TestLedgerImportNoReceiptsDir verifies that a vault with no ledger/receipts
// directory reports 0 stored, 0 unchanged, 0 conflict(s), 0 error(s) and exits 0.
func TestLedgerImportNoReceiptsDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "import", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	want := "0 stored, 0 unchanged, 0 conflict(s), 0 error(s)\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestLedgerImportUsageAndExitCodes pins exit 2 on bad subcommands, extra positional
// arguments, and exit 0 on -h/--help/help.
func TestLedgerImportUsageAndExitCodes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")

	// No subcommands -> exit 2
	_, _, code := captureRun(t, func() int {
		return run([]string{"ledger"})
	})
	if code != 2 {
		t.Errorf("run(ledger) exit %d, want 2", code)
	}

	// Unknown subcommand -> exit 2
	_, _, code = captureRun(t, func() int {
		return run([]string{"ledger", "badsub", "--vault", root})
	})
	if code != 2 {
		t.Errorf("run(ledger badsub) exit %d, want 2", code)
	}

	// A-074-4 (C2): check and sum were unknown in C1 (exit 2, a transitional
	// pin); since C2 they are known subcommands and exit 0 on a clean vault.
	for _, sub := range []string{"check", "sum"} {
		_, _, code = captureRun(t, func() int {
			return run([]string{"ledger", sub, "--vault", root})
		})
		if code != 0 {
			t.Errorf("run(ledger %s) exit %d, want 0 (known subcommand, clean vault)", sub, code)
		}
	}

	// Unexpected argument to import -> exit 2
	_, _, code = captureRun(t, func() int {
		return run([]string{"ledger", "import", "extra", "--vault", root})
	})
	if code != 2 {
		t.Errorf("run(ledger import extra) exit %d, want 2", code)
	}

	// Unknown flag to import -> exit 2
	_, _, code = captureRun(t, func() int {
		return run([]string{"ledger", "import", "--no-such-flag", "--vault", root})
	})
	if code != 2 {
		t.Errorf("run(ledger import --no-such-flag) exit %d, want 2", code)
	}

	// -h, --help, help -> exit 0
	for _, h := range []string{"-h", "--help", "help"} {
		stdout, _, code := captureRun(t, func() int {
			return run([]string{"ledger", h})
		})
		if code != 0 {
			t.Errorf("run(ledger %s) exit %d, want 0", h, code)
		}
		if !strings.Contains(stdout, "usage: lw ledger import") {
			t.Errorf("run(ledger %s) stdout missing usage:\n%s", h, stdout)
		}
	}
}

// TestLedgerImportHelpRow pins the one --help row in `lw --help` (074 C1).
func TestLedgerImportHelpRow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	stdout, _, code := captureRun(t, func() int {
		return run([]string{"--help"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "ledger import [--dry-run]") {
		t.Errorf("--help lacks `ledger import [--dry-run]`:\n%s", stdout)
	}
}

// TestLedgerImportC4StorageDirLiteralsRefactor pins F13: the 5 literal sites use vault.StorageDir.
func TestLedgerImportC4StorageDirLiteralsRefactor(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "../.."))

	checkSite := func(relPath string, wantSub string, forbidden string) {
		t.Helper()
		fullPath := filepath.Join(repoRoot, relPath)
		b, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
		s := string(b)
		if !strings.Contains(s, wantSub) {
			t.Errorf("%s missing %q", relPath, wantSub)
		}
		if forbidden != "" && strings.Contains(s, forbidden) {
			t.Errorf("%s still contains forbidden literal %q", relPath, forbidden)
		}
	}

	// 1. internal/storage/storage.go lines 72, 87
	checkSite("internal/storage/storage.go", "filepath.Join(s.vaultDir, vault.StorageDir)", `filepath.Join(s.vaultDir, "storage")`)

	// 2. internal/storage/reconcile.go line 175
	checkSite("internal/storage/reconcile.go", "filepath.Join(dir, vault.StorageDir, filepath.FromSlash(name))", `filepath.Join(dir, "storage", filepath.FromSlash(name))`)

	// 3. cmd/lw/cmd_storage.go line 352
	checkSite("cmd/lw/cmd_storage.go", `filepath.Join(vault.StorageDir, filepath.FromSlash(entry.Name))`, `filepath.Join("storage", filepath.FromSlash(entry.Name))`)

	// 4. internal/vaultsync/vaultsync.go line 51
	checkSite("internal/vaultsync/vaultsync.go", `"/" + vault.StorageDir + "/\n"`, `"/storage/\n"`)
}

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/ledger"
	"github.com/awepo-pro/lw/internal/testutil"
)

func dirDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			fmt.Fprintf(h, "D %s\n", filepath.ToSlash(rel))
			return nil
		}
		fmt.Fprintf(h, "F %s %d\n", filepath.ToSlash(rel), info.Size())
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("dirDigest: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestLedgerCollect_K1 tests K1: root 2026-11-10.md = D1, no bookkeeping.md:
// stdout has the collected line and 'collected 1 day note(s), 0 waiting';
// bookkeeping.md == CentralLine + "\n";
// the note is at ledger/2026/11/2026-11-10.md byte-identical and gone from the root;
// ledger report 2026-11 --today 2026-11-11 then shows spent 118.00 (4 item(s) on 1 day(s)).
func TestLedgerCollect_K1(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	d1Content := "44 (jollibee)\nkfc 45\n23 + bus 6 #transport\n"
	notePath := filepath.Join(root, "2026-11-10.md")
	if err := os.WriteFile(notePath, []byte(d1Content), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}

	wantCollected := "collected 2026-11-10: 44 (jollibee) + 45 (kfc) + 23 + 6 (bus) #transport -> ledger/2026/11/2026-11-10.md\n"
	if !strings.Contains(stdout, wantCollected) {
		t.Errorf("stdout missing collected line:\ngot: %q\nwant: %q", stdout, wantCollected)
	}
	wantSummary := "collected 1 day note(s), 0 waiting\n"
	if !strings.HasSuffix(stdout, wantSummary) {
		t.Errorf("stdout missing summary line:\ngot: %q\nwant suffix: %q", stdout, wantSummary)
	}

	// Verify bookkeeping.md
	bkPath := filepath.Join(root, ledger.LooseFile)
	bkData, err := os.ReadFile(bkPath)
	if err != nil {
		t.Fatalf("read bookkeeping.md: %v", err)
	}
	wantBk := "2026-11-10, 44 (jollibee) + 45 (kfc) + 23 + 6 (bus) #transport [[2026-11-10]]\n"
	if string(bkData) != wantBk {
		t.Errorf("bookkeeping.md content mismatch:\ngot:  %q\nwant: %q", string(bkData), wantBk)
	}

	// Note moved to ledger/2026/11/2026-11-10.md
	archPath := filepath.Join(root, "ledger", "2026", "11", "2026-11-10.md")
	archData, err := os.ReadFile(archPath)
	if err != nil {
		t.Fatalf("read archive note: %v", err)
	}
	if string(archData) != d1Content {
		t.Errorf("archive note content mismatch")
	}
	if _, err := os.Stat(notePath); !os.IsNotExist(err) {
		t.Errorf("original note still exists at root")
	}

	// Report check
	stdoutRep, stderrRep, codeRep := captureRun(t, func() int {
		return run([]string{"ledger", "report", "2026-11", "--today", "2026-11-11", "--vault", root})
	})
	if codeRep != 0 {
		t.Fatalf("report exit code = %d, stderr=%q", codeRep, stderrRep)
	}
	if !strings.Contains(stdoutRep, "spent 118.00 (4 item(s) on 1 day(s))") {
		t.Errorf("report output mismatch:\n%s", stdoutRep)
	}
}

// TestLedgerCollect_K2 tests K2: run twice: the second run prints 'collected 0 day note(s), 0 waiting';
// bookkeeping.md unchanged.
func TestLedgerCollect_K2(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	d1Content := "44 (jollibee)\nkfc 45\n23 + bus 6 #transport\n"
	notePath := filepath.Join(root, "2026-11-10.md")
	if err := os.WriteFile(notePath, []byte(d1Content), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	// First run
	code1 := run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	if code1 != 0 {
		t.Fatalf("first run exit = %d", code1)
	}
	bkPath := filepath.Join(root, ledger.LooseFile)
	bkBefore, err := os.ReadFile(bkPath)
	if err != nil {
		t.Fatalf("read bk: %v", err)
	}

	// Second run
	stdout, stderr, code2 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	})
	if code2 != 0 {
		t.Fatalf("second run exit = %d, stderr=%q", code2, stderr)
	}
	wantSummary := "collected 0 day note(s), 0 waiting\n"
	if stdout != wantSummary {
		t.Errorf("second run stdout mismatch: got %q, want %q", stdout, wantSummary)
	}

	bkAfter, err := os.ReadFile(bkPath)
	if err != nil {
		t.Fatalf("read bk after: %v", err)
	}
	if string(bkBefore) != string(bkAfter) {
		t.Errorf("bookkeeping.md changed between runs")
	}
}

// TestLedgerCollect_K3 tests K3: crash replay: bookkeeping.md already has the line with the link
// and the note is still at the root -> the note is archived, no second line.
func TestLedgerCollect_K3(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	d1Content := "44 (jollibee)\nkfc 45\n23 + bus 6 #transport\n"
	notePath := filepath.Join(root, "2026-11-10.md")
	if err := os.WriteFile(notePath, []byte(d1Content), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	// Pre-populate bookkeeping.md with the line containing [[2026-11-10]]
	bkLine := "2026-11-10, 44 (jollibee) + 45 (kfc) + 23 + 6 (bus) #transport [[2026-11-10]]\n"
	bkPath := filepath.Join(root, ledger.LooseFile)
	if err := os.WriteFile(bkPath, []byte(bkLine), 0o644); err != nil {
		t.Fatalf("write bookkeeping.md: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q stdout=%q", code, stderr, stdout)
	}

	// Note should be archived
	archPath := filepath.Join(root, "ledger", "2026", "11", "2026-11-10.md")
	if _, err := os.Stat(archPath); err != nil {
		t.Fatalf("archive note missing: %v", err)
	}
	if _, err := os.Stat(notePath); !os.IsNotExist(err) {
		t.Errorf("original note not removed from root")
	}

	// bookkeeping.md should still only have the single line
	bkAfter, err := os.ReadFile(bkPath)
	if err != nil {
		t.Fatalf("read bk: %v", err)
	}
	if string(bkAfter) != bkLine {
		t.Errorf("bookkeeping.md got extra line: %q", string(bkAfter))
	}
}

// TestLedgerCollect_K4 tests K4: a note with 'bus 26 6': waiting line with the issue, exit 0,
// nothing moved, bookkeeping.md not created.
// Then --day 2026-11-10 --line "6 (bus 26)" collects it: the line 2026-11-10, 6 (bus 26) [[2026-11-10]].
// --line "abc" -> exit 2, nothing changed.
// --day naming no note -> exit 2.
func TestLedgerCollect_K4(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	notePath := filepath.Join(root, "2026-11-10.md")
	if err := os.WriteFile(notePath, []byte("bus 26 6\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	// Pass 1: normal collect -> waiting
	stdout1, stderr1, code1 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	})
	if code1 != 0 {
		t.Fatalf("pass 1 code = %d, stderr=%q", code1, stderr1)
	}
	if !strings.Contains(stdout1, "waiting 2026-11-10.md: line 1: two numbers in \"bus 26 6\" (write 23 + 45, or put the note in parentheses)") {
		t.Errorf("stdout1 missing waiting issue line:\n%s", stdout1)
	}
	if !strings.Contains(stdout1, "collected 0 day note(s), 1 waiting") {
		t.Errorf("stdout1 missing summary:\n%s", stdout1)
	}
	bkPath := filepath.Join(root, ledger.LooseFile)
	if _, err := os.Stat(bkPath); !os.IsNotExist(err) {
		t.Errorf("bookkeeping.md should not be created")
	}

	// Pass 2: --line "abc" -> exit 2, nothing changed
	_, stderr2, code2 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--day", "2026-11-10", "--line", "abc"})
	})
	if code2 != 2 {
		t.Errorf("pass 2 code = %d, want 2; stderr=%q", code2, stderr2)
	}

	// Pass 3: --day naming no note -> exit 2
	_, stderr3, code3 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--day", "2026-11-20", "--line", "6 (bus)"})
	})
	if code3 != 2 {
		t.Errorf("pass 3 code = %d, want 2; stderr=%q", code3, stderr3)
	}

	// Pass 4: --day 2026-11-10 --line "6 (bus 26)" -> collects it
	stdout4, stderr4, code4 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--day", "2026-11-10", "--line", "6 (bus 26)"})
	})
	if code4 != 0 {
		t.Fatalf("pass 4 code = %d, stderr=%q stdout=%q", code4, stderr4, stdout4)
	}
	wantBk := "2026-11-10, 6 (bus 26) [[2026-11-10]]\n"
	bkData, err := os.ReadFile(bkPath)
	if err != nil {
		t.Fatalf("read bk: %v", err)
	}
	if string(bkData) != wantBk {
		t.Errorf("bk content mismatch: got %q, want %q", string(bkData), wantBk)
	}
}

// TestLedgerCollect_K5 tests K5: a receipt-only note + r.jpg at the root: waiting 'only a receipt image';
// --day ... --line "88 (dinner)" moves the note AND r.jpg to ledger/receipts/2026/11/r.jpg,
// prints (1 receipt(s)) and the import hint.
func TestLedgerCollect_K5(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	notePath := filepath.Join(root, "2026-11-10.md")
	if err := os.WriteFile(notePath, []byte("![[r.jpg]]\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	imgPath := filepath.Join(root, "r.jpg")
	imgBytes := []byte("fake-jpeg-data")
	if err := os.WriteFile(imgPath, imgBytes, 0o644); err != nil {
		t.Fatalf("write img: %v", err)
	}

	// Pass 1: normal collect -> waiting only a receipt image
	stdout1, stderr1, code1 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	})
	if code1 != 0 {
		t.Fatalf("pass 1 code = %d, stderr=%q", code1, stderr1)
	}
	if !strings.Contains(stdout1, "waiting 2026-11-10.md: only a receipt image") {
		t.Errorf("stdout1 missing waiting receipt image line:\n%s", stdout1)
	}

	// Pass 2: manual line collection
	stdout2, stderr2, code2 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--day", "2026-11-10", "--line", "88 (dinner)"})
	})
	if code2 != 0 {
		t.Fatalf("pass 2 code = %d, stderr=%q stdout=%q", code2, stderr2, stdout2)
	}
	if !strings.Contains(stdout2, "(1 receipt(s))") {
		t.Errorf("stdout2 missing receipt count:\n%s", stdout2)
	}
	if !strings.Contains(stdout2, "run lw ledger import to store the receipts so lw sync carries them") {
		t.Errorf("stdout2 missing import hint:\n%s", stdout2)
	}

	// Verify r.jpg moved to ledger/receipts/2026/11/r.jpg
	archImgPath := filepath.Join(root, "ledger", "receipts", "2026", "11", "r.jpg")
	data, err := os.ReadFile(archImgPath)
	if err != nil {
		t.Fatalf("read archived receipt: %v", err)
	}
	if string(data) != string(imgBytes) {
		t.Errorf("archived receipt content mismatch")
	}
	if _, err := os.Stat(imgPath); !os.IsNotExist(err) {
		t.Errorf("original r.jpg still at root")
	}
}

// TestLedgerCollect_K6 tests K6: an existing different archive file -> waiting, nothing changed.
// An identical one -> source removed, line appended once.
func TestLedgerCollect_K6(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	notePath := filepath.Join(root, "2026-11-10.md")
	noteContent := "44 (kfc)\n"
	if err := os.WriteFile(notePath, []byte(noteContent), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	archDir := filepath.Join(root, "ledger", "2026", "11")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatalf("mkdir arch: %v", err)
	}
	archFile := filepath.Join(archDir, "2026-11-10.md")
	if err := os.WriteFile(archFile, []byte("different content\n"), 0o644); err != nil {
		t.Fatalf("write arch: %v", err)
	}

	// Case A: different archive file -> waiting
	stdout1, stderr1, code1 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	})
	if code1 != 0 {
		t.Fatalf("case A code = %d, stderr=%q", code1, stderr1)
	}
	if !strings.Contains(stdout1, "waiting 2026-11-10.md: the archive already holds a different 2026-11-10.md") {
		t.Errorf("stdout1 missing archive conflict line:\n%s", stdout1)
	}
	if _, err := os.Stat(notePath); os.IsNotExist(err) {
		t.Errorf("source note should not be removed on conflict")
	}

	// Case B: identical archive file -> source removed, line appended once
	if err := os.WriteFile(archFile, []byte(noteContent), 0o644); err != nil {
		t.Fatalf("write identical arch: %v", err)
	}
	_, stderr2, code2 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	})
	if code2 != 0 {
		t.Fatalf("case B code = %d, stderr=%q", code2, stderr2)
	}
	if _, err := os.Stat(notePath); !os.IsNotExist(err) {
		t.Errorf("source note should be removed when identical file in archive")
	}
	bkPath := filepath.Join(root, ledger.LooseFile)
	bkData, err := os.ReadFile(bkPath)
	if err != nil {
		t.Fatalf("read bk: %v", err)
	}
	wantBk := "2026-11-10, 44 (kfc) [[2026-11-10]]\n"
	if string(bkData) != wantBk {
		t.Errorf("bk content mismatch: got %q, want %q", string(bkData), wantBk)
	}
}

// TestLedgerCollect_K7 tests K7: a note dated --today is skipped; --include-today collects it;
// a future one is skipped.
func TestLedgerCollect_K7(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	todayNote := filepath.Join(root, "2026-11-10.md")
	futureNote := filepath.Join(root, "2026-11-12.md")
	if err := os.WriteFile(todayNote, []byte("10\n"), 0o644); err != nil {
		t.Fatalf("write today note: %v", err)
	}
	if err := os.WriteFile(futureNote, []byte("20\n"), 0o644); err != nil {
		t.Fatalf("write future note: %v", err)
	}

	// Run without --include-today
	stdout1, stderr1, code1 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-10"})
	})
	if code1 != 0 {
		t.Fatalf("run 1 code = %d, stderr=%q", code1, stderr1)
	}
	if !strings.Contains(stdout1, "skipped 2026-11-10.md: today (use --include-today)") {
		t.Errorf("stdout1 missing today skipped line:\n%s", stdout1)
	}
	if !strings.Contains(stdout1, "skipped 2026-11-12.md: future date") {
		t.Errorf("stdout1 missing future skipped line:\n%s", stdout1)
	}

	// Run with --include-today
	stdout2, stderr2, code2 := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-10", "--include-today"})
	})
	if code2 != 0 {
		t.Fatalf("run 2 code = %d, stderr=%q", code2, stderr2)
	}
	if !strings.Contains(stdout2, "collected 2026-11-10: 10 -> ledger/2026/11/2026-11-10.md") {
		t.Errorf("stdout2 missing collected line:\n%s", stdout2)
	}
	if !strings.Contains(stdout2, "skipped 2026-11-12.md: future date") {
		t.Errorf("stdout2 missing future skipped line:\n%s", stdout2)
	}
}

// TestLedgerCollect_K8 tests K8: --dry-run: output names what would happen; the tree digest is unchanged.
func TestLedgerCollect_K8(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	notePath := filepath.Join(root, "2026-11-10.md")
	if err := os.WriteFile(notePath, []byte("44 (kfc)\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	beforeDigest := dirDigest(t, root)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11", "--dry-run"})
	})
	if code != 0 {
		t.Fatalf("dry-run exit = %d, stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "would collect 2026-11-10: 44 (kfc) -> ledger/2026/11/2026-11-10.md") {
		t.Errorf("dry-run stdout missing would collect:\n%s", stdout)
	}

	afterDigest := dirDigest(t, root)
	if beforeDigest != afterDigest {
		t.Errorf("dir digest changed during --dry-run: before=%s, after=%s", beforeDigest, afterDigest)
	}
}

// TestLedgerCollect_K9 tests K9: an appended line lands after existing content that has no final newline,
// on its own line; existing bytes of bookkeeping.md are otherwise untouched (prefix compare).
func TestLedgerCollect_K9(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	notePath := filepath.Join(root, "2026-11-10.md")
	if err := os.WriteFile(notePath, []byte("44 (kfc)\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	existingContent := "Nov - \u5bb6\u7528 4000" // no final newline!
	bkPath := filepath.Join(root, ledger.LooseFile)
	if err := os.WriteFile(bkPath, []byte(existingContent), 0o644); err != nil {
		t.Fatalf("write bk: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q stdout=%q", code, stderr, stdout)
	}

	bkData, err := os.ReadFile(bkPath)
	if err != nil {
		t.Fatalf("read bk: %v", err)
	}
	wantBk := existingContent + "\n2026-11-10, 44 (kfc) [[2026-11-10]]\n"
	if string(bkData) != wantBk {
		t.Errorf("bk content mismatch:\ngot:  %q\nwant: %q", string(bkData), wantBk)
	}
	if !strings.HasPrefix(string(bkData), existingContent) {
		t.Errorf("bk data did not preserve original prefix")
	}
}

// TestLedgerCollect_K10 tests K10: an image target '../outside.jpg' or a symlink is 'no file for image',
// nothing outside the vault is touched.
func TestLedgerCollect_K10(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	parent := filepath.Dir(root)
	outsideFile := filepath.Join(parent, "outside.jpg")
	outsideBytes := []byte("outside-vault-data")
	if err := os.WriteFile(outsideFile, outsideBytes, 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	defer os.Remove(outsideFile)

	noteContent := "44 (kfc)\n![[../outside.jpg]]\n"
	notePath := filepath.Join(root, "2026-11-10.md")
	if err := os.WriteFile(notePath, []byte(noteContent), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "no file for image ../outside.jpg") {
		t.Errorf("stdout missing 'no file for image ../outside.jpg':\n%s", stdout)
	}

	// Verify outside file is still there and untouched
	data, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != string(outsideBytes) {
		t.Errorf("outside file content changed!")
	}
}

// TestLedgerCollect_K11 tests K11: day notes in ledger/ (not deeper) are candidates;
// ledger/2026/11/2026-11-01.md is not.
func TestLedgerCollect_K11(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	ledgerDir := filepath.Join(root, "ledger")
	if err := os.MkdirAll(ledgerDir, 0o755); err != nil {
		t.Fatalf("mkdir ledger: %v", err)
	}

	// Candidate in ledger/
	inLedger := filepath.Join(ledgerDir, "2026-11-09.md")
	if err := os.WriteFile(inLedger, []byte("15 (bus)\n"), 0o644); err != nil {
		t.Fatalf("write inLedger: %v", err)
	}

	// Deep file in ledger/2026/11/2026-11-01.md (already archived)
	deepDir := filepath.Join(ledgerDir, "2026", "11")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatalf("mkdir deep: %v", err)
	}
	deepFile := filepath.Join(deepDir, "2026-11-01.md")
	if err := os.WriteFile(deepFile, []byte("99 (deep)\n"), 0o644); err != nil {
		t.Fatalf("write deepFile: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "collect", "--vault", root, "--today", "2026-11-11"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "collected 2026-11-09: 15 (bus) -> ledger/2026/11/2026-11-09.md") {
		t.Errorf("stdout missing collection of ledger/2026-11-09.md:\n%s", stdout)
	}
	if strings.Contains(stdout, "2026-11-01") {
		t.Errorf("stdout should not collect deeper file 2026-11-01.md:\n%s", stdout)
	}
	if !strings.Contains(stdout, "collected 1 day note(s), 0 waiting") {
		t.Errorf("summary mismatch:\n%s", stdout)
	}
}

// TestLedgerCollect_K12 tests K12: ledger report prints the 'not collected yet' line
// with 1 candidate and omits it with none.
func TestLedgerCollect_K12(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	// Initially no candidates
	stdout1, stderr1, code1 := captureRun(t, func() int {
		return run([]string{"ledger", "report", "2026-11", "--today", "2026-11-11", "--vault", root})
	})
	if code1 != 0 {
		t.Fatalf("code1 = %d, stderr=%q", code1, stderr1)
	}
	if strings.Contains(stdout1, "not collected yet") {
		t.Errorf("report with 0 candidates should omit not collected yet line:\n%s", stdout1)
	}

	// Add 1 candidate dated today or earlier
	notePath := filepath.Join(root, "2026-11-10.md")
	if err := os.WriteFile(notePath, []byte("44 (kfc)\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	stdout2, stderr2, code2 := captureRun(t, func() int {
		return run([]string{"ledger", "report", "2026-11", "--today", "2026-11-11", "--vault", root})
	})
	if code2 != 0 {
		t.Fatalf("code2 = %d, stderr=%q", code2, stderr2)
	}
	wantNoteLine := "1 day note(s) not collected yet: run lw ledger collect"
	if !strings.Contains(stdout2, wantNoteLine) {
		t.Errorf("report with 1 candidate missing hint line:\n%s", stdout2)
	}
}

// TestLedgerCollect_K13 tests K13: usage/help/completion list collect.
func TestLedgerCollect_K13(t *testing.T) {
	// lw ledger usage
	stdoutUsage, _, codeUsage := captureRun(t, func() int {
		return run([]string{"ledger", "--help"})
	})
	if codeUsage != 0 {
		t.Errorf("codeUsage = %d", codeUsage)
	}
	if !strings.Contains(stdoutUsage, "lw ledger collect") {
		t.Errorf("ledger --help missing 'lw ledger collect':\n%s", stdoutUsage)
	}

	// lw --help
	stdoutHelp, _, codeHelp := captureRun(t, func() int {
		return run([]string{"--help"})
	})
	if codeHelp != 0 {
		t.Errorf("codeHelp = %d", codeHelp)
	}
	if !strings.Contains(stdoutHelp, "ledger collect") {
		t.Errorf("main --help missing 'ledger collect':\n%s", stdoutHelp)
	}

	// lw completion bash
	stdoutComp, _, codeComp := captureRun(t, func() int {
		return run([]string{"completion", "bash"})
	})
	if codeComp != 0 {
		t.Errorf("codeComp = %d", codeComp)
	}
	if !strings.Contains(stdoutComp, "collect") {
		t.Errorf("completion bash missing 'collect':\n%s", stdoutComp)
	}
}

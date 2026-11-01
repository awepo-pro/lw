package main

// cmd_ledger_074_test.go pins F12 (lw ledger check and sum) and I7
// (read-only invariant: check and sum write nothing anywhere).

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// setupFixtureMonth creates a vault with a hand-written month:
// - two valid day files (2026-10-30.md, 2026-10-31.md)
// - one ledger/notes.md (layout issue)
// - one id-date mismatch (in 2026-10-31.md line 2)
// - one duplicate id across two files (in 2026-10-30.md line 1 and ledger/notes.md line 1: offline two-device case)
// - one bad line (in 2026-10-31.md line 3)
func setupFixtureMonth(t *testing.T) string {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")

	dayDir := filepath.Join(root, vault.LedgerDir, "2026", "10")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("mkdir ledger: %v", err)
	}

	// 1. Valid day file 1: 2026-10-30.md
	day1 := `- (id:: 20261030-1200-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash) lunch
- (id:: 20261030-1400-a) (amt:: -10.00) (cur:: USD) (cat:: books) (acct:: visa) ebook
`
	if err := os.WriteFile(filepath.Join(dayDir, "2026-10-30.md"), []byte(day1), 0o644); err != nil {
		t.Fatalf("write day1: %v", err)
	}

	// 2. Valid day file 2: 2026-10-31.md with 1 id-date mismatch and 1 bad line
	day2 := `- (id:: 20261031-0900-a) (amt:: -15.00) (cur:: HKD) (cat:: transit) (acct:: octopus) subway
- (id:: 20261030-0800-a) (amt:: -30.00) (cur:: HKD) (cat:: coffee) (acct:: cash) espresso
- (id:: 20261031-1000-a) (amt:: 1.5) (cur:: HKD) (cat:: food) (acct:: cash) snack
`
	if err := os.WriteFile(filepath.Join(dayDir, "2026-10-31.md"), []byte(day2), 0o644); err != nil {
		t.Fatalf("write day2: %v", err)
	}

	// 3. Layout issue: ledger/notes.md with 1 duplicate ID (from 2026-10-30.md:1)
	notes := `- (id:: 20261030-1200-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash) duplicate from phone B
`
	if err := os.WriteFile(filepath.Join(root, vault.LedgerDir, "notes.md"), []byte(notes), 0o644); err != nil {
		t.Fatalf("write notes.md: %v", err)
	}

	return root
}

// TestLedgerCheckFixtureWithIssues pins F12 check on a vault with issues:
// exact issue lines, summary line, exit code 1.
func TestLedgerCheckFixtureWithIssues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := setupFixtureMonth(t)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "check", "--vault", root})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr, got %q", stderr)
	}

	wantStdout := "ledger/2026/10/2026-10-31.md:2: id date 2026-10-30 is not the file date 2026-10-31\n" +
		"ledger/2026/10/2026-10-31.md:3: bad line: invalid amount: \"1.5\"\n" +
		"ledger/notes.md:0: not a ledger day file (want ledger/YYYY/MM/YYYY-MM-DD.md)\n" +
		"ledger/notes.md:1: duplicate id 20261030-1200-a (first at ledger/2026/10/2026-10-30.md:1)\n" +
		"4 issue(s); 5 valid transaction(s) in 3 file(s)\n"

	if stdout != wantStdout {
		t.Errorf("stdout mismatch:\ngot:\n%s\nwant:\n%s", stdout, wantStdout)
	}
}

// TestLedgerCheckCleanVault pins F12 check on a clean vault (exit 0, ok summary).
func TestLedgerCheckCleanVault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	dayDir := filepath.Join(root, vault.LedgerDir, "2026", "10")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	day := "- (id:: 20261031-1200-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash) lunch\n"
	if err := os.WriteFile(filepath.Join(dayDir, "2026-10-31.md"), []byte(day), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "check", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr, got %q", stderr)
	}
	want := "ok: 1 transaction(s) in 1 file(s)\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestLedgerSumFixtureWithIssues pins F12 sum:
// tabwriter aligned table, currency totals, stderr warning when issues exist, exit 0.
func TestLedgerSumFixtureWithIssues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := setupFixtureMonth(t)

	// 1. With month filter 2026-10
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "sum", "2026-10", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}

	wantWarn := "lw: ledger: 4 issue(s) found; totals may be wrong - run lw ledger check\n"
	if stderr != wantWarn {
		t.Errorf("stderr = %q, want %q", stderr, wantWarn)
	}

	wantStdout := "HKD  coffee   -30.00   (1)\n" +
		"HKD  food     -90.00   (2)\n" +
		"HKD  transit  -15.00   (1)\n" +
		"HKD  total    -135.00  (4)\n" +
		"USD  books    -10.00   (1)\n" +
		"USD  total    -10.00   (1)\n"

	if stdout != wantStdout {
		t.Errorf("stdout mismatch:\ngot:\n%s\nwant:\n%s", stdout, wantStdout)
	}

	// 2. Without month filter (same transactions since all fall in 2026-10)
	stdoutNoFilter, stderrNoFilter, codeNoFilter := captureRun(t, func() int {
		return run([]string{"ledger", "sum", "--vault", root})
	})
	if codeNoFilter != 0 {
		t.Fatalf("sum without filter exit %d, want 0", codeNoFilter)
	}
	if stderrNoFilter != wantWarn {
		t.Errorf("stderrNoFilter = %q, want %q", stderrNoFilter, wantWarn)
	}
	if stdoutNoFilter != wantStdout {
		t.Errorf("stdoutNoFilter mismatch:\ngot:\n%s\nwant:\n%s", stdoutNoFilter, wantStdout)
	}

	// 3. Month filter with no matching transactions -> "no transactions", exit 0, warning still present
	stdoutEmptyMonth, stderrEmptyMonth, codeEmptyMonth := captureRun(t, func() int {
		return run([]string{"ledger", "sum", "2026-11", "--vault", root})
	})
	if codeEmptyMonth != 0 {
		t.Fatalf("sum 2026-11 exit %d, want 0", codeEmptyMonth)
	}
	if stderrEmptyMonth != wantWarn {
		t.Errorf("stderrEmptyMonth = %q, want %q", stderrEmptyMonth, wantWarn)
	}
	if stdoutEmptyMonth != "no transactions\n" {
		t.Errorf("stdoutEmptyMonth = %q, want \"no transactions\\n\"", stdoutEmptyMonth)
	}
}

// TestLedgerSumCleanVault pins F12 sum on clean vault: no warning on stderr.
func TestLedgerSumCleanVault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	dayDir := filepath.Join(root, vault.LedgerDir, "2026", "10")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	day := "- (id:: 20261031-1200-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash) lunch\n"
	if err := os.WriteFile(filepath.Join(dayDir, "2026-10-31.md"), []byte(day), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "sum", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr, got %q", stderr)
	}
	want := "HKD  food   -45.00  (1)\n" +
		"HKD  total  -45.00  (1)\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestLedgerEmptyVault pins check/sum on an empty vault (no ledger dir).
func TestLedgerEmptyVault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")

	// check on empty vault -> ok: 0 transaction(s) in 0 file(s), exit 0
	stdoutCheck, stderrCheck, codeCheck := captureRun(t, func() int {
		return run([]string{"ledger", "check", "--vault", root})
	})
	if codeCheck != 0 {
		t.Fatalf("check empty vault exit %d, want 0; stderr=%q", codeCheck, stderrCheck)
	}
	if stderrCheck != "" {
		t.Errorf("expected empty stderr from check, got %q", stderrCheck)
	}
	if stdoutCheck != "ok: 0 transaction(s) in 0 file(s)\n" {
		t.Errorf("stdoutCheck = %q, want \"ok: 0 transaction(s) in 0 file(s)\\n\"", stdoutCheck)
	}

	// sum on empty vault -> no transactions, exit 0
	stdoutSum, stderrSum, codeSum := captureRun(t, func() int {
		return run([]string{"ledger", "sum", "--vault", root})
	})
	if codeSum != 0 {
		t.Fatalf("sum empty vault exit %d, want 0; stderr=%q", codeSum, stderrSum)
	}
	if stderrSum != "" {
		t.Errorf("expected empty stderr from sum, got %q", stderrSum)
	}
	if stdoutSum != "no transactions\n" {
		t.Errorf("stdoutSum = %q, want \"no transactions\\n\"", stdoutSum)
	}
}

// TestLedgerUsageAndExitCodes074 pins exit codes 2 on usage errors:
// sum with bad month (2026-13, 2026-1, etc.), extra positional arguments, bad flags.
func TestLedgerUsageAndExitCodes074(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")

	// sum with invalid months -> exit 2
	badMonths := []string{"2026-13", "2026-1", "2026-00", "2026-123", "2026", "invalid", "2026-0a"}
	for _, bm := range badMonths {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ledger", "sum", bm, "--vault", root})
		})
		if code != 2 {
			t.Errorf("sum with bad month %q: exit %d, want 2; stderr=%q", bm, code, stderr)
		}
	}

	// sum with multiple positional arguments -> exit 2
	_, _, code := captureRun(t, func() int {
		return run([]string{"ledger", "sum", "2026-10", "extra", "--vault", root})
	})
	if code != 2 {
		t.Errorf("sum with extra argument: exit %d, want 2", code)
	}

	// sum with unknown flag -> exit 2
	_, _, code = captureRun(t, func() int {
		return run([]string{"ledger", "sum", "--unknown-flag", "--vault", root})
	})
	if code != 2 {
		t.Errorf("sum with unknown flag: exit %d, want 2", code)
	}

	// check with unexpected positional arguments -> exit 2
	_, _, code = captureRun(t, func() int {
		return run([]string{"ledger", "check", "extra", "--vault", root})
	})
	if code != 2 {
		t.Errorf("check with extra argument: exit %d, want 2", code)
	}

	// check with unknown flag -> exit 2
	_, _, code = captureRun(t, func() int {
		return run([]string{"ledger", "check", "--unknown-flag", "--vault", root})
	})
	if code != 2 {
		t.Errorf("check with unknown flag: exit %d, want 2", code)
	}
}

// TestLedgerHelpRowsPinsAllThree pins that lw --help contains all three ledger rows.
func TestLedgerHelpRowsPinsAllThree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	stdout, _, code := captureRun(t, func() int {
		return run([]string{"--help"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	for _, sub := range []string{"ledger check", "ledger sum [YYYY-MM]", "ledger import [--dry-run]"} {
		if !strings.Contains(stdout, sub) {
			t.Errorf("--help missing %q;\ngot:\n%s", sub, stdout)
		}
	}
}

type fileSnapshotEntry struct {
	content []byte
	mtime   time.Time
	size    int64
}

func takeVaultSnapshot(t *testing.T, root string) map[string]fileSnapshotEntry {
	t.Helper()
	snap := make(map[string]fileSnapshotEntry)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snap[filepath.ToSlash(rel)] = fileSnapshotEntry{
			content: data,
			mtime:   info.ModTime(),
			size:    info.Size(),
		}
		return nil
	})
	if err != nil {
		t.Fatalf("take snapshot: %v", err)
	}
	return snap
}

func assertVaultSnapshotsIdentical(t *testing.T, before, after map[string]fileSnapshotEntry) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("file count mismatch: before %d, after %d", len(before), len(after))
	}
	for path, b := range before {
		a, ok := after[path]
		if !ok {
			t.Fatalf("file %s removed after command", path)
		}
		if !bytes.Equal(b.content, a.content) {
			t.Fatalf("file %s content modified", path)
		}
		if !b.mtime.Equal(a.mtime) {
			t.Fatalf("file %s mtime changed: before %v, after %v", path, b.mtime, a.mtime)
		}
		if b.size != a.size {
			t.Fatalf("file %s size changed: before %d, after %d", path, b.size, a.size)
		}
	}
}

// TestLedgerI7CheckAndSumSnapshot pins invariant I7:
// lw ledger check and sum write nothing anywhere.
// Snapshots every file's bytes + mtime before and after.
func TestLedgerI7CheckAndSumSnapshot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := setupFixtureMonth(t)

	// Snapshot before check
	snapBeforeCheck := takeVaultSnapshot(t, root)

	// Run check
	_ = run([]string{"ledger", "check", "--vault", root})

	// Snapshot after check
	snapAfterCheck := takeVaultSnapshot(t, root)
	assertVaultSnapshotsIdentical(t, snapBeforeCheck, snapAfterCheck)

	// Run sum
	_ = run([]string{"ledger", "sum", "2026-10", "--vault", root})

	// Snapshot after sum
	snapAfterSum := takeVaultSnapshot(t, root)
	assertVaultSnapshotsIdentical(t, snapBeforeCheck, snapAfterSum)

	// Run sum with no filter
	_ = run([]string{"ledger", "sum", "--vault", root})

	// Snapshot after sum no filter
	snapAfterSumNoFilter := takeVaultSnapshot(t, root)
	assertVaultSnapshotsIdentical(t, snapBeforeCheck, snapAfterSumNoFilter)
}

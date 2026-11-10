package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/ledger"
	"github.com/awepo-pro/lw/internal/testutil"
)

const realFixture082 = "Nov - \u5bb6\u7528 4000 \n\n2026-11-02, 44 (jollibee)\n2026-11-03, 25\n" +
	"2026-11-04, 23 + 45 (kfc) + 32 (\u8461\u64bb)\n2026-11-05, 29 + 30 + 49.5 (7\u4ed4)\n\n"

func normalizeOutput(s string) string {
	lines := strings.Split(s, "\n")
	var norm []string
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		words := strings.Fields(l)
		norm = append(norm, strings.Join(words, " "))
	}
	for len(norm) > 0 && norm[len(norm)-1] == "" {
		norm = norm[:len(norm)-1]
	}
	return strings.Join(norm, "\n")
}

// TestLedger082_C1_CheckReal tests C1: REAL at the vault root: ledger check -> stdout ok: 9 transaction(s) in 1 file(s)\n, exit 0.
func TestLedger082_C1_CheckReal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	if err := os.WriteFile(filepath.Join(root, ledger.LooseFile), []byte(realFixture082), 0o644); err != nil {
		t.Fatalf("write bookkeeping.md: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "check", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	wantStdout := "ok: 9 transaction(s) in 1 file(s)\n"
	if stdout != wantStdout {
		t.Errorf("stdout mismatch: got %q, want %q", stdout, wantStdout)
	}
}

// TestLedger082_C2_CheckRealWithIssue tests C2: REAL + a line lunch -> exit 1, stdout has
// bookkeeping.md:<n>: not understood and 1 issue(s); 9 valid transaction(s) in 1 file(s).
func TestLedger082_C2_CheckRealWithIssue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	content := realFixture082 + "lunch\n"
	if err := os.WriteFile(filepath.Join(root, ledger.LooseFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write bookkeeping.md: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "check", "--vault", root})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "bookkeeping.md:8: not understood") {
		t.Errorf("stdout missing 'bookkeeping.md:8: not understood': %q", stdout)
	}
	if !strings.Contains(stdout, "1 issue(s); 9 valid transaction(s) in 1 file(s)") {
		t.Errorf("stdout missing summary: %q", stdout)
	}
}

// TestLedger082_C3_CheckWarningOnly tests C3: a duplicate-date warning only -> exit 0, a warning: line, then the ok: line.
func TestLedger082_C3_CheckWarningOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	content := "2026-11-02, 10\n2026-11-02, 20\n"
	if err := os.WriteFile(filepath.Join(root, ledger.LooseFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write bookkeeping.md: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "check", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "bookkeeping.md:2: warning: date 2026-11-02 also on line 1\n") {
		t.Errorf("stdout missing warning line: %q", stdout)
	}
	if !strings.Contains(stdout, "ok: 2 transaction(s) in 1 file(s)\n") {
		t.Errorf("stdout missing ok line: %q", stdout)
	}
}

// TestLedger082_C4_SumReal tests C4: ledger sum 2026-11 on REAL: rows (space-collapsed)
// - uncategorized -277.50 (8), - \u5bb6\u7528 -4000.00 (1), - total -4277.50 (9), sorted by category bytes.
func TestLedger082_C4_SumReal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	if err := os.WriteFile(filepath.Join(root, ledger.LooseFile), []byte(realFixture082), 0o644); err != nil {
		t.Fatalf("write bookkeeping.md: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "sum", "2026-11", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}

	norm := normalizeOutput(stdout)
	wantLines := []string{
		"- uncategorized -277.50 (8)",
		"- \u5bb6\u7528 -4000.00 (1)",
		"- total -4277.50 (9)",
	}
	for _, w := range wantLines {
		if !strings.Contains(norm, w) {
			t.Errorf("stdout missing line %q, got:\n%s", w, norm)
		}
	}
}

// TestLedger082_C5_ReportCLI tests C5: ledger report 2026-11 --today 2026-11-10 on REAL = R1;
// --today nope exits 2; report 2026-13 exits 2.
func TestLedger082_C5_ReportCLI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	if err := os.WriteFile(filepath.Join(root, ledger.LooseFile), []byte(realFixture082), 0o644); err != nil {
		t.Fatalf("write bookkeeping.md: %v", err)
	}

	// Valid report call
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "report", "2026-11", "--today", "2026-11-10", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	want := "2026-11\n" +
		"spent 277.50 (8 item(s) on 4 day(s))\n" +
		"fixed 4000.00 (\u5bb6\u7528 4000.00)\n" +
		"money in 0.00\n" +
		"net out 4277.50\n" +
		"by category:\n" +
		"\u5bb6\u7528 4000.00 (1)\n" +
		"uncategorized 277.50 (8)\n" +
		"average per recorded day 69.38\n" +
		"no entry on 5 day(s): 06 07 08 09 10"
	if normalizeOutput(stdout) != normalizeOutput(want) {
		t.Errorf("report output mismatch:\ngot:\n%s\nwant:\n%s", stdout, want)
	}

	// --today nope -> exit 2
	_, _, codeBadToday := captureRun(t, func() int {
		return run([]string{"ledger", "report", "2026-11", "--today", "nope", "--vault", root})
	})
	if codeBadToday != 2 {
		t.Errorf("exit code for bad today = %d, want 2", codeBadToday)
	}

	// report 2026-13 -> exit 2
	_, _, codeBadMonth := captureRun(t, func() int {
		return run([]string{"ledger", "report", "2026-13", "--vault", root})
	})
	if codeBadMonth != 2 {
		t.Errorf("exit code for bad month = %d, want 2", codeBadMonth)
	}
}

// TestLedger082_C6_NoBookkeepingUnchanged tests C6: no bookkeeping.md: ledger check and ledger sum
// output is byte-identical to before (the existing 074 tests, unedited, are the proof).
func TestLedger082_C6_NoBookkeepingUnchanged(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	stdoutCheck, _, codeCheck := captureRun(t, func() int {
		return run([]string{"ledger", "check", "--vault", root})
	})
	if codeCheck != 0 || stdoutCheck != "ok: 0 transaction(s) in 0 file(s)\n" {
		t.Errorf("check on empty vault mismatch: code=%d stdout=%q", codeCheck, stdoutCheck)
	}

	stdoutSum, _, codeSum := captureRun(t, func() int {
		return run([]string{"ledger", "sum", "--vault", root})
	})
	if codeSum != 0 || stdoutSum != "no transactions\n" {
		t.Errorf("sum on empty vault mismatch: code=%d stdout=%q", codeSum, stdoutSum)
	}
}

// TestLedger082_C7_HelpUsageCompletion tests C7: lw ledger usage lists report;
// lw --help has a ledger report line; lw completion bash offers report after ledger.
func TestLedger082_C7_HelpUsageCompletion(t *testing.T) {
	// lw ledger usage lists report
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ledger", "--help"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "lw ledger report [YYYY-MM] [--today YYYY-MM-DD] [--vault P]") {
		t.Errorf("ledger usage missing 'report': %q", stdout)
	}

	// lw --help has a ledger report line
	stdoutHelp, _, codeHelp := captureRun(t, func() int {
		return run([]string{"--help"})
	})
	if codeHelp != 0 {
		t.Fatalf("exit code = %d, want 0", codeHelp)
	}
	if !strings.Contains(stdoutHelp, "ledger report") {
		t.Errorf("lw --help missing 'ledger report': %q", stdoutHelp)
	}

	// lw completion bash offers report after ledger
	stdoutComp, _, codeComp := captureRun(t, func() int {
		return run([]string{"completion", "bash"})
	})
	if codeComp != 0 {
		t.Fatalf("exit code = %d, want 0", codeComp)
	}
	// Check that bash script contains report in the ledger case
	if !strings.Contains(stdoutComp, "ledger)") || !strings.Contains(stdoutComp, "report") {
		t.Errorf("bash completion script missing 'report' for ledger: %q", stdoutComp)
	}
}

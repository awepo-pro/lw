package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/ledger"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestLedger082F1WarningsDoNotTaintTotals tests F1:
// A vault whose bookkeeping.md has two valid day lines with the same date and nothing else wrong:
// `ledger sum` and `ledger report 2026-11 --today 2026-11-10` exit 0 with EMPTY stderr.
// And with one more line `lunch` the note says "1 issue(s)".
func TestLedger082F1WarningsDoNotTaintTotals(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	content := "2026-11-02, 10\n2026-11-02, 20\n"
	bookkeepingPath := filepath.Join(root, ledger.LooseFile)
	if err := os.WriteFile(bookkeepingPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write bookkeeping.md: %v", err)
	}

	// 1. ledger sum should exit 0 with EMPTY stderr
	_, stderrSum, codeSum := captureRun(t, func() int {
		return run([]string{"ledger", "sum", "--vault", root})
	})
	if codeSum != 0 {
		t.Fatalf("ledger sum exit code = %d, want 0; stderr=%q", codeSum, stderrSum)
	}
	if stderrSum != "" {
		t.Errorf("ledger sum expected empty stderr on warning-only vault, got: %q", stderrSum)
	}

	// 2. ledger report 2026-11 --today 2026-11-10 should exit 0 with EMPTY stderr
	_, stderrReport, codeReport := captureRun(t, func() int {
		return run([]string{"ledger", "report", "2026-11", "--today", "2026-11-10", "--vault", root})
	})
	if codeReport != 0 {
		t.Fatalf("ledger report exit code = %d, want 0; stderr=%q", codeReport, stderrReport)
	}
	if stderrReport != "" {
		t.Errorf("ledger report expected empty stderr on warning-only vault, got: %q", stderrReport)
	}

	// 3. Add one more line `lunch` (a non-warning issue)
	contentWithIssue := content + "lunch\n"
	if err := os.WriteFile(bookkeepingPath, []byte(contentWithIssue), 0o644); err != nil {
		t.Fatalf("write bookkeeping.md with issue: %v", err)
	}

	// ledger sum stderr note should mention "1 issue(s)"
	_, stderrSumWithIssue, codeSumWithIssue := captureRun(t, func() int {
		return run([]string{"ledger", "sum", "--vault", root})
	})
	if codeSumWithIssue != 0 {
		t.Fatalf("ledger sum exit code = %d, want 0; stderr=%q", codeSumWithIssue, stderrSumWithIssue)
	}
	if !strings.Contains(stderrSumWithIssue, "1 issue(s)") {
		t.Errorf("ledger sum stderr expected '1 issue(s)', got: %q", stderrSumWithIssue)
	}

	// ledger report stderr note should mention "1 issue(s)"
	_, stderrReportWithIssue, codeReportWithIssue := captureRun(t, func() int {
		return run([]string{"ledger", "report", "2026-11", "--today", "2026-11-10", "--vault", root})
	})
	if codeReportWithIssue != 0 {
		t.Fatalf("ledger report exit code = %d, want 0; stderr=%q", codeReportWithIssue, stderrReportWithIssue)
	}
	if !strings.Contains(stderrReportWithIssue, "1 issue(s)") {
		t.Errorf("ledger report stderr expected '1 issue(s)', got: %q", stderrReportWithIssue)
	}
}

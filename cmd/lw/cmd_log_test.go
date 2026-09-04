package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestCmdLogShowsFullLifecycle stages, commits, and checks lw log prints
// every event backbone §5.7 D-BF assigns a writer to along that path —
// changeset_opened, op_proposed, commit_begin, commit_end — in that
// order (Filter's oldest-first ordering, backbone §5.7 D-AU).
func TestCmdLogShowsFullLifecycle(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openCreatePageChangeset(t, root, "wiki/concepts/log-target.md", "Log Target")
	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "for the log test"})
	}); code != 0 {
		t.Fatalf("commit: exit = %d, stderr=%q", code, stderr)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"log", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	wantKinds := []string{"changeset_opened", "op_proposed", "commit_begin", "commit_end"}
	if len(lines) != len(wantKinds) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(wantKinds), stdout)
	}
	for i, want := range wantKinds {
		if !strings.Contains(lines[i], " "+want) {
			t.Errorf("line %d = %q, want it to contain kind %q", i, lines[i], want)
		}
	}
}

// TestCmdLogLimit checks --limit N returns the most recent N events,
// still printed oldest-first (backbone §5.7 D-AU: Limit selects the most
// recent N, the result stays oldest-first). With two commits' worth of
// events, --limit 1 must show the SECOND commit_end, not the first.
func TestCmdLogLimit(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	openCreatePageChangeset(t, root, "wiki/concepts/log-limit-a.md", "Log Limit A")
	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "first"})
	}); code != 0 {
		t.Fatalf("first commit: exit = %d, stderr=%q", code, stderr)
	}
	openCreatePageChangeset(t, root, "wiki/concepts/log-limit-b.md", "Log Limit B")
	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "second"})
	}); code != 0 {
		t.Fatalf("second commit: exit = %d, stderr=%q", code, stderr)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"log", "--vault", root, "--limit", "1"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1:\n%s", len(lines), stdout)
	}
	if !strings.Contains(lines[0], "commit=000002") {
		t.Errorf("line = %q, want the SECOND commit (000002), not the first", lines[0])
	}
}

// TestCmdLogRejected checks --rejected restricts output to
// changeset_rejected events only.
func TestCmdLogRejected(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openCreatePageChangeset(t, root, "wiki/concepts/log-rejected.md", "Log Rejected")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	if err := e.Reject("no longer needed"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"log", "--vault", root, "--rejected"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1:\n%s", len(lines), stdout)
	}
	if !strings.Contains(lines[0], "changeset_rejected") {
		t.Errorf("line = %q, want it to contain %q", lines[0], "changeset_rejected")
	}
}

// TestCmdLogBadSinceIsUsageError covers a malformed --since value.
func TestCmdLogBadSinceIsUsageError(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"log", "--vault", root, "--since", "not-a-date"})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

// TestCmdLogBadFlagIsUsageError covers the flag.ContinueOnError path.
func TestCmdLogBadFlagIsUsageError(t *testing.T) {
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"log", "--bogusflag"})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

// TestCmdLogEmptyJournalPrintsNothing checks a freshly opened vault with
// no activity prints no lines and still exits 0.
func TestCmdLogEmptyJournalPrintsNothing(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"log", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

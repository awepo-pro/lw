package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

func TestCmdStatusMinimal(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"status", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "4 pages · 2 raw · 12 tags") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "4 pages · 2 raw · 12 tags")
	}
	if !strings.Contains(stdout, "no open changeset") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "no open changeset")
	}
}

func TestCmdStatusBadFlag(t *testing.T) {
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"status", "--bogusflag"})
	})

	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

func TestCmdStatusUnexpectedArg(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"status", "--vault", root, "extra-arg"})
	})

	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

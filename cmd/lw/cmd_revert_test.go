package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestCmdRevertPrintsSkippedPaths runs the exact shape gate G2 measures
// (backbone §5.8 D-BY/D-CA rule (b), wave-8 entry item 7): committing a
// create_page and reverting it. index.md's line for the created page
// survives — the tombstone is still a vault.Page, so index-sync still
// demands it — and Revert must report that path as skipped, never drop
// it silently.
func TestCmdRevertPrintsSkippedPaths(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openCreatePageChangeset(t, root, "wiki/concepts/revert-target.md", "Revert Target")
	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "add page to revert"})
	}); code != 0 {
		t.Fatalf("commit: exit = %d, stderr=%q", code, stderr)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"revert", "--vault", root, "000001"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "opened cs-") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "opened cs-")
	}
	if !strings.Contains(stdout, "skipped: index.md") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "skipped: index.md")
	}

	// The revert must be committable, not merely proposable (gate G2's
	// own "not just proposable" clause) — this proves the printed
	// skipped-index.md line describes a review-only carve-out, not a
	// broken changeset.
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "commit the revert"})
	})
	if code != 0 {
		t.Fatalf("commit revert: exit = %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "committed 000002") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "committed 000002")
	}
}

// TestCmdRevertNoSkippedPathsPrintsNoSkippedLines checks that a fully
// invertible revert prints no "skipped:" lines at all.
func TestCmdRevertNoSkippedPathsPrintsNoSkippedLines(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	kvCache, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("minimal fixture missing wiki/concepts/kv-cache.md")
	}
	old := string(kvCache.Serialize())
	newContent := strings.Replace(old,
		"- [[flash-attention]]",
		"- [[gpt-4]] — an unrelated edit for TestCmdRevertNoSkippedPathsPrintsNoSkippedLines.\n- [[flash-attention]]",
		1)
	if newContent == old {
		t.Fatal("test setup: replacement did not match kv-cache.md's body")
	}
	if _, err := e.OpenChangeset("patch kv-cache", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(stage.Op{
		Kind:    stage.OpPatchPage,
		Path:    "wiki/concepts/kv-cache.md",
		Section: "## Related",
		Before:  kvCache.SHA256(),
		Content: []byte(newContent),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "patch kv-cache"})
	}); code != 0 {
		t.Fatalf("commit: exit = %d, stderr=%q", code, stderr)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"revert", "--vault", root, "000001"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "skipped:") {
		t.Errorf("stdout = %q, want no skipped: lines for a fully invertible revert", stdout)
	}
}

// TestCmdRevertUsageErrors covers the exit-code Contract for a missing
// or extra positional argument and a bad flag.
func TestCmdRevertUsageErrors(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	tests := []struct {
		name string
		args []string
	}{
		{"missing commit id", []string{"revert", "--vault", root}},
		{"extra argument", []string{"revert", "--vault", root, "000001", "000002"}},
		{"bad flag", []string{"revert", "--bogusflag", "000001"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, code := captureRun(t, func() int {
				return run(tt.args)
			})
			if code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
			}
		})
	}
}

// TestCmdRevertUnknownCommitIsBareError covers the bare-error/exit-1 path
// for a commit id with no predecessor snapshot.
func TestCmdRevertUnknownCommitIsBareError(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"revert", "--vault", root, "000099"})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.HasPrefix(stderr, "lw: revert: ") {
		t.Errorf("stderr = %q, want it to start with %q", stderr, "lw: revert: ")
	}
}

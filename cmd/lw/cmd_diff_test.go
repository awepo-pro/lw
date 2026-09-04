package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestCmdDiffNoChangeset covers the exit-code Contract (backbone §13
// D-AC): a bare failure — no open changeset — prints "lw: diff: ..." and
// exits 1.
func TestCmdDiffNoChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"diff", "--vault", root})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.HasPrefix(stderr, "lw: diff: ") {
		t.Errorf("stderr = %q, want it to start with %q", stderr, "lw: diff: ")
	}
}

// TestCmdDiffUnified stages one create_page op and checks the printed
// unified diff carries the new page's content under a /dev/null header.
func TestCmdDiffUnified(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openCreatePageChangeset(t, root, "wiki/concepts/diff-target.md", "Diff Target")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"diff", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "--- /dev/null") {
		t.Errorf("stdout missing /dev/null creation header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "+++ b/wiki/concepts/diff-target.md") {
		t.Errorf("stdout missing the new page's +++ header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "+title: Diff Target") {
		t.Errorf("stdout missing the new page's content:\n%s", stdout)
	}
}

// TestCmdDiffOpFilter checks --op narrows the unified diff to only the
// files a single op's FileDiff entries name — including index.md's
// derived line, which S2-T8's applyDerivedIndexDiff attributes to the
// create_page that produced it, but excluding a second, unrelated op's
// own entries (backbone §5.6, cmdDiff's own filterDiffByOp).
func TestCmdDiffOpFilter(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	if _, err := e.OpenChangeset("test changeset", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	content := "---\n" +
		"title: Diff Target\n" +
		"created: 2026-08-30\n" +
		"updated: 2026-08-30\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Diff Target\n" +
		"\n" +
		"See [[kv-cache]] and [[flash-attention]] for background.\n"
	createID, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/diff-target.md",
		Content:    []byte(content),
		Rationale:  "test fixture",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	})
	if err != nil {
		t.Fatalf("Append create_page: %v", err)
	}
	kvCache, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("minimal fixture missing wiki/concepts/kv-cache.md")
	}
	oldKVCache := string(kvCache.Serialize())
	newKVCache := strings.Replace(oldKVCache,
		"- [[flash-attention]]",
		"- [[gpt-4]] — an unrelated edit for TestCmdDiffOpFilter.\n- [[flash-attention]]",
		1)
	if newKVCache == oldKVCache {
		t.Fatal("test setup: replacement did not match kv-cache.md's body")
	}
	patchID, err := e.Append(stage.Op{
		Kind:    stage.OpPatchPage,
		Path:    "wiki/concepts/kv-cache.md",
		Section: "## Related",
		Before:  kvCache.SHA256(),
		Content: []byte(newKVCache),
	})
	if err != nil {
		t.Fatalf("Append patch_page: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"diff", "--vault", root, "--op", createID})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "+++ b/wiki/concepts/diff-target.md") {
		t.Errorf("stdout missing the create_page's own file:\n%s", stdout)
	}
	if !strings.Contains(stdout, "+- [[diff-target]] — Diff Target") {
		t.Errorf("stdout missing index.md's derived line, attributed to the same op:\n%s", stdout)
	}
	if strings.Contains(stdout, "kv-cache.md") {
		t.Errorf("stdout contains the second op's own file, which --op=%s should have filtered out:\n%s", createID, stdout)
	}

	stdout2, stderr2, code2 := captureRun(t, func() int {
		return run([]string{"diff", "--vault", root, "--op", patchID})
	})
	if code2 != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code2, stderr2)
	}
	if !strings.Contains(stdout2, "+++ b/wiki/concepts/kv-cache.md") {
		t.Errorf("stdout2 missing the patch_page's own file:\n%s", stdout2)
	}
	if strings.Contains(stdout2, "diff-target") {
		t.Errorf("stdout2 contains the first op's own paths, which --op=%s should have filtered out:\n%s", patchID, stdout2)
	}
}

// TestCmdDiffStat checks --stat prints a per-file summary instead of the
// full unified diff.
func TestCmdDiffStat(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openCreatePageChangeset(t, root, "wiki/concepts/diff-target.md", "Diff Target")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"diff", "--vault", root, "--stat"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "wiki/concepts/diff-target.md | +") {
		t.Errorf("stdout missing the per-file stat line:\n%s", stdout)
	}
	if strings.Contains(stdout, "--- ") {
		t.Errorf("--stat output should not contain a unified diff header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "file(s) changed") {
		t.Errorf("stdout missing the summary line:\n%s", stdout)
	}
}

// TestCmdDiffBadFlag covers the usage-error exit code.
func TestCmdDiffBadFlag(t *testing.T) {
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"diff", "--bogusflag"})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

// openCreatePageChangeset opens a changeset over root and appends one
// well-formed create_page op at path with title, returning the resulting
// Changeset. Shared by cmd_diff_test.go and cmd_commit_test.go.
func openCreatePageChangeset(t *testing.T, root, path, title string) *stage.Changeset {
	t.Helper()

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	if _, err := e.OpenChangeset("test changeset", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	content := "---\n" +
		"title: " + title + "\n" +
		"created: 2026-08-30\n" +
		"updated: 2026-08-30\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# " + title + "\n" +
		"\n" +
		"See [[kv-cache]] and [[flash-attention]] for background.\n"

	if _, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       path,
		Content:    []byte(content),
		Rationale:  "test fixture",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	return c
}

package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestCmdStatusChangesetLineNoOpenChangeset covers changesetStatusLine's
// no-changeset branch through the real `lw status` verb — S1-T6's
// original placeholder behaviour, still correct once internal/stage is
// live and the vault genuinely has none open.
func TestCmdStatusChangesetLineNoOpenChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"status", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "no open changeset") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "no open changeset")
	}
}

// TestCmdStatusChangesetLineOpenChangeset covers the other half: with a
// changeset open, the line names its id, intent, op count, checks and
// stale-op count (this subtask's Implement item 5).
func TestCmdStatusChangesetLineOpenChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	c := openCreatePageChangeset(t, root, "wiki/concepts/status-target.md", "Status Target")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"status", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "no open changeset") {
		t.Errorf("stdout = %q, want it to report the open changeset, not \"no open changeset\"", stdout)
	}
	if !strings.Contains(stdout, c.ID) {
		t.Errorf("stdout = %q, want it to contain the changeset id %q", stdout, c.ID)
	}
	if !strings.Contains(stdout, "test changeset") {
		t.Errorf("stdout = %q, want it to contain the intent %q", stdout, "test changeset")
	}
	if !strings.Contains(stdout, "1 op") {
		t.Errorf("stdout = %q, want it to contain the op count", stdout)
	}
	if !strings.Contains(stdout, "schema=pass") || !strings.Contains(stdout, "lint=pass") {
		t.Errorf("stdout = %q, want it to contain the Checks summary", stdout)
	}
	if !strings.Contains(stdout, "0 stale") {
		t.Errorf("stdout = %q, want it to report 0 stale ops", stdout)
	}
}

// TestCmdStatusChangesetLineCountsStaleOps checks the stale-op count
// reflects a real StateStale op, not just zero on every changeset.
func TestCmdStatusChangesetLineCountsStaleOps(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openCreatePageChangeset(t, root, "wiki/concepts/status-stale.md", "Status Stale")

	// Race the working tree out from under the create_page op — the same
	// shape backbone §5.4 D-AJ stales it by (the target path now exists).
	writeFileDirect(t, root, "wiki/concepts/status-stale.md", "---\n"+
		"title: Raced\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\n"+
		"tags: [inference]\n---\n\nSee [[kv-cache]] and [[flash-attention]].\n")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"status", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "1 stale") {
		t.Errorf("stdout = %q, want it to report 1 stale op", stdout)
	}
}

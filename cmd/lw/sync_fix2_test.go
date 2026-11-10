package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/vaultsync"
)

// TestSyncFix2_F2_1: a test hook makes rebase --continue fail.
// The sync returns within the test's 20 s deadline (no infinite loop),
// the tree digest equals the one before, and no conflict copy remains.
func TestSyncFix2_F2_1(t *testing.T) {
	a, b, remote := syncPair(t)
	a.write("notes/x.md", "base line\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A initial sync failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B initial sync failed")
	}

	a.appendTo("notes/x.md", "line from A\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync failed")
	}

	b.appendTo("notes/x.md", "line from B\n")
	b.git("add", "-A")
	b.git("commit", "--quiet", "-m", "b edit")

	beforeHead := b.git("rev-parse", "HEAD")
	beforeDigest := treeDigest(t, b.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json")

	vaultsync.SetTestRebaseContinueHook(func() error {
		return errors.New("injected continue failure")
	})
	defer vaultsync.SetTestRebaseContinueHook(nil)

	done := make(chan struct{})
	var (
		stdout string
		stderr string
		code   int
	)
	go func() {
		stdout, stderr, code = b.lw("sync")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("F2-1: sync did not return within 20s deadline (rebase loop hung)")
	}

	if code == 0 {
		t.Fatalf("expected B sync to fail, got exit 0 (stdout: %s, stderr: %s)", stdout, stderr)
	}

	if got := b.git("rev-parse", "HEAD"); got != beforeHead {
		t.Errorf("HEAD changed: %s, want %s", got, beforeHead)
	}
	if got := b.git("status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("git status dirty:\n%s", got)
	}
	if got := treeDigest(t, b.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json"); got != beforeDigest {
		t.Errorf("tree digest changed after giveUp:\ngot  %s\nwant %s", got, beforeDigest)
	}
	if copies := findConflictCopies(b.root); len(copies) != 0 {
		t.Errorf("conflict copies remained: %v", copies)
	}
	_ = remote
}

// TestSyncFix2_F2_2: a conflicting note `notes/a b .md` (space before the dot)
// resolves to a copy with the same stem.
func TestSyncFix2_F2_2(t *testing.T) {
	a, b, remote := syncPair(t)
	notePath := "notes/a b .md"
	a.write(notePath, "base line\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A initial sync failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B catchup sync failed")
	}

	a.appendTo(notePath, "line from A\n")
	b.appendTo(notePath, "line from B\n")

	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync failed")
	}

	bContentBefore := b.read(notePath)
	aContent := a.read(notePath)

	stdout, stderr, code := b.lw("sync")
	if code != 0 {
		t.Fatalf("B sync failed: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if got := b.read(notePath); got != aContent {
		t.Errorf("B's %s = %q, want %q", notePath, got, aContent)
	}

	copies := findConflictCopies(b.root)
	if len(copies) != 1 {
		t.Fatalf("found %d conflict copies in B, want 1: %v", len(copies), copies)
	}
	copyRel := copies[0]
	// Must have the same stem "a b " before ".conflict-"
	expectedPrefix := "notes/a b .conflict-"
	if !strings.HasPrefix(copyRel, expectedPrefix) {
		t.Errorf("conflict copy path %q does not start with %q", copyRel, expectedPrefix)
	}
	if got := b.read(copyRel); got != bContentBefore {
		t.Errorf("conflict copy content = %q, want B's content %q", got, bContentBefore)
	}
	_ = remote
}

// TestSyncFix2_F2_3: both PCs append a different line to bookkeeping.md;
// after sync both lines are in the file on both PCs, in the remote, and no conflict copy exists.
// A note under notes/ edited the same way still gets its conflict copy.
func TestSyncFix2_F2_3(t *testing.T) {
	a, b, remote := syncPair(t)
	a.write("bookkeeping.md", "base bookkeeping\n")
	a.write("notes/regular.md", "base note\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A initial sync failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B catchup sync failed")
	}

	// 1. Both append to bookkeeping.md
	a.appendTo("bookkeeping.md", "line from A\n")
	b.appendTo("bookkeeping.md", "line from B\n")

	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync bookkeeping failed")
	}

	stdout, stderr, code := b.lw("sync")
	if code != 0 {
		t.Fatalf("B sync failed: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// No conflict copies for bookkeeping.md
	copiesB := findConflictCopies(b.root)
	if len(copiesB) != 0 {
		t.Fatalf("found unexpected conflict copies in B: %v", copiesB)
	}

	// Both lines on B
	bContent := b.read("bookkeeping.md")
	if !strings.Contains(bContent, "line from A\n") || !strings.Contains(bContent, "line from B\n") {
		t.Errorf("B bookkeeping.md missing lines:\n%s", bContent)
	}

	// Both lines on remote
	remContent := remoteFile(t, remote, "bookkeeping.md")
	if !strings.Contains(remContent, "line from A") || !strings.Contains(remContent, "line from B") {
		t.Errorf("remote bookkeeping.md missing lines:\n%s", remContent)
	}

	// A syncs and gets both lines, with no conflict copy
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A catchup sync failed")
	}
	copiesA := findConflictCopies(a.root)
	if len(copiesA) != 0 {
		t.Fatalf("found unexpected conflict copies in A: %v", copiesA)
	}
	aContent := a.read("bookkeeping.md")
	if !strings.Contains(aContent, "line from A\n") || !strings.Contains(aContent, "line from B\n") {
		t.Errorf("A bookkeeping.md missing lines:\n%s", aContent)
	}

	// 2. A note under notes/ edited the same way still gets its conflict copy
	a.appendTo("notes/regular.md", "note edit from A\n")
	b.appendTo("notes/regular.md", "note edit from B\n")

	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync regular note failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B sync regular note failed")
	}

	copiesAfter := findConflictCopies(b.root)
	if len(copiesAfter) != 1 {
		t.Fatalf("expected 1 conflict copy for notes/regular.md, got %d: %v", len(copiesAfter), copiesAfter)
	}
	if !strings.HasPrefix(copiesAfter[0], "notes/regular.conflict-") {
		t.Errorf("expected conflict copy prefix notes/regular.conflict-, got %q", copiesAfter[0])
	}
}

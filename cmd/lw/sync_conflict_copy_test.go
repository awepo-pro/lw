package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vaultsync"
)

// CC1 both PCs append a different line to the same notes/x.md; A pushes; B lw sync exits 0;
// B's notes/x.md equals A's; exactly one conflict copy exists beside it holding B's version
// byte for byte; the remote has both files; A's next lw sync brings the copy to A.
// Run on the base first: it must fail with diverged.
func TestSyncConflictCopyCC1(t *testing.T) {
	a, b, remote := syncPair(t)
	a.write("notes/x.md", "base line\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A initial sync failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B catchup sync failed")
	}

	a.appendTo("notes/x.md", "line from A\n")
	b.appendTo("notes/x.md", "line from B\n")

	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync failed")
	}

	bContentBefore := b.read("notes/x.md")
	aContent := a.read("notes/x.md")

	stdout, stderr, code := b.lw("sync")
	if code != 0 {
		t.Fatalf("B sync failed: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if got := b.read("notes/x.md"); got != aContent {
		t.Errorf("B's notes/x.md = %q, want %q", got, aContent)
	}

	copies := findConflictCopies(b.root)
	if len(copies) != 1 {
		t.Fatalf("found %d conflict copies in B, want 1: %v", len(copies), copies)
	}
	copyRel := copies[0]
	if !strings.HasPrefix(copyRel, "notes/x.conflict-") {
		t.Errorf("conflict copy path %q does not start with notes/x.conflict-", copyRel)
	}
	if got := b.read(copyRel); got != bContentBefore {
		t.Errorf("conflict copy content = %q, want B's content %q", got, bContentBefore)
	}

	if remoteFile(t, remote, "notes/x.md") != strings.TrimSpace(aContent) {
		t.Errorf("remote notes/x.md does not equal A's content")
	}
	if remoteFile(t, remote, copyRel) != strings.TrimSpace(bContentBefore) {
		t.Errorf("remote lacks conflict copy %s or has wrong content", copyRel)
	}

	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync to fetch copy failed")
	}
	if got := a.read(copyRel); got != bContentBefore {
		t.Errorf("A lacks conflict copy %s or content mismatch: %q", copyRel, got)
	}
}

// CC2 the remote deleted the note, this PC edited it: the note survives with this PC's bytes; no copy.
func TestSyncConflictCopyCC2(t *testing.T) {
	a, b, remote := syncPair(t)
	a.write("notes/x.md", "initial text\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A initial sync failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B catchup sync failed")
	}

	// Remote (A) deletes the note and pushes
	if err := os.Remove(filepath.Join(a.root, "notes/x.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync delete failed")
	}

	// This PC (B) edits the note
	b.appendTo("notes/x.md", "B's edit\n")
	bWant := b.read("notes/x.md")

	// B syncs
	stdout, stderr, code := b.lw("sync")
	if code != 0 {
		t.Fatalf("B sync failed: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// The note survives with this PC's bytes; no copy.
	if got := b.read("notes/x.md"); got != bWant {
		t.Errorf("B's notes/x.md = %q, want %q", got, bWant)
	}
	if copies := findConflictCopies(b.root); len(copies) != 0 {
		t.Errorf("unexpected conflict copies in B: %v", copies)
	}

	// A syncs and brings B's version
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync failed")
	}
	if got := a.read("notes/x.md"); got != bWant {
		t.Errorf("A's notes/x.md = %q, want %q", got, bWant)
	}
	if copies := findConflictCopies(a.root); len(copies) != 0 {
		t.Errorf("unexpected conflict copies in A: %v", copies)
	}
	_ = remote
}

// CC3 this PC deleted it, the remote edited it: the note is there with the remote's bytes; no copy.
func TestSyncConflictCopyCC3(t *testing.T) {
	a, b, remote := syncPair(t)
	a.write("notes/x.md", "initial text\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A initial sync failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B catchup sync failed")
	}

	// Remote (A) edits the note and pushes
	a.appendTo("notes/x.md", "A's edit\n")
	aWant := a.read("notes/x.md")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync edit failed")
	}

	// This PC (B) deletes the note
	if err := os.Remove(filepath.Join(b.root, "notes/x.md")); err != nil {
		t.Fatal(err)
	}

	// B syncs
	stdout, stderr, code := b.lw("sync")
	if code != 0 {
		t.Fatalf("B sync failed: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// The note is there with the remote's bytes; no copy.
	if got := b.read("notes/x.md"); got != aWant {
		t.Errorf("B's notes/x.md = %q, want %q", got, aWant)
	}
	if copies := findConflictCopies(b.root); len(copies) != 0 {
		t.Errorf("unexpected conflict copies in B: %v", copies)
	}
	_ = remote
}

// CC4 a wiki page edited on both sides is still refused, tree restored;
// a note AND a wiki page both conflicting: refused, nothing written.
func TestSyncConflictCopyCC4(t *testing.T) {
	a, b, remote := syncPair(t)
	a.write("notes/x.md", "note base\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A initial sync failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B catchup sync failed")
	}

	// A edits both a note AND a wiki page, then pushes
	a.appendTo("notes/x.md", "A's note edit\n")
	a.appendTo(kvPage, "\nA's wiki edit\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync failed")
	}

	// B edits both the note AND the wiki page
	b.appendTo("notes/x.md", "B's note edit\n")
	b.appendTo(kvPage, "\nB's wiki edit\n")
	b.git("add", "-A")
	b.git("commit", "--quiet", "-m", "lw sync")

	headBefore := b.git("rev-parse", "HEAD")
	digestBefore := treeDigest(t, b.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json")

	// B syncs: must be refused!
	_, stderr, code := b.lw("sync")
	if code != 1 || !strings.Contains(stderr, "diverged from "+remote) {
		t.Fatalf("B sync exit %d, stderr %q; want divergence refusal", code, stderr)
	}

	// B's HEAD, tree and status must be restored; nothing written.
	if got := b.git("rev-parse", "HEAD"); got != headBefore {
		t.Errorf("B HEAD changed: %s, want %s", got, headBefore)
	}
	if got := treeDigest(t, b.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json"); got != digestBefore {
		t.Errorf("B tree digest changed after refused sync")
	}
	if copies := findConflictCopies(b.root); len(copies) != 0 {
		t.Errorf("conflict copies were created despite refusal: %v", copies)
	}
}

// CC5 two local commits that both conflict on the same note: ends clean, the note has
// the remote's bytes, the copy (or copies) hold this PC's final version.
func TestSyncConflictCopyCC5(t *testing.T) {
	a, b, remote := syncPair(t)
	a.write("notes/x.md", "line 0\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A initial sync failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B catchup sync failed")
	}

	// A pushes remote edit
	a.appendTo("notes/x.md", "remote edit from A\n")
	aWant := a.read("notes/x.md")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync failed")
	}

	// B makes TWO local commits touching the same note
	b.appendTo("notes/x.md", "local commit 1 from B\n")
	b.git("add", "-A")
	b.git("commit", "--quiet", "-m", "commit 1")

	b.appendTo("notes/x.md", "local commit 2 from B\n")
	b.git("add", "-A")
	b.git("commit", "--quiet", "-m", "commit 2")

	bFinal := b.read("notes/x.md")

	// B syncs
	stdout, stderr, code := b.lw("sync")
	if code != 0 {
		t.Fatalf("B sync failed: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// B's notes/x.md has the remote's bytes
	if got := b.read("notes/x.md"); got != aWant {
		t.Errorf("B notes/x.md = %q, want remote bytes %q", got, aWant)
	}

	// At least one conflict copy holds B's final version
	copies := findConflictCopies(b.root)
	if len(copies) == 0 {
		t.Fatal("expected at least one conflict copy in B")
	}
	foundFinal := false
	for _, c := range copies {
		if b.read(c) == bFinal {
			foundFinal = true
			break
		}
	}
	if !foundFinal {
		t.Errorf("none of the conflict copies %v held B's final version %q", copies, bFinal)
	}
	_ = remote
}

// CC6 a non-overlapping edit of the same note on both sides still merges with no copy
// (git's own 3-way merge).
func TestSyncConflictCopyCC6(t *testing.T) {
	a, b, remote := syncPair(t)
	initial := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10\n"
	a.write("notes/x.md", initial)
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A initial sync failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B catchup sync failed")
	}

	// A edits top of note and pushes
	a.write("notes/x.md", "line 1 from A\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync failed")
	}

	// B edits bottom of note
	b.write("notes/x.md", "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10 from B\n")

	// B syncs
	stdout, stderr, code := b.lw("sync")
	if code != 0 {
		t.Fatalf("B sync failed: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Merged cleanly without conflict copy
	expectedMerged := "line 1 from A\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10 from B\n"
	if got := b.read("notes/x.md"); got != expectedMerged {
		t.Errorf("B merged notes/x.md = %q, want %q", got, expectedMerged)
	}
	if copies := findConflictCopies(b.root); len(copies) != 0 {
		t.Errorf("expected 0 conflict copies for clean 3-way merge, got %d: %v", len(copies), copies)
	}
	_ = remote
}

// TestSyncConflictCopySafety proves that if any step fails after a path was resolved,
// the giveUp path brings the vault back to exactly what it was (HEAD, index, work tree).
func TestSyncConflictCopySafety(t *testing.T) {
	a, b, remote := syncPair(t)
	a.write("notes/x.md", "base x\n")
	a.write("notes/y.md", "base y\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A initial sync failed")
	}
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B catchup sync failed")
	}

	// Both edit notes/x.md and notes/y.md
	a.appendTo("notes/x.md", "A edit x\n")
	a.appendTo("notes/y.md", "A edit y\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A sync failed")
	}

	b.appendTo("notes/x.md", "B edit x\n")
	b.appendTo("notes/y.md", "B edit y\n")
	b.git("add", "-A")
	b.git("commit", "--quiet", "-m", "lw sync")

	beforeHead := b.git("rev-parse", "HEAD")
	beforeDigest := treeDigest(t, b.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json")

	// Inject failure after one path was resolved
	vaultsync.SetTestConflictCopyPostHook(func(p string) error {
		return errors.New("injected failure after path resolution")
	})
	defer vaultsync.SetTestConflictCopyPostHook(nil)

	_, _, code := b.lw("sync")
	if code == 0 {
		t.Fatal("expected B sync to fail due to injected error")
	}

	// Verify HEAD, index, and tree digest restored byte-for-byte
	if got := b.git("rev-parse", "HEAD"); got != beforeHead {
		t.Errorf("HEAD changed: %s, want %s", got, beforeHead)
	}
	if got := b.git("status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("git status dirty:\n%s", got)
	}
	if got := treeDigest(t, b.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json"); got != beforeDigest {
		t.Errorf("tree digest changed after restored failure: %s vs %s", got, beforeDigest)
	}
	if copies := findConflictCopies(b.root); len(copies) != 0 {
		t.Errorf("conflict copies left behind: %v", copies)
	}
	_ = remote
}

// TestSyncConflictCopyPatternMatches verifies that generated conflict copies match conflictCopyPattern.
func TestSyncConflictCopyPatternMatches(t *testing.T) {
	names := []string{
		"x.conflict-anton-20261110-143045.md",
		"x.conflict-anton-20261110-143045-2.md",
		"x.conflict-my-pc-20261110-143045.md",
		"notes.v1.conflict-host-20261110-143045.txt",
		"todo.conflict-host-20261110-143045",
	}
	for _, n := range names {
		if !conflictCopyPattern.MatchString(n) {
			t.Errorf("name %q does not match conflictCopyPattern", n)
		}
	}
}

// TestIsConflictCopyableNote verifies the note-path rule.
func TestIsConflictCopyableNote(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"notes/x.md", true},
		{"notes/deep/sub/doc.txt", true},
		{"ledger/2026-11-10.md", true},
		{"ledger/receipts/rec.jpg", false},
		{"ledger/receipts/2026/01.png", false},
		{"root-note.md", true},
		{"quicknote.txt", true},
		{"todo", true},
		{"SCHEMA.md", false},
		{"index.md", false},
		{"log.md", false},
		{"curator-memory.md", false},
		{".gitignore", false},
		{"wiki/concepts/kv.md", false},
		{"raw/paper.pdf", false},
		{".llmwiki/journal.ndjson", false},
		{".obsidian/workspace.json", false},
		{"", false},
		{".", false},
	}
	for _, tc := range cases {
		if got := isConflictCopyableNote(tc.path); got != tc.want {
			t.Errorf("isConflictCopyableNote(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

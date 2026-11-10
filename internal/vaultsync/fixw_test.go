package vaultsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFixW_FW7_GitLockInternal(t *testing.T) {
	dir := t.TempDir()
	o := Options{Dir: dir}

	// Init git repo
	r, err := newRunner(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.out(context.Background(), "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.out(context.Background(), "config", "user.name", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.out(context.Background(), "config", "user.email", "test@test.com"); err != nil {
		t.Fatal(err)
	}

	lockFile := filepath.Join(dir, ".git", "index.lock")

	// 1. Fresh lock -> lockError, file untouched
	if err := os.WriteFile(lockFile, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = r.out(context.Background(), "add", "-A")
	var le *lockError
	if !errors.As(err, &le) {
		t.Fatalf("expected lockError for fresh lock, got: %v", err)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("fresh lock file was deleted: %v", err)
	}

	// 2. Stale lock (> 10 mins old) -> lock removed, add succeeds!
	oldTime := time.Now().Add(-15 * time.Minute)
	if err := os.Chtimes(lockFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	_, err = r.out(context.Background(), "add", "-A")
	if err != nil {
		t.Fatalf("expected add to succeed after stale lock removal, got: %v", err)
	}
	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Fatalf("stale lock file was not removed")
	}

	// 3. Stale lock outside this vault's .git (e.g. in worktree root or /tmp)
	// Must NOT be removed by runner!
	outsideDir := t.TempDir()
	outsideLock := filepath.Join(outsideDir, "outside.lock")
	if err := os.WriteFile(outsideLock, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(outsideLock, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	if r.isVaultGitLock(outsideLock) {
		t.Fatalf("isVaultGitLock reported true for lock outside vault's .git: %s", outsideLock)
	}
}

func TestFixW_FW8_InternalSnapshot(t *testing.T) {
	t.Skip("BLOCKED: FW8 contradicts existing frozen tests TestCollisionMarkerBlocksSyncUntilTakeRemote and TestTakeRemoteKeepsMarkerWhileStillColliding in internal/vaultsync/vaultsync_review_test.go which assert that on a marked collision the backup branch must point at HEAD (before.head) and untracked files must not be removed")
	// Set up remote repo
	remoteDir := t.TempDir()
	remRunner, err := newRunner(Options{Dir: remoteDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remRunner.out(context.Background(), "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := remRunner.out(context.Background(), "config", "user.name", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := remRunner.out(context.Background(), "config", "user.email", "test@test.com"); err != nil {
		t.Fatal(err)
	}
	// Initial commit on remote
	if err := os.WriteFile(filepath.Join(remoteDir, "note.md"), []byte("remote v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := remRunner.out(context.Background(), "add", "note.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := remRunner.out(context.Background(), "commit", "-q", "-m", "init"); err != nil {
		t.Fatal(err)
	}

	// Local vault clones from remote
	localDir := t.TempDir()
	o := Options{Dir: localDir, Remotes: []string{remoteDir}}
	locRunner, err := newRunner(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := Clone(context.Background(), o, 1); err != nil {
		t.Fatal(err)
	}

	// Create collision marker in local .git
	marker := filepath.Join(localDir, ".git", collisionMarker)
	if err := os.WriteFile(marker, []byte("note.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Edit note.md and add new.md in local
	if err := os.WriteFile(filepath.Join(localDir, "note.md"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "new.md"), []byte("local new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add remote commit so take-remote has something to take
	if err := os.WriteFile(filepath.Join(remoteDir, "note.md"), []byte("remote v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := remRunner.out(context.Background(), "commit", "-q", "-am", "remote v2"); err != nil {
		t.Fatal(err)
	}

	// Run takeRemoteAt
	backup, _, err := takeRemoteAt(context.Background(), o, 1, time.Now())
	if err != nil {
		t.Fatalf("takeRemoteAt failed: %v", err)
	}

	// Verify temporary index was cleaned up
	matches, _ := filepath.Glob(filepath.Join(localDir, ".git", "tmp-index-*"))
	if len(matches) != 0 {
		t.Fatalf("temporary index was not cleaned up: %v", matches)
	}

	// Verify backup branch holds local edit and new file
	showNote, err := locRunner.out(context.Background(), "show", backup+":note.md")
	if err != nil || showNote != "local edit" {
		t.Fatalf("backup branch note.md = %q (err: %v), want 'local edit'", showNote, err)
	}
	showNew, err := locRunner.out(context.Background(), "show", backup+":new.md")
	if err != nil || showNew != "local new" {
		t.Fatalf("backup branch new.md = %q (err: %v), want 'local new'", showNew, err)
	}

	// Verify work tree matches remote v2
	noteBytes, _ := os.ReadFile(filepath.Join(localDir, "note.md"))
	if string(noteBytes) != "remote v2\n" {
		t.Fatalf("worktree note.md = %q, want 'remote v2\\n'", string(noteBytes))
	}
	if _, err := os.Stat(filepath.Join(localDir, "new.md")); !os.IsNotExist(err) {
		t.Fatalf("worktree new.md still exists")
	}

	// Verify collision marker removed
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("collision marker was not removed")
	}
}

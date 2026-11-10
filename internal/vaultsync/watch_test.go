package vaultsync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.name=test",
		"-c", "user.email=test@test",
		"-c", "commit.gpgsign=false",
		"-c", "init.defaultBranch=main",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, errb.String())
	}
	return out.String()
}

func TestDirty_CleanTree(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	file := filepath.Join(dir, "note.md")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "init")

	st, err := Dirty(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if len(st.Paths) != 0 {
		t.Errorf("Paths = %v, want empty", st.Paths)
	}
	if st.Deleted != 0 {
		t.Errorf("Deleted = %d, want 0", st.Deleted)
	}
	if st.Tracked != 1 {
		t.Errorf("Tracked = %d, want 1", st.Tracked)
	}
}

func TestDirty_BeforeFirstCommit(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	file := filepath.Join(dir, "uncommitted.md")
	if err := os.WriteFile(file, []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Dirty(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if st.Tracked != 0 {
		t.Errorf("Tracked = %d, want 0 before first commit", st.Tracked)
	}
	if len(st.Paths) != 1 || st.Paths[0] != "uncommitted.md" {
		t.Errorf("Paths = %v, want [uncommitted.md]", st.Paths)
	}
}

func TestDirty_ModifiedFile(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	file := filepath.Join(dir, "note.md")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "init")

	// Modify file
	modTime := time.Now().Add(5 * time.Second).Truncate(time.Second)
	if err := os.WriteFile(file, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, modTime, modTime); err != nil {
		t.Fatal(err)
	}

	st, err := Dirty(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if len(st.Paths) != 1 || st.Paths[0] != "note.md" {
		t.Errorf("Paths = %v, want [note.md]", st.Paths)
	}
	if !st.Newest.Equal(modTime) {
		t.Errorf("Newest = %v, want %v", st.Newest, modTime)
	}
}

func TestDirty_UntrackedFile(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	file := filepath.Join(dir, "tracked.md")
	if err := os.WriteFile(file, []byte("tracked"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "init")

	untracked := filepath.Join(dir, "untracked.md")
	untrackedTime := time.Now().Add(10 * time.Second).Truncate(time.Second)
	if err := os.WriteFile(untracked, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(untracked, untrackedTime, untrackedTime); err != nil {
		t.Fatal(err)
	}

	st, err := Dirty(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if len(st.Paths) != 1 || st.Paths[0] != "untracked.md" {
		t.Errorf("Paths = %v, want [untracked.md]", st.Paths)
	}
	if !st.Newest.Equal(untrackedTime) {
		t.Errorf("Newest = %v, want %v", st.Newest, untrackedTime)
	}
}

func TestDirty_IgnoredFileDoesNotCount(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	gitignore := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignore, []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "init")

	ignored := filepath.Join(dir, "ignored.txt")
	if err := os.WriteFile(ignored, []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Dirty(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if len(st.Paths) != 0 {
		t.Errorf("Paths = %v, want empty (ignored file should not count)", st.Paths)
	}
}

func TestDirty_DeletedTrackedFile(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	file1 := filepath.Join(dir, "stay.md")
	file2 := filepath.Join(dir, "delete.md")
	if err := os.WriteFile(file1, []byte("stay"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("delete"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "init")

	if err := os.Remove(file2); err != nil {
		t.Fatal(err)
	}

	st, err := Dirty(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if len(st.Paths) != 1 || st.Paths[0] != "delete.md" {
		t.Errorf("Paths = %v, want [delete.md]", st.Paths)
	}
	if st.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", st.Deleted)
	}
	if st.Tracked != 2 {
		t.Errorf("Tracked = %d, want 2", st.Tracked)
	}
	if !st.Newest.IsZero() {
		t.Errorf("Newest = %v, want zero time since deleted file does not exist", st.Newest)
	}
}

func TestDirty_NewestEqualsNewestMtime(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	file1 := filepath.Join(dir, "file1.md")
	file2 := filepath.Join(dir, "file2.md")
	if err := os.WriteFile(file1, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "init")

	t1 := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	t2 := time.Now().Add(-2 * time.Minute).Truncate(time.Second)

	if err := os.WriteFile(file1, []byte("one updated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file1, t1, t1); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(file2, []byte("two updated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file2, t2, t2); err != nil {
		t.Fatal(err)
	}

	st, err := Dirty(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if len(st.Paths) != 2 {
		t.Fatalf("Paths = %v, want 2 paths", st.Paths)
	}
	if !st.Newest.Equal(t2) {
		t.Errorf("Newest = %v, want %v", st.Newest, t2)
	}
}

func TestRemoteHead_BareRepo(t *testing.T) {
	temp := t.TempDir()
	bare := filepath.Join(temp, "bare.git")
	runGit(t, temp, "init", "--bare", bare)

	vault := filepath.Join(temp, "vault")
	runGit(t, temp, "init", vault)
	file := filepath.Join(vault, "doc.md")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, vault, "add", "-A")
	runGit(t, vault, "commit", "-m", "init")

	// Before push to bare, remote has no main yet.
	sha, local, remote, err := RemoteHead(context.Background(), Options{Dir: vault, Remotes: []string{bare}})
	if err != nil {
		t.Fatalf("RemoteHead before push: %v", err)
	}
	if sha != "" {
		t.Errorf("sha = %q, want empty before push", sha)
	}
	if remote != bare {
		t.Errorf("remote = %q, want %q", remote, bare)
	}
	if local != "" {
		t.Errorf("local = %q, want empty", local)
	}

	// Another clone pushes to bare
	clone := filepath.Join(temp, "clone")
	runGit(t, temp, "clone", bare, clone)
	runGit(t, clone, "checkout", "-b", "main")
	cfile := filepath.Join(clone, "doc.md")
	if err := os.WriteFile(cfile, []byte("remote commit"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "add", "-A")
	runGit(t, clone, "commit", "-m", "remote commit")
	expectedSha := runGit(t, clone, "rev-parse", "HEAD")
	expectedSha = expectedSha[:len(expectedSha)-1] // trim newline
	runGit(t, clone, "push", "origin", "main")

	// Now RemoteHead should return the sha of the bare repo's main
	sha, local, remote, err = RemoteHead(context.Background(), Options{Dir: vault, Remotes: []string{bare}})
	if err != nil {
		t.Fatalf("RemoteHead after push: %v", err)
	}
	if sha != expectedSha {
		t.Errorf("sha = %q, want %q", sha, expectedSha)
	}
	if remote != bare {
		t.Errorf("remote = %q, want %q", remote, bare)
	}
}

func TestRemoteHead_DeadRemote(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")

	_, _, _, err := RemoteHead(context.Background(), Options{
		Dir:     dir,
		Remotes: []string{"/nonexistent/dead/remote/path"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *RemoteError
	if !errors.As(err, &re) {
		t.Fatalf("expected *RemoteError, got %T: %v", err, err)
	}
	if len(re.Tried) != 1 || re.Tried[0] != "/nonexistent/dead/remote/path" {
		t.Errorf("re.Tried = %v", re.Tried)
	}
}

package vaultsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestUnmergedPathsTrailingSpace verifies that unmergedPaths does not trim
// trailing spaces from paths returned by git diff -z (F2-2).
func TestUnmergedPathsTrailingSpace(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	git(t, dir, "config", "user.name", "Test")
	git(t, dir, "config", "user.email", "test@example.com")

	r, err := newRunner(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	target := "notes/trailing "
	abs := filepath.Join(dir, filepath.FromSlash(target))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", "base")

	git(t, dir, "branch", "feat")
	if err := os.WriteFile(abs, []byte("main edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-am", "main edit")

	git(t, dir, "checkout", "feat")
	if err := os.WriteFile(abs, []byte("feat edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-am", "feat edit")

	_, _ = r.gitCall(context.Background(), call{args: []string{"rebase", "main"}})
	defer func() {
		_, _ = r.gitCall(context.Background(), call{args: []string{"rebase", "--abort"}})
	}()

	paths, err := r.unmergedPaths(context.Background())
	if err != nil {
		t.Fatalf("unmergedPaths: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("unmergedPaths returned %d paths, want 1: %v", len(paths), paths)
	}
	if paths[0] != target {
		t.Errorf("unmergedPaths[0] = %q, want %q", paths[0], target)
	}
}

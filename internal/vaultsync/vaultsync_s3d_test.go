package vaultsync

// vaultsync_s3d_test.go pins 042 S3d M4: Init does not adopt a repository that
// is the user's own.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInitRefusesAnExistingRepositoryWithHistory: a vault that is already a git
// repository with commits and no lw ref has history of its own; Init would
// commit its work in progress onto the user's branch and push the whole
// history as main. It refuses, and changes nothing.
func TestInitRefusesAnExistingRepositoryWithHistory(t *testing.T) {
	hermetic(t)
	vault := makeVault(t)
	git(t, vault, "init", "--quiet", "-b", "dev")
	git(t, vault, "add", "-A")
	git(t, vault, "commit", "--quiet", "-m", "my own history")
	put(t, vault, "notes/wip.md", "work in progress\n")
	remote := filepath.Join(t.TempDir(), "remote.git")
	head := git(t, vault, "rev-parse", "HEAD")

	_, err := Init(t.Context(), opts(vault, remote))
	want := vault + " is already a git repository with its own history — lw sync init will not adopt it; move its .git aside or start from a copy"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %q", err, want)
	}
	if git(t, vault, "rev-parse", "HEAD") != head || git(t, vault, "rev-parse", "--abbrev-ref", "HEAD") != "dev" {
		t.Error("Init changed the user's repository")
	}
	if got := git(t, vault, "status", "--porcelain", "--untracked-files=all"); got != "?? notes/wip.md" {
		t.Errorf("the work in progress was touched; status:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(vault, ".gitignore")); err == nil {
		t.Error("Init wrote a .gitignore into the user's repository")
	}
	if _, err := os.Stat(remote); err == nil {
		t.Error("Init created the remote before refusing")
	}
}

// TestInitAcceptsWhatItMadeOrWhatHasNoHistory: an empty repository (git init,
// no commit) is fine, and so is the repository a failed Init left behind — its
// retry is the point of the order Init works in — and a successful Init leaves
// no marker.
func TestInitAcceptsWhatItMadeOrWhatHasNoHistory(t *testing.T) {
	t.Run("an empty repository", func(t *testing.T) {
		hermetic(t)
		vault := makeVault(t)
		git(t, vault, "init", "--quiet", "-b", "main")
		remote := filepath.Join(t.TempDir(), "remote.git")
		if st, err := Init(t.Context(), opts(vault, remote)); err != nil || st.Pushed != 1 {
			t.Fatalf("Init = %+v, %v", st, err)
		}
	})

	t.Run("its own repository after a failed push", func(t *testing.T) {
		hermetic(t)
		bare := filepath.Join(t.TempDir(), "r.git")
		git(t, filepath.Dir(bare), "init", "--bare", "-b", "main", bare)
		hook := filepath.Join(bare, "hooks", "pre-receive")
		put(t, bare, "hooks/pre-receive", "#!/bin/sh\necho 'rejected by policy' >&2\nexit 1\n")
		if err := os.Chmod(hook, 0o755); err != nil {
			t.Fatal(err)
		}
		vault := makeVault(t)
		if _, err := Init(t.Context(), opts(vault, bare)); err == nil {
			t.Fatal("the first Init succeeded through a rejecting hook")
		}
		if git(t, vault, "rev-list", "--count", "HEAD") != "1" {
			t.Fatal("setup: the failed Init left no commit behind")
		}
		if err := os.Remove(hook); err != nil {
			t.Fatal(err)
		}
		st, err := Init(t.Context(), opts(vault, bare))
		if err != nil || st.Pushed != 1 {
			t.Fatalf("retry = %+v, %v; want Pushed 1", st, err)
		}
		if _, err := os.Stat(filepath.Join(vault, ".git", "lw-init")); err == nil {
			t.Error("a successful Init left its marker behind")
		}
		// Now it is under lw sync, and says so.
		if _, err := Init(t.Context(), opts(vault, bare)); err == nil || err.Error() != "already under lw sync" {
			t.Errorf("a third Init = %v, want `already under lw sync`", err)
		}
	})
}

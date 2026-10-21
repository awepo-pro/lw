package vaultsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStorageIgnoredByGit tests that files in <vault>/storage/ are NOT staged
// or committed by commitWork / lw sync (verify git status and git ls-tree).
func TestStorageIgnoredByGit(t *testing.T) {
	hermetic(t)
	vault := makeVault(t)
	remote := bareRemote(t)

	ctx := t.Context()
	if _, err := Init(ctx, opts(vault, remote)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Add files into storage/ and a regular file outside storage/.
	put(t, vault, "storage/sample.bin", "storage content 1\n")
	put(t, vault, "storage/nested/data.bin", "nested storage content\n")
	put(t, vault, "notes/testnote.md", "a new note\n")

	committed, err := CommitWork(opts(vault, remote), "add note and storage")
	if err != nil {
		t.Fatalf("CommitWork: %v", err)
	}
	if !committed {
		t.Fatal("expected CommitWork to commit note")
	}

	// Verify git status contains no mentions of storage.
	status := git(t, vault, "status", "--porcelain")
	if strings.Contains(status, "storage") {
		t.Fatalf("git status contains storage:\n%s", status)
	}

	// Verify git ls-tree at HEAD contains zero blobs from storage/.
	names := treeNames(t, vault, "HEAD")
	for _, name := range names {
		if strings.HasPrefix(name, "storage/") || name == "storage" {
			t.Fatalf("git ls-tree contains storage entry: %s", name)
		}
	}

	// Verify git ls-files contains no storage entries.
	lsFiles := git(t, vault, "ls-files", "storage")
	if strings.TrimSpace(lsFiles) != "" {
		t.Fatalf("git ls-files returned storage entries:\n%s", lsFiles)
	}
}

// TestSyncStorageBidirectional tests bidirectional SyncStorage between
// local vault and local remote (<remote>.storage).
func TestSyncStorageBidirectional(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()

	remoteStorageDir := p.bare + ".storage"

	// 1. Vault A adds files in storage/.
	put(t, p.a, "storage/fileA.txt", "content from vault A\n")
	put(t, p.a, "storage/sub/nestedA.txt", "nested content from vault A\n")

	if err := SyncStorage(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatalf("SyncStorage p.a: %v", err)
	}

	// Remote storage dir should have fileA.txt and sub/nestedA.txt.
	if got := readFile(t, filepath.Join(remoteStorageDir, "fileA.txt")); got != "content from vault A\n" {
		t.Fatalf("remote fileA.txt = %q, want content from vault A", got)
	}
	if got := readFile(t, filepath.Join(remoteStorageDir, "sub", "nestedA.txt")); got != "nested content from vault A\n" {
		t.Fatalf("remote sub/nestedA.txt = %q, want nested content from vault A", got)
	}

	// 2. Vault B adds a storage file and syncs with remote.
	put(t, p.b, "storage/fileB.txt", "content from vault B\n")

	if err := SyncStorage(ctx, opts(p.b, p.remote)); err != nil {
		t.Fatalf("SyncStorage p.b: %v", err)
	}

	// Vault B should have received files from Vault A via remote.
	if got := readFile(t, filepath.Join(p.b, "storage", "fileA.txt")); got != "content from vault A\n" {
		t.Fatalf("p.b storage/fileA.txt = %q, want content from vault A", got)
	}
	if got := readFile(t, filepath.Join(p.b, "storage", "sub", "nestedA.txt")); got != "nested content from vault A\n" {
		t.Fatalf("p.b storage/sub/nestedA.txt = %q, want nested content from vault A", got)
	}

	// Remote should now also contain fileB.txt pushed by Vault B.
	if got := readFile(t, filepath.Join(remoteStorageDir, "fileB.txt")); got != "content from vault B\n" {
		t.Fatalf("remote fileB.txt = %q, want content from vault B", got)
	}

	// 3. Vault A syncs and should receive fileB.txt from remote.
	if err := SyncStorage(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatalf("SyncStorage p.a second time: %v", err)
	}
	if got := readFile(t, filepath.Join(p.a, "storage", "fileB.txt")); got != "content from vault B\n" {
		t.Fatalf("p.a storage/fileB.txt = %q, want content from vault B", got)
	}

	// 4. Test size change propagation.
	time.Sleep(20 * time.Millisecond)
	updatedContent := "updated much longer content from vault A\n"
	put(t, p.a, "storage/fileA.txt", updatedContent)
	if err := SyncStorage(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatalf("SyncStorage p.a after update: %v", err)
	}
	if got := readFile(t, filepath.Join(remoteStorageDir, "fileA.txt")); got != updatedContent {
		t.Fatalf("remote fileA.txt after update = %q, want %q", got, updatedContent)
	}

	if err := SyncStorage(ctx, opts(p.b, p.remote)); err != nil {
		t.Fatalf("SyncStorage p.b after update: %v", err)
	}
	if got := readFile(t, filepath.Join(p.b, "storage", "fileA.txt")); got != updatedContent {
		t.Fatalf("p.b fileA.txt after update = %q, want %q", got, updatedContent)
	}
}

// TestSyncStorageNoRemotesOrNoStorage tests no-op scenarios.
func TestSyncStorageNoRemotesOrNoStorage(t *testing.T) {
	hermetic(t)
	ctx := t.Context()

	// Empty remotes: should return nil
	if err := SyncStorage(ctx, Options{Dir: t.TempDir()}); err != nil {
		t.Fatalf("SyncStorage with no remotes: %v", err)
	}

	// Neither local nor remote has storage: should return nil and create nothing
	vault := makeVault(t)
	remote := bareRemote(t)
	if err := SyncStorage(ctx, opts(vault, remote)); err != nil {
		t.Fatalf("SyncStorage with no storage: %v", err)
	}

	if _, err := os.Stat(filepath.Join(vault, "storage")); !os.IsNotExist(err) {
		t.Fatalf("expected vault/storage not to exist, got err: %v", err)
	}
	if _, err := os.Stat(remote + ".storage"); !os.IsNotExist(err) {
		t.Fatalf("expected remote.storage not to exist, got err: %v", err)
	}
}

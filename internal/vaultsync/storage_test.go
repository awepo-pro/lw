package vaultsync

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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

// storageCatalog describes every regular file under vault/storage the way the
// catalog (.llmwiki/storage.json) would: name -> sha256 and size.
func storageCatalog(t *testing.T, vault string) map[string]Object {
	t.Helper()
	cat := map[string]Object{}
	root := filepath.Join(vault, "storage")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		cat[filepath.ToSlash(rel)] = objOf(string(b))
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return cat
}

// mergeCatalogs is what a pull does to the catalog: both PCs' entries, the
// later argument winning a name (a replacement).
func mergeCatalogs(cats ...map[string]Object) map[string]Object {
	out := map[string]Object{}
	for _, c := range cats {
		for k, v := range c {
			out[k] = v
		}
	}
	return out
}

// uploadStorage is the upload step of a sync, written against the object API:
// one question for the whole catalog, then a put for each object the remote
// lacks. Content goes up before any pointer to it (B6).
func uploadStorage(t *testing.T, o Options, vault string, cat map[string]Object) {
	t.Helper()
	names := make([]string, 0, len(cat))
	var objs []Object
	for name, obj := range cat {
		names = append(names, name)
		objs = append(objs, obj)
	}
	sort.Strings(names)
	have, _, err := HaveObjects(t.Context(), o, objs)
	if err != nil {
		t.Fatalf("HaveObjects: %v", err)
	}
	for _, name := range names {
		obj := cat[name]
		if have[obj.SHA256] {
			continue
		}
		if err := PutObject(t.Context(), o, obj, filepath.Join(vault, "storage", filepath.FromSlash(name))); err != nil {
			t.Fatalf("PutObject %s: %v", name, err)
		}
	}
}

// fetchStorage is the fetch step: every catalog entry whose bytes are absent
// or differ from the catalog's sha256 is streamed from the remote into
// storage/<name>. The decision is the catalog's sha, never size or mtime (B2).
func fetchStorage(t *testing.T, o Options, vault string, cat map[string]Object) {
	t.Helper()
	for name, obj := range cat {
		p := filepath.Join(vault, "storage", filepath.FromSlash(name))
		if b, err := os.ReadFile(p); err == nil && objOf(string(b)) == obj {
			continue
		}
		var buf bytes.Buffer
		if err := GetObject(t.Context(), o, obj, &buf); err != nil {
			t.Fatalf("GetObject %s: %v", name, err)
		}
		if objOf(buf.String()) != obj {
			t.Fatalf("GetObject %s returned bytes that do not match the catalog", name)
		}
		put(t, vault, "storage/"+name, buf.String())
	}
}

// remoteObjects lists the object files under <remote>.storage/objects as
// "<sha[0:2]>/<sha[2:]>", sorted.
func remoteObjects(t *testing.T, storeRoot string) []string {
	t.Helper()
	var out []string
	root := filepath.Join(storeRoot, "objects")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// TestSyncStorageBidirectional keeps 062's scenario — two vaults sharing a
// local remote exchange stored files in both directions, and a changed file
// reaches the other side — over 066's transport: the remote holds
// <remote>.storage/objects/<sha[0:2]>/<sha[2:]>, never the file names (A-066-2).
func TestSyncStorageBidirectional(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	remoteStorageDir := p.bare + ".storage"
	oa, ob := opts(p.a, p.remote), opts(p.b, p.remote)

	// 1. Vault A stores two files and uploads them.
	put(t, p.a, "storage/fileA.txt", "content from vault A\n")
	put(t, p.a, "storage/sub/nestedA.txt", "nested content from vault A\n")
	catA := storageCatalog(t, p.a)
	uploadStorage(t, oa, p.a, catA)

	// The remote holds objects by hash and no file by name.
	for name, content := range map[string]string{"fileA.txt": "content from vault A\n", "sub/nestedA.txt": "nested content from vault A\n"} {
		obj := catA[name]
		if got := readFile(t, objFile(remoteStorageDir, obj)); got != content {
			t.Fatalf("object for %s = %q, want %q", name, got, content)
		}
		if exists(filepath.Join(remoteStorageDir, filepath.FromSlash(name))) {
			t.Fatalf("the remote holds %s by name; 066 keeps only objects/", name)
		}
	}
	if n := len(remoteObjects(t, remoteStorageDir)); n != 2 {
		t.Fatalf("remote has %d objects, want 2", n)
	}

	// 2. Vault B stores a file, takes A's catalog entries, fetches them, uploads its own.
	put(t, p.b, "storage/fileB.txt", "content from vault B\n")
	catB := mergeCatalogs(catA, storageCatalog(t, p.b))
	fetchStorage(t, ob, p.b, catB)
	uploadStorage(t, ob, p.b, catB)

	if got := readFile(t, filepath.Join(p.b, "storage", "fileA.txt")); got != "content from vault A\n" {
		t.Fatalf("p.b storage/fileA.txt = %q, want content from vault A", got)
	}
	if got := readFile(t, filepath.Join(p.b, "storage", "sub", "nestedA.txt")); got != "nested content from vault A\n" {
		t.Fatalf("p.b storage/sub/nestedA.txt = %q, want nested content from vault A", got)
	}
	if got := readFile(t, objFile(remoteStorageDir, catB["fileB.txt"])); got != "content from vault B\n" {
		t.Fatalf("remote object for fileB.txt = %q, want content from vault B", got)
	}
	if n := len(remoteObjects(t, remoteStorageDir)); n != 3 {
		t.Fatalf("remote has %d objects, want 3", n)
	}

	// 3. Vault A fetches fileB.txt.
	fetchStorage(t, oa, p.a, catB)
	if got := readFile(t, filepath.Join(p.a, "storage", "fileB.txt")); got != "content from vault B\n" {
		t.Fatalf("p.a storage/fileB.txt = %q, want content from vault B", got)
	}

	// 4. A longer replacement propagates; the old object stays (objects are write-once).
	oldObj := catA["fileA.txt"]
	updated := "updated much longer content from vault A\n"
	put(t, p.a, "storage/fileA.txt", updated)
	catA2 := mergeCatalogs(catB, storageCatalog(t, p.a))
	uploadStorage(t, oa, p.a, catA2)
	if got := readFile(t, objFile(remoteStorageDir, catA2["fileA.txt"])); got != updated {
		t.Fatalf("remote object after update = %q, want %q", got, updated)
	}
	if got := readFile(t, objFile(remoteStorageDir, oldObj)); got != "content from vault A\n" {
		t.Fatalf("the replaced object was altered or deleted: %q", got)
	}
	fetchStorage(t, ob, p.b, catA2)
	if got := readFile(t, filepath.Join(p.b, "storage", "fileA.txt")); got != updated {
		t.Fatalf("p.b fileA.txt after update = %q, want %q", got, updated)
	}

	// 5. B2: a replacement of the SAME size and mtime still propagates, because
	// the catalog's sha256 — not size or mtime — decides.
	sameSize := "updated much longer content from vault Z\n"
	if len(sameSize) != len(updated) {
		t.Fatal("test setup: replacement must have the same size")
	}
	put(t, p.a, "storage/fileA.txt", sameSize)
	stamp := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	for _, v := range []string{p.a, p.b} {
		if err := os.Chtimes(filepath.Join(v, "storage", "fileA.txt"), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	catA3 := mergeCatalogs(catA2, storageCatalog(t, p.a))
	uploadStorage(t, oa, p.a, catA3)
	fetchStorage(t, ob, p.b, catA3)
	if got := readFile(t, filepath.Join(p.b, "storage", "fileA.txt")); got != sameSize {
		t.Fatalf("p.b fileA.txt after a same-size replacement = %q, want %q", got, sameSize)
	}
}

// TestSyncStorageNoRemotesOrNoStorage keeps 062's no-op scenarios over the
// object API: nothing to ask is nothing to do, and asking a remote that has no
// storage creates nothing on either side.
func TestSyncStorageNoRemotesOrNoStorage(t *testing.T) {
	hermetic(t)
	ctx := t.Context()

	// No remotes, nothing to ask: no error, no network.
	if have, _, err := HaveObjects(ctx, Options{Dir: t.TempDir()}, nil); err != nil || len(have) != 0 {
		t.Fatalf("HaveObjects with no remotes and no objects: %v, %v", have, err)
	}

	// Neither local nor remote has storage: the question is answered "none"
	// and creates nothing.
	vault := makeVault(t)
	remote := bareRemote(t)
	have, _, err := HaveObjects(ctx, opts(vault, remote), []Object{objOf("x")})
	if err != nil || len(have) != 0 {
		t.Fatalf("HaveObjects with no storage: %v, %v", have, err)
	}
	if _, err := os.Stat(filepath.Join(vault, "storage")); !os.IsNotExist(err) {
		t.Fatalf("expected vault/storage not to exist, got err: %v", err)
	}
	if _, err := os.Stat(remote + ".storage"); !os.IsNotExist(err) {
		t.Fatalf("expected remote.storage not to exist, got err: %v", err)
	}
}

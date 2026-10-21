package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncStorage verifies that lw sync synchronizes storage files between two
// vaults sharing a local remote, that .llmwiki/storage.json is tracked by git,
// and that git history contains zero blobs from storage/.
func TestSyncStorage(t *testing.T) {
	a, b, remote := syncPair(t)

	// 1. Add a file to storage on PC A using `lw storage add`.
	tempDir := t.TempDir()
	docA := filepath.Join(tempDir, "sample.pdf")
	docAContent := []byte("%PDF-1.4 sample pdf content for storage\n")
	if err := os.WriteFile(docA, docAContent, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := a.lw("storage", "add", docA, "--name", "docs/sample.pdf", "-m", "first stored pdf", "--vault", a.root)
	if code != 0 {
		t.Fatalf("storage add: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Verify local storage file and manifest exist on PC A.
	localDocA := filepath.Join(a.root, "storage", "docs", "sample.pdf")
	if data, err := os.ReadFile(localDocA); err != nil || string(data) != string(docAContent) {
		t.Fatalf("local storage file on A = %q, err: %v", string(data), err)
	}
	manifestA := filepath.Join(a.root, ".llmwiki", "storage.json")
	if data, err := os.ReadFile(manifestA); err != nil || !strings.Contains(string(data), "docs/sample.pdf") {
		t.Fatalf("manifest on A missing entry, data: %s, err: %v", string(data), err)
	}

	// 2. Run `lw sync` on PC A to commit .llmwiki/storage.json and push storage files.
	stdout, stderr, code = a.lw("sync", "--vault", a.root)
	if code != 0 {
		t.Fatalf("sync on A: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Verify that remote storage directory contains the storage file.
	remoteStorageFile := filepath.Join(remote+".storage", "docs", "sample.pdf")
	if data, err := os.ReadFile(remoteStorageFile); err != nil || string(data) != string(docAContent) {
		t.Fatalf("remote storage file = %q, err: %v", string(data), err)
	}

	// 3. Verify git history on PC A:
	// - Contains .llmwiki/storage.json
	// - Contains ZERO blobs from storage/
	logFiles := a.git("log", "--name-only", "--pretty=format:")
	if !strings.Contains(logFiles, ".llmwiki/storage.json") {
		t.Fatalf("git log does not contain .llmwiki/storage.json:\n%s", logFiles)
	}

	for _, line := range strings.Split(logFiles, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "storage/") || line == "storage" {
			t.Fatalf("git log contains file from storage/: %s", line)
		}
	}

	treeFiles := a.git("ls-tree", "-r", "--name-only", "HEAD")
	for _, line := range strings.Split(treeFiles, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "storage/") || line == "storage" {
			t.Fatalf("git tree at HEAD contains storage file: %s", line)
		}
	}

	// 4. Run `lw sync` on PC B to pull git commits and storage files.
	stdout, stderr, code = b.lw("sync", "--vault", b.root)
	if code != 0 {
		t.Fatalf("sync on B: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Verify PC B received both the manifest and the storage file.
	bDocA := filepath.Join(b.root, "storage", "docs", "sample.pdf")
	if data, err := os.ReadFile(bDocA); err != nil || string(data) != string(docAContent) {
		t.Fatalf("PC B storage file = %q, err: %v", string(data), err)
	}
	bManifest := filepath.Join(b.root, ".llmwiki", "storage.json")
	if data, err := os.ReadFile(bManifest); err != nil || !strings.Contains(string(data), "docs/sample.pdf") {
		t.Fatalf("PC B manifest missing entry, data: %s, err: %v", string(data), err)
	}

	// 5. Bidirectional: Add storage file on PC B and sync back to PC A.
	docB := filepath.Join(tempDir, "data.bin")
	docBContent := []byte("binary data from PC B\n")
	if err := os.WriteFile(docB, docBContent, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code = b.lw("storage", "add", docB, "--name", "data.bin", "--vault", b.root)
	if code != 0 {
		t.Fatalf("storage add on B: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	stdout, stderr, code = b.lw("sync", "--vault", b.root)
	if code != 0 {
		t.Fatalf("sync on B after adding data.bin: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Remote now has data.bin.
	remoteDocB := filepath.Join(remote+".storage", "data.bin")
	if data, err := os.ReadFile(remoteDocB); err != nil || string(data) != string(docBContent) {
		t.Fatalf("remote storage data.bin = %q, err: %v", string(data), err)
	}

	// Sync PC A to pull data.bin.
	stdout, stderr, code = a.lw("sync", "--vault", a.root)
	if code != 0 {
		t.Fatalf("sync on A to pull from B: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	aDocB := filepath.Join(a.root, "storage", "data.bin")
	if data, err := os.ReadFile(aDocB); err != nil || string(data) != string(docBContent) {
		t.Fatalf("PC A storage data.bin = %q, err: %v", string(data), err)
	}

	// 6. Test clone receives storage files via syncClone.
	c := newSyncPC(t, "c")
	stdout, stderr, code = c.lw("sync", "clone", remote, c.root)
	if code != 0 {
		t.Fatalf("sync clone on C: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	cDocA := filepath.Join(c.root, "storage", "docs", "sample.pdf")
	if data, err := os.ReadFile(cDocA); err != nil || string(data) != string(docAContent) {
		t.Fatalf("cloned PC C docs/sample.pdf = %q, err: %v", string(data), err)
	}
	cDocB := filepath.Join(c.root, "storage", "data.bin")
	if data, err := os.ReadFile(cDocB); err != nil || string(data) != string(docBContent) {
		t.Fatalf("cloned PC C data.bin = %q, err: %v", string(data), err)
	}
}

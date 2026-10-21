package storage_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/storage"
)

func createTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for test file: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	return p
}

func fileSHA256(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

func TestOpen(t *testing.T) {
	if _, err := storage.Open(""); err == nil {
		t.Error("Open(\"\") expected error, got nil")
	}

	tmpDir := t.TempDir()
	st, err := storage.Open(tmpDir)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", tmpDir, err)
	}
	if st == nil {
		t.Fatal("Open returned nil storage")
	}
}

func TestCleanName(t *testing.T) {
	st, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"empty", "", "", true},
		{"null byte", "foo\x00bar", "", true},
		{"contains dot dot", "../foo", "", true},
		{"embedded dot dot", "foo/../bar", "", true},
		{"trailing dot dot", "foo/..", "", true},
		{"leading slash", "/etc/passwd", "", true},
		{"simple filename", "dataset.csv", "dataset.csv", false},
		{"subdir", "data/nested/file.txt", "data/nested/file.txt", false},
		{"dot slash prefix", "./data/file.txt", "data/file.txt", false},
		{"redundant slashes", "data///file.txt", "data/file.txt", false},
		{"dot component", "data/./file.txt", "data/file.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := st.CleanName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CleanName(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("CleanName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAdd_NewEntry(t *testing.T) {
	vaultDir := t.TempDir()
	st, err := storage.Open(vaultDir)
	if err != nil {
		t.Fatal(err)
	}

	srcDir := t.TempDir()
	content := "col1,col2\nval1,val2\n"
	srcFile := createTestFile(t, srcDir, "original.csv", content)

	entry, err := st.Add(srcFile, "custom/data.csv", "sample dataset", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if entry.Name != "custom/data.csv" {
		t.Errorf("entry.Name = %q, want custom/data.csv", entry.Name)
	}
	if entry.Note != "sample dataset" {
		t.Errorf("entry.Note = %q, want 'sample dataset'", entry.Note)
	}
	if entry.Size != int64(len(content)) {
		t.Errorf("entry.Size = %d, want %d", entry.Size, len(content))
	}
	wantSHA := fileSHA256(content)
	if entry.SHA256 != wantSHA {
		t.Errorf("entry.SHA256 = %q, want %q", entry.SHA256, wantSHA)
	}

	// Verify file exists on disk
	storedPath := filepath.Join(vaultDir, "storage", "custom", "data.csv")
	storedBytes, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatalf("failed to read stored file: %v", err)
	}
	if string(storedBytes) != content {
		t.Errorf("stored content = %q, want %q", string(storedBytes), content)
	}

	// Verify manifest
	list, err := st.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d entries, want 1", len(list))
	}
	if list[0].Name != "custom/data.csv" {
		t.Errorf("list[0].Name = %q, want custom/data.csv", list[0].Name)
	}
}

func TestAdd_DefaultName(t *testing.T) {
	vaultDir := t.TempDir()
	st, err := storage.Open(vaultDir)
	if err != nil {
		t.Fatal(err)
	}

	srcFile := createTestFile(t, t.TempDir(), "archive.tar.gz", "tarball content")
	entry, err := st.Add(srcFile, "", "", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if entry.Name != "archive.tar.gz" {
		t.Errorf("entry.Name = %q, want archive.tar.gz", entry.Name)
	}
}

func TestAdd_DedupSameSHA(t *testing.T) {
	vaultDir := t.TempDir()
	st, err := storage.Open(vaultDir)
	if err != nil {
		t.Fatal(err)
	}

	srcFile := createTestFile(t, t.TempDir(), "blob.bin", "same content")
	e1, err := st.Add(srcFile, "", "initial note", false)
	if err != nil {
		t.Fatalf("Add 1 failed: %v", err)
	}

	// Add again with updated note
	e2, err := st.Add(srcFile, "", "updated note", false)
	if err != nil {
		t.Fatalf("Add 2 failed: %v", err)
	}
	if e2.Note != "updated note" {
		t.Errorf("e2.Note = %q, want 'updated note'", e2.Note)
	}
	if e2.SHA256 != e1.SHA256 {
		t.Errorf("SHA256 changed: %s -> %s", e1.SHA256, e2.SHA256)
	}

	list, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(list))
	}
	if list[0].Note != "updated note" {
		t.Errorf("list[0].Note = %q, want 'updated note'", list[0].Note)
	}
}

func TestAdd_CollisionDifferentSHA(t *testing.T) {
	vaultDir := t.TempDir()
	st, err := storage.Open(vaultDir)
	if err != nil {
		t.Fatal(err)
	}

	file1 := createTestFile(t, t.TempDir(), "doc.txt", "version 1")
	file2 := createTestFile(t, t.TempDir(), "doc.txt", "version 2 (different)")

	_, err = st.Add(file1, "doc.txt", "v1", false)
	if err != nil {
		t.Fatalf("Add file1 failed: %v", err)
	}

	// Collision without force should fail
	_, err = st.Add(file2, "doc.txt", "v2", false)
	if err == nil {
		t.Fatal("Add file2 without force should have failed with collision error")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("expected error mentioning --force, got %v", err)
	}

	// Collision with force should succeed and overwrite
	e2, err := st.Add(file2, "doc.txt", "v2", true)
	if err != nil {
		t.Fatalf("Add file2 with force failed: %v", err)
	}
	if e2.SHA256 != fileSHA256("version 2 (different)") {
		t.Errorf("e2.SHA256 = %s, want v2 sha", e2.SHA256)
	}

	// Verify disk content
	storedBytes, err := os.ReadFile(filepath.Join(vaultDir, "storage", "doc.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(storedBytes) != "version 2 (different)" {
		t.Errorf("stored content = %q, want version 2", string(storedBytes))
	}
}

func TestShowAndGet(t *testing.T) {
	vaultDir := t.TempDir()
	st, err := storage.Open(vaultDir)
	if err != nil {
		t.Fatal(err)
	}

	content := "data payload bytes"
	srcFile := createTestFile(t, t.TempDir(), "test.dat", content)
	_, err = st.Add(srcFile, "sub/test.dat", "binary data", false)
	if err != nil {
		t.Fatal(err)
	}

	// Show existing
	entry, found, err := st.Show("sub/test.dat")
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}
	if !found {
		t.Fatal("Show reported not found")
	}
	if entry.Name != "sub/test.dat" || entry.Note != "binary data" {
		t.Errorf("unexpected entry: %+v", entry)
	}

	// Show nonexistent
	_, found, err = st.Show("nonexistent.dat")
	if err != nil {
		t.Fatalf("Show nonexistent failed: %v", err)
	}
	if found {
		t.Error("Show reported found for nonexistent entry")
	}

	// Get existing
	rc, gotEntry, err := st.Get("sub/test.dat")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(data) != content {
		t.Errorf("read content = %q, want %q", string(data), content)
	}
	if gotEntry.Name != "sub/test.dat" {
		t.Errorf("gotEntry.Name = %q", gotEntry.Name)
	}

	// Get nonexistent
	if _, _, err := st.Get("nonexistent.dat"); err == nil {
		t.Error("Get nonexistent expected error, got nil")
	}
}

func TestRemove(t *testing.T) {
	vaultDir := t.TempDir()
	st, err := storage.Open(vaultDir)
	if err != nil {
		t.Fatal(err)
	}

	srcFile := createTestFile(t, t.TempDir(), "to_remove.txt", "remove me")
	_, err = st.Add(srcFile, "a/b/c/to_remove.txt", "", false)
	if err != nil {
		t.Fatal(err)
	}

	filePath := filepath.Join(vaultDir, "storage", "a", "b", "c", "to_remove.txt")
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file does not exist before remove: %v", err)
	}

	if err := st.Remove("a/b/c/to_remove.txt"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// File should be deleted
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("file still exists on disk after remove: %v", err)
	}

	// Empty parent directories a/b/c should be cleaned up
	if _, err := os.Stat(filepath.Join(vaultDir, "storage", "a")); !os.IsNotExist(err) {
		t.Errorf("empty parent dir 'a' still exists: %v", err)
	}

	// storage/ directory itself must still exist or be allowed to exist
	entries, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after remove, got %d", len(entries))
	}

	// Remove nonexistent entry should return error
	if err := st.Remove("a/b/c/to_remove.txt"); err == nil {
		t.Error("Remove nonexistent expected error, got nil")
	}
}

func TestValidate(t *testing.T) {
	vaultDir := t.TempDir()
	st, err := storage.Open(vaultDir)
	if err != nil {
		t.Fatal(err)
	}

	// Fresh vault: 0 files, ok
	total, ok, issues, err := st.Validate()
	if err != nil {
		t.Fatalf("Validate on fresh vault: %v", err)
	}
	if total != 0 || !ok || len(issues) != 0 {
		t.Errorf("Validate on fresh vault = (total=%d, ok=%v, issues=%v)", total, ok, issues)
	}

	// Add 2 files
	f1 := createTestFile(t, t.TempDir(), "file1.txt", "content one")
	f2 := createTestFile(t, t.TempDir(), "file2.txt", "content two")
	if _, err := st.Add(f1, "file1.txt", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(f2, "dir/file2.txt", "", false); err != nil {
		t.Fatal(err)
	}

	// All healthy
	total, ok, issues, err = st.Validate()
	if err != nil {
		t.Fatalf("Validate healthy: %v", err)
	}
	if total != 2 || !ok || len(issues) != 0 {
		t.Errorf("Validate healthy = (total=%d, ok=%v, issues=%v)", total, ok, issues)
	}

	// Corrupt file1 (sha mismatch)
	if err := os.WriteFile(filepath.Join(vaultDir, "storage", "file1.txt"), []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	total, ok, issues, err = st.Validate()
	if err != nil {
		t.Fatalf("Validate corrupt: %v", err)
	}
	if total != 2 || ok || len(issues) != 1 {
		t.Errorf("Validate corrupt = (total=%d, ok=%v, issues=%v)", total, ok, issues)
	}

	// Remove file2 from disk (missing)
	if err := os.Remove(filepath.Join(vaultDir, "storage", "dir", "file2.txt")); err != nil {
		t.Fatal(err)
	}
	total, ok, issues, err = st.Validate()
	if err != nil {
		t.Fatalf("Validate corrupt+missing: %v", err)
	}
	if total != 2 || ok || len(issues) != 2 {
		t.Errorf("Validate corrupt+missing = (total=%d, ok=%v, issues=%v)", total, ok, issues)
	}
}

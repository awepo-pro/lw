package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/storage"
	"github.com/awepo-pro/lw/internal/testutil"
)

func writeScratchFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return p
}

func TestStorageUsageAndErrors(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	// No subcommands
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"storage"})
	})
	if code != 2 {
		t.Fatalf("run(storage) exit code = %d, want 2; stderr=%q", code, stderr)
	}

	// Unknown subcommand
	_, stderr, code = captureRun(t, func() int {
		return run([]string{"storage", "badsub", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("run(storage badsub) exit code = %d, want 2; stderr=%q", code, stderr)
	}

	// add with missing argument
	_, _, code = captureRun(t, func() int {
		return run([]string{"storage", "add", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("run(storage add without args) exit code = %d, want 2", code)
	}

	// show with missing argument
	_, _, code = captureRun(t, func() int {
		return run([]string{"storage", "show", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("run(storage show without args) exit code = %d, want 2", code)
	}

	// get with missing argument
	_, _, code = captureRun(t, func() int {
		return run([]string{"storage", "get", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("run(storage get without args) exit code = %d, want 2", code)
	}

	// rm with missing argument
	_, _, code = captureRun(t, func() int {
		return run([]string{"storage", "rm", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("run(storage rm without args) exit code = %d, want 2", code)
	}

	// help
	stdout, _, code := captureRun(t, func() int {
		return run([]string{"storage", "--help"})
	})
	if code != 0 {
		t.Fatalf("run(storage --help) exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "usage: lw storage") {
		t.Errorf("stdout = %q, want usage", stdout)
	}
}

func TestStorageAddAndList(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	content := "header1,header2\nval1,val2\n"
	srcFile := writeScratchFile(t, t.TempDir(), "input.csv", content)

	// Add file
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"storage", "add", srcFile, "-m", "benchmark set", "--name", "data/set.csv", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("storage add failed: code=%d, stderr=%q, stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "stored data/set.csv") {
		t.Errorf("stdout = %q, want 'stored data/set.csv'", stdout)
	}

	// Verify file on disk
	storedPath := filepath.Join(root, "storage", "data", "set.csv")
	storedBytes, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatalf("failed to read stored file: %v", err)
	}
	if string(storedBytes) != content {
		t.Errorf("content = %q, want %q", string(storedBytes), content)
	}

	// List table
	stdout, _, code = captureRun(t, func() int {
		return run([]string{"storage", "list", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("storage list failed: code=%d", code)
	}
	if !strings.Contains(stdout, "data/set.csv") || !strings.Contains(stdout, "benchmark set") {
		t.Errorf("list output missing entry:\n%s", stdout)
	}

	// List -v
	stdout, _, code = captureRun(t, func() int {
		return run([]string{"storage", "list", "-v", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("storage list -v failed: code=%d", code)
	}
	if !strings.Contains(stdout, "present") || !strings.Contains(stdout, "STATUS") {
		t.Errorf("list -v output missing status:\n%s", stdout)
	}

	// List --json
	stdout, _, code = captureRun(t, func() int {
		return run([]string{"storage", "list", "--json", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("storage list --json failed: code=%d", code)
	}
	var entries []storage.Entry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("failed to parse json list: %v\n%s", err, stdout)
	}
	if len(entries) != 1 || entries[0].Name != "data/set.csv" {
		t.Errorf("unexpected json entries: %+v", entries)
	}
}

func TestStorageDedupAndCollision(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	fileA := writeScratchFile(t, t.TempDir(), "fileA.txt", "same content")
	fileB := writeScratchFile(t, t.TempDir(), "fileB.txt", "different content")

	// Add fileA
	_, _, code := captureRun(t, func() int {
		return run([]string{"storage", "add", fileA, "--name", "target.txt", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("add fileA failed with code %d", code)
	}

	// Re-add fileA (same sha256) is no-op
	stdout, _, code := captureRun(t, func() int {
		return run([]string{"storage", "add", fileA, "--name", "target.txt", "-m", "updated note", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("re-add fileA failed with code %d", code)
	}
	if !strings.Contains(stdout, "stored target.txt") {
		t.Errorf("stdout = %q", stdout)
	}

	// Add fileB with same name without force: collision error
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"storage", "add", fileB, "--name", "target.txt", "--vault", root})
	})
	if code != 1 {
		t.Fatalf("add collision without force exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("stderr = %q, want --force mention", stderr)
	}

	// Add fileB with --force: succeeds
	_, _, code = captureRun(t, func() int {
		return run([]string{"storage", "add", fileB, "--name", "target.txt", "--force", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("add collision with --force failed with code %d", code)
	}

	// Verify disk has fileB content
	b, err := os.ReadFile(filepath.Join(root, "storage", "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "different content" {
		t.Errorf("stored content = %q, want 'different content'", string(b))
	}
}

func TestStorageShow(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	f := writeScratchFile(t, t.TempDir(), "doc.pdf", "fake pdf header and bytes")

	_, _, code := captureRun(t, func() int {
		return run([]string{"storage", "add", f, "-m", "important doc", "--name", "doc.pdf", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("add failed with code %d", code)
	}

	// show existing
	stdout, _, code := captureRun(t, func() int {
		return run([]string{"storage", "show", "doc.pdf", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("show failed with code %d", code)
	}
	if !strings.Contains(stdout, "Name:    doc.pdf") ||
		!strings.Contains(stdout, "Path:    storage/doc.pdf") ||
		!strings.Contains(stdout, "Status:  present") ||
		!strings.Contains(stdout, "Note:    important doc") {
		t.Errorf("show output unexpected:\n%s", stdout)
	}

	// show nonexistent
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"storage", "show", "nonexistent.pdf", "--vault", root})
	})
	if code != 1 {
		t.Fatalf("show nonexistent code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr = %q, want 'not found'", stderr)
	}
}

func TestStorageGet(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	content := "hello world from storage locker"
	f := writeScratchFile(t, t.TempDir(), "hello.txt", content)

	_, _, code := captureRun(t, func() int {
		return run([]string{"storage", "add", f, "--name", "hello.txt", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("add failed with code %d", code)
	}

	// get to stdout
	stdout, _, code := captureRun(t, func() int {
		return run([]string{"storage", "get", "hello.txt", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("get to stdout failed with code %d", code)
	}
	if stdout != content {
		t.Errorf("stdout = %q, want %q", stdout, content)
	}

	// get to file with -o
	outPath := filepath.Join(t.TempDir(), "out", "retrieved.txt")
	_, _, code = captureRun(t, func() int {
		return run([]string{"storage", "get", "hello.txt", "-o", outPath, "--vault", root})
	})
	if code != 0 {
		t.Fatalf("get with -o failed with code %d", code)
	}
	outBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read retrieved file: %v", err)
	}
	if string(outBytes) != content {
		t.Errorf("retrieved file content = %q, want %q", string(outBytes), content)
	}
}

func TestStorageRemove(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	f := writeScratchFile(t, t.TempDir(), "delete_me.dat", "temp data")

	_, _, code := captureRun(t, func() int {
		return run([]string{"storage", "add", f, "--name", "dir/sub/delete_me.dat", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("add failed with code %d", code)
	}

	// rm existing
	stdout, _, code := captureRun(t, func() int {
		return run([]string{"storage", "rm", "dir/sub/delete_me.dat", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("rm failed with code %d", code)
	}
	if !strings.Contains(stdout, "removed dir/sub/delete_me.dat") {
		t.Errorf("stdout = %q, want 'removed dir/sub/delete_me.dat'", stdout)
	}

	// Check file and directories are gone
	filePath := filepath.Join(root, "storage", "dir", "sub", "delete_me.dat")
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "storage", "dir")); !os.IsNotExist(err) {
		t.Errorf("parent directory still exists: %v", err)
	}

	// List is empty
	stdout, _, code = captureRun(t, func() int {
		return run([]string{"storage", "list", "--json", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("list --json failed: code=%d", code)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("list --json = %q, want '[]'", stdout)
	}

	// rm nonexistent fails
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"storage", "rm", "dir/sub/delete_me.dat", "--vault", root})
	})
	if code != 1 {
		t.Fatalf("rm nonexistent code = %d, want 1; stderr=%q", code, stderr)
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

type fileMeta struct {
	size    int64
	modTime time.Time
}

func snapshotVault(t *testing.T, root string) map[string]fileMeta {
	t.Helper()
	out := make(map[string]fileMeta)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if relSlash == ".llmwiki/logs" || strings.HasPrefix(relSlash, ".llmwiki/logs/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		out[relSlash] = fileMeta{
			size:    info.Size(),
			modTime: info.ModTime(),
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotVault: %v", err)
	}
	return out
}

func assertVaultUnchanged(t *testing.T, before, after map[string]fileMeta) {
	t.Helper()
	if len(before) != len(after) {
		t.Errorf("vault file count changed: before %d, after %d", len(before), len(after))
	}
	for p, metaBefore := range before {
		metaAfter, ok := after[p]
		if !ok {
			t.Errorf("file deleted: %s", p)
			continue
		}
		if metaBefore.size != metaAfter.size {
			t.Errorf("file %s size changed: %d -> %d", p, metaBefore.size, metaAfter.size)
		}
		if !metaBefore.modTime.Equal(metaAfter.modTime) {
			t.Errorf("file %s mtime changed: %v -> %v", p, metaBefore.modTime, metaAfter.modTime)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("file added: %s", p)
		}
	}
}

// TestGraphExitCodes verifies exit 2 on bad format and depth, and exit 1 on unknown focus.
func TestGraphExitCodes(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	// Unknown format -> exit 2
	_, _, code := captureRun(t, func() int {
		return run([]string{"graph", "--format", "bogus", "--vault", root})
	})
	if code != 2 {
		t.Errorf("bad format exit code = %d, want 2", code)
	}

	// Depth 0 -> exit 2
	_, _, code = captureRun(t, func() int {
		return run([]string{"graph", "--depth", "0", "--vault", root})
	})
	if code != 2 {
		t.Errorf("depth 0 exit code = %d, want 2", code)
	}

	// Depth 6 -> exit 2
	_, _, code = captureRun(t, func() int {
		return run([]string{"graph", "--depth", "6", "--vault", root})
	})
	if code != 2 {
		t.Errorf("depth 6 exit code = %d, want 2", code)
	}

	// Unknown focus -> exit 1, stderr says `lw graph: no page "<arg>"`
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"graph", "--focus", "nonexistent-page", "--vault", root})
	})
	if code != 1 {
		t.Errorf("unknown focus exit code = %d, want 1", code)
	}
	wantMsg := "lw graph: no page \"nonexistent-page\"\n"
	if stderr != wantMsg {
		t.Errorf("unknown focus stderr = %q, want %q", stderr, wantMsg)
	}
}

// TestGraphDOTOutput verifies that DOT output starts with `digraph lw {` on a fixture copy.
func TestGraphDOTOutput(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"graph", "--format", "dot", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "digraph lw {\n") {
		t.Errorf("stdout does not start with digraph lw {:\n%s", stdout)
	}
}

// TestGraphVaultUnchanged verifies that running graph leaves the vault completely unchanged.
func TestGraphVaultUnchanged(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	before := snapshotVault(t, root)

	commands := [][]string{
		{"graph", "--vault", root},
		{"graph", "--vault", root, "--format", "dot"},
		{"graph", "--vault", root, "--format", "json"},
		{"graph", "--vault", root, "--format", "mermaid"},
		{"graph", "--vault", root, "--focus", "kv-cache", "--depth", "1"},
	}

	for _, cmd := range commands {
		stdout, stderr, code := captureRun(t, func() int {
			return run(cmd)
		})
		if code != 0 {
			t.Fatalf("cmd %v failed with code %d: stderr=%s stdout=%s", cmd, code, stderr, stdout)
		}
	}

	after := snapshotVault(t, root)
	assertVaultUnchanged(t, before, after)
}

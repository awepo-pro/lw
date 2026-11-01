package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

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

	assertVaultUnchanged(t, root, before)
}

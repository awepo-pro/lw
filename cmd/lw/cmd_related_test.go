package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/similar"
)

// setupTestVault creates a minimal test vault with known pages for CLI testing.
func setupTestVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	schemaContent := `# SCHEMA

## Domain
ml-systems

## Tags
- inference
- memory
- decoding
`
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}

	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatalf("mkdir concepts: %v", err)
	}

	pages := map[string]string{
		"kv-cache.md": `---
title: KV Cache System
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [inference, memory]
sources: [raw/articles/kv-cache-explained.md]
---

# KV Cache System

The key value cache stores attention projections from earlier decoding steps.
`,
		"key-value-cache.md": `---
title: KV Cache System
created: 2026-08-21
updated: 2026-08-29
type: concept
tags: [decoding]
sources: [raw/articles/kv-cache-explained.md]
---

# KV Cache System

The key value cache stores attention projections from earlier decoding steps.
`,
	}

	for filename, content := range pages {
		p := filepath.Join(conceptsDir, filename)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write page %s: %v", filename, err)
		}
	}

	return dir
}

type relatedFileSnap struct {
	size  int64
	mtime time.Time
}

func takeRelatedVaultSnapshot(t *testing.T, root string) map[string]relatedFileSnap {
	t.Helper()
	snap := make(map[string]relatedFileSnap)
	err := filepath.Walk(root, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		// Ignore .llmwiki/logs directory
		relSlash := filepath.ToSlash(rel)
		if relSlash == ".llmwiki/logs" || strings.HasPrefix(relSlash, ".llmwiki/logs/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() {
			snap[relSlash] = relatedFileSnap{
				size:  info.Size(),
				mtime: info.ModTime(),
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("take snapshot: %v", err)
	}
	return snap
}

// TestRelatedUnknownPage tests that querying an unknown page exits 1 with a message on stderr.
func TestRelatedUnknownPage(t *testing.T) {
	root := setupTestVault(t)
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"related", "--vault", root, "nonexistent-page"})
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	wantMsg := `lw related: no page "nonexistent-page"`
	if !strings.Contains(stderr, wantMsg) {
		t.Errorf("stderr = %q, want containing %q", stderr, wantMsg)
	}
}

// TestRelatedInvalidN tests that -n 0 exits 2.
func TestRelatedInvalidN(t *testing.T) {
	root := setupTestVault(t)
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"related", "--vault", root, "-n", "0", "kv-cache"})
	})

	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

// TestDupesInvalidFlags tests that invalid thresholds and -n 0 exit 2.
func TestDupesInvalidFlags(t *testing.T) {
	root := setupTestVault(t)

	// --threshold 0 -> exit 2
	_, _, code0 := captureRun(t, func() int {
		return run([]string{"dupes", "--vault", root, "--threshold", "0"})
	})
	if code0 != 2 {
		t.Errorf("--threshold 0: exit code = %d, want 2", code0)
	}

	// --threshold 1.5 -> exit 2
	_, _, code15 := captureRun(t, func() int {
		return run([]string{"dupes", "--vault", root, "--threshold", "1.5"})
	})
	if code15 != 2 {
		t.Errorf("--threshold 1.5: exit code = %d, want 2", code15)
	}

	// -n 0 -> exit 2
	_, _, codeN := captureRun(t, func() int {
		return run([]string{"dupes", "--vault", root, "-n", "0"})
	})
	if codeN != 2 {
		t.Errorf("-n 0: exit code = %d, want 2", codeN)
	}
}

// TestRelatedTextAndJSON tests text and --json outputs for related.
func TestRelatedTextAndJSON(t *testing.T) {
	root := setupTestVault(t)

	// Text output
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"related", "--vault", root, "kv-cache"})
	})
	if code != 0 {
		t.Fatalf("related text: exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "related to wiki/concepts/kv-cache.md (KV Cache System)") {
		t.Errorf("missing header in stdout: %q", stdout)
	}
	if !strings.Contains(stdout, "wiki/concepts/key-value-cache.md") {
		t.Errorf("missing related page in stdout: %q", stdout)
	}

	// JSON output
	jsonOut, jsonErr, code := captureRun(t, func() int {
		return run([]string{"related", "--vault", root, "--json", "kv-cache"})
	})
	if code != 0 {
		t.Fatalf("related --json: exit code = %d, want 0; stderr=%q", code, jsonErr)
	}
	var matches []similar.Match
	if err := json.Unmarshal([]byte(jsonOut), &matches); err != nil {
		t.Fatalf("unmarshal json: %v\noutput was:\n%s", err, jsonOut)
	}
	if len(matches) == 0 {
		t.Errorf("expected matches in JSON output, got 0")
	}
}

// TestDupesTextAndJSON tests text and --json outputs for dupes.
func TestDupesTextAndJSON(t *testing.T) {
	root := setupTestVault(t)

	// Text output
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"dupes", "--vault", root, "--threshold", "0.45"})
	})
	if code != 0 {
		t.Fatalf("dupes text: exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "<->") {
		t.Errorf("expected '<->' in stdout: %q", stdout)
	}
	if !strings.Contains(stdout, `"KV Cache System" / "KV Cache System"`) {
		t.Errorf("missing titles in stdout: %q", stdout)
	}

	// JSON output
	jsonOut, jsonErr, code := captureRun(t, func() int {
		return run([]string{"dupes", "--vault", root, "--threshold", "0.45", "--json"})
	})
	if code != 0 {
		t.Fatalf("dupes --json: exit code = %d, want 0; stderr=%q", code, jsonErr)
	}
	var pairs []similar.Pair
	if err := json.Unmarshal([]byte(jsonOut), &pairs); err != nil {
		t.Fatalf("unmarshal json: %v\noutput was:\n%s", err, jsonOut)
	}
	if len(pairs) == 0 {
		t.Errorf("expected pairs in JSON output, got 0")
	}
}

// TestRelatedAndDupesVaultUnchanged tests that running related and dupes leaves the vault unchanged.
func TestRelatedAndDupesVaultUnchanged(t *testing.T) {
	root := setupTestVault(t)
	before := takeRelatedVaultSnapshot(t, root)

	captureRun(t, func() int {
		return run([]string{"related", "--vault", root, "kv-cache"})
	})
	captureRun(t, func() int {
		return run([]string{"dupes", "--vault", root, "--threshold", "0.45"})
	})

	after := takeRelatedVaultSnapshot(t, root)

	if len(before) != len(after) {
		t.Fatalf("vault file count changed: before %d, after %d", len(before), len(after))
	}

	for p, bSnap := range before {
		aSnap, ok := after[p]
		if !ok {
			t.Errorf("file %s was removed", p)
			continue
		}
		if bSnap.size != aSnap.size {
			t.Errorf("file %s size changed: %d -> %d", p, bSnap.size, aSnap.size)
		}
		if !bSnap.mtime.Equal(aSnap.mtime) {
			t.Errorf("file %s modtime changed: %v -> %v", p, bSnap.mtime, aSnap.mtime)
		}
	}
}

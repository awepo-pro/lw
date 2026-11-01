package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// TestDupesIncludeLinked tests that --include-linked reveals pairs that already link each other.
func TestDupesIncludeLinked(t *testing.T) {
	root := setupTestVault(t)

	// Add a linked page that shares high similarity with kv-cache
	linkedPath := filepath.Join(root, "wiki", "concepts", "paged-cache.md")
	linkedContent := `---
title: KV Cache System
created: 2026-08-22
updated: 2026-08-29
type: concept
---
# KV Cache System
The key value cache stores attention projections from earlier decoding steps for [[kv-cache]].
`
	if err := os.WriteFile(linkedPath, []byte(linkedContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Default: should not contain the linked pair (paged-cache <-> kv-cache)
	stdoutDefault, _, code := captureRun(t, func() int {
		return run([]string{"dupes", "--vault", root, "--threshold", "0.45"})
	})
	if code != 0 {
		t.Fatalf("dupes default exit %d", code)
	}
	if strings.Contains(stdoutDefault, "kv-cache.md  <->  wiki/concepts/paged-cache.md") ||
		strings.Contains(stdoutDefault, "paged-cache.md  <->  wiki/concepts/kv-cache.md") {
		t.Errorf("default dupes should not show linked pair:\n%s", stdoutDefault)
	}

	// With --include-linked: should contain the linked pair with [already linked]
	stdoutLinked, _, code := captureRun(t, func() int {
		return run([]string{"dupes", "--vault", root, "--threshold", "0.45", "--include-linked"})
	})
	if code != 0 {
		t.Fatalf("dupes --include-linked exit %d", code)
	}
	hasLinkedPair := strings.Contains(stdoutLinked, "kv-cache.md  <->  wiki/concepts/paged-cache.md") ||
		strings.Contains(stdoutLinked, "paged-cache.md  <->  wiki/concepts/kv-cache.md")
	if !hasLinkedPair || !strings.Contains(stdoutLinked, "[already linked]") {
		t.Errorf("expected linked pair and '[already linked]' with --include-linked:\n%s", stdoutLinked)
	}
}

// TestRelatedAndDupesVaultUnchanged tests that running related and dupes leaves the vault unchanged.
func TestRelatedAndDupesVaultUnchanged(t *testing.T) {
	root := setupTestVault(t)
	before := snapshotVault(t, root)

	captureRun(t, func() int {
		return run([]string{"related", "--vault", root, "kv-cache"})
	})
	captureRun(t, func() int {
		return run([]string{"dupes", "--vault", root, "--threshold", "0.45"})
	})

	assertVaultUnchanged(t, root, before)
}

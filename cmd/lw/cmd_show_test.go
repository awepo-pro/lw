package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F5: show by path, by path without .md, by basename, by title (different case), by alias: each prints exact bytes, exit 0.
func TestShowResolveVariations(t *testing.T) {
	root := setupSearchTestVault(t)
	expectedBytes, err := os.ReadFile(filepath.Join(root, "wiki/concepts/kv-cache.md"))
	if err != nil {
		t.Fatalf("read expected bytes: %v", err)
	}

	queries := []string{
		"wiki/concepts/kv-cache.md", // path
		"wiki/concepts/kv-cache",    // path without .md
		"kv-cache",                  // basename
		"KV Cache",                  // title exact
		"kv cache",                  // title different case
		"kv",                        // alias
		"key-value cache",           // alias multi-word
	}

	for _, q := range queries {
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"show", "--vault", root, q})
		})
		if code != 0 {
			t.Fatalf("query %q: exit code = %d, want 0; stderr: %s", q, code, stderr)
		}
		if stderr != "" {
			t.Fatalf("query %q: expected empty stderr, got: %s", q, stderr)
		}
		if stdout != string(expectedBytes) {
			t.Fatalf("query %q: stdout did not match exact file bytes", q)
		}
	}
}

// F6: show --links lists outbound (with unresolved link shown as unresolved: <raw>) and backlinks with line numbers.
func TestShowLinks(t *testing.T) {
	root := setupSearchTestVault(t)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"show", "--vault", root, "--links", "kv-cache"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got: %s", stderr)
	}

	if !strings.Contains(stdout, "--- links: wiki/concepts/kv-cache.md") {
		t.Fatalf("missing '--- links: ...' header in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "outbound (3):") {
		t.Fatalf("missing 'outbound (3):' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "  unresolved: unresolved-target") {
		t.Fatalf("missing unresolved target in outbound output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "  wiki/concepts/flash-attention.md") {
		t.Fatalf("missing flash-attention in outbound output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "  wiki/concepts/speculative-decoding.md") {
		t.Fatalf("missing speculative-decoding in outbound output:\n%s", stdout)
	}

	// Backlinks: kv-cache is linked by flash-attention, speculative-decoding, and gpt-4
	if !strings.Contains(stdout, "backlinks (") {
		t.Fatalf("missing 'backlinks (...):' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "wiki/concepts/flash-attention.md:") {
		t.Fatalf("missing flash-attention backlink with line number in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "wiki/concepts/speculative-decoding.md:") {
		t.Fatalf("missing speculative-decoding backlink with line number in output:\n%s", stdout)
	}
}

// F7: show unknown - exit 1, stderr has "no page" and at least one candidate line when search finds something.
func TestShowUnknown(t *testing.T) {
	root := setupSearchTestVault(t)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"show", "--vault", root, "flash"})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got: %q", stdout)
	}
	if !strings.Contains(stderr, `lw show: no page "flash"`) {
		t.Fatalf("expected stderr to contain 'lw show: no page \"flash\"', got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "wiki/concepts/flash-attention.md") {
		t.Fatalf("expected stderr to contain candidate line with flash-attention, got:\n%s", stderr)
	}
}

// F8: show ambiguous title (two pages with the same title) - exit 1, stderr says ambiguous and lists both paths.
func TestShowAmbiguousTitle(t *testing.T) {
	root := setupSearchTestVault(t)

	pageA := `---
title: Shared Title
created: 2026-08-20
updated: 2026-08-20
type: concept
tags: [inference]
sources: [raw/articles/kv-cache-explained.md]
confidence: high
---

# Shared Title A
`
	pageB := `---
title: Shared Title
created: 2026-08-20
updated: 2026-08-20
type: concept
tags: [inference]
sources: [raw/articles/kv-cache-explained.md]
confidence: high
---

# Shared Title B
`
	if err := os.WriteFile(filepath.Join(root, "wiki/concepts/shared-a.md"), []byte(pageA), 0644); err != nil {
		t.Fatalf("write shared-a.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "wiki/concepts/shared-b.md"), []byte(pageB), 0644); err != nil {
		t.Fatalf("write shared-b.md: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"show", "--vault", root, "Shared Title"})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got: %q", stdout)
	}
	if !strings.Contains(stderr, `lw show: "Shared Title" is ambiguous`) {
		t.Fatalf("expected stderr to mention ambiguous, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "wiki/concepts/shared-a.md") || !strings.Contains(stderr, "wiki/concepts/shared-b.md") {
		t.Fatalf("expected stderr to list both paths, got:\n%s", stderr)
	}
}

// F9: show --json parses and has all expected fields.
func TestShowJSON(t *testing.T) {
	root := setupSearchTestVault(t)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"show", "--vault", root, "--json", "kv-cache"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got: %s", stderr)
	}

	var page showPageJSON
	if err := json.Unmarshal([]byte(stdout), &page); err != nil {
		t.Fatalf("unmarshal json: %v; raw stdout:\n%s", err, stdout)
	}

	if page.Path != "wiki/concepts/kv-cache.md" {
		t.Errorf("Path = %q, want wiki/concepts/kv-cache.md", page.Path)
	}
	if page.Title != "KV Cache" {
		t.Errorf("Title = %q, want 'KV Cache'", page.Title)
	}
	if page.Type != "concept" {
		t.Errorf("Type = %q, want 'concept'", page.Type)
	}
	if len(page.Tags) != 2 || page.Tags[0] != "inference" || page.Tags[1] != "memory" {
		t.Errorf("Tags = %+v, want [inference memory]", page.Tags)
	}
	if len(page.Sources) != 1 || page.Sources[0] != "raw/articles/kv-cache-explained.md" {
		t.Errorf("Sources = %+v, want [raw/articles/kv-cache-explained.md]", page.Sources)
	}
	if len(page.Outbound) != 2 || page.Outbound[0] != "wiki/concepts/flash-attention.md" || page.Outbound[1] != "wiki/concepts/speculative-decoding.md" {
		t.Errorf("Outbound = %+v, want resolved [flash-attention speculative-decoding]", page.Outbound)
	}
	if len(page.Broken) != 1 || page.Broken[0] != "unresolved-target" {
		t.Errorf("Broken = %+v, want [unresolved-target]", page.Broken)
	}
	if len(page.Backlinks) == 0 {
		t.Errorf("Backlinks empty, want > 0 backlinks")
	}
	for _, bl := range page.Backlinks {
		if bl.From == "" || bl.Line <= 0 || bl.Context == "" {
			t.Errorf("invalid backlink entry: %+v", bl)
		}
	}
	if !strings.Contains(page.Body, "# KV Cache") {
		t.Errorf("Body missing expected heading, got: %q", page.Body)
	}
}

// F10 part 2: show writes nothing into the vault
func TestShowVaultReadOnly(t *testing.T) {
	root := setupSearchTestVault(t)

	before := takeVaultDirSnapshot(t, root)

	captureRun(t, func() int { return run([]string{"show", "--vault", root, "kv-cache"}) })
	captureRun(t, func() int { return run([]string{"show", "--vault", root, "--links", "kv-cache"}) })
	captureRun(t, func() int { return run([]string{"show", "--vault", root, "--json", "kv-cache"}) })
	captureRun(t, func() int { return run([]string{"show", "--vault", root, "unknown-page"}) })

	after := takeVaultDirSnapshot(t, root)
	compareVaultSnapshots(t, before, after)
}

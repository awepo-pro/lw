package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupSearchTestVault constructs a minimal test vault with at least 3 pages.
func setupSearchTestVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// Copy SCHEMA.md from spec/fixtures/minimal
	fixtureSchema, err := os.ReadFile("spec/fixtures/minimal/SCHEMA.md")
	if err != nil {
		// Try from repo root if running from cmd/lw
		fixtureSchema, err = os.ReadFile("../../spec/fixtures/minimal/SCHEMA.md")
		if err != nil {
			t.Fatalf("read fixture SCHEMA.md: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "SCHEMA.md"), fixtureSchema, 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}

	for _, dir := range []string{
		"wiki/concepts",
		"wiki/entities",
		"raw/articles",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// Page 1: kv-cache with aliases and attention in body
	pageKV := `---
title: KV Cache
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [inference, memory]
sources: [raw/articles/kv-cache-explained.md]
confidence: high
aliases: [kv, key-value cache]
---

# KV Cache

The key/value cache stores per-layer attention projections from previous decoding steps.

## Related

- [[flash-attention]]
- [[speculative-decoding]]
- [[unresolved-target]]
`
	if err := os.WriteFile(filepath.Join(root, "wiki/concepts/kv-cache.md"), []byte(pageKV), 0644); err != nil {
		t.Fatalf("write kv-cache.md: %v", err)
	}

	// Page 2: flash-attention with attention in title and body
	pageFlash := `---
title: Flash Attention
created: 2026-08-21
updated: 2026-08-29
type: concept
tags: [inference, attention]
sources: [raw/articles/flash-attention.md]
confidence: high
---

# Flash Attention

Fast flash attention kernel that integrates with the [[kv-cache]] to reduce memory bandwidth.

## Related

- [[kv-cache]]
`
	if err := os.WriteFile(filepath.Join(root, "wiki/concepts/flash-attention.md"), []byte(pageFlash), 0644); err != nil {
		t.Fatalf("write flash-attention.md: %v", err)
	}

	// Page 3: speculative-decoding
	pageSpec := `---
title: Speculative Decoding
created: 2026-08-22
updated: 2026-08-29
type: concept
tags: [inference, decoding]
sources: [raw/articles/spec-dec.md]
confidence: high
---

# Speculative Decoding

Speculative decoding speeds up inference by having a draft model predict tokens.
Both models access the [[kv-cache]] during verification.
`
	if err := os.WriteFile(filepath.Join(root, "wiki/concepts/speculative-decoding.md"), []byte(pageSpec), 0644); err != nil {
		t.Fatalf("write speculative-decoding.md: %v", err)
	}

	// Page 4: gpt-4 (type entity)
	pageGPT4 := `---
title: GPT-4
created: 2026-08-20
updated: 2026-08-20
type: entity
tags: [inference, llm]
sources: [raw/articles/gpt4.md]
confidence: high
---

# GPT-4

Large language model used for inference tasks with [[kv-cache]].
`
	if err := os.WriteFile(filepath.Join(root, "wiki/entities/gpt-4.md"), []byte(pageGPT4), 0644); err != nil {
		t.Fatalf("write gpt-4.md: %v", err)
	}

	return root
}

// F1: search hit - query matching 2 pages prints both paths, exit 0, the better match first.
func TestSearchHit(t *testing.T) {
	root := setupSearchTestVault(t)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"search", "--vault", root, "attention"})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got: %s", stderr)
	}

	flashIdx := strings.Index(stdout, "wiki/concepts/flash-attention.md")
	kvIdx := strings.Index(stdout, "wiki/concepts/kv-cache.md")
	if flashIdx < 0 || kvIdx < 0 {
		t.Fatalf("expected both paths in stdout, got:\n%s", stdout)
	}
	if flashIdx >= kvIdx {
		t.Fatalf("expected flash-attention (better match) before kv-cache, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "    ") {
		t.Fatalf("expected indented snippet in output, got:\n%s", stdout)
	}
}

// F2: search no hit - exit 1, stderr contains "no pages match", stdout empty.
func TestSearchNoHit(t *testing.T) {
	root := setupSearchTestVault(t)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"search", "--vault", root, "nonexistentqueryword"})
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got: %q", stdout)
	}
	if !strings.Contains(stderr, `no pages match "nonexistentqueryword"`) {
		t.Fatalf("expected stderr to contain 'no pages match \"nonexistentqueryword\"', got: %q", stderr)
	}
}

// F3: search --json - valid JSON array, length == hits, fields present; --json with no hit prints [] exit 1.
func TestSearchJSON(t *testing.T) {
	root := setupSearchTestVault(t)

	// Hits case
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"search", "--vault", root, "--json", "attention"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got: %s", stderr)
	}

	var hits []searchHitJSON
	if err := json.Unmarshal([]byte(stdout), &hits); err != nil {
		t.Fatalf("unmarshal json: %v; raw stdout:\n%s", err, stdout)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	for _, h := range hits {
		if h.Path == "" || h.Title == "" || h.Score <= 0 {
			t.Fatalf("hit missing expected fields: %+v", h)
		}
	}

	// No hits case
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"search", "--vault", root, "--json", "nonexistentqueryword"})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("expected stdout '[]', got: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr in --json no hit, got: %q", stderr)
	}
}

// F4: search filters - --type filters; --tag filters (AND); -n 1 returns one hit; -n 0 exits 2; no query exits 2.
func TestSearchFiltersAndLimit(t *testing.T) {
	root := setupSearchTestVault(t)

	// --type entity: matches only gpt-4
	stdout, _, code := captureRun(t, func() int {
		return run([]string{"search", "--vault", root, "--type", "entity", "inference"})
	})
	if code != 0 {
		t.Fatalf("type filter exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "wiki/entities/gpt-4.md") || strings.Contains(stdout, "wiki/concepts/") {
		t.Fatalf("expected only gpt-4.md with --type entity, got:\n%s", stdout)
	}

	// --tag inference --tag memory: matches only kv-cache
	stdout, _, code = captureRun(t, func() int {
		return run([]string{"search", "--vault", root, "--tag", "inference", "--tag", "memory", "cache"})
	})
	if code != 0 {
		t.Fatalf("tag filter exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "wiki/concepts/kv-cache.md") || strings.Contains(stdout, "wiki/concepts/flash-attention.md") {
		t.Fatalf("expected only kv-cache.md with multiple tags, got:\n%s", stdout)
	}

	// -n 1 returns exactly one hit
	stdout, _, code = captureRun(t, func() int {
		return run([]string{"search", "--vault", root, "-n", "1", "attention"})
	})
	if code != 0 {
		t.Fatalf("-n 1 exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "flash-attention.md") || strings.Contains(stdout, "kv-cache.md") {
		t.Fatalf("expected exactly 1 hit for -n 1, got:\n%s", stdout)
	}

	// -n 0 exits 2
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"search", "--vault", root, "-n", "0", "attention"})
	})
	if code != 2 {
		t.Fatalf("-n 0 exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "must be >= 1") {
		t.Fatalf("expected stderr to mention 'must be >= 1', got: %s", stderr)
	}

	// no query exits 2
	_, stderr, code = captureRun(t, func() int {
		return run([]string{"search", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("no query exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "usage: lw search") {
		t.Fatalf("expected stderr to contain usage, got: %s", stderr)
	}
}

// F10 part 1: search writes nothing into the vault
func TestSearchVaultReadOnly(t *testing.T) {
	root := setupSearchTestVault(t)

	before := snapshotVault(t, root)

	captureRun(t, func() int { return run([]string{"search", "--vault", root, "attention"}) })
	captureRun(t, func() int { return run([]string{"search", "--vault", root, "--json", "attention"}) })
	captureRun(t, func() int { return run([]string{"search", "--vault", root, "nonexistent"}) })
	captureRun(t, func() int { return run([]string{"search", "--vault", root, "-n", "0", "attention"}) })

	assertVaultUnchanged(t, root, before)
}

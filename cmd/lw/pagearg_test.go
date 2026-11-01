package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

func setupPageArgTestVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	schema := `# SCHEMA
## Domain
test-domain

## Tags
- memory
`
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schema), 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}

	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	entitiesDir := filepath.Join(dir, "wiki", "entities")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatalf("mkdir concepts: %v", err)
	}
	if err := os.MkdirAll(entitiesDir, 0755); err != nil {
		t.Fatalf("mkdir entities: %v", err)
	}

	pages := map[string]string{
		"wiki/concepts/flash-attention.md": `---
title: Flash Attention System
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [memory]
aliases: [FA, Fused Attention]
---
# Flash Attention System
Body text.
`,
		"wiki/concepts/kv-cache.md": `---
title: KV Cache System
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [memory]
aliases:
  - Key-Value Cache
  - KVC
---
# KV Cache System
Body text.
`,
		"wiki/concepts/ambig-one.md": `---
title: Duplicate Concept
created: 2026-08-20
updated: 2026-08-29
type: concept
---
# Duplicate Concept
One.
`,
		"wiki/entities/ambig-two.md": `---
title: Duplicate Concept
created: 2026-08-20
updated: 2026-08-29
type: entity
---
# Duplicate Concept
Two.
`,
	}

	for rel, content := range pages {
		p := filepath.Join(dir, rel)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write page %s: %v", rel, err)
		}
	}

	return dir
}

func TestResolvePageArg(t *testing.T) {
	root := setupPageArgTestVault(t)
	v, err := vault.Open(root)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}

	t.Run("resolve by path", func(t *testing.T) {
		p, amb := resolvePageArg(v, "wiki/concepts/kv-cache.md")
		if p == nil || len(amb) > 0 || p.Path != "wiki/concepts/kv-cache.md" {
			t.Fatalf("expected kv-cache.md, got p=%v, amb=%v", p, amb)
		}
	})

	t.Run("resolve by basename", func(t *testing.T) {
		p, amb := resolvePageArg(v, "kv-cache")
		if p == nil || len(amb) > 0 || p.Path != "wiki/concepts/kv-cache.md" {
			t.Fatalf("expected kv-cache.md, got p=%v, amb=%v", p, amb)
		}
	})

	t.Run("resolve by title case-insensitive", func(t *testing.T) {
		p, amb := resolvePageArg(v, "flash attention system")
		if p == nil || len(amb) > 0 || p.Path != "wiki/concepts/flash-attention.md" {
			t.Fatalf("expected flash-attention.md, got p=%v, amb=%v", p, amb)
		}

		p2, _ := resolvePageArg(v, "FLASH ATTENTION SYSTEM")
		if p2 == nil || p2.Path != "wiki/concepts/flash-attention.md" {
			t.Fatalf("expected upper case match, got %v", p2)
		}
	})

	t.Run("resolve by alias case-insensitive", func(t *testing.T) {
		p, amb := resolvePageArg(v, "fa")
		if p == nil || len(amb) > 0 || p.Path != "wiki/concepts/flash-attention.md" {
			t.Fatalf("expected flash-attention.md for alias 'fa', got p=%v, amb=%v", p, amb)
		}

		p2, amb2 := resolvePageArg(v, "key-value cache")
		if p2 == nil || len(amb2) > 0 || p2.Path != "wiki/concepts/kv-cache.md" {
			t.Fatalf("expected kv-cache.md for alias 'key-value cache', got p=%v, amb=%v", p2, amb2)
		}
	})

	t.Run("ambiguous title returns candidates sorted by path", func(t *testing.T) {
		p, amb := resolvePageArg(v, "duplicate concept")
		if p != nil || len(amb) != 2 {
			t.Fatalf("expected 2 ambiguous candidates, got p=%v, len(amb)=%d", p, len(amb))
		}
		if amb[0].Path != "wiki/concepts/ambig-one.md" || amb[1].Path != "wiki/entities/ambig-two.md" {
			t.Fatalf("candidates not sorted by path: %v, %v", amb[0].Path, amb[1].Path)
		}
	})

	t.Run("not found returns nil, nil", func(t *testing.T) {
		p, amb := resolvePageArg(v, "non-existent-page-xyz")
		if p != nil || amb != nil {
			t.Fatalf("expected nil, nil for missing page, got p=%v, amb=%v", p, amb)
		}
	})
}

func TestResolvePageArg_GraphFocus(t *testing.T) {
	root := setupPageArgTestVault(t)

	// Case-insensitive title match for graph --focus
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"graph", "--vault", root, "--focus", "FLASH ATTENTION SYSTEM"})
	})
	if code != 0 {
		t.Fatalf("graph --focus with title failed (exit %d): stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "wiki/concepts/flash-attention.md") {
		t.Errorf("stdout missing focus node: %s", stdout)
	}

	// Ambiguous title prints error and exits 1
	_, stderrAmb, codeAmb := captureRun(t, func() int {
		return run([]string{"graph", "--vault", root, "--focus", "duplicate concept"})
	})
	if codeAmb != 1 {
		t.Fatalf("expected exit 1 for ambiguous focus, got %d", codeAmb)
	}
	if !strings.Contains(stderrAmb, `"duplicate concept" is ambiguous`) {
		t.Errorf("stderr missing ambiguous message: %s", stderrAmb)
	}
}

func TestResolvePageArg_RelatedAlias(t *testing.T) {
	root := setupPageArgTestVault(t)

	// Resolve by alias for related
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"related", "--vault", root, "kvc"})
	})
	if code != 0 {
		t.Fatalf("related with alias failed (exit %d): stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "related to wiki/concepts/kv-cache.md") {
		t.Errorf("stdout missing expected related target: %s", stdout)
	}

	// Ambiguous title prints error and exits 1
	_, stderrAmb, codeAmb := captureRun(t, func() int {
		return run([]string{"related", "--vault", root, "Duplicate Concept"})
	})
	if codeAmb != 1 {
		t.Fatalf("expected exit 1 for ambiguous related target, got %d", codeAmb)
	}
	if !strings.Contains(stderrAmb, `"Duplicate Concept" is ambiguous`) {
		t.Errorf("stderr missing ambiguous message: %s", stderrAmb)
	}
}

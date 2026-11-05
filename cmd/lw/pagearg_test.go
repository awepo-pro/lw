package main

import (
	"os"
	"path/filepath"
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

	t.Run("ambiguous basename across directories returns candidates sorted by path", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte("# SCHEMA\n## Domain\ntest\n## Tags\n- test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "wiki", "entities"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "foo.md"), []byte("---\ntitle: Foo Concept\ntype: concept\ntags: [test]\n---\nFoo concept.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "wiki", "entities", "foo.md"), []byte("---\ntitle: Foo Entity\ntype: entity\ntags: [test]\n---\nFoo entity.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		vaultObj, err := vault.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		p, amb := resolvePageArg(vaultObj, "foo")
		if p != nil || len(amb) != 2 {
			t.Fatalf("expected nil page and 2 ambiguous candidates, got p=%v, len(amb)=%d", p, len(amb))
		}
		if amb[0].Path != "wiki/concepts/foo.md" || amb[1].Path != "wiki/entities/foo.md" {
			t.Fatalf("candidates not sorted by path: %v, %v", amb[0].Path, amb[1].Path)
		}

		p2, amb2 := resolvePageArg(vaultObj, "foo.md")
		if p2 != nil || len(amb2) != 2 {
			t.Fatalf("expected nil page and 2 ambiguous candidates for foo.md, got p=%v, len(amb)=%d", p2, len(amb2))
		}
	})
}

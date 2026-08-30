package lint_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/vault"
)

// minimalSchema is a bare SCHEMA.md sufficient for vault.ParseSchema to
// succeed, for synthetic vaults built by buildVault.
const minimalSchema = `# SCHEMA

## Domain

test domain.

## Tags

- ` + "`test`" + ` — a placeholder tag for synthetic fixtures.

## Conventions

- Filenames are lowercase-hyphen.md.
`

// buildVault writes files (vault-relative path -> content) under a fresh
// t.TempDir() — defaulting SCHEMA.md to minimalSchema unless the caller
// supplies its own — and returns the lint.Context built over the result.
//
// Used for synthetic, boundary-condition cases that spec/fixtures/** does
// not (and must not) carry: that tree is ground truth, not a scratch pad
// (00-conventions.md §6, MASTER §9 D-X).
func buildVault(t *testing.T, files map[string]string) *lint.Context {
	t.Helper()

	root := t.TempDir()
	if _, ok := files["SCHEMA.md"]; !ok {
		files["SCHEMA.md"] = minimalSchema
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("buildVault: mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("buildVault: write %s: %v", path, err)
		}
	}

	v, err := vault.Open(root)
	if err != nil {
		t.Fatalf("buildVault: vault.Open: %v", err)
	}
	return &lint.Context{
		Vault: v,
		Index: index.Build(v),
		Graph: v.Graph(),
	}
}

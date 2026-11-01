package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type vaultFileMeta struct {
	size    int64
	modTime time.Time
}

// snapshotVault returns a map of relative slash paths to vaultFileMeta for all regular files
// in the vault, skipping .llmwiki/logs/.
func snapshotVault(t *testing.T, root string) map[string]vaultFileMeta {
	t.Helper()
	snap := make(map[string]vaultFileMeta)
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		slashRel := filepath.ToSlash(rel)
		if slashRel == ".llmwiki/logs" || strings.HasPrefix(slashRel, ".llmwiki/logs/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() {
			snap[slashRel] = vaultFileMeta{
				size:    info.Size(),
				modTime: info.ModTime(),
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotVault: %v", err)
	}
	return snap
}

// assertVaultUnchanged re-snapshots root and reports added/removed/size/mtime changes with t.Errorf.
func assertVaultUnchanged(t *testing.T, root string, before map[string]vaultFileMeta) {
	t.Helper()
	after := snapshotVault(t, root)
	if len(before) != len(after) {
		t.Errorf("vault file count changed: before %d, after %d", len(before), len(after))
	}
	for path, b := range before {
		a, ok := after[path]
		if !ok {
			t.Errorf("file %s was removed", path)
			continue
		}
		if a.size != b.size {
			t.Errorf("file %s size changed: was %d, now %d", path, b.size, a.size)
		}
		if !a.modTime.Equal(b.modTime) {
			t.Errorf("file %s mtime changed: was %v, now %v", path, b.modTime, a.modTime)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("file %s was added", path)
		}
	}
}

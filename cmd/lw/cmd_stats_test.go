package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vaultstats"
)

type vaultFileSnapshot struct {
	size  int64
	mtime time.Time
}

func snapshotVault(t *testing.T, root string) map[string]vaultFileSnapshot {
	t.Helper()
	snap := make(map[string]vaultFileSnapshot)
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
			snap[slashRel] = vaultFileSnapshot{
				size:  info.Size(),
				mtime: info.ModTime(),
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotVault: %v", err)
	}
	return snap
}

func assertVaultUnchanged(t *testing.T, root string, before map[string]vaultFileSnapshot) {
	t.Helper()
	after := snapshotVault(t, root)
	if len(before) != len(after) {
		t.Fatalf("vault file count changed: before %d, after %d", len(before), len(after))
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
		if !a.mtime.Equal(b.mtime) {
			t.Errorf("file %s mtime changed: was %v, now %v", path, b.mtime, a.mtime)
		}
	}
}

func TestStats_FixtureCopy(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	before := snapshotVault(t, root)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"stats", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("run stats exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "pages") {
		t.Errorf("stdout does not contain 'pages':\n%s", stdout)
	}

	assertVaultUnchanged(t, root, before)
}

func TestStats_JSON(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"stats", "--vault", root, "--json"})
	})
	if code != 0 {
		t.Fatalf("run stats --json exit code = %d, want 0; stderr:\n%s", code, stderr)
	}

	var s vaultstats.Stats
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatalf("failed to parse JSON stats: %v\nstdout was:\n%s", err, stdout)
	}
	if s.Pages == 0 {
		t.Errorf("s.Pages = 0 in minimal fixture")
	}
}

func TestStats_StaleDaysInvalid(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"stats", "--vault", root, "--stale-days", "0"})
	})
	if code != 2 {
		t.Errorf("exit code = %d, want 2 for --stale-days 0", code)
	}
	if !strings.Contains(stderr, "--stale-days must be >= 1") {
		t.Errorf("stderr does not mention '--stale-days must be >= 1': %s", stderr)
	}
}

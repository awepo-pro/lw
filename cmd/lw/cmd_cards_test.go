package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

type vaultFileMeta struct {
	size  int64
	mtime int64
}

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
		relSlash := filepath.ToSlash(rel)
		if relSlash == ".llmwiki/logs" || strings.HasPrefix(relSlash, ".llmwiki/logs/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		snap[relSlash] = vaultFileMeta{
			size:  info.Size(),
			mtime: info.ModTime().UnixNano(),
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotVault: %v", err)
	}
	return snap
}

func TestCardsBadFormat(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"cards", "--format", "invalid", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "unknown format") {
		t.Errorf("stderr %q does not mention 'unknown format'", stderr)
	}
}

func TestCardsZeroCards(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"cards", "--type", "nonexistent", "--vault", root})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "no cards (check --type/--tag)") {
		t.Errorf("stderr %q does not contain 'no cards (check --type/--tag)'", stderr)
	}
}

func TestCardsOutputInsideVault(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	outPath := filepath.Join(root, "cards.tsv")
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"cards", "-o", outPath, "--vault", root})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", code, stderr)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("output file inside vault %s exists; wanted nothing written", outPath)
	}
}

func TestCardsOutputOutsideVault(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "exported-cards.tsv")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"cards", "-o", outPath, "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) == 0 || lines[0] != "#separator:tab" {
		t.Fatalf("first line = %q, want '#separator:tab'", lines[0])
	}

	if !strings.Contains(stderr, "wrote ") || !strings.Contains(stderr, "cards from ") {
		t.Errorf("stderr %q missing 'wrote N cards from P pages'", stderr)
	}
	if !strings.Contains(stderr, "import in Anki: File > Import, keep \"Update existing notes\"") {
		t.Errorf("stderr %q missing anki instructions", stderr)
	}
}

func TestCardsVaultUnchanged(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "cards.tsv")

	before := snapshotVault(t, root)

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"cards", "-o", outPath, "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}

	after := snapshotVault(t, root)

	if !reflect.DeepEqual(before, after) {
		t.Fatalf("vault changed after running cards!\nbefore: %+v\nafter: %+v", before, after)
	}
}

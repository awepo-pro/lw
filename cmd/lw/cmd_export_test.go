package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

func TestExportUsage(t *testing.T) {
	cases := [][]string{
		{"export"},
		{"export", "pdf"},
		{"export", "html"},
	}
	for _, tc := range cases {
		_, _, code := captureRun(t, func() int {
			return run(tc)
		})
		if code != 2 {
			t.Errorf("run(%v) = %d, want exit 2", tc, code)
		}
	}
}

func TestExportSuccess(t *testing.T) {
	fixtureDir := testutil.CopyFixture(t, "minimal")
	outDir := t.TempDir()

	type fileMeta struct {
		size int64
		mod  time.Time
	}
	snapshot := make(map[string]fileMeta)
	err := filepath.Walk(fixtureDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fixtureDir, p)
		if strings.HasPrefix(rel, ".llmwiki") {
			return nil
		}
		if !info.IsDir() {
			snapshot[rel] = fileMeta{
				size: info.Size(),
				mod:  info.ModTime(),
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot fixture: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"export", "html", "--vault", fixtureDir, outDir})
	})
	if code != 0 {
		t.Fatalf("run export html returned %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, "exported") {
		t.Errorf("stdout does not contain 'exported': %s", stdout)
	}
	if !strings.Contains(stdout, "open") {
		t.Errorf("stdout does not contain 'open': %s", stdout)
	}

	afterSnapshot := make(map[string]fileMeta)
	err = filepath.Walk(fixtureDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fixtureDir, p)
		if strings.HasPrefix(rel, ".llmwiki") {
			return nil
		}
		if !info.IsDir() {
			afterSnapshot[rel] = fileMeta{
				size: info.Size(),
				mod:  info.ModTime(),
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot fixture after: %v", err)
	}

	if len(snapshot) != len(afterSnapshot) {
		t.Fatalf("fixture file count changed: before=%d, after=%d", len(snapshot), len(afterSnapshot))
	}
	for rel, before := range snapshot {
		after, ok := afterSnapshot[rel]
		if !ok {
			t.Errorf("file missing after export: %s", rel)
			continue
		}
		if before.size != after.size {
			t.Errorf("file %s size changed: %d -> %d", rel, before.size, after.size)
		}
		if !before.mod.Equal(after.mod) {
			t.Errorf("file %s modtime changed: %v -> %v", rel, before.mod, after.mod)
		}
	}
}

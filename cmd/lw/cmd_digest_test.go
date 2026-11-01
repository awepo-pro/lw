package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

// bad --since / --format exit 2
func TestDigestBadFlags(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	tests := []struct {
		name string
		args []string
	}{
		{"bad_format", []string{"--vault", root, "--format", "pdf"}},
		{"bad_since_string", []string{"--vault", root, "--since", "last-week"}},
		{"bad_since_zero_days", []string{"--vault", root, "--since", "0d"}},
		{"bad_since_huge_days", []string{"--vault", root, "--since", "4000d"}},
		{"bad_until_string", []string{"--vault", root, "--until", "tomorrow"}},
		{"since_equals_until", []string{"--vault", root, "--since", "2026-10-15", "--until", "2026-10-15"}},
		{"since_after_until", []string{"--vault", root, "--since", "2026-10-20", "--until", "2026-10-10"}},
		{"unexpected_positional_arg", []string{"--vault", root, "extra"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := cmdDigest(tc.args)
			if err == nil {
				t.Fatalf("cmdDigest(%v) expected error, got nil", tc.args)
			}
			var ee *exitError
			if !errors.As(err, &ee) {
				t.Fatalf("cmdDigest(%v) error is not *exitError: %v", tc.args, err)
			}
			if ee.code != 2 {
				t.Errorf("cmdDigest(%v) exit code = %d, want 2", tc.args, ee.code)
			}
		})
	}
}

// -o inside the vault exits 1 and writes nothing
func TestDigestOutputInsideVaultRefused(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	targetFile := filepath.Join(root, "attempted-digest.md")

	err := cmdDigest([]string{"--vault", root, "-o", targetFile})
	if err == nil {
		t.Fatal("cmdDigest with -o inside vault succeeded, want refusal")
	}

	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("cmdDigest error is not *exitError: %v", err)
	}
	if ee.code != 1 {
		t.Errorf("exit code = %d, want 1", ee.code)
	}

	if _, statErr := os.Stat(targetFile); !os.IsNotExist(statErr) {
		t.Errorf("file %s was created inside the vault despite refusal", targetFile)
	}

	// Also test nested directory inside vault
	nestedFile := filepath.Join(root, "wiki", "attempted.md")
	errNested := cmdDigest([]string{"--vault", root, "-o", nestedFile})
	if errNested == nil {
		t.Fatal("cmdDigest with -o nested inside vault succeeded, want refusal")
	}
	if _, statErr := os.Stat(nestedFile); !os.IsNotExist(statErr) {
		t.Errorf("nested file %s was created inside vault", nestedFile)
	}
}

// -o outside writes the file
func TestDigestOutputOutsideWritesFile(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	outDir := t.TempDir()

	// 1. Markdown output
	mdFile := filepath.Join(outDir, "report.md")
	if err := cmdDigest([]string{"--vault", root, "--since", "30d", "-o", mdFile}); err != nil {
		t.Fatalf("cmdDigest markdown failed: %v", err)
	}
	content, err := os.ReadFile(mdFile)
	if err != nil {
		t.Fatalf("read output file %s: %v", mdFile, err)
	}
	if !strings.Contains(string(content), "# Wiki digest") {
		t.Errorf("output does not contain header: %s", string(content))
	}

	// 2. HTML output
	htmlFile := filepath.Join(outDir, "report.html")
	if err := cmdDigest([]string{"--vault", root, "--since", "30d", "--format", "html", "-o", htmlFile}); err != nil {
		t.Fatalf("cmdDigest html failed: %v", err)
	}
	htmlContent, err := os.ReadFile(htmlFile)
	if err != nil {
		t.Fatalf("read html output file %s: %v", htmlFile, err)
	}
	if !strings.Contains(string(htmlContent), "<!DOCTYPE html>") {
		t.Errorf("html output missing doctype: %s", string(htmlContent))
	}
}

type fileSnapshot struct {
	size  int64
	mtime time.Time
}

// the vault is unchanged after a run (snapshot paths+sizes+mtimes ignoring .llmwiki/logs/)
func TestDigestVaultUnchanged(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	takeSnapshot := func() map[string]fileSnapshot {
		snap := make(map[string]fileSnapshot)
		logsPrefix := filepath.Join(root, ".llmwiki", "logs")
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if strings.HasPrefix(path, logsPrefix) {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			snap[rel] = fileSnapshot{
				size:  info.Size(),
				mtime: info.ModTime(),
			}
			return nil
		})
		if err != nil {
			t.Fatalf("takeSnapshot: %v", err)
		}
		return snap
	}

	before := takeSnapshot()

	outPath := filepath.Join(t.TempDir(), "digest.md")
	if err := cmdDigest([]string{"--vault", root, "--since", "365d", "-o", outPath}); err != nil {
		t.Fatalf("cmdDigest failed: %v", err)
	}

	after := takeSnapshot()

	if len(before) != len(after) {
		t.Errorf("file count changed: before %d, after %d", len(before), len(after))
	}

	for rel, bSnap := range before {
		aSnap, ok := after[rel]
		if !ok {
			t.Errorf("file %s was removed", rel)
			continue
		}
		if aSnap.size != bSnap.size {
			t.Errorf("file %s size changed: was %d, now %d", rel, bSnap.size, aSnap.size)
		}
		if !aSnap.mtime.Equal(bSnap.mtime) {
			t.Errorf("file %s mtime changed: was %v, now %v", rel, bSnap.mtime, aSnap.mtime)
		}
	}

	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Errorf("unexpected new file added: %s", rel)
		}
	}
}

// Pending notes in digest output
func TestDigestPendingNotes(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	notesDir := filepath.Join(root, "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	notePath := filepath.Join(notesDir, "20261015-120000-pending-test.md")
	noteContent := "2026-10-15T12:00:00Z\n\nPending note text.\n"
	if err := os.WriteFile(notePath, []byte(noteContent), 0o644); err != nil {
		t.Fatal(err)
	}

	outFile := filepath.Join(t.TempDir(), "out.md")
	if err := cmdDigest([]string{"--vault", root, "--since", "30d", "-o", outFile}); err != nil {
		t.Fatalf("cmdDigest: %v", err)
	}

	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	if !strings.Contains(content, "## Notes not yet ingested") {
		t.Errorf("output missing pending notes section:\n%s", content)
	}
	if !strings.Contains(content, "- 20261015-120000-pending-test") {
		t.Errorf("output missing pending note stem:\n%s", content)
	}
}

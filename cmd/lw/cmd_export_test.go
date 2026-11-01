package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	before := snapshotVault(t, fixtureDir)

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

	assertVaultUnchanged(t, fixtureDir, before)
}

// Reproduction 3a: export html --force site where site/wiki -> <vault>/wiki
// must exit 1 (refusal on stderr) and leave the vault unchanged.
func TestExportSymlinkDirectoryWriteThroughRefused(t *testing.T) {
	fixtureDir := testutil.CopyFixture(t, "minimal")
	outDir := t.TempDir()
	siteDir := filepath.Join(outDir, "site")
	if err := os.MkdirAll(siteDir, 0755); err != nil {
		t.Fatal(err)
	}

	vaultWiki := filepath.Join(fixtureDir, "wiki")
	siteWiki := filepath.Join(siteDir, "wiki")
	if err := os.Symlink(vaultWiki, siteWiki); err != nil {
		t.Fatal(err)
	}

	before := snapshotVault(t, fixtureDir)

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"export", "html", "--force", "--vault", fixtureDir, siteDir})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "refusing") && !strings.Contains(stderr, "symlink") {
		t.Errorf("stderr %q missing refusal/symlink notice", stderr)
	}

	assertVaultUnchanged(t, fixtureDir, before)
}

// Reproduction 3b: export html --force site where site/index.html -> <vault>/index.md
// must exit 1 (refusal on stderr) and leave the vault unchanged.
func TestExportSymlinkFileWriteThroughRefused(t *testing.T) {
	fixtureDir := testutil.CopyFixture(t, "minimal")
	vaultIndex := filepath.Join(fixtureDir, "index.md")
	if err := os.WriteFile(vaultIndex, []byte("pre-existing vault index"), 0644); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	siteDir := filepath.Join(outDir, "site")
	if err := os.MkdirAll(siteDir, 0755); err != nil {
		t.Fatal(err)
	}

	siteIndex := filepath.Join(siteDir, "index.html")
	if err := os.Symlink(vaultIndex, siteIndex); err != nil {
		t.Fatal(err)
	}

	before := snapshotVault(t, fixtureDir)

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"export", "html", "--force", "--vault", fixtureDir, siteDir})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "refusing") && !strings.Contains(stderr, "symlink") {
		t.Errorf("stderr %q missing refusal/symlink notice", stderr)
	}

	assertVaultUnchanged(t, fixtureDir, before)
}

// TestExportSymlinkFileOutsideVaultRefused verifies export --force into an outdir
// containing a symlinked FILE pointing OUTSIDE the vault is refused too and the symlink target is unchanged.
func TestExportSymlinkFileOutsideVaultRefused(t *testing.T) {
	fixtureDir := testutil.CopyFixture(t, "minimal")
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	original := []byte("outside target untouched")
	if err := os.WriteFile(outsideFile, original, 0644); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	siteDir := filepath.Join(outDir, "site")
	if err := os.MkdirAll(siteDir, 0755); err != nil {
		t.Fatal(err)
	}

	siteIndex := filepath.Join(siteDir, "index.html")
	if err := os.Symlink(outsideFile, siteIndex); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"export", "html", "--force", "--vault", fixtureDir, siteDir})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "refusing") && !strings.Contains(stderr, "symlink") {
		t.Errorf("stderr %q missing refusal/symlink notice", stderr)
	}

	after, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("outside file was modified: got %q, want %q", after, original)
	}
}

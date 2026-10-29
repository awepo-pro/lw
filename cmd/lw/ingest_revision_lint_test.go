package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestC3_LintReportsUnrepointedPages pins 070 C3 (moved out of S3 by A-070-2):
// end to end through `lw lint`, a page that still cites a superseded raw gets a
// cite-superseded warning naming the successor, and re-pointing it clears the
// warning. The old raw's bytes are never touched.
func TestC3_LintReportsUnrepointedPages(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	commitRawWithURL(t, root, "raw/articles/c3.md", "/n/c3.md", "# C3\n\nFirst version.\n")
	oldPath := filepath.Join(root, "raw", "articles", "c3.md")
	oldBefore, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}

	ingested, err := vault.ParseDate("2026-10-02")
	if err != nil {
		t.Fatal(err)
	}
	body := "# C3\n\nSecond version.\n"
	newRaw := (&vault.RawSource{
		SourceURL:  "/n/c3.md",
		Ingested:   ingested,
		SHA256:     vault.BodySHA256(body),
		Supersedes: "raw/articles/c3.md",
		Body:       body,
	}).Serialize()
	if err := os.WriteFile(filepath.Join(root, "raw", "articles", "c3-2.md"), newRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	rcWritePage(t, root, "wiki/concepts/c3-page.md", []string{"raw/articles/c3.md"}, "Claim ^[raw/articles/c3.md]\n")

	stdout, stderr, _ := captureRun(t, func() int { return run([]string{"lint", "--vault", root}) })
	if !strings.Contains(stdout, "cite-superseded") || !strings.Contains(stdout, "wiki/concepts/c3-page.md") ||
		!strings.Contains(stdout, "raw/articles/c3-2.md") {
		t.Fatalf("lint did not report the un-repointed page\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	rcWritePage(t, root, "wiki/concepts/c3-page.md", []string{"raw/articles/c3-2.md"}, "Claim ^[raw/articles/c3-2.md]\n")
	stdout, stderr, _ = captureRun(t, func() int { return run([]string{"lint", "--vault", root}) })
	if strings.Contains(stdout, "cite-superseded") {
		t.Fatalf("lint still reports cite-superseded after re-pointing\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	oldAfter, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(oldBefore) != string(oldAfter) {
		t.Errorf("the old raw changed:\nbefore %q\nafter  %q", oldBefore, oldAfter)
	}
}

package tools

// stray_original_test.go pins 058 FIX1's F2: a file already sitting at the
// name an original would take — a hand-copied raw/articles/x.html, say — must
// not wedge the ingest. The engine refuses an original that exists
// ("original_path … already exists"), and resolveSourcePath used to look at
// the raw's .md only, so the same title picked the same blocked name every
// time and the model had no way out. A candidate is now taken when its
// original's path exists too, so the -2 suffix is chosen and the raw and its
// original are both suffixed.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
)

// TestIngestStrayOriginalTakesSuffix: a stray file at the original's name
// makes the candidate taken, whatever kind of original it would have been.
func TestIngestStrayOriginalTakesSuffix(t *testing.T) {
	html := []byte("<html><body><h1>Postgres Step By Step</h1><p>Install it.</p></body></html>")
	md := []byte("# Max Combination\r\n\r\nPick the best pair.\r\n")
	txt := []byte("# Reading Notes\n\nChapter one matters.\n")

	pdf := filepath.Join(t.TempDir(), "quaternion-introduction.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.6 fake pdf for TestIngestStrayOriginalTakesSuffix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pdfDoc := &extract.Doc{
		Title:     "Quaternion Introduction",
		SourceURL: "https://example.test/quaternion",
		Markdown:  "# Quaternion Introduction\n\nThe extracted body.\n",
		Kind:      "paper",
		Extractor: "docling/pdf",
		Original:  pdf,
	}

	for _, tc := range []struct {
		name     string
		ex       extract.Extractor
		uri      string
		kindDir  string
		strays   []string // files already in the vault, beside where the raw would land
		wantRaw  string
		wantOrig string
	}{
		{
			name: "html: a stray .html", ex: realExtractors(), uri: writeSource(t, "page.html", html), kindDir: "articles",
			strays:  []string{"postgres-step-by-step.html"},
			wantRaw: "raw/articles/postgres-step-by-step-2.md", wantOrig: "raw/articles/postgres-step-by-step-2.html",
		},
		{
			name: "markdown: a stray .md.orig", ex: realExtractors(), uri: writeSource(t, "note.md", md), kindDir: "articles",
			strays:  []string{"max-combination.md.orig"},
			wantRaw: "raw/articles/max-combination-2.md", wantOrig: "raw/articles/max-combination-2.md.orig",
		},
		{
			name: "text: a stray .txt", ex: realExtractors(), uri: writeSource(t, "notes.txt", txt), kindDir: "articles",
			strays:  []string{"reading-notes.txt"},
			wantRaw: "raw/articles/reading-notes-2.md", wantOrig: "raw/articles/reading-notes-2.txt",
		},
		{
			name: "pdf: a stray .pdf", ex: originalExtractor{doc: pdfDoc}, uri: "quaternion.pdf", kindDir: "papers",
			strays:  []string{"quaternion-introduction.pdf"},
			wantRaw: "raw/papers/quaternion-introduction-2.md", wantOrig: "raw/papers/quaternion-introduction-2.pdf",
		},
		{
			name: "the -2 original is stray too: -3", ex: realExtractors(), uri: writeSource(t, "page.html", html), kindDir: "articles",
			strays:  []string{"postgres-step-by-step.html", "postgres-step-by-step-2.html"},
			wantRaw: "raw/articles/postgres-step-by-step-3.md", wantOrig: "raw/articles/postgres-step-by-step-3.html",
		},
		{
			name: "strays at other extensions do not collide", ex: realExtractors(), uri: writeSource(t, "page.html", html), kindDir: "articles",
			strays:  []string{"postgres-step-by-step.txt", "postgres-step-by-step.md.orig", "postgres-step-by-step.pdf"},
			wantRaw: "raw/articles/postgres-step-by-step.md", wantOrig: "raw/articles/postgres-step-by-step.html",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, e, dir := engineRegistry(t, tc.ex)
			for _, s := range tc.strays {
				if err := os.WriteFile(filepath.Join(dir, "raw", tc.kindDir, s), []byte("stray "+s), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			r := ingestPath(t, reg, tc.uri)
			if r.IsError {
				t.Fatalf("ingest rejected: %s", r.Content)
			}
			op := onlyOp(t, e)
			if op.Path != tc.wantRaw || op.OriginalPath != tc.wantOrig {
				t.Errorf("staged %q + original %q, want %q + %q", op.Path, op.OriginalPath, tc.wantRaw, tc.wantOrig)
			}
			if strings.Contains(r.Content, tc.wantOrig) {
				t.Errorf("the result names the original, which the model must not see: %q", r.Content)
			}

			// The commit lands the pair and leaves every stray alone.
			if _, err := e.Commit("ingest past a stray original"); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			for _, rel := range []string{tc.wantRaw, tc.wantOrig} {
				if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
					t.Errorf("committed %s missing: %v", rel, err)
				}
			}
			for _, s := range tc.strays {
				got, err := os.ReadFile(filepath.Join(dir, "raw", tc.kindDir, s))
				if err != nil || !bytes.Equal(got, []byte("stray "+s)) {
					t.Errorf("stray %s was touched: %q (%v)", s, got, err)
				}
			}
		})
	}
}

package tools

// stage_close_originals_test.go pins 058 FIX1's F1: the model never sees an
// original. stage.close's summary used to list every path the changeset
// touches, and an ingest op touches its original's path beside the raw's —
// so after 058 every html/txt/md ingest told the model "files touched: 2" and
// named x.html. The summary now skips live ingest ops' originals (a PDF's
// included), so its text is the same with or without one.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
)

// closeAfterIngest ingests uri through a fresh registry over ex, reads the
// staged source in full (stage.close refuses an unread ingest, 040), closes,
// and returns the close text with the changeset id blanked — the one part
// that differs between two runs — plus the staged original's path ("" when
// the ingest kept none).
func closeAfterIngest(t *testing.T, ex extract.Extractor, uri string) (closeText, originalPath string) {
	t.Helper()
	reg, e, _ := engineRegistry(t, ex)
	if r := ingestPath(t, reg, uri); r.IsError {
		t.Fatalf("ingest rejected: %s", r.Content)
	}
	op := onlyOp(t, e)
	args, err := json.Marshal(map[string]any{"source": op.Path, "chunk": 1})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := reg.Call(context.Background(), "raw.get", args); err != nil || r.IsError {
		t.Fatalf("raw.get %s: %+v %v", op.Path, r, err)
	}
	r, err := reg.Call(context.Background(), "stage.close", json.RawMessage(`{}`))
	if err != nil || r.IsError {
		t.Fatalf("stage.close: %+v %v", r, err)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(r.Content, cs.ID, "<changeset>"), op.OriginalPath
}

// TestStageCloseOmitsOriginals: the stage.close text for an ingest that
// keeps an original is byte-identical to the text for the same ingest
// without one — for an HTML page, a text file, a markdown file (x.md.orig)
// and a PDF-style original named by Doc.Original.
func TestStageCloseOmitsOriginals(t *testing.T) {
	type closeCase struct {
		name string
		with extract.Extractor
		// without is the control: the same source as v2.40.0 staged it.
		without extract.Extractor
		uri     string
	}

	html := writeSource(t, "page.html", []byte("<html><head><title>pg</title></head><body><h1>Postgres Step By Step</h1><p>Install it.</p></body></html>"))
	txt := writeSource(t, "notes.txt", []byte("# Reading Notes\n\nChapter one matters.\n"))
	md := writeSource(t, "note.md", []byte("# Max Combination\r\n\r\nPick the best pair.\r\n"))
	chain, control := realExtractors(), withoutOriginal{inner: realExtractors()}

	// A PDF-style original: Doc.Original names a real file, the 033 path.
	pdf := filepath.Join(t.TempDir(), "quaternion-introduction.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.6 fake pdf for TestStageCloseOmitsOriginals\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pdfDoc := extract.Doc{
		Title:     "Quaternion Introduction",
		SourceURL: "https://example.test/quaternion",
		Markdown:  "# Quaternion Introduction\n\nThe extracted body.\n",
		Kind:      "paper",
		Extractor: "docling/pdf",
	}
	withPDF, withoutPDF := pdfDoc, pdfDoc
	withPDF.Original = pdf

	for _, tc := range []closeCase{
		{"html", chain, control, html},
		{"txt", chain, control, txt},
		{"md", chain, control, md},
		{"pdf", originalExtractor{doc: &withPDF}, originalExtractor{doc: &withoutPDF}, "quaternion.pdf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withText, withOrig := closeAfterIngest(t, tc.with, tc.uri)
			withoutText, withoutOrig := closeAfterIngest(t, tc.without, tc.uri)
			if withOrig == "" {
				t.Fatalf("the control is vacuous: the ingest kept no original")
			}
			if withoutOrig != "" {
				t.Fatalf("the control staged an original: %q", withoutOrig)
			}
			if withText != withoutText {
				t.Errorf("stage.close text differs when an original is kept:\nwith:\n%s\n\nwithout:\n%s", withText, withoutText)
			}
			if !strings.Contains(withText, "files touched: 1\n") {
				t.Errorf("close text does not count one touched file:\n%s", withText)
			}
			if strings.Contains(withText, withOrig) {
				t.Errorf("close text names the original %q:\n%s", withOrig, withText)
			}
		})
	}
}

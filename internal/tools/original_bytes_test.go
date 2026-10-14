package tools

// original_bytes_test.go pins 058's D2: stage.ingest_source keeps a verbatim
// original beside EVERY raw — an HTML page, a markdown or text file — not
// only beside a PDF's. The bytes come from the extractor chain
// (extract.Doc.OriginalBytes), never from the model's arguments, and the
// tool's result text is unchanged: the model never sees the original.
// Every test here runs the real extractors over real files, so the whole
// path from disk to the staged op is the one a user's `lw ingest` takes.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

// realExtractors is the chain `lw ingest` builds for local files, minus the
// PDF backend (no sidecar in a unit test).
func realExtractors() extract.Extractor {
	return extract.Chain(extract.NewHTML(nil), extract.NewFile())
}

// writeSource writes b to name in a fresh temp dir and returns its path.
func writeSource(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// ingestPath opens a changeset and runs stage.ingest_source over the local
// file at path, with no kind argument — the way `lw ingest <file>` hands a
// path to the model.
func ingestPath(t *testing.T, reg *Registry, path string) Result {
	t.Helper()
	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest a source"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}
	args, err := json.Marshal(map[string]string{"uri": path})
	if err != nil {
		t.Fatal(err)
	}
	r, err := reg.Call(ctx, "stage.ingest_source", args)
	if err != nil {
		t.Fatalf("stage.ingest_source: %v", err)
	}
	return r
}

// onlyOp returns the changeset's single op.
func onlyOp(t *testing.T, e *stage.Engine) stage.Op {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 1 {
		t.Fatalf("staged %d ops, want 1", len(cs.Ops))
	}
	return cs.Ops[0]
}

// casBytes reads the object stored under sha in e's content store — where
// Append leaves an op's original once it has cleared OriginalContent.
func casBytes(t *testing.T, e *stage.Engine, sha string) []byte {
	t.Helper()
	store, err := stage.OpenStore(filepath.Join(e.Vault().Root(), ".llmwiki", "objects"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	b, err := store.Get(sha)
	if err != nil {
		t.Fatalf("CAS Get(%s): %v", sha, err)
	}
	return b
}

func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestIngestHTMLStagesOriginal: ingesting a saved page stages the page
// itself beside its markdown — same slug, .html — with the sha of its bytes
// recorded in both the op and the raw's frontmatter.
func TestIngestHTMLStagesOriginal(t *testing.T) {
	page := []byte("<!DOCTYPE html>\r\n<html><head><title>pg</title></head><body>\r\n" +
		"<h1>Postgres Step By Step</h1>\r\n<p>Install it, then <code>psql</code> in.</p>\r\n</body></html>\r\n")
	src := writeSource(t, "page.html", page)
	reg, e, _ := engineRegistry(t, realExtractors())

	r := ingestPath(t, reg, src)
	if r.IsError {
		t.Fatalf("ingest rejected: %s", r.Content)
	}
	op := onlyOp(t, e)

	if want := "raw/articles/postgres-step-by-step.md"; op.Path != want {
		t.Errorf("op.Path = %q, want %q", op.Path, want)
	}
	if want := "raw/articles/postgres-step-by-step.html"; op.OriginalPath != want {
		t.Errorf("op.OriginalPath = %q, want %q", op.OriginalPath, want)
	}
	if want := sha256Of(page); op.Original != want {
		t.Errorf("op.Original = %q, want the page's sha256 %q", op.Original, want)
	}
	if got := casBytes(t, e, op.Original); !bytes.Equal(got, page) {
		t.Errorf("staged original = %q, want the file's bytes %q", got, page)
	}

	proposed := proposedRawDiff(t, e).New
	rs, err := vault.ParseRawSource(op.Path, []byte(proposed))
	if err != nil {
		t.Fatalf("ParseRawSource: %v", err)
	}
	if want := "raw/articles/postgres-step-by-step.html"; rs.Original != want {
		t.Errorf("frontmatter original = %q, want %q", rs.Original, want)
	}
	if rs.OriginalSHA256 != sha256Of(page) {
		t.Errorf("frontmatter original_sha256 = %q, want %q", rs.OriginalSHA256, sha256Of(page))
	}
	for _, line := range []string{
		"original: raw/articles/postgres-step-by-step.html\n",
		"original_sha256: " + sha256Of(page) + "\n",
	} {
		if !strings.Contains(proposed, line) {
			t.Errorf("proposed raw lacks the frontmatter line %q:\n%s", line, proposed)
		}
	}
}

// TestIngestMarkdownStagesMdOrig: a markdown source's original is named
// x.md.orig — a .md name would collide with the raw itself — and carries the
// file's bytes untouched (CRLF included), though the raw body is normalized.
func TestIngestMarkdownStagesMdOrig(t *testing.T) {
	note := []byte("# Max Combination\r\n\r\nPick the best pair.\r\n\r\n- one\r\n- two\r\n")
	src := writeSource(t, "notes.md", note)
	reg, e, _ := engineRegistry(t, realExtractors())

	r := ingestPath(t, reg, src)
	if r.IsError {
		t.Fatalf("ingest rejected: %s", r.Content)
	}
	op := onlyOp(t, e)

	if want := "raw/articles/max-combination.md"; op.Path != want {
		t.Errorf("op.Path = %q, want %q", op.Path, want)
	}
	if want := "raw/articles/max-combination.md.orig"; op.OriginalPath != want {
		t.Errorf("op.OriginalPath = %q, want %q", op.OriginalPath, want)
	}
	if want := sha256Of(note); op.Original != want {
		t.Errorf("op.Original = %q, want the file's sha256 %q", op.Original, want)
	}
	if got := casBytes(t, e, op.Original); !bytes.Equal(got, note) {
		t.Errorf("staged original = %q, want the file byte-identical, CRLF kept: %q", got, note)
	}

	proposed := proposedRawDiff(t, e).New
	rs, err := vault.ParseRawSource(op.Path, []byte(proposed))
	if err != nil {
		t.Fatalf("ParseRawSource: %v", err)
	}
	if rs.Original != "raw/articles/max-combination.md.orig" {
		t.Errorf("frontmatter original = %q, want the .md.orig path", rs.Original)
	}
	if strings.Contains(rs.Body, "\r") {
		t.Errorf("raw body kept a carriage return, want it normalized: %q", rs.Body)
	}
}

// TestIngestTextStagesTxt: a plain-text source keeps its .txt beside the raw.
func TestIngestTextStagesTxt(t *testing.T) {
	text := []byte("# Reading Notes\n\nChapter one is the one that matters.\n")
	src := writeSource(t, "x.txt", text)
	reg, e, _ := engineRegistry(t, realExtractors())

	r := ingestPath(t, reg, src)
	if r.IsError {
		t.Fatalf("ingest rejected: %s", r.Content)
	}
	op := onlyOp(t, e)

	if want := "raw/articles/reading-notes.txt"; op.OriginalPath != want {
		t.Errorf("op.OriginalPath = %q, want %q", op.OriginalPath, want)
	}
	if got := casBytes(t, e, op.Original); !bytes.Equal(got, text) {
		t.Errorf("staged original = %q, want %q", got, text)
	}
}

// TestIngestOriginalNamesFollowTheSourceExtension pins the whole mapping in
// one place, including the untitled-source fallback to the file's own name
// and the -n suffix a taken candidate gets: the original always follows the
// raw it sits beside, and its extension is the source's, lower-cased.
func TestIngestOriginalNamesFollowTheSourceExtension(t *testing.T) {
	cases := []struct {
		file     string
		body     string
		wantRaw  string
		wantOrig string
	}{
		{"Page.HTM", "<html><body><h1>Upper Htm</h1></body></html>", "raw/articles/upper-htm.md", "raw/articles/upper-htm.htm"},
		{"Page.HTML", "<html><body><h1>Upper Html</h1></body></html>", "raw/articles/upper-html.md", "raw/articles/upper-html.html"},
		{"a.markdown", "# Long Ext\n\nbody\n", "raw/articles/long-ext.md", "raw/articles/long-ext.markdown"},
		{"A.MD", "# Shouting\n\nbody\n", "raw/articles/shouting.md", "raw/articles/shouting.md.orig"},
		{"no-title.txt", "just some prose, no heading at all\n", "raw/articles/no-title.md", "raw/articles/no-title.txt"},
		{"no-title.md", "just some prose, no heading at all\n", "raw/articles/no-title.md", "raw/articles/no-title.md.orig"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			src := writeSource(t, tc.file, []byte(tc.body))
			reg, e, _ := engineRegistry(t, realExtractors())
			if r := ingestPath(t, reg, src); r.IsError {
				t.Fatalf("ingest rejected: %s", r.Content)
			}
			op := onlyOp(t, e)
			if op.Path != tc.wantRaw || op.OriginalPath != tc.wantOrig {
				t.Errorf("staged %q + original %q, want %q + %q", op.Path, op.OriginalPath, tc.wantRaw, tc.wantOrig)
			}
		})
	}

	t.Run("taken candidate is suffixed and so is its original", func(t *testing.T) {
		reg, e, _ := engineRegistry(t, realExtractors())
		first := writeSource(t, "one.html", []byte("<html><body><h1>Same Title</h1><p>first body</p></body></html>"))
		second := writeSource(t, "two.html", []byte("<html><body><h1>Same Title</h1><p>a different body</p></body></html>"))
		if r := ingestPath(t, reg, first); r.IsError {
			t.Fatalf("first ingest rejected: %s", r.Content)
		}
		args, _ := json.Marshal(map[string]string{"uri": second})
		if r, err := reg.Call(context.Background(), "stage.ingest_source", args); err != nil || r.IsError {
			t.Fatalf("second ingest: %+v %v", r, err)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		if len(cs.Ops) != 2 {
			t.Fatalf("staged %d ops, want 2", len(cs.Ops))
		}
		if cs.Ops[1].Path != "raw/articles/same-title-2.md" || cs.Ops[1].OriginalPath != "raw/articles/same-title-2.html" {
			t.Errorf("second op = %q + %q, want the -2 pair", cs.Ops[1].Path, cs.Ops[1].OriginalPath)
		}
	})
}

// normalizeResult blanks the op id, the one volatile part of an ingest
// result, so two runs over fresh vaults can be compared byte for byte.
func normalizeResult(s, opID string) string { return strings.ReplaceAll(s, opID, "<op>") }

// TestIngestResultTextUnchanged: what the model reads back from
// stage.ingest_source is byte-identical whether or not an original rides
// along — 058 adds no word to any model-visible text. 033 already adds
// nothing about an original to the result (it names the staged path and the
// chunk count only); the same doc without OriginalBytes is the control.
func TestIngestResultTextUnchanged(t *testing.T) {
	page := []byte("<html><head><title>pg</title></head><body><h1>Postgres Step By Step</h1><p>Install it.</p></body></html>")
	src := writeSource(t, "page.html", page)

	// With: the real chain, so the Doc carries OriginalBytes.
	regWith, eWith, _ := engineRegistry(t, realExtractors())
	with := ingestPath(t, regWith, src)
	if with.IsError {
		t.Fatalf("ingest with original rejected: %s", with.Content)
	}
	if opWith := onlyOp(t, eWith); opWith.OriginalPath == "" {
		t.Fatalf("the control is vacuous: the real chain staged no original")
	}

	// Without: the same Doc, OriginalBytes dropped by a wrapper.
	regWithout, eWithout, _ := engineRegistry(t, withoutOriginal{inner: realExtractors()})
	without := ingestPath(t, regWithout, src)
	if without.IsError {
		t.Fatalf("ingest without original rejected: %s", without.Content)
	}
	if opWithout := onlyOp(t, eWithout); opWithout.OriginalPath != "" {
		t.Fatalf("the control staged an original: %q", opWithout.OriginalPath)
	}

	idWith, _ := with.Data.(string)
	idWithout, _ := without.Data.(string)
	if idWith == "" || idWithout == "" {
		t.Fatalf("results carry no op id: %q / %q", idWith, idWithout)
	}
	if a, b := normalizeResult(with.Content, idWith), normalizeResult(without.Content, idWithout); a != b {
		t.Errorf("result text differs:\nwith original:    %q\nwithout original: %q", a, b)
	}
	for _, leak := range []string{"original", ".html", ".orig", "sha256"} {
		if strings.Contains(strings.ToLower(with.Content), leak) {
			t.Errorf("result text mentions %q: %s", leak, with.Content)
		}
	}
}

// withoutOriginal is the control extractor: the wrapped chain's Doc with its
// 058 original bytes removed, i.e. exactly what v2.40.0 produced.
type withoutOriginal struct{ inner extract.Extractor }

func (w withoutOriginal) CanHandle(uri string) bool { return w.inner.CanHandle(uri) }
func (w withoutOriginal) Extract(ctx context.Context, uri string) (*extract.Doc, error) {
	doc, err := w.inner.Extract(ctx, uri)
	if err != nil {
		return nil, err
	}
	doc.OriginalBytes, doc.OriginalExt = nil, ""
	return doc, nil
}

// TestIngestCommitWritesOriginal: committing the changeset writes the raw and
// its original, the original byte-exact; dropping the ingest instead writes
// neither. The committed pair then lints clean at every severity — an
// .md.orig beside an .md trips no check.
func TestIngestCommitWritesOriginal(t *testing.T) {
	page := []byte("<html><body><h1>Committed Page</h1><p>kept</p></body></html>\r\n")
	note := []byte("# Committed Note\r\n\r\nkept too\r\n")

	t.Run("commit", func(t *testing.T) {
		htmlSrc := writeSource(t, "page.html", page)
		noteSrc := writeSource(t, "note.md", note)
		reg, e, dir := engineRegistry(t, realExtractors())
		if r := ingestPath(t, reg, htmlSrc); r.IsError {
			t.Fatalf("html ingest rejected: %s", r.Content)
		}
		args, _ := json.Marshal(map[string]string{"uri": noteSrc})
		if r, err := reg.Call(context.Background(), "stage.ingest_source", args); err != nil || r.IsError {
			t.Fatalf("md ingest: %+v %v", r, err)
		}

		if _, err := e.Commit("ingest a page and a note"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		for rel, want := range map[string][]byte{
			"raw/articles/committed-page.html":    page,
			"raw/articles/committed-note.md.orig": note,
		} {
			got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("committed original %s not on disk: %v", rel, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s = %q, want the source's bytes %q", rel, got, want)
			}
		}
		for _, rel := range []string{"raw/articles/committed-page.md", "raw/articles/committed-note.md"} {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
				t.Errorf("committed raw %s not on disk: %v", rel, err)
			}
		}

		// Reopen from disk: both raws load with their originals, and no
		// error-level finding appears anywhere.
		v, err := vault.Open(dir)
		if err != nil {
			t.Fatalf("vault.Open: %v", err)
		}
		for rel, orig := range map[string]string{
			"raw/articles/committed-page.md": "raw/articles/committed-page.html",
			"raw/articles/committed-note.md": "raw/articles/committed-note.md.orig",
		} {
			rs, ok := v.RawSource(rel)
			if !ok {
				t.Fatalf("raw %s did not load", rel)
			}
			if rs.Original != orig {
				t.Errorf("%s original = %q, want %q", rel, rs.Original, orig)
			}
		}
		report := lint.Run(&lint.Context{Vault: v, Index: index.Build(v), Graph: v.Graph()}, nil)
		for _, f := range report.Findings {
			if strings.HasPrefix(f.Path, "raw/articles/committed-") {
				t.Errorf("a committed raw + original pair lints with a finding: %+v", f)
			}
		}
	})

	t.Run("drop", func(t *testing.T) {
		htmlSrc := writeSource(t, "page.html", page)
		noteSrc := writeSource(t, "note.md", note)
		reg, e, dir := engineRegistry(t, realExtractors())
		if r := ingestPath(t, reg, htmlSrc); r.IsError {
			t.Fatalf("html ingest rejected: %s", r.Content)
		}
		args, _ := json.Marshal(map[string]string{"uri": noteSrc})
		if r, err := reg.Call(context.Background(), "stage.ingest_source", args); err != nil || r.IsError {
			t.Fatalf("md ingest: %+v %v", r, err)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, op := range cs.Ops {
			ids = append(ids, op.ID)
		}
		if err := e.DropOps(ids); err != nil {
			t.Fatalf("DropOps: %v", err)
		}
		if _, err := e.Commit("nothing left"); !errors.Is(err, stage.ErrNothingToCommit) {
			t.Fatalf("Commit after DropOps = %v, want ErrNothingToCommit", err)
		}
		for _, rel := range []string{
			"raw/articles/committed-page.md", "raw/articles/committed-page.html",
			"raw/articles/committed-note.md", "raw/articles/committed-note.md.orig",
		} {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
				t.Errorf("dropped ingest left %s on disk (stat err = %v)", rel, err)
			}
		}
	})
}

// TestIngestEmptyOriginalBytesStagesNoOriginal: a zero-byte original proves
// nothing, so a Doc whose OriginalBytes is empty stages the raw alone.
func TestIngestEmptyOriginalBytesStagesNoOriginal(t *testing.T) {
	doc := &extract.Doc{
		Title:         "Empty Original",
		SourceURL:     "https://example.test/empty",
		Markdown:      "# Empty Original\n\nBody.\n",
		Kind:          "article",
		Extractor:     "go/html",
		OriginalBytes: []byte{},
		OriginalExt:   ".html",
	}
	reg, e, _ := engineRegistry(t, originalExtractor{doc: doc})
	if r := ingestPath(t, reg, "empty.html"); r.IsError {
		t.Fatalf("ingest rejected: %s", r.Content)
	}
	op := onlyOp(t, e)
	if op.OriginalPath != "" || op.Original != "" {
		t.Errorf("an empty original was staged: %q / %q", op.OriginalPath, op.Original)
	}
	if strings.Contains(proposedRawDiff(t, e).New, "original") {
		t.Errorf("an empty original reached the raw frontmatter")
	}
}

// TestIngestOriginalBytesWithoutUsableExtRefused: bytes with no extension to
// name them by cannot be placed beside the raw; the refusal is the same
// recoverable IsError 033 gives a PDF path with no extension, naming the
// source the bytes came from since there is no original file to name.
func TestIngestOriginalBytesWithoutUsableExtRefused(t *testing.T) {
	doc := &extract.Doc{
		Title:         "No Extension",
		SourceURL:     "https://example.test/noext",
		Markdown:      "# No Extension\n\nBody.\n",
		Kind:          "article",
		Extractor:     "go/html",
		OriginalBytes: []byte("<html></html>"),
		OriginalExt:   "",
	}
	reg, e, _ := engineRegistry(t, originalExtractor{doc: doc})
	r := ingestPath(t, reg, "noext.html")
	if !r.IsError {
		t.Fatalf("ingest of original bytes with no extension succeeded: %+v", r)
	}
	if want := `stage.ingest_source: the extractor's original "noext.html" has no usable non-.md extension`; r.Content != want {
		t.Errorf("result = %q, want %q", r.Content, want)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 0 {
		t.Errorf("a refused ingest staged %d ops", len(cs.Ops))
	}
}

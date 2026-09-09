package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

// ingestDoc is the deterministic document the fake extractor returns for
// every uri ending in .md.
type ingestDoc struct {
	Title     string
	SourceURL string
	Markdown  string
	Kind      string
}

func (d ingestDoc) toExtract() *extract.Doc {
	return &extract.Doc{Title: d.Title, SourceURL: d.SourceURL, Markdown: d.Markdown, Kind: d.Kind, Extractor: "test"}
}

// ingestRegistry returns a registry wired to a minimal-fixture engine whose
// extractor always returns doc.
func ingestRegistry(t *testing.T, doc ingestDoc) (*Registry, *stage.Engine) {
	t.Helper()
	reg, e, _ := engineRegistry(t, fakeExtractor{doc: doc.toExtract()})
	return reg, e
}

// proposeIngest opens a changeset and runs stage.ingest_source over uri.
func proposeIngest(t *testing.T, reg *Registry, uri string) Result {
	t.Helper()
	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest a source"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}
	r, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"`+uri+`","kind":"paper"}`))
	if err != nil {
		t.Fatalf("stage.ingest_source: %v", err)
	}
	return r
}

// proposedRawDiff returns the FileDiff of the changeset's single
// ingest_source op — the exact bytes Commit would write to raw/.
func proposedRawDiff(t *testing.T, e *stage.Engine) stage.FileDiff {
	t.Helper()
	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	var found []stage.FileDiff
	for _, f := range d.Files {
		if f.Kind == stage.OpIngestSource {
			found = append(found, f)
		}
	}
	if len(found) != 1 {
		t.Fatalf("ingest_source file diffs = %d, want 1", len(found))
	}
	return found[0]
}

// rawVaultWith writes content to rel inside a fresh single-source vault and
// opens it — the on-disk shape `lw status` and `lw lint` read.
func rawVaultWith(t *testing.T, rel string, content []byte) *vault.Vault {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(testSchemaMD), 0o644); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("vault.Open %s: %v", dir, err)
	}
	return v
}

const testSchemaMD = "# SCHEMA\n\n## Domain\n\nTest domain.\n\n## Tags\n\n- `inference` — running a trained model.\n- `decoding` — generating tokens one step at a time.\n"

// todayUTC is the date the handler records as `ingested` — today, UTC,
// exactly as stage.create_page derives created and updated.
func todayUTC(t *testing.T) vault.Date {
	t.Helper()
	d, err := vault.ParseDate(time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		t.Fatalf("parse today: %v", err)
	}
	return d
}

// TestIngestSourceProposesProvenanceFrontmatter drives the tool the way the
// model does, through Registry.Call, and asserts the proposed raw file is a
// frontmatter-bearing RawSource rather than bare markdown (TD-5).
func TestIngestSourceProposesProvenanceFrontmatter(t *testing.T) {
	doc := ingestDoc{
		Title:     "Leviathan",
		SourceURL: "https://example.test/leviathan",
		Markdown:  "# Leviathan\n\nA body the extractor produced.\n",
		Kind:      "paper",
	}
	reg, e := ingestRegistry(t, doc)
	if r := proposeIngest(t, reg, "source.md"); r.IsError {
		t.Fatalf("ingest rejected: %s", r.Content)
	}

	proposed := proposedRawDiff(t, e).New
	if !strings.HasPrefix(proposed, "---\nsource_url: ") {
		t.Fatalf("proposed raw file has no provenance frontmatter:\n%q", proposed)
	}

	rs, err := vault.ParseRawSource("raw/papers/leviathan.md", []byte(proposed))
	if err != nil {
		t.Fatalf("ParseRawSource: %v", err)
	}
	body := normalizeToolBody(doc.Markdown)
	if rs.SourceURL != doc.SourceURL {
		t.Errorf("source_url = %q, want %q", rs.SourceURL, doc.SourceURL)
	}
	if rs.Ingested != todayUTC(t) {
		t.Errorf("ingested = %q, want today %q", rs.Ingested.String(), todayUTC(t).String())
	}
	if rs.SHA256 != vault.BodySHA256(body) {
		t.Errorf("sha256 = %q, want body sha %q", rs.SHA256, vault.BodySHA256(body))
	}
	// The markdown the extractor produced is untouched behind the block.
	if _, after, ok := strings.Cut(proposed, "---\n\n"); !ok || after != body {
		t.Errorf("body after frontmatter = %q, want extractor markdown %q", after, body)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	op := cs.Ops[0]
	if op.SHA256 != vault.BodySHA256(proposed) {
		t.Errorf("op.SHA256 = %q, want sha of the proposed file bytes %q", op.SHA256, vault.BodySHA256(proposed))
	}
}

// TestIngestSourceProposedRawFileLintsClean writes the proposed bytes into a
// vault and runs the named checks over it: the file the tool proposes must
// not be the lint failure the bare-markdown proposal was.
func TestIngestSourceProposedRawFileLintsClean(t *testing.T) {
	for name, doc := range map[string]ingestDoc{
		"plain body": {
			Title:     "Leviathan",
			SourceURL: "https://example.test/leviathan",
			Markdown:  "# Leviathan\n\nA body the extractor produced.\n",
			Kind:      "paper",
		},
		"leading blank lines": {
			// ParseRawSource strips leading blank lines off a body, so the
			// recorded sha has to be taken after that strip — a proposal
			// hashed before it drifts against itself (src-integrity).
			Title:     "Leviathan",
			SourceURL: "https://example.test/leviathan",
			Markdown:  "\n\n# Leviathan\n\nA body the extractor produced.\n",
			Kind:      "paper",
		},
		"local path source url": {
			Title:     "Leviathan",
			SourceURL: "",
			Markdown:  "# Leviathan\n\nA body the extractor produced.\n",
			Kind:      "paper",
		},
	} {
		t.Run(name, func(t *testing.T) {
			reg, e := ingestRegistry(t, doc)
			if r := proposeIngest(t, reg, "source.md"); r.IsError {
				t.Fatalf("ingest rejected: %s", r.Content)
			}
			proposed := proposedRawDiff(t, e).New

			v := rawVaultWith(t, "raw/papers/leviathan.md", []byte(proposed))
			ctx := &lint.Context{Vault: v, Index: index.Build(v), Graph: v.Graph()}
			report := lint.Run(ctx, []string{"fm-required", "src-provenance", "src-integrity"})
			if len(report.Findings) != 0 {
				t.Errorf("lint findings on the proposed raw file: %+v", report.Findings)
			}
		})
	}
}

// TestIngestSourceProposedRawFileIsCounted pins the `lw status` half of
// TD-5: a committed proposal is a RawSource the vault counts, with the
// frontmatter fields read back intact.
func TestIngestSourceProposedRawFileIsCounted(t *testing.T) {
	doc := ingestDoc{Title: "Leviathan", SourceURL: "https://example.test/leviathan", Markdown: "# Leviathan\n\nBody.\n", Kind: "paper"}
	reg, e := ingestRegistry(t, doc)
	if r := proposeIngest(t, reg, "source.md"); r.IsError {
		t.Fatalf("ingest rejected: %s", r.Content)
	}
	proposed := proposedRawDiff(t, e).New

	v := rawVaultWith(t, "raw/papers/leviathan.md", []byte(proposed))
	sources := v.RawSources()
	if len(sources) != 1 {
		t.Fatalf("RawSources() = %d, want 1", len(sources))
	}
	rs := sources[0]
	if rs.Path != "raw/papers/leviathan.md" {
		t.Errorf("Path = %q", rs.Path)
	}
	if rs.SourceURL != doc.SourceURL {
		t.Errorf("SourceURL = %q, want %q", rs.SourceURL, doc.SourceURL)
	}
	if rs.Title != "Leviathan" {
		t.Errorf("Title = %q, want Leviathan", rs.Title)
	}
	if rs.Ingested != todayUTC(t) {
		t.Errorf("Ingested = %q, want today", rs.Ingested.String())
	}
	if rs.SHA256 != vault.BodySHA256(rs.Body) {
		t.Errorf("SHA256 = %q, does not match its own body", rs.SHA256)
	}
}

// TestIngestSourceDeterministicBytes proposes the same source twice under
// two names: the two files must be byte-identical, because the only input
// that is not derived from the document is the calendar date.
func TestIngestSourceDeterministicBytes(t *testing.T) {
	body := "# Same Body\n\nIdentical content, two names.\n"
	first := ingestDoc{Title: "Alpha One", SourceURL: "https://example.test/same", Markdown: body, Kind: "paper"}

	reg, e := ingestRegistry(t, first)
	if r := proposeIngest(t, reg, "source.md"); r.IsError {
		t.Fatalf("first ingest rejected: %s", r.Content)
	}
	// A second registry over the same document under a different title
	// lands at a different path, so nothing rejects it as a duplicate.
	second := first
	second.Title = "Alpha Two"
	reg2, e2 := ingestRegistry(t, second)
	if r := proposeIngest(t, reg2, "source.md"); r.IsError {
		t.Fatalf("second ingest rejected: %s", r.Content)
	}

	a := proposedRawDiff(t, e)
	b := proposedRawDiff(t, e2)
	if a.New != b.New {
		t.Errorf("same input produced different bytes:\n%q\nvs\n%q", a.New, b.New)
	}
	if a.Path == b.Path {
		t.Errorf("both proposals landed at %s", a.Path)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	cs2, err := e2.Current()
	if err != nil {
		t.Fatal(err)
	}
	if cs.Ops[0].SHA256 != cs2.Ops[0].SHA256 {
		t.Errorf("op shas differ: %q vs %q", cs.Ops[0].SHA256, cs2.Ops[0].SHA256)
	}
}

// TestRawSourceDocumentShape pins the byte layout of the block itself:
// three keys in source_url, ingested, sha256 order (backbone §2.7), no
// title key, then the closing delimiter, a blank line and the body.
func TestRawSourceDocumentShape(t *testing.T) {
	body := "# Leviathan\n\nBody line.\n"
	got := string(rawSourceDocument("https://example.test/leviathan", todayUTC(t), body))
	want := "---\n" +
		"source_url: https://example.test/leviathan\n" +
		"ingested: " + todayUTC(t).String() + "\n" +
		"sha256: " + vault.BodySHA256(body) + "\n" +
		"---\n" +
		"\n" +
		body
	if got != want {
		t.Errorf("rawSourceDocument =\n%q\nwant\n%q", got, want)
	}
	if again := string(rawSourceDocument("https://example.test/leviathan", todayUTC(t), body)); again != got {
		t.Errorf("rawSourceDocument is not deterministic: %q vs %q", again, got)
	}
	if strings.Contains(got, "title:") {
		t.Errorf("raw source frontmatter carries a title key: %q", got)
	}
}

// TestIngestSourceRefusesUnrepresentableSourceURL covers the guard that
// keeps a write-once raw file from being staged unparseable: a source_url
// that plain YAML cannot carry round-trips to a different source, and the
// tool refuses instead of proposing it.
func TestIngestSourceRefusesUnrepresentableSourceURL(t *testing.T) {
	doc := ingestDoc{Title: "Leviathan", SourceURL: "https://example.test/a # trailing comment", Markdown: "# Leviathan\n\nBody.\n", Kind: "paper"}
	reg, e := ingestRegistry(t, doc)
	r := proposeIngest(t, reg, "source.md")
	if !r.IsError {
		t.Fatalf("ingest accepted a source_url plain YAML cannot carry: %+v", r)
	}
	if !strings.Contains(r.Content, "plain YAML scalar") {
		t.Errorf("result = %q, want the fix-stating rejection", r.Content)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 0 {
		t.Errorf("rejected ingest staged %d ops", len(cs.Ops))
	}
}

// TestIngestSourceRejectsAlreadyIngestedBody carries the dedupe-by-hash
// guarantee the engine's own check can no longer express now that
// ingest_source's Content is a whole file rather than a bare body: a body
// already in the vault is refused, whichever path it sits at.
func TestIngestSourceRejectsAlreadyIngestedBody(t *testing.T) {
	_, e, dir := engineRegistry(t, nil)
	v := e.Vault()
	existing, ok := v.RawSource("raw/articles/kv-cache-explained.md")
	if !ok {
		t.Fatalf("fixture raw/articles/kv-cache-explained.md missing from %s", dir)
	}
	doc := ingestDoc{Title: "Cache Duplicate", SourceURL: "https://example.test/copy", Markdown: existing.Body, Kind: "article"}
	reg := NewRegistry(Deps{
		Vault: v, Index: index.Build(v), Engine: e,
		Extract: fakeExtractor{doc: doc.toExtract()},
		Author:  stage.Author{Kind: "agent", Model: "test"},
	})

	r := proposeIngest(t, reg, "copy.md")
	if !r.IsError {
		t.Fatalf("re-ingesting a committed body was accepted: %+v", r)
	}
	if !strings.Contains(r.Content, "already ingested") || !strings.Contains(r.Content, "raw/articles/kv-cache-explained.md") {
		t.Errorf("result = %q, want the existing path named", r.Content)
	}
}

// TestIngestCommitProducesLintCleanRawSource replays the cold-start story
// TD-5 broke: propose through the registry, commit through the engine, then
// reopen the vault a fresh process would see.
func TestIngestCommitProducesLintCleanRawSource(t *testing.T) {
	doc := ingestDoc{
		Title:     "Leviathan",
		SourceURL: "https://example.test/leviathan",
		Markdown:  "# Leviathan\n\nA body the extractor produced.\n",
		Kind:      "paper",
	}
	reg, e := ingestRegistry(t, doc)
	if r := proposeIngest(t, reg, "source.md"); r.IsError {
		t.Fatalf("ingest rejected: %s", r.Content)
	}
	fd := proposedRawDiff(t, e)
	proposed := fd.New
	if _, err := e.Commit("ingest one source"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	v, err := vault.Open(e.Vault().Root())
	if err != nil {
		t.Fatal(err)
	}
	sources := v.RawSources()
	if len(sources) != 3 {
		t.Fatalf("RawSources() = %d, want the fixture's 2 plus the ingested 1", len(sources))
	}
	ingested, ok := v.RawSource(fd.Path)
	if !ok {
		t.Fatalf("the committed source is not in the vault as %s", fd.Path)
	}
	if ingested.SHA256 != vault.BodySHA256(ingested.Body) {
		t.Errorf("committed source %s does not hash to its own frontmatter sha256", ingested.Path)
	}

	onDisk, err := v.Read(fd.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != proposed {
		t.Errorf("committed bytes differ from the proposal:\n%q\nvs\n%q", onDisk, proposed)
	}

	ctx := &lint.Context{Vault: v, Index: index.Build(v), Graph: v.Graph()}
	for _, check := range []string{"fm-required", "src-provenance", "src-integrity"} {
		if f := lint.Run(ctx, []string{check}).ByCheck[check]; len(f) != 0 {
			t.Errorf("%s reported the committed raw source: %+v", check, f)
		}
	}
}

// TestIngestSourceRejectsDuplicateProposalInChangeset pins the in-changeset
// half of dedupe: a second proposal of byte-identical file content, at a
// different path, is still refused.
func TestIngestSourceRejectsDuplicateProposalInChangeset(t *testing.T) {
	doc := ingestDoc{Title: "Leviathan", SourceURL: "https://example.test/leviathan", Markdown: "# Leviathan\n\nBody.\n", Kind: "paper"}
	reg, _ := ingestRegistry(t, doc)
	if r := proposeIngest(t, reg, "source.md"); r.IsError {
		t.Fatalf("first ingest rejected: %s", r.Content)
	}
	r, err := reg.Call(context.Background(), "stage.ingest_source", json.RawMessage(`{"uri":"second.md","kind":"article"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError || !strings.Contains(r.Content, "already ingested") {
		t.Fatalf("duplicate proposal result = %+v", r)
	}
}

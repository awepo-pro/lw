package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// anyExtractor claims every uri (naming tests feed ".MD" and ".html"
// spellings the .md-only fakeExtractor would refuse) and hands out its docs
// in order, repeating the last one — so consecutive stage.ingest_source
// calls in one test can see different documents.
type anyExtractor struct {
	docs []*extract.Doc
	i    int
}

func (e *anyExtractor) CanHandle(string) bool { return true }

func (e *anyExtractor) Extract(context.Context, string) (*extract.Doc, error) {
	d := e.docs[e.i]
	if e.i < len(e.docs)-1 {
		e.i++
	}
	cp := *d
	return &cp, nil
}

// namedFile is one raw source written into the copied fixture before the
// vault opens, so a test can arrange a committed collision. A slice, not a
// map: the write order stays fixed.
type namedFile struct {
	rel  string
	body string
}

// namingRegistry copies the minimal fixture, writes pre into it, and opens
// an engine whose extractor hands out docs in order.
func namingRegistry(t *testing.T, pre []namedFile, docs ...*extract.Doc) (*Registry, *stage.Engine) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	for _, f := range pre {
		writeRawSource(t, dir, f.rel, f.body)
	}
	return namingRegistryOn(t, dir, docs...)
}

// namingRegistryOn opens an engine and registry over an existing vault
// directory — the second phase of a test that commits in between must see
// the first phase's committed file, so it reuses the same dir.
func namingRegistryOn(t *testing.T, dir string, docs ...*extract.Doc) (*Registry, *stage.Engine) {
	t.Helper()
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v := e.Vault()
	reg := NewRegistry(Deps{
		Vault: v, Index: index.Build(v), Engine: e,
		Extract: &anyExtractor{docs: docs},
		Author:  stage.Author{Kind: "agent", Model: "test"},
	})
	return reg, e
}

func nameOpen(t *testing.T, reg *Registry) {
	t.Helper()
	if r, err := reg.Call(context.Background(), "stage.open", json.RawMessage(`{"intent":"naming"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}
}

// nameIngest runs stage.ingest_source for uri with kind article, returning
// the raw result — naming tests assert on both error and success shapes.
func nameIngest(t *testing.T, reg *Registry, uri string) Result {
	t.Helper()
	r, err := reg.Call(context.Background(), "stage.ingest_source", json.RawMessage(`{"uri":"`+uri+`","kind":"article"}`))
	if err != nil {
		t.Fatalf("stage.ingest_source(%s): %v", uri, err)
	}
	return r
}

func namingDoc(title, body string) *extract.Doc {
	return &extract.Doc{Title: title, SourceURL: "https://example.test/" + slugSourceName(title), Markdown: body, Kind: "article", Extractor: "test"}
}

// TestIngestSourceNaming pins the raw-source naming rule 008 contract §4.1:
// title, else basename minus one trailing extension, else "untitled" — and
// a taken candidate that holds a DIFFERENT source gets the first free -n
// suffix instead of refusing (008 U6: the schema has no name argument and
// the agent has no filesystem verbs, so a refusal was a dead end and the
// source was simply never ingested).
func TestIngestSourceNaming(t *testing.T) {
	t.Run("two_untitled_scratch_files_get_distinct_paths", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		reg, e := namingRegistryOn(t, dir, namingDoc("", "# Scratch\n\nFirst untitled body.\n"))
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "01-untitled.md"); r.IsError {
			t.Fatalf("first ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/01-untitled.md" {
			t.Fatalf("first staged path = %s, want raw/articles/01-untitled.md", got)
		}
		if _, err := e.Commit("ingest first scratch"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		// A second agent session — a fresh engine over the same vault —
		// ingests another untitled scratch file with a different body. It
		// must land at the first free -2 path, not be refused the way the
		// pre-008 engine refused it.
		reg2, e2 := namingRegistryOn(t, dir, namingDoc("", "# Scratch\n\nA different untitled body.\n"))
		nameOpen(t, reg2)
		r2 := nameIngest(t, reg2, "01-untitled.md")
		if r2.IsError {
			t.Fatalf("second untitled ingest refused (U6): %s", r2.Content)
		}
		if got := proposedRawDiff(t, e2).Path; got != "raw/articles/01-untitled-2.md" {
			t.Fatalf("second staged path = %s, want raw/articles/01-untitled-2.md", got)
		}
	})

	t.Run("extension_stripped_from_basename", func(t *testing.T) {
		reg, e := namingRegistry(t, nil, namingDoc("", "# N\n\nBody from an .MD file.\n"))
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "/x/Notes.MD"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/notes.md" {
			t.Fatalf("Notes.MD staged at %s, want raw/articles/notes.md (not the old notes-md.md)", got)
		}

		reg2, e2 := namingRegistry(t, nil, namingDoc("", "# P\n\nBody from an .html file.\n"))
		nameOpen(t, reg2)
		if r := nameIngest(t, reg2, "/x/saved.html"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e2).Path; got != "raw/articles/saved.md" {
			t.Fatalf("saved.html staged at %s, want raw/articles/saved.md", got)
		}
	})

	t.Run("untitled_when_basename_is_only_an_extension", func(t *testing.T) {
		reg, e := namingRegistry(t, nil, namingDoc("", "# U\n\nBody from a bare .md name.\n"))
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "/x/.md"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/untitled.md" {
			t.Fatalf("staged at %s, want raw/articles/untitled.md", got)
		}
	})

	t.Run("title_still_wins", func(t *testing.T) {
		reg, e := namingRegistry(t, nil, namingDoc("Kv Cache", "# Kv Cache\n\nBody.\n"))
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "/x/whatever.md"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/kv-cache.md" {
			t.Fatalf("staged at %s, want raw/articles/kv-cache.md", got)
		}
	})

	t.Run("different_body_gets_suffix_2", func(t *testing.T) {
		pre := []namedFile{{rel: "raw/articles/x.md", body: "# X\n\nAlready committed body.\n"}}
		reg, e := namingRegistry(t, pre, namingDoc("X", "# X\n\nA different body.\n"))
		nameOpen(t, reg)
		r := nameIngest(t, reg, "x.md")
		if r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/x-2.md" {
			t.Fatalf("staged at %s, want raw/articles/x-2.md", got)
		}
	})

	t.Run("third_collision_gets_suffix_3", func(t *testing.T) {
		pre := []namedFile{
			{rel: "raw/articles/x.md", body: "# X\n\nFirst committed body.\n"},
			{rel: "raw/articles/x-2.md", body: "# X\n\nSecond committed body.\n"},
		}
		reg, e := namingRegistry(t, pre, namingDoc("X", "# X\n\nA third body.\n"))
		nameOpen(t, reg)
		r := nameIngest(t, reg, "x.md")
		if r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/x-3.md" {
			t.Fatalf("staged at %s, want raw/articles/x-3.md", got)
		}
	})

	t.Run("staged_collision_gets_suffix", func(t *testing.T) {
		docs := []*extract.Doc{
			namingDoc("X", "# X\n\nFirst staged body.\n"),
			namingDoc("X", "# X\n\nA different staged body.\n"),
		}
		reg, e := namingRegistry(t, nil, docs...)
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "x.md"); r.IsError {
			t.Fatalf("first ingest refused: %s", r.Content)
		}
		// The second body collides with an UNCOMMITTED op at x.md in the
		// open changeset — it must still get a -2 path, not a refusal.
		r := nameIngest(t, reg, "x.md")
		if r.IsError {
			t.Fatalf("second ingest refused: %s", r.Content)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		if got := cs.Ops[1].Path; got != "raw/articles/x-2.md" {
			t.Fatalf("second op staged at %s, want raw/articles/x-2.md", got)
		}
	})

	t.Run("same_body_still_deduped", func(t *testing.T) {
		// A committed occupant with the same body is refused with the
		// existing text, not suffixed around.
		pre := []namedFile{{rel: "raw/articles/x.md", body: "Shared body, committed already.\n"}}
		reg, _ := namingRegistry(t, pre, namingDoc("X", "Shared body, committed already.\n"))
		nameOpen(t, reg)
		r := nameIngest(t, reg, "x.md")
		if !r.IsError {
			t.Fatal("re-ingesting a committed body was accepted")
		}
		if !strings.Contains(r.Content, "is already ingested at raw/articles/x.md") {
			t.Errorf("result = %q, want the existing dedupe text naming raw/articles/x.md", r.Content)
		}

		// A staged op with the same body is refused with the existing
		// in-changeset text, not suffixed around either.
		same := namingDoc("X", "Shared body, staged already.\n")
		reg2, _ := namingRegistry(t, nil, same)
		nameOpen(t, reg2)
		if r := nameIngest(t, reg2, "x.md"); r.IsError {
			t.Fatalf("first ingest refused: %s", r.Content)
		}
		r2 := nameIngest(t, reg2, "x.md")
		if !r2.IsError {
			t.Fatal("re-ingesting a staged body was accepted")
		}
		if !strings.Contains(r2.Content, "already ingested or proposed in this changeset") {
			t.Errorf("result = %q, want the existing in-changeset dedupe text", r2.Content)
		}

		// C-806: the same body under a DIFFERENT source_url is different
		// whole-file bytes, so a whole-file-sha comparison would buy it a
		// -2 suffix instead of the refusal. The criterion is the body sha,
		// regardless of path.
		c806 := []*extract.Doc{
			{Title: "X", SourceURL: "https://example.test/one", Markdown: "Shared C-806 body.\n", Kind: "article", Extractor: "test"},
			{Title: "X", SourceURL: "https://example.test/two", Markdown: "Shared C-806 body.\n", Kind: "article", Extractor: "test"},
		}
		reg3, _ := namingRegistry(t, nil, c806...)
		nameOpen(t, reg3)
		if r := nameIngest(t, reg3, "x.md"); r.IsError {
			t.Fatalf("first C-806 ingest refused: %s", r.Content)
		}
		r3 := nameIngest(t, reg3, "x.md")
		if !r3.IsError {
			t.Fatal("the same body under a different source_url was staged instead of refused")
		}
		if !strings.Contains(r3.Content, "already ingested or proposed in this changeset") {
			t.Errorf("result = %q, want the existing in-changeset dedupe text", r3.Content)
		}
	})

	t.Run("result_names_the_collision", func(t *testing.T) {
		pre := []namedFile{{rel: "raw/articles/x.md", body: "# X\n\nOccupant body.\n"}}
		reg, _ := namingRegistry(t, pre, namingDoc("X", "# X\n\nA newer body.\n"))
		nameOpen(t, reg)
		r := nameIngest(t, reg, "x.md")
		if r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		want := "named raw/articles/x-2.md because raw/articles/x.md already holds a different source."
		if !strings.Contains(r.Content, want) {
			t.Errorf("result = %q, want it to contain %q", r.Content, want)
		}

		// A non-suffixed result says nothing about collisions.
		reg2, _ := namingRegistry(t, nil, namingDoc("Y", "# Y\n\nUncontested body.\n"))
		nameOpen(t, reg2)
		r2 := nameIngest(t, reg2, "y.md")
		if r2.IsError {
			t.Fatalf("ingest refused: %s", r2.Content)
		}
		if strings.Contains(r2.Content, "already holds") {
			t.Errorf("non-suffixed result mentions a collision: %q", r2.Content)
		}
	})
}

package stage

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// newTestEngine opens an Engine over a private copy of the "minimal"
// fixture vault, with a frozen clock so timestamps are reproducible
// (00-conventions.md §3). It registers e.Close via t.Cleanup.
func newTestEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")

	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()
	return e, dir
}

// testAuthor is the Author every test in this package proposes changesets
// and ops as, unless a test needs a different one.
var testAuthor = Author{Kind: "agent", Model: "test-model"}

// newConceptPageContent returns a well-formed wiki/concepts page body
// under path, with frontmatter valid against the minimal fixture's
// SCHEMA.md taxonomy and at least 2 outbound wikilinks to pages that
// already exist in the minimal fixture — the shape validateCreatePage
// demands.
func newConceptPageContent(title string) []byte {
	return []byte("---\n" +
		"title: " + title + "\n" +
		"created: 2026-08-29\n" +
		"updated: 2026-08-29\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# " + title + "\n" +
		"\n" +
		"See [[kv-cache]] and [[gpt-4]] for background.\n")
}

// stageKVCachePatch appends one live patch_page op rewriting a line of the
// fixture's kv-cache page — the smallest op the minimal fixture validates —
// and returns the op id. This is the package's standard "give this test one
// live op" step, for tests that commit (a commit with zero live ops is
// refused, 008 ErrNothingToCommit) and for tests that need changeset
// traffic of any kind.
func stageKVCachePatch(t *testing.T, e *Engine) string {
	t.Helper()
	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	const oldLine = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newLine := "- [[flash-attention]] — a kernel design that reduces the memory cost"
	if !strings.Contains(page.Body, oldLine) {
		t.Fatalf("fixture body does not contain the hunk's old line:\n%s", page.Body)
	}
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)
	opID, err := e.Append(Op{
		Kind:      OpPatchPage,
		Path:      page.Path,
		Section:   "## Related",
		Before:    page.SHA256(),
		Content:   rewritten.Serialize(),
		Rationale: "this test needs one live op",
		Hunks: []Hunk{{
			ID:   "h1",
			Path: page.Path,
			Del:  []string{oldLine},
			Add:  []string{newLine},
		}},
	})
	if err != nil {
		t.Fatalf("Append patch_page: %v", err)
	}
	return opID
}

// stagedRawDoc renders the whole serialized raw/ file an ingest_source op
// carries as its Content: the three-key provenance block, a blank line,
// then body — the shape internal/tools' stage.ingest_source proposes.
func stagedRawDoc(sourceURL, body string) []byte {
	d, _ := vault.ParseDate("2026-08-29")
	return (&vault.RawSource{
		SourceURL: sourceURL,
		Ingested:  d,
		SHA256:    vault.BodySHA256(body),
		Body:      body,
	}).Serialize()
}

// stageIngest appends one live ingest_source op at path carrying body,
// wrapped as a whole raw/ document, and returns the op id.
func stageIngest(t *testing.T, e *Engine, path, body string) string {
	t.Helper()
	opID, err := e.Append(Op{
		Kind:      OpIngestSource,
		Path:      path,
		Extractor: "go/html",
		Content:   stagedRawDoc("https://example.test/"+path, body),
	})
	if err != nil {
		t.Fatalf("Append ingest_source %s: %v", path, err)
	}
	return opID
}

// syntheticNoBacklinksVault returns a tiny in-memory vault (SCHEMA.md,
// index.md, and two pages) in which wiki/concepts/lonely.md has no
// inbound wikilink from anywhere — not from another page, not from
// index.md — the shape TestRenameWithNoBacklinksValidates needs and that
// the shipped minimal fixture cannot provide (every one of its four pages
// is linked from index.md by construction).
func syntheticNoBacklinksVault(t *testing.T) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		"SCHEMA.md": &fstest.MapFile{Data: []byte("# SCHEMA\n\n" +
			"## Domain\n\nsynthetic\n\n" +
			"## Tags\n\n- `inference` — a tag.\n")},
		"index.md": &fstest.MapFile{Data: []byte("# Index\n\n- [[anchor]] — the only linked page.\n")},
		"wiki/concepts/anchor.md": &fstest.MapFile{Data: []byte("---\n" +
			"title: Anchor\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: concept\n" +
			"tags: [inference]\n---\n\nNo outbound links needed for this fixture.\n")},
		"wiki/concepts/lonely.md": &fstest.MapFile{Data: []byte("---\n" +
			"title: Lonely\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: concept\n" +
			"tags: [inference]\n---\n\nSee [[anchor]] for context, but nothing links back.\n")},
	}
}

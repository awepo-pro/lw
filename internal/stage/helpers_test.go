package stage

import (
	"testing"
	"testing/fstest"

	"github.com/awepo-pro/lw/internal/testutil"
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

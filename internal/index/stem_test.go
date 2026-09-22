package index

import (
	"strings"
	"testing"
)

// TestStemTable pins 028's F.S1 table exactly, one row per frozen mapping.
// The library (snowball v0.10.0) is the authority on every English row: if
// it disagrees, the row is amended to the library's actual output and
// logged as a Test-spec amendment — never hand-patched.
func TestStemTable(t *testing.T) {
	cases := []struct{ tok, want string }{
		{"decoding", "decod"},
		{"decoder", "decod"},
		{"decode", "decod"},
		{"caching", "cach"},
		{"cache", "cach"},
		{"running", "run"},
		{"studies", "studi"},
		{"speculative-decoding", "specul-decod"},
		{"kv-cache", "kv-cach"},
		{"gpt4", "gpt4"}, // a digit: passed through unchanged
		{"café", "café"}, // a non-ASCII rune: passed through unchanged
	}
	for _, c := range cases {
		if got := stem(c.tok); got != c.want {
			t.Errorf("stem(%q) = %q, want %q", c.tok, got, c.want)
		}
	}
}

// TestMorphologyRanks pins 028's headline behaviour: queries in any
// surface form of a word rank the page whose abstract uses a DIFFERENT
// surface form of the same word first. Page A's abstract says
// "speculative decoding"; pre-028 the query "decoder" missed it entirely
// because no surface token of A equals "decoder".
func TestMorphologyRanks(t *testing.T) {
	ix := absIndexOver(t, map[string]string{
		"wiki/concepts/a-decoding.md": absPageSrc("Speculative Decoding",
			"## Abstract\n\nspeculative decoding drafts tokens.\n\n## Notes\n\nthe drafts are verified token by token.\n"),
		"wiki/concepts/b-caching.md": absPageSrc("Caching",
			"## Abstract\n\ncaching keeps warm results.\n\n## Notes\n\na cache hit is cheap.\n"),
		"wiki/concepts/c-quokka.md": absPageSrc("Quokka",
			"## Abstract\n\nquokka habitat notes.\n\n## Notes\n\nunrelated prose.\n"),
	})

	for _, q := range []string{"decoder", "decode", "decoding"} {
		hits := ix.Search(q, Options{})
		if len(hits) == 0 || hits[0].Path != "wiki/concepts/a-decoding.md" {
			t.Errorf("Search(%q) = %+v, want a-decoding.md first (pre-028 the query missed it)", q, hits)
		}
	}
}

// TestHyphenCompound pins the compound path: a body wikilink to
// [[speculative-decoding]] must be found by a query that splits the
// compound differently — "speculative decoder" — with both sides stemmed
// per segment after the wikilink extra-segment rule ran.
func TestHyphenCompound(t *testing.T) {
	ix := absIndexOver(t, map[string]string{
		"wiki/concepts/linker.md": absPageSrc("Linker",
			"See [[speculative-decoding]] for the trick.\n"),
	})

	hits := ix.Search("speculative decoder", Options{})
	if len(hits) == 0 || hits[0].Path != "wiki/concepts/linker.md" {
		t.Fatalf("Search(speculative decoder) = %+v, want linker.md", hits)
	}
}

// TestSnippetLiteral pins F.S3: the query's surface form is never in the
// text ("decoder" does not occur in "speculative decoding"), so the
// literal surface pass misses, the stem pass centres the window on the
// stem's position, and the snippet the reader sees contains the text's own
// surface word.
func TestSnippetLiteral(t *testing.T) {
	ix := absIndexOver(t, map[string]string{
		"wiki/concepts/a-decoding.md": absPageSrc("Speculative Decoding",
			"lead filler prose.\n\n## Abstract\n\nspeculative decoding drafts tokens.\n\n## Notes\n\nmore prose.\n"),
	})

	hits := ix.Search("decoder", Options{})
	if len(hits) == 0 {
		t.Fatal("Search(decoder) returned no hits")
	}
	if !strings.Contains(hits[0].Snippet, "decoding") {
		t.Fatalf("snippet %q does not contain the surface word %q — the stem pass must centre it there",
			hits[0].Snippet, "decoding")
	}
}

package index

import (
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// openMinimal copies spec/fixtures/minimal into a private temp dir and
// opens it as a *vault.Vault.
func openMinimal(t *testing.T) *vault.Vault {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	return v
}

func mustDate(t *testing.T, s string) vault.Date {
	t.Helper()
	d, err := vault.ParseDate(s)
	if err != nil {
		t.Fatalf("vault.ParseDate(%q): %v", s, err)
	}
	return d
}

// TestSearchRanking is the stage file's headline assertion: searching for
// "speculative decoding" over spec/fixtures/minimal must rank
// wiki/concepts/speculative-decoding.md first — it matches the query in its
// title (x3), one of its tags (x2), and its body.
func TestSearchRanking(t *testing.T) {
	v := openMinimal(t)
	ix := Build(v)

	hits := ix.Search("speculative decoding", Options{})
	if len(hits) == 0 {
		t.Fatal("Search returned no hits")
	}
	if hits[0].Path != "wiki/concepts/speculative-decoding.md" {
		t.Fatalf("hits[0].Path = %q, want %q\nfull hits: %+v",
			hits[0].Path, "wiki/concepts/speculative-decoding.md", hits)
	}
	if hits[0].Score <= 0 {
		t.Fatalf("hits[0].Score = %v, want > 0", hits[0].Score)
	}
}

// TestSearchFindsAliasWords proves the point of indexing a wikilink's
// alias (backbone §3 amendment): a page becomes findable by words that only
// ever appear as an alias, never as the link's target or anywhere else in
// prose. wiki.search is the agent's primary discovery surface and, per
// /docs/design.md §11.3, what stops duplicate pages being created — it must be
// able to find a page by the words actually rendered on it.
func TestSearchFindsAliasWords(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}

	// Sanity: "gizmo" appears nowhere in the unmodified fixture.
	if hits := Build(v).Search("gizmo", Options{}); len(hits) != 0 {
		t.Fatalf("gizmo unexpectedly found before the edit: %+v", hits)
	}

	const kvCachePath = "wiki/concepts/kv-cache.md"
	abs := filepath.Join(dir, filepath.FromSlash(kvCachePath))
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read %s: %v", kvCachePath, err)
	}
	edited := string(b) + "\n[[flash-attention|gizmo optimization]]\n"
	if err := os.WriteFile(abs, []byte(edited), 0o644); err != nil {
		t.Fatalf("write %s: %v", kvCachePath, err)
	}
	if err := v.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	hits := Build(v).Search("gizmo", Options{})
	assertPaths(t, hits, kvCachePath)
}

// TestFilters exercises Type, AND-ed Tags, and a date window, each chosen
// so the filter provably removes a page that would otherwise match the
// query.
func TestFilters(t *testing.T) {
	v := openMinimal(t)
	ix := Build(v)

	t.Run("Type", func(t *testing.T) {
		// "model" appears in both speculative-decoding.md (concept) and
		// gpt-4.md (entity); Type:"entity" must leave only the latter.
		hits := ix.Search("model", Options{Type: "entity"})
		assertPaths(t, hits, "wiki/entities/gpt-4.md")
	})

	t.Run("Tags AND", func(t *testing.T) {
		// "cache" appears in both kv-cache.md (tags: inference, memory) and
		// speculative-decoding.md (tags: inference, decoding). Requiring
		// both "inference" and "decoding" must exclude kv-cache.md, which
		// has no "decoding" tag.
		hits := ix.Search("cache", Options{Tags: []string{"inference", "decoding"}})
		assertPaths(t, hits, "wiki/concepts/speculative-decoding.md")
	})

	t.Run("Date window excludes a known page", func(t *testing.T) {
		// "attention" appears in both flash-attention.md (updated
		// 2026-08-27) and kv-cache.md (updated 2026-08-29, "attention
		// projections"). Before 2026-08-28 must exclude kv-cache.md even
		// though it matches the query.
		hits := ix.Search("attention", Options{Before: mustDate(t, "2026-08-28")})
		assertPaths(t, hits, "wiki/concepts/flash-attention.md")
	})

	t.Run("After excludes everything", func(t *testing.T) {
		hits := ix.Search("attention", Options{After: mustDate(t, "2027-01-01")})
		if len(hits) != 0 {
			t.Fatalf("hits = %+v, want none", hits)
		}
	})
}

// assertPaths fails t unless hits' paths are exactly want, in order.
func assertPaths(t *testing.T, hits []Hit, want ...string) {
	t.Helper()
	if len(hits) != len(want) {
		t.Fatalf("got %d hits %+v, want paths %v", len(hits), hits, want)
	}
	for i, h := range hits {
		if h.Path != want[i] {
			t.Fatalf("hits[%d].Path = %q, want %q (full hits: %+v)", i, h.Path, want[i], hits)
		}
	}
}

// TestIncrementalMatchesFull proves an index built entirely through Update
// calls, and an index with a page dropped through Update, both match a
// fully-rebuilt Index byte-for-byte in every field that Search exposes. An
// incremental index that drifts from a full rebuild is worse than none
// (stage file, S1-T4).
func TestIncrementalMatchesFull(t *testing.T) {
	v := openMinimal(t)
	full := Build(v)

	t.Run("built entirely via Update", func(t *testing.T) {
		var incremental Index // zero value: must be usable directly
		for _, p := range v.Pages() {
			incremental.Update(v, []string{p.Path})
		}
		if incremental.Len() != full.Len() {
			t.Fatalf("incremental.Len() = %d, full.Len() = %d", incremental.Len(), full.Len())
		}
		for _, q := range searchQueries {
			assertSameHits(t, q, full.Search(q, Options{}), incremental.Search(q, Options{}))
		}
	})

	t.Run("Update drops a page that no longer exists", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		v2, err := vault.Open(dir)
		if err != nil {
			t.Fatalf("vault.Open: %v", err)
		}

		clone := Build(v2)
		const removed = "wiki/concepts/flash-attention.md"
		if _, ok := v2.Page(removed); !ok {
			t.Fatalf("fixture missing %s", removed)
		}

		if err := removeFile(dir, removed); err != nil {
			t.Fatalf("remove %s: %v", removed, err)
		}
		if err := v2.Reload(); err != nil {
			t.Fatalf("Reload: %v", err)
		}

		clone.Update(v2, []string{removed})
		wantFull := Build(v2)

		if clone.Len() != wantFull.Len() {
			t.Fatalf("clone.Len() = %d, want %d", clone.Len(), wantFull.Len())
		}
		for _, q := range searchQueries {
			assertSameHits(t, q, wantFull.Search(q, Options{}), clone.Search(q, Options{}))
		}
	})
}

// searchQueries is reused by the incremental and determinism tests so both
// exercise several different fields (title, tag, body, wikilink-derived).
var searchQueries = []string{
	"speculative decoding",
	"cache",
	"attention",
	"gpt-4",
	"model",
}

// assertSameHits fails t unless got and want are identical in every field
// Hit exposes, in the same order.
func assertSameHits(t *testing.T, q string, want, got []Hit) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("query %q: got %d hits, want %d\ngot:  %+v\nwant: %+v", q, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("query %q: hit[%d] = %+v, want %+v", q, i, got[i], want[i])
		}
	}
}

// TestDeterministicBuild builds the same vault twice, independently, and
// requires identical Search results — nothing in Build or Search may leak
// map iteration order into the output.
func TestDeterministicBuild(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	v1, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	v2, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}

	ix1 := Build(v1)
	ix2 := Build(v2)

	if ix1.Len() != ix2.Len() {
		t.Fatalf("ix1.Len() = %d, ix2.Len() = %d", ix1.Len(), ix2.Len())
	}
	for _, q := range searchQueries {
		assertSameHits(t, q, ix1.Search(q, Options{}), ix2.Search(q, Options{}))
		// Repeated calls on the very same Index must also agree with
		// themselves.
		assertSameHits(t, q, ix1.Search(q, Options{}), ix1.Search(q, Options{}))
	}
}

// TestSnippetMultibyte proves Snippet never splits a UTF-8 rune, even when
// the window boundary would otherwise land in the middle of a multi-byte
// character, and never exceeds 200 runes. spec/fixtures/minimal's page
// bodies contain "—" (an em dash, 3 bytes) for exactly this reason.
func TestSnippetMultibyte(t *testing.T) {
	v := openMinimal(t)
	ix := Build(v)

	hits := ix.Search("cache", Options{})
	if len(hits) == 0 {
		t.Fatal("Search returned no hits")
	}
	for _, h := range hits {
		if !utf8.ValidString(h.Snippet) {
			t.Errorf("%s: snippet is not valid UTF-8: %q", h.Path, h.Snippet)
		}
		if n := utf8.RuneCountInString(h.Snippet); n > snippetMaxRunes {
			t.Errorf("%s: snippet has %d runes, want <= %d", h.Path, n, snippetMaxRunes)
		}
	}

	// A synthetic body forces the naive centre point to land inside a
	// multi-byte rune's bytes; runeWindow must still produce valid UTF-8.
	body := ""
	for i := 0; i < 40; i++ {
		body += "café résumé naïve façade coöperate "
	}
	for byteOffset := 0; byteOffset < len(body); byteOffset += 7 {
		got := runeWindow(body, byteOffset)
		if !utf8.ValidString(got) {
			t.Fatalf("runeWindow(body, %d) produced invalid UTF-8: %q", byteOffset, got)
		}
		if n := utf8.RuneCountInString(got); n > snippetMaxRunes {
			t.Fatalf("runeWindow(body, %d) has %d runes, want <= %d", byteOffset, n, snippetMaxRunes)
		}
	}
}

// TestLenAndStaleAgainst covers the small, easily-regressed bookkeeping
// methods together with a real vault.
func TestLenAndStaleAgainst(t *testing.T) {
	v := openMinimal(t)
	ix := Build(v)

	if got, want := ix.Len(), len(v.Pages()); got != want {
		t.Fatalf("Len() = %d, want %d", got, want)
	}
	if ix.StaleAgainst(v) {
		t.Fatal("StaleAgainst(v) = true immediately after Build(v)")
	}

	var empty Index
	if !empty.StaleAgainst(v) {
		t.Fatal("an empty Index must be stale against a non-empty vault")
	}
}

// removeFile deletes the file at vault-relative path rel under dir.
func removeFile(dir, rel string) error {
	return os.Remove(filepath.Join(dir, filepath.FromSlash(rel)))
}

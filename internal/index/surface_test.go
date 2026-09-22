// surface_test.go pins 028's A-028-2 exact-surface boost. T1's stemming made
// "decoder" find "decoding", but the measured corpus showed the price: a
// stem that OVER-merges ("lateral" and "later" both stem to "later") lets a
// page whose prose merely says "later" outrank the page that actually spells
// the query. The frozen answer is BM25F's: keep the stemmed recall, and let
// an exact surface-form match multiply the BM25F sum by up to 1+surfaceBoost.
package index

import (
	"bytes"
	"encoding/gob"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/awepo-pro/lw/internal/vault"
)

// lateralCollision is the measured case as a fixture: page A is the security
// page the query is really about (its abstract says "lateral"), pages B and
// C are the quaternion pages whose prose merely says "later" — more times
// than A says "lateral", which is exactly why raw BM25F put them above A
// before the boost. B and C carry no surface "lateral", so the boost must
// not touch them; A carries it, so the boost must put A back on top.
func lateralCollision(t *testing.T) *Index {
	t.Helper()
	return absIndexOver(t, map[string]string{
		"wiki/concepts/a-private-network-access.md": absPageSrc("Private Network Access",
			"## Abstract\n\nLateral movement across a flat network.\n\n## Notes\n\nmoves between hosts without crossing segment boundaries.\n"),
		"wiki/concepts/b-quaternion-basics.md": absPageSrc("Quaternion Basics",
			"later later later later later later later later quaternion rotation vector scalar basis orientation angular velocity formulate.\n"),
		"wiki/concepts/c-quaternion-advanced.md": absPageSrc("Quaternion Advanced",
			"later later later later later later later later quaternion rotation vector scalar basis orientation angular velocity formulate.\n"),
	})
}

func TestExactSurfaceBoost(t *testing.T) {
	t.Run("exact_outranks_collision", func(t *testing.T) {
		ix := lateralCollision(t)

		hits := ix.Search("lateral", Options{})
		if len(hits) != 3 {
			t.Fatalf("Search(lateral) = %d hits, want 3: %+v", len(hits), hits)
		}
		// Full order: the boosted page first, then the two collision pages
		// in their path-tie order.
		assertPaths(t, hits,
			"wiki/concepts/a-private-network-access.md",
			"wiki/concepts/b-quaternion-basics.md",
			"wiki/concepts/c-quaternion-advanced.md")
	})

	t.Run("morphology_preserved", func(t *testing.T) {
		// T1's headline ranking (stem_test.go TestMorphologyRanks) must hold
		// with the boost in place. "decoder" and "decode" appear in no
		// page's surface vocabulary, so every multiplier is exactly 1 and
		// the ranking is T1's own; "decoding" surface-matches page A and
		// was already first.
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
				t.Errorf("Search(%q) = %+v, want a-decoding.md first (T1 morphology)", q, hits)
			}
		}
	})

	t.Run("boost_is_bounded", func(t *testing.T) {
		// One doc whose surface says "alpha" and "beta" — never "alphas" or
		// "betas". The two queries stem to the SAME terms (snowball strips
		// the plural), so their BM25F sums are the same number S; the only
		// difference is the surface factor: 1.25 for the query the doc
		// spells, exactly 1 for the surface-absent variant (pure
		// morphology). Equal S means Score(alpha beta) must equal
		// 1.25*Score(alphas betas) exactly — which also proves no
		// compounding per token (1.25² = 1.5625 would fail the equality).
		ix := absIndexOver(t, map[string]string{
			"wiki/concepts/alpha-beta.md": absPageSrc("Alpha Beta",
				"## Abstract\n\nalpha beta pairing keeps the example small.\n\n## Notes\n\nmore alpha beta prose.\n"),
		})

		spelled := ix.Search("alpha beta", Options{})
		morph := ix.Search("alphas betas", Options{})
		if len(spelled) != 1 || len(morph) != 1 {
			t.Fatalf("got %d and %d hits, want one each: %+v | %+v", len(spelled), len(morph), spelled, morph)
		}
		if spelled[0].Path != morph[0].Path {
			t.Fatalf("the variants matched different docs: %q vs %q", spelled[0].Path, morph[0].Path)
		}
		if want := 1.25 * morph[0].Score; spelled[0].Score != want {
			t.Fatalf("full surface match Score = %v, want exactly 1.25 × the unboosted %v = %v",
				spelled[0].Score, morph[0].Score, want)
		}
	})

	t.Run("round_trip", func(t *testing.T) {
		if indexSchema != 3 {
			t.Fatalf("indexSchema = %d, want the frozen A-028-2 value 3", indexSchema)
		}

		fsys := fstest.MapFS{
			"SCHEMA.md": &fstest.MapFile{
				Data: []byte("# SCHEMA\n\n## Tags\n\n- `inference` — running a trained model to produce outputs.\n"),
			},
			"wiki/concepts/tapir.md": &fstest.MapFile{Data: []byte(absPageSrc("Tapir Study",
				"## Abstract\n\nthe tapir summary mentions foraging and habitat.\n\n## Notes\n\nmore tapir notes about foraging.\n"))},
		}
		v, err := vault.OpenFS(fsys)
		if err != nil {
			t.Fatalf("vault.OpenFS: %v", err)
		}
		original := Build(v)

		// The set is surface (pre-stem), per doc, over every field — title
		// included — and nothing else: no stems, no frequencies, no absent
		// field.
		wantSet := map[string]struct{}{
			"tapir": {}, "study": {}, // title (tapir also in abstract+body)
			"abstract": {}, "summary": {}, "mentions": {}, "foraging": {}, "habitat": {}, // abstract section (heading included)
			"more": {}, "notes": {}, "about": {}, // notes section
		}
		got := (*original.docs.Load())["wiki/concepts/tapir.md"].SurfaceSet
		if !reflect.DeepEqual(got, wantSet) {
			t.Fatalf("built SurfaceSet = %v, want the surface vocabulary %v", got, wantSet)
		}

		path := filepath.Join(t.TempDir(), "index.gob")
		if err := original.Save(path); err != nil {
			t.Fatalf("Save: %v", err)
		}
		loaded, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		after := (*loaded.docs.Load())["wiki/concepts/tapir.md"].SurfaceSet
		if !reflect.DeepEqual(after, wantSet) {
			t.Fatalf("loaded SurfaceSet = %v, want %v — the surface set did not survive Save/Load", after, wantSet)
		}
		if loaded.StaleAgainst(v) {
			t.Fatal("a freshly saved schema-3 index is stale after Save/Load")
		}

		// A schema-2 file (T1's layout: no Surface field on the wire) must
		// be stale against a schema-3 want, even with every page SHA
		// matching — only the version can say so. Built the way a T1 binary
		// would have written it: current docs, wire Schema rewritten to 2.
		blob, err := loaded.GobEncode()
		if err != nil {
			t.Fatalf("GobEncode: %v", err)
		}
		var g gobIndex
		if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&g); err != nil {
			t.Fatalf("decode gobIndex: %v", err)
		}
		g.Schema = 2
		var inner bytes.Buffer
		if err := gob.NewEncoder(&inner).Encode(&g); err != nil {
			t.Fatalf("encode schema-2 gobIndex: %v", err)
		}
		legacy := &Index{}
		if err := legacy.GobDecode(inner.Bytes()); err != nil {
			t.Fatalf("GobDecode schema-2: %v", err)
		}
		if !legacy.StaleAgainst(v) {
			t.Fatal("a schema-2 index must be stale against a schema-3 want")
		}
		// And its nil surface sets must be safe to score, not panic: the
		// factor is 1, the stemmed fields still answer.
		hits := legacy.Search("foraging", Options{})
		if len(hits) != 1 || hits[0].Path != "wiki/concepts/tapir.md" {
			t.Fatalf("schema-2 decoded Search(foraging) = %+v, want tapir.md without a panic", hits)
		}
	})
}

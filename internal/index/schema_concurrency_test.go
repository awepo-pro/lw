// schema_concurrency_test.go pins the read/write pairing of the (docs,
// schema) atomics that 028 added: every writer (Rebuild, GobDecode) stores
// docs FIRST and schema second, and every reader that interprets the docs
// must therefore load schema FIRST — a reader that sees the current schema
// must never be able to see anything but the docs that schema describes.
// StaleAgainst already loads in that order; this file pins that GobEncode
// — Save's payload — does too, so an encode racing a legacy→current
// rebuild can never emit a file that claims the current schema over
// pre-stemming term frequencies. Such a file would pass StaleAgainst
// forever (every page SHA matches; only the version could say otherwise)
// and every later open would trust raw-term frequencies as stemmed ones.
package index

import (
	"bytes"
	"encoding/gob"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// legacyMarkerKey stands in for a raw (pre-stemming) term-frequency key: it
// exists in a legacy docEntry and in nothing Build produces, so its presence
// in an encoded doc identifies legacy docs beyond doubt.
const legacyMarkerKey = "~legacy-raw-term~"

// legacyPair returns an Index holding the fixture's docs with legacy marker
// freq keys and schema 0 — the in-memory state a pre-028 index.gob decodes
// to (schema_test.go's wire-level case, made concurrent here). The single
// legacy→current transition each instance then makes is the only transition
// a real index ever makes: legacy state comes from GobDecode at Load, and
// Rebuild is what ends it.
func legacyPair(t *testing.T, v *vault.Vault) *Index {
	t.Helper()
	fresh := Build(v)
	old := fresh.docs.Load()
	docs := make(map[string]*docEntry, len(*old))
	for k, d := range *old {
		nd := *d
		nd.BodyTermFreq = map[string]int{legacyMarkerKey: 1}
		docs[k] = &nd
	}
	ix := &Index{}
	ix.docs.Store(&docs)
	ix.schema.Store(0)
	return ix
}

// TestGobEncodeNeverPairsCurrentSchemaWithLegacyDocs races Save's payload
// against that one transition and requires every encoded blob that claims
// the current schema to describe current docs. The tear direction this
// forbids is the harmful one: the reverse tear (current docs under the old
// schema) merely makes the next open rebuild once more.
func TestGobEncodeNeverPairsCurrentSchemaWithLegacyDocs(t *testing.T) {
	v := openMinimal(t)

	for round := 0; round < 200; round++ {
		ix := legacyPair(t, v)
		done := make(chan struct{})
		go func() {
			defer close(done)
			ix.Rebuild(v) // the real code path: docs.Store, then schema.Store
		}()

		for {
			blob, err := ix.GobEncode()
			if err != nil {
				t.Fatalf("round %d: GobEncode: %v", round, err)
			}
			var g gobIndex
			if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&g); err != nil {
				t.Fatalf("round %d: decode blob: %v", round, err)
			}
			if g.Schema == indexSchema {
				for _, d := range g.Docs {
					if _, bad := d.BodyTermFreq[legacyMarkerKey]; bad {
						t.Fatalf("round %d: GobEncode paired schema %d with legacy (pre-stemming) docs — a save concurrent with a rebuild poisons the cache", round, g.Schema)
					}
				}
			}
			select {
			case <-done:
				goto nextRound
			default:
			}
		}
	nextRound:
	}
}

// TestStaleAgainstSeesCurrentSchemaOnlyWithCurrentDocs pins the same pairing
// on the reader that decides rebuilds: across a legacy→current transition,
// StaleAgainst may report stale (the benign tear — the next open rebuilds
// once more), but the moment it reports current, the docs behind that
// answer are the current ones.
func TestStaleAgainstSeesCurrentSchemaOnlyWithCurrentDocs(t *testing.T) {
	v := openMinimal(t)

	for round := 0; round < 200; round++ {
		ix := legacyPair(t, v)
		done := make(chan struct{})
		go func() {
			defer close(done)
			ix.Rebuild(v)
		}()

		for {
			if !ix.StaleAgainst(v) {
				// Reported current: the schema load passed, so the docs load
				// that follows must see the rebuilt map. Reload the docs the
				// same way StaleAgainst does and confirm.
				p := ix.docs.Load()
				if p == nil {
					t.Fatalf("round %d: StaleAgainst said current with no docs", round)
				}
				if d := (*p)["wiki/concepts/speculative-decoding.md"]; d == nil {
					t.Fatalf("round %d: StaleAgainst said current but the fixture doc is gone", round)
				} else if _, bad := d.BodyTermFreq[legacyMarkerKey]; bad {
					t.Fatalf("round %d: StaleAgainst said current over legacy docs", round)
				}
			}
			select {
			case <-done:
				goto nextRound
			default:
			}
		}
	nextRound:
	}
}

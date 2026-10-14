package stage

// schema_conformance_original_test.go extends TestSchemaConformance's 033
// .pdf case (schema_conformance_test.go) to the originals 058 writes: a saved
// page (.html, .htm), plain text (.txt, .markdown) and a markdown source's
// x.md.orig. Every changeset.json that carries one must validate against
// spec/changeset.schema.json — the schema's original_path pattern has
// additionalProperties:false behind it, so a pattern that refused an
// extension would make a perfectly good ingest unreadable on disk — and the
// pattern must still refuse what the engine refuses: the raw's own .md name.

import (
	"encoding/json"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// ingestChangesetWithOriginal is a one-op changeset whose ingest_source
// carries originalPath beside raw/articles/postgres-step-by-step.md.
func ingestChangesetWithOriginal(originalPath string) *Changeset {
	return &Changeset{
		ID:       "cs-5858585",
		Intent:   "ingest a source with its original",
		Author:   Author{Kind: "agent", Model: "m"},
		OpenedAt: testutil.FixedClock()(),
		Ops: []Op{{
			ID:           "op1",
			Kind:         OpIngestSource,
			Path:         "raw/articles/postgres-step-by-step.md",
			SHA256:       "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111",
			Extractor:    "go/html",
			OriginalPath: originalPath,
			Original:     "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222",
			State:        StateProposed,
		}},
		Checks: Checks{Schema: "pass", Lint: "pass", Orphans: 0, BrokenLinks: 0},
	}
}

// TestSchemaConformanceOriginals: the original paths 058 stages validate, and
// the one the engine refuses does not.
func TestSchemaConformanceOriginals(t *testing.T) {
	root, s := loadChangesetSchema(t)

	for _, path := range []string{
		"raw/articles/postgres-step-by-step.html",
		"raw/articles/postgres-step-by-step.htm",
		"raw/articles/postgres-step-by-step.txt",
		"raw/articles/postgres-step-by-step.markdown",
		"raw/articles/postgres-step-by-step.md.orig",
		"raw/articles/postgres-step-by-step-2.md.orig",
		"raw/papers/postgres-step-by-step.pdf",
	} {
		t.Run("accepts "+path, func(t *testing.T) {
			assertValidatesAgainstSchema(t, root, s, ingestChangesetWithOriginal(path))
		})
	}

	for _, path := range []string{
		"raw/articles/postgres-step-by-step.md", // the raw's own name: never an original
		"raw/articles/postgres-step-by-step",    // no extension at all
		"wiki/concepts/postgres-step-by-step.html",
	} {
		t.Run("refuses "+path, func(t *testing.T) {
			b, err := json.Marshal(ingestChangesetWithOriginal(path))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var generic any
			if err := json.Unmarshal(b, &generic); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if errs := s.validate(root, generic, "$"); len(errs) == 0 {
				t.Fatalf("original_path %q validated; want a schema violation:\n%s", path, b)
			}
		})
	}
}

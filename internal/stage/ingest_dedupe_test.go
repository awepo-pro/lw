// ingest_dedupe_test.go pins the body-hash half of validateIngestSource's
// dedupe (008 contract §3): an ingest_source op carrying the WHOLE
// serialized raw file — frontmatter included, the shape stage.ingest_source
// really proposes — is refused when its BODY matches a committed source.
// That check had gone dead when proposals grew frontmatter; only the
// bare-body shape ever matched. The error text is unchanged.
package stage

import (
	"strings"
	"testing"
)

func TestIngestHashDedupeBody(t *testing.T) {
	t.Run("whole_file_same_body_rejected", func(t *testing.T) {
		v := newTestVault(t)
		existing, ok := v.RawSource("raw/papers/leviathan-2023.md")
		if !ok {
			t.Fatal("fixture missing raw/papers/leviathan-2023.md")
		}
		op := Op{
			Kind:      OpIngestSource,
			Path:      "raw/papers/whole-file-duplicate.md",
			Extractor: "go/html",
			// The whole serialized file — frontmatter + body — exactly what
			// stage.ingest_source stages since TD-5. Hashing this whole would
			// never match the committed body sha; hashing the parsed body does.
			Content: existing.Serialize(),
		}
		err := ValidateOp(op, v, v.Schema())
		if err == nil {
			t.Fatal("ValidateOp accepted a whole-file duplicate of a committed source's body")
		}
		if !strings.Contains(err.Error(), "content already ingested at") {
			t.Fatalf("error = %v, want the existing \"content already ingested at\" text", err)
		}
	})

	t.Run("bare_body_shape_still_rejected", func(t *testing.T) {
		v := newTestVault(t)
		existing, ok := v.RawSource("raw/papers/leviathan-2023.md")
		if !ok {
			t.Fatal("fixture missing raw/papers/leviathan-2023.md")
		}
		op := Op{
			Kind:      OpIngestSource,
			Path:      "raw/papers/bare-body-duplicate.md",
			Extractor: "go/html",
			// The pre-TD-5 shape: bare markdown with no frontmatter. It does
			// not parse as a raw source, so the whole-content fallback must
			// still catch it.
			Content: []byte(existing.Body),
		}
		err := ValidateOp(op, v, v.Schema())
		if err == nil {
			t.Fatal("ValidateOp accepted a bare-body duplicate of a committed source's body")
		}
		if !strings.Contains(err.Error(), "content already ingested at") {
			t.Fatalf("error = %v, want the existing \"content already ingested at\" text", err)
		}
	})

	t.Run("different_body_accepted", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:      OpIngestSource,
			Path:      "raw/papers/fresh-body.md",
			Extractor: "go/html",
			Content: stagedRawDoc("https://example.test/fresh-body",
				"# Fresh Body\n\nA body nothing committed carries.\n"),
		}
		if err := ValidateOp(op, v, v.Schema()); err != nil {
			t.Fatalf("ValidateOp: unexpected error: %v", err)
		}
	})
}

package tools

import (
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// TestSourceBodySHA pins A-807's one body-hash rule: SourceBodySHA is the
// exact hash stage.ingest_source records in a raw file's frontmatter, so
// cmdIngest's duplicate pre-check compares the same number the tool writes
// and the two can never disagree (008 contract §14).
func TestSourceBodySHA(t *testing.T) {
	t.Run("matches_what_ingest_source_records", func(t *testing.T) {
		doc := namingDoc("", "A body whose recorded hash must equal SourceBodySHA\nof the markdown it was extracted from.\n")
		reg, e := namingRegistry(t, nil, doc)
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "m3-note.md"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}

		// The hash the tool records for the body lives in the staged file's
		// frontmatter — the number Vault.RawSources() carries once committed
		// and the number the pre-check compares against. (The live Op's own
		// SHA256 field holds the whole-file CAS sha after Append — C-806 —
		// so the frontmatter, not that field, is where the body hash lives.)
		diff := proposedRawDiff(t, e)
		src, err := vault.ParseRawSource(diff.Path, []byte(diff.New))
		if err != nil {
			t.Fatalf("ParseRawSource(%s): %v", diff.Path, err)
		}
		if got, want := src.SHA256, SourceBodySHA(doc.Markdown); got != want {
			t.Errorf("ingest_source recorded sha256 %q, want SourceBodySHA(doc.Markdown) = %q", got, want)
		}
	})

	t.Run("leading_blank_lines_do_not_change_it", func(t *testing.T) {
		md := "# Heading\n\nA body that may arrive with blank leading lines.\n"
		if got, want := SourceBodySHA("\n\n"+md), SourceBodySHA(md); got != want {
			t.Errorf("SourceBodySHA(%q) = %q, want %q (same body, blank leading lines stripped)", "\n\n"+md, got, want)
		}
	})
}

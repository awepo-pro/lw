package ask

// cite_page_test.go is 034 T4's ask-pane pins for paged provenance markers:
// the inline renderer consumes "^[raw/papers/x.md p.12]" into the muted
// "[x.md p.12]" (style id 5, the provenance token), the 027 strip removes it
// together with its preceding space, and the fileability gate accepts it —
// a paged marker is a provenance marker like any other, just with pages.

import (
	"strings"
	"testing"
)

// TestInlineCellsPagedProvenance pins the live tail's rendering: the marker
// becomes "[x.md p.12]" in one run of the provenance style (id 5 into
// inlineStyleTable), with the claim text around it unstyled.
func TestInlineCellsPagedProvenance(t *testing.T) {
	cells := inlineCells("Claim.^[raw/papers/x.md p.12]")

	var got strings.Builder
	ids := map[int]bool{}
	for _, c := range cells {
		if c.id == 5 {
			got.WriteRune(c.r)
			ids[5] = true
		}
	}
	if got.String() != "[x.md p.12]" {
		t.Fatalf("provenance-styled run = %q, want \"[x.md p.12]\"", got.String())
	}

	var plain strings.Builder
	for _, c := range cells {
		plain.WriteRune(c.r)
	}
	if plain.String() != "Claim.[x.md p.12]" {
		t.Fatalf("inlineCells consumed the marker wrong: %q", plain.String())
	}
	if !ids[5] {
		t.Fatal("no cell carries the provenance style id 5")
	}
}

// TestStripMarkersPagedProvenance pins the 027 display strip against the
// paged form: the marker and exactly its one preceding space go.
func TestStripMarkersPagedProvenance(t *testing.T) {
	if got := stripMarkers("Claim. ^[raw/papers/x.md p.12]", false); got != "Claim." {
		t.Fatalf("stripMarkers = %q, want %q", got, "Claim.")
	}
}

// TestFileablePagedProvenance pins that the fileability gate reads a paged
// marker as provenance — an answer citing pages is still fileable.
func TestFileablePagedProvenance(t *testing.T) {
	if !fileableRe.MatchString("^[raw/papers/x.md p.12]") {
		t.Fatal("fileableRe does not match a paged provenance marker")
	}
}

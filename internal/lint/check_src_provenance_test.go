package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// TestSrcProvenanceDirty isolates src-provenance's one row on
// spec/fixtures/dirty.
func TestSrcProvenanceDirty(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")
	report := lint.Run(ctx, []string{"src-provenance"})

	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Path != "wiki/concepts/no-provenance.md" {
		t.Errorf("Path = %q, want wiki/concepts/no-provenance.md", f.Path)
	}
	want := "cites raw/papers/valid-source.md but has no ^[raw/papers/valid-source.md] marker; add one or drop the source"
	if f.Message != want {
		t.Errorf("Message = %q, want %q", f.Message, want)
	}
}

// TestSrcProvenancePerSource proves a page citing two sources gets one
// finding per source lacking its own marker — a marker for one source
// does not excuse a missing marker for another.
func TestSrcProvenancePerSource(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"raw/papers/a.md": rawSourceFixture,
		"raw/papers/b.md": rawSourceFixture,
		"wiki/concepts/two-sources.md": `---
title: Two Sources
created: 2026-01-01
updated: 2026-01-02
type: concept
sources: [raw/papers/a.md, raw/papers/b.md]
---

# Two Sources

Only the first citation carries a marker.^[raw/papers/a.md]
`,
	})

	report := lint.Run(ctx, []string{"src-provenance"})
	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	want := "cites raw/papers/b.md but has no ^[raw/papers/b.md] marker; add one or drop the source"
	if f.Message != want {
		t.Errorf("Message = %q, want %q", f.Message, want)
	}
}

// TestSrcProvenanceNoSourcesIsClean proves a page with no sources: at all
// never fires, regardless of body content.
func TestSrcProvenanceNoSourcesIsClean(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/no-sources.md": `---
title: No Sources
created: 2026-01-01
updated: 2026-01-02
type: concept
---

# No Sources

Nothing to cite here.
`,
	})

	report := lint.Run(ctx, []string{"src-provenance"})
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
	}
}

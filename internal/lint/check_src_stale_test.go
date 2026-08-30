package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// TestSrcStaleDirty isolates src-stale's one row on spec/fixtures/dirty.
// drift-cite.md deliberately does not fire (its updated date is after its
// source's ingested date), which this also guards against regressing.
func TestSrcStaleDirty(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")
	report := lint.Run(ctx, []string{"src-stale"})

	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Path != "wiki/concepts/no-provenance.md" {
		t.Errorf("Path = %q, want wiki/concepts/no-provenance.md", f.Path)
	}
	want := "updated 2026-01-10 is more than 90 days before raw/papers/valid-source.md was ingested 2026-08-12; review the page against its source"
	if f.Message != want {
		t.Errorf("Message = %q, want %q", f.Message, want)
	}
}

func rawSourceIngested(date string) string {
	return `---
source_url: https://example.org/x
ingested: ` + date + `
sha256: 0000000000000000000000000000000000000000000000000000000000000000
---

# Source

Body text.
`
}

func pageUpdated(date string) string {
	return `---
title: Page
created: 2026-01-01
updated: ` + date + `
type: concept
sources: [raw/papers/src.md]
---

# Page

Cites the source.^[raw/papers/src.md]
`
}

// TestSrcStaleBoundary proves the 90-day threshold is strictly greater
// than, never met exactly: a gap of exactly 90 days must not fire, 91
// days must.
func TestSrcStaleBoundary(t *testing.T) {
	tests := []struct {
		name     string
		ingested string
		want     int
	}{
		{"exactly 90 days does not fire", "2026-04-01", 0}, // 2026-01-01 + 90d
		{"91 days fires", "2026-04-02", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := buildVault(t, map[string]string{
				"raw/papers/src.md":     rawSourceIngested(tc.ingested),
				"wiki/concepts/page.md": pageUpdated("2026-01-01"),
			})
			report := lint.Run(ctx, []string{"src-stale"})
			if len(report.Findings) != tc.want {
				t.Fatalf("got %d findings, want %d: %+v", len(report.Findings), tc.want, report.Findings)
			}
		})
	}
}

// TestSrcStaleUpdatedAfterIngestedIsClean proves a page revisited after
// its source was ingested never fires, mirroring drift-cite.md.
func TestSrcStaleUpdatedAfterIngestedIsClean(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"raw/papers/src.md":     rawSourceIngested("2026-01-01"),
		"wiki/concepts/page.md": pageUpdated("2026-06-01"),
	})
	report := lint.Run(ctx, []string{"src-stale"})
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
	}
}

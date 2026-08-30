package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// TestSrcIntegrityDirty isolates src-integrity's two rows on
// spec/fixtures/dirty: a missing raw/ entry attributed to the citing page,
// and a body/frontmatter sha256 drift attributed to the raw file itself.
func TestSrcIntegrityDirty(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")
	report := lint.Run(ctx, []string{"src-integrity"})

	want := []lint.Finding{
		{
			Check: "src-integrity", Path: "raw/papers/drifted-source.md", Severity: lint.SevError,
			Message: "body sha256 does not match frontmatter sha256; re-ingest to refresh the hash",
		},
		{
			Check: "src-integrity", Path: "wiki/concepts/missing-raw.md", Severity: lint.SevError,
			Message: "sources entry raw/papers/nonexistent-source.md not found under raw/; ingest it or drop the citation",
		},
	}
	if len(report.Findings) != len(want) {
		t.Fatalf("src-integrity produced %d findings, want %d: %+v", len(report.Findings), len(want), report.Findings)
	}
	for i := range want {
		got := report.Findings[i]
		if got.Path != want[i].Path || got.Message != want[i].Message || got.Severity != want[i].Severity {
			t.Errorf("row %d: got %+v, want %+v", i, got, want[i])
		}
	}
}

// rawSourceFixture is a minimal raw/ file whose frontmatter sha256 is
// deliberately wrong, for exercising the "reported once, not once per
// citer" attribution rule.
const rawSourceFixture = `---
source_url: https://example.org/drift
ingested: 2026-01-01
sha256: 0000000000000000000000000000000000000000000000000000000000000000
---

# Drift Fixture

This body does not hash to the sha256 above.
`

func citerPage(title string) string {
	return `---
title: ` + title + `
created: 2026-01-01
updated: 2026-01-02
type: concept
sources: [raw/papers/drift.md]
---

# ` + title + `

Cites the drifted source.^[raw/papers/drift.md]
`
}

// TestSrcIntegrityDriftReportedOnce proves a drifted raw source produces
// exactly one finding even when several pages cite it — the defect is a
// property of the raw file, not of any citer (backbone §4's attribution
// note).
func TestSrcIntegrityDriftReportedOnce(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"raw/papers/drift.md":        rawSourceFixture,
		"wiki/concepts/citer-one.md": citerPage("Citer One"),
		"wiki/concepts/citer-two.md": citerPage("Citer Two"),
	})

	report := lint.Run(ctx, []string{"src-integrity"})
	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Path != "raw/papers/drift.md" {
		t.Errorf("Path = %q, want raw/papers/drift.md", f.Path)
	}
	if f.Message != "body sha256 does not match frontmatter sha256; re-ingest to refresh the hash" {
		t.Errorf("Message = %q", f.Message)
	}
}

// TestSrcIntegrityMissingIsAttributedToCiter proves a sources: entry that
// names a raw/ path with no corresponding file is attributed to the
// citing page, not to the (nonexistent) source.
func TestSrcIntegrityMissingIsAttributedToCiter(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/citer.md": `---
title: Citer
created: 2026-01-01
updated: 2026-01-02
type: concept
sources: [raw/papers/never-ingested.md]
---

# Citer

Cites a source that was never ingested.
`,
	})

	report := lint.Run(ctx, []string{"src-integrity"})
	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Path != "wiki/concepts/citer.md" {
		t.Errorf("Path = %q, want wiki/concepts/citer.md", f.Path)
	}
	want := "sources entry raw/papers/never-ingested.md not found under raw/; ingest it or drop the citation"
	if f.Message != want {
		t.Errorf("Message = %q, want %q", f.Message, want)
	}
}

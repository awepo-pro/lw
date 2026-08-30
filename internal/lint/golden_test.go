package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// openFixtureContext copies spec/fixtures/<name> into a private t.TempDir()
// (00-conventions.md §6: spec/fixtures/** is ground truth, never written to
// by a test — CopyFixture is the sanctioned way to reach it) and builds the
// lint.Context that vault, index and graph feed it.
func openFixtureContext(t *testing.T, name string) *lint.Context {
	t.Helper()

	root := testutil.CopyFixture(t, name)
	v, err := vault.Open(root)
	if err != nil {
		t.Fatalf("vault.Open(%s): %v", name, err)
	}
	return &lint.Context{
		Vault: v,
		Index: index.Build(v),
		Graph: v.Graph(),
	}
}

// TestMinimalIsClean asserts lint.Run finds nothing at all over
// spec/fixtures/minimal, which was independently re-verified against all 14
// checks at S1 stage entry (s1-vault-engine.md's cross-check note). A
// finding here is this subtask's bug, not a fixture problem.
func TestMinimalIsClean(t *testing.T) {
	ctx := openFixtureContext(t, "minimal")
	report := lint.Run(ctx, nil)

	if len(report.Findings) != 0 {
		t.Fatalf("lint.Run(minimal) produced %d findings, want 0:\n%+v", len(report.Findings), report.Findings)
	}
	if !report.Clean() {
		t.Fatalf("report.Clean() = false over a zero-finding report")
	}
}

// TestDirtyGolden asserts lint.Run over spec/fixtures/dirty reproduces all
// 16 rows of EXPECTED-LINT.md exactly, across all 14 checks — same order
// (already Path/Line/Check sorted by Run), same Path, Line, Severity and
// Message. Comparison is done in memory against a hard-coded golden, never
// through testutil's golden-file helper against a path under
// spec/fixtures/** (MASTER §9 D-X): that helper writes rather than compares
// under -update and would silently corrupt EXPECTED-LINT.md's ground truth.
func TestDirtyGolden(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")
	report := lint.Run(ctx, nil)

	want := []lint.Finding{
		{
			Check: "index-sync", Path: "index.md", Line: 0, Severity: lint.SevError,
			Message: "wiki/concepts/thin-links.md has no line in index.md; add one",
		},
		{
			Check: "index-sync", Path: "index.md", Line: 14, Severity: lint.SevError,
			Message: "entry [[nonexistent-catalog-entry]] points to a page that does not exist; remove it or create the page",
		},
		{
			Check: "log-rotate", Path: "log.md", Line: 0, Severity: lint.SevInfo,
			Message: "505 entries exceeds the 500-entry rotation threshold; rotate to log-2026.md",
		},
		{
			Check: "src-integrity", Path: "raw/papers/drifted-source.md", Line: 0, Severity: lint.SevError,
			Message: "body sha256 does not match frontmatter sha256; re-ingest to refresh the hash",
		},
		{
			Check: "path-convention", Path: "wiki/concepts/KV_Cache.md", Line: 0, Severity: lint.SevWarn,
			Message: "filename is not lowercase-hyphen.md; rename to kv-cache.md",
		},
		{
			Check: "fm-dates", Path: "wiki/concepts/backwards-dates.md", Line: 0, Severity: lint.SevWarn,
			Message: "created 2026-09-05 is after updated 2026-08-01; fix one of the dates",
		},
		{
			Check: "link-broken", Path: "wiki/concepts/broken-target.md", Line: 7, Severity: lint.SevError,
			Message: "[[nonexistent-target]] resolves to nothing; fix the target or create the page",
		},
		{
			Check: "size-split", Path: "wiki/concepts/long-page.md", Line: 0, Severity: lint.SevInfo,
			Message: "body exceeds 200 lines; consider splitting into smaller pages",
		},
		{
			Check: "fm-required", Path: "wiki/concepts/malformed.md", Line: 0, Severity: lint.SevError,
			Message: "frontmatter block never closes; add the closing --- delimiter or fix the YAML",
		},
		{
			Check: "src-integrity", Path: "wiki/concepts/missing-raw.md", Line: 0, Severity: lint.SevError,
			Message: "sources entry raw/papers/nonexistent-source.md not found under raw/; ingest it or drop the citation",
		},
		{
			Check: "src-provenance", Path: "wiki/concepts/no-provenance.md", Line: 0, Severity: lint.SevWarn,
			Message: "cites raw/papers/valid-source.md but has no ^[raw/papers/valid-source.md] marker; add one or drop the source",
		},
		{
			Check: "src-stale", Path: "wiki/concepts/no-provenance.md", Line: 0, Severity: lint.SevWarn,
			Message: "updated 2026-01-10 is more than 90 days before raw/papers/valid-source.md was ingested 2026-08-12; review the page against its source",
		},
		{
			Check: "link-orphan", Path: "wiki/concepts/orphan-page.md", Line: 0, Severity: lint.SevWarn,
			Message: "no inbound links; link to it from a related page or retract it",
		},
		{
			Check: "fm-taxonomy", Path: "wiki/concepts/rogue-tag.md", Line: 0, Severity: lint.SevError,
			Message: "tag `nonexistent-tag` is not in SCHEMA.md; add it to the taxonomy or retag",
		},
		{
			Check: "fm-quality", Path: "wiki/concepts/thin-links.md", Line: 0, Severity: lint.SevInfo,
			Message: "confidence is low; corroborate with another source or raise the confidence",
		},
		{
			Check: "link-min-out", Path: "wiki/concepts/thin-links.md", Line: 0, Severity: lint.SevWarn,
			Message: "only 1 outbound wikilink; add at least one more",
		},
	}

	if len(report.Findings) != len(want) {
		t.Fatalf("lint.Run(dirty) produced %d findings, want %d\ngot:  %+v\nwant: %+v",
			len(report.Findings), len(want), report.Findings, want)
	}
	for i := range want {
		got := report.Findings[i]
		if got.Check != want[i].Check ||
			got.Path != want[i].Path ||
			got.Line != want[i].Line ||
			got.Severity != want[i].Severity ||
			got.Message != want[i].Message {
			t.Errorf("row %d:\n got  %+v\n want %+v", i, got, want[i])
		}
	}
}

// TestRunFilter exercises Run(ctx, only): a non-empty only must return
// exactly the findings for the named checks, with ByCheck and the
// Errors/Warns tally computed over that filtered set, not the full 14.
func TestRunFilter(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")

	report := lint.Run(ctx, []string{"path-convention"})

	if len(report.Findings) != 1 {
		t.Fatalf("Run(only=[path-convention]) = %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Check != "path-convention" || f.Path != "wiki/concepts/KV_Cache.md" {
		t.Fatalf("Run(only=[path-convention]) finding = %+v, want the KV_Cache.md row", f)
	}
	if len(report.ByCheck) != 1 || len(report.ByCheck["path-convention"]) != 1 {
		t.Fatalf("ByCheck = %+v, want exactly one entry for path-convention", report.ByCheck)
	}
	if report.Errors != 0 || report.Warns != 1 {
		t.Fatalf("Errors=%d Warns=%d, want Errors=0 Warns=1", report.Errors, report.Warns)
	}

	// An unrecognized-only ID space still runs cleanly, producing no findings
	// rather than falling back to All().
	empty := lint.Run(ctx, []string{"no-such-check"})
	if len(empty.Findings) != 0 {
		t.Fatalf("Run(only=[unknown]) = %d findings, want 0", len(empty.Findings))
	}
}

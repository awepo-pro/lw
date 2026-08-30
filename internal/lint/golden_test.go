package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// ownedChecks are the seven check IDs S1-T5a owns. Filtering a full Run's
// findings down to these isolates exactly this subtask's rows even while
// S1-T5b's seven checks are still the check_stubs_t5b.go no-ops.
var ownedChecks = map[string]bool{
	"fm-required":     true,
	"fm-taxonomy":     true,
	"fm-dates":        true,
	"path-convention": true,
	"link-broken":     true,
	"link-min-out":    true,
	"link-orphan":     true,
}

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

// TestDirtyGolden asserts the findings lint.Run produces for this
// subtask's seven checks, over spec/fixtures/dirty, equal exactly the seven
// rows of EXPECTED-LINT.md whose check ID is one of them — same order
// (already Path/Line/Check sorted by Run), same Path, Line, Severity and
// Message. Comparison is done in memory against a hard-coded golden, never
// through testutil's golden-file helper against a path under
// spec/fixtures/** (MASTER §9 D-X): that helper writes rather than compares
// under -update and would silently corrupt EXPECTED-LINT.md's ground truth.
func TestDirtyGolden(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")
	report := lint.Run(ctx, nil)

	var got []lint.Finding
	for _, f := range report.Findings {
		if ownedChecks[f.Check] {
			got = append(got, f)
		}
	}

	want := []lint.Finding{
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
			Check: "fm-required", Path: "wiki/concepts/malformed.md", Line: 0, Severity: lint.SevError,
			Message: "frontmatter block never closes; add the closing --- delimiter or fix the YAML",
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
			Check: "link-min-out", Path: "wiki/concepts/thin-links.md", Line: 0, Severity: lint.SevWarn,
			Message: "only 1 outbound wikilink; add at least one more",
		},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d findings for the seven owned checks, want %d\ngot:  %+v\nwant: %+v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Check != want[i].Check ||
			got[i].Path != want[i].Path ||
			got[i].Line != want[i].Line ||
			got[i].Severity != want[i].Severity ||
			got[i].Message != want[i].Message {
			t.Errorf("row %d:\n got  %+v\n want %+v", i, got[i], want[i])
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

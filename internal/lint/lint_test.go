package lint

import (
	"reflect"
	"testing"
)

// wantCheckOrder is backbone §4's table order for the checks (MASTER §9 D-V).
// 014 amendment (workflow §9 A2): page-abstract joins as check 15.
// 020 amendment (workflow T-C): duplicate-section joins as check 16.
var wantCheckOrder = []string{
	"fm-required",
	"fm-taxonomy",
	"fm-dates",
	"path-convention",
	"link-broken",
	"link-min-out",
	"link-orphan",
	"src-integrity",
	"src-provenance",
	"index-sync",
	"size-split",
	"fm-quality",
	"src-stale",
	"log-rotate",
	"page-abstract",
	"duplicate-section",
}

// TestAllReturnsSixteenInTableOrder — renamed by the 020 amendment
// (workflow T-C): duplicate-section joins as check 16.
func TestAllReturnsSixteenInTableOrder(t *testing.T) {
	checks := All()
	if len(checks) != 16 {
		t.Fatalf("len(All()) = %d, want 16", len(checks))
	}

	var got []string
	for _, c := range checks {
		got = append(got, c.ID())
	}
	if !reflect.DeepEqual(got, wantCheckOrder) {
		t.Fatalf("All() IDs = %v, want %v", got, wantCheckOrder)
	}
}

func TestAllIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range All() {
		if seen[c.ID()] {
			t.Fatalf("duplicate check ID %q in All()", c.ID())
		}
		seen[c.ID()] = true
	}
}

func TestSelectChecksEmptyMeansAll(t *testing.T) {
	// 014 amendment (workflow §9 A2): page-abstract joins as check 15.
	// 020 amendment (workflow T-C): duplicate-section joins as check 16.
	if got := len(selectChecks(nil)); got != 16 {
		t.Fatalf("selectChecks(nil) has %d checks, want 16", got)
	}
	if got := len(selectChecks([]string{})); got != 16 {
		t.Fatalf("selectChecks([]string{}) has %d checks, want 16", got)
	}
}

func TestSelectChecksFilters(t *testing.T) {
	got := selectChecks([]string{"link-broken", "fm-required"})
	if len(got) != 2 {
		t.Fatalf("selectChecks(2 ids) = %d checks, want 2", len(got))
	}
	// selectChecks preserves All()'s table order, not the order requested.
	if got[0].ID() != "fm-required" || got[1].ID() != "link-broken" {
		t.Fatalf("selectChecks order = [%s, %s], want [fm-required, link-broken]", got[0].ID(), got[1].ID())
	}
}

func TestSelectChecksUnknownIDYieldsNothing(t *testing.T) {
	got := selectChecks([]string{"no-such-check"})
	if len(got) != 0 {
		t.Fatalf("selectChecks(unknown) = %d checks, want 0", len(got))
	}
}

// TestSortFindingsCheckTieBreak exercises the ordering contract's
// load-bearing tie-break (backbone §4, spec/fixtures/dirty/EXPECTED-LINT.md):
// when two findings share a Path and Line, Check breaks the tie ascending.
// EXPECTED-LINT.md has exactly two such pairs; both are reproduced here so a
// regression in either direction is caught before S1-T5b's checks land.
func TestSortFindingsCheckTieBreak(t *testing.T) {
	findings := []Finding{
		{Check: "link-min-out", Path: "wiki/concepts/thin-links.md", Line: 0},
		{Check: "fm-quality", Path: "wiki/concepts/thin-links.md", Line: 0},
		{Check: "src-stale", Path: "wiki/concepts/no-provenance.md", Line: 0},
		{Check: "src-provenance", Path: "wiki/concepts/no-provenance.md", Line: 0},
		{Check: "link-broken", Path: "wiki/concepts/broken-target.md", Line: 7},
	}
	sortFindings(findings)

	var got []string
	for _, f := range findings {
		got = append(got, f.Path+"#"+f.Check)
	}
	want := []string{
		"wiki/concepts/broken-target.md#link-broken",
		"wiki/concepts/no-provenance.md#src-provenance",
		"wiki/concepts/no-provenance.md#src-stale",
		"wiki/concepts/thin-links.md#fm-quality",
		"wiki/concepts/thin-links.md#link-min-out",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortFindings order = %v, want %v", got, want)
	}
}

func TestReportClean(t *testing.T) {
	tests := []struct {
		name string
		r    Report
		want bool
	}{
		{"no findings", Report{}, true},
		{"warns only", Report{Warns: 3}, true},
		{"one error", Report{Errors: 1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Clean(); got != tc.want {
				t.Errorf("Clean() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReportRegresses(t *testing.T) {
	tests := []struct {
		name string
		r    Report
		prev Report
		want bool
	}{
		{"same errors", Report{Errors: 2}, Report{Errors: 2}, false},
		{"fewer errors", Report{Errors: 1}, Report{Errors: 2}, false},
		{"more errors", Report{Errors: 3}, Report{Errors: 2}, true},
		{"warns do not count", Report{Warns: 10}, Report{Warns: 0}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Regresses(tc.prev); got != tc.want {
				t.Errorf("Regresses() = %v, want %v", got, tc.want)
			}
		})
	}
}

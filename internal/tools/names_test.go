package tools

import (
	"regexp"
	"testing"
)

// wireNameRE mirrors the pattern every OpenAI-compatible endpoint enforces
// on tools[*].function.name (backbone §6/§7's amendment, D-CY/C-112):
// '^[a-zA-Z0-9_-]+$'. A dotted canonical name like "stage.create_page"
// never satisfies it — that is the whole reason WireName exists.
var wireNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// TestWireNameRoundTripsThroughList proves the mapping is total and
// bijective over the real 18 tool names — driven off reg.List(), not a
// hand-built copy of the table, so a renamed or added tool exercises this
// too. It is also names.go's own regression test for the switch-not-
// ReplaceAll requirement: "stage.create_page" carries an underscore inside
// its own leaf component, so a bug here would surface as
// CanonicalName(WireName("stage.create_page")) == "stage.create.page".
func TestWireNameRoundTripsThroughList(t *testing.T) {
	reg := minimalRegistry(t)
	names := reg.List()
	if len(names) != 18 {
		t.Fatalf("registry has %d tools, want 18", len(names))
	}
	for _, tool := range names {
		wire := WireName(tool.Name)
		if !wireNameRE.MatchString(wire) {
			t.Errorf("WireName(%q) = %q, does not match %s", tool.Name, wire, wireNameRE)
		}
		if got := CanonicalName(wire); got != tool.Name {
			t.Errorf("CanonicalName(WireName(%q)) = %q, want %q", tool.Name, got, tool.Name)
		}
	}
}

// TestNameDefaultBranchesAreConsistent exercises both switches' default
// branch — a name outside the fixed 18 — pinned as its own case because
// neither switch's default fires anywhere in
// TestWireNameRoundTripsThroughList.
func TestNameDefaultBranchesAreConsistent(t *testing.T) {
	if got, want := WireName("does.not.exist"), "does_not_exist"; got != want {
		t.Errorf("WireName(%q) = %q, want %q", "does.not.exist", got, want)
	}
	if got, want := CanonicalName("does_not_exist"), "does.not.exist"; got != want {
		t.Errorf("CanonicalName(%q) = %q, want %q", "does_not_exist", got, want)
	}
	if got, want := WireName(""), ""; got != want {
		t.Errorf("WireName(\"\") = %q, want %q", got, want)
	}
	if got, want := CanonicalName(""), ""; got != want {
		t.Errorf("CanonicalName(\"\") = %q, want %q", got, want)
	}
}

// TestWireNamesWeb pins 010's wire mapping for the web.search verb (010
// contract §3) in BOTH literal switches, D-CY style. The verb's leaf has
// no underscore of its own, so the default ReplaceAll branches would
// happen to round-trip — the explicit cases are still the contract: the
// mapping table, not an accident of punctuation, is what the wire sees.
func TestWireNamesWeb(t *testing.T) {
	if got, want := WireName("web.search"), "web_search"; got != want {
		t.Errorf("WireName(%q) = %q, want %q", "web.search", got, want)
	}
	if got, want := CanonicalName("web_search"), "web.search"; got != want {
		t.Errorf("CanonicalName(%q) = %q, want %q", "web_search", got, want)
	}
}

// TestCanonicalNameDoesNotOverCollapseUnderscores pins the exact defect
// D-CY warns against: a naive strings.ReplaceAll(wire, "_", ".") turns
// "stage_create_page" into "stage.create.page" instead of the real
// canonical "stage.create_page", since that tool's own leaf name contains
// an underscore. Every stage.*_* tool with an underscore in its leaf is
// covered so a future regression cannot pick a name that happens to dodge
// this.
func TestCanonicalNameDoesNotOverCollapseUnderscores(t *testing.T) {
	tests := []struct {
		wire      string
		canonical string
	}{
		{"stage_create_page", "stage.create_page"},
		{"stage_patch_page", "stage.patch_page"},
		{"stage_rename_page", "stage.rename_page"},
		{"stage_merge_pages", "stage.merge_pages"},
		{"stage_split_page", "stage.split_page"},
		{"stage_add_link", "stage.add_link"},
		{"stage_ingest_source", "stage.ingest_source"},
	}
	for _, tt := range tests {
		if got := CanonicalName(tt.wire); got != tt.canonical {
			t.Errorf("CanonicalName(%q) = %q, want %q", tt.wire, got, tt.canonical)
		}
	}
}

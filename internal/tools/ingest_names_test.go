package tools

// ingest_names_test.go is 063 S1's pin of the ingest offer set: the tools
// `lw tool` serves an external agent that curates an ingest. It is the ingest
// turn's tool set (every tool registered without a web.search provider, 056)
// minus stage.open, because `lw ingest --prepare` has already opened the
// changeset. Permanent regression test (D-10C).

import (
	"reflect"
	"sort"
	"testing"
)

// wantIngestToolNames is the exact offer set, sorted: the 18 tools NewRegistry
// registers over a Deps with no Search, less stage.open. A tool added to the
// registry changes this list on purpose — the test fails until a person decides
// whether an external agent should be offered it.
var wantIngestToolNames = []string{
	"raw.get",
	"raw.list",
	"stage.add_link",
	"stage.close",
	"stage.create_page",
	"stage.ingest_source",
	"stage.merge_pages",
	"stage.patch_page",
	"stage.rename_page",
	"stage.retract",
	"stage.split_page",
	"vault.orient",
	"wiki.backlinks",
	"wiki.get",
	"wiki.lint",
	"wiki.neighbors",
	"wiki.search",
}

func TestIngestToolNames(t *testing.T) {
	got := IngestToolNames()
	if !reflect.DeepEqual(got, wantIngestToolNames) {
		t.Fatalf("IngestToolNames() =\n%v\nwant\n%v", got, wantIngestToolNames)
	}
	if len(got) != 17 {
		t.Errorf("IngestToolNames() has %d tools, want 17 (18 registered without web.search, less stage.open)", len(got))
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("IngestToolNames() is not sorted: %v", got)
	}
	in := map[string]bool{}
	for _, n := range got {
		in[n] = true
	}
	for _, n := range []string{"stage.ingest_source", "raw.get", "stage.close"} {
		if !in[n] {
			t.Errorf("IngestToolNames() lacks %q", n)
		}
	}
	for _, n := range []string{"web.search", "stage.open"} {
		if in[n] {
			t.Errorf("IngestToolNames() offers %q", n)
		}
	}

	// Derived from the registry, not copied: every name is a tool NewRegistry
	// registers, and the only registered tool left out is stage.open.
	registered := map[string]bool{}
	for _, tool := range NewRegistry(Deps{}).List() {
		registered[tool.Name] = true
	}
	for _, n := range got {
		if !registered[n] {
			t.Errorf("IngestToolNames() names %q, which NewRegistry does not register", n)
		}
	}
	if len(registered)-len(got) != 1 || !registered["stage.open"] {
		t.Errorf("registry has %d tools and the offer set %d; want exactly stage.open left out", len(registered), len(got))
	}

	// Each call hands out a slice of its own: a caller sorting or editing it
	// must not change what the next caller sees.
	got[0] = "tampered"
	if again := IngestToolNames(); !reflect.DeepEqual(again, wantIngestToolNames) {
		t.Errorf("IngestToolNames() changed after a caller edited a previous result: %v", again)
	}
}

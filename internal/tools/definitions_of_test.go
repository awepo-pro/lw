package tools

// definitions_of_test.go is 039's pin on Registry.DefinitionsOf: the filtered
// twin of Definitions that lets a read-only turn advertise only the tools it
// may use. Permanent regression tests (D-10C).

import (
	"reflect"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
)

// defNames is the wire names of defs, in order.
func defNames(defs []llm.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

func TestDefinitionsOf(t *testing.T) {
	t.Run("sorted_wire_names_whatever_order_asked", func(t *testing.T) {
		reg := minimalRegistry(t)
		asked := []string{"wiki.search", "raw.get", "vault.orient", "wiki.get", "raw.list"}
		before := append([]string(nil), asked...)

		got := reg.DefinitionsOf(asked)

		want := []string{"raw_get", "raw_list", "vault_orient", "wiki_get", "wiki_search"}
		if !reflect.DeepEqual(defNames(got), want) {
			t.Fatalf("DefinitionsOf names = %v, want %v (Definitions' sort order, wire spelling)", defNames(got), want)
		}
		if !reflect.DeepEqual(asked, before) {
			t.Fatalf("DefinitionsOf reordered its argument: %v, was %v", asked, before)
		}
	})

	t.Run("same_definition_as_Definitions", func(t *testing.T) {
		reg := minimalRegistry(t)
		byName := map[string]llm.ToolDef{}
		for _, d := range reg.Definitions() {
			byName[d.Name] = d
		}
		for _, d := range reg.DefinitionsOf([]string{"wiki.get", "stage.open", "raw.get"}) {
			full, ok := byName[d.Name]
			if !ok {
				t.Fatalf("DefinitionsOf offers %q, which Definitions does not", d.Name)
			}
			if !reflect.DeepEqual(d, full) {
				t.Errorf("DefinitionsOf %q = %+v, want Definitions' %+v", d.Name, d, full)
			}
		}
	})

	t.Run("duplicates_collapse", func(t *testing.T) {
		reg := minimalRegistry(t)
		got := reg.DefinitionsOf([]string{"wiki.get", "wiki.get", "raw.get", "wiki.get"})
		if want := []string{"raw_get", "wiki_get"}; !reflect.DeepEqual(defNames(got), want) {
			t.Fatalf("names = %v, want %v", defNames(got), want)
		}
	})

	t.Run("unregistered_names_are_skipped", func(t *testing.T) {
		reg := minimalRegistry(t) // no search provider: web.search is not registered
		got := reg.DefinitionsOf([]string{"web.search", "no.such_tool", "wiki.get"})
		if want := []string{"wiki_get"}; !reflect.DeepEqual(defNames(got), want) {
			t.Fatalf("names = %v, want %v — an unbacked verb is not offered, not offered-and-failing", defNames(got), want)
		}
	})

	t.Run("web_search_offered_when_registered", func(t *testing.T) {
		reg := webSearchRegistry(t, &fakeSearchProvider{})
		got := reg.DefinitionsOf([]string{"wiki.get", "web.search"})
		if want := []string{"web_search", "wiki_get"}; !reflect.DeepEqual(defNames(got), want) {
			t.Fatalf("names = %v, want %v", defNames(got), want)
		}
	})

	t.Run("nothing_asked_nothing_offered", func(t *testing.T) {
		reg := minimalRegistry(t)
		if got := reg.DefinitionsOf(nil); len(got) != 0 {
			t.Fatalf("DefinitionsOf(nil) = %v, want none — nil means no tools, never all of them", defNames(got))
		}
		if got := reg.DefinitionsOf([]string{}); len(got) != 0 {
			t.Fatalf("DefinitionsOf([]) = %v, want none", defNames(got))
		}
	})

	t.Run("every_name_is_Definitions", func(t *testing.T) {
		reg := minimalRegistry(t)
		var names []string
		for _, tool := range reg.List() {
			names = append(names, tool.Name)
		}
		if got, want := reg.DefinitionsOf(names), reg.Definitions(); !reflect.DeepEqual(got, want) {
			t.Fatalf("DefinitionsOf(every name) differs from Definitions():\n got  %v\n want %v", defNames(got), defNames(want))
		}
	})
}

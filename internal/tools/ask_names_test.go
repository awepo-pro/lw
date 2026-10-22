package tools

import (
	"reflect"
	"sort"
	"testing"
)

// wantAskToolNames is the frozen D1 list: exactly 8 read-only tools, sorted.
var wantAskToolNames = []string{
	"raw.get",
	"raw.list",
	"vault.orient",
	"wiki.backlinks",
	"wiki.get",
	"wiki.lint",
	"wiki.neighbors",
	"wiki.search",
}

func TestAskToolNames(t *testing.T) {
	got := AskToolNames()
	if !reflect.DeepEqual(got, wantAskToolNames) {
		t.Fatalf("AskToolNames() =\n%v\nwant\n%v", got, wantAskToolNames)
	}
	if len(got) != 8 {
		t.Errorf("AskToolNames() has %d tools, want 8", len(got))
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("AskToolNames() is not sorted: %v", got)
	}

	// Each name is registered in NewRegistry(Deps{}) and has ReadOnly == true.
	reg := NewRegistry(Deps{})
	for _, n := range got {
		tool, ok := reg.Get(n)
		if !ok {
			t.Errorf("tool %q in AskToolNames() is not registered in NewRegistry(Deps{})", n)
			continue
		}
		if !tool.ReadOnly {
			t.Errorf("tool %q in AskToolNames() has ReadOnly == false, want true", n)
		}
	}

	// Two calls return independent slices.
	got[0] = "tampered"
	again := AskToolNames()
	if !reflect.DeepEqual(again, wantAskToolNames) {
		t.Errorf("AskToolNames() was mutated by editing a previous return value: %v", again)
	}
}

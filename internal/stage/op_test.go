package stage

import (
	"reflect"
	"testing"
)

func TestFindOpPtrRecursesIntoCascade(t *testing.T) {
	ops := []Op{
		{ID: "op1", Kind: OpCreatePage},
		{
			ID:   "op2",
			Kind: OpRenamePage,
			Cascade: []Op{
				{ID: "op3", Kind: OpPatchPage, Path: "wiki/concepts/a.md"},
			},
		},
	}
	p := findOpPtr(ops, "op3")
	if p == nil {
		t.Fatal("findOpPtr did not find the cascade sub-op op3")
	}
	if p.Path != "wiki/concepts/a.md" {
		t.Fatalf("found op has Path %q, want wiki/concepts/a.md", p.Path)
	}

	// Must return a pointer into the real slice, not a copy.
	p.Path = "mutated.md"
	if ops[1].Cascade[0].Path != "mutated.md" {
		t.Fatal("findOpPtr did not return a pointer into the real backing slice")
	}

	if findOpPtr(ops, "op-does-not-exist") != nil {
		t.Fatal("findOpPtr found an id that is not present")
	}
}

func TestMaxOpNCountsCascade(t *testing.T) {
	ops := []Op{
		{ID: "op1"},
		{ID: "op5", Cascade: []Op{{ID: "op6"}, {ID: "op2"}}},
	}
	if got := maxOpN(ops); got != 6 {
		t.Fatalf("maxOpN = %d, want 6", got)
	}
	if got := maxOpN(nil); got != 0 {
		t.Fatalf("maxOpN(nil) = %d, want 0", got)
	}
}

func TestOpTouchesCascade(t *testing.T) {
	op := Op{
		Kind:    OpMergePages,
		Sources: []string{"wiki/concepts/a.md", "wiki/concepts/b.md"},
		To:      "wiki/concepts/c.md",
		Cascade: []Op{
			{Path: "index.md"},
			{Path: "wiki/concepts/d.md"},
		},
	}
	// opTouches carries no ordering contract of its own (Changeset.Touches
	// is what sorts); it must simply include every path op and its
	// cascade touch, in the field order To, Sources, then Cascade.
	got := opTouches(op)
	want := []string{"wiki/concepts/c.md", "wiki/concepts/a.md", "wiki/concepts/b.md", "index.md", "wiki/concepts/d.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("opTouches = %v, want %v", got, want)
	}
}

func TestChangesetLiveExcludesDroppedAndRejected(t *testing.T) {
	c := &Changeset{Ops: []Op{
		{ID: "op1", State: StateProposed},
		{ID: "op2", State: StateDropped},
		{ID: "op3", State: StateRejected},
		{ID: "op4", State: StateStale},
	}}
	live := c.Live()
	var ids []string
	for _, op := range live {
		ids = append(ids, op.ID)
	}
	want := []string{"op1", "op4"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("Live() ids = %v, want %v", ids, want)
	}
}

func TestChangesetTouchesSortedDeduped(t *testing.T) {
	c := &Changeset{Ops: []Op{
		{ID: "op1", State: StateProposed, Kind: OpCreatePage, Path: "wiki/concepts/b.md"},
		{ID: "op2", State: StateProposed, Kind: OpPatchPage, Path: "wiki/concepts/a.md"},
		{ID: "op3", State: StateDropped, Kind: OpCreatePage, Path: "wiki/concepts/z.md"}, // excluded
		{ID: "op4", State: StateProposed, Kind: OpPatchPage, Path: "wiki/concepts/a.md"}, // dup
	}}
	got := c.Touches()
	want := []string{"wiki/concepts/a.md", "wiki/concepts/b.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Touches() = %v, want %v", got, want)
	}
}

func TestReverseAddressingFourForms(t *testing.T) {
	tests := []struct {
		name             string
		target, from, to string
		want             string
	}{
		{"exact path", "wiki/entities/gpt-4.md", "wiki/entities/gpt-4.md", "wiki/entities/gpt-4x.md", "wiki/entities/gpt-4x.md"},
		{"target+.md == from", "wiki/entities/gpt-4", "wiki/entities/gpt-4.md", "wiki/entities/gpt-4x.md", "wiki/entities/gpt-4x"},
		{"bare basename exact", "gpt-4.md", "wiki/entities/gpt-4.md", "wiki/entities/gpt-4x.md", "gpt-4x.md"},
		{"bare basename case-insensitive no .md", "GPT-4", "wiki/entities/gpt-4.md", "wiki/entities/gpt-4x.md", "gpt-4x"},
		{"dir/t form", "entities/gpt-4", "wiki/entities/gpt-4.md", "wiki/entities/gpt-4x.md", "entities/gpt-4x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reverseAddressing(tt.target, tt.from, tt.to); got != tt.want {
				t.Fatalf("reverseAddressing(%q, %q, %q) = %q, want %q", tt.target, tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestApplyHunksSubstitutesAndSkipsDropped(t *testing.T) {
	before := []byte("line one\nline two\nline three\n")
	hunks := []Hunk{
		{ID: "h1", Del: []string{"line one"}, Add: []string{"line ONE"}},
		{ID: "h2", Del: []string{"line two"}, Add: []string{"line TWO"}, Dropped: true},
	}
	got := string(applyHunks(before, hunks))
	want := "line ONE\nline two\nline three\n"
	if got != want {
		t.Fatalf("applyHunks = %q, want %q", got, want)
	}
}

func TestApplyHunksAllDroppedIsIdentity(t *testing.T) {
	before := []byte("a\nb\nc\n")
	hunks := []Hunk{{ID: "h1", Del: []string{"a"}, Add: []string{"A"}, Dropped: true}}
	got := string(applyHunks(before, hunks))
	if got != string(before) {
		t.Fatalf("applyHunks with every hunk dropped = %q, want the original content %q", got, before)
	}
}

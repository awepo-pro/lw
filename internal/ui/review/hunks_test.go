package review

import (
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
)

func TestBuildCursorStopsFlattensInFileThenHunkOrder(t *testing.T) {
	d := stage.Diff{
		Files: []stage.FileDiff{
			{OpID: "op1", Hunks: []stage.Hunk{{ID: "h1"}, {ID: "h2"}}},
			{OpID: "op2", Hunks: nil}, // zero hunks: contributes no stop
			{OpID: "op3", Hunks: []stage.Hunk{{ID: "h1"}}},
		},
	}
	stops := buildCursorStops(d)
	want := []cursorStop{{fileIdx: 0, hunkIdx: 0}, {fileIdx: 0, hunkIdx: 1}, {fileIdx: 2, hunkIdx: 0}}
	if len(stops) != len(want) {
		t.Fatalf("len(stops) = %d, want %d (%v)", len(stops), len(want), stops)
	}
	for i, s := range stops {
		if s != want[i] {
			t.Errorf("stops[%d] = %+v, want %+v", i, s, want[i])
		}
	}
}

func TestClampCursor(t *testing.T) {
	cases := []struct {
		name string
		i, n int
		want int
	}{
		{"empty collapses to zero", 5, 0, 0},
		{"negative floors to zero", -1, 3, 0},
		{"in range is unchanged", 1, 3, 1},
		{"over range ceilings to last", 9, 3, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clampCursor(c.i, c.n); got != c.want {
				t.Errorf("clampCursor(%d, %d) = %d, want %d", c.i, c.n, got, c.want)
			}
		})
	}
}

func TestResolveCursor(t *testing.T) {
	d := stage.Diff{
		Files: []stage.FileDiff{
			{OpID: "op1", Hunks: []stage.Hunk{{ID: "h1"}, {ID: "h2"}}},
		},
	}
	stops := buildCursorStops(d)

	opID, hunkID, ok := resolveCursor(d, stops, 1)
	if !ok || opID != "op1" || hunkID != "h2" {
		t.Errorf("resolveCursor(1) = (%q, %q, %v), want (op1, h2, true)", opID, hunkID, ok)
	}

	if _, _, ok := resolveCursor(d, stops, -1); ok {
		t.Error("resolveCursor(-1) = ok, want false")
	}
	if _, _, ok := resolveCursor(d, stops, len(stops)); ok {
		t.Error("resolveCursor(len(stops)) = ok, want false")
	}
	if _, _, ok := resolveCursor(stage.Diff{}, nil, 0); ok {
		t.Error("resolveCursor over an empty diff = ok, want false")
	}
}

func TestFlattenLiveOpsIncludesCascadeAndExcludesDropped(t *testing.T) {
	cs := &stage.Changeset{
		Ops: []stage.Op{
			{ID: "op1", State: stage.StateProposed, Cascade: []stage.Op{
				{ID: "op2", State: stage.StateProposed},
				{ID: "op3", State: stage.StateDropped},
			}},
			{ID: "op4", State: stage.StateDropped},
			{ID: "op5", State: stage.StateRejected},
			{ID: "op6", State: stage.StateStale},
		},
	}
	got := flattenLiveOps(cs)
	var ids []string
	for _, op := range got {
		ids = append(ids, op.ID)
	}
	want := []string{"op1", "op2", "op6"}
	if len(ids) != len(want) {
		t.Fatalf("flattenLiveOps ids = %v, want %v", ids, want)
	}
	for i, id := range ids {
		if id != want[i] {
			t.Errorf("ids[%d] = %q, want %q (full: %v)", i, id, want[i], ids)
		}
	}
}

func TestOpDisplayPath(t *testing.T) {
	cases := []struct {
		name string
		op   stage.Op
		want string
	}{
		{"create_page", stage.Op{Kind: stage.OpCreatePage, Path: "wiki/x.md"}, "wiki/x.md"},
		{"rename_page", stage.Op{Kind: stage.OpRenamePage, From: "a.md", To: "b.md"}, "a.md -> b.md"},
		{"merge_pages", stage.Op{Kind: stage.OpMergePages, Sources: []string{"a.md", "b.md"}, To: "c.md"}, "a.md, b.md -> c.md"},
		{"split_page", stage.Op{Kind: stage.OpSplitPage, Path: "a.md", Sources: []string{"b.md", "c.md"}}, "a.md -> b.md, c.md"},
		{"add_link", stage.Op{Kind: stage.OpAddLink, From: "a.md", To: "b.md"}, "a.md -> b.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := opDisplayPath(c.op); got != c.want {
				t.Errorf("opDisplayPath(%+v) = %q, want %q", c.op, got, c.want)
			}
		})
	}
}

package review

import (
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
)

// TestBuildCursorStopsFollowsOpsOrder replaces the pre-T20 ordering test
// (which pinned the old Diff-only walk, where an op with zero hunks
// contributed no stop and the walk followed d.Files across ops): since
// C32/D-3U the stops follow the Ops panel's op order, each op's hunks in
// Diff order within that op, and an op with no hunks — no Diff entry at
// all, or an entry without hunks — contributes exactly one op-level stop.
func TestBuildCursorStopsFollowsOpsOrder(t *testing.T) {
	d := stage.Diff{
		Files: []stage.FileDiff{
			// Diff's risk order need not match the ops order: here op3's
			// file comes first.
			{OpID: "op3", Hunks: []stage.Hunk{{ID: "h1"}}},
			{OpID: "op1", Hunks: []stage.Hunk{{ID: "h1"}, {ID: "h2"}}},
			{OpID: "op2", Hunks: nil}, // a Diff entry, but no hunks
		},
	}
	ops := []stage.Op{
		{ID: "op1"},
		{ID: "op2"},
		{ID: "op3"},
		{ID: "op4"}, // fully dropped: no Diff entry at all
	}
	stops := buildCursorStops(d, ops)
	want := []cursorStop{
		{fileIdx: 1, hunkIdx: 0},
		{fileIdx: 1, hunkIdx: 1},
		{opID: "op2"},
		{fileIdx: 0, hunkIdx: 0},
		{opID: "op4"},
	}
	if len(stops) != len(want) {
		t.Fatalf("len(stops) = %d, want %d (%v)", len(stops), len(want), stops)
	}
	for i, s := range stops {
		if s != want[i] {
			t.Errorf("stops[%d] = %+v, want %+v", i, s, want[i])
		}
	}

	// No ops, no stops: the empty-changeset state resolves to nothing.
	if got := buildCursorStops(d, nil); len(got) != 0 {
		t.Errorf("buildCursorStops with no ops = %v, want none", got)
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
	stops := buildCursorStops(d, []stage.Op{{ID: "op1"}})

	opID, hunkID, ok := resolveCursor(d, stops, 1)
	if !ok || opID != "op1" || hunkID != "h2" {
		t.Errorf("resolveCursor(1) = (%q, %q, %v), want (op1, h2, true)", opID, hunkID, ok)
	}

	// An op-level stop resolves to its op with no hunk: never a y/n target.
	opID, hunkID, ok = resolveCursor(d, []cursorStop{{opID: "op9"}}, 0)
	if !ok || opID != "op9" || hunkID != "" {
		t.Errorf("resolveCursor(op-level) = (%q, %q, %v), want (op9, \"\", true)", opID, hunkID, ok)
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

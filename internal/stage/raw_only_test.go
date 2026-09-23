// raw_only_test.go is 029 T1's F1 evidence: one predicate, one definition.
// A-029-1 amends 008 §5/§6's raw-only rule from "zero live create_page ops"
// to "no live op of any other kind" — every non-ingest kind changes the
// wiki, so ingest+patch is page work, not a raw-only commit. Built in
// memory over Changeset values; no engine, no disk.
package stage

import (
	"reflect"
	"testing"
)

func TestChangesetRawOnly(t *testing.T) {
	const (
		rawA  = "raw/articles/a.md"
		rawB  = "raw/articles/b.md"
		pageP = "wiki/concepts/p.md"
	)

	live := func(kind OpKind, path string) Op {
		return Op{ID: "op", Kind: kind, Path: path, State: StateProposed}
	}
	droppedPatch := func() Op {
		return Op{ID: "op", Kind: OpPatchPage, Path: pageP, State: StateDropped}
	}

	tests := []struct {
		name     string
		ops      []Op
		wantRaws []string
		wantOK   bool
	}{
		{name: "ingest_only",
			ops:      []Op{live(OpIngestSource, rawA), live(OpIngestSource, rawB)},
			wantRaws: []string{rawA, rawB}, wantOK: true},

		// U5 — the live bug's shape: one ingest_source plus chained
		// patch_page ops read as "zero pages" under the old create-only
		// rule. Under A-029-1 they are page changes, not raw-only.
		{name: "ingest_plus_patch",
			ops:      []Op{live(OpIngestSource, rawA), live(OpPatchPage, pageP), live(OpPatchPage, pageP)},
			wantRaws: []string{rawA}, wantOK: false},

		{name: "ingest_plus_create",
			ops:      []Op{live(OpIngestSource, rawA), live(OpCreatePage, pageP)},
			wantRaws: []string{rawA}, wantOK: false},

		// Dropped ops do not count: Live() already excludes them, so an
		// ingest whose only patch was dropped stays raw-only.
		{name: "patch_dropped",
			ops:      []Op{live(OpIngestSource, rawA), droppedPatch()},
			wantRaws: []string{rawA}, wantOK: true},

		{name: "no_ingest",
			ops:      []Op{live(OpPatchPage, pageP)},
			wantRaws: nil, wantOK: false},

		{name: "empty",
			ops:      nil,
			wantRaws: nil, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Changeset{Ops: tt.ops}
			raws, ok := c.RawOnly()
			if ok != tt.wantOK {
				t.Errorf("RawOnly ok = %v, want %v (raws %q)", ok, tt.wantOK, raws)
			}
			if tt.wantRaws == nil {
				if raws != nil {
					t.Errorf("RawOnly raws = %q, want nil", raws)
				}
				return
			}
			if !reflect.DeepEqual(raws, tt.wantRaws) {
				t.Errorf("RawOnly raws = %q, want %q", raws, tt.wantRaws)
			}
		})
	}
}

// TestChangesetRawOnlyEachOtherKind pins A-029-1's "no live op of any other
// kind" per kind: a subtest per remaining op kind, each paired with one
// ingest_source and each NOT raw-only.
func TestChangesetRawOnlyEachOtherKind(t *testing.T) {
	for _, kind := range []OpKind{OpRenamePage, OpMergePages, OpSplitPage, OpAddLink, OpRetract} {
		t.Run(string(kind), func(t *testing.T) {
			c := &Changeset{Ops: []Op{
				{ID: "op1", Kind: OpIngestSource, Path: "raw/articles/a.md", State: StateProposed},
				{ID: "op2", Kind: kind, Path: "wiki/concepts/p.md", State: StateProposed},
			}}
			raws, ok := c.RawOnly()
			if ok {
				t.Errorf("ingest + %s: RawOnly ok = true, want false", kind)
			}
			if !reflect.DeepEqual(raws, []string{"raw/articles/a.md"}) {
				t.Errorf("ingest + %s: RawOnly raws = %q, want [raw/articles/a.md]", kind, raws)
			}
		})
	}
}

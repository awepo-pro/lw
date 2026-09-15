// hunks.go implements the review screen's cursor model: a single cursor
// walking every Ops-panel row in the panel's own order — each op's hunks
// as (FileDiff, Hunk) index pairs in the engine's own risk order (backbone
// §5.6, which this package must never re-sort — s4-tui.md S4-T3 §5 item
// 5), and each op without hunks as one op-level stop (C32/D-3U). Kept as
// small, pure functions so the cursor arithmetic is unit-testable
// without constructing a Model, an Engine, or a tea.Program at all.
package review

import (
	"strings"

	"github.com/awepo-pro/lw/internal/stage"
)

// cursorStop is one addressable position in the review cursor's walk
// order. A hunk stop (opID "") addresses the Hunk hunkIdx of the FileDiff
// fileIdx within a Diff's Files. An op-level stop (opID set) addresses an
// op that contributes no hunks at all — a create_page, an ingest_source,
// a fully dropped op, or any op whose Diff entry has no hunks — so its
// row stays reachable with j/k and the Ops panel always has a cursor row
// (C32/D-3U); y/n refuse it like an ownerless window.
type cursorStop struct {
	fileIdx int
	hunkIdx int
	opID    string // non-empty: an op-level stop; fileIdx/hunkIdx unused
}

// buildCursorStops walks ops — the Ops panel's render order, dropped ops
// included — and for each op emits its hunk stops: one per hunk of that
// op's files in d, in d's own order within the op. d.Files still arrives
// risk-sorted by the engine (backbone §5.6: "sorted by risk:
// create/rename/merge/split, then patch, then add_link") — this function
// must never re-sort it, only filter it per op. An op with no hunks in d
// — create_page, ingest_source, the derived index.md, a fully dropped op
// — contributes exactly ONE op-level stop instead, so every Ops-panel row
// is a stop and the cursor's walk follows the panel's (C32/D-3U).
func buildCursorStops(d stage.Diff, ops []stage.Op) []cursorStop {
	var stops []cursorStop
	for _, op := range ops {
		n := 0
		for fi, f := range d.Files {
			if f.OpID != op.ID {
				continue
			}
			for hi := range f.Hunks {
				stops = append(stops, cursorStop{fileIdx: fi, hunkIdx: hi})
				n++
			}
		}
		if n == 0 {
			stops = append(stops, cursorStop{opID: op.ID})
		}
	}
	return stops
}

// clampCursor keeps i within [0, n), landing on 0 when n is 0 — the
// "nothing to select" state a fresh load, an empty diff, or an
// all-zero-hunk changeset must all tolerate without ever indexing
// stops[i] out of range.
func clampCursor(i, n int) int {
	if n <= 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// resolveCursor returns the (opID, hunkID) that stops[i] addresses within
// d, and false when i is out of range for stops or a hunk stop's indices
// are out of range for d — an empty diff, a changeset with no ops, and
// stale indices after a reload all resolve to ok == false rather than a
// panic. An op-level stop resolves to its op with no hunk (hunkID ""): the
// ops list and the stops are rebuilt together on every load, so the op it
// names is the op the panels drew, and a consumer that cannot find it
// handles the miss as it already handles any unknown id.
func resolveCursor(d stage.Diff, stops []cursorStop, i int) (opID, hunkID string, ok bool) {
	if i < 0 || i >= len(stops) {
		return "", "", false
	}
	st := stops[i]
	if st.opID != "" {
		return st.opID, "", true
	}
	if st.fileIdx < 0 || st.fileIdx >= len(d.Files) {
		return "", "", false
	}
	f := d.Files[st.fileIdx]
	if st.hunkIdx < 0 || st.hunkIdx >= len(f.Hunks) {
		return "", "", false
	}
	return f.OpID, f.Hunks[st.hunkIdx].ID, true
}

// hasWindow reports whether files holds at least one DisplayHunk whose
// HunkID is id. An id of "" is never found: ownerless windows (contract
// §1 note 4 — a create, an ingest, a derived index.md) are display-only
// and never a y/n target (s2-screens.md T06), so the empty id falls
// through to false even when ownerless windows exist.
func hasWindow(files []stage.FileOpDiff, id string) bool {
	if id == "" {
		return false
	}
	for _, f := range files {
		for _, w := range f.Hunks {
			if w.HunkID == id {
				return true
			}
		}
	}
	return false
}

// flattenLiveOps returns cs's top-level live ops, each immediately
// followed by its own live Cascade entries, recursively (backbone §5.3: a
// cascade sub-op carries its own op<N> id and is independently
// droppable). This is the op list's render order, and it is also the set
// "accept all" walks to undrop every dropped hunk of every live op
// (s4-tui.md S4-T3 item 8) — not just the top-level ones Changeset.Live()
// returns.
func flattenLiveOps(cs *stage.Changeset) []stage.Op {
	if cs == nil {
		return nil
	}
	var out []stage.Op
	for _, op := range cs.Live() {
		out = append(out, op)
		out = append(out, flattenLiveCascade(op.Cascade)...)
	}
	return out
}

// flattenLiveCascade is flattenLiveOps' recursive step over one op's
// Cascade slice, which — unlike a Changeset — has no Live() of its own, so
// the dropped/rejected filter is applied by hand at every level.
func flattenLiveCascade(cascade []stage.Op) []stage.Op {
	var out []stage.Op
	for _, op := range cascade {
		if op.State == stage.StateDropped || op.State == stage.StateRejected {
			continue
		}
		out = append(out, op)
		out = append(out, flattenLiveCascade(op.Cascade)...)
	}
	return out
}

// opDisplayPath returns the one path (or path pair) the op list shows for
// op. Which field carries it depends on Kind: rename_page, merge_pages and
// split_page have no single Path (backbone §5.3), and add_link's endpoints
// are From/To rather than Path.
func opDisplayPath(op stage.Op) string {
	switch op.Kind {
	case stage.OpRenamePage:
		return op.From + " -> " + op.To
	case stage.OpMergePages:
		return strings.Join(op.Sources, ", ") + " -> " + op.To
	case stage.OpSplitPage:
		return op.Path + " -> " + strings.Join(op.Sources, ", ")
	case stage.OpAddLink:
		return op.From + " -> " + op.To
	default:
		return op.Path
	}
}

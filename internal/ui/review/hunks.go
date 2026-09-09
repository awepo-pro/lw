// hunks.go implements the review screen's cursor model: a single cursor
// walking the flattened sequence of (FileDiff, Hunk) pairs across a
// stage.Diff's Files, in the engine's own risk order (backbone §5.6) — an
// order this package must never re-sort (s4-tui.md S4-T3 §5 item 5). Kept
// as small, pure functions so the cursor arithmetic is unit-testable
// without constructing a Model, an Engine, or a tea.Program at all.
package review

import (
	"strings"

	"github.com/awepo-pro/lw/internal/stage"
)

// cursorStop is one addressable position in the review cursor's walk
// order: the index of its FileDiff within a Diff's Files, and the index of
// its Hunk within that FileDiff's Hunks. An op with zero hunks — a rename,
// a merge, a split, an add_link marker — contributes no cursorStop at all:
// it renders in the op list but is never a cursor stop, and there is no
// op-level focus key in the frozen keymap to make it one (s4-tui.md S4-T3
// §5).
type cursorStop struct {
	fileIdx int
	hunkIdx int
}

// buildCursorStops flattens d.Files, in order, and within each file its
// Hunks, in order, into the review cursor's walk order. d.Files already
// arrives risk-sorted by the engine (backbone §5.6: "sorted by risk:
// create/rename/merge/split, then patch, then add_link") — this function
// must never re-sort it, only walk it.
func buildCursorStops(d stage.Diff) []cursorStop {
	var stops []cursorStop
	for fi, f := range d.Files {
		for hi := range f.Hunks {
			stops = append(stops, cursorStop{fileIdx: fi, hunkIdx: hi})
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
// d, and false when i is out of range for stops or the stop it names is
// out of range for d — an empty diff, or a changeset whose every op has
// zero hunks, both resolve to ok == false rather than a panic.
func resolveCursor(d stage.Diff, stops []cursorStop, i int) (opID, hunkID string, ok bool) {
	if i < 0 || i >= len(stops) {
		return "", "", false
	}
	st := stops[i]
	if st.fileIdx < 0 || st.fileIdx >= len(d.Files) {
		return "", "", false
	}
	f := d.Files[st.fileIdx]
	if st.hunkIdx < 0 || st.hunkIdx >= len(f.Hunks) {
		return "", "", false
	}
	return f.OpID, f.Hunks[st.hunkIdx].ID, true
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

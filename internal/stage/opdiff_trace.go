// opdiff_trace.go owns the Hunk mechanics every hunk-bearing path in this
// package shares: applyHunks — the one function that positions a hunk's
// content, run by DropHunk, UndropHunk and Commit (backbone §5.4) and by
// OpDiff's proposed-change reconstruction (issue C-131, MASTER §9 D-3M) —
// its engine applyHunksTraced, which adds the per-line ownership record
// contract §1 note 4's attribution consumes, the owner-run splitting that
// turns a trace into DisplayHunk runs, and buildCascadeHunks, the one hunk
// constructor, kept beside the applier because the applier's
// first-text-match anchor IS the Hunk contract the constructor writes
// against.
//
// Ownership is carried through reconstruction, never inferred from text
// (§1 note 4, amended 2026-09-15, MASTER §8 ORCH-7 — joint review C1):
// hunkWindows merges changes fewer than 2*diffContext+1 ops apart into one
// window, so text matching labelled whole merged windows with one hunk's
// id and let Review's y/n act on lines that hunk did not own. The trace
// tags each spliced line with its producing Hunk.ID at splice time;
// applyHunks wraps the traced form and discards the record.
package stage

import (
	"fmt"
	"strings"
)

// hunkTrace is applyHunksTraced's per-line ownership record for one
// patch_page FileDiff (contract §1 note 4, amended 2026-09-15). newOwner
// names, per line of out (index-aligned with strings.Split(string(out),
// "\n")), the Hunk.ID that produced it — "" for a line before already
// had. oldRemover names, per line of before (index-aligned the same way),
// the Hunk.ID that removed it from the output — "" when the line survives.
// before and out are kept so opDiffWindows can prove BOTH diff sides align
// with the trace's indices, byte for byte, before it indexes them: a
// stale op's Old is the working tree, not the Before these positions
// refer to, and indexing the trace against it would be fiction.
type hunkTrace struct {
	before     []byte
	out        []byte
	newOwner   []string
	oldRemover []string
}

// applyHunksTraced is applyHunks with a per-line ownership record: at
// splice time it tags every line of the output with the Hunk.ID that
// produced it, and every line of before with the Hunk.ID that removed it.
// The bytes are applyHunks' by construction — applyHunks is this
// function's thin wrapper — so DropHunk, UndropHunk and Commit behave
// exactly as before (contract §1 note 4).
//
// Hunk semantics, unchanged since 001: every hunk this package itself
// constructs (buildCascadeHunks below) pairs each Del line with the Add
// line it becomes, one pair per hunk, so applying a hunk is "find this
// old line, replace it with this new line"; a hunk with more Del than Add
// entries removes the extras, and one with more Add than Del inserts the
// extras after the last matched position (or at the end of the body if
// nothing matched). This is deliberately minimal, not diff.ComputeHunks'
// inverse — that generic Myers/LCS machinery lives in diff.go (§5.6).
//
// Ownership is recorded at the splice, never inferred from text: a hunk
// that replaces line X owns the replacement even when another hunk adds
// the same text elsewhere, and two adjacent insert-only hunks stay two
// owners (the C1 defect class this exists to close). A dropped hunk owns
// nothing — it is skipped exactly as the bytes skip it.
//
// A hunk's Del line can match a line an EARLIER hunk produced (indexOfLine
// scans the current lines, not before's): such a line has no before-index
// to mark as removed, so oldRemover records nothing for it — and rightly
// so, since the diff against before contains no '-' line for it either.
func applyHunksTraced(before []byte, hunks []Hunk) (out []byte, newOwner, oldRemover []string) {
	lines := strings.Split(string(before), "\n")
	owner := make([]string, len(lines))   // per output line: producing Hunk.ID, "" = inherited from before
	origin := make([]int, len(lines))     // per output line: index into before's lines, -1 once hunk-produced
	remover := make([]string, len(lines)) // per before line: removing Hunk.ID, "" = survives into out
	for i := range origin {
		origin[i] = i
	}

	for _, h := range hunks {
		if h.Dropped {
			continue
		}
		if len(h.Add) > 0 && len(h.Del) == 0 && h.Section != "" {
			var at int
			lines, at = insertAtSectionEndAt(lines, h.Section, h.Add)
			owner = insertStrings(owner, at, len(h.Add), h.ID)
			origin = insertInts(origin, at, len(h.Add), -1)
			continue
		}
		n := len(h.Del)
		if len(h.Add) > n {
			n = len(h.Add)
		}
		pos := len(lines)
		for i := 0; i < n; i++ {
			switch {
			case i < len(h.Del) && i < len(h.Add):
				if idx := indexOfLine(lines, h.Del[i]); idx >= 0 {
					if o := origin[idx]; o >= 0 {
						remover[o] = h.ID // the replaced line is gone from out
					}
					lines[idx] = h.Add[i]
					owner[idx] = h.ID
					origin[idx] = -1
					pos = idx + 1
				}
			case i < len(h.Del):
				if idx := indexOfLine(lines, h.Del[i]); idx >= 0 {
					if o := origin[idx]; o >= 0 {
						remover[o] = h.ID
					}
					lines = append(lines[:idx], lines[idx+1:]...)
					owner = append(owner[:idx], owner[idx+1:]...)
					origin = append(origin[:idx], origin[idx+1:]...)
					pos = idx
				}
			default:
				ins := h.Add[i]
				tail := append([]string{ins}, lines[pos:]...)
				lines = append(lines[:pos], tail...)
				owner = insertStrings(owner, pos, 1, h.ID)
				origin = insertInts(origin, pos, 1, -1)
				pos++
			}
		}
	}
	return []byte(strings.Join(lines, "\n")), owner, remover
}

// applyHunks reconstructs a patch_page op's projected content by applying
// its non-dropped hunks, in order, to before's lines (backbone §5.4
// DropHunk Contract: "recomputes the op's projected content from Before
// plus its remaining live hunks"). A thin wrapper over applyHunksTraced:
// the bytes are that function's exactly — it IS the per-hunk loop — with
// the per-line ownership record discarded, so DropHunk, UndropHunk and
// Commit compute what they always computed. A pure insertion (Add
// non-empty, Del empty) carrying a Section is the one shape this loop
// cannot place by its own Del anchor: insertAtSectionEndAt (mdsection.go)
// positions it instead — the C-131 fix.
func applyHunks(before []byte, hunks []Hunk) []byte {
	out, _, _ := applyHunksTraced(before, hunks)
	return out
}

// indexOfLine returns the index of the first element of lines equal to s,
// or -1.
func indexOfLine(lines []string, s string) int {
	for i, l := range lines {
		if l == s {
			return i
		}
	}
	return -1
}

// buildCascadeHunks diffs oldBody against newBody line by line and returns
// one Hunk per changed line ("h1", "h2", … in body order), each pairing the
// single old line it replaces (Del) with the single new line it becomes
// (Add). RewriteWikilinks only ever substitutes text within a line — it
// never inserts or removes a "\n" — so oldBody and newBody always have the
// same line count and a positional line-by-line diff is exact, not an
// approximation of a general LCS diff (which is diff.go's job — see
// applyHunksTraced).
func buildCascadeHunks(p, oldBody, newBody string) []Hunk {
	oldLines := strings.Split(oldBody, "\n")
	newLines := strings.Split(newBody, "\n")
	n := len(oldLines)
	if len(newLines) < n {
		n = len(newLines)
	}
	var hunks []Hunk
	id := 1
	for i := 0; i < n; i++ {
		if oldLines[i] == newLines[i] {
			continue
		}
		hunks = append(hunks, Hunk{
			ID:   fmt.Sprintf("h%d", id),
			Path: p,
			Del:  []string{oldLines[i]},
			Add:  []string{newLines[i]},
		})
		id++
	}
	return hunks
}

// insertStrings returns s with n copies of v inserted at index i.
func insertStrings(s []string, i, n int, v string) []string {
	s = append(s, make([]string, n)...)
	copy(s[i+n:], s[i:len(s)-n])
	for k := 0; k < n; k++ {
		s[i+k] = v
	}
	return s
}

// insertInts returns s with n copies of v inserted at index i.
func insertInts(s []int, i, n int, v int) []int {
	s = append(s, make([]int, n)...)
	copy(s[i+n:], s[i:len(s)-n])
	for k := 0; k < n; k++ {
		s[i+k] = v
	}
	return s
}

// lineIn reports whether text appears verbatim in lines. Used by the
// attribution suite to check that a window's owned lines belong to its own
// hunk.
func lineIn(lines []string, text string) bool {
	for _, l := range lines {
		if l == text {
			return true
		}
	}
	return false
}

// ownerRun is one contiguous same-owner stretch of a hunkWindows window,
// as an inclusive range of indices into the diffOps slice.
type ownerRun struct {
	lo, hi int
	owner  string
}

// ownerRuns splits window w at every change of owned line: one run per
// contiguous stretch of '+'/'-' ops sharing a Hunk.ID. A context (' ') op
// belongs to the run that FOLLOWS it — the context a change sits in is
// what the reviewer reads the change against — so leading context opens
// the first run, context between two runs travels with the later one, and
// trailing context closes the last.
//
// A '+'/'-' op whose owner is "" — possible only in the duplicate-text
// corner where the LCS aligns a byte-identical line differently than
// applyHunks' first-match anchor, so the op's before-index names no line
// any hunk removed (or no line any hunk produced) — is NOT context and
// must NOT be folded into a neighbouring run: it gets its own ownerless
// run, because a DisplayHunk's HunkID must own exactly the lines it shows
// and y/n acts on that id. A window with no owned op at all yields a
// single ownerless run covering the whole window (HunkID "").
func ownerRuns(ops []diffOp, w hunkWindow, tr *hunkTrace, oldPos, newPos []int) []ownerRun {
	var runs []ownerRun
	pending := w.lo // first op not yet placed: context buffered for the run that follows
	for k := w.lo; k <= w.hi; k++ {
		if ops[k].kind == ' ' {
			continue // context: stays pending
		}
		var owner string
		switch ops[k].kind {
		case '+':
			owner = tr.newOwner[newPos[k]]
		case '-':
			owner = tr.oldRemover[oldPos[k]]
		}
		if n := len(runs); n > 0 && runs[n-1].owner == owner {
			runs[n-1].hi = k
		} else {
			runs = append(runs, ownerRun{lo: pending, hi: k, owner: owner})
		}
		pending = k + 1
	}
	if len(runs) == 0 {
		return []ownerRun{{lo: w.lo, hi: w.hi}}
	}
	runs[len(runs)-1].hi = w.hi // trailing context closes the last run
	return runs
}

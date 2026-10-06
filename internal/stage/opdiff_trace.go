// opdiff_trace.go owns the Hunk mechanics every hunk-bearing path in this
// package shares: applyHunks — the one function that positions a hunk's
// content, run by DropHunk, UndropHunk and Commit (backbone §5.4) and by
// OpDiff's proposed-change reconstruction (issue C-131, MASTER §9 D-3M) —
// its engine applyHunksTraced, which adds the per-line ownership record
// contract §1 note 4's attribution consumes, the owner-run splitting that
// turns a trace into DisplayHunk runs, and buildCascadeHunks, the cascade's
// hunk constructor, kept beside the applier because the Hunk contract the
// constructor writes against is the applier's. (A hunk that carries At and
// Lines — everything ComputeHunks and buildCascadeHunks return since 052 —
// is placed by its own window, applyWindow; the first-text-match anchor
// below is what a hunk without a position still gets.)
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
// Hunk semantics, unchanged since 001, for a hunk WITHOUT a position
// (At == 0 or no Lines: staged before 052, or built by hand): each Del line
// pairs with the Add line it becomes, one pair per hunk, so applying a hunk
// is "find this old line, replace it with this new line"; a hunk with more
// Del than Add entries removes the extras, and one with more Add than Del
// inserts the extras after the last matched position (or at the end of the
// body if nothing matched). This is deliberately minimal, not
// diff.ComputeHunks' inverse — that generic Myers/LCS machinery lives in
// diff.go (§5.6).
//
// A hunk WITH a position (052, TD-15) is applied like patch instead:
// applyWindow locates its window through origin — the output line that is
// still before's line At — and walks its Lines. The legacy placement could
// not be byte-exact after a Review drop or undrop: a Del went to its FIRST
// textual match (a blank line took the file's first blank), an Add-only hunk
// to its section's end (insert_before landed after its target), and a merged
// window lost the context between its changes (050's review M1: a body line
// landed inside the YAML). A window that cannot be located, or whose lines no
// longer match, applies nothing by this path and falls back to the legacy
// placement, so a stale position is never worse than before 052.
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

	// The empty element strings.Split leaves after before's final "\n" is not
	// a line of the file; a window must never consume it (its origin).
	phantom := -1
	if strings.HasSuffix(string(before), "\n") {
		phantom = len(lines) - 1
	}

	for _, h := range hunks {
		if h.Dropped {
			continue
		}
		if h.At > 0 && len(h.Lines) > 0 {
			var ok bool
			if lines, owner, origin, ok = applyWindow(h, lines, owner, origin, remover, phantom); ok {
				continue
			}
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

// applyWindow applies h's positioned window — h.At and h.Lines — to lines
// like patch does, keeping owner, origin and remover in step with it. It
// finds the window's start as the output line whose origin is before's line
// h.At (origin follows a line through every earlier hunk's splices, so an
// earlier hunk that shifted the lines below it is accounted for), then walks
// h.Lines: " " must equal the line under the cursor and moves past it, "-"
// must equal it and deletes it (remover records h as its remover when it is
// one of before's own lines), and "+" inserts a line owned by h.
//
// ok is false — and lines, owner and origin come back untouched — when the
// window cannot be located, runs past the last line, meets a line that
// differs from its own, or holds a malformed diff line. The check is a
// dry run over the whole window before anything is spliced, so a hunk is
// applied entirely or not at all. phantom is the origin of the empty tail
// strings.Split leaves after before's final newline (-1 when there is
// none): consuming it would eat the file's trailing newline.
func applyWindow(h Hunk, lines, owner []string, origin []int, remover []string, phantom int) (outLines, outOwner []string, outOrigin []int, ok bool) {
	start := -1
	for i, o := range origin {
		if o == h.At-1 {
			start = i
			break
		}
	}
	if start < 0 {
		return lines, owner, origin, false
	}

	pos := start
	for _, l := range h.Lines {
		if l == "" {
			return lines, owner, origin, false
		}
		switch l[0] {
		case ' ', '-':
			if pos >= len(lines) || lines[pos] != l[1:] || origin[pos] == phantom {
				return lines, owner, origin, false
			}
			pos++
		case '+':
		default:
			return lines, owner, origin, false
		}
	}

	pos = start
	for _, l := range h.Lines {
		switch l[0] {
		case ' ':
			pos++
		case '-':
			if o := origin[pos]; o >= 0 {
				remover[o] = h.ID // the removed line is gone from out
			}
			lines = append(lines[:pos], lines[pos+1:]...)
			owner = append(owner[:pos], owner[pos+1:]...)
			origin = append(origin[:pos], origin[pos+1:]...)
		case '+':
			lines = insertStrings(lines, pos, 1, l[1:])
			owner = insertStrings(owner, pos, 1, h.ID)
			origin = insertInts(origin, pos, 1, -1)
			pos++
		}
	}
	return lines, owner, origin, true
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
// positions it instead — the C-131 fix. Both are the placement of a hunk
// without a position; one with At and Lines is applied by applyWindow
// (052), which is byte-exact where these two are not.
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

// buildCascadeHunks diffs oldText against newText line by line and returns
// one Hunk per changed line ("h1", "h2", … in file order), each pairing the
// single old line it replaces (Del) with the single new line it becomes
// (Add). RewriteWikilinks only ever substitutes text within a line — it
// never inserts or removes a "\n" — so oldText and newText always have the
// same line count and a positional line-by-line diff is exact, not an
// approximation of a general LCS diff (which is diff.go's job — see
// applyHunksTraced).
//
// 052: each hunk also carries its position — At is the 1-based line of the
// rewrite and Lines is its two-line window, "-old" and "+new" — so applying
// it replaces THAT line, not the first line with the same text: with one
// link line twice in a page, dropping the first rewrite used to rewrite the
// second. The texts must therefore be the whole before and after FILES, the
// bytes op.Before and the post-image hold (a page's frontmatter included),
// never a body alone: At indexes the file applyHunksTraced splits.
func buildCascadeHunks(p, oldText, newText string) []Hunk {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
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
			ID:    fmt.Sprintf("h%d", id),
			Path:  p,
			Del:   []string{oldLines[i]},
			Add:   []string{newLines[i]},
			At:    i + 1,
			Lines: []string{"-" + oldLines[i], "+" + newLines[i]},
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

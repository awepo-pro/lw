// opdiff.go implements contract §1's OpDiff: an op's PROPOSED change —
// every hunk applied regardless of its Dropped flag, a dropped op shown as
// if it were still live — rendered as unified windows attributed to
// Op.Hunks ids. Read-only: no lock, no store write, no journal event, no
// changeset persist. It only reads e.vault, e.store and the changeset
// already on disk/in memory, the same guarantee Diff() makes (diff.go).
//
// DisplayHunk exists so Review can show a dropped hunk exactly as it looked
// before it was dropped — the opposite question from Diff's FileDiff, which
// renders what Commit will actually write. The two share the same
// window/header arithmetic (diffOps + hunkWindows + prefixCounts,
// diff.go:497-681, contract §1 note 3) through opDiffWindows below, an
// unexported helper local to this file that calls diff.go's existing
// unexported functions. diff.go and op.go are not edited by that helper.
//
// Reconstructing a dropped hunk's content (opDiffFileDiffs' patch_page
// branch) is done through applyHunks (opdiff_trace.go) — the same function
// DropHunk/UndropHunk call to compute what Commit actually writes (MASTER
// §9 D-3M, issue C-131). Before this file's T15 revision, this
// reconstruction went through a second, display-only copy of that per-hunk
// loop (opDiffApplyHunks/sectionInsertionPoint), so an add-only hunk's
// Section anchor only ever fixed what Review SHOWED, not what UndropHunk
// actually committed — a display that disagreed with Commit is worse than
// not showing the position at all. There is now exactly one function that
// positions a hunk, applyHunksTraced (opdiff_trace.go), with applyHunks as
// its byte-exact wrapper.
//
// Attribution (contract §1 note 4, amended 2026-09-15, MASTER §8 ORCH-7) is
// also carried through that reconstruction, and only through it: the joint
// T01+T15 review (C1) found the previous first-text-match rule unsound —
// hunkWindows merges changes fewer than 7 ops apart, so two insert-only
// hunks in one section became ONE window labelled h1, and byte-identical
// hunks could be attributed to each other, making Review's y/n act on a
// different hunk than the one displayed. opDiffWindows correlates each
// diff op to its line index in the traced output (a '+' op to the hunk
// that produced that line, a '-' op to the hunk that removed that line)
// and splits every window at owner changes. When the diff sides do NOT
// align with the trace byte for byte — a stale patch_page, whose Old is
// the working tree rather than the Before the trace's indices refer to —
// no id is attached at all: a window whose ownership cannot be proven must
// not offer y/n a target, and DropHunk/UndropHunk have no stale guard to
// catch a guessed one.
package stage

import (
	"bytes"
	"fmt"
)

// DisplayLine is one line of a DisplayHunk: Kind is ' ' (context), '+' or
// '-'.
type DisplayLine struct {
	Kind byte
	Text string
}

// DisplayHunk is one unified-diff window of an op's PROPOSED change: every
// Op.Hunk applied, dropped or not. It exists so Review can show a dropped
// hunk exactly as it looked before it was dropped. Display only: never
// persisted, never used by Commit.
type DisplayHunk struct {
	Header  string        // "@@ -34,6 +34,10 @@", formatted exactly as Diff.Unified does
	HunkID  string        // the Op.Hunks id this window belongs to; "" when the op persists no hunks
	Dropped bool          // the owning Op.Hunk's Dropped flag; false when HunkID == ""
	Lines   []DisplayLine // no "\ No newline" markers
}

// FileOpDiff is one file an op touches, as DisplayHunks.
type FileOpDiff struct {
	Path  string
	OpID  string
	Kind  OpKind
	Stale bool
	Hunks []DisplayHunk
}

// OpDiff returns the proposed change of op opID (top-level or cascade) as
// one FileOpDiff per file, in the order Engine.Diff lists that op's files
// (contract §1).
//
// Contract note 1: ErrNoChangeset when none is open; an unknown id returns
// "stage: op diff: no such op %q" — Changeset.Op searches top-level ops and
// every op's Cascade (findOpPtr, op.go), so a cascade sub-op id resolves
// exactly like a top-level one.
func (e *Engine) OpDiff(opID string) ([]FileOpDiff, error) {
	c, err := e.currentOpen()
	if err != nil {
		return nil, err
	}
	op, ok := c.Op(opID)
	if !ok {
		return nil, fmt.Errorf("stage: op diff: no such op %q", opID)
	}

	fds, traces, err := e.opDiffFileDiffs(*op)
	if err != nil {
		return nil, err
	}
	for i := range fds {
		// Contract note 2: Old is always the working tree, regardless of
		// kind — the op's proposed change is compared against what the
		// vault holds right now, not a per-kind synthetic Old.
		fds[i].Old = e.workingTreeContent(fds[i].Path)
		if owner, ok := c.Op(fds[i].OpID); ok {
			fds[i].Stale = owner.State == StateStale
		}
	}

	out := make([]FileOpDiff, 0, len(fds)+1)
	for i, fd := range fds {
		var hunks []Hunk
		if owner, ok := c.Op(fd.OpID); ok {
			hunks = owner.Hunks
		}
		out = append(out, FileOpDiff{
			Path:  fd.Path,
			OpID:  fd.OpID,
			Kind:  fd.Kind,
			Stale: fd.Stale,
			Hunks: opDiffWindows(fd.Old, fd.New, hunks, traces[i]),
		})
	}

	idx, err := e.opDiffDerivedIndex(out, opID)
	if err != nil {
		return nil, err
	}
	if idx != nil {
		out = append(out, *idx)
	}
	return out, nil
}

// opDiffFileDiffs returns the FileDiff entries op (top-level or cascade)
// produces PROPOSED — every hunk applied regardless of its Dropped flag,
// and the op itself never skipped for being Dropped/Rejected (contract §1
// note 2), with, parallel to the entries, each entry's hunkTrace (nil for
// every entry whose New is not applyHunks-derived). It never mutates op:
// the patch_page branch copies Hunks before clearing Dropped, and every
// other kind copies op by value before forcing State — the persisted op
// (reached through c.Op, a pointer into the live Changeset) is never
// written through.
//
// patch_page is special-cased because DropHunk already overwrites the
// persisted Op.After with the post-drop projection (backbone §5.4 DropHunk
// Contract) — postImage(op) would therefore already hide a dropped hunk's
// change, the opposite of what OpDiff exists to show, whenever a hunk
// actually IS dropped. Only then does this reconstruct from Before, through
// applyHunksTraced — the same function DropHunk/UndropHunk run (through
// applyHunks), so display and commit can never disagree about where a
// hunk's content lands (MASTER §9 D-3M, issue C-131).
//
// The postImage fast path (nothing dropped) still attributes through the
// trace (note 4): tracePatch rebuilds the projection with every Dropped
// forced false and checks it reproduces the post-image bytes. When it does
// not — op.Content and op.Hunks disagree about the projection, which
// nothing in §5.5 prevents for a hand-built op — the traced bytes are
// DISPLAYED instead of the post-image: the hunks are the reviewable unit,
// and the first DropHunk/UndropHunk would recompute After to exactly these
// bytes, so showing the stale post-image would be the one view a y/n
// toggle could not reproduce. Every other kind carries no such baked-in
// drop (create_page/ingest_source's After is the whole post-image;
// rename/merge/split/retract/add_link derive straight from the vault), so
// fileDiffsForOp on a live copy is already the "everything applied" view;
// State is forced to StateProposed only so a DROPPED OP is not skipped
// outright by fileDiffsForOp's own Dropped/Rejected guard.
func (e *Engine) opDiffFileDiffs(op Op) ([]FileDiff, []*hunkTrace, error) {
	if op.Kind == OpPatchPage {
		if !anyHunkDropped(op.Hunks) {
			post, err := e.postImage(op)
			if err != nil {
				return nil, nil, fmt.Errorf("stage: op diff: %s: %w", op.ID, err)
			}
			newContent, tr := e.tracePatch(op, post)
			return []FileDiff{{
				Path: op.Path, OpID: op.ID, Kind: op.Kind,
				New: string(newContent),
			}}, []*hunkTrace{tr}, nil
		}

		before, err := e.store.Get(op.Before)
		if err != nil {
			return nil, nil, fmt.Errorf("stage: op diff: %s: %w", op.ID, err)
		}
		hunks := make([]Hunk, len(op.Hunks))
		copy(hunks, op.Hunks)
		for i := range hunks {
			hunks[i].Dropped = false
		}
		out, newOwner, oldRemover := applyHunksTraced(before, hunks)
		return []FileDiff{{
			Path: op.Path, OpID: op.ID, Kind: op.Kind,
			New: string(out),
		}}, []*hunkTrace{{before: before, out: out, newOwner: newOwner, oldRemover: oldRemover}}, nil
	}

	live := op
	live.State = StateProposed
	fds, err := e.fileDiffsForOp(live)
	if err != nil {
		return nil, nil, err
	}
	// No trace: none of these News come from applyHunks, so there is no
	// ownership to carry (note 4: their windows keep HunkID "").
	return fds, make([]*hunkTrace, len(fds)), nil
}

// tracePatch rebuilds op's traced projection with every Dropped forced
// false and returns it next to its trace. post is op's stored post-image,
// kept when the traced bytes reproduce it exactly (the common case: a
// proposer's Content and Hunks agree). When op has no readable pre-image —
// §5.5 requires a patch_page Before to hash the current canonical content,
// so this is defensive for a hand-written changeset.json — post is
// returned with a nil trace and the windows keep HunkID "".
func (e *Engine) tracePatch(op Op, post []byte) ([]byte, *hunkTrace) {
	if op.Before == "" {
		return post, nil
	}
	before, err := e.store.Get(op.Before)
	if err != nil {
		return post, nil
	}
	hunks := make([]Hunk, len(op.Hunks))
	copy(hunks, op.Hunks)
	for i := range hunks {
		hunks[i].Dropped = false
	}
	out, newOwner, oldRemover := applyHunksTraced(before, hunks)
	if !bytes.Equal(out, post) {
		// Content and Hunks disagree; the hunks win for display (see
		// opDiffFileDiffs' fast-path note).
		return out, &hunkTrace{before: before, out: out, newOwner: newOwner, oldRemover: oldRemover}
	}
	return post, &hunkTrace{before: before, out: out, newOwner: newOwner, oldRemover: oldRemover}
}

// anyHunkDropped reports whether any of hunks is currently marked Dropped.
func anyHunkDropped(hunks []Hunk) bool {
	for _, h := range hunks {
		if h.Dropped {
			return true
		}
	}
	return false
}

// opDiffDerivedIndex returns the derived index.md FileOpDiff (contract §1
// note 5) when Engine.Diff() attributes one to opID and index.md is not
// already among out, else (nil, nil). HunkID is always "" — the derived
// line has no persisted hunk to attribute to (note 5: "HunkID ”").
func (e *Engine) opDiffDerivedIndex(out []FileOpDiff, opID string) (*FileOpDiff, error) {
	for _, fo := range out {
		if fo.Path == "index.md" {
			return nil, nil
		}
	}
	d, err := e.Diff()
	if err != nil {
		return nil, err
	}
	for _, fd := range d.Files {
		if fd.Path == "index.md" && fd.OpID == opID {
			return &FileOpDiff{
				Path:  fd.Path,
				OpID:  fd.OpID,
				Kind:  fd.Kind,
				Stale: fd.Stale,
				Hunks: opDiffWindows(fd.Old, fd.New, nil, nil),
			}, nil
		}
	}
	return nil, nil
}

// opDiffWindows computes old -> new's unified windows with the identical
// arithmetic diffFile uses (diffOps + hunkWindows + prefixCounts, diff.go —
// contract §1 note 3): same context, same header positions, same line
// kinds and texts. It never emits a "\ No newline at end of file" marker —
// DisplayLine has no representation for one (note 3).
//
// Attribution (note 4, amended 2026-09-15) is carried through the
// reconstruction, never inferred from text. When tr is non-nil AND both
// diff sides align with it byte for byte — old == tr.before, i.e. the op
// is not stale (a stale op's Old is the working tree, while its hunks were
// cut against Before), and new == tr.out — every '+' op is owned by
// tr.newOwner[newPos[k]] and every '-' op by tr.oldRemover[oldPos[k]], and
// each hunkWindows window is split at every owner change into one
// DisplayHunk per contiguous owner run (ownerRuns, opdiff_trace.go). The
// same HunkID may appear in several DisplayHunks when another hunk's
// changes interleave with its own; the header is recomputed per run with
// diffFile's own arithmetic.
//
// With no usable trace — create_page, ingest_source, the derived
// index.md, any non-patch kind, a patch_page without a readable Before,
// and a STALE patch_page, whose Old is the working tree the trace's
// indices know nothing about — each window is ONE DisplayHunk with HunkID
// "" and Dropped false. No id may be guessed there, not even by text:
// y/n acts on HunkID, DropHunk/UndropHunk never check staleness, and a
// window labelled with a hunk that does not own its lines would let a
// reviewer drop a hunk they were never shown — a flag that persists in
// the changeset until Commit writes the wrong projection. The stale op's
// proposed content is still displayed in full; it just carries no y/n
// target, which is the honest state for a proposal the tree has moved
// out from under.
func opDiffWindows(old, new string, hunks []Hunk, tr *hunkTrace) []DisplayHunk {
	oldLines, _ := diffSplitLines(old)
	newLines, _ := diffSplitLines(new)
	ops := diffOps(oldLines, newLines)
	windows := hunkWindows(ops, diffContext)
	if len(windows) == 0 {
		return nil
	}
	oldPos, newPos := prefixCounts(ops)

	// The trace's indices are only meaningful against the exact byte
	// strings they were computed over; string equality makes every line
	// split and every prefix position align one to one.
	correlated := tr != nil && old == string(tr.before) && new == string(tr.out)

	byID := make(map[string]Hunk, len(hunks))
	for _, h := range hunks {
		byID[h.ID] = h
	}

	out := make([]DisplayHunk, 0, len(windows))
	for _, w := range windows {
		if !correlated {
			out = append(out, DisplayHunk{
				Header: windowHeader(ops, oldPos, newPos, w.lo, w.hi),
				Lines:  windowLines(ops, w.lo, w.hi),
			})
			continue
		}
		for _, r := range ownerRuns(ops, w, tr, oldPos, newPos) {
			dropped := false
			if r.owner != "" {
				if h, ok := byID[r.owner]; ok {
					dropped = h.Dropped
				}
			}
			out = append(out, DisplayHunk{
				Header:  windowHeader(ops, oldPos, newPos, r.lo, r.hi),
				HunkID:  r.owner,
				Dropped: dropped,
				Lines:   windowLines(ops, r.lo, r.hi),
			})
		}
	}
	return out
}

// windowLines copies ops[lo..hi] as DisplayLines.
func windowLines(ops []diffOp, lo, hi int) []DisplayLine {
	lines := make([]DisplayLine, 0, hi-lo+1)
	for k := lo; k <= hi; k++ {
		lines = append(lines, DisplayLine{Kind: ops[k].kind, Text: ops[k].text})
	}
	return lines
}

// windowHeader formats the "@@ -a,b +c,d @@" header for the op range
// [lo,hi] with diffFile's own arithmetic (diff.go).
func windowHeader(ops []diffOp, oldPos, newPos []int, lo, hi int) string {
	oldCount := oldPos[hi+1] - oldPos[lo]
	newCount := newPos[hi+1] - newPos[lo]
	oldStart := oldPos[lo] + 1
	if oldCount == 0 {
		oldStart = oldPos[lo]
	}
	newStart := newPos[lo] + 1
	if newCount == 0 {
		newStart = newPos[lo]
	}
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)
}

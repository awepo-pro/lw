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
// unexported functions. diff.go and op.go are not edited.
package stage

import (
	"fmt"
	"strings"
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

	fds, err := e.opDiffFileDiffs(*op)
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
	for _, fd := range fds {
		var hunks []Hunk
		if owner, ok := c.Op(fd.OpID); ok {
			hunks = owner.Hunks
		}
		out = append(out, FileOpDiff{
			Path:  fd.Path,
			OpID:  fd.OpID,
			Kind:  fd.Kind,
			Stale: fd.Stale,
			Hunks: opDiffWindows(fd.Old, fd.New, hunks),
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
// note 2). It never mutates op: the patch_page branch copies Hunks before
// clearing Dropped, and every other kind copies op by value before forcing
// State — the persisted op (reached through c.Op, a pointer into the live
// Changeset) is never written through.
//
// patch_page is special-cased because DropHunk already overwrites the
// persisted Op.After with the post-drop projection (backbone §5.4 DropHunk
// Contract) — postImage(op) would therefore already hide a dropped hunk's
// change, the opposite of what OpDiff exists to show, whenever a hunk
// actually IS dropped. When NONE of op.Hunks is dropped, postImage(op) is
// used directly instead of reconstructing: op.After already IS "every hunk
// applied" in that case (nothing to undo), and using it verbatim is what
// makes contract note 6 an identity rather than a coincidence — Diff's own
// patch_page branch reads the exact same postImage(op). Only when a hunk
// actually needs undropping does this reconstruct from Before, through
// opDiffApplyHunks below (see its own doc comment for why that is not
// simply op.go's applyHunks called verbatim).
//
// Every other kind carries no such baked-in drop (create_page/ingest_
// source's After is the whole post-image; rename/merge/split/retract/
// add_link derive straight from the vault), so fileDiffsForOp on a live
// copy is already the "everything applied" view; State is forced to
// StateProposed only so a DROPPED OP is not skipped outright by
// fileDiffsForOp's own Dropped/Rejected guard.
func (e *Engine) opDiffFileDiffs(op Op) ([]FileDiff, error) {
	if op.Kind == OpPatchPage {
		if !anyHunkDropped(op.Hunks) {
			newContent, err := e.postImage(op)
			if err != nil {
				return nil, fmt.Errorf("stage: op diff: %s: %w", op.ID, err)
			}
			return []FileDiff{{
				Path: op.Path, OpID: op.ID, Kind: op.Kind,
				New: string(newContent),
			}}, nil
		}

		before, err := e.store.Get(op.Before)
		if err != nil {
			return nil, fmt.Errorf("stage: op diff: %s: %w", op.ID, err)
		}
		hunks := make([]Hunk, len(op.Hunks))
		copy(hunks, op.Hunks)
		for i := range hunks {
			hunks[i].Dropped = false
		}
		newContent := opDiffApplyHunks(before, hunks)
		return []FileDiff{{
			Path: op.Path, OpID: op.ID, Kind: op.Kind,
			New: string(newContent),
		}}, nil
	}

	live := op
	live.State = StateProposed
	return e.fileDiffsForOp(live)
}

// opDiffApplyHunks reconstructs a patch_page's PROPOSED content the same
// way op.go's applyHunks does — same per-hunk loop, same Del-anchored
// replace/remove via indexOfLine — with one addition this display-only
// path needs and Commit's write path (DropHunk) does not: positioning a
// pure-insertion hunk (Add non-empty, Del EMPTY).
//
// applyHunks anchors a hunk by finding one of its own Del lines; a hunk
// with no Del has no such anchor, so applyHunks' own documented fallback is
// "insert... at the end of the body if nothing matched" (op.go). That
// fallback is correct for DropHunk's purpose (recomputing Op.After for
// Commit, where an add-only hunk is rare and the recomputed content is
// re-diffed against the working tree by Diff() regardless of where the new
// lines physically sit in the file). It is NOT correct for OpDiff's purpose
// — displaying a dropped hunk positioned where it was actually proposed —
// because Hunk.Before, the field that recorded that position, is
// deliberately excluded from JSON (changeset.go: `json:"-"`, "context/
// removed lines, for display") and does not survive a reload from
// changeset.json; by the time OpDiff runs, "the end of the body" is the
// only position left for applyHunks to fall back to.
//
// Op.Section/Hunk.Section (backbone §5.3), unlike Hunk.Before, IS
// serialized, and it is exactly the anchor derive.go's insertIntoSection
// already uses for the identical problem on index.md: "insert this new
// content after the named section's own content, before the next heading."
// So a pure-insertion hunk here anchors on h.Section the same way, via
// sectionInsertionPoint below, and only falls back to applyHunks' original
// end-of-body behavior when the hunk carries no Section or it names a
// heading not present in before (e.g. a hand-built hunk in a test that sets
// neither) — so every hunk this package's own ComputeHunks or
// buildCascadeHunks ever produces, and every hunk with a Del anchor,
// behaves exactly as applyHunks already does; only a persisted, dropped,
// add-only, Section-carrying hunk (the shape a patch_page proposal with a
// brand new subsection takes) takes the new path. See this subtask's report
// for the fixture evidence (spec: the mockup vault's op3 and op4) that
// motivated this addition, and MASTER §8 for the correction-log entry.
func opDiffApplyHunks(before []byte, hunks []Hunk) []byte {
	lines := strings.Split(string(before), "\n")
	for _, h := range hunks {
		if h.Dropped {
			continue
		}
		n := len(h.Del)
		if len(h.Add) > n {
			n = len(h.Add)
		}
		pos := len(lines)
		if len(h.Del) == 0 && len(h.Add) > 0 {
			if p, ok := sectionInsertionPoint(lines, h.Section); ok {
				pos = p
			}
		}
		for i := 0; i < n; i++ {
			switch {
			case i < len(h.Del) && i < len(h.Add):
				if idx := indexOfLine(lines, h.Del[i]); idx >= 0 {
					lines[idx] = h.Add[i]
					pos = idx + 1
				}
			case i < len(h.Del):
				if idx := indexOfLine(lines, h.Del[i]); idx >= 0 {
					lines = append(lines[:idx], lines[idx+1:]...)
					pos = idx
				}
			default:
				ins := h.Add[i]
				tail := append([]string{ins}, lines[pos:]...)
				lines = append(lines[:pos], tail...)
				pos++
			}
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// sectionInsertionPoint returns the index in lines immediately before the
// first heading line ("#" prefix) that follows the line exactly equal to
// section — mirroring derive.go's insertIntoSection ("insert after this
// section's content, before the next heading") — or (0, false) when section
// is empty or does not appear in lines verbatim.
func sectionInsertionPoint(lines []string, section string) (int, bool) {
	if section == "" {
		return 0, false
	}
	head := -1
	for i, l := range lines {
		if l == section {
			head = i
			break
		}
	}
	if head < 0 {
		return 0, false
	}
	for i := head + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "#") {
			return i, true
		}
	}
	return len(lines), true
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
				Hunks: opDiffWindows(fd.Old, fd.New, nil),
			}, nil
		}
	}
	return nil, nil
}

// opDiffWindows computes old -> new's unified windows with the identical
// arithmetic diffFile uses (diffOps + hunkWindows + prefixCounts, diff.go —
// contract §1 note 3): same context, same header positions, same line
// kinds and texts. It never emits a "\ No newline at end of file" marker —
// DisplayLine has no representation for one (note 3). Each window is then
// attributed to the first hunks[i] (in persisted order) with at least one
// '+' line text in Add or one '-' line text in Del (note 4); no match
// leaves HunkID "" and Dropped false.
func opDiffWindows(old, new string, hunks []Hunk) []DisplayHunk {
	oldLines, _ := diffSplitLines(old)
	newLines, _ := diffSplitLines(new)
	ops := diffOps(oldLines, newLines)
	windows := hunkWindows(ops, diffContext)
	if len(windows) == 0 {
		return nil
	}
	oldPos, newPos := prefixCounts(ops)

	out := make([]DisplayHunk, 0, len(windows))
	for _, w := range windows {
		oldCount := oldPos[w.hi+1] - oldPos[w.lo]
		newCount := newPos[w.hi+1] - newPos[w.lo]
		oldStart := oldPos[w.lo] + 1
		if oldCount == 0 {
			oldStart = oldPos[w.lo]
		}
		newStart := newPos[w.lo] + 1
		if newCount == 0 {
			newStart = newPos[w.lo]
		}
		header := fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)

		lines := make([]DisplayLine, 0, w.hi-w.lo+1)
		for k := w.lo; k <= w.hi; k++ {
			lines = append(lines, DisplayLine{Kind: ops[k].kind, Text: ops[k].text})
		}

		hunkID, dropped := attributeWindow(lines, hunks)
		out = append(out, DisplayHunk{Header: header, HunkID: hunkID, Dropped: dropped, Lines: lines})
	}
	return out
}

// attributeWindow implements contract §1 note 4: the window belongs to the
// first hunk (in persisted order) that contributed at least one of the
// window's '+' or '-' lines.
func attributeWindow(lines []DisplayLine, hunks []Hunk) (id string, dropped bool) {
	for _, h := range hunks {
		for _, l := range lines {
			switch l.Kind {
			case '+':
				if containsLine(h.Add, l.Text) {
					return h.ID, h.Dropped
				}
			case '-':
				if containsLine(h.Del, l.Text) {
					return h.ID, h.Dropped
				}
			}
		}
	}
	return "", false
}

// containsLine reports whether text appears verbatim in lines.
func containsLine(lines []string, text string) bool {
	for _, l := range lines {
		if l == text {
			return true
		}
	}
	return false
}

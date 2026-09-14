// diff.go implements the backbone §5.6 Diff/FileDiff types, Unified,
// UnifiedFile and ComputeHunks, plus func (e *Engine) Diff() (Diff, error)
// from §5.4's Engine block — declared there, assigned here because no
// earlier subtask claimed it (MASTER §9 D-BG).
//
// Two decisions carry the whole file and are documented at length in the
// backbone's §5.6 Contract notes; restated briefly here because they are
// easy to get backwards while reading the code:
//
//   - D-BQ: Unified/UnifiedFile position and group hunks with
//     ComputeHunks(Old, New) AT RENDER TIME. They never read FileDiff.Hunks.
//     FileDiff.Hunks stays the persisted Op.Hunks, verbatim, forever — the
//     review surface a human accepts or drops, never renumbered.
//   - D-BR/D-BS: rename_page, merge_pages, split_page and add_link carry no
//     Before/After/Hunks on the op itself; their content is derived from the
//     vault and the CAS. A retract's New is the tombstone Commit will
//     actually write (retractTombstone, apply.go — a call, not an edit).
package stage

import (
	"fmt"
	"sort"
	"strings"
)

// FileDiff is one file's before/after view within a Diff (backbone §5.6).
type FileDiff struct {
	Path    string
	OpID    string
	Kind    OpKind
	Old     string // working-tree content ("" for a new file)
	New     string // projected content ("" for a deletion)
	Hunks   []Hunk
	Stale   bool
	Added   int
	Removed int
}

// Diff is the open changeset rendered as a set of per-file before/after
// views (backbone §5.6).
type Diff struct {
	Changeset string
	Files     []FileDiff // sorted by risk: create/rename/merge/split, then patch, then add_link
	Added     int
	Removed   int
}

// Diff renders the currently open changeset.
//
// Contract (backbone §5.4, §5.6, MASTER §9 D-BG): returns ErrNoChangeset
// when none is open. It never acquires the lock (§5.2 names Diff
// explicitly) — it only reads e.vault, e.store and the changeset already on
// disk. It iterates Changeset.Live(): dropped top-level ops are excluded by
// Live() itself; a dropped or rejected CASCADE sub-op is excluded here, by
// the same per-node check apply.go's planOp applies at every level of the
// cascade tree, so Diff shows exactly the set of writes Commit will
// actually perform. Files is sorted by risk, tie-broken by Path then OpID
// so a golden comparison cannot flake.
func (e *Engine) Diff() (Diff, error) {
	c, err := e.currentOpen()
	if err != nil {
		return Diff{}, err
	}

	var files []FileDiff
	for _, op := range c.Live() {
		fds, err := e.fileDiffsForOp(op)
		if err != nil {
			return Diff{}, err
		}
		files = append(files, fds...)
	}

	if err := e.applyDerivedIndexDiff(&files, c.Live()); err != nil {
		return Diff{}, err
	}

	sortFileDiffs(files)

	d := Diff{Changeset: c.ID, Files: files}
	for i := range d.Files {
		_, added, removed := diffFile(d.Files[i].Old, d.Files[i].New)
		d.Files[i].Added = added
		d.Files[i].Removed = removed
		d.Added += added
		d.Removed += removed
	}
	return d, nil
}

// applyDerivedIndexDiff is S2-T8 rule (a)'s THIRD surface: the identical
// derivation pass buildCommitMaterialization (apply.go) and projectedTree
// (projection.go) already run, so what a reviewer sees here is what
// Commit actually writes (/.dev-notes/PLAN-v1.md §1 — nothing lands without hunk-level
// human review, and a review surface that misdescribes index.md is
// exactly the C-65 defect class this repair closes on the third and last
// place it could still hide). Mutates files in place.
//
// Seeding mirrors the other two passes exactly: from an existing
// index.md FileDiff's New when a rename/merge cascade sub-op already
// produced one (fileDiffsForOp's OpPatchPage case sets that from
// e.postImage — the cascade's rewritten content), else from the working
// tree via e.vault.Read directly, matching apply.go/projection.go's own
// "a vault with no index.md at all derives nothing, not an error" rule
// (haveIndex stays false and this contributes no entry at all).
//
// Contract (D-BQ): when an index.md FileDiff already exists, only its New
// is replaced — OpID, Kind and Hunks are untouched, so a cascade's
// persisted hunk ids never renumber. A brand new entry (no cascade wrote
// one) carries no Hunks at all, the same shape retract/split_page/
// rename_page/merge_pages already have (D-BR/D-BS): derived content that
// does not round-trip through changeset.json carries none: Unified/
// UnifiedFile compute what to render from ComputeHunks(Old, New) at
// render time regardless (D-BQ). Its OpID is the first live create_page
// that derived a line, so DropOp on that op drops its index.md line's
// FileDiff attribution with it rather than leaving an orphaned entry
// pointing at a dropped op.
func (e *Engine) applyDerivedIndexDiff(files *[]FileDiff, live []Op) error {
	creates := liveCreatePages(live)
	if len(creates) == 0 {
		return nil
	}

	var idxDiff *FileDiff
	for i := range *files {
		if (*files)[i].Path == "index.md" {
			idxDiff = &(*files)[i]
			break
		}
	}

	var seed []byte
	var oldContent string
	haveIndex := false
	switch {
	case idxDiff != nil:
		seed = []byte(idxDiff.New)
		oldContent = idxDiff.Old
		haveIndex = true
	default:
		if b, err := e.vault.Read("index.md"); err == nil {
			seed = b
			oldContent = string(b)
			haveIndex = true
		}
	}
	if !haveIndex {
		return nil
	}

	derived, err := deriveIndex(seed, creates, e.postImage)
	if err != nil {
		return fmt.Errorf("stage: diff: derive index: %w", err)
	}

	if idxDiff != nil {
		idxDiff.New = string(derived)
		return nil
	}
	if string(derived) == oldContent {
		return nil
	}
	*files = append(*files, FileDiff{
		Path: "index.md", OpID: creates[0].ID, Kind: OpPatchPage,
		Old: oldContent, New: string(derived),
	})
	return nil
}

// fileDiffsForOp returns the FileDiff entries op itself produces, per the
// frozen per-kind table below (backbone §5.6, MASTER §9 D-BS), plus —
// recursively — every entry op's live Cascade sub-ops produce. A Dropped or
// Rejected op (top-level or nested) contributes nothing, mirroring
// apply.go's planOp, which applies the identical skip at every level so
// Commit and Diff agree on what a dropped node means.
//
// Frozen per-kind table (§5.6 D-BS — copied, not re-derived):
//
//	Kind           Entries              Old                          New
//	create_page    1 @ Path             ""                           Store.Get(After)
//	patch_page     1 @ Path             working tree at Path         Store.Get(After)
//	ingest_source  1 @ Path             working tree at Path, else "" Store.Get(SHA256)
//	rename_page    2 — From, then To    From: working tree           From: ""
//	                                    To: ""                       To: Vault.Page(From).Serialize()
//	merge_pages    1 per Sources entry  working tree                 ""
//	split_page     1 @ Path             working tree                 the stub Commit writes (splitStub, D-CB)
//	retract        1 @ Path             working tree                 the tombstone (D-BR)
//	add_link       1 marker @ To        ""                           ""
//
// Every entry carries the PRODUCING op's OpID and Kind. FileDiff.Stale is
// op.State == StateStale.
//
// Decision beyond the table (see this subtask's report): a rename_page's
// From, or one of a merge_pages' Sources, is skipped from the plain
// "source" entry above when that same path also appears as one of the same
// op's own Cascade targets. Measured against the shipped apply.go: two
// merge sources that link to EACH OTHER (spec/fixtures/minimal's kv-cache.md
// and flash-attention.md) both appear as their own cascade rewrite targets,
// and planOp's per-path disposal checks m.writes before m.moves — so the
// source is not tombstoned at all, it is written in place with its
// cascade-rewritten body. Emitting both the naive whole-file deletion and
// the cascade's content-patch for that one path would be a lie either way
// (git apply --check also rejects two conflicting patches against the same
// file), so the cascade entry — which does describe what Commit actually
// writes — is kept, and the deletion entry it would otherwise collide with
// is dropped.
func (e *Engine) fileDiffsForOp(op Op) ([]FileDiff, error) {
	if op.State == StateDropped || op.State == StateRejected {
		return nil, nil
	}
	stale := op.State == StateStale

	var out []FileDiff
	switch op.Kind {
	case OpCreatePage:
		newContent, err := e.postImage(op)
		if err != nil {
			return nil, fmt.Errorf("stage: diff: %s: %w", op.ID, err)
		}
		out = append(out, FileDiff{
			Path: op.Path, OpID: op.ID, Kind: op.Kind,
			Old: "", New: string(newContent), Hunks: op.Hunks, Stale: stale,
		})

	case OpPatchPage:
		newContent, err := e.postImage(op)
		if err != nil {
			return nil, fmt.Errorf("stage: diff: %s: %w", op.ID, err)
		}
		out = append(out, FileDiff{
			Path: op.Path, OpID: op.ID, Kind: op.Kind,
			Old: e.workingTreeContent(op.Path), New: string(newContent),
			Hunks: op.Hunks, Stale: stale,
		})

	case OpIngestSource:
		newContent, err := e.postImage(op)
		if err != nil {
			return nil, fmt.Errorf("stage: diff: %s: %w", op.ID, err)
		}
		out = append(out, FileDiff{
			Path: op.Path, OpID: op.ID, Kind: op.Kind,
			Old: e.workingTreeContent(op.Path), New: string(newContent),
			Hunks: op.Hunks, Stale: stale,
		})

	case OpRenamePage:
		cascaded := cascadePathSet(op)
		if !cascaded[op.From] {
			out = append(out, FileDiff{
				Path: op.From, OpID: op.ID, Kind: op.Kind,
				Old: e.workingTreeContent(op.From), New: "", Stale: stale,
			})
		}
		toContent := ""
		if page, ok := e.vault.Page(op.From); ok {
			toContent = string(page.Serialize())
		}
		out = append(out, FileDiff{
			Path: op.To, OpID: op.ID, Kind: op.Kind,
			Old: "", New: toContent, Stale: stale,
		})

	case OpMergePages:
		cascaded := cascadePathSet(op)
		for _, src := range op.Sources {
			if cascaded[src] {
				continue
			}
			out = append(out, FileDiff{
				Path: src, OpID: op.ID, Kind: op.Kind,
				Old: e.workingTreeContent(src), New: "", Stale: stale,
			})
		}

	case OpSplitPage:
		cascaded := cascadePathSet(op)
		if !cascaded[op.Path] {
			// New is the disambiguation stub Commit will actually leave in
			// place at op.Path (S2-T8 rule (c), D-CB) — never "", which
			// would show the reviewer a whole-file deletion for a path
			// that still exists, page-shaped, after commit. Same call
			// apply.go's planOp and projection.go's applyOp make; the
			// retract arm above renders retractTombstone the same way
			// (D-BR).
			newContent := ""
			if page, ok := e.vault.Page(op.Path); ok {
				newContent = string(splitStub(page, op.Sources, e.now().UTC().Format("2006-01-02")))
			}
			out = append(out, FileDiff{
				Path: op.Path, OpID: op.ID, Kind: op.Kind,
				Old: e.workingTreeContent(op.Path), New: newContent, Stale: stale,
			})
		}

	case OpRetract:
		old, newContent := "", ""
		if page, ok := e.vault.Page(op.Path); ok {
			old = string(page.Serialize())
			newContent = string(retractTombstone(page, op.Rationale, e.now().UTC().Format("2006-01-02")))
		}
		out = append(out, FileDiff{
			Path: op.Path, OpID: op.ID, Kind: op.Kind,
			Old: old, New: newContent, Stale: stale,
		})

	case OpAddLink:
		out = append(out, FileDiff{
			Path: op.To, OpID: op.ID, Kind: op.Kind,
			Old: "", New: "", Stale: stale,
		})
	}

	for _, sub := range op.Cascade {
		subDiffs, err := e.fileDiffsForOp(sub)
		if err != nil {
			return nil, err
		}
		out = append(out, subDiffs...)
	}
	return out, nil
}

// cascadePathSet returns the set of paths op's own live Cascade entries
// target, used to suppress a colliding plain "source" entry (see
// fileDiffsForOp's decision note above). A dropped or rejected cascade sub
// is excluded — it will not be committed either, so it must not suppress
// the source entry describing what actually happens to that path.
func cascadePathSet(op Op) map[string]bool {
	set := make(map[string]bool, len(op.Cascade))
	for _, sub := range op.Cascade {
		if sub.State == StateDropped || sub.State == StateRejected {
			continue
		}
		set[sub.Path] = true
	}
	return set
}

// workingTreeContent returns p's current canonical content in e.vault, or
// "" if p does not exist there.
func (e *Engine) workingTreeContent(p string) string {
	b, ok := canonicalContent(e.vault, p)
	if !ok {
		return ""
	}
	return string(b)
}

// diffRisk ranks an OpKind for Diff.Files ordering (backbone §5.6 Contract,
// /.dev-notes/PLAN-v1.md §14 "new pages and renames first, link additions last"). A
// cascade sub-op is always OpPatchPage, so it sorts into rank 2 regardless
// of which top-level op produced it.
func diffRisk(k OpKind) int {
	switch k {
	case OpCreatePage, OpRenamePage, OpMergePages, OpSplitPage:
		return 1
	case OpPatchPage, OpIngestSource, OpRetract:
		return 2
	case OpAddLink:
		return 3
	default:
		return 4
	}
}

// sortFileDiffs sorts files by risk, tie-broken by Path then OpID, so the
// order is total (backbone §5.6 Contract).
func sortFileDiffs(files []FileDiff) {
	sort.SliceStable(files, func(i, j int) bool {
		ri, rj := diffRisk(files[i].Kind), diffRisk(files[j].Kind)
		if ri != rj {
			return ri < rj
		}
		if files[i].Path != files[j].Path {
			return files[i].Path < files[j].Path
		}
		return files[i].OpID < files[j].OpID
	})
}

// --- unified rendering ------------------------------------------------

// Unified renders the whole Diff as a standard unified diff, 3 lines of
// context, one file at a time in Files order.
//
// Contract (backbone §5.6, MASTER §9 D-BQ): positions and groups hunks with
// ComputeHunks(Old, New) at render time — never FileDiff.Hunks. An entry
// whose Old equals New (an add_link marker) prints nothing.
func (d Diff) Unified() string {
	var b strings.Builder
	for _, fd := range d.Files {
		b.WriteString(renderFileDiffBody(fd))
	}
	return b.String()
}

// UnifiedFile renders only the Files entries whose Path matches path, in
// Files order, or "" when the changeset does not touch path.
func (d Diff) UnifiedFile(path string) string {
	var b strings.Builder
	for _, fd := range d.Files {
		if fd.Path != path {
			continue
		}
		b.WriteString(renderFileDiffBody(fd))
	}
	return b.String()
}

// renderFileDiffBody renders one FileDiff's header and hunks, or "" when
// Old == New.
func renderFileDiffBody(fd FileDiff) string {
	if fd.Old == fd.New {
		return ""
	}
	body, _, _ := diffFile(fd.Old, fd.New)
	return fileHeader(fd.Path, fd.Old, fd.New) + body
}

// fileHeader renders the plain two-line unified-diff header, /dev/null on
// the empty side of a creation or a deletion (backbone §5.6 Contract): no
// "diff --git" line, no "new file mode" — the plain form is what
// git apply --check was verified to accept.
func fileHeader(path, old, new string) string {
	a, b := "a/"+path, "b/"+path
	if old == "" {
		a = "/dev/null"
	}
	if new == "" {
		b = "/dev/null"
	}
	return fmt.Sprintf("--- %s\n+++ %s\n", a, b)
}

// ComputeHunks is a plain line-level LCS diff, 3 lines of context, ids
// h1..hN in file order (backbone §5.6). It receives no path and cannot fill
// Hunk.Path — its caller stamps it (backbone §5.3, D-BH). Before carries
// the hunk's old-side lines (context and removed, in original order) for
// display; Add and Del carry only the inserted and removed lines.
func ComputeHunks(old, new string) []Hunk {
	oldLines, _ := diffSplitLines(old)
	newLines, _ := diffSplitLines(new)
	ops := diffOps(oldLines, newLines)
	windows := hunkWindows(ops, diffContext)

	hunks := make([]Hunk, 0, len(windows))
	for i, w := range windows {
		var before, add, del []string
		for k := w.lo; k <= w.hi; k++ {
			switch ops[k].kind {
			case ' ':
				before = append(before, ops[k].text)
			case '-':
				before = append(before, ops[k].text)
				del = append(del, ops[k].text)
			case '+':
				add = append(add, ops[k].text)
			}
		}
		hunks = append(hunks, Hunk{
			ID:     fmt.Sprintf("h%d", i+1),
			Before: before,
			Add:    add,
			Del:    del,
		})
	}
	return hunks
}

// --- the line-level diff engine ----------------------------------------
//
// diffFile, diffOps, hunkWindows and diffSplitLines are the shared machinery
// behind both ComputeHunks (the exported, position-free Hunk shape) and
// Unified/UnifiedFile (which need full line-position information to render
// "@@ -l,c +l,c @@" headers — information the frozen Hunk struct has no
// field for, D-BQ's Unified Contract note is read as "use the same
// algorithm ComputeHunks uses", not "reconstruct positions by re-parsing
// ComputeHunks' lossy return value"). See this subtask's report.

// diffContext is the number of context lines on each side of a change,
// per backbone §5.6 ("3 lines of context").
const diffContext = 3

// diffOp is one step of a line-level edit script: ' ' (equal, present in
// both), '-' (present only in old) or '+' (present only in new).
type diffOp struct {
	kind byte
	text string
}

// diffSplitLines splits s into lines without its line terminators, and reports
// whether s ends with "\n". An empty s has zero lines — the sentinel this
// package uses throughout for "no such file" (backbone §5.6 D-BS), never a
// legitimately-empty existing file, since every canonical page or raw
// source this package hashes is non-empty.
func diffSplitLines(s string) (lines []string, endsWithNewline bool) {
	if s == "" {
		return nil, true
	}
	endsWithNewline = strings.HasSuffix(s, "\n")
	trimmed := s
	if endsWithNewline {
		trimmed = s[:len(s)-1]
	}
	return strings.Split(trimmed, "\n"), endsWithNewline
}

// diffOps computes a line-level edit script turning oldLines into newLines,
// via a classic LCS table over line equality — a plain LCS diff, per
// backbone §5.6 ("no dependency; the allowlist has no diff library").
func diffOps(oldLines, newLines []string) []diffOp {
	n, m := len(oldLines), len(newLines)

	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case oldLines[i] == newLines[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			ops = append(ops, diffOp{' ', oldLines[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', oldLines[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', oldLines[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', newLines[j]})
	}
	return ops
}

// hunkWindow is a contiguous, inclusive range of indices into a diffOp
// slice, already expanded by context.
type hunkWindow struct {
	lo, hi int
}

// hunkWindows groups ops' non-equal runs into windows expanded by context
// lines on each side, merging runs whose expanded windows would otherwise
// overlap (a gap of at most 2*context+1 between two changed positions).
// nil when ops has no changes.
func hunkWindows(ops []diffOp, context int) []hunkWindow {
	var changed []int
	for i, op := range ops {
		if op.kind != ' ' {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}

	type run struct{ lo, hi int }
	var runs []run
	start, end := changed[0], changed[0]
	for _, idx := range changed[1:] {
		if idx-end <= 2*context+1 {
			end = idx
			continue
		}
		runs = append(runs, run{start, end})
		start, end = idx, idx
	}
	runs = append(runs, run{start, end})

	windows := make([]hunkWindow, len(runs))
	for i, r := range runs {
		lo := r.lo - context
		if lo < 0 {
			lo = 0
		}
		hi := r.hi + context
		if hi > len(ops)-1 {
			hi = len(ops) - 1
		}
		windows[i] = hunkWindow{lo: lo, hi: hi}
	}
	return windows
}

// prefixCounts returns, for every op index 0..len(ops), how many old-side
// and new-side lines precede that index — the running position each side
// of a hunk header is computed from.
func prefixCounts(ops []diffOp) (oldPos, newPos []int) {
	oldPos = make([]int, len(ops)+1)
	newPos = make([]int, len(ops)+1)
	for i, op := range ops {
		oldPos[i+1] = oldPos[i]
		newPos[i+1] = newPos[i]
		switch op.kind {
		case ' ':
			oldPos[i+1]++
			newPos[i+1]++
		case '-':
			oldPos[i+1]++
		case '+':
			newPos[i+1]++
		}
	}
	return oldPos, newPos
}

// diffFile renders the full unified-diff body (every "@@" hunk, no file
// header) turning old into new, plus the added/removed line counts backing
// FileDiff.Added/Removed (backbone §5.6: "count the RENDERED +/- lines").
func diffFile(old, new string) (body string, added, removed int) {
	oldLines, oldNL := diffSplitLines(old)
	newLines, newNL := diffSplitLines(new)
	ops := diffOps(oldLines, newLines)

	for _, op := range ops {
		switch op.kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}

	windows := hunkWindows(ops, diffContext)
	if len(windows) == 0 {
		return "", added, removed
	}

	oldPos, newPos := prefixCounts(ops)
	totalOld, totalNew := oldPos[len(ops)], newPos[len(ops)]

	var b strings.Builder
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
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)

		for k := w.lo; k <= w.hi; k++ {
			op := ops[k]
			b.WriteByte(op.kind)
			b.WriteString(op.text)
			b.WriteByte('\n')

			oldNoNL := op.kind != '+' && oldPos[k]+1 == totalOld && !oldNL
			newNoNL := op.kind != '-' && newPos[k]+1 == totalNew && !newNL
			if oldNoNL || newNoNL {
				b.WriteString("\\ No newline at end of file\n")
			}
		}
	}
	return b.String(), added, removed
}

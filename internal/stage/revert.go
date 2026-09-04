// revert.go implements Engine.Revert (backbone §5.8): diff two consecutive
// snapshots, invert every entry per the backbone's per-kind inversion
// table, and open the result as a new changeset for review — never applied
// directly (/PLAN.md §7). Owned by S2-T6.
package stage

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// revertHistoryPathPattern matches log.md and log-<year>.md — both
// excluded from the revert delta entirely (backbone §5.8, MASTER §9
// D-BV). They are append-only history, and Commit rewrites log.md at step
// 8, after the step-6 snapshot this diff is built from, so an inverse
// built from a snapshot's recorded log.md sha is stale against the working
// tree the instant it would be proposed — and, had it ever committed,
// would have deleted a permanent record.
var revertHistoryPathPattern = regexp.MustCompile(`^log(-\d{4})?\.md$`)

// Revert diffs snapshot(commitID) against snapshot(commitID-1), inverts
// every delta entry per backbone §5.8's per-kind inversion table, and
// opens the result as a new changeset for review.
//
// Contract (backbone §5.8): it never applies anything directly — a revert
// is a proposal like any other (/PLAN.md §7) — and it never takes the
// lock: like OpenChangeset and Append, it writes only under
// changesets/open/ and never touches the working tree (MASTER §9 D-BZ).
// Any path it cannot express as an op (an ingest_source addition, or a
// removed page a create_page's own validation rejects — MASTER §9 D-BY) is
// recorded, never dropped silently, in both the reverted journal event's
// Data field and the changeset's Intent (D-BZ).
func (e *Engine) Revert(commitID string) (*Changeset, error) {
	predID, err := predecessorCommitID(commitID)
	if err != nil {
		return nil, fmt.Errorf("stage: revert %s: %w", commitID, err)
	}
	prev, err := e.Snapshot(predID)
	if err != nil {
		return nil, fmt.Errorf("stage: revert %s: predecessor snapshot %s: %w", commitID, predID, err)
	}
	cur, err := e.Snapshot(commitID)
	if err != nil {
		return nil, fmt.Errorf("stage: revert %s: snapshot %s: %w", commitID, commitID, err)
	}

	added, removed, changed := classifyRevertDelta(prev, cur)
	renames, added, removed := pairRenameInversions(added, removed, cur, prev)

	ops, skipped, err := e.buildRevertOps(commitID, prev, cur, renames, added, removed, changed)
	if err != nil {
		return nil, fmt.Errorf("stage: revert %s: %w", commitID, err)
	}
	sort.Strings(skipped)

	intent := fmt.Sprintf("revert of %s", commitID)
	if len(skipped) > 0 {
		intent = fmt.Sprintf("revert of %s (skipped: %s)", commitID, strings.Join(skipped, ", "))
	}

	c, err := e.OpenChangeset(intent, Author{Kind: "human"})
	if err != nil {
		return nil, fmt.Errorf("stage: revert %s: %w", commitID, err)
	}

	for _, op := range ops {
		if _, err := e.Append(op); err != nil {
			return nil, fmt.Errorf("stage: revert %s: append %s op for %s: %w", commitID, op.Kind, revertOpLabel(op), err)
		}
	}

	ev := Event{
		TS:        e.now().UTC(),
		Kind:      EvReverted,
		Changeset: c.ID,
		Commit:    commitID,
		Actor:     c.Author,
	}
	if len(skipped) > 0 {
		data, err := json.Marshal(struct {
			Skipped []string `json:"skipped"`
		}{Skipped: skipped})
		if err != nil {
			return nil, fmt.Errorf("stage: revert %s: %w", commitID, err)
		}
		ev.Data = json.RawMessage(data)
	}
	if err := e.journal.Append(ev); err != nil {
		return nil, fmt.Errorf("stage: revert %s: %w", commitID, err)
	}

	return c, nil
}

// classifyRevertDelta partitions cur against prev into the ADDED, REMOVED
// and CHANGED sets backbone §5.8's per-kind table classifies, excluding
// every path revertHistoryPathPattern matches before classification even
// starts (D-BV). Every returned slice is sorted — never range a map when
// order affects output (00-conventions.md §3).
func classifyRevertDelta(prev, cur Snapshot) (added, removed, changed []string) {
	for p, sha := range cur {
		if revertHistoryPathPattern.MatchString(p) {
			continue
		}
		if prevSHA, ok := prev[p]; ok {
			if prevSHA != sha {
				changed = append(changed, p)
			}
		} else {
			added = append(added, p)
		}
	}
	for p := range prev {
		if revertHistoryPathPattern.MatchString(p) {
			continue
		}
		if _, ok := cur[p]; !ok {
			removed = append(removed, p)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return added, removed, changed
}

// revertRename is one REMOVED+ADDED pair sharing a sha (backbone §5.8's
// first inversion-table row): from is the path currently live in the
// vault (the reverted commit's ADDED side), to is the path being restored
// (its REMOVED side).
type revertRename struct {
	from, to string
}

// pairRenameInversions pairs every ADDED path with a REMOVED path sharing
// the same canonical sha: a rename_page's content is untouched by the
// move, so its added and removed sides are byte-identical (backbone §5.8,
// MASTER §9 D-BW), which is what makes "share one sha" the correct, and
// only, rename test. Pairing runs positionally over each side's own sorted
// order so the result stays deterministic even in the pathological (and
// untested) case of more than one path sharing a sha within one commit.
// Unpaired remainders are returned separately, still sorted.
func pairRenameInversions(added, removed []string, cur, prev Snapshot) (pairs []revertRename, restAdded, restRemoved []string) {
	addedBySHA := map[string][]string{}
	for _, p := range added {
		addedBySHA[cur[p]] = append(addedBySHA[cur[p]], p)
	}
	removedBySHA := map[string][]string{}
	for _, p := range removed {
		removedBySHA[prev[p]] = append(removedBySHA[prev[p]], p)
	}

	pairedAdded := map[string]bool{}
	pairedRemoved := map[string]bool{}

	var shas []string
	for sha := range addedBySHA {
		if _, ok := removedBySHA[sha]; ok {
			shas = append(shas, sha)
		}
	}
	sort.Strings(shas)

	for _, sha := range shas {
		as, rs := addedBySHA[sha], removedBySHA[sha]
		n := len(as)
		if len(rs) < n {
			n = len(rs)
		}
		for i := 0; i < n; i++ {
			pairs = append(pairs, revertRename{from: as[i], to: rs[i]})
			pairedAdded[as[i]] = true
			pairedRemoved[rs[i]] = true
		}
	}

	for _, p := range added {
		if !pairedAdded[p] {
			restAdded = append(restAdded, p)
		}
	}
	for _, p := range removed {
		if !pairedRemoved[p] {
			restRemoved = append(restRemoved, p)
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].to < pairs[j].to })
	return pairs, restAdded, restRemoved
}

// buildRevertOps builds the concrete inverse Ops for every classified
// delta entry, per backbone §5.8's per-kind inversion table, and separates
// out every path it determines it cannot invert (MASTER §9 D-BY, D-BZ)
// rather than ever dropping one silently.
//
// A create_page, retract or patch_page candidate is dry-run checked with
// ValidateOp before it is queued; a rejection there is a "cannot invert"
// skip, not a hard error — generalizing the two cases backbone §5.8 names
// (an ingest_source addition under raw/, always skipped outright without
// even building a candidate; and a removed page validateCreatePage rejects
// for fewer than 2 outbound links or a path/type it refuses) to any other
// ValidateOp rejection of the same three kinds, rather than hard-coding
// just those two shapes. A rename_page inversion is not dry-run checked
// this way: backbone §5.8 gives Append the job of rebuilding its cascade
// (D-BI, cascadeLinkingPages/buildCascade in op.go), so this function
// trusts that, and any rename failure at the real Append call in Revert is
// a hard error.
//
// Content for a create_page or patch_page candidate is always the CAS blob
// at the predecessor's sha (D-AY) — never the working tree, per the
// backbone's explicit instruction; canonicalContent/e.Vault().Page are
// used only for the CHANGED side's *current* (old) content, which a
// patch_page's Hunks and Before both need to describe what is being
// replaced, not what replaces it.
func (e *Engine) buildRevertOps(commitID string, prev, cur Snapshot, renames []revertRename, added, removed, changed []string) (ops []Op, skipped []string, err error) {
	// REMOVED+ADDED sharing a sha -> one rename_page back (backbone §5.8's
	// first table row). Its own cascade — recomputed by Append itself, not
	// here — will repair index.md, curator-memory.md and every linking
	// page, so cascadeLinkingPages' set is subtracted from the CHANGED loop
	// below to avoid proposing the same rewrite twice under two different
	// ops in one changeset.
	covered := map[string]bool{}
	for _, r := range renames {
		for _, p := range cascadeLinkingPages(e.vault, []string{r.from}) {
			covered[p] = true
		}
		ops = append(ops, Op{Kind: OpRenamePage, From: r.from, To: r.to})
	}

	// CHANGED, path is a vault.Page -> patch_page, Before = the current
	// sha, Content = the CAS blob at the predecessor's sha. CHANGED, path
	// is not a vault.Page and a paired rename's own cascade already covers
	// it (index.md, curator-memory.md, or a linking page the rename
	// rewrote) -> dropped from the delta, since that cascade repairs it for
	// free. CHANGED, path is not a vault.Page and NOTHING covers it — a
	// merge_pages or split_page whose added/removed sides never share a
	// sha, so no rename pairing ever fires (C-58: no op kind can address a
	// vault-root file such as index.md at all) -> skipped, not dropped:
	// repair-1 fix. The un-fixed code silently dropped this case too,
	// which is exactly what MASTER §9 D-BY/D-BZ forbid — a review surface
	// that misdescribes what will land is the one defect /PLAN.md §1
	// cannot ship. TestRevertMergeReportsUncoveredPaths pins this and was
	// proved to fail against the pre-fix code.
	for _, p := range changed {
		if covered[p] {
			continue
		}
		page, ok := e.vault.Page(p)
		if !ok {
			skipped = append(skipped, p)
			continue
		}
		content, gerr := e.store.Get(prev[p])
		if gerr != nil {
			return nil, nil, fmt.Errorf("read predecessor content for %s: %w", p, gerr)
		}
		hunks := ComputeHunks(string(page.Serialize()), string(content))
		for i := range hunks {
			hunks[i].Path = p
		}
		op := Op{
			Kind:    OpPatchPage,
			Path:    p,
			Before:  cur[p],
			Content: content,
			Hunks:   hunks,
		}
		if verr := ValidateOp(op, e.vault, e.vault.Schema()); verr != nil {
			skipped = append(skipped, p)
			continue
		}
		ops = append(ops, op)
	}

	// REMOVED, unpaired -> create_page, Content = the CAS blob at the
	// predecessor's sha.
	for _, p := range removed {
		content, gerr := e.store.Get(prev[p])
		if gerr != nil {
			return nil, nil, fmt.Errorf("read predecessor content for %s: %w", p, gerr)
		}
		op := Op{
			Kind:       OpCreatePage,
			Path:       p,
			Rationale:  fmt.Sprintf("revert of commit %s", commitID),
			Provenance: provenanceForRevert(p, content),
			Content:    content,
		}
		if verr := ValidateOp(op, e.vault, e.vault.Schema()); verr != nil {
			skipped = append(skipped, p)
			continue
		}
		ops = append(ops, op)
	}

	// ADDED, unpaired: under wiki/ -> retract. Under raw/ -> not
	// invertible at all (a RawSource is not a vault.Page, so both retract
	// and patch_page reject it outright) — reported, never attempted
	// (MASTER §9 D-BY).
	for _, p := range added {
		if strings.HasPrefix(p, "raw/") {
			skipped = append(skipped, p)
			continue
		}
		op := Op{
			Kind:      OpRetract,
			Path:      p,
			Rationale: fmt.Sprintf("revert of commit %s", commitID),
		}
		if verr := ValidateOp(op, e.vault, e.vault.Schema()); verr != nil {
			skipped = append(skipped, p)
			continue
		}
		ops = append(ops, op)
	}

	return ops, skipped, nil
}

// provenanceForRevert returns the Provenance a revert's create_page
// inversion carries: the restored page's own declared sources when it had
// any — the closest available record of what informed it — else the
// page's own path. Both are already well-formed vaultPath strings
// (backbone §5.5's schema requirement, MASTER §9 D-BH: a create_page's
// provenance entries must match the vault-path pattern), since a
// frontmatter sources: entry and a page's own path are both
// vault-relative "*.md" strings.
func provenanceForRevert(p string, content []byte) []string {
	page, err := vault.ParsePage(p, content)
	if err != nil || len(page.FM.Sources) == 0 {
		return []string{p}
	}
	return append([]string(nil), page.FM.Sources...)
}

// revertOpLabel names op for an error message: its Path, or its
// From->To pair for a rename, which carries neither.
func revertOpLabel(op Op) string {
	if op.Path != "" {
		return op.Path
	}
	return op.From + " -> " + op.To
}

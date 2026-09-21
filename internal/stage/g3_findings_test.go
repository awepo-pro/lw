// g3_findings_test.go pins the three behaviour fixes of workflow 020's fix
// wave 3a, taken from the G3 whole-branch review (runs/G3-review.md):
//
//   - Finding 1 — a dropped cascade sub-op must leave EVERY surface: the
//     projection (applyOp) and StagedFile's scan skipped nothing, while
//     Commit's planOp and diff's fileDiffsForOp dropped it, so a reviewer's
//     drop could land baked into a dependent patch and the pre-commit lint
//     gate described a tree Commit would not write.
//   - Finding 3 — StateStale is DERIVED: refreshOp only ever flipped TO
//     stale, so a transient drop→undrop of a middle hunk bricked the chain
//     tail forever (amendment A20-1 to backbone §5.4 D-AJ).
//   - Finding 4 — a rename/merge whose source carries a live content op is
//     refused at Append, because planOp's rename reads the COMMITTED page
//     and the landed tree would contradict the diff in both directions.
//
// Every test here was written BEFORE the fixes and proved to fail against
// the unchanged branch (fail-first); the red state is captured in
// runs/FIX-3a/report.md.
package stage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// flashPath is the minimal-fixture page whose only relevant property is
// that it wikilinks [[kv-cache]] — so a rename of kv-cache gives it an
// engine-built cascade rewrite sub-op, the sub-op every F1 assertion
// drops.
const flashPath = "wiki/concepts/flash-attention.md"

// cascadeSubFor returns a COPY of the live cascade sub-op targeting path,
// searching every top-level op's Cascade, and whether one was found.
func cascadeSubFor(c *Changeset, path string) (Op, bool) {
	for i := range c.Ops {
		for j := range c.Ops[i].Cascade {
			if c.Ops[i].Cascade[j].Path == path {
				return c.Ops[i].Cascade[j], true
			}
		}
	}
	return Op{}, false
}

// TestDroppedCascadeSubOpLeavesEverySurface pins G3 finding 1: once the
// reviewer drops a cascade rewrite sub-op, no surface may still apply or
// serve it — StagedFile must stop resolving the path, the projected tree
// must keep the committed content, and a dependent patch chained on the
// dropped sub's After must be refused at Append (its Before no longer
// describes anything the projection holds). Before the fix the projection
// and StagedFile applied the dropped rewrite while planOp and the diff
// skipped it — the divergence that let a dropped rewrite land inside a
// dependent patch with no hunk describing it.
func TestDroppedCascadeSubOpLeavesEverySurface(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("rename with cascade", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:      OpRenamePage,
		From:      "wiki/concepts/kv-cache.md",
		To:        "wiki/concepts/kv-cache-v2.md",
		Rationale: "versioned name",
	}); err != nil {
		t.Fatalf("Append rename: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	sub, ok := cascadeSubFor(c, flashPath)
	if !ok {
		t.Fatalf("fixture: the kv-cache rename built no cascade sub-op for %s", flashPath)
	}
	rewrite, err := e.store.Get(sub.After)
	if err != nil {
		t.Fatalf("store.Get(sub.After): %v", err)
	}
	if !bytes.Contains(rewrite, []byte("[[kv-cache-v2]]")) {
		t.Fatalf("precondition: the sub-op's post-image is not the rewrite:\n%s", rewrite)
	}
	subID, subAfter := sub.ID, sub.After

	if err := e.DropOp(subID); err != nil {
		t.Fatalf("DropOp(%s): %v", subID, err)
	}

	// Surface 1 — the tool channel: StagedFile resolves no live op for the
	// path any more, so a stage.patch_page computed from it starts from the
	// committed page instead of the dropped rewrite.
	got, found, err := e.StagedFile(flashPath)
	if err != nil {
		t.Fatalf("StagedFile after drop: %v", err)
	}
	if found {
		t.Fatalf("StagedFile still serves the dropped sub-op's rewrite:\n%s", got)
	}
	if got != nil {
		t.Fatalf("StagedFile bytes = %v after the drop, want nil", got)
	}

	// Surface 2 — the projection every Check and ProjectedReport describes:
	// the rewrite is not applied, the committed link stands.
	c, err = e.Current()
	if err != nil {
		t.Fatalf("Current after drop: %v", err)
	}
	tree, err := e.projectedTree(c.Live())
	if err != nil {
		t.Fatalf("projectedTree: %v", err)
	}
	projected, ok := tree[flashPath]
	if !ok {
		t.Fatalf("projected tree lost %s entirely; want the committed content", flashPath)
	}
	if bytes.Contains(projected, []byte("[[kv-cache-v2]]")) {
		t.Fatalf("projected tree still applies the dropped rewrite:\n%s", projected)
	}
	if !bytes.Contains(projected, []byte("[[kv-cache]]")) {
		t.Fatalf("projected tree does not carry the committed link:\n%s", projected)
	}

	// Surface 3 — the chain: a dependent whose Before is the dropped sub's
	// After is refused at Append. Accepting it would re-anchor the agent on
	// bytes no live op produces and smuggle the dropped rewrite back in.
	dep, err := vault.ParsePage(flashPath, rewrite)
	if err != nil {
		t.Fatalf("parse rewrite bytes: %v", err)
	}
	dep.Body += "\nAgent note on the rewritten body.\n"
	depContent := dep.Serialize()
	_, err = e.Append(Op{
		Kind:    OpPatchPage,
		Path:    flashPath,
		Before:  subAfter,
		Content: depContent,
		Hunks: []Hunk{{
			ID:   "h1",
			Path: flashPath,
			Add:  []string{"Agent note on the rewritten body."},
		}},
	})
	if err == nil {
		t.Fatal("Append accepted a dependent chained on a dropped cascade sub-op; want refusal")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Append error = %v; want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), "before does not match") {
		t.Errorf("refusal does not name the before mismatch: %v", err)
	}
}

// refineAlpha returns the post-image of a one-line patch to the ## Alpha
// body over the ALREADY-COMPOSED staged bytes — patch3 of the F3 chain,
// whose anchor is patch2's stored After.
func refineAlpha(t *testing.T, base []byte) (Op, []byte) {
	t.Helper()
	page, err := vault.ParsePage(chainedTargetPath, base)
	if err != nil {
		t.Fatalf("parse staged body: %v", err)
	}
	refined := *page
	refined.Body = strings.Replace(page.Body, "Alpha body.", "Alpha body, extended.", 1)
	content := refined.Serialize()
	return Op{
		Kind:    OpPatchPage,
		Path:    chainedTargetPath,
		Section: "## Alpha",
		Content: content,
		Hunks: []Hunk{{
			ID:      "h1",
			Path:    chainedTargetPath,
			Section: "## Alpha",
			Del:     []string{"Alpha body."},
			Add:     []string{"Alpha body, extended."},
		}},
	}, content
}

// relinkIntro returns the post-image of a one-line replace of the intro
// sentence over base — patch2 of the F3 chain. The hunk carries the exact
// Del and Add lines, so applyHunks's drop→undrop recompute reproduces
// these bytes byte-for-byte and the tail's anchor can re-match on undrop
// (an Add-only hunk is re-applied at section END, which would re-home the
// insertion and keep the anchor mismatched).
func relinkIntro(t *testing.T, base []byte) (Op, []byte) {
	t.Helper()
	page, err := vault.ParsePage(chainedTargetPath, base)
	if err != nil {
		t.Fatalf("parse staged body: %v", err)
	}
	const oldLine = "Intro sentence linking [[kv-cache]] and [[gpt-4]]."
	newLine := "Intro sentence linking [[kv-cache]] and [[gpt-4]], retitled."
	if !strings.Contains(page.Body, oldLine) {
		t.Fatalf("staged body does not contain the intro line:\n%s", page.Body)
	}
	retitled := *page
	retitled.Body = strings.Replace(page.Body, oldLine, newLine, 1)
	content := retitled.Serialize()
	return Op{
		Kind:    OpPatchPage,
		Path:    chainedTargetPath,
		Content: content,
		Hunks: []Hunk{{
			ID:   "h1",
			Path: chainedTargetPath,
			Del:  []string{oldLine},
			Add:  []string{newLine},
		}},
	}, content
}

// TestStaleClearsWhenAnchorReMatches pins G3 finding 3: staleness is a
// DERIVED state, recomputed from the anchor on every mutation verb — not a
// one-way trap. A reviewer's transient drop of a middle hunk flips the
// chain tail StateStale (correct, T-A); undropping it recomputes the middle
// op's After to the byte-identical sha, so the tail's anchor matches again
// and the tail must return to StateProposed in the same UndropHunk verb —
// no Refresh call, and the commit proceeds to land the composed chain.
// Before the fix nothing ever cleared StateStale, so the tail stayed
// bricked: commit refused indefinitely, the only escape discarding the
// reviewed edit.
func TestStaleClearsWhenAnchorReMatches(t *testing.T) {
	e, dir := newChainedEngine(t)
	if _, err := e.OpenChangeset("three-op chain", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	// The 3-op chain: remove Beta → retitle the intro line → refine the
	// Alpha body, each chained on its predecessor's stored After.
	patch1, p1Content := removeBeta(t, e)
	op1, err := e.Append(patch1)
	if err != nil {
		t.Fatalf("Append patch1: %v", err)
	}
	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current after patch1: %v", err)
	}
	live1, _ := c.Op(op1)
	patch2, p2Content := relinkIntro(t, p1Content)
	patch2.Before = live1.After
	op2, err := e.Append(patch2)
	if err != nil {
		t.Fatalf("Append patch2: %v", err)
	}
	c, err = e.Current()
	if err != nil {
		t.Fatalf("Current after patch2: %v", err)
	}
	live2, _ := c.Op(op2)
	patch3, p3Content := refineAlpha(t, p2Content)
	patch3.Before = live2.After
	op3, err := e.Append(patch3)
	if err != nil {
		t.Fatalf("Append patch3: %v", err)
	}

	// Reviewer drops the middle op's hunk: the tail flips stale in the same
	// verb (T-A, already pinned) and Commit would refuse.
	if err := e.DropHunk(op2, "h1"); err != nil {
		t.Fatalf("DropHunk(%s, h1): %v", op2, err)
	}
	c, err = e.Current()
	if err != nil {
		t.Fatalf("Current after DropHunk: %v", err)
	}
	tail, _ := c.Op(op3)
	if tail.State != StateStale {
		t.Fatalf("tail %s state after the middle hunk drop = %q; want %q", op3, tail.State, StateStale)
	}

	// Reviewer changes their mind: the middle op's After recomputes to the
	// byte-identical sha, the tail's anchor matches again — and with no
	// Refresh call the tail must be fresh again, not stale forever.
	if err := e.UndropHunk(op2, "h1"); err != nil {
		t.Fatalf("UndropHunk(%s, h1): %v", op2, err)
	}
	c, err = e.Current()
	if err != nil {
		t.Fatalf("Current after UndropHunk: %v", err)
	}
	tail, _ = c.Op(op3)
	if tail.State != StateProposed {
		t.Fatalf("tail %s state after the undrop = %q; want %q — a transient drop must not brick the chain", op3, tail.State, StateProposed)
	}

	// The reviewed chain commits and lands the full composition.
	if _, err := e.Commit("remove Beta, insert Gamma, refine Alpha"); err != nil {
		t.Fatalf("Commit after undrop: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(chainedTargetPath)))
	if err != nil {
		t.Fatalf("read committed page: %v", err)
	}
	if !bytes.Equal(onDisk, p3Content) {
		t.Fatalf("landed bytes are not the composed chain:\n%s", onDisk)
	}
	body := string(onDisk)
	if strings.Contains(body, "## Beta") {
		t.Errorf("committed page still carries Beta:\n%s", body)
	}
	if !strings.Contains(body, "retitled.") {
		t.Errorf("committed page lost the middle op's edit:\n%s", body)
	}
	if !strings.Contains(body, "Alpha body, extended.") {
		t.Errorf("committed page lost the tail op's edit:\n%s", body)
	}
}

// TestAppendRefusesRenameWithLiveContentOpOnSource pins G3 finding 4: a
// rename (or merge) whose source page already carries a LIVE content op in
// the same changeset is refused at Append. planOp materializes a rename
// from the COMMITTED page, so accepting the rename would land a tree that
// contradicts the diff in both directions — the patched page the diff
// showed renamed stays behind with its patch, and the new page holds
// unpatched content. The refusal names the blocking op id and says what to
// do; the control proves a rename whose source carries no content op still
// appends in the very same changeset.
func TestAppendRefusesRenameWithLiveContentOpOnSource(t *testing.T) {
	e, _ := newChainedEngine(t)
	if _, err := e.OpenChangeset("patch then rename", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	patch1, _ := removeBeta(t, e)
	op1, err := e.Append(patch1)
	if err != nil {
		t.Fatalf("Append patch1: %v", err)
	}

	_, err = e.Append(Op{
		Kind:      OpRenamePage,
		From:      chainedTargetPath,
		To:        "wiki/concepts/chain-target-v2.md",
		Rationale: "renamed after a patch",
	})
	if err == nil {
		t.Fatal("Append accepted a rename whose source carries a live content op; want refusal")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Append error = %v; want ErrValidation", err)
	}
	for _, want := range []string{op1, "commit", "drop"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}

	// Control: the same changeset, a rename whose source no content op
	// touches — accepted exactly as before.
	if _, err := e.Append(Op{
		Kind:      OpRenamePage,
		From:      "wiki/entities/gpt-4.md",
		To:        "wiki/entities/gpt-4-v2.md",
		Rationale: "control: untouched source",
	}); err != nil {
		t.Fatalf("Append control rename: %v", err)
	}
}

// TestAppendRefusesRenameWhoseSourceHasLiveCascadeSubOp pins finding 4's
// cascade half: a rename's engine-built rewrite sub-ops are live content
// ops too, so a second rename whose SOURCE is one of those sub-op targets
// is refused for the same commit-side reason — the sub writes the path in
// place while the second rename moves it away.
func TestAppendRefusesRenameWhoseSourceHasLiveCascadeSubOp(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("rename then rename a rewritten neighbour", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:      OpRenamePage,
		From:      "wiki/concepts/kv-cache.md",
		To:        "wiki/concepts/kv-cache-v2.md",
		Rationale: "versioned name",
	}); err != nil {
		t.Fatalf("Append rename: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	sub, ok := cascadeSubFor(c, flashPath)
	if !ok {
		t.Fatalf("fixture: no cascade sub-op for %s", flashPath)
	}

	_, err = e.Append(Op{
		Kind:      OpRenamePage,
		From:      flashPath,
		To:        "wiki/concepts/flash-attention-v2.md",
		Rationale: "renaming a cascade-rewritten page",
	})
	if err == nil {
		t.Fatal("Append accepted a rename whose source is a live cascade sub-op target; want refusal")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Append error = %v; want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), sub.ID) {
		t.Errorf("refusal does not name the blocking sub-op %s: %v", sub.ID, err)
	}
}

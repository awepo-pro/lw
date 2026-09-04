// or13_cascade_projection_test.go pins MASTER §10 OR-13, the repair that
// closes OQ-10: a cascade is built against the tree its op will actually
// apply to — the changeset's preceding live ops projected over the working
// tree — instead of against the working tree itself.
//
// Orchestrator-owned, like force_test.go (OR-12). The repair spans op.go
// and engine_changeset.go, both S2-T2's files, so MASTER §8 rule 3 reserves
// it; these tests are what stop a later refactor silently re-tightening it.
//
// Every test here was proved to FAIL against the pre-OR-13 code before it
// was accepted.
package stage

import (
	"strings"
	"testing"
)

// TestTwoRenamesInOneChangesetKeepBothRewrites is OQ-10 itself. Each
// rename's cascade used to be built from the on-disk vault and carry a
// whole-file post-image; planOp and applyOp are both last-write-wins, so
// the second cascade silently reverted the first one's link rewrite.
//
// In spec/fixtures/minimal, index.md, wiki/concepts/kv-cache.md and
// wiki/entities/gpt-4.md all link to BOTH renamed pages, so all three lose
// a rewrite without the fix. Measured pre-fix: 4 lint errors and 1 warn,
// two of them link-broken on real content pages — worse than the "2
// index-sync errors" OQ-10 was recorded with.
func TestTwoRenamesInOneChangesetKeepBothRewrites(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("two renames", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	const (
		fromA = "wiki/concepts/flash-attention.md"
		toA   = "wiki/concepts/flash-attention-v2.md"
		fromB = "wiki/concepts/speculative-decoding.md"
		toB   = "wiki/concepts/speculative-decoding-v2.md"
	)
	if _, err := e.Append(Op{Kind: OpRenamePage, From: fromA, To: toA}); err != nil {
		t.Fatalf("Append rename A: %v", err)
	}
	if _, err := e.Append(Op{Kind: OpRenamePage, From: fromB, To: toB}); err != nil {
		t.Fatalf("Append rename B: %v", err)
	}
	if _, err := e.Commit("two renames"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	for _, path := range []string{
		"index.md",
		"wiki/concepts/kv-cache.md",
		"wiki/entities/gpt-4.md",
	} {
		raw, err := e.vault.Read(path)
		if err != nil {
			t.Errorf("%s: read: %v", path, err)
			continue
		}
		body := string(raw)
		for _, want := range []string{"flash-attention-v2", "speculative-decoding-v2"} {
			if !strings.Contains(body, "[["+want+"]]") {
				t.Errorf("%s: missing [[%s]] — a cascade clobbered the other rename's rewrite", path, want)
			}
		}
		for _, stale := range []string{"[[flash-attention]]", "[[speculative-decoding]]"} {
			if strings.Contains(body, stale) {
				t.Errorf("%s: stale link %s survived the commit", path, stale)
			}
		}
	}

	if rep := lintProjection(e.vault); rep.Errors != 0 || rep.Warns != 0 {
		t.Errorf("post-commit lint = %d errors, %d warns; want 0/0", rep.Errors, rep.Warns)
		for _, f := range rep.Findings {
			t.Logf("  [%s] %s %s: %s", f.Severity, f.Check, f.Path, f.Message)
		}
	}
}

// TestCascadeDoesNotClobberAnEarlierPatch is the everyday shape of the same
// defect, and the one that mattered most: a patch_page followed by a
// rename_page whose cascade happens to cover the patched page. The cascade
// post-image was computed from disk, without the patch, and landed second —
// so the patch was silently discarded with no error, no warning and no
// skipped report.
//
// TestDiffGolden's checked-in golden encoded exactly that loss before
// OR-13, which is why it had to be regenerated (see the run record).
func TestCascadeDoesNotClobberAnEarlierPatch(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("patch then rename", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	// flash-attention.md links to [[kv-cache]], so renaming kv-cache.md
	// cascades into it.
	const patched = "wiki/concepts/flash-attention.md"
	page, ok := e.Vault().Page(patched)
	if !ok {
		t.Fatalf("fixture missing %s", patched)
	}
	const (
		oldLine = "- Reduces memory-bandwidth traffic, which dominates attention's runtime cost."
		newLine = "- Reduces memory-bandwidth traffic, the dominant cost of naive attention."
	)
	if !bodyContains(page.Body, oldLine) {
		t.Fatalf("fixture body does not contain %q", oldLine)
	}
	rewritten := *page
	rewritten.Body = replaceLine(page.Body, oldLine, newLine)
	if _, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    patched,
		Section: "## Why it matters",
		Before:  page.SHA256(),
		Content: rewritten.Serialize(),
		Hunks:   []Hunk{{ID: "h1", Path: patched, Del: []string{oldLine}, Add: []string{newLine}}},
	}); err != nil {
		t.Fatalf("Append patch_page: %v", err)
	}

	if _, err := e.Append(Op{
		Kind: OpRenamePage,
		From: "wiki/concepts/kv-cache.md",
		To:   "wiki/concepts/kv-cache-v2.md",
	}); err != nil {
		t.Fatalf("Append rename_page: %v", err)
	}

	if _, err := e.Commit("patch then rename"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	raw, err := e.vault.Read(patched)
	if err != nil {
		t.Fatalf("read %s: %v", patched, err)
	}
	body := string(raw)
	if !strings.Contains(body, newLine) {
		t.Errorf("%s: the patch_page edit was lost — the rename's cascade clobbered it", patched)
	}
	if strings.Contains(body, oldLine) {
		t.Errorf("%s: the pre-patch line is still present", patched)
	}
	if !strings.Contains(body, "[[kv-cache-v2]]") {
		t.Errorf("%s: the rename cascade did not land", patched)
	}
}

// TestCascadeReachesAPageCreatedInTheSameChangeset is the third distinct
// instance of the same root cause, and the one TestDiffGolden's checked-in
// golden had been carrying in plain sight: a page created by an earlier op
// does not exist in the working tree, so a later rename's cascade — built
// from that tree — could not see it and never rewrote its links. The new
// page committed pointing at a path the same changeset had just renamed
// away.
//
// The pre-OR-13 golden ends with "See [[kv-cache]] and [[gpt-4]]" and no
// later block touching it, in a changeset that renames kv-cache.md.
func TestCascadeReachesAPageCreatedInTheSameChangeset(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("create then rename", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	// newConceptPageContent links to [[kv-cache]] and [[gpt-4]].
	const created = "wiki/concepts/or13-new-page.md"
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       created,
		Content:    newConceptPageContent("OR13 New Page"),
		Rationale:  "regression fixture",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append create_page: %v", err)
	}
	if _, err := e.Append(Op{
		Kind: OpRenamePage,
		From: "wiki/concepts/kv-cache.md",
		To:   "wiki/concepts/kv-cache-v2.md",
	}); err != nil {
		t.Fatalf("Append rename_page: %v", err)
	}
	if _, err := e.Commit("create then rename"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	raw, err := e.vault.Read(created)
	if err != nil {
		t.Fatalf("read %s: %v", created, err)
	}
	body := string(raw)
	if strings.Contains(body, "[[kv-cache]]") {
		t.Errorf("%s: still links to the renamed-away [[kv-cache]]", created)
	}
	if !strings.Contains(body, "[[kv-cache-v2]]") {
		t.Errorf("%s: the cascade never reached a page created in the same changeset", created)
	}

	if rep := lintProjection(e.vault); rep.Errors != 0 {
		t.Errorf("post-commit lint = %d errors; want 0", rep.Errors)
		for _, f := range rep.Findings {
			t.Logf("  [%s] %s %s: %s", f.Severity, f.Check, f.Path, f.Message)
		}
	}
}

// TestDroppingABaseOpMarksDependentCascadeStale covers the cost of building
// a cascade on a projection: the later op's post-image encodes the earlier
// op's effect, so dropping the earlier op invalidates it. Nothing may
// silently reintroduce a dropped op's rewrite, so the dependent cascade
// sub-ops must go StateStale — which hasStaleOp turns into a refused
// commit, exactly as an externally edited working tree does.
func TestDroppingABaseOpMarksDependentCascadeStale(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("drop the base", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	first, err := e.Append(Op{
		Kind: OpRenamePage,
		From: "wiki/concepts/flash-attention.md",
		To:   "wiki/concepts/flash-attention-v2.md",
	})
	if err != nil {
		t.Fatalf("Append rename A: %v", err)
	}
	if _, err := e.Append(Op{
		Kind: OpRenamePage,
		From: "wiki/concepts/speculative-decoding.md",
		To:   "wiki/concepts/speculative-decoding-v2.md",
	}); err != nil {
		t.Fatalf("Append rename B: %v", err)
	}

	if err := e.DropOp(first); err != nil {
		t.Fatalf("DropOp: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if !hasStaleOp(c.Ops) {
		t.Fatal("dropping the base op left the dependent cascade non-stale; its post-image still carries the dropped rename's rewrite")
	}
	if _, err := e.Commit("should refuse"); err == nil {
		t.Fatal("Commit succeeded on a changeset whose cascade is stale; want refusal")
	}
}

// TestRenameSourceMustExistInWorkingTree pins the boundary OR-13 chose not
// to cross. Validating a cascade-bearing op against the projection would
// also make chaining legal — renaming the OUTPUT of an earlier rename —
// and Commit materializes a rename from the working tree, so such a source
// has no pre-image to move. It stays refused, and says why.
func TestRenameSourceMustExistInWorkingTree(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("chained rename", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	const mid = "wiki/concepts/flash-attention-v2.md"
	if _, err := e.Append(Op{
		Kind: OpRenamePage,
		From: "wiki/concepts/flash-attention.md",
		To:   mid,
	}); err != nil {
		t.Fatalf("Append rename A: %v", err)
	}

	_, err := e.Append(Op{
		Kind: OpRenamePage,
		From: mid,
		To:   "wiki/concepts/flash-attention-v3.md",
	})
	if err == nil {
		t.Fatal("Append accepted a rename chained off an uncommitted rename; want refusal")
	}
	if !strings.Contains(err.Error(), "working tree") {
		t.Errorf("error does not explain the working-tree rule: %v", err)
	}
}

// TestFirstOpCascadeUsesTheWorkingTree pins the compatibility property the
// repair rests on: with no preceding live op, cascadeBase returns e.vault
// itself, so a single-op changeset takes byte-for-byte the pre-OR-13 path.
// This is what makes every shipped single-rename test still meaningful.
func TestFirstOpCascadeUsesTheWorkingTree(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("one rename", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	base, err := e.cascadeBase(nil)
	if err != nil {
		t.Fatalf("cascadeBase(nil): %v", err)
	}
	if base != e.vault {
		t.Error("cascadeBase(nil) returned a copy; it must return e.vault itself so the common path is unchanged")
	}

	if _, err := e.Append(Op{
		Kind: OpRenamePage,
		From: "wiki/concepts/kv-cache.md",
		To:   "wiki/concepts/kv-cache-v2.md",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	for _, sub := range c.Ops[0].Cascade {
		want, ok := canonicalSHA(e.vault, sub.Path)
		if !ok {
			t.Errorf("%s: no canonical sha in the working tree", sub.Path)
			continue
		}
		if sub.Before != want {
			t.Errorf("%s: cascade Before = %s, want the working-tree sha %s", sub.Path, sub.Before, want)
		}
	}
}

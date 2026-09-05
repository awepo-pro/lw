package stage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestPatchRootFileLandsAndReverts pins OQ-9 phase 1's headline claim: a
// patch_page op may target curator-memory.md, lands byte-exactly through
// Commit, and Revert restores the pre-patch bytes exactly (backbone §5.8,
// OQ-9 §9 phase 1 table: "curator-memory.md becomes restorable from the
// CAS").
func TestPatchRootFileLandsAndReverts(t *testing.T) {
	e, dir := newTestEngine(t)

	const path = "curator-memory.md"
	origContent, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	before, ok := canonicalSHA(e.Vault(), path)
	if !ok {
		t.Fatalf("canonicalSHA(%s): not found", path)
	}
	newContent := append(append([]byte(nil), origContent...), []byte("\n## Review\n- Curator patches land through hunk review too.\n")...)

	if _, err := e.OpenChangeset("patch curator memory", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	opID, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    path,
		Before:  before,
		Content: newContent,
	})
	if err != nil {
		t.Fatalf("Append(patch_page %s): %v", path, err)
	}

	commitID, err := e.Commit("patch curator memory")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read %s after commit: %v", path, err)
	}
	if string(got) != string(newContent) {
		t.Fatalf("%s after commit = %q, want %q", path, got, newContent)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}
	if skipped := lastRevertedSkipped(t, e); containsString(skipped, path) {
		t.Fatalf("%s reported as skipped; OQ-9 phase 1 makes it restorable from the CAS, not skipped: %v", path, skipped)
	}

	var revertOp *Op
	for i := range revertCS.Ops {
		if revertCS.Ops[i].Path == path {
			revertOp = &revertCS.Ops[i]
		}
	}
	if revertOp == nil {
		t.Fatalf("revert changeset carries no op for %s: %+v", path, revertCS.Ops)
	}
	if revertOp.Kind != OpPatchPage {
		t.Fatalf("revert op kind = %q, want %q", revertOp.Kind, OpPatchPage)
	}

	if _, err := e.Commit("revert " + commitID); err != nil {
		t.Fatalf("Commit (revert): %v", err)
	}

	final, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read %s after revert commit: %v", path, err)
	}
	if string(final) != string(origContent) {
		t.Fatalf("%s after revert = %q, want the original %q (byte-exact round trip)", path, final, origContent)
	}

	_ = opID
}

// TestPatchRootFileRefusesLogAndSchema pins OQ-9's L1: log.md and
// SCHEMA.md are refused with a message saying why, never a bare "does
// not exist" (OQ-9 §9 phase 1 table).
//
// log.md reaches validatePatchPage's non-Page branch through the public
// ValidateOp entry point exactly like any other patch_page, since its
// name is already a well-formed lowercase-hyphen.md path — it is tested
// through ValidateOp end to end.
//
// SCHEMA.md does NOT reach that branch through ValidateOp: ValidateOp's
// own generic path-shape gate (requireValidPath, the code above the
// per-kind switch — not part of validatePatchPage, and not owned by this
// subtask, whose file list is scoped to "the non-Page branch of
// validatePatchPage only") rejects "SCHEMA.md" first, because
// vaultPathFilenameRE requires lowercase and SCHEMA.md is not. That gate
// refuses it with an unrelated "not a vault-relative... path" message —
// still refused, and still not a bare "does not exist", but not the
// OQ-9-specific reason either. This is flagged in
// runs/S4-T0-rootfile-patch/report.md as a structural conflict between
// the OQ-9 spec and the existing, out-of-scope path-shape gate.
// validatePatchPage is therefore also exercised DIRECTLY here (same
// package, unexported call), which is the actual unit this subtask owns
// and changes, to prove its SCHEMA.md-specific message is correct and
// ready should that outer gate ever change.
func TestPatchRootFileRefusesLogAndSchema(t *testing.T) {
	t.Run("log.md via ValidateOp", func(t *testing.T) {
		v := newTestVault(t)
		before, ok := canonicalSHA(v, "log.md")
		if !ok {
			t.Fatal("canonicalSHA(log.md): not found")
		}
		op := Op{Kind: OpPatchPage, Path: "log.md", Before: before, Content: []byte("tampered\n")}
		err := ValidateOp(op, v, v.Schema())
		wantValidationError(t, err)
		if strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("log.md refusal = %q, must not be a bare \"does not exist\"", err)
		}
		if !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("log.md refusal = %q, want it to say why (append-only / rotates)", err)
		}
	})

	t.Run("SCHEMA.md via ValidateOp is still refused, just not by this subtask's message", func(t *testing.T) {
		v := newTestVault(t)
		before, ok := canonicalSHA(v, "SCHEMA.md")
		if !ok {
			t.Fatal("canonicalSHA(SCHEMA.md): not found")
		}
		op := Op{Kind: OpPatchPage, Path: "SCHEMA.md", Before: before, Content: []byte("tampered\n")}
		err := ValidateOp(op, v, v.Schema())
		wantValidationError(t, err)
		if strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("SCHEMA.md refusal = %q, must not be a bare \"does not exist\"", err)
		}
	})

	t.Run("SCHEMA.md via validatePatchPage directly names the reason", func(t *testing.T) {
		v := newTestVault(t)
		before, ok := canonicalSHA(v, "SCHEMA.md")
		if !ok {
			t.Fatal("canonicalSHA(SCHEMA.md): not found")
		}
		op := Op{Kind: OpPatchPage, Path: "SCHEMA.md", Before: before, Content: []byte("tampered\n")}
		err := validatePatchPage(op, v, v.Schema())
		wantValidationError(t, err)
		if strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("SCHEMA.md refusal = %q, must not be a bare \"does not exist\"", err)
		}
		if !strings.Contains(err.Error(), "rules") {
			t.Fatalf("SCHEMA.md refusal = %q, want it to say why (it is the rules the validator reads)", err)
		}
	})
}

// TestPatchRootFileRefusesIndexInPhase1 pins OQ-9 phase 1's scope: index.md
// is refused with a message saying it is not YET enabled, not that it is
// forbidden forever — S4-T7 adds it after the review screen ships.
func TestPatchRootFileRefusesIndexInPhase1(t *testing.T) {
	v := newTestVault(t)
	before, ok := canonicalSHA(v, "index.md")
	if !ok {
		t.Fatal("canonicalSHA(index.md): not found")
	}
	op := Op{Kind: OpPatchPage, Path: "index.md", Before: before, Content: []byte("tampered\n")}
	err := ValidateOp(op, v, v.Schema())
	wantValidationError(t, err)
	if strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("index.md refusal = %q, must not be a bare \"does not exist\"", err)
	}
	if strings.Contains(err.Error(), "forever") {
		t.Fatalf("index.md refusal = %q, must not read as forbidden forever", err)
	}
	if !strings.Contains(err.Error(), "not enabled") && !strings.Contains(err.Error(), "phase 2") {
		t.Fatalf("index.md refusal = %q, want it to say it is not yet enabled in this phase", err)
	}
}

// TestPatchRootFileRefusesSection pins OQ-9's L4: op.Section set on a
// root-file patch is refused — whole-file or hunks only, never a section
// target.
func TestPatchRootFileRefusesSection(t *testing.T) {
	v := newTestVault(t)
	before, ok := canonicalSHA(v, "curator-memory.md")
	if !ok {
		t.Fatal("canonicalSHA(curator-memory.md): not found")
	}
	op := Op{
		Kind:    OpPatchPage,
		Path:    "curator-memory.md",
		Section: "## Naming",
		Before:  before,
		Content: []byte("tampered\n"),
	}
	err := ValidateOp(op, v, v.Schema())
	wantValidationError(t, err)
	if !strings.Contains(err.Error(), "section") {
		t.Fatalf("curator-memory.md + Section refusal = %q, want it to mention the section restriction", err)
	}
}

// TestOneWriterGuardNamesOtherOp pins OQ-9's L2: a changeset carrying both
// a rename_page whose cascade rewrites curator-memory.md and a top-level
// patch_page targeting the same file is refused, naming the rename's id
// and kind — not merely refused (OQ-9 §7 L2: "a refusal the reviewer
// cannot act on is a worse defect than the collision").
func TestOneWriterGuardNamesOtherOp(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	// Seed curator-memory.md with a wikilink to gpt-4 BEFORE the engine
	// ever opens the vault, so the rename's cascade (built against the
	// working tree for the first op of a changeset) sees it and covers
	// it — the shipped minimal fixture's curator-memory.md carries no
	// wikilinks at all.
	memPath := filepath.Join(dir, "curator-memory.md")
	orig, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatalf("read curator-memory.md: %v", err)
	}
	seeded := string(orig) + "\n## See also\n- [[gpt-4]] for the vendor-name convention above.\n"
	if err := os.WriteFile(memPath, []byte(seeded), 0o644); err != nil {
		t.Fatalf("seed curator-memory.md: %v", err)
	}

	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()

	if _, err := e.OpenChangeset("rename gpt-4", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	renameID, err := e.Append(Op{
		Kind: OpRenamePage,
		From: "wiki/entities/gpt-4.md",
		To:   "wiki/entities/gpt-4-turbo.md",
	})
	if err != nil {
		t.Fatalf("Append(rename_page): %v", err)
	}

	// Sanity: the rename's cascade really does cover curator-memory.md —
	// otherwise this test would pass for the wrong reason.
	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	renameOp, ok := c.Op(renameID)
	if !ok {
		t.Fatalf("op %s not found", renameID)
	}
	found := false
	for _, sub := range renameOp.Cascade {
		if sub.Path == "curator-memory.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("test setup: rename's cascade does not cover curator-memory.md: %+v", renameOp.Cascade)
	}

	curBefore, ok := canonicalSHA(e.Vault(), "curator-memory.md")
	if !ok {
		t.Fatal("canonicalSHA(curator-memory.md): not found")
	}
	_, err = e.Append(Op{
		Kind:    OpPatchPage,
		Path:    "curator-memory.md",
		Before:  curBefore,
		Content: []byte(seeded + "\nA manual note added on top.\n"),
	})
	if err == nil {
		t.Fatal("Append(patch_page curator-memory.md) succeeded, want the one-writer guard to refuse it")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), renameID) {
		t.Errorf("refusal %q does not name the other op's id %q", err.Error(), renameID)
	}
	if !strings.Contains(err.Error(), string(OpRenamePage)) {
		t.Errorf("refusal %q does not name the other op's kind %q", err.Error(), OpRenamePage)
	}
}

// TestOneWriterGuardCreateDerivation exercises the OTHER half of L2 — a
// create_page's index.md derivation — directly against
// rootFileWriterConflict, since index.md is not allow-listed in this
// phase (TestPatchRootFileRefusesIndexInPhase1) and so can never reach
// this collision check through Append/ValidateOp yet. OQ-9 §9 requires L2
// "built here in full" so that S4-T7 (phase 2) only has to add one entry
// to the allow-list — this proves that half of the guard is already
// correct and ready.
func TestOneWriterGuardCreateDerivation(t *testing.T) {
	top := Op{ID: "op3", Kind: OpCreatePage, Path: "wiki/concepts/new-concept.md"}
	err := rootFileWriterConflict(top, "index.md")
	if err == nil {
		t.Fatal("rootFileWriterConflict: got nil, want a collision naming op3 (create_page)")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), "op3") || !strings.Contains(err.Error(), string(OpCreatePage)) {
		t.Fatalf("refusal %q does not name the other op's id and kind", err)
	}

	// A create_page targeting some other path is not a writer of
	// curator-memory.md at all (no derivation ever targets it).
	if err := rootFileWriterConflict(top, "curator-memory.md"); err != nil {
		t.Fatalf("rootFileWriterConflict(create_page, curator-memory.md) = %v, want nil: no derivation targets curator-memory.md", err)
	}
}

// TestOneWriterGuardReverseOrder pins OQ-9's L2 as order-independent
// (S4-T0 repair-1, closed for real in repair-2): a changeset carrying a
// top-level patch_page on curator-memory.md FIRST, then a rename_page
// whose cascade rewrites the same file SECOND, must be refused when the
// rename is proposed — the reverse of TestOneWriterGuardNamesOtherOp's
// order. The probe that found this gap: "patch landed as op1 ... two live
// writers of curator-memory.md in one changeset — patch op1 and op2's
// cascade — and the guard did not fire." OQ-9 §9 L2 says "any automatic
// writer", not "any automatic writer proposed first".
//
// Root cause (repair-1's finding, still true): validateCascade's own
// checkNewWriterOneWriter(cv, ...) call (validate.go) reads cv, which
// Append's cascadeBase (engine_changeset.go) sets to an in-memory
// fstest.MapFS projection — Root() unconditionally "" — the moment the
// changeset already carries another live op, precisely the case a
// collision requires. checkNewWriterOneWriter's disk read is therefore a
// no-op in that call.
//
// Fix (repair-2, engine_changeset.go — authorized outside this subtask's
// original file list): Append now repeats the mirror check itself, right
// after ValidateOp succeeds for OpRenamePage/OpMergePages, against
// e.vault — the real, disk-backed vault — rather than cv. Both subtests
// below now assert the full contract end to end with no t.Skip.
func TestOneWriterGuardReverseOrder(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	// Seed curator-memory.md with a wikilink to gpt-4 BEFORE the engine
	// ever opens the vault, exactly as TestOneWriterGuardNamesOtherOp
	// does, so the rename's cascade sees it and covers it.
	memPath := filepath.Join(dir, "curator-memory.md")
	orig, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatalf("read curator-memory.md: %v", err)
	}
	seeded := string(orig) + "\n## See also\n- [[gpt-4]] for the vendor-name convention above.\n"
	if err := os.WriteFile(memPath, []byte(seeded), 0o644); err != nil {
		t.Fatalf("seed curator-memory.md: %v", err)
	}

	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()

	if _, err := e.OpenChangeset("patch then rename", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	// op1: patch curator-memory.md directly through the real engine. Must
	// succeed — it is the first op in the changeset, nothing to collide
	// with yet.
	curBefore, ok := canonicalSHA(e.Vault(), "curator-memory.md")
	if !ok {
		t.Fatal("canonicalSHA(curator-memory.md): not found")
	}
	patchID, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    "curator-memory.md",
		Before:  curBefore,
		Content: []byte(seeded + "\nA manual note added on top.\n"),
	})
	if err != nil {
		t.Fatalf("Append(patch_page curator-memory.md) as op1: %v", err)
	}

	renameFrom, renameTo := "wiki/entities/gpt-4.md", "wiki/entities/gpt-4-turbo.md"

	// Sanity: prove the rename's cascade really would cover
	// curator-memory.md, by building it against the vault directly the
	// same way Append does — otherwise a refusal below could pass for an
	// unrelated reason (e.g. the rename itself being malformed).
	cascade, err := buildCascade(e.Vault(), []string{renameFrom}, renameTo)
	if err != nil {
		t.Fatalf("buildCascade: %v", err)
	}
	found := false
	for _, sub := range cascade {
		if sub.Path == "curator-memory.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("test setup: rename's cascade does not cover curator-memory.md: %+v", cascade)
	}

	t.Run("end-to-end via Append", func(t *testing.T) {
		if _, err := e.Append(Op{Kind: OpRenamePage, From: renameFrom, To: renameTo}); err == nil {
			t.Fatal("Append(rename_page) succeeded, want the one-writer guard's mirror direction to refuse it")
		} else if !errors.Is(err, ErrValidation) {
			t.Fatalf("error = %v, want ErrValidation", err)
		} else if !strings.Contains(err.Error(), patchID) {
			t.Errorf("refusal %q does not name the other op's id %q", err.Error(), patchID)
		} else if !strings.Contains(err.Error(), string(OpPatchPage)) {
			t.Errorf("refusal %q does not name the other op's kind %q", err.Error(), OpPatchPage)
		}
	})

	t.Run("guard logic against the real vault", func(t *testing.T) {
		// Exercises the exact production call chain (ValidateOp ->
		// validateRenamePage -> validateCascade ->
		// checkNewWriterOneWriter) with e.Vault() (real root) standing in
		// for the cv projection ValidateOp would see internally — the same
		// substitution Append's own repair-2 hook makes. op1 is still the
		// only live op on disk (the sibling subtest's Append call above was
		// refused, so its rename never actually landed), so this reads
		// exactly the changeset state the probe scenario describes.
		freshRename := Op{Kind: OpRenamePage, From: renameFrom, To: renameTo, Cascade: cascade}
		err := ValidateOp(freshRename, e.Vault(), e.Vault().Schema())
		if err == nil {
			t.Fatal("ValidateOp(rename_page, e.Vault(), ...) = nil, want the one-writer guard's mirror direction to refuse it")
		}
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("error = %v, want ErrValidation", err)
		}
		if !strings.Contains(err.Error(), patchID) {
			t.Errorf("refusal %q does not name the other op's id %q", err.Error(), patchID)
		}
		if !strings.Contains(err.Error(), string(OpPatchPage)) {
			t.Errorf("refusal %q does not name the other op's kind %q", err.Error(), OpPatchPage)
		}
	})
}

// TestOneWriterGuardReverseOrderCreateDerivation is
// TestOneWriterGuardReverseOrder's create_page mirror: a live patch_page
// on index.md must refuse a create_page whose index.md derivation would
// also write it. index.md is not allow-listed in phase 1
// (TestPatchRootFileRefusesIndexInPhase1), so a live index.md patch_page
// can never actually reach the open changeset yet — this is exercised
// directly against newWriterConflict instead, exactly as
// TestOneWriterGuardCreateDerivation already does for the forward
// direction's own not-yet-reachable case. newWriterConflict gates on
// isKnownRootFile rather than isPatchableRootFile for the same reason
// rootFileWriterConflict carries no phase gate of its own at all: the
// phase gate belongs to validateNonPagePatch, the one place a root-file
// patch_page can ever become live, so this proves the mirror check is
// already correct and ready for S4-T7 to allow-list index.md with no
// further change here.
func TestOneWriterGuardReverseOrderCreateDerivation(t *testing.T) {
	live := Op{ID: "op1", Kind: OpPatchPage, Path: "index.md"}
	newOp := Op{Kind: OpCreatePage, Path: "wiki/concepts/new-concept.md"}

	err := newWriterConflict(newOp, live)
	if err == nil {
		t.Fatal("newWriterConflict: got nil, want a collision naming op1 (patch_page)")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), "op1") || !strings.Contains(err.Error(), string(OpPatchPage)) {
		t.Fatalf("refusal %q does not name the other op's id and kind", err)
	}

	// A live patch_page targeting SCHEMA.md is a known root file, but
	// SCHEMA.md is forbidden forever (never allow-listed, not phase-gated
	// like index.md) and no create_page derivation ever targets it
	// either — nothing to collide with.
	schemaLive := Op{ID: "op2", Kind: OpPatchPage, Path: "SCHEMA.md"}
	if err := newWriterConflict(newOp, schemaLive); err != nil {
		t.Fatalf("newWriterConflict(create_page, patch_page SCHEMA.md) = %v, want nil: no derivation targets SCHEMA.md", err)
	}

	// A live patch_page on curator-memory.md is not written by ANY
	// create_page derivation, so there is nothing to collide with either.
	curatorLive := Op{ID: "op3", Kind: OpPatchPage, Path: "curator-memory.md"}
	if err := newWriterConflict(newOp, curatorLive); err != nil {
		t.Fatalf("newWriterConflict(create_page, patch_page curator-memory.md) = %v, want nil: no derivation targets curator-memory.md", err)
	}

	// A live patch_page on an ORDINARY page — not one of OQ-9's four
	// known root files at all — must never be treated as a root-file
	// collision, even when a rename_page's cascade happens to cover that
	// same ordinary page: isKnownRootFile is what keeps this guard scoped
	// to vault-root bookkeeping files, not every page two ops might both
	// touch.
	ordinaryLive := Op{ID: "op4", Kind: OpPatchPage, Path: "wiki/entities/vendor-list.md"}
	renameOp := Op{
		Kind: OpRenamePage,
		Cascade: []Op{
			{Kind: OpPatchPage, Path: "wiki/entities/vendor-list.md"},
		},
	}
	if err := newWriterConflict(renameOp, ordinaryLive); err != nil {
		t.Fatalf("newWriterConflict(rename_page, patch_page on an ordinary page) = %v, want nil: OQ-9 L2 is scoped to vault-root files only", err)
	}
}

// TestUndropHunkRestoresContent pins backbone §5.4's UndropHunk Contract
// (MASTER §9 D-CL): the exact inverse of DropHunk. Dropping then
// undropping a hunk must restore Op.After (and the CAS content it names)
// to exactly what it was before the drop, and journal hunk_undropped.
func TestUndropHunkRestoresContent(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("undrop hunk", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	oldLine := "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newLine := "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
	if !bodyContains(page.Body, oldLine) {
		t.Fatalf("fixture body does not contain the expected line %q", oldLine)
	}
	newBody := replaceLine(page.Body, oldLine, newLine)
	rewritten := *page
	rewritten.Body = newBody

	id, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    page.Path,
		Section: "## Related",
		Before:  page.SHA256(),
		Content: rewritten.Serialize(),
		Hunks: []Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldLine}, Add: []string{newLine}},
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	c, _ := e.Current()
	op, _ := c.Op(id)
	afterAppend := op.After

	if err := e.DropHunk(id, "h1"); err != nil {
		t.Fatalf("DropHunk: %v", err)
	}
	c, _ = e.Current()
	op, _ = c.Op(id)
	afterDrop := op.After
	if afterDrop == afterAppend {
		t.Fatal("op.After did not change after DropHunk")
	}

	if err := e.UndropHunk(id, "h1"); err != nil {
		t.Fatalf("UndropHunk: %v", err)
	}

	c, err = e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok = c.Op(id)
	if !ok {
		t.Fatal("op not found after UndropHunk")
	}
	if op.Hunks[0].Dropped {
		t.Fatal("hunk h1 is still marked Dropped after UndropHunk")
	}
	if op.After != afterAppend {
		t.Fatalf("op.After = %q after UndropHunk, want it restored to %q (the pre-drop value)", op.After, afterAppend)
	}

	got, err := e.store.Get(op.After)
	if err != nil {
		t.Fatalf("Store.Get(op.After): %v", err)
	}
	if string(got) != string(rewritten.Serialize()) {
		t.Fatal("after undropping the only hunk, projected content should equal the rewritten page again")
	}

	events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvHunkUndropped}})
	if err != nil {
		t.Fatalf("Journal().Query(EvHunkUndropped): %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("hunk_undropped events = %d, want 1", len(events))
	}
	if events[0].Op != id || events[0].Hunk != "h1" {
		t.Fatalf("hunk_undropped event = %+v, want Op=%q Hunk=%q", events[0], id, "h1")
	}
}

// TestUndropLiveHunkIsNoOp pins backbone §5.4's UndropHunk Contract: a
// hunk that was never dropped stays exactly as it was — the review
// screen presses "y" on every hunk it walks past, including ones that
// were never dropped, and it must not error just because the reviewer
// agreed with the default.
func TestUndropLiveHunkIsNoOp(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("undrop live hunk", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	oldLine := "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newLine := "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
	newBody := replaceLine(page.Body, oldLine, newLine)
	rewritten := *page
	rewritten.Body = newBody

	id, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    page.Path,
		Section: "## Related",
		Before:  page.SHA256(),
		Content: rewritten.Serialize(),
		Hunks: []Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldLine}, Add: []string{newLine}},
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	c, _ := e.Current()
	op, _ := c.Op(id)
	if op.Hunks[0].Dropped {
		t.Fatal("test setup: hunk h1 starts Dropped, want live")
	}
	beforeUndrop := op.After

	if err := e.UndropHunk(id, "h1"); err != nil {
		t.Fatalf("UndropHunk on an already-live hunk returned an error, want a no-op success: %v", err)
	}

	c, err = e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok = c.Op(id)
	if !ok {
		t.Fatal("op not found after UndropHunk")
	}
	if op.Hunks[0].Dropped {
		t.Fatal("hunk h1 became Dropped after UndropHunk on a live hunk")
	}
	if op.After != beforeUndrop {
		t.Fatalf("op.After = %q after undropping a live hunk, want it unchanged at %q", op.After, beforeUndrop)
	}
}

// TestUndropHunkUnknownIDsError pins the error-shape half of D-CL: an
// unknown op or hunk id is still an error, worded exactly as DropHunk's.
func TestUndropHunkUnknownIDsError(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("undrop errors", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	id, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    page.Path,
		Section: "## Related",
		Before:  page.SHA256(),
		Content: page.Serialize(),
		Hunks:   []Hunk{{ID: "h1", Path: page.Path}},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := e.UndropHunk("op-does-not-exist", "h1"); err == nil {
		t.Fatal("UndropHunk with an unknown op id: got nil, want an error")
	}
	if err := e.UndropHunk(id, "h-does-not-exist"); err == nil {
		t.Fatal("UndropHunk with an unknown hunk id: got nil, want an error")
	}
}

// rootfile_index_test.go proves OQ-9 phase 2 (S4-T7): index.md joins
// patchableRootFiles (rootfile.go) and the one-writer guard S4-T0 already
// built and exercised against curator-memory.md now refuses the SAME two
// collision shapes for the file that actually has automatic writers.
//
// index.md carries TWO automatic writers (OQ-9 §7, C-98 as measured, not as
// its own loose prose): a create_page's index derivation (derive.go,
// liveCreatePages collects OpCreatePage only) and a rename_page/merge_pages
// cascade (cascadeRoots, op.go) whenever the renamed/merged page happens to
// be linked from index.md — which, in the shipped "minimal" fixture, every
// page is, by construction (helpers_test.go's syntheticNoBacklinksVault
// doc comment). So unlike TestOneWriterGuardNamesOtherOp's curator-memory.md
// scenario, no fixture seeding is needed here to make a rename's cascade
// cover index.md.
//
// No new guard code is added or exercised here beyond what S4-T0 already
// shipped (rootfile.go, validate.go, engine_changeset.go) — these tests only
// prove the existing, unchanged guard now reaches index.md through the
// public Append entry point, which it could not before this subtask's
// one-line allow-list addition.
//
// One thing this subtask DID have to add beyond that one line:
// isRevertableRootFile (rootfile.go), because index.md's allow-listing
// exposed a second, previously-unreachable question the shared
// isPatchableRootFile predicate could not answer correctly — see D-CW
// (s4-tui.md C-110) and TestSoloIndexPatchLandsAndReverts below.
package stage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIndexPatchPlusCreateRefused pins OQ-9 phase 2's derivation collision:
// a changeset holding a top-level patch_page on index.md and a create_page
// (whose derivation rewrites index.md) is refused, naming both ops. Checked
// in both orders — create_page first (validateCreatePage's own
// checkNewWriterOneWriter, exercised end to end for the first time now that
// index.md is allow-listed) and patch_page first (checkRootFileOneWriter,
// the same forward direction TestOneWriterGuardNamesOtherOp already proves
// for curator-memory.md) — since this is the collision phase 1 could only
// prove with direct unit calls against rootFileWriterConflict/
// newWriterConflict (TestOneWriterGuardCreateDerivation,
// TestOneWriterGuardReverseOrderCreateDerivation), never through Append.
func TestIndexPatchPlusCreateRefused(t *testing.T) {
	t.Run("create-first: create_page's derivation collides with a later index.md patch", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("create then patch index", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		createID, err := e.Append(Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/probe-concept.md",
			Content:    newConceptPageContent("Probe Concept"),
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		})
		if err != nil {
			t.Fatalf("Append(create_page) as op1: %v", err)
		}

		before, ok := canonicalSHA(e.Vault(), "index.md")
		if !ok {
			t.Fatal("canonicalSHA(index.md): not found")
		}
		_, err = e.Append(Op{
			Kind:    OpPatchPage,
			Path:    "index.md",
			Before:  before,
			Content: []byte("tampered\n"),
		})
		if err == nil {
			t.Fatal("Append(patch_page index.md) succeeded, want the one-writer guard to refuse it")
		}
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("error = %v, want ErrValidation", err)
		}
		if !strings.Contains(err.Error(), createID) {
			t.Errorf("refusal %q does not name the other op's id %q", err.Error(), createID)
		}
		if !strings.Contains(err.Error(), string(OpCreatePage)) {
			t.Errorf("refusal %q does not name the other op's kind %q", err.Error(), OpCreatePage)
		}
	})

	t.Run("patch-first: a live index.md patch collides with a later create_page's derivation", func(t *testing.T) {
		e, dir := newTestEngine(t)
		orig, err := os.ReadFile(filepath.Join(dir, "index.md"))
		if err != nil {
			t.Fatalf("read index.md: %v", err)
		}
		before, ok := canonicalSHA(e.Vault(), "index.md")
		if !ok {
			t.Fatal("canonicalSHA(index.md): not found")
		}

		if _, err := e.OpenChangeset("patch then create index", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		patchID, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    "index.md",
			Before:  before,
			Content: append(append([]byte(nil), orig...), []byte("\n## Notes\n- A manually added note.\n")...),
		})
		if err != nil {
			t.Fatalf("Append(patch_page index.md) as op1: %v", err)
		}

		_, err = e.Append(Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/probe-concept-2.md",
			Content:    newConceptPageContent("Probe Concept 2"),
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		})
		if err == nil {
			t.Fatal("Append(create_page) succeeded, want the one-writer guard's mirror direction to refuse it")
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

// TestIndexPatchPlusRenameCascadeRefused pins OQ-9 phase 2's cascade
// collision: a patch_page on index.md plus a rename_page whose cascade
// rewrites index.md is refused, naming both ops — asserted in both orders,
// since C-97 exists precisely because the first cut of this guard was
// one-directional (curator-memory.md's TestOneWriterGuardNamesOtherOp /
// TestOneWriterGuardReverseOrder are the same proof, against the file OQ-9
// shipped first because it carries no automatic writer at all). No fixture
// seeding is needed: index.md already links wiki/entities/gpt-4.md in the
// shipped "minimal" fixture, so renaming it triggers index.md's cascade by
// construction.
func TestIndexPatchPlusRenameCascadeRefused(t *testing.T) {
	const renameFrom, renameTo = "wiki/entities/gpt-4.md", "wiki/entities/gpt-4-turbo.md"

	t.Run("rename-first: a live cascade collides with a later index.md patch", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("rename then patch index", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		renameID, err := e.Append(Op{Kind: OpRenamePage, From: renameFrom, To: renameTo})
		if err != nil {
			t.Fatalf("Append(rename_page) as op1: %v", err)
		}

		// Sanity: the rename's cascade really does cover index.md — otherwise
		// this test would pass for the wrong reason.
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
			if sub.Path == "index.md" {
				found = true
			}
		}
		if !found {
			t.Fatalf("test setup: rename's cascade does not cover index.md: %+v", renameOp.Cascade)
		}

		before, ok := canonicalSHA(e.Vault(), "index.md")
		if !ok {
			t.Fatal("canonicalSHA(index.md): not found")
		}
		_, err = e.Append(Op{
			Kind:    OpPatchPage,
			Path:    "index.md",
			Before:  before,
			Content: []byte("tampered\n"),
		})
		if err == nil {
			t.Fatal("Append(patch_page index.md) succeeded, want the one-writer guard to refuse it")
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
	})

	t.Run("patch-first: a live index.md patch collides with a later rename's cascade", func(t *testing.T) {
		e, dir := newTestEngine(t)
		orig, err := os.ReadFile(filepath.Join(dir, "index.md"))
		if err != nil {
			t.Fatalf("read index.md: %v", err)
		}
		before, ok := canonicalSHA(e.Vault(), "index.md")
		if !ok {
			t.Fatal("canonicalSHA(index.md): not found")
		}

		if _, err := e.OpenChangeset("patch then rename index", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		patchID, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    "index.md",
			Before:  before,
			Content: append(append([]byte(nil), orig...), []byte("\n## Notes\n- A manually added note.\n")...),
		})
		if err != nil {
			t.Fatalf("Append(patch_page index.md) as op1: %v", err)
		}

		_, err = e.Append(Op{Kind: OpRenamePage, From: renameFrom, To: renameTo})
		if err == nil {
			t.Fatal("Append(rename_page) succeeded, want the one-writer guard's mirror direction to refuse it")
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

// TestSoloIndexPatchLandsAndReverts pins OQ-9 phase 2's headline claim,
// mirroring TestPatchRootFileLandsAndReverts (S4-T0, curator-memory.md):
// a LONE patch_page on index.md — the only live op in its changeset, so
// nothing collides with it — validates and commits, landing byte-exactly.
//
// Revert's half of this test does NOT mirror curator-memory.md's, and
// that difference is this test's actual point, per decision D-CW
// (s4-tui.md C-110, found when adding index.md to the allow-list turned
// nine unrelated tests red): "may a patch_page target this path"
// (isPatchableRootFile — index.md: yes, as of this subtask) and "can
// Revert reconstruct this path's previous content from a stored CAS
// blob" (isRevertableRootFile — index.md: no) are different questions.
// index.md's bytes are computed by derive.go's index derivation rather
// than always being an explicitly captured pre-image the way
// curator-memory.md's are (rootfile.go's revertableRootFiles doc
// comment), so buildRevertOps (revert.go) reports index.md as SKIPPED —
// exactly its pre-S4-T7 behaviour, restored on purpose rather than
// "fixed" into a reconstruction it cannot honestly perform.
//
// This means OQ-9 §5 pro 2 — "byte-exact undo for merges and splits,
// closing the last fidelity gap" — is NOT delivered by this subtask for
// a solo index.md patch either, and this run's report says so plainly:
// storing index.md's pre-images explicitly (so a direct patch's own
// "before" bytes, at least, become reconstructable) is a larger design
// this subtask's file list does not build, and D-CW's own text rules out
// storing derived files in CAS as an in-scope fix.
func TestSoloIndexPatchLandsAndReverts(t *testing.T) {
	e, dir := newTestEngine(t)

	const path = "index.md"
	origContent, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	before, ok := canonicalSHA(e.Vault(), path)
	if !ok {
		t.Fatalf("canonicalSHA(%s): not found", path)
	}
	newContent := append(append([]byte(nil), origContent...), []byte("\n## Notes\n- Curator can now edit the index description directly.\n")...)

	if _, err := e.OpenChangeset("patch index", testAuthor); err != nil {
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

	commitID, err := e.Commit("patch index")
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
	skipped := lastRevertedSkipped(t, e)
	if !containsString(skipped, path) {
		t.Fatalf("%s not reported as skipped: %v — D-CW: index.md fails isRevertableRootFile, so Revert must report it honestly rather than fabricate a reconstruction it cannot perform", path, skipped)
	}
	if !strings.Contains(revertCS.Intent, path) {
		t.Errorf("changeset Intent = %q, want it to mention the skipped path %s", revertCS.Intent, path)
	}
	for i := range revertCS.Ops {
		if revertCS.Ops[i].Path == path {
			t.Fatalf("revert changeset carries an op for %s: %+v, want it skipped instead (D-CW: index.md is not revertable from CAS)", path, revertCS.Ops[i])
		}
	}

	_ = opID
}

// apply_preimage_test.go pins S6-C126: Commit's step 4 must store the
// pre-commit bytes of every path it overwrites, including a file rewritten
// only IMPLICITLY (with no Op of its own) such as index.md's create_page
// derivation (derive.go, buildCommitMaterialization). Before the fix in
// apply.go's capturePreExistingWrites, index.md's pre-commit blob never
// reached the CAS on a vault's very first page-creating commit — measured
// live, 2026-09-14, gate G6: `lw doctor` reported the baseline snapshot's
// index.md sha missing from objects/, and `lw revert 000001` skipped
// index.md outright.
package stage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCommitStoresPreImageOfImplicitlyRewrittenIndex is test (a) from this
// subtask's brief: a create_page's own index.md derivation carries no
// Before/patch_page op at all, so before the fix nothing ever stored
// index.md's pre-commit bytes. After the fix, the CAS holds exactly the
// bytes the vault's index.md carried immediately before the commit.
func TestCommitStoresPreImageOfImplicitlyRewrittenIndex(t *testing.T) {
	e, dir := newTestEngine(t)

	preCommitIndex, err := os.ReadFile(filepath.Join(dir, "index.md"))
	if err != nil {
		t.Fatalf("read pre-commit index.md: %v", err)
	}
	preSHA := sha256Hex(preCommitIndex)

	seedCreatePage(t, e, "wiki/concepts/new-page.md", "New Page")

	store, err := OpenStore(filepath.Join(dir, ".llmwiki", "objects"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if !store.Has(preSHA) {
		t.Fatalf("CAS is missing index.md's pre-commit blob %s", preSHA)
	}
	got, err := store.Get(preSHA)
	if err != nil {
		t.Fatalf("Get(%s): %v", preSHA, err)
	}
	if string(got) != string(preCommitIndex) {
		t.Fatalf("stored pre-image of index.md =\n%q\nwant\n%q", got, preCommitIndex)
	}
}

// TestCommitStoresPreImagesForEveryCommitBeginPath is the second half of
// test (a): every path the commit's own commit_begin names, that pre-existed
// (present in the predecessor snapshot), has its pre-image blob in the CAS —
// exactly the invariant cmd/lw's checkObjects (checkObjects, cmd_doctor.go)
// requires. This walks the same journal event and the same predecessor
// snapshot checkObjects itself reads, rather than re-deriving the path list
// a second way that could drift from it.
func TestCommitStoresPreImagesForEveryCommitBeginPath(t *testing.T) {
	e, dir := newTestEngine(t)
	commitID := seedCreatePage(t, e, "wiki/concepts/new-page.md", "New Page")

	events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvCommitBegin}})
	if err != nil {
		t.Fatalf("Journal().Query(EvCommitBegin): %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("%d commit_begin events, want 1", len(events))
	}
	paths := events[0].Paths

	predID, err := predecessorCommitID(commitID)
	if err != nil {
		t.Fatalf("predecessorCommitID(%s): %v", commitID, err)
	}
	baseline, err := ReadSnapshot(filepath.Join(dir, ".llmwiki", "snapshots"), predID)
	if err != nil {
		t.Fatalf("ReadSnapshot(%s): %v", predID, err)
	}

	store, err := OpenStore(filepath.Join(dir, ".llmwiki", "objects"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	preExisting := 0
	for _, p := range paths {
		sha, ok := baseline[p]
		if !ok {
			continue // did not pre-exist (the new page itself): no pre-image to require.
		}
		preExisting++
		if !store.Has(sha) {
			t.Errorf("commit_begin path %s pre-existed (baseline sha %s) but its pre-image is not in the CAS", p, sha)
		}
	}
	if preExisting == 0 {
		t.Fatal("test setup produced no pre-existing commit_begin path; this test would pass vacuously")
	}
}

// TestCommitStoresPreImageOnSecondIndexRewrite is test (c): a second commit
// that again rewrites index.md (via its own create_page derivation) stores
// ITS pre-commit content too, not only the very first commit's. Guards
// against a fix that happened to work only for the genesis index.md.
func TestCommitStoresPreImageOnSecondIndexRewrite(t *testing.T) {
	e, dir := newTestEngine(t)
	seedCreatePage(t, e, "wiki/concepts/first-page.md", "First Page")

	midIndex, err := os.ReadFile(filepath.Join(dir, "index.md"))
	if err != nil {
		t.Fatalf("read index.md between commits: %v", err)
	}
	midSHA := sha256Hex(midIndex)

	seedCreatePage(t, e, "wiki/concepts/second-page.md", "Second Page")

	store, err := OpenStore(filepath.Join(dir, ".llmwiki", "objects"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if !store.Has(midSHA) {
		t.Fatalf("CAS is missing index.md's content between the first and second commit (%s)", midSHA)
	}
}

// TestRevertSkipsIndexDespitePreImageNowPresent documents a gap found while
// implementing S6-C126, reported to the orchestrator rather than fixed here
// (see this subtask's report.md): after the fix above, the CAS holds
// exactly the blob buildRevertOps (revert.go) would need to reconstruct
// index.md — this test's own precondition check proves it — but
// revert.go's CHANGED-path branch gates on isRevertableRootFile
// (rootfile.go), which excludes index.md from revertableRootFiles by a
// SEPARATE, deliberate, already-documented rule unrelated to whether any
// one blob happens to exist ("index.md ... is excluded from this set
// entirely", rootfile.go's revertableRootFiles doc comment) — so Revert
// still reports it skipped. This pins that CURRENT, intentional behaviour;
// it is not the desired end state and this subtask's brief explicitly
// forbids changing Revert's rule to reach it without the orchestrator's
// say-so.
func TestRevertSkipsIndexDespitePreImageNowPresent(t *testing.T) {
	e, dir := newTestEngine(t)
	commitID := seedCreatePage(t, e, "wiki/concepts/new-page.md", "New Page")

	predID, err := predecessorCommitID(commitID)
	if err != nil {
		t.Fatalf("predecessorCommitID(%s): %v", commitID, err)
	}
	baseline, err := ReadSnapshot(filepath.Join(dir, ".llmwiki", "snapshots"), predID)
	if err != nil {
		t.Fatalf("ReadSnapshot(%s): %v", predID, err)
	}
	store, err := OpenStore(filepath.Join(dir, ".llmwiki", "objects"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if !store.Has(baseline["index.md"]) {
		t.Fatalf("precondition failed: index.md's pre-commit blob %s should already be in the CAS (capturePreExistingWrites)", baseline["index.md"])
	}

	if _, err := e.Revert(commitID); err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}
	skipped := lastRevertedSkipped(t, e)
	if !containsString(skipped, "index.md") {
		t.Fatalf("skipped = %v, want index.md still reported skipped — if isRevertableRootFile changed, this test (and S6-C126's report) needs re-checking, not silently deleting", skipped)
	}
}

package stage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// requireGitApplyCheck git-inits dir, commits its current contents, then
// pipes patch through `git apply --check` and fails t unless it exits 0 —
// the mechanical proof that Unified()'s output is a patch git actually
// accepts (backbone §5.6 Contract; this subtask's stated goal).
func requireGitApplyCheck(t *testing.T, dir, patch string) {
	t.Helper()

	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "Test")
	run("git", "add", "-A")
	run("git", "commit", "-q", "-m", "initial")

	patchPath := filepath.Join(t.TempDir(), "diff.patch")
	if err := os.WriteFile(patchPath, []byte(patch), 0o644); err != nil {
		t.Fatalf("write patch: %v", err)
	}

	cmd := exec.Command("git", "apply", "--check", patchPath)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply --check failed: %v\n%s\n--- patch ---\n%s", err, out, patch)
	}
}

// fileDiffByOp returns a pointer into d.Files for the entry whose OpID and
// Path match, or nil.
func fileDiffByOp(d Diff, opID, path string) *FileDiff {
	for i := range d.Files {
		if d.Files[i].OpID == opID && d.Files[i].Path == path {
			return &d.Files[i]
		}
	}
	return nil
}

// TestDiffGolden pins Unified()'s exact text output for a changeset holding
// one of each of four op kinds — create_page, patch_page, rename_page (with
// its cascade) and add_link — against a checked-in golden. The golden lives
// under internal/stage/testdata/, never spec/fixtures/ (correction C-54):
// testutil.Golden rewrites its own file under -update, and spec/ is ground
// truth the suite must never be able to edit.
func TestDiffGolden(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("golden diff", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/golden-new-page.md",
		Content:    newConceptPageContent("Golden New Page"),
		Rationale:  "golden fixture",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append create_page: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/flash-attention.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/flash-attention.md")
	}
	oldLine := "- Reduces memory-bandwidth traffic, which dominates attention's runtime cost."
	newLine := "- Reduces memory-bandwidth traffic, the dominant cost of naive attention."
	if !bodyContains(page.Body, oldLine) {
		t.Fatalf("fixture body does not contain %q", oldLine)
	}
	rewritten := *page
	rewritten.Body = replaceLine(page.Body, oldLine, newLine)
	if _, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    page.Path,
		Section: "## Why it matters",
		Before:  page.SHA256(),
		Content: rewritten.Serialize(),
		Hunks: []Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldLine}, Add: []string{newLine}},
		},
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

	if _, err := e.Append(Op{
		Kind: OpAddLink,
		From: "wiki/concepts/flash-attention.md",
		To:   "wiki/entities/gpt-4.md",
	}); err != nil {
		t.Fatalf("Append add_link: %v", err)
	}

	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	testutil.Golden(t, filepath.Join("testdata", "diff-golden.want"), []byte(d.Unified()))
}

// TestUnifiedAppliesCleanly proves Unified() is a patch git apply --check
// actually accepts, including the adjacent-line cascade case that
// motivated D-BQ: merging wiki/concepts/kv-cache.md and
// wiki/concepts/flash-attention.md rewrites two adjacent lines of
// index.md. Rendering the persisted hunks (one per changed line, 3 lines of
// context each) there produces overlapping ranges that git apply rejects;
// this test is what proves Unified() renders from Old/New via ComputeHunks
// instead.
func TestUnifiedAppliesCleanly(t *testing.T) {
	e, dir := newTestEngine(t)
	if _, err := e.OpenChangeset("apply check", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	sources := []string{"wiki/concepts/kv-cache.md", "wiki/concepts/flash-attention.md"}
	if _, err := e.Append(Op{Kind: OpMergePages, Sources: sources, To: "wiki/concepts/attention.md"}); err != nil {
		t.Fatalf("Append merge_pages: %v", err)
	}

	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	unified := d.Unified()
	if unified == "" {
		t.Fatal("Unified() is empty; expected at least the index.md cascade rewrite")
	}
	if !strings.Contains(unified, "index.md") {
		t.Fatalf("Unified() does not touch index.md, the adjacent-line case this test exists to cover:\n%s", unified)
	}

	requireGitApplyCheck(t, dir, unified)
}

// TestHunkIDsStableAcrossRecompute pins Gotcha 4: an op's persisted hunk
// ids must not shift when Diff() is called again, even after an unrelated
// op's hunk is dropped in between.
func TestHunkIDsStableAcrossRecompute(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("stable hunks", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	old1 := "Without caching, generating token n would repeat O(n) work already done for"
	new1 := "Without caching, regenerating token n repeats O(n) work already completed for"
	old2 := "step, at the cost of memory that grows with sequence length."
	new2 := "step, at the cost of memory proportional to sequence length."
	body := replaceLine(replaceLine(page.Body, old1, new1), old2, new2)
	rewritten := *page
	rewritten.Body = body
	trackedID, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    page.Path,
		Section: "## Why it matters",
		Before:  page.SHA256(),
		Content: rewritten.Serialize(),
		Hunks: []Hunk{
			{ID: "h1", Path: page.Path, Del: []string{old1}, Add: []string{new1}},
			{ID: "h2", Path: page.Path, Del: []string{old2}, Add: []string{new2}},
		},
	})
	if err != nil {
		t.Fatalf("Append tracked patch_page: %v", err)
	}

	otherPage, ok := e.Vault().Page("wiki/concepts/flash-attention.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/flash-attention.md")
	}
	oOld := "- Produces numerically identical results to standard attention."
	oNew := "- Produces bit-identical results to standard attention."
	oRewritten := *otherPage
	oRewritten.Body = replaceLine(otherPage.Body, oOld, oNew)
	otherID, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    otherPage.Path,
		Section: "## Why it matters",
		Before:  otherPage.SHA256(),
		Content: oRewritten.Serialize(),
		Hunks: []Hunk{
			{ID: "h1", Path: otherPage.Path, Del: []string{oOld}, Add: []string{oNew}},
		},
	})
	if err != nil {
		t.Fatalf("Append other patch_page: %v", err)
	}

	hunkIDs := func(d Diff) []string {
		fd := fileDiffByOp(d, trackedID, page.Path)
		if fd == nil {
			t.Fatalf("no FileDiff for tracked op %s", trackedID)
		}
		var ids []string
		for _, h := range fd.Hunks {
			ids = append(ids, h.ID)
		}
		return ids
	}

	d1, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff #1: %v", err)
	}
	want := hunkIDs(d1)
	if len(want) != 2 {
		t.Fatalf("tracked op hunk ids = %v, want 2 entries", want)
	}

	d2, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff #2: %v", err)
	}
	if got := hunkIDs(d2); !equalStrings(got, want) {
		t.Fatalf("hunk ids changed across an uneventful recompute: got %v, want %v", got, want)
	}

	if err := e.DropHunk(otherID, "h1"); err != nil {
		t.Fatalf("DropHunk on unrelated op: %v", err)
	}

	d3, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff #3: %v", err)
	}
	if got := hunkIDs(d3); !equalStrings(got, want) {
		t.Fatalf("hunk ids changed after dropping an unrelated op's hunk: got %v, want %v", got, want)
	}
}

// TestDropHunkIsolated proves dropping the middle hunk of three changes
// only its own lines in New, leaving the other two hunks' ids and content
// untouched.
func TestDropHunkIsolated(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("drop isolated", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	old1 := "Without caching, generating token n would repeat O(n) work already done for"
	new1 := "Without caching, regenerating token n repeats O(n) work already completed for"
	old2 := "earlier tokens. Caching turns that into a constant amount of new work per"
	new2 := "earlier tokens. Caching turns that into a fixed amount of new work per"
	old3 := "step, at the cost of memory that grows with sequence length."
	new3 := "step, at the cost of memory proportional to sequence length."

	body := page.Body
	body = replaceLine(body, old1, new1)
	body = replaceLine(body, old2, new2)
	body = replaceLine(body, old3, new3)
	rewritten := *page
	rewritten.Body = body

	id, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    page.Path,
		Section: "## Why it matters",
		Before:  page.SHA256(),
		Content: rewritten.Serialize(),
		Hunks: []Hunk{
			{ID: "h1", Path: page.Path, Del: []string{old1}, Add: []string{new1}},
			{ID: "h2", Path: page.Path, Del: []string{old2}, Add: []string{new2}},
			{ID: "h3", Path: page.Path, Del: []string{old3}, Add: []string{new3}},
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := e.DropHunk(id, "h2"); err != nil {
		t.Fatalf("DropHunk h2: %v", err)
	}

	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	fd := fileDiffByOp(d, id, page.Path)
	if fd == nil {
		t.Fatalf("no FileDiff for op %s", id)
	}

	if len(fd.Hunks) != 3 {
		t.Fatalf("FileDiff.Hunks = %d entries, want 3", len(fd.Hunks))
	}
	wantIDs := []string{"h1", "h2", "h3"}
	for i, h := range fd.Hunks {
		if h.ID != wantIDs[i] {
			t.Fatalf("Hunks[%d].ID = %q, want %q — dropping a hunk must not renumber ids", i, h.ID, wantIDs[i])
		}
	}
	if fd.Hunks[0].Dropped || fd.Hunks[2].Dropped {
		t.Fatal("h1 and h3 must not be marked Dropped")
	}
	if !fd.Hunks[1].Dropped {
		t.Fatal("h2 must be marked Dropped")
	}

	if !bodyContains(fd.New, new1) {
		t.Fatalf("New is missing h1's change:\n%s", fd.New)
	}
	if !bodyContains(fd.New, new3) {
		t.Fatalf("New is missing h3's change:\n%s", fd.New)
	}
	if bodyContains(fd.New, new2) {
		t.Fatalf("New contains the dropped hunk's change:\n%s", fd.New)
	}
	if !bodyContains(fd.New, old2) {
		t.Fatalf("New should still carry h2's original, undropped line:\n%s", fd.New)
	}
}

// TestRetractRendersTombstone proves a retract's FileDiff.New is the
// tombstone Commit will actually write (backbone §5.6 D-BR), never a
// whole-file deletion — asserting New == "" here would be asserting the
// exact bug D-BR exists to prevent.
func TestRetractRendersTombstone(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("retract test", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	id, err := e.Append(Op{
		Kind:      OpRetract,
		Path:      "wiki/concepts/speculative-decoding.md",
		Rationale: "superseded by a forthcoming survey page",
	})
	if err != nil {
		t.Fatalf("Append retract: %v", err)
	}

	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	fd := fileDiffByOp(d, id, "wiki/concepts/speculative-decoding.md")
	if fd == nil {
		t.Fatalf("no FileDiff for retract op %s", id)
	}

	if fd.Old == "" {
		t.Fatal("retract FileDiff.Old is empty; want the current page content")
	}
	if fd.New == "" {
		t.Fatal("retract FileDiff.New is empty; want a tombstone, not a deletion")
	}
	if !strings.Contains(fd.New, "retracted:") {
		t.Fatalf("tombstone missing the 'retracted:' frontmatter key:\n%s", fd.New)
	}
	if !strings.Contains(fd.New, "> **Retracted.**") {
		t.Fatalf("tombstone missing the '> **Retracted.**' callout:\n%s", fd.New)
	}
	if !strings.Contains(fd.New, "superseded by a forthcoming survey page") {
		t.Fatalf("tombstone missing the rationale:\n%s", fd.New)
	}
}

// TestRenameCascadeOneFilePerBacklink proves the rename cascade renders as
// one FileDiff per rewritten backlink, each carrying the cascade sub-op's
// own op<N> id and patch_page kind — never the parent rename's — plus the
// From/To pair from the rename itself.
func TestRenameCascadeOneFilePerBacklink(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("rename cascade", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	from, to := "wiki/concepts/flash-attention.md", "wiki/concepts/flash-attention-v2.md"
	id, err := e.Append(Op{Kind: OpRenamePage, From: from, To: to})
	if err != nil {
		t.Fatalf("Append rename_page: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	renameOp, ok := c.Op(id)
	if !ok {
		t.Fatalf("rename op %s not found", id)
	}
	if len(renameOp.Cascade) == 0 {
		t.Fatal("expected a non-empty cascade for flash-attention.md's backlinks")
	}

	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	fromEntry := fileDiffByOp(d, id, from)
	toEntry := fileDiffByOp(d, id, to)
	if fromEntry == nil {
		t.Fatalf("missing rename From entry for op %s at %s", id, from)
	}
	if toEntry == nil {
		t.Fatalf("missing rename To entry for op %s at %s", id, to)
	}
	if fromEntry.New != "" {
		t.Fatalf("rename From entry New = %q, want empty (the source disappears)", fromEntry.New)
	}
	if toEntry.Old != "" {
		t.Fatalf("rename To entry Old = %q, want empty (the destination is new)", toEntry.Old)
	}
	if toEntry.New == "" {
		t.Fatal("rename To entry New is empty, want the source page's content")
	}

	for _, sub := range renameOp.Cascade {
		fd := fileDiffByOp(d, sub.ID, sub.Path)
		if fd == nil {
			t.Fatalf("no FileDiff for cascade sub-op %s at %s", sub.ID, sub.Path)
		}
		if fd.Kind != OpPatchPage {
			t.Fatalf("cascade FileDiff for %s has Kind %s, want patch_page", sub.Path, fd.Kind)
		}
		if fd.OpID == id {
			t.Fatalf("cascade FileDiff for %s carries the parent rename's id %s, want its own", sub.Path, id)
		}
	}

	// Exactly one FileDiff per cascade sub-op path — no duplicates, no
	// entries invented beyond what the cascade actually lists.
	gotCascadeFiles := 0
	for _, fd := range d.Files {
		if fd.Kind == OpPatchPage {
			for _, sub := range renameOp.Cascade {
				if fd.OpID == sub.ID && fd.Path == sub.Path {
					gotCascadeFiles++
				}
			}
		}
	}
	if gotCascadeFiles != len(renameOp.Cascade) {
		t.Fatalf("cascade FileDiffs = %d, want exactly %d (one per backlink)", gotCascadeFiles, len(renameOp.Cascade))
	}
}

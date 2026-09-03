package stage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- shared test helpers -------------------------------------------------

// allTouchedPaths returns every path touched by ops, top-level and
// cascade, recursively (op.go's opTouches already recurses through
// Cascade).
func allTouchedPaths(ops []Op) []string {
	var out []string
	for _, op := range ops {
		out = append(out, opTouches(op)...)
	}
	return out
}

// containsString reports whether s is in ss.
func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// lastRevertedSkipped returns the "skipped" payload of the most recent
// reverted journal event, or nil when that event carries no Data (backbone
// §5.8's Revert Contract step 6: the key is omitted entirely when nothing
// was skipped).
func lastRevertedSkipped(t *testing.T, e *Engine) []string {
	t.Helper()
	events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvReverted}})
	if err != nil {
		t.Fatalf("Journal().Query(EvReverted): %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no reverted event in the journal")
	}
	last := events[len(events)-1]
	if len(last.Data) == 0 {
		return nil
	}
	var payload struct {
		Skipped []string `json:"skipped"`
	}
	if err := json.Unmarshal(last.Data, &payload); err != nil {
		t.Fatalf("unmarshal reverted event data %s: %v", last.Data, err)
	}
	return payload.Skipped
}

// assertSnapshotsMatchExcludingHistory asserts that got and want agree on
// every path except log.md/log-<year>.md (D-BV, excluded from the revert
// delta entirely and so irrelevant to a round-trip assertion) and every
// path in allowExtra (a create_page's tombstone, D-BX, present in got with
// different content than the pre-commit tree it is compared against,
// which never had that path at all).
func assertSnapshotsMatchExcludingHistory(t *testing.T, got, want Snapshot, allowExtra map[string]bool) {
	t.Helper()
	for p, wantSHA := range want {
		if revertHistoryPathPattern.MatchString(p) {
			continue
		}
		gotSHA, ok := got[p]
		if !ok {
			t.Errorf("round-tripped tree is missing %s (want sha %s)", p, wantSHA)
			continue
		}
		if gotSHA != wantSHA {
			t.Errorf("round-tripped tree: %s = %s, want %s", p, gotSHA, wantSHA)
		}
	}
	for p := range got {
		if revertHistoryPathPattern.MatchString(p) || allowExtra[p] {
			continue
		}
		if _, ok := want[p]; !ok {
			t.Errorf("round-tripped tree has unexpected extra path %s", p)
		}
	}
}

// seedCreatePage opens a changeset, appends a single well-formed
// create_page op at path, and commits it — the minimal scaffolding every
// test below needs at least once to get a commit to revert.
func seedCreatePage(t *testing.T, e *Engine, path, title string) string {
	t.Helper()
	if _, err := e.OpenChangeset("seed "+path, testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       path,
		Content:    newConceptPageContent(title),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	commitID, err := e.Commit("seed " + path)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return commitID
}

// --- the six required tests ----------------------------------------------

// TestRevertRoundTripsTree seeds a commit, then a create_page commit
// mirroring G2's own create-page.json shape, then reverts and commits the
// revert: every non-history entry must match snapshot(commitID-1) exactly,
// except the created path, which must hold a tombstone, not nothing
// (backbone §5.8, MASTER §9 D-BX: no op kind in the frozen §5.3 vocabulary
// can make a path absent).
func TestRevertRoundTripsTree(t *testing.T) {
	e, dir := newTestEngine(t)

	seedCreatePage(t, e, "wiki/concepts/seed-page.md", "Seed Page")

	baseline, err := e.Snapshot("000001")
	if err != nil {
		t.Fatalf("Snapshot(000001): %v", err)
	}

	const newPath = "wiki/concepts/new-page.md"
	commitID := seedCreatePage(t, e, newPath, "New Page")
	if commitID != "000002" {
		t.Fatalf("commit id = %q, want 000002", commitID)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}
	if len(revertCS.Ops) != 1 || revertCS.Ops[0].Kind != OpRetract || revertCS.Ops[0].Path != newPath {
		t.Fatalf("revert ops = %+v, want exactly one retract of %s", revertCS.Ops, newPath)
	}

	revertCommitID, err := e.Commit("revert " + commitID)
	if err != nil {
		t.Fatalf("Commit (revert): %v", err)
	}
	if revertCommitID != "000003" {
		t.Fatalf("revert commit id = %q, want 000003", revertCommitID)
	}

	final, err := e.buildSnapshot()
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	assertSnapshotsMatchExcludingHistory(t, final, baseline, map[string]bool{newPath: true})

	tomb, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(newPath)))
	if err != nil {
		t.Fatalf("read tombstoned %s: %v", newPath, err)
	}
	if !strings.Contains(string(tomb), "retracted:") || !strings.Contains(string(tomb), "**Retracted.**") {
		t.Fatalf("%s = %q, want a retract tombstone in place, not an absent file (D-BX)", newPath, tomb)
	}
}

// TestRevertRenameRoundTripsExactly is the byte-exact case: commit a
// rename_page, revert it, commit the revert, and assert the tree is
// identical to the pre-rename snapshot, index.md included (backbone §5.8's
// first inversion-table row, MASTER §9 D-BW/D-BI: the paired rename_page's
// own cascade repairs index.md and every linking page for free).
func TestRevertRenameRoundTripsExactly(t *testing.T) {
	e, dir := newTestEngine(t)

	const from = "wiki/concepts/kv-cache.md"
	const to = "wiki/concepts/kv-caching.md"

	origIndex, err := os.ReadFile(filepath.Join(dir, "index.md"))
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}

	if _, err := e.OpenChangeset("rename kv-cache", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{Kind: OpRenamePage, From: from, To: to}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	commitID, err := e.Commit("rename kv-cache")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	baseline, err := e.Snapshot("000000")
	if err != nil {
		t.Fatalf("Snapshot(000000): %v", err)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}
	if len(revertCS.Ops) != 1 || revertCS.Ops[0].Kind != OpRenamePage {
		t.Fatalf("revert ops = %+v, want exactly one rename_page", revertCS.Ops)
	}
	if got := revertCS.Ops[0]; got.From != to || got.To != from {
		t.Fatalf("revert rename From=%q To=%q, want From=%q To=%q", got.From, got.To, to, from)
	}

	// Still just a proposal: the source of the ORIGINAL rename (kv-caching)
	// has not moved back yet.
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(from))); !os.IsNotExist(err) {
		t.Fatalf("%s exists before the revert is committed (stat err = %v)", from, err)
	}

	if _, err := e.Commit("revert " + commitID); err != nil {
		t.Fatalf("Commit (revert): %v", err)
	}

	final, err := e.buildSnapshot()
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	assertSnapshotsMatchExcludingHistory(t, final, baseline, nil)

	gotIndex, err := os.ReadFile(filepath.Join(dir, "index.md"))
	if err != nil {
		t.Fatalf("re-read index.md: %v", err)
	}
	if string(gotIndex) != string(origIndex) {
		t.Fatalf("index.md after the round trip:\n%s\nwant (pre-rename):\n%s", gotIndex, origIndex)
	}
}

// TestRevertOpensChangesetNotApplies asserts that Revert only proposes:
// the working tree, the loaded Vault and snapshots/ are all untouched
// until the revert changeset is itself committed (backbone §5.8, /PLAN.md
// §7 — "never apply directly").
func TestRevertOpensChangesetNotApplies(t *testing.T) {
	e, dir := newTestEngine(t)

	const path = "wiki/concepts/probe-page.md"
	commitID := seedCreatePage(t, e, path, "Probe Page")

	beforeContent, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read %s before revert: %v", path, err)
	}
	snapshotsDir := filepath.Join(dir, ".llmwiki", "snapshots")
	beforeEntries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		t.Fatalf("read snapshots dir: %v", err)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}

	afterContent, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read %s after revert: %v", path, err)
	}
	if string(afterContent) != string(beforeContent) {
		t.Fatalf("%s changed on disk merely by proposing a revert; Revert must not touch the working tree", path)
	}

	afterEntries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		t.Fatalf("re-read snapshots dir: %v", err)
	}
	if len(afterEntries) != len(beforeEntries) {
		t.Fatalf("snapshots/ gained an entry merely by proposing a revert: before=%d after=%d", len(beforeEntries), len(afterEntries))
	}

	if _, ok := e.Vault().Page(path); !ok {
		t.Fatalf("%s no longer present in the loaded vault after a mere proposal", path)
	}

	if _, err := os.Stat(filepath.Join(dir, ".llmwiki", "changesets", "open", revertCS.ID, "changeset.json")); err != nil {
		t.Fatalf("revert changeset not persisted under changesets/open/: %v", err)
	}
}

// TestRevertExcludesHistory asserts that no op the returned changeset
// carries — top-level or cascade — names log.md or a log-<year>.md
// (backbone §5.8, MASTER §9 D-BV), in two shapes: an ordinary log.md that
// differs between two consecutive snapshots (every commit after the
// first), and a log-<year>.md a rotation created between them. Both are
// excluded from the delta entirely, not attempted and reported as skipped.
func TestRevertExcludesHistory(t *testing.T) {
	t.Run("log.md", func(t *testing.T) {
		e, _ := newTestEngine(t)

		seedCreatePage(t, e, "wiki/concepts/hist-seed.md", "Hist Seed")
		commitID := seedCreatePage(t, e, "wiki/concepts/hist-second.md", "Hist Second")
		if commitID != "000002" {
			t.Fatalf("commit id = %q, want 000002 — log.md only differs between snapshots from the second commit on", commitID)
		}

		revertCS, err := e.Revert(commitID)
		if err != nil {
			t.Fatalf("Revert(%s): %v", commitID, err)
		}
		for _, p := range allTouchedPaths(revertCS.Ops) {
			if revertHistoryPathPattern.MatchString(p) {
				t.Errorf("revert op touches history path %s", p)
			}
		}
		if skipped := lastRevertedSkipped(t, e); containsString(skipped, "log.md") {
			t.Errorf("log.md reported as skipped; it must be excluded from the delta entirely, not attempted and skipped")
		}
	})

	t.Run("log-year.md rotation", func(t *testing.T) {
		e, dir := newTestEngine(t)

		var seed strings.Builder
		seed.WriteString("# Log\n\n")
		for i := 1; i <= logRotateThreshold; i++ {
			fmt.Fprintf(&seed, "- 2020-01-01 00:00 000000 seed entry %d (+0 pages, ~0 edits)\n", i)
		}
		if err := os.WriteFile(filepath.Join(dir, "log.md"), []byte(seed.String()), 0o644); err != nil {
			t.Fatalf("seed log.md: %v", err)
		}

		seedCreatePage(t, e, "wiki/concepts/rotate-me.md", "Rotate Me")
		if _, err := os.Stat(filepath.Join(dir, "log-2020.md")); err != nil {
			t.Fatalf("log-2020.md was not created by rotation: %v", err)
		}

		commitID := seedCreatePage(t, e, "wiki/concepts/after-rotate.md", "After Rotate")

		revertCS, err := e.Revert(commitID)
		if err != nil {
			t.Fatalf("Revert(%s): %v", commitID, err)
		}
		for _, p := range allTouchedPaths(revertCS.Ops) {
			if revertHistoryPathPattern.MatchString(p) {
				t.Errorf("revert op touches history path %s", p)
			}
		}
		if skipped := lastRevertedSkipped(t, e); containsString(skipped, "log-2020.md") {
			t.Errorf("log-2020.md reported as skipped; it must be excluded from the delta entirely")
		}
	})
}

// TestRevertReportsSkippedPaths reverts an ingest_source commit: the
// changeset still opens, the raw/ path is not an op (a RawSource is not a
// vault.Page, so no op kind can invert it — MASTER §9 D-BY), and it
// appears both in the reverted event's Data and in the changeset's Intent
// (D-BZ — never dropped silently).
func TestRevertReportsSkippedPaths(t *testing.T) {
	e, _ := newTestEngine(t)

	const rawPath = "raw/papers/appended-source.md"
	if _, err := e.OpenChangeset("ingest", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:      OpIngestSource,
		Path:      rawPath,
		Extractor: "go/html",
		Content:   []byte("Unique ingest content.\n"),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	commitID, err := e.Commit("ingest a source")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}

	for _, p := range allTouchedPaths(revertCS.Ops) {
		if p == rawPath {
			t.Fatalf("revert changeset carries an op for %s, which no op kind can invert (D-BY)", rawPath)
		}
	}
	if !strings.Contains(revertCS.Intent, rawPath) {
		t.Errorf("changeset Intent = %q, want it to mention the skipped path %s", revertCS.Intent, rawPath)
	}

	skipped := lastRevertedSkipped(t, e)
	if !containsString(skipped, rawPath) {
		t.Fatalf("reverted event skipped = %v, want it to contain %s", skipped, rawPath)
	}
}

// TestRevertOfFirstCommit reverts commit 000001 — the D-BU baseline case
// gate G2 exercises. The delta must be exactly that commit's own effect,
// not the whole pre-existing vault: a snapshot holds the whole tree, so
// treating a missing predecessor as empty would make every pre-existing
// page look ADDED and propose retracting the entire fixture.
func TestRevertOfFirstCommit(t *testing.T) {
	e, _ := newTestEngine(t)

	const path = "wiki/concepts/first-commit-probe.md"
	commitID := seedCreatePage(t, e, path, "First Commit Probe")
	if commitID != "000001" {
		t.Fatalf("commit id = %q, want 000001", commitID)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v — the D-BU baseline snapshot(000000) must exist", commitID, err)
	}
	if len(revertCS.Ops) != 1 || revertCS.Ops[0].Kind != OpRetract || revertCS.Ops[0].Path != path {
		t.Fatalf("revert ops = %+v, want exactly one retract of %s — the delta must be this commit's own effect, not the whole pre-existing vault", revertCS.Ops, path)
	}
}

// TestRevertMergeReportsUncoveredPaths is repair-1: a merge_pages's added
// and removed sides never share a sha (the merged body differs from every
// source), so no rename pairing ever fires and buildRevertOps's "covered"
// set stays empty — yet the merge's own cascade still rewrites index.md
// (C-58: not a vault.Page, so no op kind can address it). Before the
// repair-1 fix, that left index.md CHANGED in the delta with no op and no
// entry in skipped: a silent drop, exactly what MASTER §9 D-BY/D-BZ
// forbid. This test was proved to fail against the pre-fix code (see this
// subtask's report) and passes against the fix: index.md appears in
// neither Op, but does appear in both the Intent and the reverted event's
// skipped list.
func TestRevertMergeReportsUncoveredPaths(t *testing.T) {
	e, _ := newTestEngine(t)

	sources := []string{"wiki/concepts/kv-cache.md", "wiki/concepts/flash-attention.md"}
	const mergedPath = "wiki/concepts/attention.md"

	if _, err := e.OpenChangeset("merge kv-cache and flash-attention", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{Kind: OpMergePages, Sources: sources, To: mergedPath}); err != nil {
		t.Fatalf("Append merge_pages: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       mergedPath,
		Content:    newConceptPageContent("Attention"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append create_page (merged): %v", err)
	}
	commitID, err := e.Commit("merge kv-cache and flash-attention")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}

	for _, p := range allTouchedPaths(revertCS.Ops) {
		if p == "index.md" {
			t.Fatalf("revert changeset carries an op touching index.md, which no op kind can address (C-58)")
		}
	}
	if !strings.Contains(revertCS.Intent, "index.md") {
		t.Errorf("changeset Intent = %q, want it to mention the uncovered path index.md", revertCS.Intent)
	}
	skipped := lastRevertedSkipped(t, e)
	if !containsString(skipped, "index.md") {
		t.Fatalf("reverted event skipped = %v, want it to contain index.md (buildRevertOps must not drop an uncovered non-page CHANGED path silently)", skipped)
	}
}

// TestRevertSplitPageDoesNotDropPaths covers a split_page revert: commit a
// split (one source moved to tombstones, two new product pages created —
// apply.go's planOp gives split_page no cascade, unlike rename/merge, so
// this shape cannot itself trigger the index.md-style uncovered-CHANGED
// case repair-1 fixes; see this subtask's report), then assert every path
// the original commit touched — the source and both products — is
// accounted for by the revert: named by an op, or named in skipped. Never
// silently absent from both.
func TestRevertSplitPageDoesNotDropPaths(t *testing.T) {
	e, _ := newTestEngine(t)

	const source = "wiki/concepts/kv-cache.md"
	products := []string{"wiki/concepts/kv-cache-part-a.md", "wiki/concepts/kv-cache-part-b.md"}

	if _, err := e.OpenChangeset("split kv-cache", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{Kind: OpSplitPage, Path: source, Sources: products}); err != nil {
		t.Fatalf("Append split_page: %v", err)
	}
	for _, p := range products {
		if _, err := e.Append(Op{
			Kind:       OpCreatePage,
			Path:       p,
			Content:    newConceptPageContent("Part of KV Cache"),
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}); err != nil {
			t.Fatalf("Append create_page (%s): %v", p, err)
		}
	}
	commitID, err := e.Commit("split kv-cache")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}

	touched := allTouchedPaths(revertCS.Ops)
	skipped := lastRevertedSkipped(t, e)
	for _, p := range append([]string{source}, products...) {
		if !containsString(touched, p) && !containsString(skipped, p) {
			t.Errorf("%s is named by no op and not in skipped; the split's own effect on it was silently dropped", p)
		}
	}

	// The source must come back as a create_page (D-BW: REMOVED, unpaired
	// -> create_page), and each product must come back as a retract
	// (D-BW: ADDED, unpaired, under wiki/ -> retract) — none of the three
	// should have needed to be skipped in this scenario.
	if containsString(skipped, source) || containsString(skipped, products[0]) || containsString(skipped, products[1]) {
		t.Errorf("skipped = %v, want none of %s/%s/%s skipped for a plain split", skipped, source, products[0], products[1])
	}
	var gotCreate, gotRetracts int
	for _, op := range revertCS.Ops {
		switch {
		case op.Kind == OpCreatePage && op.Path == source:
			gotCreate++
		case op.Kind == OpRetract && containsString(products, op.Path):
			gotRetracts++
		}
	}
	if gotCreate != 1 {
		t.Errorf("got %d create_page ops for %s, want 1", gotCreate, source)
	}
	if gotRetracts != len(products) {
		t.Errorf("got %d retract ops for the split's products, want %d", gotRetracts, len(products))
	}
}

// --- extra coverage, still filtered by the "TestRevert" prefix -----------

// TestRevertPatchPageRoundTrips exercises the CHANGED-that-is-a-vault.Page
// row of backbone §5.8's inversion table directly, without a rename in
// the way: an ordinary section edit reverts and round-trips byte for
// byte, and the produced patch_page carries at least one hunk (the
// schema's patch_page branch requires "hunks" with minItems 1 — nothing
// in ValidateOp enforces that in Go, so this is the test that would catch
// a caller who forgot it).
func TestRevertPatchPageRoundTrips(t *testing.T) {
	e, dir := newTestEngine(t)

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	origBytes := page.Serialize()
	oldLine := "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newLine := "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
	if !bodyContains(page.Body, oldLine) {
		t.Fatalf("fixture body does not contain %q", oldLine)
	}
	rewritten := *page
	rewritten.Body = replaceLine(page.Body, oldLine, newLine)

	if _, err := e.OpenChangeset("edit kv-cache", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    page.Path,
		Section: "## Related",
		Before:  page.SHA256(),
		Content: rewritten.Serialize(),
		Hunks: []Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldLine}, Add: []string{newLine}},
		},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	commitID, err := e.Commit("edit kv-cache")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	baseline, err := e.Snapshot("000000")
	if err != nil {
		t.Fatalf("Snapshot(000000): %v", err)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}
	if len(revertCS.Ops) != 1 || revertCS.Ops[0].Kind != OpPatchPage || revertCS.Ops[0].Path != page.Path {
		t.Fatalf("revert ops = %+v, want exactly one patch_page for %s", revertCS.Ops, page.Path)
	}
	if len(revertCS.Ops[0].Hunks) == 0 {
		t.Fatalf("revert patch_page carries no hunks; the schema requires at least one")
	}

	if _, err := e.Commit("revert " + commitID); err != nil {
		t.Fatalf("Commit (revert): %v", err)
	}

	final, err := e.buildSnapshot()
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	assertSnapshotsMatchExcludingHistory(t, final, baseline, nil)

	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(page.Path)))
	if err != nil {
		t.Fatalf("re-read %s: %v", page.Path, err)
	}
	if string(got) != string(origBytes) {
		t.Fatalf("%s after the round trip:\n%s\nwant (original):\n%s", page.Path, got, origBytes)
	}
}

// TestRevertProducesSchemaValidChangeset guards the invariant §5's
// Contract states for every changeset.json this package writes ("the JSON
// must validate against spec/changeset.schema.json ... from the moment it
// is opened", D-BA): the changeset Revert returns — a rename_page whose
// cascade covers index.md, plus every field this file itself sets on that
// op — validates against the real, parsed schema, not a hand-duplicated
// copy of its rules.
func TestRevertProducesSchemaValidChangeset(t *testing.T) {
	e, _ := newTestEngine(t)

	const from = "wiki/concepts/kv-cache.md"
	const to = "wiki/concepts/kv-caching.md"
	if _, err := e.OpenChangeset("rename kv-cache", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{Kind: OpRenamePage, From: from, To: to}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	commitID, err := e.Commit("rename kv-cache")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}

	root, schema := loadChangesetSchema(t)
	assertValidatesAgainstSchema(t, root, schema, revertCS)
}

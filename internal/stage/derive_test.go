package stage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// --- derive.go unit tests, isolated from *Engine -------------------------

// TestDeriveIndexSectionForEveryPageType pins S2-T8 rule (a)'s table: every
// one of the five PageType values maps to its own "## <Section>" heading.
func TestDeriveIndexSectionForEveryPageType(t *testing.T) {
	cases := []struct {
		typ  vault.PageType
		want string
	}{
		{vault.TypeEntity, "## Entities"},
		{vault.TypeConcept, "## Concepts"},
		{vault.TypeComparison, "## Comparisons"},
		{vault.TypeQuery, "## Queries"},
		{vault.TypeSummary, "## Summaries"},
	}
	for _, c := range cases {
		t.Run(string(c.typ), func(t *testing.T) {
			if got := indexSectionFor(c.typ); got != c.want {
				t.Errorf("indexSectionFor(%s) = %q, want %q", c.typ, got, c.want)
			}
		})
	}
}

// TestDeriveInsertIndexLineEdges pins insertion rules 1-5, each measured
// against a concrete before/after string so a regression in any one rule
// fails a specific subtest rather than a vague golden diff.
func TestDeriveInsertIndexLineEdges(t *testing.T) {
	cases := []struct {
		name              string
		content           string
		section, basename string
		title             string
		want              string
	}{
		{
			name:    "section present with content, not last",
			content: "# Index\n\n## Concepts\n\n- [[a]] — A.\n\n## Entities\n\n- [[b]] — B.\n",
			section: "## Concepts", basename: "c", title: "C",
			want: "# Index\n\n## Concepts\n\n- [[a]] — A.\n- [[c]] — C\n\n## Entities\n\n- [[b]] — B.\n",
		},
		{
			name:    "section absent",
			content: "# Index\n\n## Entities\n\n- [[b]] — B.\n",
			section: "## Concepts", basename: "c", title: "C",
			want: "# Index\n\n## Entities\n\n- [[b]] — B.\n\n## Concepts\n\n- [[c]] — C\n",
		},
		{
			name:    "section present but empty",
			content: "# Index\n\n## Concepts\n\n## Entities\n\n- [[b]] — B.\n",
			section: "## Concepts", basename: "c", title: "C",
			want: "# Index\n\n## Concepts\n\n- [[c]] — C\n\n## Entities\n\n- [[b]] — B.\n",
		},
		{
			name:    "section present with content, last in file",
			content: "# Index\n\n## Entities\n\n- [[b]] — B.\n",
			section: "## Entities", basename: "c", title: "C",
			want: "# Index\n\n## Entities\n\n- [[b]] — B.\n- [[c]] — C\n",
		},
		{
			name:    "section empty and last in file",
			content: "# Index\n\n## Entities\n",
			section: "## Entities", basename: "c", title: "C",
			want: "# Index\n\n## Entities\n\n- [[c]] — C\n",
		},
		{
			name:    "file without a trailing newline",
			content: "# Index\n\n## Entities\n\n- [[b]] — B.",
			section: "## Entities", basename: "c", title: "C",
			want: "# Index\n\n## Entities\n\n- [[b]] — B.\n- [[c]] — C\n",
		},
		{
			name:    "already present is idempotent",
			content: "# Index\n\n## Entities\n\n- [[c]] — C\n",
			section: "## Entities", basename: "c", title: "C (a different title should not matter)",
			want: "# Index\n\n## Entities\n\n- [[c]] — C\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := insertIndexLine(c.content, c.section, c.basename, c.title)
			if got != c.want {
				t.Errorf("insertIndexLine() =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// TestDeriveIndexComposesInOpOrder pins rules 6-7 directly against
// deriveIndex, without an Engine: two live create_page ops fold into the
// running content in order, each insertion building on what the previous
// one produced.
func TestDeriveIndexComposesInOpOrder(t *testing.T) {
	running := []byte("# Index\n\n## Concepts\n\n- [[existing]] — Existing.\n")
	creates := []Op{
		{ID: "op1", Kind: OpCreatePage, Path: "wiki/concepts/alpha.md", State: StateProposed},
		{ID: "op2", Kind: OpCreatePage, Path: "wiki/entities/beta.md", State: StateProposed},
	}
	bodies := map[string][]byte{
		"wiki/concepts/alpha.md": []byte("---\ntitle: Alpha\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: concept\n---\n\nBody.\n"),
		"wiki/entities/beta.md":  []byte("---\ntitle: Beta\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: entity\n---\n\nBody.\n"),
	}
	post := func(op Op) ([]byte, error) { return bodies[op.Path], nil }

	got, err := deriveIndex(running, creates, post)
	if err != nil {
		t.Fatalf("deriveIndex: %v", err)
	}
	want := "# Index\n\n## Concepts\n\n- [[existing]] — Existing.\n- [[alpha]] — Alpha\n\n## Entities\n\n- [[beta]] — Beta\n"
	if string(got) != want {
		t.Errorf("deriveIndex() =\n%q\nwant\n%q", got, want)
	}
}

// TestDeriveLiveCreatePagesSkipsDroppedAndRejected pins liveCreatePages'
// per-node walk: a dropped top-level op, a rejected cascade sub-op, and a
// non-create op are all excluded, while a live create_page nested inside a
// live cascade is still found — order preserved (rule 6).
func TestDeriveLiveCreatePagesSkipsDroppedAndRejected(t *testing.T) {
	ops := []Op{
		{ID: "op1", Kind: OpCreatePage, Path: "a.md", State: StateProposed},
		{ID: "op2", Kind: OpCreatePage, Path: "b.md", State: StateDropped},
		{
			ID: "op3", Kind: OpRenamePage, State: StateProposed,
			Cascade: []Op{
				{ID: "op4", Kind: OpCreatePage, Path: "c.md", State: StateProposed},
				{ID: "op5", Kind: OpCreatePage, Path: "d.md", State: StateRejected},
			},
		},
		{ID: "op6", Kind: OpPatchPage, Path: "e.md", State: StateProposed},
	}

	got := liveCreatePages(ops)
	var paths []string
	for _, op := range got {
		paths = append(paths, op.Path)
	}
	want := []string{"a.md", "c.md"}
	if !equalStrings(paths, want) {
		t.Errorf("liveCreatePages paths = %v, want %v", paths, want)
	}
}

// TestDeriveSplitStubShape golden-compares splitStub's exact serialized
// bytes against a checked-in golden under this package's own testdata/
// (00-conventions.md §6: never spec/fixtures/, which testutil.Golden would
// silently overwrite under -update).
func TestDeriveSplitStubShape(t *testing.T) {
	e, _ := newTestEngine(t)
	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}

	got := splitStub(page, []string{"wiki/concepts/kv-cache-part-a.md", "wiki/concepts/kv-cache-part-b.md"}, "2026-08-30")
	testutil.Golden(t, filepath.Join("testdata", "split-stub-golden.want"), got)
}

// --- end-to-end, through *Engine ------------------------------------------

// TestT8CreatePageCommitsLintClean pins S2-T8's stated goal directly: a
// create_page commit on the minimal fixture lints clean. Before this
// subtask's fix it is 1 index-sync error (the created page has no
// index.md line).
func TestT8CreatePageCommitsLintClean(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("add a page", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/new-concept.md",
		Content:    newConceptPageContent("New Concept"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append create_page: %v", err)
	}
	if _, err := e.Commit("add a page"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	report := lint.Run(e.vaultLintContext(), nil)
	if report.Errors != 0 {
		t.Fatalf("Errors = %d, want 0: %+v", report.Errors, report.Findings)
	}
}

// TestT8TwoCreatesInOneChangesetBothIndexed is the ⚠️ box's own scenario:
// two create_page ops that each carry a whole-file post-image of
// index.md in the SAME changeset must not clobber each other. Before this
// subtask's fix, planOp/applyOp's last-write-wins m.writes["index.md"]
// loses the first create's line entirely.
func TestT8TwoCreatesInOneChangesetBothIndexed(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("add two pages", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	pages := []struct{ path, title, basename string }{
		{"wiki/concepts/alpha-concept.md", "Alpha Concept", "alpha-concept"},
		{"wiki/concepts/beta-concept.md", "Beta Concept", "beta-concept"},
	}
	for _, p := range pages {
		if _, err := e.Append(Op{
			Kind:       OpCreatePage,
			Path:       p.path,
			Content:    newConceptPageContent(p.title),
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}); err != nil {
			t.Fatalf("Append create_page(%s): %v", p.path, err)
		}
	}
	if _, err := e.Commit("add two pages"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	idx, err := e.vault.Read("index.md")
	if err != nil {
		t.Fatalf("Read index.md: %v", err)
	}
	for _, p := range pages {
		if !strings.Contains(string(idx), "[["+p.basename+"]]") {
			t.Errorf("index.md missing [[%s]] — the two creates clobbered each other", p.basename)
		}
	}

	report := lint.Run(e.vaultLintContext(), nil)
	if report.Errors != 0 {
		t.Fatalf("Errors = %d, want 0: %+v", report.Errors, report.Findings)
	}
}

// TestT8SplitPageCommitsLintClean pins the measured C-66 baseline (7
// errors: 4 link-broken + 3 index-sync on minimal, splitting kv-cache.md)
// going to 0 errors, with the source path left holding a stub
// that links to both products. 014 amendment (workflow §9 A6): the stub
// and the two new parts each carry no ## Abstract, so the committed vault
// reports exactly 3 page-abstract warns.
func TestT8SplitPageCommitsLintClean(t *testing.T) {
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
			t.Fatalf("Append create_page(%s): %v", p, err)
		}
	}
	if _, err := e.Commit("split kv-cache"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	report := lint.Run(e.vaultLintContext(), nil)
	// 014 amendment (workflow §9 A6): 0/0 → 0 errors, 3 warns (one
	// page-abstract each for the stub and the two parts).
	if report.Errors != 0 || report.Warns != 3 {
		t.Fatalf("Errors=%d Warns=%d, want 0/3: %+v", report.Errors, report.Warns, report.Findings)
	}

	page, ok := e.vault.Page(source)
	if !ok {
		t.Fatalf("%s no longer exists after split", source)
	}
	if !strings.Contains(page.Body, "> **Split.**") {
		t.Errorf("stub body = %q, want a > **Split.** block", page.Body)
	}
	for _, p := range products {
		base := basenameNoExt(p)
		if !strings.Contains(page.Body, "[["+base+"]]") {
			t.Errorf("stub body does not link to %s", base)
		}
	}
}

// TestT8RetractKeepsIndexLine pins rule (b): after a retract commits,
// index.md still names the retracted (now tombstoned) page, and the
// commit lints clean. Removing the line would itself be an index-sync
// error, since the tombstone is still a vault.Page.
func TestT8RetractKeepsIndexLine(t *testing.T) {
	e, _ := newTestEngine(t)
	const path = "wiki/concepts/flash-attention.md"

	if _, err := e.OpenChangeset("retract flash-attention", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{Kind: OpRetract, Path: path, Rationale: "superseded"}); err != nil {
		t.Fatalf("Append retract: %v", err)
	}
	if _, err := e.Commit("retract flash-attention"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	idx, err := e.vault.Read("index.md")
	if err != nil {
		t.Fatalf("Read index.md: %v", err)
	}
	if !strings.Contains(string(idx), "[[flash-attention]]") {
		t.Errorf("index.md lost the retracted page's line")
	}

	report := lint.Run(e.vaultLintContext(), nil)
	if report.Errors != 0 {
		t.Fatalf("Errors = %d, want 0: %+v", report.Errors, report.Findings)
	}
}

// TestT8ProjectionMatchesCommit pins C-65: the open changeset's projected
// Checks must equal the lint result of the tree Commit actually produces,
// for both a retract and a split. Before this subtask's fix,
// projection.go's applyOp deletes both paths outright while apply.go's
// planOp writes a page in place, so the two disagree.
func TestT8ProjectionMatchesCommit(t *testing.T) {
	t.Run("retract", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("retract flash-attention", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		if _, err := e.Append(Op{Kind: OpRetract, Path: "wiki/concepts/flash-attention.md", Rationale: "superseded"}); err != nil {
			t.Fatalf("Append retract: %v", err)
		}
		assertProjectionMatchesCommit(t, e, "retract flash-attention")
	})

	t.Run("split", func(t *testing.T) {
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
				t.Fatalf("Append create_page(%s): %v", p, err)
			}
		}
		assertProjectionMatchesCommit(t, e, "split kv-cache")
	})
}

// assertProjectionMatchesCommit captures the currently open changeset's
// projected Checks, commits it, and asserts Checks.Lint/Orphans/BrokenLinks
// equal the lint result and graph state of the tree the commit actually
// produced.
func assertProjectionMatchesCommit(t *testing.T, e *Engine, message string) {
	t.Helper()

	current, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	projected := current.Checks

	if _, err := e.Commit(message); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	report := lint.Run(e.vaultLintContext(), nil)
	wantLint := "pass"
	if !report.Clean() {
		wantLint = "fail"
	}
	if projected.Lint != wantLint {
		t.Errorf("open changeset Checks.Lint = %q, want %q (the post-commit reality)", projected.Lint, wantLint)
	}

	graph := e.vault.Graph()
	if projected.Orphans != len(graph.Orphans()) {
		t.Errorf("projected Checks.Orphans = %d, want %d", projected.Orphans, len(graph.Orphans()))
	}
	if projected.BrokenLinks != len(graph.Broken()) {
		t.Errorf("projected Checks.BrokenLinks = %d, want %d", projected.BrokenLinks, len(graph.Broken()))
	}
}

// TestT8CreateComposesWithRename covers the ⚠️ box's compositional claim
// end to end: a rename_page's cascade rewrites index.md, and a sibling
// create_page in the SAME changeset derives a new line — both must land in
// the one index.md Commit writes. Renames wiki/concepts/flash-attention.md
// rather than kv-cache.md or gpt-4.md deliberately: newConceptPageContent
// hard-codes outbound links to both of those, so renaming either would
// break the new page's own links and the test would be chasing that
// probe artefact instead of this rule (per this subtask's brief).
func TestT8CreateComposesWithRename(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("rename and create", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	const from, to = "wiki/concepts/flash-attention.md", "wiki/concepts/flash-attention-v2.md"
	if _, err := e.Append(Op{Kind: OpRenamePage, From: from, To: to}); err != nil {
		t.Fatalf("Append rename_page: %v", err)
	}

	const newPath = "wiki/concepts/new-concept.md"
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       newPath,
		Content:    newConceptPageContent("New Concept"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append create_page: %v", err)
	}

	if _, err := e.Commit("rename and create"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	idx, err := e.vault.Read("index.md")
	if err != nil {
		t.Fatalf("Read index.md: %v", err)
	}
	content := string(idx)
	if strings.Contains(content, "[[flash-attention]]") {
		t.Errorf("index.md still links to the pre-rename name")
	}
	if !strings.Contains(content, "[[flash-attention-v2]]") {
		t.Errorf("index.md missing the rewritten rename link")
	}
	if !strings.Contains(content, "[[new-concept]]") {
		t.Errorf("index.md missing the new create's line — the rename cascade and the create clobbered each other")
	}

	report := lint.Run(e.vaultLintContext(), nil)
	if report.Errors != 0 {
		t.Fatalf("Errors = %d, want 0: %+v", report.Errors, report.Findings)
	}
}

// --- Diff()'s own derivation pass (the third surface) ---------------------

// indexFileDiff returns the single "index.md" entry in d.Files, or nil.
func indexFileDiff(d Diff) *FileDiff {
	for i := range d.Files {
		if d.Files[i].Path == "index.md" {
			return &d.Files[i]
		}
	}
	return nil
}

// TestT8DiffShowsDerivedIndexLine pins the first of the two measured
// Diff() defects: before this repair a lone create_page's Diff().Files
// carried no index.md entry at all, even though Commit writes one.
func TestT8DiffShowsDerivedIndexLine(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("add a page", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/brand-new.md",
		Content:    newConceptPageContent("Brand New"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append create_page: %v", err)
	}

	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	idx := indexFileDiff(d)
	if idx == nil {
		t.Fatalf("Diff().Files has no index.md entry for a create_page commit: %+v", d.Files)
	}
	if !strings.Contains(idx.New, "- [[brand-new]] — Brand New") {
		t.Errorf("index.md New = %q, want it to contain the derived line", idx.New)
	}

	working, err := e.vault.Read("index.md")
	if err != nil {
		t.Fatalf("Read index.md: %v", err)
	}
	if idx.Old != string(working) {
		t.Errorf("index.md Old = %q, want the working-tree content %q", idx.Old, working)
	}

	unified := d.UnifiedFile("index.md")
	if !strings.Contains(unified, "+- [[brand-new]] — Brand New") {
		t.Errorf("Unified rendering of index.md = %q, want a '+' line for the derived entry", unified)
	}
}

// TestT8DiffIndexComposesWithCascade pins the second measured Diff()
// defect: a rename_page cascade already produces an index.md FileDiff,
// and before this repair a sibling create_page's line never landed in it
// — the reviewer was shown the cascade's rewrite alone, which is not what
// Commit actually writes. Asserts the real invariant directly: Diff's
// index.md New equals the projected tree's index.md byte for byte.
func TestT8DiffIndexComposesWithCascade(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("rename and create", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	const from, to = "wiki/concepts/flash-attention.md", "wiki/concepts/flash-attention-v2.md"
	if _, err := e.Append(Op{Kind: OpRenamePage, From: from, To: to}); err != nil {
		t.Fatalf("Append rename_page: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/new-concept.md",
		Content:    newConceptPageContent("New Concept"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append create_page: %v", err)
	}

	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	var idxEntries []FileDiff
	for _, fd := range d.Files {
		if fd.Path == "index.md" {
			idxEntries = append(idxEntries, fd)
		}
	}
	if len(idxEntries) != 1 {
		t.Fatalf("index.md entries in Diff().Files = %d, want exactly 1: %+v", len(idxEntries), idxEntries)
	}
	idx := idxEntries[0]

	if strings.Contains(idx.New, "[[flash-attention]]") {
		t.Errorf("index.md New still links to the pre-rename name: %q", idx.New)
	}
	if !strings.Contains(idx.New, "[[flash-attention-v2]]") {
		t.Errorf("index.md New missing the cascade's rewritten link: %q", idx.New)
	}
	if !strings.Contains(idx.New, "[[new-concept]]") {
		t.Errorf("index.md New missing the new create's derived line — the two writers disagreed: %q", idx.New)
	}

	// The real invariant: what the diff shows is what the commit writes.
	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	tree, err := e.projectedTree(c.Live())
	if err != nil {
		t.Fatalf("projectedTree: %v", err)
	}
	if idx.New != string(tree["index.md"]) {
		t.Errorf("Diff's index.md New does not match the projected tree byte for byte\ndiff: %q\nprojected: %q", idx.New, tree["index.md"])
	}

	// D-BQ: the cascade's persisted hunk ids must not renumber just
	// because New was replaced with the derived content.
	var wantHunks []Hunk
	for _, op := range c.Ops {
		if op.Kind != OpRenamePage {
			continue
		}
		for _, sub := range op.Cascade {
			if sub.Path == "index.md" {
				wantHunks = sub.Hunks
			}
		}
	}
	if wantHunks == nil {
		t.Fatal("test assumption broken: the rename's cascade has no index.md sub-op")
	}
	if len(idx.Hunks) != len(wantHunks) {
		t.Fatalf("Hunks has %d entries, want %d (must stay the persisted cascade hunks, D-BQ)", len(idx.Hunks), len(wantHunks))
	}
	for i := range wantHunks {
		if idx.Hunks[i].ID != wantHunks[i].ID {
			t.Errorf("Hunks[%d].ID = %q, want %q — hunk ids must not renumber (D-BQ)", i, idx.Hunks[i].ID, wantHunks[i].ID)
		}
	}
}

// TestT8DiffOmitsIndexWhenNothingDerived proves the negative: a changeset
// with no create_page and no cascade touching index.md must add no
// index.md FileDiff at all — never an empty/no-op entry.
func TestT8DiffOmitsIndexWhenNothingDerived(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("patch only", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
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

	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if idx := indexFileDiff(d); idx != nil {
		t.Fatalf("Diff().Files has an index.md entry when nothing derives one: %+v", *idx)
	}
}

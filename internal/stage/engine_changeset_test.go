package stage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestOneOpenChangeset pins /PLAN.md D7: OpenChangeset returns
// ErrOpenChangeset when changesets/open/ is already non-empty.
func TestOneOpenChangeset(t *testing.T) {
	e, _ := newTestEngine(t)

	if _, err := e.OpenChangeset("first", testAuthor); err != nil {
		t.Fatalf("first OpenChangeset: %v", err)
	}
	if _, err := e.OpenChangeset("second", testAuthor); !errors.Is(err, ErrOpenChangeset) {
		t.Fatalf("second OpenChangeset: got %v, want ErrOpenChangeset", err)
	}
}

// TestOpenChangesetWritesValidJSON pins C-33/D-BA: OpenChangeset must
// persist changeset.json — with Ops non-nil and Checks computed — before
// returning, and that file must validate against
// spec/changeset.schema.json while it still holds zero ops.
func TestOpenChangesetWritesValidJSON(t *testing.T) {
	e, dir := newTestEngine(t)

	c, err := e.OpenChangeset("test intent", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if c.Ops == nil {
		t.Fatal("OpenChangeset: c.Ops is nil, want a non-nil empty slice")
	}
	if c.Checks == (Checks{}) {
		t.Fatal("OpenChangeset: c.Checks is the zero value, want it computed")
	}

	path := filepath.Join(dir, ".llmwiki", "changesets", "open", c.ID, "changeset.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("changeset.json was not written before OpenChangeset returned: %v", err)
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Fatal("changeset.json must end with exactly one trailing newline")
	}

	root, s := loadChangesetSchema(t)
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("parse changeset.json: %v", err)
	}
	if errs := s.validate(root, generic, "$"); len(errs) > 0 {
		t.Fatalf("changeset.json does not validate against the schema:\n%s", joinLines(errs))
	}
}

// TestNextOpSurvivesReopen pins C-34/D-BB: e.nextOp is never carried by
// OpenEngine, so a SECOND Engine instance over the same vault must derive
// it from the persisted changeset — no id collision across the process
// boundary lw stage / lw stage really has.
func TestNextOpSurvivesReopen(t *testing.T) {
	e1, dir := newTestEngine(t)
	if _, err := e1.OpenChangeset("first process", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	id1, err := e1.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/first-page.md",
		Content:    newConceptPageContent("First Page"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	})
	if err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if id1 != "op1" {
		t.Fatalf("first Append id = %q, want op1", id1)
	}

	// A brand new Engine, as a second `lw stage` process would construct.
	e2, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("second OpenEngine: %v", err)
	}
	defer e2.Close()
	e2.now = e1.now

	id2, err := e2.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/second-page.md",
		Content:    newConceptPageContent("Second Page"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	})
	if err != nil {
		t.Fatalf("second Append (second Engine instance): %v", err)
	}
	if id2 != "op2" {
		t.Fatalf("second Append id = %q, want op2 (no collision with the first process's op1)", id2)
	}

	c, err := e2.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(c.Ops) != 2 {
		t.Fatalf("changeset has %d ops, want 2 (both processes' appends)", len(c.Ops))
	}
}

// TestChecksReflectAllLiveOps pins C-31/D-AX: recomputed Checks must
// reflect EVERY live op, not just the one just appended. Two create_page
// ops, each an orphan (no inbound link, and neither is added to
// index.md), must both count: appending the second must not silently
// drop the first's effect from the projection.
func TestChecksReflectAllLiveOps(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("two orphans", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/orphan-a.md",
		Content:    newConceptPageContent("Orphan A"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("append orphan-a: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	orphansAfterOne := c.Checks.Orphans

	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/orphan-b.md",
		Content:    newConceptPageContent("Orphan B"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("append orphan-b: %v", err)
	}

	c, err = e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if c.Checks.Orphans != orphansAfterOne+1 {
		t.Fatalf("Checks.Orphans after appending a second op = %d, want %d (the first op's orphan must still count)",
			c.Checks.Orphans, orphansAfterOne+1)
	}
}

// TestProjectionIncludesRootFiles pins C-29/D-AX: an op that desyncs
// index.md (a new page never added to it) must make Checks.Lint "fail" —
// the projection must include index.md at all, or index-sync's finding
// would never fire and Checks.Lint would stay "pass" on real drift.
func TestProjectionIncludesRootFiles(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("desync index.md", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/not-in-index.md",
		Content:    newConceptPageContent("Not In Index"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if c.Checks.Lint != "fail" {
		t.Fatalf("Checks.Lint = %q, want %q (index-sync should fire on the new, unlisted page)", c.Checks.Lint, "fail")
	}
}

// TestAppendStoresContentAndClearsIt pins D-AY: Append must Store.Put
// op.Content, write the resulting sha into After, and clear Content in
// the persisted changeset.
func TestAppendStoresContentAndClearsIt(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("store content", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	content := newConceptPageContent("Stored Page")
	id, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/stored-page.md",
		Content:    content,
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok := c.Op(id)
	if !ok {
		t.Fatalf("Current: op %s not found", id)
	}
	if op.Content != nil {
		t.Fatalf("op.Content = %v, want nil (cleared after storing)", op.Content)
	}
	if op.After == "" {
		t.Fatal("op.After is empty, want the sha256 Store.Put returned")
	}

	got, err := e.store.Get(op.After)
	if err != nil {
		t.Fatalf("Store.Get(op.After): %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("stored content does not match what was appended")
	}
}

// TestAppendIngestSourceSetsSHA256 pins the ingest_source half of D-AY:
// Append writes the stored sha into SHA256 too, not just After.
func TestAppendIngestSourceSetsSHA256(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("ingest", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	id, err := e.Append(Op{
		Kind:      OpIngestSource,
		Path:      "raw/papers/appended-source.md",
		Extractor: "go/html",
		Content:   []byte("Unique ingest content.\n"),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	c, _ := e.Current()
	op, ok := c.Op(id)
	if !ok {
		t.Fatal("op not found")
	}
	if op.SHA256 == "" {
		t.Fatal("op.SHA256 is empty, want it set for ingest_source")
	}
	if op.After != op.SHA256 {
		t.Fatalf("op.After = %q, op.SHA256 = %q, want them equal", op.After, op.SHA256)
	}
}

// TestAppendJournalsOpProposed pins §5.7's event→method table (D-BF):
// Append must journal exactly one op_proposed event per call, with a
// non-zero TS (D-AV/D-BF: the caller sets it, never the journal itself).
func TestAppendJournalsOpProposed(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("journal check", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	id, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/journaled-page.md",
		Content:    newConceptPageContent("Journaled Page"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvOpProposed}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d op_proposed events, want 1", len(events))
	}
	if events[0].Op != id {
		t.Fatalf("event.Op = %q, want %q", events[0].Op, id)
	}
	if events[0].TS.IsZero() {
		t.Fatal("event.TS is zero; Append must set it via e.now().UTC()")
	}
}

// TestOpenChangesetJournalsChangesetOpened pins D-BF's event table.
func TestOpenChangesetJournalsChangesetOpened(t *testing.T) {
	e, _ := newTestEngine(t)
	c, err := e.OpenChangeset("intent text", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvChangesetOpened}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d changeset_opened events, want 1", len(events))
	}
	if events[0].Changeset != c.ID {
		t.Fatalf("event.Changeset = %q, want %q", events[0].Changeset, c.ID)
	}
}

// TestDropHunkRecomputesAfterAndChecks pins §5.4's DropHunk Contract:
// dropping a hunk must recompute the op's projected content from Before
// plus the remaining live hunks, overwrite After, and recompute Checks.
func TestDropHunkRecomputesAfterAndChecks(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("drop hunk", testAuthor); err != nil {
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
	afterBeforeDrop := op.After

	if err := e.DropHunk(id, "h1"); err != nil {
		t.Fatalf("DropHunk: %v", err)
	}

	c, err = e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok = c.Op(id)
	if !ok {
		t.Fatal("op not found after DropHunk")
	}
	if !op.Hunks[0].Dropped {
		t.Fatal("hunk h1 is not marked Dropped")
	}
	if op.After == afterBeforeDrop {
		t.Fatal("op.After did not change after dropping its only hunk")
	}

	got, err := e.store.Get(op.After)
	if err != nil {
		t.Fatalf("Store.Get(op.After): %v", err)
	}
	if string(got) != string(page.Serialize()) {
		t.Fatalf("after dropping the only hunk, projected content should equal the original page again")
	}
}

// TestDropOpMarksDroppedAndExcludesFromChecks pins §5.4's DropOp Contract
// and Changeset.Live's definition.
func TestDropOpMarksDroppedAndExcludesFromChecks(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("drop op", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	id, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/to-be-dropped.md",
		Content:    newConceptPageContent("To Be Dropped"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	c, _ := e.Current()
	orphansWithOp := c.Checks.Orphans

	if err := e.DropOp(id); err != nil {
		t.Fatalf("DropOp: %v", err)
	}

	c, err = e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok := c.Op(id)
	if !ok {
		t.Fatal("op not found after DropOp")
	}
	if op.State != StateDropped {
		t.Fatalf("op.State = %q, want %q", op.State, StateDropped)
	}
	for _, live := range c.Live() {
		if live.ID == id {
			t.Fatal("Live() still returns the dropped op")
		}
	}
	if c.Checks.Orphans != orphansWithOp-1 {
		t.Fatalf("Checks.Orphans after dropping the op = %d, want %d", c.Checks.Orphans, orphansWithOp-1)
	}

	events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvOpDropped}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 || events[0].Op != id {
		t.Fatalf("op_dropped events = %+v, want exactly one for %s", events, id)
	}
}

// TestRefreshFlipsStaleWithoutTouchingHunks pins D-AJ/D-BC: Refresh must
// flip a patch_page op to StateStale when the target's current canonical
// sha no longer matches Before, and it must never recompute Hunks.
func TestRefreshFlipsStaleWithoutTouchingHunks(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("refresh", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, _ := e.Vault().Page("wiki/concepts/kv-cache.md")
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

	// Simulate the working tree changing under the op: edit the file on
	// disk directly (never through this package's own write path) and
	// reload the vault, exactly as Commit's own Refresh step would see a
	// concurrent Obsidian edit.
	onDisk := filepath.Join(e.root, filepath.FromSlash(page.Path))
	newBytes := append([]byte{}, page.Serialize()...)
	newBytes = append(newBytes, []byte("\nEdited directly on disk.\n")...)
	if err := os.WriteFile(onDisk, newBytes, 0o644); err != nil {
		t.Fatalf("write on-disk change: %v", err)
	}
	if err := e.Vault().Reload(); err != nil {
		t.Fatalf("Vault.Reload: %v", err)
	}

	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok := c.Op(id)
	if !ok {
		t.Fatal("op not found")
	}
	if op.State != StateStale {
		t.Fatalf("op.State = %q, want %q after the working tree changed under it", op.State, StateStale)
	}
	if len(op.Hunks) != 1 || op.Hunks[0].ID != "h1" {
		t.Fatalf("Refresh must not recompute Hunks: got %+v", op.Hunks)
	}
}

// TestRejectMovesToRejectedDir pins D-AI: Reject moves open/<id> to
// rejected/<id> via os.Rename, never a delete, and journals
// changeset_rejected.
func TestRejectMovesToRejectedDir(t *testing.T) {
	e, dir := newTestEngine(t)
	c, err := e.OpenChangeset("to be rejected", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	if err := e.Reject("no longer needed"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	openPath := filepath.Join(dir, ".llmwiki", "changesets", "open", c.ID)
	if _, err := os.Stat(openPath); !os.IsNotExist(err) {
		t.Fatalf("changesets/open/%s still exists after Reject", c.ID)
	}
	rejectedPath := filepath.Join(dir, ".llmwiki", "changesets", "rejected", c.ID, "changeset.json")
	if _, err := os.Stat(rejectedPath); err != nil {
		t.Fatalf("changesets/rejected/%s/changeset.json missing after Reject: %v", c.ID, err)
	}

	if _, err := e.Current(); !errors.Is(err, ErrNoChangeset) {
		t.Fatalf("Current after Reject: got %v, want ErrNoChangeset", err)
	}

	events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvChangesetRejected}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 || events[0].Changeset != c.ID {
		t.Fatalf("changeset_rejected events = %+v, want exactly one for %s", events, c.ID)
	}
}

// TestCurrentNoChangeset pins the ErrNoChangeset half of §5.4's Current
// Contract.
func TestCurrentNoChangeset(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.Current(); !errors.Is(err, ErrNoChangeset) {
		t.Fatalf("Current on a fresh engine: got %v, want ErrNoChangeset", err)
	}
}

// bodyContains and replaceLine are small test-only string helpers so
// TestDropHunkRecomputesAfterAndChecks reads clearly.
func bodyContains(body, line string) bool {
	return indexOfLine(splitLines(body), line) >= 0
}

func replaceLine(body, old, new string) string {
	lines := splitLines(body)
	if i := indexOfLine(lines, old); i >= 0 {
		lines[i] = new
	}
	return joinLinesWithNewline(lines)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}

func joinLinesWithNewline(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

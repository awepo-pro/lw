// op_undrop_test.go is T15's regression suite for issue C-131: DropHunk
// followed by UndropHunk must restore Op.After byte for byte, for every
// hunk shape, and OpDiff must reconstruct a dropped hunk through the same
// applyHunks DropHunk/UndropHunk use (MASTER §9 D-3M).
//
// Before this subtask's fix, an insert-only hunk (Add non-empty, Del
// empty) had no Del line to anchor on, so applyHunks fell back to "insert
// at the end of the body" — the wrong position for any hunk that named a
// Section. TestUndropRestoresProposal's add_only_hunk_with_section subtest
// and TestUndropRoundTripMockupVault are the tests that catch it; the
// other three TestUndropRestoresProposal subtests are regression coverage
// proving the fix left every other hunk shape's round trip exactly as it
// was (backbone §5.4 DropHunk/UndropHunk, "exact inverse", D-CL).
package stage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// nestedHeadingFixtureBefore is a standalone page (not part of the
// "minimal" fixture, so it is written directly into a private copy of that
// fixture — never into spec/fixtures/ itself) with a "###" subsection
// nested inside "## Parent Section". It exists to prove
// insertAtSectionEnd's level check: a hunk anchored on "## Parent Section"
// must land after the WHOLE section, including "### Child Subsection", not
// stop at the first "#"-prefixed line the way a level-blind scan would.
const nestedHeadingFixtureBefore = "---\n" +
	"title: Nested Heading Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference]\n" +
	"confidence: medium\n" +
	"---\n" +
	"\n" +
	"# Nested Heading Fixture\n" +
	"\n" +
	"Intro paragraph linking to [[kv-cache]] and [[gpt-4]].\n" +
	"\n" +
	"## Parent Section\n" +
	"\n" +
	"Parent content line.\n" +
	"\n" +
	"### Child Subsection\n" +
	"\n" +
	"Child content line.\n" +
	"\n" +
	"## Next Section\n" +
	"\n" +
	"Next content line.\n"

// nestedHeadingFixtureAfter is nestedHeadingFixtureBefore with a hunk
// proposing "New parent content added by the reviewer." at the end of
// "## Parent Section" already applied — i.e. after "### Child Subsection"'s
// own content, before "## Next Section". Hand-built independently of
// insertAtSectionEnd, so the test is not circular.
const nestedHeadingFixtureAfter = "---\n" +
	"title: Nested Heading Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference]\n" +
	"confidence: medium\n" +
	"---\n" +
	"\n" +
	"# Nested Heading Fixture\n" +
	"\n" +
	"Intro paragraph linking to [[kv-cache]] and [[gpt-4]].\n" +
	"\n" +
	"## Parent Section\n" +
	"\n" +
	"Parent content line.\n" +
	"\n" +
	"### Child Subsection\n" +
	"\n" +
	"Child content line.\n" +
	"\n" +
	"New parent content added by the reviewer.\n" +
	"\n" +
	"## Next Section\n" +
	"\n" +
	"Next content line.\n"

// currentOpAfterBytes re-reads the changeset, finds op id, and returns
// Store.Get(op.After) — the projected content DropHunk/UndropHunk
// (backbone §5.4) most recently computed for it.
func currentOpAfterBytes(t *testing.T, e *Engine, id string) []byte {
	t.Helper()
	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok := c.Op(id)
	if !ok {
		t.Fatalf("op %s not found in the open changeset", id)
	}
	b, err := e.store.Get(op.After)
	if err != nil {
		t.Fatalf("Store.Get(op.After) for %s: %v", id, err)
	}
	return b
}

// TestUndropRestoresProposal proves DropHunk followed by UndropHunk
// restores Op.After byte for byte, for every hunk shape (backbone §5.4,
// "exact inverse", D-CL) — the invariant issue C-131 found broken for a
// Section-anchored, insert-only hunk.
func TestUndropRestoresProposal(t *testing.T) {
	// add_only_hunk_with_section is the exact shape C-131 reported: a hunk
	// with Add lines and no Del anchor, carrying a Section. Before this
	// subtask's fix, DropHunk -> UndropHunk moved its content to the end
	// of the body instead of restoring it to the end of "## Parent
	// Section" (see the orchestrator's pre-fix probe,
	// runs/T01-stage-opdiff/orchestrator-probe.log, for the same defect
	// measured on the mockup vault's op3/op4).
	t.Run("add_only_hunk_with_section", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		fixtureRel := filepath.Join("wiki", "concepts", "nested-heading-fixture.md")
		if err := os.WriteFile(filepath.Join(dir, fixtureRel), []byte(nestedHeadingFixtureBefore), 0o644); err != nil {
			t.Fatalf("write nested-heading-fixture.md: %v", err)
		}

		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { e.Close() })
		e.now = testutil.FixedClock()

		if _, err := e.OpenChangeset("add only with section", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		path := filepath.ToSlash(fixtureRel)
		page, ok := e.Vault().Page(path)
		if !ok {
			t.Fatalf("fixture page %s not loaded", path)
		}
		if string(page.Serialize()) != nestedHeadingFixtureBefore {
			t.Fatalf("nestedHeadingFixtureBefore is not already canonical:\n--- Serialize() ---\n%s\n--- literal ---\n%s",
				page.Serialize(), nestedHeadingFixtureBefore)
		}

		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    path,
			Section: "## Parent Section",
			Before:  page.SHA256(),
			Content: []byte(nestedHeadingFixtureAfter),
			Hunks: []Hunk{
				{ID: "h1", Path: path, Section: "## Parent Section",
					Add: []string{"New parent content added by the reviewer.", ""}},
			},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		want := currentOpAfterBytes(t, e, id)
		if string(want) != nestedHeadingFixtureAfter {
			t.Fatalf("op.After right after Append != the proposed content:\n--- got ---\n%s\n--- want ---\n%s", want, nestedHeadingFixtureAfter)
		}

		if err := e.DropHunk(id, "h1"); err != nil {
			t.Fatalf("DropHunk: %v", err)
		}
		dropped := currentOpAfterBytes(t, e, id)
		if string(dropped) != nestedHeadingFixtureBefore {
			t.Fatalf("op.After after dropping the only hunk != the original page:\n--- got ---\n%s\n--- want ---\n%s", dropped, nestedHeadingFixtureBefore)
		}

		if err := e.UndropHunk(id, "h1"); err != nil {
			t.Fatalf("UndropHunk: %v", err)
		}
		got := currentOpAfterBytes(t, e, id)
		if string(got) != string(want) {
			t.Fatalf("drop+undrop did not restore the proposed After byte-for-byte:\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})

	// del_anchored_hunk is the shape applyHunks already handled correctly
	// before this subtask (it has its own Del anchor) — regression
	// coverage that the fix left it untouched.
	t.Run("del_anchored_hunk", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("del anchored", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		old := "step, at the cost of memory that grows with sequence length."
		newLine := "step, at the cost of memory proportional to sequence length."
		rewritten := *page
		rewritten.Body = replaceLine(page.Body, old, newLine)

		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Why it matters",
			Before:  page.SHA256(),
			Content: rewritten.Serialize(),
			Hunks: []Hunk{
				{ID: "h1", Path: page.Path, Del: []string{old}, Add: []string{newLine}},
			},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		want := currentOpAfterBytes(t, e, id)

		if err := e.DropHunk(id, "h1"); err != nil {
			t.Fatalf("DropHunk: %v", err)
		}
		if err := e.UndropHunk(id, "h1"); err != nil {
			t.Fatalf("UndropHunk: %v", err)
		}
		got := currentOpAfterBytes(t, e, id)
		if string(got) != string(want) {
			t.Fatalf("drop+undrop did not restore the proposed After byte-for-byte:\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})

	// one_of_two_hunks mixes a Del-anchored hunk (h1) with a
	// Section-anchored, insert-only hunk (h2) in the SAME op, drops only
	// h2, then undrops only h2 — proving the two anchor styles coexist
	// correctly and that h1 (never touched) is unaffected.
	t.Run("one_of_two_hunks", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("one of two", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}

		old1 := "step, at the cost of memory that grows with sequence length."
		new1 := "step, at the cost of memory proportional to sequence length."
		addedPara := "A note: eviction happens under memory pressure."

		body := replaceLine(page.Body, old1, new1)
		const marker = "\n\n## Example\n"
		if !strings.Contains(body, marker) {
			t.Fatal("fixture kv-cache.md no longer has the expected ## Example boundary")
		}
		body = strings.Replace(body, marker, "\n\n"+addedPara+"\n\n## Example\n", 1)

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
				{ID: "h2", Path: page.Path, Section: "## Why it matters", Add: []string{addedPara, ""}},
			},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		want := currentOpAfterBytes(t, e, id)

		if err := e.DropHunk(id, "h2"); err != nil {
			t.Fatalf("DropHunk h2: %v", err)
		}
		if dropped := currentOpAfterBytes(t, e, id); string(dropped) == string(want) {
			t.Fatal("DropHunk h2 did not change op.After")
		}

		if err := e.UndropHunk(id, "h2"); err != nil {
			t.Fatalf("UndropHunk h2: %v", err)
		}
		got := currentOpAfterBytes(t, e, id)
		if string(got) != string(want) {
			t.Fatalf("drop+undrop of h2 did not restore the proposed After byte-for-byte:\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})

	// accept_all_after_drops simulates Review's "A": drop two of three
	// hunks, then call UndropHunk on every hunk in the op, including h1,
	// which was never dropped — the no-op-on-live path D-CL's contract
	// promises. The final content must still match the original proposal.
	t.Run("accept_all_after_drops", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("accept all", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}

		old1 := "step, at the cost of memory that grows with sequence length."
		new1 := "step, at the cost of memory proportional to sequence length."
		addedPara := "A note: eviction happens under memory pressure."
		old3 := "- [[speculative-decoding]] — both the draft and target model read the cache"
		new3 := "- [[speculative-decoding]] — both the draft and target models consult the cache"

		body := replaceLine(page.Body, old1, new1)
		const marker = "\n\n## Example\n"
		if !strings.Contains(body, marker) {
			t.Fatal("fixture kv-cache.md no longer has the expected ## Example boundary")
		}
		body = strings.Replace(body, marker, "\n\n"+addedPara+"\n\n## Example\n", 1)
		if !bodyContains(body, old3) {
			t.Fatal("fixture kv-cache.md no longer has the expected ## Related bullet")
		}
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
				{ID: "h2", Path: page.Path, Section: "## Why it matters", Add: []string{addedPara, ""}},
				{ID: "h3", Path: page.Path, Del: []string{old3}, Add: []string{new3}},
			},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		want := currentOpAfterBytes(t, e, id)

		if err := e.DropHunk(id, "h2"); err != nil {
			t.Fatalf("DropHunk h2: %v", err)
		}
		if err := e.DropHunk(id, "h3"); err != nil {
			t.Fatalf("DropHunk h3: %v", err)
		}

		for _, hid := range []string{"h1", "h2", "h3"} {
			if err := e.UndropHunk(id, hid); err != nil {
				t.Fatalf("UndropHunk %s: %v", hid, err)
			}
		}

		got := currentOpAfterBytes(t, e, id)
		if string(got) != string(want) {
			t.Fatalf("after dropping h2/h3 then undropping every hunk, op.After != the pre-drop proposal:\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})
}

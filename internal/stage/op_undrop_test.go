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
	"reflect"
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

// TestUndropRoundTripMockupVault asserts the same drop -> undrop
// byte-identity as TestUndropRestoresProposal, against the private mockup
// vault's op3 and op4 — the exact reproduction in issue C-131. Skipped
// unless LW_MOCKUP_VAULT is set. Their OpDiff headers must still be
// @@ -34,6 +34,10 @@ and @@ -21,6 +21,10 @@ after the round trip: the
// same frozen headers TestOpDiffMockupVault asserts for the never-dropped
// case, now proven to survive a drop and an undrop too.
func TestUndropRoundTripMockupVault(t *testing.T) {
	src := os.Getenv("LW_MOCKUP_VAULT")
	if src == "" {
		t.Skip("LW_MOCKUP_VAULT not set: this conformance check runs locally against the private mockup vault")
	}

	dst := filepath.Join(t.TempDir(), "ml-notes")
	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copy mockup vault: %v", err)
	}

	e, err := OpenEngine(dst)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()

	cases := []struct {
		op         string
		wantHeader string
	}{
		{"op3", "@@ -34,6 +34,10 @@"},
		{"op4", "@@ -21,6 +21,10 @@"},
	}

	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			c, err := e.Current()
			if err != nil {
				t.Fatalf("Current: %v", err)
			}
			op, ok := c.Op(tc.op)
			if !ok {
				t.Fatalf("no such op %s", tc.op)
			}
			if len(op.Hunks) != 1 {
				t.Fatalf("%s has %d hunks, want exactly 1 for this fixture", tc.op, len(op.Hunks))
			}
			hunkID := op.Hunks[0].ID

			want := currentOpAfterBytes(t, e, tc.op)

			if err := e.DropHunk(tc.op, hunkID); err != nil {
				t.Fatalf("DropHunk(%s,%s): %v", tc.op, hunkID, err)
			}
			if err := e.UndropHunk(tc.op, hunkID); err != nil {
				t.Fatalf("UndropHunk(%s,%s): %v", tc.op, hunkID, err)
			}

			got := currentOpAfterBytes(t, e, tc.op)
			if string(got) != string(want) {
				t.Fatalf("%s: drop+undrop did not restore the proposed After byte-for-byte", tc.op)
			}

			fods, err := e.OpDiff(tc.op)
			if err != nil {
				t.Fatalf("OpDiff(%s): %v", tc.op, err)
			}
			if len(fods) == 0 || len(fods[0].Hunks) == 0 {
				t.Fatalf("OpDiff(%s) produced no windows: %+v", tc.op, fods)
			}
			if gotHeader := fods[0].Hunks[0].Header; gotHeader != tc.wantHeader {
				t.Fatalf("OpDiff(%s) header after round trip = %q, want %q", tc.op, gotHeader, tc.wantHeader)
			}
		})
	}
}

// fencedSetupFixtureBefore is a standalone page (written directly into a
// private copy of "minimal", never into spec/fixtures/ itself) whose target
// section, "## Setup", contains a fenced ```bash block with a shell
// comment "# install the tool" inside it — the exact shape repair-1's
// orchestrator probe found misread as a level-1 heading
// (runs/T15-stage-undrop-anchor/orchestrator-probe.log), which made
// insertAtSectionEnd splice new content INSIDE the fence instead of at the
// real end of "## Setup".
const fencedSetupFixtureBefore = "---\n" +
	"title: Fenced Setup Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference]\n" +
	"confidence: medium\n" +
	"---\n" +
	"\n" +
	"# Fenced Setup Fixture\n" +
	"\n" +
	"Intro paragraph linking to [[kv-cache]] and [[gpt-4]].\n" +
	"\n" +
	"## Setup\n" +
	"\n" +
	"Run this:\n" +
	"\n" +
	"```bash\n" +
	"# install the tool\n" +
	"make install\n" +
	"```\n" +
	"\n" +
	"More setup prose.\n" +
	"\n" +
	"## Next\n" +
	"\n" +
	"Other text.\n"

// fencedSetupFixtureAfter is fencedSetupFixtureBefore with a hunk proposing
// "### Added under Setup" at the end of "## Setup" already applied: after
// "More setup prose." (the section's real last content line), before
// "## Next" — and, critically, NOT inside the ```bash fence. Hand-built
// independently of insertAtSectionEnd, so the test is not circular.
const fencedSetupFixtureAfter = "---\n" +
	"title: Fenced Setup Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference]\n" +
	"confidence: medium\n" +
	"---\n" +
	"\n" +
	"# Fenced Setup Fixture\n" +
	"\n" +
	"Intro paragraph linking to [[kv-cache]] and [[gpt-4]].\n" +
	"\n" +
	"## Setup\n" +
	"\n" +
	"Run this:\n" +
	"\n" +
	"```bash\n" +
	"# install the tool\n" +
	"make install\n" +
	"```\n" +
	"\n" +
	"More setup prose.\n" +
	"\n" +
	"### Added under Setup\n" +
	"\n" +
	"New content.\n" +
	"\n" +
	"## Next\n" +
	"\n" +
	"Other text.\n"

// assertLines fails t with a readable diff when got != want.
func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines mismatch:\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestSectionScanCommonMark is repair-1's regression suite (MASTER §9
// D-3M, issue C-131): the heading/fence scanner insertAtSectionEnd uses
// must follow CommonMark's ATX-heading and fenced-code-block rules, not
// "any line starting with '#'" — the orchestrator's probe found a shell
// comment inside a ```bash fence misread as a heading, corrupting the page
// on undrop.
func TestSectionScanCommonMark(t *testing.T) {
	t.Run("hash_comment_inside_backtick_fence_is_not_a_heading", func(t *testing.T) {
		lines := []string{
			"# Page",
			"",
			"## Setup",
			"",
			"Run this:",
			"",
			"```bash",
			"# install the tool",
			"make install",
			"```",
			"",
			"More setup prose.",
			"",
			"## Next",
			"",
			"Other text.",
		}
		add := []string{"### Added under Setup", "", "New content.", ""}
		want := []string{
			"# Page",
			"",
			"## Setup",
			"",
			"Run this:",
			"",
			"```bash",
			"# install the tool",
			"make install",
			"```",
			"",
			"More setup prose.",
			"",
			"### Added under Setup",
			"",
			"New content.",
			"",
			"## Next",
			"",
			"Other text.",
		}
		got := insertAtSectionEnd(lines, "## Setup", add)
		assertLines(t, got, want)

		// The fence's own content (the shell comment and the install line)
		// must appear verbatim, unmoved, exactly once.
		for _, fenced := range []string{"# install the tool", "make install"} {
			if strings.Count(strings.Join(got, "\n"), fenced) != 1 {
				t.Fatalf("fence content %q does not appear exactly once in the result", fenced)
			}
		}
	})

	t.Run("tilde_fence", func(t *testing.T) {
		lines := []string{
			"## Setup",
			"",
			"~~~text",
			"# not a heading either",
			"some content",
			"~~~",
			"",
			"More setup prose.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		add := []string{"New note.", ""}
		want := []string{
			"## Setup",
			"",
			"~~~text",
			"# not a heading either",
			"some content",
			"~~~",
			"",
			"More setup prose.",
			"",
			"New note.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		assertLines(t, insertAtSectionEnd(lines, "## Setup", add), want)
	})

	t.Run("longer_closing_fence_and_unclosed_fence", func(t *testing.T) {
		// A fence opened with 4 backticks, closed by a longer (5-backtick)
		// run: the close rule is "count >= the opening count", so a
		// same-or-longer closer must still close it, and the fake heading
		// inside must still be ignored.
		closed := []string{
			"## Setup",
			"",
			"````",
			"# fake heading inside",
			"some content",
			"`````",
			"",
			"## Next",
			"",
			"Tail.",
		}
		add := []string{"New note.", ""}
		wantClosed := []string{
			"## Setup",
			"",
			"````",
			"# fake heading inside",
			"some content",
			"`````",
			"",
			"New note.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		assertLines(t, insertAtSectionEnd(closed, "## Setup", add), wantClosed)

		// An unclosed fence runs to the end of the body (CommonMark): no
		// heading inside it is ever a boundary candidate, and since
		// nothing follows it, the insertion falls back to the end of the
		// body — applyHunks' own fallback for an unanchored insertion.
		unclosed := []string{
			"## Setup",
			"",
			"Some content.",
			"",
			"```text",
			"# not a real heading, still open",
			"no closing marker here",
		}
		wantUnclosed := []string{
			"## Setup",
			"",
			"Some content.",
			"",
			"```text",
			"# not a real heading, still open",
			"no closing marker here",
			"New note.",
			"",
		}
		assertLines(t, insertAtSectionEnd(unclosed, "## Setup", add), wantUnclosed)
	})

	t.Run("hashtag_is_not_a_heading", func(t *testing.T) {
		lines := []string{
			"## Setup",
			"",
			"#tag",
			"",
			"Some content.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		add := []string{"New note.", ""}
		want := []string{
			"## Setup",
			"",
			"#tag",
			"",
			"Some content.",
			"",
			"New note.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		assertLines(t, insertAtSectionEnd(lines, "## Setup", add), want)
	})

	t.Run("section_heading_inside_fence_is_not_matched", func(t *testing.T) {
		lines := []string{
			"# Page",
			"",
			"```text",
			"## Setup",
			"```",
			"",
			"## Setup",
			"",
			"Real content.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		add := []string{"New note.", ""}
		want := []string{
			"# Page",
			"",
			"```text",
			"## Setup",
			"```",
			"",
			"## Setup",
			"",
			"Real content.",
			"",
			"New note.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		assertLines(t, insertAtSectionEnd(lines, "## Setup", add), want)
	})

	t.Run("atx_rules", func(t *testing.T) {
		cases := []struct {
			name string
			line string
			want int
		}{
			{"two hashes, space", "## a", 2},
			{"two hashes, tab", "##\t a", 2},
			{"two hashes at EOL", "##", 2},
			{"three leading spaces, three hashes", "   ### a", 3},
			{"hashtag, no space", "#tag", 0},
			{"seven hashes", "####### a", 0},
			{"four leading spaces", "    ## a", 0},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := atxHeadingLevel(tc.line); got != tc.want {
					t.Fatalf("atxHeadingLevel(%q) = %d, want %d", tc.line, got, tc.want)
				}
			})
		}
	})

	// drop_undrop_round_trip_with_fence proves the fence-aware scanner
	// through the full engine, not just the helper: a section containing a
	// fenced "#" comment must still round-trip DropHunk -> UndropHunk byte
	// for byte, matching the other TestUndropRestoresProposal subtests'
	// contract.
	t.Run("drop_undrop_round_trip_with_fence", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		fixtureRel := filepath.Join("wiki", "concepts", "fenced-setup-fixture.md")
		if err := os.WriteFile(filepath.Join(dir, fixtureRel), []byte(fencedSetupFixtureBefore), 0o644); err != nil {
			t.Fatalf("write fenced-setup-fixture.md: %v", err)
		}

		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { e.Close() })
		e.now = testutil.FixedClock()

		if _, err := e.OpenChangeset("fenced setup", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		path := filepath.ToSlash(fixtureRel)
		page, ok := e.Vault().Page(path)
		if !ok {
			t.Fatalf("fixture page %s not loaded", path)
		}
		if string(page.Serialize()) != fencedSetupFixtureBefore {
			t.Fatalf("fencedSetupFixtureBefore is not already canonical:\n--- Serialize() ---\n%s\n--- literal ---\n%s",
				page.Serialize(), fencedSetupFixtureBefore)
		}

		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    path,
			Section: "## Setup",
			Before:  page.SHA256(),
			Content: []byte(fencedSetupFixtureAfter),
			Hunks: []Hunk{
				{ID: "h1", Path: path, Section: "## Setup",
					Add: []string{"### Added under Setup", "", "New content.", ""}},
			},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		want := currentOpAfterBytes(t, e, id)
		if string(want) != fencedSetupFixtureAfter {
			t.Fatalf("op.After right after Append != the proposed content:\n--- got ---\n%s\n--- want ---\n%s", want, fencedSetupFixtureAfter)
		}

		if err := e.DropHunk(id, "h1"); err != nil {
			t.Fatalf("DropHunk: %v", err)
		}
		dropped := currentOpAfterBytes(t, e, id)
		if string(dropped) != fencedSetupFixtureBefore {
			t.Fatalf("op.After after dropping the only hunk != the original page:\n--- got ---\n%s\n--- want ---\n%s", dropped, fencedSetupFixtureBefore)
		}

		if err := e.UndropHunk(id, "h1"); err != nil {
			t.Fatalf("UndropHunk: %v", err)
		}
		got := currentOpAfterBytes(t, e, id)
		if string(got) != string(want) {
			t.Fatalf("drop+undrop did not restore the proposed After byte-for-byte:\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})

	// html_comment_hash_line_is_not_a_heading extends the scanner over
	// CommonMark HTML blocks type 2 (I1, joint T01+T15 review): a "#" line
	// inside a <!-- --> comment is commented-out draft text, not a heading
	// — neither an anchor nor a section boundary — exactly like the fenced
	// case above. One-line and unclosed comments are pinned too.
	t.Run("html_comment_hash_line_is_not_a_heading", func(t *testing.T) {
		lines := []string{
			"## Setup",
			"",
			"<!--",
			"# not a heading, just commented-out draft",
			"-->",
			"",
			"More prose.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		add := []string{"New note.", ""}
		want := []string{
			"## Setup",
			"",
			"<!--",
			"# not a heading, just commented-out draft",
			"-->",
			"",
			"More prose.",
			"",
			"New note.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		assertLines(t, insertAtSectionEnd(lines, "## Setup", add), want)

		// A comment that opens and closes on one line: the delimiter line
		// itself is level 0, and nothing after it is swallowed.
		oneLine := []string{
			"## Setup",
			"",
			"<!-- # inline, closed same line -->",
			"",
			"More prose.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		wantOne := []string{
			"## Setup",
			"",
			"<!-- # inline, closed same line -->",
			"",
			"More prose.",
			"",
			"New note.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		assertLines(t, insertAtSectionEnd(oneLine, "## Setup", add), wantOne)

		// An unclosed comment runs to the end of the body (CommonMark's
		// rule for an unclosed block): no heading inside it or after it is
		// a boundary candidate, so the insertion falls back to the end of
		// the body.
		unclosed := []string{
			"## Setup",
			"",
			"<!-- draft notes follow",
			"# never closed",
		}
		wantUnclosed := []string{
			"## Setup",
			"",
			"<!-- draft notes follow",
			"# never closed",
			"New note.",
			"",
		}
		assertLines(t, insertAtSectionEnd(unclosed, "## Setup", add), wantUnclosed)
	})

	// duplicate_section_heading_anchors_first_occurrence pins the
	// first-occurrence rule (I3, joint T01+T15 review; issue C-132): with
	// "## Setup" appearing twice, insertAtSectionEnd anchors the FIRST
	// one. DropHunk and UndropHunk pick the same occurrence, so display
	// and commit still agree — this test exists so a change to that rule
	// is never silent.
	t.Run("duplicate_section_heading_anchors_first_occurrence", func(t *testing.T) {
		lines := []string{
			"# Page",
			"",
			"## Setup",
			"",
			"First occurrence content.",
			"",
			"## Middle",
			"",
			"Middle content.",
			"",
			"## Setup",
			"",
			"Second occurrence content.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		add := []string{"New note.", ""}
		want := []string{
			"# Page",
			"",
			"## Setup",
			"",
			"First occurrence content.",
			"",
			"New note.",
			"",
			"## Middle",
			"",
			"Middle content.",
			"",
			"## Setup",
			"",
			"Second occurrence content.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		assertLines(t, insertAtSectionEnd(lines, "## Setup", add), want)
	})

	// fence_line_containing_comment_marker pins "whichever opens first
	// wins" for a fence whose info string or content carries "<!--": the
	// fence opened first, so nothing inside it — HTML comment marker
	// included — can open a comment block or be a heading.
	t.Run("fence_line_containing_comment_marker", func(t *testing.T) {
		lines := []string{
			"## Setup",
			"",
			"```html <!-- not a comment, an info string -->",
			"<!-- # inside the fence, not a comment block -->",
			"## not a heading, fence content",
			"```",
			"",
			"More prose.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		add := []string{"New note.", ""}
		want := []string{
			"## Setup",
			"",
			"```html <!-- not a comment, an info string -->",
			"<!-- # inside the fence, not a comment block -->",
			"## not a heading, fence content",
			"```",
			"",
			"More prose.",
			"",
			"New note.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		assertLines(t, insertAtSectionEnd(lines, "## Setup", add), want)
	})

	// comment_containing_fence_line pins the same rule the other way
	// round: the comment opened first, so a fence line inside it never
	// opens a code block and its "# ..." content is never a heading — the
	// comment runs to the line containing "-->" regardless.
	t.Run("comment_containing_fence_line", func(t *testing.T) {
		lines := []string{
			"## Setup",
			"",
			"<!-- draft snippet, not a real block:",
			"```bash",
			"# install the tool — commented out with the block",
			"-->",
			"",
			"More prose.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		add := []string{"New note.", ""}
		want := []string{
			"## Setup",
			"",
			"<!-- draft snippet, not a real block:",
			"```bash",
			"# install the tool — commented out with the block",
			"-->",
			"",
			"More prose.",
			"",
			"New note.",
			"",
			"## Next",
			"",
			"Tail.",
		}
		assertLines(t, insertAtSectionEnd(lines, "## Setup", add), want)
	})
}

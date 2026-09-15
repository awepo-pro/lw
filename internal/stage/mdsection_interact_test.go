package stage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// sectionScanInteractionSubtests is TestSectionScanCommonMark's engine-level
// half, moved here whole (a pure move, file-size split): the subtests that
// drive DropHunk -> UndropHunk and insertAtSectionEnd through fences,
// comments and duplicate headings.
func sectionScanInteractionSubtests(t *testing.T) {
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

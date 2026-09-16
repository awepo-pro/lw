package stage

import (
	"strings"
	"testing"
)

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

	sectionScanInteractionSubtests(t)
}

package vault

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestSection covers ParseSections, including the fenced-code-block
// exclusion that a line-based regexp cannot get right (00-conventions.md
// §2.4's whole reason for existing).
func TestSection(t *testing.T) {
	t.Run("heading_in_code_fence", func(t *testing.T) {
		body := "```text\n" +
			"## this is not a heading, just log text\n" +
			"[[fake]]\n" +
			"```\n"

		got := ParseSections(body)
		if len(got) != 0 {
			t.Fatalf("ParseSections(fenced-only body) = %#v, want zero sections", got)
		}
	})

	t.Run("level2_start_offset", func(t *testing.T) {
		body := "# Title\n\ntext\n\n## Related\n\nmore text\n"
		secs := ParseSections(body)
		if len(secs) != 2 {
			t.Fatalf("len(sections) = %d, want 2 (%#v)", len(secs), secs)
		}
		sec := secs[1]
		if sec.Level != 2 {
			t.Fatalf("secs[1].Level = %d, want 2", sec.Level)
		}
		// The single easiest thing to get subtly wrong here (per the brief):
		// Start must be the byte offset of the heading LINE, not of the
		// heading's text content past the "## ".
		if got := body[sec.Start : sec.Start+2]; got != "##" {
			t.Fatalf("body[Start:Start+2] = %q, want %q", got, "##")
		}
	})

	t.Run("fields_and_slug", func(t *testing.T) {
		body := "# Title\n\nintro\n\n## Why It Matters!!\n\ndetail\n"
		secs := ParseSections(body)
		if len(secs) != 2 {
			t.Fatalf("len(sections) = %d, want 2", len(secs))
		}
		sec := secs[1]
		if sec.Heading != "## Why It Matters!!" {
			t.Errorf("Heading = %q, want %q", sec.Heading, "## Why It Matters!!")
		}
		if sec.Title != "Why It Matters!!" {
			t.Errorf("Title = %q, want %q", sec.Title, "Why It Matters!!")
		}
		if sec.Slug != "why-it-matters" {
			t.Errorf("Slug = %q, want %q", sec.Slug, "why-it-matters")
		}
	})

	t.Run("heading_line_trimmed_of_trailing_space", func(t *testing.T) {
		body := "## Trailing   \nbody\n"
		secs := ParseSections(body)
		if len(secs) != 1 {
			t.Fatalf("len(sections) = %d, want 1", len(secs))
		}
		if secs[0].Heading != "## Trailing" {
			t.Errorf("Heading = %q, want %q", secs[0].Heading, "## Trailing")
		}
		// Body must still point just past the *real* line's newline, not
		// the trimmed heading's length.
		if got, want := body[secs[0].Body:], "body\n"; got != want {
			t.Errorf("body[Body:] = %q, want %q", got, want)
		}
	})

	t.Run("deeper_heading_does_not_end_parent", func(t *testing.T) {
		body := "## Parent\n\npara\n\n### Child\n\nchild body\n\n## Sibling\n\nsib body\n"
		secs := ParseSections(body)
		if len(secs) != 3 {
			t.Fatalf("len(sections) = %d, want 3 (%#v)", len(secs), secs)
		}
		parent, child, sibling := secs[0], secs[1], secs[2]
		if parent.Level != 2 || child.Level != 3 || sibling.Level != 2 {
			t.Fatalf("levels = %d,%d,%d, want 2,3,2", parent.Level, child.Level, sibling.Level)
		}
		// Parent's End must be the start of Sibling (same level), not
		// Child (deeper level) — a deeper heading never ends its parent.
		if parent.End != sibling.Start {
			t.Errorf("parent.End = %d, want sibling.Start = %d", parent.End, sibling.Start)
		}
		if child.End != sibling.Start {
			t.Errorf("child.End = %d, want sibling.Start = %d", child.End, sibling.Start)
		}
	})

	t.Run("last_section_ends_at_len_body", func(t *testing.T) {
		body := "## Only\n\ncontent\n"
		secs := ParseSections(body)
		if len(secs) != 1 {
			t.Fatalf("len(sections) = %d, want 1", len(secs))
		}
		if secs[0].End != len(body) {
			t.Errorf("End = %d, want len(body) = %d", secs[0].End, len(body))
		}
	})

	t.Run("kv_cache_fixture", func(t *testing.T) {
		root := testutil.FixtureRoot(t)
		path := filepath.Join(root, "minimal", "wiki", "concepts", "kv-cache.md")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		_, body, err := ParseFrontmatter(b)
		if err != nil {
			t.Fatalf("ParseFrontmatter: %v", err)
		}

		secs := ParseSections(string(body))

		var titles []string
		for _, s := range secs {
			titles = append(titles, s.Title)
		}
		// 014 amendment (workflow §9 A6): kv-cache.md gained an ## Abstract
		// section between the intro and ## Why it matters.
		want := []string{"KV Cache", "Abstract", "Why it matters", "Example", "Related"}
		if len(titles) != len(want) {
			t.Fatalf("titles = %v, want %v", titles, want)
		}
		for i := range want {
			if titles[i] != want[i] {
				t.Errorf("titles[%d] = %q, want %q", i, titles[i], want[i])
			}
		}

		// The fenced "##" log line inside "Example" must not have produced
		// its own section, and it must not have split "Example" early:
		// "Example"'s body must contain the fenced block's fake heading
		// text as plain content.
		for _, s := range secs {
			if s.Title != "Example" {
				continue
			}
			content := string(body[s.Body:s.End])
			if !containsFakeHeadingLine(content) {
				t.Errorf("Example section body does not contain the fenced fake-heading line:\n%s", content)
			}
		}
	})

	t.Run("nested_list_body_survives", func(t *testing.T) {
		checkSingleSectionSurvives(t, "nested-list-body.want.md")
	})

	t.Run("table_body_survives", func(t *testing.T) {
		checkSingleSectionSurvives(t, "table.want.md")
	})

	t.Run("fenced_delimiter_survives", func(t *testing.T) {
		// spec/fixtures/pages/fenced-delimiter.want.md has a fenced block
		// containing a bare "---" line: proof this is AST-based, not a
		// naive scan for lines that look like delimiters or headings.
		checkSingleSectionSurvives(t, "fenced-delimiter.want.md")
	})

	t.Run("replace_section_preserves_siblings", func(t *testing.T) {
		body := "# Title\n\nintro\n\n## First\n\nfirst body\n\n## Second\n\nsecond body\n\n## Third\n\nthird body\n"
		secs := ParseSections(body)
		if len(secs) != 4 {
			t.Fatalf("len(sections) = %d, want 4", len(secs))
		}
		second := secs[2]
		if second.Title != "Second" {
			t.Fatalf("secs[2].Title = %q, want %q", second.Title, "Second")
		}

		beforeSecond := body[:second.Start]
		afterSecond := body[second.End:]

		got := ReplaceSection(body, second, "replaced body\n")

		if got[:len(beforeSecond)] != beforeSecond {
			t.Errorf("bytes before the replaced section changed:\n before=%q\n  got=%q", beforeSecond, got[:len(beforeSecond)])
		}
		if got[len(got)-len(afterSecond):] != afterSecond {
			t.Errorf("bytes after the replaced section changed:\n before=%q\n  got=%q", afterSecond, got[len(got)-len(afterSecond):])
		}

		// Re-parse and confirm First/Third are unaffected in content.
		newSecs := ParseSections(got)
		if len(newSecs) != 4 {
			t.Fatalf("len(newSecs) = %d, want 4 (%#v)", len(newSecs), newSecs)
		}
		if got[newSecs[1].Body:newSecs[1].End] != "\nfirst body\n\n" {
			t.Errorf("First's body changed: %q", got[newSecs[1].Body:newSecs[1].End])
		}
		if got[newSecs[2].Body:newSecs[2].End] != "replaced body\n" {
			t.Errorf("Second's body = %q, want %q", got[newSecs[2].Body:newSecs[2].End], "replaced body\n")
		}
		if got[newSecs[3].Body:newSecs[3].End] != "\nthird body\n" {
			t.Errorf("Third's body changed: %q", got[newSecs[3].Body:newSecs[3].End])
		}
	})

	t.Run("replace_last_section_normalizes_trailing_newline", func(t *testing.T) {
		body := "## Only\n\ncontent\n"
		secs := ParseSections(body)
		got := ReplaceSection(body, secs[0], "new content\n\n\n\n")
		want := "## Only\nnew content\n"
		if got != want {
			t.Errorf("ReplaceSection = %q, want %q", got, want)
		}
	})

	t.Run("append_to_section", func(t *testing.T) {
		body := "## Only\n\nfirst\n"
		secs := ParseSections(body)
		got := AppendToSection(body, secs[0], "\nsecond")
		want := "## Only\n\nfirst\n\nsecond\n"
		if got != want {
			t.Errorf("AppendToSection = %q, want %q", got, want)
		}
	})

	t.Run("insert_after_section", func(t *testing.T) {
		body := "## First\n\nfirst body\n\n## Second\n\nsecond body\n"
		secs := ParseSections(body)
		got := InsertAfterSection(body, secs[0], "## Inserted\n\ninserted body")
		want := "## First\n\nfirst body\n\n## Inserted\n\ninserted body\n\n## Second\n\nsecond body\n"
		if got != want {
			t.Errorf("InsertAfterSection = %q, want %q", got, want)
		}
	})

	t.Run("insert_after_last_section", func(t *testing.T) {
		body := "## Only\n\nonly body\n"
		secs := ParseSections(body)
		got := InsertAfterSection(body, secs[0], "## New\n\nnew body\n\n\n")
		want := "## Only\n\nonly body\n\n## New\n\nnew body\n"
		if got != want {
			t.Errorf("InsertAfterSection = %q, want %q", got, want)
		}
	})

	t.Run("insert_before_first_section_after_preamble", func(t *testing.T) {
		body := "Preamble prose before any section.\n\n## First\n\nfirst body\n\n## Second\n\nsecond body\n"
		secs := ParseSections(body)
		if len(secs) != 2 {
			t.Fatalf("len(sections) = %d, want 2", len(secs))
		}
		// Extra trailing newlines in block prove it is seam-normalized,
		// same as insert_after_last_section.
		got := InsertBeforeSection(body, secs[0], "## New Head\n\nnew body\n\n\n")
		want := "Preamble prose before any section.\n\n## New Head\n\nnew body\n\n## First\n\nfirst body\n\n## Second\n\nsecond body\n"
		if got != want {
			t.Errorf("InsertBeforeSection = %q, want %q", got, want)
		}
	})

	t.Run("insert_before_mid_section", func(t *testing.T) {
		body := "## First\n\nfirst body\n\n## Second\n\nsecond body\n\n## Third\n\nthird body\n"
		secs := ParseSections(body)
		got := InsertBeforeSection(body, secs[1], "## Inserted\n\ninserted body")
		want := "## First\n\nfirst body\n\n## Inserted\n\ninserted body\n\n## Second\n\nsecond body\n\n## Third\n\nthird body\n"
		if got != want {
			t.Errorf("InsertBeforeSection = %q, want %q", got, want)
		}
	})

	t.Run("insert_before_last_section", func(t *testing.T) {
		body := "## First\n\nfirst body\n\n## Last\n\nlast body\n"
		secs := ParseSections(body)
		got := InsertBeforeSection(body, secs[1], "## Inserted\n\ninserted body")
		want := "## First\n\nfirst body\n\n## Inserted\n\ninserted body\n\n## Last\n\nlast body\n"
		if got != want {
			t.Errorf("InsertBeforeSection = %q, want %q", got, want)
		}
	})

	t.Run("insert_before_only_section_opens_body", func(t *testing.T) {
		body := "## Only\n\nonly body\n"
		secs := ParseSections(body)
		got := InsertBeforeSection(body, secs[0], "## New\n\nnew body")
		want := "## New\n\nnew body\n\n## Only\n\nonly body\n"
		if got != want {
			t.Errorf("InsertBeforeSection = %q, want %q", got, want)
		}
	})

	t.Run("remove_first_section_keeps_preamble", func(t *testing.T) {
		body := "Preamble prose.\n\n## First\n\nfirst body\n\n## Second\n\nsecond body\n"
		secs := ParseSections(body)
		got := RemoveSection(body, secs[0])
		want := "Preamble prose.\n\n## Second\n\nsecond body\n"
		if got != want {
			t.Errorf("RemoveSection = %q, want %q", got, want)
		}
	})

	t.Run("remove_first_section_no_leading_blank_line", func(t *testing.T) {
		body := "## First\n\nfirst body\n\n## Second\n\nsecond body\n"
		secs := ParseSections(body)
		got := RemoveSection(body, secs[0])
		want := "## Second\n\nsecond body\n"
		if got != want {
			t.Errorf("RemoveSection = %q, want %q", got, want)
		}
	})

	t.Run("remove_mid_section_single_blank_seam", func(t *testing.T) {
		body := "## First\n\nfirst body\n\n## Second\n\nsecond body\n\n## Third\n\nthird body\n"
		secs := ParseSections(body)
		got := RemoveSection(body, secs[1])
		want := "## First\n\nfirst body\n\n## Third\n\nthird body\n"
		if got != want {
			t.Errorf("RemoveSection = %q, want %q", got, want)
		}
	})

	t.Run("remove_last_section_normalizes_trailing_newline", func(t *testing.T) {
		body := "## First\n\nfirst body\n\n## Last\n\nlast body\n"
		secs := ParseSections(body)
		got := RemoveSection(body, secs[1])
		want := "## First\n\nfirst body\n"
		if got != want {
			t.Errorf("RemoveSection = %q, want %q", got, want)
		}
	})

	t.Run("remove_only_section_yields_empty", func(t *testing.T) {
		body := "## Only\n\nonly body\n"
		secs := ParseSections(body)
		got := RemoveSection(body, secs[0])
		if got != "" {
			t.Errorf("RemoveSection = %q, want empty string", got)
		}
	})
}

// checkSingleSectionSurvives parses spec/fixtures/pages/<name>'s body and
// asserts ParseSections finds exactly the page's single top-level heading,
// spanning the whole body — proof that a table's "|" and a nested list's
// leading "-"/indentation are not misread as heading or section boundaries.
func checkSingleSectionSurvives(t *testing.T, name string) {
	t.Helper()

	root := testutil.FixtureRoot(t)
	path := filepath.Join(root, "pages", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	_, body, err := ParseFrontmatter(b)
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}

	secs := ParseSections(string(body))
	if len(secs) != 1 {
		t.Fatalf("len(sections) = %d, want 1 (%#v)", len(secs), secs)
	}
	if secs[0].Level != 1 {
		t.Errorf("Level = %d, want 1", secs[0].Level)
	}
	if secs[0].End != len(body) {
		t.Errorf("End = %d, want len(body) = %d", secs[0].End, len(body))
	}
}

// containsFakeHeadingLine reports whether s contains the exact fenced-block
// line that must never be treated as a heading.
func containsFakeHeadingLine(s string) bool {
	const want = "## this is not a heading, just log text"
	for i := 0; i+len(want) <= len(s); i++ {
		if s[i:i+len(want)] == want {
			return true
		}
	}
	return false
}

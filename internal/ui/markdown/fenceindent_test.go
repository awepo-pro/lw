package markdown

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// goFence is the fenced sample the fence tests render: two short lines, a
// blank line, and one closing brace.
const goFence = "```go\nfunc main() {\n\tfmt.Println(\"hi\") // note\n\n}\n```\n"

// fgSGRs returns the sorted, de-duplicated set of truecolor foreground SGR
// sequences in s.
func fgSGRs(s string) []string {
	re := regexp.MustCompile("\x1b\\[38;2;[0-9;]+m")
	set := map[string]bool{}
	for _, seq := range re.FindAllString(s, -1) {
		set[seq] = true
	}
	out := make([]string, 0, len(set))
	for seq := range set {
		out = append(out, seq)
	}
	sort.Strings(out)
	return out
}

// chromaFGSet is the exact foreground-SGR set a `go` fence produced before
// the fence indent landed (captured from the unmodified renderer at width
// 80 with the dark palette and pinned here as a literal): the indent adds
// plain spaces only, so this set must not move.
var chromaFGSet = []string{
	"\x1b[38;2;107;194;142m", // strings, Good
	"\x1b[38;2;122;178;242m", // function names, Accent
	"\x1b[38;2;195;160;240m", // keywords, Heading
	"\x1b[38;2;216;221;228m", // plain text, Fg
	"\x1b[38;2;94;102;114m",  // comments, Faint+italic
}

func TestFenceIndent(t *testing.T) {
	t.Run("code_lines_indented_two", func(t *testing.T) {
		lines := renderFrag(t, goFence, 80, darkStyle, false)
		indented := 0
		for _, l := range lines {
			stripped := ansi.Strip(l)
			if strings.TrimSpace(stripped) == "" {
				continue
			}
			indented++
			if !strings.HasPrefix(stripped, "  ") || stripped[2] == ' ' {
				t.Errorf("code line not indented exactly two: %q", stripped)
			}
		}
		if indented == 0 {
			t.Fatalf("no code lines found in %q", lines)
		}
	})

	t.Run("long_code_line_clipped_at_width", func(t *testing.T) {
		src := "```go\n" + strings.Repeat("x", 100) + "\n```\n"
		lines := renderFrag(t, src, 40, darkStyle, false)
		for i, l := range lines {
			if strings.TrimSpace(ansi.Strip(l)) == "" {
				continue
			}
			if n := ansi.StringWidth(l); n > 40 {
				t.Errorf("line %d is %d cells wide at width 40: %q", i, n, ansi.Strip(l))
			}
		}
		long := ""
		for _, l := range lines {
			if strings.HasPrefix(ansi.Strip(l), "  xxx") {
				long = ansi.Strip(l)
				break
			}
		}
		if long == "" {
			t.Fatalf("the long code line vanished: %q", lines)
		}
		if !strings.HasPrefix(long, "  ") {
			t.Errorf("the clipped line lost its indent: %q", long)
		}
		if !strings.HasSuffix(long, "…") {
			t.Errorf("the clipped line does not end in the ellipsis: %q", long)
		}
		// The indent counts toward the width: 38 content cells + the 2-cell
		// indent fill the 40.
		if ansi.StringWidth(long) != 40 {
			t.Errorf("clipped line is %d cells, want 40 (indent included): %q", ansi.StringWidth(long), long)
		}
	})

	t.Run("chroma_colours_unchanged", func(t *testing.T) {
		lines := renderFrag(t, goFence, 80, darkStyle, false)
		got := fgSGRs(strings.Join(lines, "\n"))
		if strings.Join(got, "|") != strings.Join(chromaFGSet, "|") {
			t.Errorf("foreground SGR set changed:\n got %q\nwant %q", got, chromaFGSet)
		}
	})

	t.Run("blank_code_line_stays_blank", func(t *testing.T) {
		lines := renderFrag(t, goFence, 80, darkStyle, false)
		blank := 0
		for i, l := range lines {
			if ansi.Strip(l) == "" && l != "" {
				t.Errorf("line %d is visually blank but carries bytes: %q", i, l)
			}
			if strings.TrimSpace(ansi.Strip(l)) == "" && strings.Contains(strings.Join(lines, "\n"), "Println") {
				if i > 0 && strings.Contains(ansi.Strip(lines[i-1]), "note") {
					blank++
					if l != "" {
						t.Errorf("the blank line inside the fence is %q, want \"\"", l)
					}
				}
			}
		}
		if blank == 0 {
			t.Fatalf("no blank code line found after the // note line: %q", lines)
		}
	})

	t.Run("unknown_language_indented_too", func(t *testing.T) {
		lines := renderFrag(t, "```weirdlang\nplain text line one\nplain text line two\n```\n", 80, darkStyle, false)
		n := 0
		for _, l := range lines {
			stripped := ansi.Strip(l)
			if strings.TrimSpace(stripped) == "" {
				continue
			}
			n++
			if !strings.HasPrefix(stripped, "  ") || stripped[2] == ' ' {
				t.Errorf("unknown-language code line not indented exactly two: %q", stripped)
			}
		}
		if n < 2 {
			t.Fatalf("expected both code lines, got %q", lines)
		}
	})
}

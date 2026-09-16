package markdown

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"gopkg.in/yaml.v3"
)

// frontDelim is the line that opens and closes a page's YAML frontmatter.
const frontDelim = "---\n"

// frontmatter is the subset of a page's YAML frontmatter the title/meta
// line needs (contract §2 note 1; mockgen.page_lines).
type frontmatter struct {
	Title      string   `yaml:"title"`
	Type       string   `yaml:"type"`
	Tags       []string `yaml:"tags"`
	Confidence string   `yaml:"confidence"`
	Updated    string   `yaml:"updated"`
}

// splitFrontmatter separates src into its frontmatter (parsed, best-effort),
// its body, and whether a "---\n...\n---\n" block was found at all. found is
// false only when no such delimited block exists — a delimited block whose
// YAML fails to parse still counts as found (fm stays zero, body is still
// correctly split off); a page is always rendered, never rejected, for a
// frontmatter problem (parse only, per contract §2 note 1). The caller
// (render.go) gates the title/meta header on found alone, so a
// delimiter-free source's body starts at line 0 (repair-1, Critical 2).
func splitFrontmatter(src []byte) (fm frontmatter, body string, found bool) {
	s := string(src)
	if !strings.HasPrefix(s, frontDelim) {
		return frontmatter{}, s, false
	}
	rest := s[len(frontDelim):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return frontmatter{}, s, false
	}
	yamlPart := rest[:end]
	body = rest[end+len("\n---\n"):]

	_ = yaml.Unmarshal([]byte(yamlPart), &fm) // best-effort: leave fm zero on error
	return fm, body, true
}

// pageHeader renders the title line, the meta line(s) and one blank
// separator (mockgen.page_lines), each at contentW cells wide, not yet
// gutter-prefixed or padded to the full line width — the caller
// (render.go) applies the gutter and pads to totalW uniformly for every
// line the renderer produces.
func pageHeader(fm frontmatter, contentW int) []styledLine {
	out := []styledLine{
		{text: ansi.Truncate(fm.Title, contentW, "…"), bold: true, color: colorFg},
	}

	var bits []string
	if fm.Type != "" {
		bits = append(bits, fm.Type)
	}
	if len(fm.Tags) > 0 {
		bits = append(bits, strings.Join(fm.Tags, ", "))
	}
	if fm.Confidence != "" {
		bits = append(bits, "confidence "+fm.Confidence)
	}
	if fm.Updated != "" {
		bits = append(bits, "updated "+fm.Updated)
	}
	meta := strings.Join(bits, " · ")
	for _, l := range wrapPlain(meta, contentW) {
		out = append(out, styledLine{text: l, color: colorFaint})
	}
	out = append(out, styledLine{text: ""})
	return out
}

// wrapPlain greedy-wraps plain text into lines of at most w cells (a local
// port of mockgen.wrap without the hang-indent this package never needs;
// contract §2 note 1 requires a local port since this package cannot
// import internal/ui.Wrap). A word longer than w is hard-split at w cells.
func wrapPlain(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}

	var lines []string
	cur := ""
	for _, word := range words {
		for ansi.StringWidth(word) > w {
			if cur != "" {
				lines = append(lines, cur)
				cur = ""
			}
			head, tail := splitRunesToWidth(word, w)
			lines = append(lines, head)
			word = tail
		}
		cand := word
		if cur != "" {
			cand = cur + " " + word
		}
		if ansi.StringWidth(cand) <= w {
			cur = cand
			continue
		}
		if cur != "" {
			lines = append(lines, cur)
		}
		cur = word
	}
	if cur != "" || len(lines) == 0 {
		lines = append(lines, cur)
	}
	return lines
}

// splitRunesToWidth splits s into a head of at most w cells and the
// remaining tail, on rune boundaries (plain text only, no ANSI to
// preserve).
func splitRunesToWidth(s string, w int) (head, tail string) {
	rs := []rune(s)
	width := 0
	for i, r := range rs {
		rw := ansi.StringWidth(string(r))
		if width+rw > w {
			return string(rs[:i]), string(rs[i:])
		}
		width += rw
	}
	return s, ""
}

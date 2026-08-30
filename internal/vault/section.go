package vault

import (
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Section is one ATX heading and everything under it, up to the next heading
// of the same or shallower level. Offsets index into Page.Body.
type Section struct {
	Level   int    // 1..6
	Heading string // "## Related" — the full raw heading line, trimmed of trailing space
	Title   string // "Related"    — heading text with the #s and space stripped
	Slug    string // "related"    — lowercase, non-alphanumerics collapsed to "-"
	Start   int    // byte offset of the heading line in Body
	Body    int    // byte offset just past the heading's newline
	End     int    // byte offset one past the last byte of the section
}

// slugNonAlnum matches runs of characters that are not lowercase ASCII
// letters or digits, for collapsing into a single "-" when building a Slug.
var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// ParseSections walks body's Markdown AST for ATX headings and returns one
// Section per heading, in document order.
//
// Contract (backbone §2.4): headings inside fenced code blocks are not
// sections. This is true because it uses the goldmark AST rather than a line
// regexp — a fenced code block's contents are never re-parsed as block
// structure, so a line reading "## not a heading" inside one never becomes
// an *ast.Heading node in the first place.
func ParseSections(body string) []Section {
	src := []byte(body)
	doc := goldmark.New().Parser().Parse(text.NewReader(src))

	heads := collectHeadings(doc, src)

	sections := make([]Section, 0, len(heads))
	for i, h := range heads {
		sections = append(sections, buildSection(src, heads, i, h))
	}
	return sections
}

// heading is an ATX heading's level and the byte offset where its source
// line begins — the minimal information ParseSections needs from the AST
// before it can compute every Section field from raw offsets alone.
type heading struct {
	level int
	start int
}

// collectHeadings walks doc for *ast.Heading nodes and returns one heading
// per node, in document order, with Start already rewound to the beginning
// of the heading's source line.
//
// goldmark's Lines() segments for an ATX heading cover only the text after
// the "#"s (see the spike behind this file), so Start here is not that
// segment's own Start — it is the byte offset of the line containing it,
// found by walking backward to the previous newline (or the start of body).
func collectHeadings(doc ast.Node, src []byte) []heading {
	var heads []heading
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok {
			return ast.WalkContinue, nil
		}
		lines := h.Lines()
		if lines.Len() == 0 {
			// A heading with no text content at all (e.g. a bare "##")
			// has nothing to anchor an offset to; skip it rather than
			// guess.
			return ast.WalkContinue, nil
		}
		textStart := lines.At(0).Start
		heads = append(heads, heading{level: h.Level, start: lineStart(src, textStart)})
		return ast.WalkContinue, nil
	})
	return heads
}

// buildSection computes the full Section for heads[i], given src and the
// complete, ordered heading list it belongs to.
func buildSection(src []byte, heads []heading, i int, h heading) Section {
	lineEnd := lineEnd(src, h.start)
	headingLine := strings.TrimRight(string(src[h.start:lineEnd]), " \t")

	bodyOff := lineEnd
	if bodyOff < len(src) && src[bodyOff] == '\n' {
		bodyOff++
	}

	end := len(src)
	for _, next := range heads[i+1:] {
		if next.level <= h.level {
			end = next.start
			break
		}
	}

	title := headingTitle(headingLine)
	return Section{
		Level:   h.level,
		Heading: headingLine,
		Title:   title,
		Slug:    slugify(title),
		Start:   h.start,
		Body:    bodyOff,
		End:     end,
	}
}

// lineStart returns the byte offset of the start of the source line
// containing pos: the position just after the previous "\n", or 0 if pos is
// on the first line.
func lineStart(src []byte, pos int) int {
	i := pos
	for i > 0 && src[i-1] != '\n' {
		i--
	}
	return i
}

// lineEnd returns the byte offset of the "\n" that ends the source line
// starting at pos, or len(src) if that line runs to the end of src with no
// trailing newline.
func lineEnd(src []byte, pos int) int {
	for i := pos; i < len(src); i++ {
		if src[i] == '\n' {
			return i
		}
	}
	return len(src)
}

// headingTitle strips an ATX heading line's leading "#"s and the single
// space that must follow them, per CommonMark, leaving just the heading
// text.
func headingTitle(line string) string {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i < len(line) && line[i] == ' ' {
		i++
	}
	return line[i:]
}

// slugify lowercases title and collapses every run of non-alphanumeric
// characters into a single "-", trimmed of leading/trailing "-".
func slugify(title string) string {
	lower := strings.ToLower(title)
	slug := slugNonAlnum.ReplaceAllString(lower, "-")
	return strings.Trim(slug, "-")
}

// normalizeTrailingNewline trims every trailing "\n" from s and appends
// exactly one back, unless s is empty or consists entirely of newlines — in
// which case it returns "", the same "no content, no manufactured newline"
// convention Page.Body uses for its own zero value (backbone §2.3, MASTER §9
// D-U).
func normalizeTrailingNewline(s string) string {
	trimmed := strings.TrimRight(s, "\n")
	if trimmed == "" {
		return ""
	}
	return trimmed + "\n"
}

// ReplaceSection replaces sec's content — [sec.Body, sec.End) — with
// newBody, keeping sec's heading line untouched.
//
// Contract (backbone §2.4): section-level, never line-level
// (00-conventions.md §5.6). The result is normalized to end in exactly one
// trailing "\n". In practice that normalization only ever changes anything
// when sec is the last section (sec.End == len(body)): otherwise the
// untouched tail after sec.End already carries body's own original,
// correctly-formed ending, and trimming-then-re-adding a single trailing
// newline on the full result is a no-op.
func ReplaceSection(body string, sec Section, newBody string) string {
	full := body[:sec.Body] + newBody + body[sec.End:]
	return normalizeTrailingNewline(full)
}

// AppendToSection appends add to the end of sec's existing content —
// immediately before sec.End — leaving the heading line and everything
// outside the section untouched. It is a byte-offset operation: add is used
// exactly as given, so a caller that wants a blank line before it (the usual
// case, matching how sections in this vault's fixtures separate paragraphs)
// includes that separator itself.
func AppendToSection(body string, sec Section, add string) string {
	return ReplaceSection(body, sec, body[sec.Body:sec.End]+add)
}

// InsertAfterSection inserts block as a new section immediately after sec's
// content ends and before the section that follows it, if any, separated
// from each neighbor by a single blank line — matching how every section
// boundary in this vault's fixtures is written. If sec is the last section,
// the result still ends in exactly one "\n" (backbone §2.3's body
// contract), never a trailing blank line.
func InsertAfterSection(body string, sec Section, block string) string {
	head := strings.TrimRight(body[:sec.End], "\n")
	inserted := strings.TrimRight(block, "\n")
	tail := body[sec.End:]

	if tail == "" {
		return head + "\n\n" + inserted + "\n"
	}
	return head + "\n\n" + inserted + "\n\n" + tail
}

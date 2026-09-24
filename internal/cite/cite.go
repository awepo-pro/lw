// Package cite owns the grammar of lw's provenance markers: ^[<source>]
// and ^[<source> p.N] / ^[<source> p.N-M]. Seven places (ask display, lint,
// stage, tools, ui) grew ad-hoc regexes and string splits for this syntax;
// this package replaces that knowledge with one leaf — standard library
// only, so every consumer may import it — before they switch over in 034's
// second wave. (034 T1.)
package cite

import (
	"regexp"
	"strconv"
	"strings"
)

// Cite is one provenance marker, parsed. The zero page convention (From ==
// To == 0) keeps legacy markers — written before 034 — representable by the
// same struct as paged ones, so consumers switch without a version bump.
// (034 T1.)
type Cite struct {
	// Source is the vault-relative path as written, e.g. "raw/papers/x.md".
	Source string
	// From is the 1-based physical page the claim sits on; 0 = no page.
	From int
	// To is == From for a single page, > From for a range; 0 = no page.
	To int
	// Raw is the marker exactly as written, "^[…]" — zero unless set by Scan.
	Raw string
	// Offset is the byte offset of the "^" in the text Scan scanned; zero
	// unless set by Scan.
	Offset int
	// Err is "" when the marker is well-formed, else a one-line reason the
	// lint can surface verbatim. A marker with an Err still carries Source,
	// so the reviewer sees which file a broken citation points at.
	Err string
}

// pageSpecRe matches the page part of a marker: exactly one space, lowercase
// "p.", then N = [1-9][0-9]{0,4} — no leading zero, at most five digits, no
// spaces around the dash — optionally "-M". Anything else after the source
// is prose that leaked into the marker and must be reported, not guessed at.
var pageSpecRe = regexp.MustCompile(`^p\.([1-9][0-9]{0,4})(?:-([1-9][0-9]{0,4}))?$`)

// anchorRe matches a PDF page anchor exactly as internal/extract writes it:
// the whole line, nothing before or after. A looser match would take prose
// like "<!-- page 2 --> for details" for an anchor and misattribute claims.
var anchorRe = regexp.MustCompile(`^<!-- page ([1-9][0-9]*) -->$`)

// Parse parses the text between "^[" and "]" into a Cite. Raw and Offset are
// left zero — only Scan knows where a marker sat. An inner with no space is
// never an error whatever the path: legacy markers and wiki/ links stay
// valid, because 034 only ever ADDS the page suffix, and breaking the old
// form would strand every marker written before it. A Cite with an Err
// never carries pages (From == To == 0), so a consumer checking Err first
// cannot mistake a rejected marker for a paged one. A paged source must
// start with "raw/" and end with ".md" (A-034-4); unpaged markers are
// never given an Err. (034 T1.)
func Parse(inner string) Cite {
	sp := strings.IndexByte(inner, ' ')
	if sp < 0 {
		return Cite{Source: inner}
	}
	c := Cite{Source: inner[:sp]}
	rest := inner[sp+1:]

	m := pageSpecRe.FindStringSubmatch(rest)
	if m == nil {
		c.Err = malformedErr(inner)
		return c
	}
	from, _ := strconv.Atoi(m[1])
	// A paged source must be a raw markdown file — the only shape 034's PDF
	// ingest produces and the only thing a page number can mean. Anything
	// else after a space is prose that leaked into the marker ("raw/x.md,
	// p.12" pasted from a sentence, "raw/x.pdf p.3"): not guessed at, but
	// reported, per amendment A-034-4.
	if !strings.HasPrefix(c.Source, "raw/") {
		c.Err = "only raw/ sources take a page"
		return c
	}
	if !strings.HasSuffix(c.Source, ".md") {
		c.Err = malformedErr(inner)
		return c
	}
	if m[2] != "" {
		to, _ := strconv.Atoi(m[2])
		if to <= from {
			c.Err = "page range p." + m[1] + "-" + m[2] + " must ascend"
			return c
		}
		c.From, c.To = from, to
		return c
	}
	c.From, c.To = from, from
	return c
}

// malformedErr builds the exact one-line reason for an unparseable page
// part. The inner goes in verbatim — the reviewer must see the bytes they
// wrote — while the fix shown is the literal shape, since no concrete
// correction can be inferred from arbitrary prose. (034 T1.)
func malformedErr(inner string) string {
	return `malformed page citation "^[` + inner + `]": write ^[<source> p.N] or ^[<source> p.N-M]`
}

// Scan returns every marker in text, in order, with Raw and Offset filled
// in. Fenced code blocks and inline code spans are excluded — a marker in
// code is an example, not a claim's provenance, and stripping it would
// corrupt both the example and the citation record; the semantics are
// copied from ui/ask's display stripping so every consumer agrees on what
// "code" means. Parse carries the grammar; Scan carries only where markers
// sit. (034 T1.)
func Scan(text string) []Cite {
	var out []Cite
	off := 0 // byte offset of the current line's start
	inFence := false
	var fenceCh byte
	var fenceLen int
	for _, line := range strings.Split(text, "\n") {
		if inFence {
			// fence content is code, the closing line with it — a marker
			// typed on a ``` line is not a citation either
			if closesFence(line, fenceCh, fenceLen) {
				inFence = false
			}
		} else if ch, n, ok := opensFence(line); ok {
			inFence = true
			fenceCh, fenceLen = ch, n
		} else {
			out = scanLine(out, line, off)
		}
		off += len(line) + 1
	}
	return out
}

// scanLine finds the markers on one line known to be outside any fence,
// appending them to out. Backtick runs toggle an inline code span exactly
// as ui/ask's stripLineMarkers does (034 T1). A "^[" whose "]" never
// arrives on the same line is skipped byte by byte, so a marker split
// across lines is never half-read; the search resumes after the "^", which
// lets a later well-formed marker on the same line still be found.
func scanLine(out []Cite, line string, lineOff int) []Cite {
	inCode := false
	for i := 0; i < len(line); {
		if line[i] == '`' {
			j := i
			for j < len(line) && line[j] == '`' {
				j++
			}
			inCode = !inCode
			i = j
			continue
		}
		if !inCode && line[i] == '^' && i+1 < len(line) && line[i+1] == '[' {
			if rel := strings.IndexByte(line[i+2:], ']'); rel >= 0 {
				end := i + 2 + rel
				c := Parse(line[i+2 : end])
				c.Raw = line[i : end+1]
				c.Offset = lineOff + i
				out = append(out, c)
				i = end + 1
				continue
			}
		}
		i++
	}
	return out
}

// String renders the canonical form: "^[src]", "^[src p.N]" or
// "^[src p.N-M]" — the shapes Parse accepts, so Parse(String()) round-trips
// and writers can canonicalize what they read. A Cite with an Err has no
// canonical form (the bytes were never valid), so Raw comes back verbatim.
// (034 T1.)
func (c Cite) String() string {
	if c.Err != "" {
		return c.Raw
	}
	if c.From == 0 {
		return "^[" + c.Source + "]"
	}
	if c.From == c.To {
		return "^[" + c.Source + " p." + strconv.Itoa(c.From) + "]"
	}
	return "^[" + c.Source + " p." + strconv.Itoa(c.From) + "-" + strconv.Itoa(c.To) + "]"
}

// Pages returns the anchor page numbers in body order. Anchors are matched
// on the whole line only, and inside code fences too: anchors are written
// by lw's own PDF ingest (internal/extract), not by users, so a fence can
// only contain one by the ingest's own doing — honoring it keeps Pages and
// PageAt consistent instead of special-casing a shape that never occurs.
// (034 T1.)
func Pages(body string) []int {
	var pages []int
	for _, line := range strings.Split(body, "\n") {
		if m := anchorRe.FindStringSubmatch(trimCR(line)); m != nil {
			n, _ := strconv.Atoi(m[1])
			pages = append(pages, n)
		}
	}
	return pages
}

// PageAt returns the page the byte offset falls on: the last anchor line
// whose start is ≤ offset, 0 when the body has no anchors. The last anchor
// at-or-before the offset is the right answer even when offset sits past
// the anchor's blank line — every byte after an anchor belongs to that
// anchor's page until the next one says otherwise. (034 T1.)
func PageAt(body string, offset int) int {
	page := 0
	start := 0
	for _, line := range strings.Split(body, "\n") {
		if m := anchorRe.FindStringSubmatch(trimCR(line)); m != nil && start <= offset {
			page, _ = strconv.Atoi(m[1])
		}
		start += len(line) + 1
	}
	return page
}

// trimCR strips one trailing carriage return so an anchor line in a CRLF
// body still matches the anchor grammar — lw writes LF, but an original
// preserved verbatim (033's ingest originals) may not, and a page number
// lost to a line ending is a silent misattribution. (034 T1.)
func trimCR(line string) string {
	return strings.TrimSuffix(line, "\r")
}

// opensFence reports whether line opens a fenced code block: after at most
// three leading spaces, a run of at least three backticks or tildes — the
// rest of the line is the info string. Semantics copied verbatim from
// ui/ask's display stripping (034 T1): every consumer must draw the
// code/prose line at exactly the same place, or Scan and the displayer
// disagree about which markers exist.
func opensFence(line string) (ch byte, n int, ok bool) {
	i := 0
	for i < 3 && i < len(line) && line[i] == ' ' {
		i++
	}
	if len(line)-i < 3 {
		return 0, 0, false
	}
	ch = line[i]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	for i+n < len(line) && line[i+n] == ch {
		n++
	}
	return ch, n, n >= 3
}

// closesFence reports whether line closes a fence opened with n of ch:
// that run alone, after at most three leading spaces. A shorter run, or
// any trailing text, is fence content, not the closer — the same rule
// ui/ask applies, so the two passes never split one block differently.
// (034 T1.)
func closesFence(line string, ch byte, n int) bool {
	i := 0
	for i < 3 && i < len(line) && line[i] == ' ' {
		i++
	}
	cnt := 0
	for i < len(line) && line[i] == ch {
		i++
		cnt++
	}
	return cnt >= n && i == len(line)
}

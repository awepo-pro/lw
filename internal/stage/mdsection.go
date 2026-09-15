// mdsection.go holds the markdown section scanner every heading-anchor
// caller in this package shares — atxHeadingLevel, fenceOpen, fenceClose,
// scanHeadingLevels — plus insertAtSectionEnd, the anchor a
// pure-insertion hunk (issue C-131, MASTER §9 D-3M) splices at. Both the
// section MATCH and the section BOUNDARY go through scanHeadingLevels, so
// the two can never disagree about what counts as a heading (repair-1).
//
// The CommonMark subset, deliberately small (vault pages are
// engine-serialized, so this is a closed world):
//
//   - ATX headings only (§4.2): 0-3 leading spaces, 1-6 '#'s, then a
//     space, a tab, or end of line. Setext headings (===/~~~ underlines)
//     are deliberately excluded — never a boundary, never an anchor:
//     pages are ATX-only by convention and the engine's own serializer
//     emits ATX, so an underline is just paragraph text here.
//   - Fenced code blocks (§4.5): ``` or ~~~, closing fence at least as
//     long as the opening one; nothing inside, delimiters included, is a
//     heading.
//   - HTML blocks type 2 (§4.7): a line that, after 0-3 spaces, starts
//     with "<!--", runs until a line containing "-->" (possibly the same
//     line); nothing inside — delimiters included — is a heading. An
//     unclosed comment runs to the end of the body.
//
// Matching is EXACT LINE EQUALITY: CRLF is not normalized (inherited from
// 001's applyHunks/indexOfLine, which split on "\n" and compare bytes), so
// a "\r"-suffixed line never matches a section heading. Section matching
// takes the first occurrence — see insertAtSectionEnd and issue C-132.
package stage

import (
	"strings"
)

// atxHeadingLevel returns line's ATX heading level (1-6), or 0 when line is
// not a CommonMark ATX heading. Rules (CommonMark §4.2, the subset that
// matters for a level test — no closing-sequence stripping, since callers
// only need the level, never the heading text): 0-3 leading spaces, then a
// run of 1-6 '#' characters, then either a space, a tab, or the end of the
// line. 4+ leading spaces, 0 or 7+ '#'s, or a '#' run immediately followed
// by anything else ("#hashtag") are all NOT a heading — measured against
// C-131's repair-1 finding (MASTER §9), where a shell comment "# install
// the tool" inside a fenced code block was misread as one.
func atxHeadingLevel(line string) int {
	i, spaces := 0, 0
	for i < len(line) && line[i] == ' ' {
		i++
		spaces++
	}
	if spaces > 3 {
		return 0
	}
	start := i
	for i < len(line) && line[i] == '#' {
		i++
	}
	n := i - start
	if n == 0 || n > 6 {
		return 0
	}
	if i == len(line) {
		return n
	}
	if c := line[i]; c == ' ' || c == '\t' {
		return n
	}
	return 0
}

// fenceOpen reports whether line opens a CommonMark fenced code block: 0-3
// leading spaces, then a run of 3 or more identical '`' or '~' characters.
// A backtick fence's info string (everything after the opening run) must
// not itself contain a backtick (CommonMark §4.5); a tilde fence's info
// string has no such restriction. ch and n are the fence character and how
// many of it opened the fence — fenceClose needs both to recognize a valid
// closing line.
func fenceOpen(line string) (ch byte, n int, ok bool) {
	i, spaces := 0, 0
	for i < len(line) && line[i] == ' ' {
		i++
		spaces++
	}
	if spaces > 3 || i >= len(line) {
		return 0, 0, false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return 0, 0, false
	}
	start := i
	for i < len(line) && line[i] == c {
		i++
	}
	count := i - start
	if count < 3 {
		return 0, 0, false
	}
	if c == '`' && strings.IndexByte(line[i:], '`') >= 0 {
		return 0, 0, false
	}
	return c, count, true
}

// fenceClose reports whether line closes a fence that was opened with
// openCount consecutive ch characters: 0-3 leading spaces, then a run of ch
// at least openCount long, then nothing but trailing spaces (CommonMark
// §4.5 — a shorter run, a different character, or any other trailing
// content does not close it).
func fenceClose(line string, ch byte, openCount int) bool {
	i, spaces := 0, 0
	for i < len(line) && line[i] == ' ' {
		i++
		spaces++
	}
	if spaces > 3 || i >= len(line) || line[i] != ch {
		return false
	}
	start := i
	for i < len(line) && line[i] == ch {
		i++
	}
	if i-start < openCount {
		return false
	}
	for ; i < len(line); i++ {
		if line[i] != ' ' {
			return false
		}
	}
	return true
}

// htmlCommentOpen reports whether line OPENS a CommonMark HTML block type
// 2 (§4.7): after 0-3 leading spaces, the line starts with "<!--". The
// block then runs until a line containing "-->" — possibly this same one,
// which scanHeadingLevels checks itself.
func htmlCommentOpen(line string) bool {
	i, spaces := 0, 0
	for i < len(line) && line[i] == ' ' {
		i++
		spaces++
	}
	if spaces > 3 {
		return false
	}
	return strings.HasPrefix(line[i:], "<!--")
}

// scanHeadingLevels returns, for every line in lines, its ATX heading level
// (atxHeadingLevel), forced to 0 for a fence's own delimiter lines and for
// every line between them, and likewise for an HTML comment (type 2): the
// "<!--" line, every line until the one containing "-->" (inclusive), and
// the delimiters themselves are never a heading, however they look.
// CommonMark fenced code content is never a heading, and neither is
// commented-out draft text — a "# heading" pasted into either is the same
// class of defect that put the fence rule here (issue C-131, MASTER §9
// D-3M). This is the one scan every heading-anchor caller in this package
// (insertAtSectionEndAt's own section match and its boundary scan) shares,
// so the two can never disagree about what counts as "inside a fence or
// comment". Whichever of a fence or a comment opens first wins: neither's
// content can open the other. A fence or comment that never closes runs to
// the end of the body — CommonMark's own rule for an unclosed construct —
// so nothing after it is a heading candidate either.
func scanHeadingLevels(lines []string) []int {
	levels := make([]int, len(lines))
	inComment := false
	for i := 0; i < len(lines); {
		line := lines[i]
		if inComment {
			levels[i] = 0
			if strings.Contains(line, "-->") {
				inComment = false
			}
			i++
			continue
		}
		if ch, n, ok := fenceOpen(line); ok {
			levels[i] = 0 // the opening delimiter line itself
			i++
			for i < len(lines) && !fenceClose(lines[i], ch, n) {
				levels[i] = 0
				i++
			}
			if i < len(lines) {
				levels[i] = 0 // the closing delimiter line itself
				i++
			}
			continue
		}
		if htmlCommentOpen(line) {
			levels[i] = 0 // the "<!--" line itself, even when it also closes
			i++
			if !strings.Contains(line, "-->") {
				inComment = true
			}
			continue
		}
		levels[i] = atxHeadingLevel(line)
		i++
	}
	return levels
}

// insertAtSectionEnd returns lines with add spliced in immediately before
// the next heading whose level is the same as or higher than (i.e. fewer
// or equal leading '#'s than) the FIRST heading line exactly equal to
// section — the anchor a pure-insertion hunk (backbone §5.4
// DropHunk/UndropHunk, MASTER §9 D-3M / issue C-131) uses instead of
// applyHunksTraced's generic Del-anchored loop. Both the section match and
// the boundary scan go through scanHeadingLevels, so a line that merely
// LOOKS like section or a heading — inside a fenced code block or an HTML
// comment, or missing the space/tab ATX needs after its '#' run — is never
// mistaken for one (repair-1, MASTER §9 D-3M: a shell comment "# install
// the tool" inside a ```bash fence must not end the section early and must
// not be matched as section itself). A deeper heading in between (e.g.
// section is "##" and an intervening "###" subheading follows before the
// next "##") is part of section's own content and does not terminate it —
// the scan skips past it, so add lands after the WHOLE section,
// subsections included.
//
// The section match takes the FIRST heading line exactly equal to section.
// On a page with a duplicated heading — two "### Related" sections under
// different parents — a hunk meant for the second occurrence lands at the
// end of the first. DropHunk and UndropHunk both pick the same occurrence,
// so display and commit still agree exactly (D-CL): the anchor is
// deterministic, just not occurrence-aware, and Hunk.Section carries no
// heading path or occurrence index to disambiguate with. That is issue
// C-132; a real fix needs a backbone field change (Hunk.SectionPath) and
// is deliberately out of 003's view-layer scope. The pinning test is
// TestSectionScanCommonMark/duplicate_section_heading_anchors_first_occurrence.
//
// When section does not appear in lines as an actual (non-fenced,
// non-commented) heading (should not happen for a hunk this package's own
// proposers build, which always name a section that already exists — but
// defensive for a hand-built hunk in a test), add is appended at the end
// of the body instead, mirroring the generic loop's own fallback for an
// unanchored insertion.
//
// add is expected to carry its own blank-line padding (the shape every
// patch_page proposal already builds it in, backbone §5.3 Hunk.Add): every
// blank line already present in lines, including the one separating
// section's last content line from the next heading, is left exactly
// where it was — add is inserted after it, not instead of it.
func insertAtSectionEnd(lines []string, section string, add []string) []string {
	out, _ := insertAtSectionEndAt(lines, section, add)
	return out
}

// insertAtSectionEndAt is insertAtSectionEnd with the insertion index: at
// is where add begins in the returned slice, so applyHunksTraced can tag
// exactly those lines with the hunk that produced them.
func insertAtSectionEndAt(lines []string, section string, add []string) (out []string, at int) {
	levels := scanHeadingLevels(lines)

	headingIdx := -1
	for i, l := range lines {
		if levels[i] > 0 && l == section {
			headingIdx = i
			break
		}
	}

	at = len(lines)
	if headingIdx >= 0 {
		level := levels[headingIdx]
		for i := headingIdx + 1; i < len(lines); i++ {
			if levels[i] > 0 && levels[i] <= level {
				at = i
				break
			}
		}
	}

	out = make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:at]...)
	out = append(out, add...)
	out = append(out, lines[at:]...)
	return out, at
}

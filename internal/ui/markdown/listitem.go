package markdown

// Parsing one rendered list or quote line into its leading parts: indent
// cells, quote bars, and the item marker — a bullet, an ordered number or
// a task checkbox — with its raw byte run. The reflow accumulator in
// listwrap.go consumes these parts.

import (
	"strings"
)

// listItem is one rendered list line's parts: the leading indent cells,
// the marker's raw byte run from its first byte through its separating
// space (styling included — the quote pass re-attaches it verbatim), the
// separating space on its own, the visible marker text and the text after
// the separator. A task item's marker is its checkbox "[ ]" / "[x]": the
// 3 ASCII cells are the item's marker width in every locale, which is
// what the hang is computed from.
type listItem struct {
	ind    int
	raw    string
	sep    string
	marker string
	rest   string
}

// splitListItem splits a rendered list line into its parts; ok is false
// when the line does not open with a marker, and rest is then the whole
// line.
func splitListItem(l string) (item listItem, ok bool) {
	item = listItem{}
	var i int
	item.ind, i = scanIndent(l)
	rest := l[i:]
	switch {
	case strings.HasPrefix(rest, "[ ]"), strings.HasPrefix(rest, "[x]"):
		item.marker, rest = rest[:3], rest[3:]
	case strings.HasPrefix(rest, "•"):
		item.marker, rest = "•", rest[len("•"):]
	default:
		var num strings.Builder
		for {
			d := 0
			for d < len(rest) && rest[d] >= '0' && rest[d] <= '9' {
				d++
			}
			if d > 0 {
				num.WriteString(rest[:d])
				rest = rest[d:]
			}
			n := decodeSGROnly(rest)
			if n == 0 {
				break
			}
			rest = rest[n:]
		}
		if num.Len() > 0 && strings.HasPrefix(rest, ".") {
			item.marker, rest = num.String()+".", rest[1:]
		}
	}
	if item.marker == "" {
		return listItem{ind: item.ind, rest: l}, false
	}
	if strings.HasPrefix(rest, " ") {
		item.sep, rest = " ", rest[1:]
	}
	item.raw = l[i : len(l)-len(rest)]
	item.rest = rest
	return item, true
}

// blockLinePrefix splits one rendered block line into its leading quote
// bars, how many levels deep they are, and the padding-trimmed text after
// them. A quote block's every line leads with its bars; a list block shows
// bars only on a blockquote nested in one of its items — detected here so
// the reflow can lay that quote out as a paragraph of its own instead of
// absorbing it into the item's text (R-508). The bars are split off the
// RAW line, before any indent or padding trimming: on a bars-only margin
// line the token's separating space is the line's last visible cell, and
// trimming first would eat it — leaving a bar colorizeQuoteBar's token
// lookahead no longer recognises (R-507).
func blockLinePrefix(line string, quote bool) (bars string, depth int, text string) {
	if quote {
		bars, depth = splitQuotePrefix(line)
		return bars, depth, trimLinePadding(line[len(bars):])
	}
	_, off := scanIndent(line)
	if off < len(line) {
		if b, d := splitQuotePrefix(line[off:]); d > 0 {
			return b, d, trimLinePadding(line[off+len(b):])
		}
	}
	return "", 0, trimLinePadding(line)
}

// decodeSGROnly returns the byte length of the SGR sequence at the front of
// s, 0 when s does not start with one.
func decodeSGROnly(s string) int {
	_, n, _, ok := decodeSGR(s)
	if !ok {
		return 0
	}
	return n
}

// skipIndentCells returns s with its leading `cells` visible space cells
// removed. Styling runs between the spaces are skipped byte-wise without
// counting, and with zero cells the string is returned untouched — a line
// that opens with runs but no indent keeps every one of them.
func skipIndentCells(s string, cells int) string {
	for i := 0; i < len(s); {
		if cells == 0 {
			return s[i:]
		}
		if s[i] == ' ' {
			cells--
			i++
			continue
		}
		if n := decodeSGROnly(s[i:]); n > 0 {
			i += n
			continue
		}
		break
	}
	return s
}

// scanIndent walks a rendered line's leading indent: space cells counted,
// styling runs skipped without advancing the column. Returns the cell count
// and the byte offset where the line's first visible (non-space) character
// starts.
func scanIndent(l string) (cells, offset int) {
	for offset < len(l) {
		if l[offset] == ' ' {
			cells++
			offset++
			continue
		}
		if n := decodeSGROnly(l[offset:]); n > 0 {
			offset += n
			continue
		}
		break
	}
	return cells, offset
}

// splitQuotePrefix returns the byte prefix of a rendered quote line's
// leading indent bars and how many levels deep they are: one "│ " token —
// a bar, one space, then an escape sequence or the line's end, the same
// shape colorizeQuoteBar's isIndentBar accepts — per level, with any
// styling runs between tokens. Quote text opening with a literal "│" has
// prose after the space, and is never taken for a bar.
func splitQuotePrefix(l string) (prefix string, depth int) {
	i := 0
	for {
		j := i
		for {
			if n := decodeSGROnly(l[j:]); n > 0 {
				j += n
				continue
			}
			break
		}
		if !strings.HasPrefix(l[j:], "│") {
			return l[:i], depth
		}
		tail := l[j+len("│"):]
		if tail != "" && (tail[0] != ' ' || len(tail) > 1 && tail[1] != '\x1b') {
			return l[:i], depth
		}
		depth++
		if tail == "" {
			return l, depth
		}
		i = j + len("│") + 1
	}
}

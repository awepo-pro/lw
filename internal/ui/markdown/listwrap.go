package markdown

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// unwrapSlack is how many cells unwrapWidth adds over the widest source
// line: enough headroom that glamour's own rendering of the line (marker,
// nested indent, real-link " <url>" suffixes) can never reach the wrap
// width, so glamour emits every line unbroken and this package decides all
// wrapping itself.
const unwrapSlack = 64

// unwrapWidth is the wrap width a list, blockquote or fence block renders
// at: above both the widest source line plus slack and the content width,
// so neither the block's own glamour wrap nor the document-level final wrap
// breaks anything. The layout passes below then do every wrap at the real
// budget — with the paragraph wrap function (lipgloss.Wrap, breakpoints
// empty) glamour itself uses, whose line widths match ansi.StringWidth
// exactly, unlike glamour's block-level wrap whose boundary overshoots by a
// cell and used to push list and quote text into the clip (C-508).
func unwrapWidth(blk []string, contentW int) int {
	w := contentW + unwrapSlack
	for _, l := range blk {
		if n := ansi.StringWidth(l) + unwrapSlack; n > w {
			w = n
		}
	}
	return w
}

// trimLinePadding cuts a rendered line back to its last visible cell:
// glamour pads every line to the wrap width one styled space at a time, and
// those pad runs must be gone before a line is re-wrapped (their escape
// bytes would otherwise be flushed into the wrapped output).
func trimLinePadding(l string) string {
	if isBlankLine(l) {
		return ""
	}
	visible := strings.TrimRight(ansi.Strip(l), " ")
	return ansi.Truncate(l, ansi.StringWidth(visible), "")
}

// reflowListLines lays a rendered list block out to the approved design:
// an item's continuation lines hang under the item's TEXT column, wrapping
// with the same ANSI-aware wrap a paragraph uses, so a hyphenated token or
// a styled span breaks exactly as it would in prose and no text is ever
// clipped. Every item's marker is recoloured accentHex — including a
// nested item's, which the indent filler's own styling runs used to hide
// from a leading-marker recolour pass. A line that does not open with a
// marker (a loose list's further paragraph, a lazy continuation) keeps its
// own leading indentation as its hang.
func reflowListLines(lines []string, contentW int, accentHex string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = trimLinePadding(l)
		if isBlankLine(l) {
			out = append(out, "")
			continue
		}
		ind, filler, marker, rest, ok := splitListItem(l)
		if !ok {
			out = append(out, reflowIndented(l, ind, contentW)...)
			continue
		}
		// The hang is the item's text column: its indent filler plus the
		// marker plus the separating space.
		hang := ind + ansi.StringWidth(marker) + 1
		sep := ""
		if strings.HasPrefix(rest, " ") {
			sep, rest = " ", rest[1:]
		}
		if accentHex != "" {
			// The marker's own styling runs are replaced by the accent
			// style, so the filler carries only the plain indent spaces.
			filler += ansi.Style{}.ForegroundColor(lipgloss.Color(accentHex)).Styled(marker)
		} else {
			filler += marker
		}
		for i, c := range wrapChunks(rest, hang, contentW) {
			if i == 0 {
				out = append(out, filler+sep+c)
				continue
			}
			out = append(out, strings.Repeat(" ", hang)+c)
		}
	}
	return out
}

// reflowQuoteLines lays a rendered blockquote out: the bar stays on every
// line — continuations included — and the text after the bars wraps like a
// paragraph at whatever room the bars leave (C-508: glamour wraps the bars
// and the text together, and the overflow used to be clipped away). The
// bars are split off and re-attached raw — pad trimming runs on the text
// side only, so a bar's separating space survives for colorizeQuoteBar's
// "│ " token lookahead — which recolours them on every line afterwards.
func reflowQuoteLines(lines []string, contentW int) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		prefix, depth := splitQuotePrefix(l)
		text := trimLinePadding(l[len(prefix):])
		if depth == 0 && isBlankLine(text) {
			out = append(out, "")
			continue
		}
		for _, c := range wrapChunks(text, depth*2, contentW) {
			out = append(out, prefix+c)
		}
	}
	return out
}

// reflowIndented wraps a line that has no marker of its own: the first
// line is kept verbatim, its continuation lines are prefixed with width
// spaces — the line's own leading indentation, kept as its hang.
func reflowIndented(l string, width, contentW int) []string {
	out := make([]string, 0, 2)
	for i, c := range wrapChunks(l, width, contentW) {
		if i == 0 {
			out = append(out, c)
			continue
		}
		out = append(out, strings.Repeat(" ", width)+c)
	}
	return out
}

// wrapChunks wraps s to contentW-width cells with the paragraph wrap
// function and splits it into lines.
func wrapChunks(s string, width, contentW int) []string {
	budget := contentW - width
	if budget < 1 {
		budget = 1
	}
	return strings.Split(lipgloss.Wrap(s, budget, ""), "\n")
}

// splitListItem splits a rendered list line into its leading indent cells,
// the plain-space filler those cells rebuild into, the visible marker text
// and the text region after the marker. ok is false when the line does not
// open with a marker; rest is then the whole line.
func splitListItem(l string) (ind int, filler, marker, rest string, ok bool) {
	ind, i := scanIndent(l)
	rest = l[i:]
	switch {
	case strings.HasPrefix(rest, "•"):
		marker, rest = "•", rest[len("•"):]
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
			marker, rest = num.String()+".", rest[1:]
		}
	}
	if marker == "" {
		return ind, "", "", l, false
	}
	return ind, strings.Repeat(" ", ind), marker, rest, true
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

// indentCodeLines draws a fenced block's lines with the approved design's
// two-column indent, which the renderBlock clip then counts toward the
// content width. A blank code line stays blank — in a fragment it must
// carry no trailing spaces — so its chroma leftover escape runs are
// dropped.
func indentCodeLines(lines []string) []string {
	for i, l := range lines {
		l = trimLinePadding(l)
		if isBlankLine(l) {
			lines[i] = ""
			continue
		}
		lines[i] = "  " + l
	}
	return lines
}

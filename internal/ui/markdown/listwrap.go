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

// reflowCfg tells reflowSoftWrapped which block kind it is laying out.
type reflowCfg struct {
	quote     bool    // lines lead with quote bars; paragraph runs join by style
	accentHex string  // list markers recolour to this (list blocks only)
	quoteKey  styleID // the leading style of quote paragraph text (quote blocks only)
}

// reflowListLines lays a rendered list block out to the approved design:
// an item's continuation lines hang under the item's TEXT column, wrapping
// with the same ANSI-aware wrap a paragraph uses, so a hyphenated token or
// a styled span breaks exactly as it would in prose and no text is ever
// clipped (C-508). The source's soft line breaks are paragraphs too: a
// rendered line without a marker continues the item's text and the whole
// paragraph re-wraps as one (R-507). Every item's marker is recoloured
// accentHex — including a nested item's. A blank line stays blank.
func reflowListLines(lines []string, contentW int, accentHex string) []string {
	return reflowSoftWrapped(lines, contentW, reflowCfg{accentHex: accentHex})
}

// reflowQuoteLines lays a rendered blockquote out: the bar stays on every
// line — continuations included — and the text after the bars wraps like a
// paragraph at whatever room the bars leave (C-508). A soft-wrapped quote
// paragraph's rendered lines are re-joined and re-wrapped as ONE paragraph
// (R-507): glamour itself loses the bar on such a line mid-paragraph, so
// every line takes the run's own depth. Only quote paragraph text —
// glamour's Muted-italic primitive, quoteKey — joins across lines; a
// heading, a fence or a nested list item keeps its own line(s).
func reflowQuoteLines(lines []string, contentW int, quoteKey styleID) []string {
	return reflowSoftWrapped(lines, contentW, reflowCfg{quote: true, quoteKey: quoteKey})
}

// reflowSoftWrapped lays a rendered list or quote block out. It walks the
// block's rendered lines accumulating one paragraph at a time: a marker
// line opens an item paragraph; any further line whose leading style
// matches the open paragraph's (always, in a list block) joins its text;
// a blank line — or a quote line that is bare bars — closes the paragraph.
// Each closed paragraph is then re-wrapped at the room its bars and hang
// leave, the first line carrying its raw marker run and every continuation
// line hanging under the item's text column.
func reflowSoftWrapped(lines []string, contentW int, cfg reflowCfg) []string {
	out := make([]string, 0, len(lines))
	var cur *blockPara
	flush := func() {
		if cur != nil {
			out = append(out, cur.emit(contentW)...)
			cur = nil
		}
	}
	for _, line := range lines {
		bars, depth := "", 0
		var text string
		if cfg.quote {
			// The bars are split off the RAW line: on a bars-only margin
			// line the token's separating space is the line's last visible
			// cell, and padding-trimming the line first would eat it —
			// leaving a bar colorizeQuoteBar's bar-token lookahead no
			// longer recognises.
			bars, depth = splitQuotePrefix(line)
			text = trimLinePadding(line[len(bars):])
		} else {
			text = trimLinePadding(line)
		}
		if isBlankLine(text) {
			// A blank line, or a quote line that is nothing but bars:
			// glamour's own margin. Paragraph boundary, kept as-is.
			flush()
			if depth > 0 {
				out = append(out, bars)
			} else {
				out = append(out, "")
			}
			continue
		}
		item, ok := splitListItem(text)
		key, styled := leadingStyleKey(text)
		switch {
		case ok:
			flush()
			cur = newItemPara(cfg, bars, depth, item, key)
		case cur != nil && cur.absorbs(cfg, key, styled):
			// The line's own indent is dropped: it joins the item's
			// paragraph, whose hang decides every column. The indent may be
			// styled cells, not plain spaces, so skip exactly that many
			// cells — never the line's own styling runs.
			cur.text += " " + skipIndentCells(item.rest, item.ind)
		default:
			flush()
			cur = &blockPara{
				bars: bars, depth: depth, hang: item.ind,
				filler: strings.Repeat(" ", item.ind), text: skipIndentCells(item.rest, item.ind),
				key: key, absorb: !styled || key == cfg.quoteKey,
			}
		}
	}
	flush()
	return out
}

// blockPara is one accumulated paragraph of a list or quote block: the
// quote bars every line re-attaches, the hang its continuations indent to,
// the first line's raw marker run, and the text its soft line breaks
// joined into.
type blockPara struct {
	bars   string  // the quote bars re-attached to every line ("" in a list)
	depth  int     // how many bars that is
	hang   int     // text column beyond the bars: indent + marker + separator
	filler string  // the first line's indent spaces plus its raw marker run
	sep    string  // the marker's separating space (quote paras keep it in filler)
	text   string  // the paragraph's text, soft breaks joined with spaces
	key    styleID // the paragraph's leading text style (quote-run matching)
	absorb bool    // whether further lines may join
}

// newItemPara opens an item paragraph from a marker line: the hang is the
// item's text column, the first line keeps the marker's raw bytes (a list
// block recolours them accent, which is the one rebuild the marker
// recolour pass may make), and continuations always join — a soft-wrapped
// item's every line belongs to it.
func newItemPara(cfg reflowCfg, bars string, depth int, item listItem, key styleID) *blockPara {
	p := &blockPara{
		bars: bars, depth: depth,
		hang: item.ind + ansi.StringWidth(item.marker) + 1,
		text: strings.TrimLeft(item.rest, " "),
		key:  key, absorb: true,
		sep: item.sep,
	}
	p.filler = strings.Repeat(" ", item.ind)
	if cfg.quote {
		// The raw run already carries the marker's separating space, so
		// the paragraph's own sep stays empty.
		p.filler += strings.TrimLeft(item.raw, " ")
		p.sep = ""
		return p
	}
	if cfg.accentHex != "" {
		item.marker = ansi.Style{}.ForegroundColor(lipgloss.Color(cfg.accentHex)).Styled(item.marker)
	}
	p.filler += item.marker
	return p
}

// absorbs reports whether a line with the given leading style may join the
// paragraph: a list block's continuations always join, and so does any
// line that carries no styling runs of its own; a quote paragraph joins
// only its own kind of text, so a heading's, a fence's or a nested list
// item's lines never merge into prose.
func (p *blockPara) absorbs(cfg reflowCfg, key styleID, styled bool) bool {
	if !p.absorb {
		return false
	}
	if !cfg.quote || !styled {
		return true
	}
	return key == p.key
}

// emit lays the paragraph out: its text re-wrapped at the room the bars
// and the hang leave, the first line carrying the raw marker run, every
// continuation line hung under the item's text column.
func (p *blockPara) emit(contentW int) []string {
	var out []string
	for i, c := range wrapChunks(p.text, p.depth*2+p.hang, contentW) {
		if i == 0 {
			out = append(out, p.bars+p.filler+p.sep+c)
			continue
		}
		out = append(out, p.bars+strings.Repeat(" ", p.hang)+c)
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

// listItem is one rendered list line's parts: the leading indent cells,
// the marker's raw byte run from its first byte through its separating
// space (styling included — the quote pass re-attaches it verbatim), the
// separating space on its own, the visible marker text and the text after
// the separator.
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

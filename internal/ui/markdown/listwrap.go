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
// accentHex — a bullet, an ordered number and a task checkbox alike —
// including a nested item's. A blockquote nested in an item is laid out as
// a paragraph of the item's own, the bar on every line (R-508). A blank
// line stays blank.
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
// line — bullet, ordered number or task checkbox — opens an item
// paragraph; any further line whose leading style matches the open
// paragraph's (always, in a list block) joins its text; a bar line in a
// list block opens the nested quote's own paragraph instead (R-508); a
// blank line — or a quote line that is bare bars — closes the paragraph.
// Each closed paragraph is then re-wrapped at the room its bars, lead and
// hang leave, the first line carrying its raw marker run and every
// continuation line hanging under the item's text column.
func reflowSoftWrapped(lines []string, contentW int, cfg reflowCfg) []string {
	out := make([]string, 0, len(lines))
	var cur *blockPara
	// List blocks only: the open item's text column — where a quote
	// nested in the item indents to, and where the item's own text
	// resumes after it — and whether the previous line belonged to such
	// a quote.
	itemCol, afterQuote := 0, false
	flush := func() {
		if cur != nil {
			out = append(out, cur.emit(contentW)...)
			cur = nil
		}
	}
	for _, line := range lines {
		bars, depth, text := blockLinePrefix(line, cfg.quote)
		if isBlankLine(text) {
			// A blank line, or a quote line that is nothing but bars:
			// glamour's own margin. Paragraph boundary, kept as-is.
			flush()
			if depth > 0 {
				lead := ""
				if !cfg.quote {
					lead = strings.Repeat(" ", itemCol)
				}
				out = append(out, lead+bars)
			} else {
				out = append(out, "")
				itemCol, afterQuote = 0, false
			}
			continue
		}
		item, ok := splitListItem(text)
		key, styled := leadingStyleKey(text)
		switch {
		case ok:
			flush()
			cur = newItemPara(cfg, bars, depth, item, key)
			if !cfg.quote {
				itemCol, afterQuote = cur.hang, false
			}
		case !cfg.quote && depth > 0:
			// A blockquote's bar line inside a list item (R-508): the
			// quote is its own paragraph inside the item — never item
			// text — indented to the item's text column with the bar on
			// every one of its lines. Lines of the same quote paragraph
			// join it, as they do in a top-level quote.
			if cur == nil || !cur.quoteLine || cur.depth != depth ||
				!cur.absorbs(key, styled) {
				flush()
				cur = newQuoteInItemPara(itemCol, bars, depth, text, key)
			} else {
				ind, _ := scanIndent(text)
				cur.text += " " + skipIndentCells(text, ind)
			}
			afterQuote = true
		case cur != nil && cur.absorbs(key, styled):
			// The line's own indent is dropped: it joins the item's
			// paragraph, whose hang decides every column. The indent may be
			// styled cells, not plain spaces, so skip exactly that many
			// cells — never the line's own styling runs.
			cur.text += " " + skipIndentCells(item.rest, item.ind)
		default:
			flush()
			hang, fill := item.ind, strings.Repeat(" ", item.ind)
			if !cfg.quote && afterQuote {
				// The item's text resumes after its nested quote: it hangs
				// under the item's text column again, never at column 0.
				hang, fill = itemCol, strings.Repeat(" ", itemCol)
			}
			cur = &blockPara{
				bars: bars, depth: depth, hang: hang, filler: fill,
				text: skipIndentCells(item.rest, item.ind),
				key:  key, absorb: !styled || key == cfg.quoteKey,
				byKey: cfg.quote,
			}
		}
	}
	flush()
	return out
}

// blockPara is one accumulated paragraph of a list or quote block: the
// quote bars every line re-attach, the hang its continuations indent to,
// the first line's raw marker run, and the text its soft line breaks
// joined into.
type blockPara struct {
	lead      string  // plain indent spaces before the bars on EVERY line: a quote nested in a list item indents to the item's text column (R-508)
	bars      string  // the quote bars re-attached to every line ("" in a list)
	depth     int     // how many bars that is
	hang      int     // text column beyond the bars: indent + marker + separator
	filler    string  // the first line's indent spaces plus its raw marker run
	sep       string  // the marker's separating space (quote paras keep it in filler)
	text      string  // the paragraph's text, soft breaks joined with spaces
	key       styleID // the paragraph's leading text style (quote-run matching)
	absorb    bool    // whether further lines may join
	byKey     bool    // joins only lines sharing its leading style (quote text)
	quoteLine bool    // a nested blockquote's own text inside a list item (R-508)
}

// newItemPara opens an item paragraph from a marker line: the hang is the
// item's text column — for a task item that is the checkbox's 3 cells plus
// the separator — the first line keeps the marker's raw bytes (a list
// block recolours them accent, which is the one rebuild the marker
// recolour pass may make), and continuations always join — a soft-wrapped
// item's every line belongs to it.
func newItemPara(cfg reflowCfg, bars string, depth int, item listItem, key styleID) *blockPara {
	p := &blockPara{
		bars: bars, depth: depth,
		hang: item.ind + ansi.StringWidth(item.marker) + 1,
		text: strings.TrimLeft(item.rest, " "),
		key:  key, absorb: true, byKey: cfg.quote,
		sep: item.sep,
	}
	p.filler = strings.Repeat(" ", item.ind)
	if bars != "" {
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

// newQuoteInItemPara opens the paragraph of a blockquote nested in a list
// item (R-508): every line indents to the owning item's text column and
// carries the quote bars verbatim — colorizeQuoteBar recolours them Border
// after the layout, exactly as it does for a top-level quote. The bars
// leave no text column beyond themselves, so hang is 0.
func newQuoteInItemPara(itemCol int, bars string, depth int, text string, key styleID) *blockPara {
	return &blockPara{
		lead:      strings.Repeat(" ", itemCol),
		bars:      bars,
		depth:     depth,
		text:      strings.TrimLeft(text, " "),
		key:       key,
		absorb:    true,
		byKey:     true,
		quoteLine: true,
	}
}

// absorbs reports whether a line with the given leading style may join the
// paragraph: a paragraph joins any line that carries no styling runs of
// its own; a list item's continuations always join, and a quote paragraph
// joins only its own kind of text, so a heading's, a fence's or a nested
// list item's lines never merge into prose.
func (p *blockPara) absorbs(key styleID, styled bool) bool {
	if !p.absorb {
		return false
	}
	if !p.byKey || !styled {
		return true
	}
	return key == p.key
}

// emit lays the paragraph out: its text re-wrapped at the room the lead,
// the bars and the hang leave, the first line carrying the raw marker run,
// every continuation line hung under the item's text column.
func (p *blockPara) emit(contentW int) []string {
	var out []string
	budget := ansi.StringWidth(p.lead) + p.depth*2 + p.hang
	for i, c := range wrapChunks(p.text, budget, contentW) {
		if i == 0 {
			out = append(out, p.lead+p.bars+p.filler+p.sep+c)
			continue
		}
		out = append(out, p.lead+p.bars+strings.Repeat(" ", p.hang)+c)
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

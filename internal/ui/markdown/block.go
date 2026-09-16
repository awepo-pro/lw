package markdown

import (
	"fmt"
	"strings"
	"unicode/utf8"

	gansi "charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
)

// renderBlock renders one top-level block (as split by splitBlocks) through
// a fresh glamour TermRenderer, inserting provenance/wikilink sentinel
// markers first unless the block is a fence (preprocess.go). It restyles
// those markers (inline.go), trims every leading and trailing blank line
// glamour's own margin/spacing adds, recolours the runes glamour cannot
// style per-role itself — a table's header row and separators, a
// blockquote's bar, a list's marker — and lays the block out to contentW
// (listwrap.go): list items hang under their text column, a quote keeps its
// bar on every line, fences indent two, and no non-fence line is ever
// clipped (C-508) except a single unbreakable token.
//
// Lists, quotes and fences render at unwrapWidth, so glamour itself never
// breaks them and the layout passes own every wrap decision; tables and
// prose render at contentW through glamour's own paragraph wrap exactly as
// before.
func renderBlock(blk []string, cfg gansi.StyleConfig, contentW int, style Style) ([]string, error) {
	text := strings.Join(blk, "\n")
	if isFenceBlock(blk) {
		// No marker syntax is recognized inside a fence, but a sentinel
		// -like rune already in the code sample must still be protected
		// (repair-2, R2) before restyleSpans runs unconditionally below.
		text = escapeSourceSentinels(text)
	} else {
		text = insertMarkers(text)
	}

	cfg = withChromaTheme(cfg, blk, style)

	wrapW := contentW
	if isFenceBlock(blk) || isListBlock(blk) || isBlockquoteBlock(blk) {
		wrapW = unwrapWidth(blk, contentW)
	}
	r, err := newBlockRenderer(cfg, wrapW)
	if err != nil {
		return nil, fmt.Errorf("markdown: build block renderer: %w", err)
	}
	out, err := r.Render(text)
	if err != nil {
		return nil, fmt.Errorf("markdown: render block: %w", err)
	}

	out = restyleSpans(out, []span{
		// Provenance is Muted, not Faint (A-5-1 change 3 / D-5C): Faint is
		// unreadable on a transparent terminal, and Ask's answers render
		// here, so the marker must match ask/inline.go's Muted.
		{open: provOpen, close: provClose, openSGR: fgSGR(style.Muted, false)},
		{open: wikiOpen, close: wikiClose, openSGR: fgSGR(style.Accent, true)},
	})
	out = unescapeMarkers(out)

	lines := strings.Split(out, "\n")
	lines = trimBlankEdges(lines)
	switch {
	case isFenceBlock(blk):
		lines = indentCodeLines(lines)
	case isTableBlock(blk):
		lines = colorizeTableHeader(lines, style)
		lines = colorizeTableSeparators(lines, style.Border)
	case isBlockquoteBlock(blk):
		lines = reflowQuoteLines(lines, contentW, quoteTextStyleKey(style))
		lines = colorizeQuoteBar(lines, style.Border)
	case isListBlock(blk):
		lines = reflowListLines(lines, contentW, style.Accent)
	}
	// Safety clip: a fenced code line longer than the content width (the
	// indent counts toward it) and a table wider than it end in "…". Every
	// other block's layout pass keeps its lines within contentW, so this
	// never fires on prose — the NoTextLost tests hold it to that.
	for i, l := range lines {
		if ansi.StringWidth(l) > contentW {
			lines[i] = ansi.Truncate(l, contentW, "…")
		}
	}
	return lines, nil
}

// withChromaTheme returns a copy of cfg whose CodeBlock.Theme selects s's
// registered chroma style (chroma.go) when blk is a fence whose language
// chroma has a lexer for. Otherwise Theme stays "": glamour then renders
// the block through its fallback path, plain text in the code block's own
// style primitive (Fg) — an unknown or empty language never reaches chroma,
// so nothing can be highlighted by accident (contract §2 note 3).
func withChromaTheme(cfg gansi.StyleConfig, blk []string, s Style) gansi.StyleConfig {
	cfg.CodeBlock.Theme = ""
	if lang := fenceLanguage(blk); lang != "" && lexers.Get(lang) != nil {
		cfg.CodeBlock.Theme = chromaTheme(s)
	}
	return cfg
}

// fenceLanguage returns the language a fenced block declares, extracted the
// way goldmark's FencedCodeBlock.Language does: the first space-delimited
// word of the opening fence's info line. "" for a bare or indented fence.
func fenceLanguage(blk []string) string {
	if len(blk) == 0 {
		return ""
	}
	info := strings.TrimLeft(blk[0], " \t")
	info = strings.TrimPrefix(info, "```")
	if i := strings.IndexByte(info, ' '); i >= 0 {
		info = info[:i]
	}
	return info
}

// isBlockquoteBlock reports whether blk opens as a blockquote (its first
// line starts with ">"; a blockquote is one block, and no other block kind
// opens with ">").
func isBlockquoteBlock(blk []string) bool {
	return len(blk) > 0 && strings.HasPrefix(strings.TrimLeft(blk[0], " \t"), ">")
}

// colorizeTableHeader lifts the table's header row (the first rendered
// line) from its cells' Fg to Accent + bold (contract §2 note 2). glamour
// styles every cell with the table's own StylePrimitive, so the header
// line carries that exact Fg SGR once per cell — head and body cells are
// styled identically and no other SGR on the line can equal it — and
// replacing each occurrence leaves the cell content, padding and
// (recoloured later) separators untouched.
func colorizeTableHeader(lines []string, s Style) []string {
	if len(lines) == 0 {
		return lines
	}
	cellSGR := ansi.Style{}.ForegroundColor(lipgloss.Color(s.Fg)).String()
	headSGR := ansi.Style{}.ForegroundColor(lipgloss.Color(s.Accent)).Bold().String()
	if cellSGR == "" || headSGR == "" || cellSGR == headSGR || !strings.Contains(lines[0], cellSGR) {
		return lines
	}
	lines[0] = strings.ReplaceAll(lines[0], cellSGR, headSGR)
	return lines
}

// isListBlock reports whether blk opens as a markdown list: a bullet item
// ("- ", "* ", "+ ") or an ordered one ("12." / "12)"). Only list blocks
// get the marker recolour pass, so a "•" or "1." typed at the start of a
// real paragraph is never touched.
func isListBlock(blk []string) bool {
	if len(blk) == 0 {
		return false
	}
	l := strings.TrimLeft(blk[0], " \t")
	switch {
	case strings.HasPrefix(l, "- "), strings.HasPrefix(l, "* "), strings.HasPrefix(l, "+ "):
		return true
	}
	d := 0
	for d < len(l) && l[d] >= '0' && l[d] <= '9' {
		d++
	}
	return d > 0 && d < len(l) && (l[d] == '.' || l[d] == ')') &&
		(d+1 == len(l) || l[d+1] == ' ' || l[d+1] == '\t')
}

// colorizeTableSeparators recolours a rendered table block's own separator
// runes in borderHex (s0-foundation.md T02 item 1: "tables with
// Border-coloured separators"), without touching a "─", "│" or "┼"
// typed inside real cell text (repair-1 minor finding, block.go:53): glamour's
// lipgloss-backed table draws these bare, with no colour of their own, and
// every row shares one fixed set of column widths, so the rule row (the
// one line made of nothing but "─"/"┼") tells us exactly which cell
// columns are separators. Only a "│" at one of those columns — and the
// whole rule row itself — gets recoloured; a stray box-drawing rune typed
// inside a cell's own text is never touched.
func colorizeTableSeparators(lines []string, borderHex string) []string {
	ruleIdx := -1
	for i, l := range lines {
		if isRuleLine(l) {
			ruleIdx = i
			break
		}
	}
	if ruleIdx < 0 {
		return lines
	}

	st := ansi.Style{}.ForegroundColor(lipgloss.Color(borderHex))
	sepCols := separatorColumns(ansi.Strip(lines[ruleIdx]))

	out := make([]string, len(lines))
	for i, l := range lines {
		if i == ruleIdx {
			out[i] = st.Styled(ansi.Strip(l))
			continue
		}
		out[i] = recolorColumns(l, sepCols, st)
	}
	return out
}

// isRuleLine reports whether l (ANSI stripped) consists solely of "─" and
// "┼" runes (glamour never colours these, so stripping is lossless here).
func isRuleLine(l string) bool {
	stripped := ansi.Strip(l)
	if stripped == "" {
		return false
	}
	saw := false
	for _, r := range stripped {
		if r != '─' && r != '┼' {
			return false
		}
		saw = true
	}
	return saw
}

// separatorColumns returns the 0-based cell-width offsets (not rune
// indices — a wide/CJK cell shifts these) of every "┼" in a stripped rule
// line: exactly the columns a "│" occupies in every other row of the same
// table, since lipgloss draws every row at the same fixed column widths.
func separatorColumns(ruleStripped string) map[int]bool {
	cols := map[int]bool{}
	col := 0
	for _, r := range ruleStripped {
		if r == '┼' {
			cols[col] = true
		}
		col += ansi.StringWidth(string(r))
	}
	return cols
}

// recolorColumns walks l cell-width by cell-width (ANSI sequences pass
// through without advancing the column), wrapping only a "│" whose column
// is in sepCols with st.
func recolorColumns(l string, sepCols map[int]bool, st ansi.Style) string {
	var b strings.Builder
	col := 0
	i := 0
	for i < len(l) {
		if seq, n, _, ok := decodeSGR(l[i:]); ok {
			b.WriteString(seq)
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(l[i:])
		if r == '│' && sepCols[col] {
			b.WriteString(st.Styled(string(r)))
		} else {
			b.WriteRune(r)
		}
		col += ansi.StringWidth(string(r))
		i += size
	}
	return b.String()
}

// trimBlankEdges drops every leading and trailing line that is blank once
// stripped of ANSI and surrounding whitespace.
func trimBlankEdges(lines []string) []string {
	start := 0
	for start < len(lines) && isBlankLine(lines[start]) {
		start++
	}
	end := len(lines)
	for end > start && isBlankLine(lines[end-1]) {
		end--
	}
	return lines[start:end]
}

func isBlankLine(l string) bool {
	return strings.TrimSpace(ansi.Strip(l)) == ""
}

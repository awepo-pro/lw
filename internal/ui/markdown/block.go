package markdown

import (
	"fmt"
	"strings"
	"unicode/utf8"

	gansi "charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderBlock renders one top-level block (as split by splitBlocks) through
// a fresh glamour TermRenderer at contentW cells (contract §2 note 3),
// inserting provenance/wikilink sentinel markers first unless the block is
// a fence (preprocess.go). It restyles those markers (inline.go), trims
// every leading and trailing blank line glamour's own margin/spacing adds,
// recolours a table block's own separators, and clips any line still wider
// than contentW: glamour never word-wraps a fenced code block, so a long
// code line is the one realistic case that needs it.
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

	r, err := newBlockRenderer(cfg, contentW)
	if err != nil {
		return nil, fmt.Errorf("markdown: build block renderer: %w", err)
	}
	out, err := r.Render(text)
	if err != nil {
		return nil, fmt.Errorf("markdown: render block: %w", err)
	}

	out = restyleSpans(out, []span{
		{open: provOpen, close: provClose, openSGR: fgSGR(style.Faint, false)},
		{open: wikiOpen, close: wikiClose, openSGR: fgSGR(style.Fg, true)},
	})
	out = unescapeMarkers(out)

	lines := strings.Split(out, "\n")
	lines = trimBlankEdges(lines)
	if isTableBlock(blk) {
		lines = colorizeTableSeparators(lines, style.Border)
	}
	for i, l := range lines {
		if ansi.StringWidth(l) > contentW {
			lines[i] = ansi.Truncate(l, contentW, "…")
		}
	}
	return lines, nil
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

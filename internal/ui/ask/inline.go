// inline.go is the ask screen's inline renderer — a port of mockgen.inline
// plus the styled-cell word-wrap its callers use (mockgen.ask_conversation
// wraps inline(ANSWER), so markers are consumed before the text wraps):
// `**b**` bold, `c` in the Code token with backticks removed, `[[x]]`
// Accent + underlined x (W5 F3/D-3W), `^[p]` muted `[base]` (A-5-1/D-5C:
// the user's transparent-terminal complaint — Faint is unreadable on one),
// `*i*` italic. 005 demotes this renderer to the live turn's plain tail
// and Ask's own chrome; settled prose renders through the shared markdown
// renderer (transcript.go).
package ask

import (
	"path/filepath"
	"regexp"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// cell is one character of a styled transcript line: its rune plus the id
// of the style it renders with (1-based into a per-render table; 0 is a
// literal, unstyled cell).
type cell struct {
	r  rune
	id int
}

// Inline markers, mockgen.INLINE verbatim: **bold**, `code`, [[link]],
// ^[provenance], *italic*.
var inlineRe = regexp.MustCompile(`\*\*(.+?)\*\*|` + "`" + `([^` + "`" + `]+)` + "`" + `|\[\[([^\]]+)\]\]|\^\[([^\]]+)\]|\*([^*]+)\*`)

// inlineStyleTable is the per-render style table the cell ids index. It
// mirrors mockgen.inline's styles against the theme tokens: base fg, bold,
// Code-token code span and Accent-underlined wikilink (W5 F3/D-3W), muted
// provenance (A-5-1/D-5C), italic.
func (m *Model) inlineStyleTable() []lipgloss.Style {
	return []lipgloss.Style{
		{},                             // 1: base (unstyled fg)
		m.theme.Bold,                   // 2: **bold**
		m.theme.Code,                   // 3: `code`
		m.theme.Accent.Underline(true), // 4: [[wikilink]]
		m.theme.Muted,                  // 5: ^[provenance] (A-5-1/D-5C: was Faint)
		m.theme.Fg.Italic(true),        // 6: *italic*
	}
}

// inlineCells converts one line of assistant text into styled cells
// (mockgen.inline): markers are consumed, `[[x|label]]` becomes the
// underlined target x, `^[p]` becomes muted `[base(p)]` (A-5-1/D-5C), code
// spans lose their backticks. The base style id is 1.
func inlineCells(text string) []cell {
	var out []cell
	put := func(s string, id int) {
		for _, r := range s {
			out = append(out, cell{r: r, id: id})
		}
	}
	pos := 0
	for _, loc := range inlineRe.FindAllStringSubmatchIndex(text, -1) {
		put(text[pos:loc[0]], 1)
		switch {
		case loc[2] >= 0:
			put(text[loc[2]:loc[3]], 2)
		case loc[4] >= 0:
			put(text[loc[4]:loc[5]], 3)
		case loc[6] >= 0:
			put(linkTarget(text[loc[6]:loc[7]]), 4)
		case loc[8] >= 0:
			put("["+filepath.Base(text[loc[8]:loc[9]])+"]", 5)
		case loc[10] >= 0:
			put(text[loc[10]:loc[11]], 6)
		}
		pos = loc[1]
	}
	put(text[pos:], 1)
	return out
}

// linkTarget strips a wikilink's `|label` half: `[[x|label]]` renders as x
// (mockgen.inline: link.split('|')[0]).
func linkTarget(s string) string {
	if i := strings.IndexByte(s, '|'); i >= 0 {
		return s[:i]
	}
	return s
}

// inlineWrap renders assistant text at w: each line inline-marked, then
// word-wrapped as styled cells — the markers change the text before it
// wraps (mockgen.ask_conversation wraps inline(ANSWER)), so wrapping must
// see the converted text, not the source.
func (m *Model) inlineWrap(text string, w int) []string {
	if w < 1 {
		w = 1
	}
	table := m.inlineStyleTable()
	var out []string
	for _, para := range strings.Split(text, "\n") {
		out = append(out, wrapStyled(inlineCells(para), table, w, 0)...)
	}
	return out
}

// wrapStyled is mockgen.wrap over styled cells: greedy on spaces,
// continuation lines hung by hang spaces, words longer than the line
// hard-split. A separating space takes the style of the cell before it;
// hang cells are unstyled. It never returns an empty slice.
func wrapStyled(cells []cell, table []lipgloss.Style, w, hang int) []string {
	var words [][]cell
	var word []cell
	for _, c := range cells {
		if c.r == ' ' {
			if len(word) > 0 {
				words = append(words, word)
				word = nil
			}
			continue
		}
		word = append(word, c)
	}
	if len(word) > 0 {
		words = append(words, word)
	}

	var lines [][]cell
	var line []cell
	capacity := func() int {
		c := w
		if len(lines) > 0 {
			c -= hang
		}
		if c < 1 {
			c = 1
		}
		return c
	}

	for _, wd := range words {
		sep := 0
		if len(line) > 0 {
			sep = 1
		}
		if len(line)+sep+len(wd) <= capacity() {
			if len(line) > 0 {
				line = append(line, cell{r: ' ', id: line[len(line)-1].id})
			}
			line = append(line, wd...)
			continue
		}
		if len(line) > 0 {
			lines = append(lines, line)
			line = nil
		}
		for len(wd) > capacity() {
			n := capacity()
			lines = append(lines, wd[:n])
			wd = wd[n:]
		}
		line = wd
	}
	if len(line) > 0 || len(lines) == 0 {
		lines = append(lines, line)
	}

	out := make([]string, len(lines))
	for i, l := range lines {
		if i > 0 && hang > 0 {
			padded := make([]cell, hang, hang+len(l))
			for j := range padded {
				padded[j] = cell{r: ' '}
			}
			l = append(padded, l...)
		}
		out[i] = renderCells(l, table)
	}
	return out
}

// renderCells flattens styled cells into one string, grouping consecutive
// same-style cells into a single Render call.
func renderCells(cs []cell, table []lipgloss.Style) string {
	var b strings.Builder
	i := 0
	for i < len(cs) {
		j := i + 1
		for j < len(cs) && cs[j].id == cs[i].id {
			j++
		}
		runes := make([]rune, j-i)
		for k := i; k < j; k++ {
			runes[k-i] = cs[k].r
		}
		text := string(runes)
		if id := cs[i].id; id > 0 {
			b.WriteString(table[id-1].Render(text))
		} else {
			b.WriteString(text)
		}
		i = j
	}
	return b.String()
}

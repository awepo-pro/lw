package conformance

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The nine-token palette's accent and cursor colours, as the SGR parameters
// lipgloss emits for them (contract §9 note 6): dark and light.
const (
	darkAccentFg  = "38;2;122;178;242" // #7AB2F2
	darkCursorBg  = "48;2;26;35;49"    // #1A2331
	lightAccentFg = "38;2;29;98;194"   // #1D62C2
	lightCursorBg = "48;2;230;238;249" // #E6EEF9
	backgroundSGR = "48;2;"
	borderRunes   = "╭╮╰╯─│┬┴├┤"
	cursorGutter  = "▌"
)

// colourSet is one polarity's expected accent and cursor colours.
type colourSet struct {
	polarity string
	accentFg string
	cursorBg string
}

var (
	darkColours  = colourSet{polarity: "dark", accentFg: darkAccentFg, cursorBg: darkCursorBg}
	lightColours = colourSet{polarity: "light", accentFg: lightAccentFg, cursorBg: lightCursorBg}
)

// checkColours runs contract §9 note 6's colour checks on one render.
// The background-discipline rule — no background SGR on any row without a
// `▌` cursor gutter — holds on every frame. The focused-panel and
// cursor-row checks apply to the frames that have a panel or a cursor row
// at all: the too-small notice, the shell below D11's minimum, has neither.
func checkColours(t *testing.T, styled, plain string, cs colourSet) {
	t.Helper()

	styledRows := strings.Split(styled, "\n")
	plainRows := strings.Split(plain, "\n")
	if len(styledRows) != len(plainRows) {
		t.Errorf("%s: styled and plain row counts differ (%d vs %d)",
			cs.polarity, len(styledRows), len(plainRows))
		return
	}

	badBackgrounds := 0
	for i := range styledRows {
		if !strings.Contains(plainRows[i], cursorGutter) && strings.Contains(styledRows[i], backgroundSGR) {
			if badBackgrounds == 0 {
				t.Errorf("%s: row %d carries a background colour but shows no %s cursor gutter",
					cs.polarity, i+1, cursorGutter)
			}
			badBackgrounds++
		}
	}
	if badBackgrounds > 1 {
		t.Errorf("%s: %d more rows carry a background colour without a cursor gutter",
			cs.polarity, badBackgrounds-1)
	}

	if hasAnyRune(plainRows, borderRunes) && !cellCarries(styledRows, borderRunes, cs.accentFg) {
		t.Errorf("%s: no panel border rune carries the focused accent %s", cs.polarity, cs.accentFg)
	}
	if hasAnyRune(plainRows, cursorGutter) && !cellCarries(styledRows, cursorGutter, cs.cursorBg) {
		t.Errorf("%s: no cursor row carries the cursor background %s", cs.polarity, cs.cursorBg)
	}
}

// hasAnyRune reports whether any line draws a rune from set.
func hasAnyRune(lines []string, set string) bool {
	for _, line := range lines {
		if strings.ContainsAny(line, set) {
			return true
		}
	}
	return false
}

// cellCarries reports whether any cell drawn from set's runes is styled
// with sgr.
func cellCarries(lines []string, set, sgr string) bool {
	for _, line := range lines {
		for _, c := range scanCells(line) {
			if strings.ContainsRune(set, c.r) && strings.Contains(c.sgr, sgr) {
				return true
			}
		}
	}
	return false
}

// styledCell is one cell of a styled line: its rune and the SGR parameters
// in force for it.
type styledCell struct {
	r   rune
	sgr string
}

// scanCells splits a styled line into cells. The frames carry SGR
// sequences only — lipgloss emits a complete SGR, then a reset — so the
// scanner tracks the last sequence's parameters as each cell's style:
// `ESC [ params m` sets it, `ESC [ 0 m` and `ESC [ m` clear it, and every
// other rune is one cell of content. (Cells, not bytes: a `▌` or `░` is
// one cell however many bytes it encodes as.)
func scanCells(line string) []styledCell {
	var (
		cells []styledCell
		cur   string
		i     int
	)
	for i < len(line) {
		if line[i] == 0x1b && strings.HasPrefix(line[i:], "\x1b[") {
			if end := strings.IndexByte(line[i+2:], 'm'); end >= 0 {
				if params := line[i+2 : i+2+end]; params == "" || params == "0" {
					cur = ""
				} else {
					cur = params
				}
				i += 2 + end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		cells = append(cells, styledCell{r: r, sgr: cur})
		i += size
	}
	return cells
}

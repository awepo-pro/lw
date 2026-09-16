package conformance

import (
	"fmt"
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
	// overlay marks the shell's `?` overlay frames (contract §5 note 4):
	// the frame under the Keys box is stripped of every colour and
	// rendered faint, so a `▌` behind the overlay has no cursor
	// background. The overlay rules replace the cursor-background rule
	// (contract §9 note 6, C30/D-3S) and are selected by the subtest's
	// view name at the call site — never inferred from the frame's
	// content.
	overlay bool
}

var (
	darkColours  = colourSet{polarity: "dark", accentFg: darkAccentFg, cursorBg: darkCursorBg}
	lightColours = colourSet{polarity: "light", accentFg: lightAccentFg, cursorBg: lightCursorBg}
)

// colourSetsFor returns the dark and light colour sets for one view's
// subtests. The `keys-*` grids render the shell's `?` overlay, so they
// check the amended overlay rules (contract §9 note 6, C30/D-3S); every
// other view keeps the original rules unchanged.
func colourSetsFor(view string) (dark, light colourSet) {
	dark, light = darkColours, lightColours
	if strings.HasPrefix(view, "keys-") {
		dark.overlay = true
		light.overlay = true
	}
	return dark, light
}

// checkColours reports every violation of contract §9 note 6's colour
// checks on one render.
func checkColours(t *testing.T, styled, plain string, cs colourSet) {
	t.Helper()
	for _, problem := range colourProblems(styled, plain, cs) {
		t.Errorf("%s: %s", cs.polarity, problem)
	}
}

// colourProblems runs the colour checks over one render and returns one
// problem per violation, without the polarity prefix. The background
// rules and the cursor-row rule depend on the frame kind; the
// focused-accent rule holds on every frame that has a panel border at
// all — under the overlay the Keys box is the frame's only accent-bearing
// border — and the too-small notice, the shell below D11's minimum, has
// neither panel nor cursor row.
func colourProblems(styled, plain string, cs colourSet) []string {
	styledRows := strings.Split(styled, "\n")
	plainRows := strings.Split(plain, "\n")
	if len(styledRows) != len(plainRows) {
		return []string{fmt.Sprintf("styled and plain row counts differ (%d vs %d)",
			len(styledRows), len(plainRows))}
	}

	var probs []string
	if cs.overlay {
		// (i) The dimming stripped every colour, backgrounds included:
		// not one `48;2;` survives anywhere in the frame — not even on
		// the `▌` rows the undimmed frames tint.
		if n := strings.Count(styled, backgroundSGR); n > 0 {
			probs = append(probs, fmt.Sprintf("the dimmed frame under the overlay carries %d %s background sequence(s)", n, backgroundSGR))
		}
	} else {
		// The background-discipline rule: no background SGR on any row
		// without a `▌` cursor gutter.
		badBackgrounds := 0
		for i := range styledRows {
			if !strings.Contains(plainRows[i], cursorGutter) && strings.Contains(styledRows[i], backgroundSGR) {
				if badBackgrounds == 0 {
					probs = append(probs, fmt.Sprintf("row %d carries a background colour but shows no %s cursor gutter",
						i+1, cursorGutter))
				}
				badBackgrounds++
			}
		}
		if badBackgrounds > 1 {
			probs = append(probs, fmt.Sprintf("%d more rows carry a background colour without a cursor gutter",
				badBackgrounds-1))
		}
	}

	// (ii) The focused accent, on the Keys box under the overlay and on
	// the focused panel everywhere else.
	if hasAnyRune(plainRows, borderRunes) && !cellCarries(styledRows, borderRunes, cs.accentFg) {
		probs = append(probs, fmt.Sprintf("no panel border rune carries the focused accent %s", cs.accentFg))
	}
	// Rule (g), full-width cursor rows (contract §9 note 6, W5 F1/C35):
	// the cursor-colour check for non-overlay frames. It subsumes the
	// "some `▌` cell carries the background" check it replaces — the
	// required span starts at the gutter itself, and a gutter whose panel
	// cannot be located is its own problem, never a silent pass.
	if !cs.overlay {
		probs = append(probs, cursorWidthProblems(styledRows, plainRows, cs)...)
	}
	return probs
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

// TestColourRulesOnOverlayFrame proves the amended overlay rules
// (contract §9 note 6, C30/D-3S) on synthetic frames, no vault: the
// dimmed frame under the shell's `?` overlay carries no background
// colour at all and the Keys box's border carries the focused accent,
// while a `▌` behind the overlay keeps no cursor background. Each
// non-passing case also checks the problem names the rule that broke, so
// a red run cannot be a different rule firing by accident.
func TestColourRulesOnOverlayFrame(t *testing.T) {
	t.Run("dimmed_background_passes", func(t *testing.T) {
		for _, cs := range []colourSet{darkColours, lightColours} {
			cs.overlay = true
			frame := overlayFrame(cs.accentFg)
			if problems := colourProblems(renderStyled(frame), renderPlain(frame), cs); len(problems) > 0 {
				t.Errorf("%s: a faint frame with a %s gutter and an accent Keys border broke %d rule(s): %v",
					cs.polarity, cursorGutter, len(problems), problems)
			}
		}
	})

	t.Run("background_under_overlay_fails", func(t *testing.T) {
		// `any 48;2;` fails — on a dimmed content row and on the `▌` row
		// itself, which outside overlay mode is the one row allowed a
		// background.
		for _, cs := range []colourSet{darkColours, lightColours} {
			cs.overlay = true
			for _, place := range []struct {
				row  int
				what string
			}{
				{2, "a dimmed row"},
				{0, "the gutter row"},
			} {
				frame := withBackground(overlayFrame(cs.accentFg), place.row, cs.cursorBg)
				problems := colourProblems(renderStyled(frame), renderPlain(frame), cs)
				if !colourRuleFired(problems, backgroundSGR) {
					t.Errorf("%s: a background colour on %s broke no overlay rule (got %v)",
						cs.polarity, place.what, problems)
				}
			}
		}
	})

	t.Run("keys_border_without_accent_fails", func(t *testing.T) {
		for _, cs := range []colourSet{darkColours, lightColours} {
			cs.overlay = true
			frame := overlayFrame("") // the border rendered faint, not accent
			problems := colourProblems(renderStyled(frame), renderPlain(frame), cs)
			if !colourRuleFired(problems, cs.accentFg) {
				t.Errorf("%s: a Keys border without the focused accent broke no overlay rule (got %v)",
					cs.polarity, problems)
			}
		}
	})

	t.Run("non_overlay_frame_keeps_cursor_rule", func(t *testing.T) {
		// The same shape, checked with the non-overlay rules: the cursor
		// check there is rule (g) (contract §9 note 6, W5 F1/C35), and a
		// `▌` with no panel around it — no `╭` above its border column —
		// fails it instead of passing silently.
		frame := overlayFrame(darkAccentFg)
		problems := colourProblems(renderStyled(frame), renderPlain(frame), darkColours)
		if !colourRuleFired(problems, "rule (g)") {
			t.Errorf("a %s without a panel around it broke no rule outside overlay mode (got %v)",
				cursorGutter, problems)
		}
	})
}

// overlayFrame builds a small synthetic `?` overlay frame: dimmed
// background rows — the first carrying a `▌` cursor gutter, faint, with
// no background — and a Keys box top border carrying accentFg. An empty
// accentFg renders the border faint instead, as the
// keys_border_without_accent_fails case needs.
func overlayFrame(accentFg string) [][]seg {
	border := []seg{{sgr: "38;2;" + accentFg, cells: "╭ Keys ─────────╮"}}
	if accentFg == "" {
		border = []seg{{sgr: "2", cells: "╭ Keys ─────────╮"}}
	}
	return [][]seg{
		{{sgr: "2", cells: "▌"}, {sgr: "2", cells: " y  accept hunk"}},
		border,
		{{sgr: "2", cells: "│"}, {cells: "esc to close"}, {sgr: "2", cells: "│"}},
	}
}

// seg is one run of cells sharing one SGR style.
type seg struct {
	sgr   string // SGR parameters; "" is unstyled
	cells string
}

// renderStyled renders rows to their styled text, `ESC [ params m` …
// `ESC [ 0 m` around each segment, the shape lipgloss emits and scanCells
// reads.
func renderStyled(rows [][]seg) string {
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		for _, s := range row {
			if s.sgr != "" {
				b.WriteString("\x1b[" + s.sgr + "m")
			}
			b.WriteString(s.cells)
			if s.sgr != "" {
				b.WriteString("\x1b[0m")
			}
		}
	}
	return b.String()
}

// renderPlain returns rows' text with every SGR sequence stripped: the
// same row count, the same cells.
func renderPlain(rows [][]seg) string {
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		for _, s := range row {
			b.WriteString(s.cells)
		}
	}
	return b.String()
}

// withBackground returns frame with row carrying one extra cell styled
// bg — the single `48;2;` sequence the background_under_overlay_fails
// case adds. overlayFrame builds fresh frames, so editing in place is
// safe.
func withBackground(frame [][]seg, row int, bg string) [][]seg {
	frame[row] = append(frame[row], seg{sgr: bg, cells: " "})
	return frame
}

// colourRuleFired reports whether problems contains a violation naming
// token.
func colourRuleFired(problems []string, token string) bool {
	for _, p := range problems {
		if strings.Contains(p, token) {
			return true
		}
	}
	return false
}

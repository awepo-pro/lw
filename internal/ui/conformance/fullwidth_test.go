package conformance

import (
	"fmt"
	"strings"
	"testing"
)

// gutterCell is cursorGutter as one cell: every rune the frames draw is a
// single cell wide (masks_test.go's cellAt), so a rune index is a cell
// index and plain and styled cells line up through scanCells.
var gutterCell = []rune(cursorGutter)[0]

// cursorWidthProblems runs rule (g), the full-width cursor-row check
// (contract §9 note 6, W5 F1/C35), over one render and returns one problem
// per offending cursor row: for every `▌` on a plain row it locates the
// panel around the gutter and reports the first inner cell the polarity's
// cursor background does not reach, naming the 1-based row and the 1-based
// first untinted column. A gutter whose panel cannot be located is itself
// a problem, so a `▌` outside a panel can never slip past the rule.
func cursorWidthProblems(styledRows, plainRows []string, cs colourSet) []string {
	if len(styledRows) != len(plainRows) {
		return nil // colourProblems already reported the differing shape
	}
	var probs []string
	for r := range plainRows {
		// Cells, not bytes: `range` over a string walks bytes, and a
		// multi-byte rune would put the gutter in the wrong column.
		for c, ch := range []rune(plainRows[r]) {
			if ch != gutterCell {
				continue
			}
			if problem, ok := cursorRowProblem(styledRows, plainRows, r, c, cs); ok {
				probs = append(probs, problem)
			}
		}
	}
	return probs
}

// cursorRowProblem checks rule (g) for one `▌` at plain cell (r, c). The
// panel's left border is at (r, c-1); walking up that column finds the top
// border's `╭` at row r0, and walking right along row r0 finds the panel's
// `╮`. The rule's span runs to the cell before the right border — one past
// the `╮` in the contract's arithmetic, whose border cell c2-1 is exempt —
// so the gutter, every content cell and the trailing blank must carry the
// cursor background. Returns the problem and true when the row breaks the
// rule, ("", false) when it is fully tinted.
func cursorRowProblem(styledRows, plainRows []string, r, c int, cs colourSet) (string, bool) {
	borderCol := c - 1
	top := -1
	for i := r - 1; i >= 0; i-- {
		if cellAt(plainRows[i], borderCol) == '╭' {
			top = i
			break
		}
	}
	if top < 0 {
		return fmt.Sprintf("rule (g): the %s at row %d, column %d has no panel top border (`╭`) above column %d",
			cursorGutter, r+1, c+1, borderCol+1), true
	}
	corner := runeIndex(plainRows[top], "╮", borderCol)
	if corner < 0 {
		return fmt.Sprintf("rule (g): the %s at row %d, column %d has no panel corner (`╮`) on border row %d",
			cursorGutter, r+1, c+1, top+1), true
	}
	c2 := corner + 1 // the contract's c2: one past the `╮`, so the border cell is c2-1
	cells := scanCells(styledRows[r])
	for col := c; col <= c2-2; col++ {
		if col >= len(cells) || !strings.Contains(cells[col].sgr, cs.cursorBg) {
			return fmt.Sprintf("rule (g): cursor row %d is not tinted to the panel's right border: column %d carries no cursor background %s",
				r+1, col+1, cs.cursorBg), true
		}
	}
	return "", false
}

// Rule (g) — full-width cursor rows (contract §9 note 6, W5 F1/C35): on
// every non-overlay frame, for each `▌` at plain cell (r, c), the panel
// around the gutter is located — left border at (r, c-1), the `╭` walking
// up that column, the `╮` walking right along the top border row — and
// every styled cell from the `▌` to the cell before the right border must
// carry the polarity's cursor background; the border cell itself is not
// required. The shipped bug (ac1c04d) ended the tint after the first
// styled run and tinted only a lone blank at w-2, which the check it
// replaces — "some `▌` cell carries the background" — passed on the broken
// render. These frames are synthetic, so the subtests run without
// LW_MOCKUP_VAULT.
//
// TestColourRuleFullWidthCursor pins the rule on the byte shapes that
// matter; TestMockupConformance applies it to the 22 non-overlay frozen
// grids, in both polarities. The overlay frames (`keys-*`) are exempt:
// their dimming strips every background (C30/D-3S), so the overlay rules
// are the only ones they answer to.

func TestColourRuleFullWidthCursor(t *testing.T) {
	t.Run("shipped_bug_shape_fails", func(t *testing.T) {
		for _, cs := range []colourSet{darkColours, lightColours} {
			frame := shippedBugFrame(cs)
			problems := colourProblems(rawStyled(frame), renderPlain(frame), cs)
			if len(problems) == 0 {
				t.Errorf("%s: the ac1c04d cursor row — a tint on `│▌●` only, then \\x1b[39m\\x1b[49m, untinted content and padding, and a lone tinted blank at w-2 — broke no rule", cs.polarity)
				continue
			}
			if !colourRuleFired(problems, "rule (g)") {
				t.Errorf("%s: the ac1c04d cursor row broke no rule-(g) check (got %v)", cs.polarity, problems)
			}
			// The failure must be the tint check itself, and it must be
			// locatable: the cursor row's 1-based row and the first
			// untinted column — cell 3, the first content cell after the
			// tinted `│▌●`.
			if !colourRuleFired(problems, "is not tinted to the panel's right border") ||
				!colourRuleFired(problems, "row 2") || !colourRuleFired(problems, "column 4") {
				t.Errorf("%s: the rule-(g) problem must name the untinted span, row 2 and the first untinted column 4 (got %v)",
					cs.polarity, problems)
			}
		}
	})

	t.Run("full_tint_passes", func(t *testing.T) {
		for _, cs := range []colourSet{darkColours, lightColours} {
			// The shape T24's per-cell tint draws: gutter, content,
			// padding and trailing blank in one tinted run — here with
			// the right border tinted as well, which the rule allows.
			frame := tintedCursorFrame(cs, true)
			if problems := colourProblems(rawStyled(frame), renderPlain(frame), cs); len(problems) > 0 {
				t.Errorf("%s: a cursor row tinted from the gutter through the trailing blank broke %d rule(s): %v",
					cs.polarity, len(problems), problems)
			}
		}
	})

	t.Run("border_cell_not_required", func(t *testing.T) {
		for _, cs := range []colourSet{darkColours, lightColours} {
			// The same full tint with the right border cell left to the
			// border style: an untinted cell at c2-1 is not a failure.
			frame := tintedCursorFrame(cs, false)
			if problems := colourProblems(rawStyled(frame), renderPlain(frame), cs); len(problems) > 0 {
				t.Errorf("%s: an untinted right border cell broke %d rule(s): %v",
					cs.polarity, len(problems), problems)
			}
		}
	})

	t.Run("overlay_frames_exempt", func(t *testing.T) {
		for _, cs := range []colourSet{darkColours, lightColours} {
			// A panel-shaped keys-* frame as the overlay draws it —
			// everything faint, no background anywhere, the Keys border
			// carrying the focused accent. Its untinted `▌` row would
			// fail rule (g), so under the overlay rules it must be clean
			// (C30/D-3S): the overlay rules are the only ones it answers
			// to, and rule (g) never runs on it.
			frame := keysPanelFrame(cs)
			overlay := cs
			overlay.overlay = true
			if problems := colourProblems(rawStyled(frame), renderPlain(frame), overlay); len(problems) > 0 {
				t.Errorf("%s: a dimmed keys frame with an untinted cursor row broke %d overlay rule(s): %v",
					cs.polarity, len(problems), problems)
			}
			// The exemption must be what saves the frame: under the
			// non-overlay rules the same frame fails rule (g).
			if problems := colourProblems(rawStyled(frame), renderPlain(frame), cs); !colourRuleFired(problems, "rule (g)") {
				t.Errorf("%s: the untinted keys frame broke no rule outside overlay mode — the exemption proves nothing (got %v)",
					cs.polarity, problems)
			}
		}
	})
}

// shippedBugFrame returns the synthetic panel whose cursor row carries the
// ac1c04d bytes (G4 finding F1): the tint covers `│▌●` only, the styled
// run's own `\x1b[39m\x1b[49m` resets end it, the content and padding run
// untinted, a lone blank at w-2 is tinted by the separately rendered
// trailing cell, and the right border is not tinted.
func shippedBugFrame(cs colourSet) [][]seg {
	return [][]seg{
		{{sgr: cs.accentFg, cells: "╭ Ops " + strings.Repeat("─", 9) + "╮"}},
		{
			{sgr: cs.accentFg + ";" + cs.cursorBg, cells: "│▌●"},
			{sgr: "39"},                       // the shipped run's foreground reset
			{sgr: "49", cells: " patch op3 "}, // ...and its background reset: content and padding untinted
			{sgr: cs.cursorBg, cells: " "},    // the separately tinted blank at w-2
			{sgr: "49", cells: "│"},
		},
		{{cells: "│ a plain row  │"}},
		{{cells: "╰" + strings.Repeat("─", 14) + "╯"}},
	}
}

// tintedCursorFrame returns the same panel with the cursor row tinted in
// one run from the gutter through the trailing blank — the shape T24's
// per-cell tint draws (contract §5 frame note 9). borderTinted styles the
// right border cell too for full_tint_passes and leaves it to the border
// style for border_cell_not_required.
func tintedCursorFrame(cs colourSet, borderTinted bool) [][]seg {
	border := seg{sgr: "49", cells: "│"}
	if borderTinted {
		border = seg{sgr: cs.cursorBg, cells: "│"}
	}
	return [][]seg{
		{{sgr: cs.accentFg, cells: "╭ Ops " + strings.Repeat("─", 9) + "╮"}},
		{
			{sgr: cs.accentFg + ";" + cs.cursorBg, cells: "│▌● patch op3  "},
			border,
		},
		{{cells: "│ a plain row  │"}},
		{{cells: "╰" + strings.Repeat("─", 14) + "╯"}},
	}
}

// keysPanelFrame returns a panel-shaped keys-* frame as the overlay draws
// it: every cell faint, no background anywhere, the Keys box's border
// carrying the focused accent. Under the overlay rules it is clean; under
// the non-overlay rules its untinted `▌` row fails rule (g) — which is
// exactly the exemption overlay_frames_exempt proves.
func keysPanelFrame(cs colourSet) [][]seg {
	faint := func(cells string) seg { return seg{sgr: "2", cells: cells} }
	return [][]seg{
		{{sgr: cs.accentFg, cells: "╭ Keys " + strings.Repeat("─", 8) + "╮"}},
		{faint("│▌ esc to close"), faint("│")},
		{faint("│ y  accept    "), faint("│")},
		{faint("╰" + strings.Repeat("─", 14) + "╯")},
	}
}

// rawStyled renders rows with each styled span emitting exactly
// `ESC [ params m` before its cells and no reset after. The shipped bytes
// end their tint with an explicit `\x1b[39m\x1b[49m` mid-line, which
// renderStyled's per-span `\x1b[0m` cannot express; scanCells reads both
// shapes the same way.
func rawStyled(rows [][]seg) string {
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
		}
	}
	return b.String()
}

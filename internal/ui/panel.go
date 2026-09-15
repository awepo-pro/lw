// panel.go implements contract §5's Panel — the one way any screen draws a
// bordered box (00-conventions.md §4.7: "never hand-draw a border") — ported
// from mockgen.box / mockgen.foot / mockgen.draw_lines.
package ui

import (
	"fmt"

	lipgloss "charm.land/lipgloss/v2"
)

// PanelSpec describes one bordered panel (mockgen.box / foot / draw_lines).
type PanelSpec struct {
	Title     string      // set into the top border: "╭ Title ───"; clipped to w-6
	Note      string      // right-aligned on the top border: "─ p preview ╮"; omitted if it doesn't fit
	NoteLevel StatusLevel // Note colour (ORCH-12/D-3T): zero value StatusInfo = Muted, unchanged; StatusGood/StatusWarn/StatusBad draw it in Good/Warn/Bad
	FootNote  string      // right-aligned on the bottom border, muted: "─ 3 of 4 ╯"; omitted if it doesn't fit
	Focused   bool        // border + title in Accent; otherwise border Border, title Bold

	Lines     []string // pre-styled content lines, each at most w-4 cells (clipped otherwise)
	CursorRow int      // index into Lines drawn with the ▌ gutter + CursorBg tint; -1 for none
	Overflow  bool     // when len(Lines) > h-2 and FootNote == "", FootNote becomes "↓ N more"
}

// Panel renders spec as exactly h lines of exactly w cells (w >= 6, h >= 2).
// Content starts at column 2; column 1 is the cursor gutter.
func Panel(t Theme, spec PanelSpec, w, h int) []string {
	if w < 6 {
		w = 6
	}
	if h < 2 {
		h = 2
	}

	border, title := t.Border, t.Bold
	if spec.Focused {
		border, title = t.Accent, t.Accent.Bold(true)
	}

	rows := make([]string, h)
	rows[0] = panelTopBorder(t, w, spec.Title, spec.Note, noteStyle(t, spec.NoteLevel), border, title)

	innerH := h - 2
	footNote := spec.FootNote
	if spec.Overflow && footNote == "" && len(spec.Lines) > innerH {
		footNote = fmt.Sprintf("↓ %d more", len(spec.Lines)-innerH)
	}

	for i := 0; i < innerH; i++ {
		var content string
		if i < len(spec.Lines) {
			content = spec.Lines[i]
		}
		rows[1+i] = panelContentRow(t, w, content, i == spec.CursorRow, border)
	}

	rows[h-1] = panelBottomBorder(t, w, footNote, border)

	return rows
}

// noteStyle is the style spec's Note is drawn in: Muted at the zero value
// (StatusInfo — every pre-ORCH-12 panel's rendering, unchanged), or the
// level's own colour when the spec asks for one.
func noteStyle(t Theme, level StatusLevel) lipgloss.Style {
	switch level {
	case StatusGood:
		return t.Good
	case StatusWarn:
		return t.Warn
	case StatusBad:
		return t.Bad
	default:
		return t.Muted
	}
}

// panelTopBorder draws the rounded top edge, with the title set into it
// (mockgen.box) and an optional right-aligned note.
func panelTopBorder(t Theme, w int, titleText, note string, noteStyle, border, title lipgloss.Style) string {
	r := newRow(w)
	r.put(0, "╭", border)
	for i := 1; i < w-1; i++ {
		r.put(i, "─", border)
	}
	r.put(w-1, "╮", border)

	used := 0
	if titleText != "" {
		tt := Clip(titleText, w-6)
		r.put(1, " ", lipgloss.Style{})
		r.put(2, tt, title)
		r.put(2+cellLen(tt), " ", lipgloss.Style{})
		used = cellLen(tt) + 3
	}
	if note != "" && used+cellLen(note)+5 <= w {
		r.put(w-3-cellLen(note), " "+note+" ", noteStyle)
	}
	return r.render()
}

// panelBottomBorder draws the rounded bottom edge, with an optional
// right-aligned foot note (mockgen.foot).
func panelBottomBorder(t Theme, w int, footNote string, border lipgloss.Style) string {
	r := newRow(w)
	r.put(0, "╰", border)
	for i := 1; i < w-1; i++ {
		r.put(i, "─", border)
	}
	r.put(w-1, "╯", border)
	if footNote != "" && cellLen(footNote)+6 <= w {
		r.put(w-3-cellLen(footNote), " "+footNote+" ", t.Muted)
	}
	return r.render()
}

// panelContentRow draws one interior row: the two side borders, a blank or
// "▌" gutter at column 1, the content padded to exactly w-4 cells at column
// 2, and — on the cursor row — CursorBg tinted across the whole inner width
// (mockgen.draw_lines / tint).
func panelContentRow(t Theme, w int, content string, cursor bool, border lipgloss.Style) string {
	inner := w - 4
	if inner < 0 {
		inner = 0
	}
	content = Pad(content, inner)

	left := border.Render("│")
	right := border.Render("│")

	if !cursor {
		return left + " " + content + " " + right
	}

	gutter := t.Accent.Background(t.CursorBg).Render("▌")
	body := lipgloss.NewStyle().Background(t.CursorBg).Render(content)
	blank := lipgloss.NewStyle().Background(t.CursorBg).Render(" ")
	return left + gutter + body + blank + right
}

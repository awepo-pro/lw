// notice.go implements the too-small notice (contract §5 frame note 3,
// mockgen.too_small): below MinWidth×MinHeight the shell renders only this,
// centred, and refuses every key but quit.
package ui

import (
	"fmt"
	"strings"
)

// tooSmallLines is the notice's four content lines, in mockgen.too_small's
// order: the title, a blank separator, the actual-vs-minimum size line (the
// undersized dimension in Bad), and the quit hint.
func tooSmallLines(t Theme, w, h int) []string {
	wStyle, hStyle := t.Fg, t.Fg
	if w < MinWidth {
		wStyle = t.Bad
	}
	if h < MinHeight {
		hStyle = t.Bad
	}

	sizeLine := t.Muted.Render("lw needs at least ") +
		t.Fg.Render(fmt.Sprintf("%d×%d", MinWidth, MinHeight)) +
		t.Muted.Render(". This one is ") +
		wStyle.Render(fmt.Sprintf("%d", w)) +
		t.Muted.Render("×") +
		hStyle.Render(fmt.Sprintf("%d", h)) +
		t.Muted.Render(".")

	quitLine := t.Muted.Render("Make the window larger, or press ") +
		t.Bold.Render("q") +
		t.Muted.Render(" to quit.")

	return []string{
		t.Bold.Render("Terminal too small"),
		"",
		sizeLine,
		quitLine,
	}
}

// tooSmallView centres tooSmallLines within exactly h rows of w cells.
func tooSmallView(t Theme, w, h int) []string {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	content := tooSmallLines(t, w, h)

	out := make([]string, h)
	for i := range out {
		out[i] = strings.Repeat(" ", w)
	}

	y0 := (h - len(content)) / 2
	if y0 < 0 {
		y0 = 0
	}
	for i, line := range content {
		y := y0 + i
		if y < 0 || y >= h {
			continue
		}
		if cellLen(line) > w {
			line = Clip(line, w)
		}
		lw := cellLen(line)
		x := (w - lw) / 2
		if x < 0 {
			x = 0
		}
		right := w - x - lw
		if right < 0 {
			right = 0
		}
		out[y] = strings.Repeat(" ", x) + line + strings.Repeat(" ", right)
	}
	return out
}

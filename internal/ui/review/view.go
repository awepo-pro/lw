// view.go lays the review screen out and composes its panels (s2-screens
// .md T06; mockgen.review): below 100 columns the Ops panel stacks over
// the Detail panel, otherwise they sit side by side with the Ops width at
// clamp((33*w+50)/100, 28, 56) and — once the pane is at least 30 rows —
// a Changeset panel of height 9 under the Ops panel. The Detail panel is
// focused; the frame's rows 0 and h-1 belong to the shell.
package review

import (
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// The frozen layout's fixed values (mockgen.review).
const (
	// sideBySideMinW is the width from which Ops and Detail sit side by
	// side instead of stacked.
	sideBySideMinW = 100

	// changesetPanelH is the Changeset panel's height, shown from pane
	// height changesetMinH.
	changesetPanelH = 9
	changesetMinH   = 30

	// stackedOpsMaxH caps the stacked Ops panel's height
	// (min(len(ops)+2, 6)).
	stackedOpsMaxH = 6
)

// panelRect is one panel's rectangle in pane-local cells — the units
// ui.Panel draws in and ui.WheelMsg reports (contract §5 frame note 7).
type panelRect struct {
	x, y, w, h int
}

// contains reports whether the pane-local cell (x, y) lies inside r.
func (r panelRect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// reviewLayout is the screen's panel geometry at one pane size: the three
// panels' rectangles, and whether the Changeset panel exists at this
// height at all.
type reviewLayout struct {
	ops, changeset, detail panelRect
	hasChangeset           bool
}

// layout is the review screen's frozen split, in one place so View's
// drawing and the wheel's hit-testing (scroll.go) cannot drift apart.
// Below 100 columns the Ops panel of min(len(ops)+2, 6) rows stacks over
// Detail (only Detail when the pane is too short for both); otherwise Ops
// takes width clamp((33*w+50)/100, 28, 56) beside a full-height Detail,
// with the Changeset panel under Ops from 30 rows.
func (m *Model) layout(w, h int) reviewLayout {
	if w < sideBySideMinW {
		opsH := len(m.ops) + 2
		if opsH > stackedOpsMaxH {
			opsH = stackedOpsMaxH
		}
		if opsH < 2 {
			opsH = 2
		}
		if h-opsH < 2 {
			return reviewLayout{detail: panelRect{0, 0, w, h}}
		}
		return reviewLayout{
			ops:    panelRect{0, 0, w, opsH},
			detail: panelRect{0, opsH, w, h - opsH},
		}
	}

	opsW := (33*w + 50) / 100
	if opsW < 28 {
		opsW = 28
	}
	if opsW > 56 {
		opsW = 56
	}
	if opsW > w-6 {
		opsW = w - 6
	}
	if h < changesetMinH {
		return reviewLayout{
			ops:    panelRect{0, 0, opsW, h},
			detail: panelRect{opsW, 0, w - opsW, h},
		}
	}
	return reviewLayout{
		ops:          panelRect{0, 0, opsW, h - changesetPanelH},
		changeset:    panelRect{0, h - changesetPanelH, opsW, changesetPanelH},
		detail:       panelRect{opsW, 0, w - opsW, h},
		hasChangeset: true,
	}
}

// View renders the review screen at exactly w by h cells (backbone §12):
// h lines, each exactly w cells, every border drawn by ui.Panel.
func (m *Model) View(w, h int) string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if w < 6 || h < 2 {
		// Below any panel's minimum: blank padding keeps the exactly-w-by-h
		// invariant without asking ui.Panel to clamp a width it would pad.
		return strings.TrimSuffix(strings.Repeat(strings.Repeat(" ", w)+"\n", h), "\n")
	}
	return strings.Join(m.body(w, h), "\n")
}

// body lays the screen out from the shared layout. An empty model flows
// through the same shape — m.ops is nil, so the Ops panel is empty and
// the Detail panel carries the one line that says why.
func (m *Model) body(w, h int) []string {
	l := m.layout(w, h)
	rows := make([]string, h)
	if l.ops.w > 0 && l.ops.h > 0 {
		ops := m.opsPanel(l.ops.w, l.ops.h)
		for i := 0; i < l.ops.h; i++ {
			rows[l.ops.y+i] = ops[i]
		}
	}
	if l.hasChangeset {
		changeset := m.changesetPanel(l.changeset.w, l.changeset.h)
		for i := 0; i < l.changeset.h; i++ {
			rows[l.changeset.y+i] = changeset[i]
		}
	}

	detail := m.detailPanel(l.detail.w, l.detail.h)
	for i := 0; i < l.detail.h; i++ {
		rows[l.detail.y+i] += detail[i]
	}
	return rows
}

// detailPanel renders the focused Detail panel — Diff or Preview — with
// the cursor's hunk window marked through PanelSpec.CursorRow + CursorSpan
// (contract §5 frame note 9, W5 F1/C35: ui.Panel tints every cursor-row
// cell itself, so this screen never splices rows) and the scroll offset
// applied (contract §5 frame note 10, W5 F2/C36): the offset is clamped
// here again, lines[off:] is what the panel draws, and with nothing below
// the fold the bottom border counts the lines hidden above instead.
func (m *Model) detailPanel(w, h int) []string {
	title, note, lines := m.detailContent(w - 4)
	inner := h - 2
	maxOff := max(0, len(lines)-inner)
	off := clampScroll(m.off, maxOff)
	if off > 0 {
		lines = lines[off:]
	}

	strs := make([]string, len(lines))
	for i, l := range lines {
		strs[i] = l.text
	}

	spec := ui.PanelSpec{
		Title:     title,
		Note:      note,
		Focused:   true,
		Lines:     strs,
		Overflow:  true,
		CursorRow: -1, // no cursor window on screen
	}
	if first, last, ok := cursorWindow(lines); ok {
		spec.CursorRow = first
		spec.CursorSpan = last - first + 1
	}
	if off > 0 && off >= maxOff {
		// Nothing below: Overflow's ↓ note would count zero, so the
		// bottom border says how much is hidden above (s2-screens.md
		// T06 Scroll).
		spec.FootNote = fmt.Sprintf("↑ %d above", off)
	}
	return ui.Panel(m.theme, spec, w, h)
}

// currentOp returns the op holding the current cursor stop, and false
// when the cursor addresses nothing (an empty diff, no stops).
func (m *Model) currentOp() (stage.Op, bool) {
	id, _, _ := resolveCursor(m.diff, m.stops, m.cursor)
	return findOp(m.ops, id)
}

// currentOpIndex is the listed index of the op the cursor addresses, or 0
// when it addresses nothing — the Diff panel starts there.
func (m *Model) currentOpIndex() int {
	id, _, _ := resolveCursor(m.diff, m.stops, m.cursor)
	if id == "" {
		return 0
	}
	for i, op := range m.ops {
		if op.ID == id {
			return i
		}
	}
	return 0
}

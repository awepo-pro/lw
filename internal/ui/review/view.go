// view.go lays the review screen out and composes its panels (s2-screens
// .md T06; mockgen.review): below 100 columns the Ops panel stacks over
// the Detail panel, otherwise they sit side by side with the Ops width at
// clamp((33*w+50)/100, 28, 56) and — once the pane is at least 30 rows —
// a Changeset panel of height 9 under the Ops panel. The Detail panel is
// focused; the frame's rows 0 and h-1 belong to the shell.
package review

import (
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

// body lays the screen out. An empty model flows through the same shape —
// m.ops is nil, so the Ops panel is empty and the Detail panel carries
// the one line that says why.
func (m *Model) body(w, h int) []string {
	if w < sideBySideMinW {
		return m.stacked(w, h)
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

	opsH := h
	if h >= changesetMinH {
		opsH = h - changesetPanelH
	}
	return m.compose(w, h, opsW, w-opsW, opsH, h)
}

// stacked draws the narrow layout: an Ops panel of min(len(ops)+2, 6)
// rows over the Detail panel for the rest, both full-width — no
// Changeset panel, which only exists in the side-by-side column. When the
// pane is too short for both, only the Detail panel is drawn.
func (m *Model) stacked(w, h int) []string {
	opsH := len(m.ops) + 2
	if opsH > stackedOpsMaxH {
		opsH = stackedOpsMaxH
	}
	if opsH < 2 {
		opsH = 2
	}
	if h-opsH < 2 {
		return m.detailPanel(w, h)
	}

	rows := make([]string, h)
	ops := m.opsPanel(w, opsH)
	for i := 0; i < opsH; i++ {
		rows[i] = ops[i]
	}
	detail := m.detailPanel(w, h-opsH)
	for i := 0; i < h-opsH; i++ {
		rows[opsH+i] = detail[i]
	}
	return rows
}

// compose draws the wide layout: the left column — Ops at opsW for opsH
// rows, plus the Changeset panel of the remaining rows when opsH < h —
// and the Detail panel at detailW for the full height.
func (m *Model) compose(w, h, opsW, detailW, opsH, detailH int) []string {
	rows := make([]string, h)
	ops := m.opsPanel(opsW, opsH)
	for i := 0; i < opsH; i++ {
		rows[i] = ops[i]
	}
	if opsH < h {
		changeset := m.changesetPanel(opsW, h-opsH)
		for i := 0; i < h-opsH; i++ {
			rows[opsH+i] = changeset[i]
		}
	}

	detail := m.detailPanel(detailW, detailH)
	for i := 0; i < detailH; i++ {
		rows[i] += detail[i]
	}
	return rows
}

// detailPanel renders the focused Detail panel — Diff or Preview — with
// every row of the cursor's hunk window marked (panelcursor.go).
func (m *Model) detailPanel(w, h int) []string {
	cw := w - 4
	title, note := "Diff", "p preview"
	lines := m.diffLines(cw)
	if !m.hasChangeset {
		msg := "(nothing staged)"
		if m.loadErr != nil {
			msg = "review: " + m.loadErr.Error()
		}
		lines = []panelLine{{text: m.theme.Faint.Render(msg)}}
	} else if m.preview {
		title, note, lines = "Preview", "p diff", m.previewLines(cw)
	}

	strs := make([]string, len(lines))
	var marked []int
	for i, l := range lines {
		strs[i] = l.text
		if l.cursor {
			marked = append(marked, i)
		}
	}

	rows := ui.Panel(m.theme, ui.PanelSpec{
		Title:     title,
		Note:      note,
		Focused:   true,
		Lines:     strs,
		Overflow:  true,
		CursorRow: -1, // the cursor window marks its own rows
	}, w, h)
	return applyCursorRows(rows, m.theme, marked)
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

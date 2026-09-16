// view.go holds App's rendering side — View and render — split from
// app.go so it stays under conventions §2's ~400-line guideline, the same
// file split route.go came from. Every method here still binds to *App and
// is called from Update's switch and the tea runtime unchanged, because Go
// methods bind to the type, not the file they're declared in.
package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// View renders the shell (backbone §12, C-79: v2's tea.Model returns
// tea.View, not string). AltScreen is a per-frame field in v2 — there is no
// tea.WithAltScreen program option (C-82). Cell-motion mouse mode is set
// every frame so the wheel reaches the active pane (contract §5 frame
// note 7, W5 F2/D-3W) — the trade-off, documented in D-3W, is that text
// selection becomes shift+drag.
func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// render composes one frame at the shell's current width and height:
// below D11's minimum, only the too-small notice (contract §5 frame note
// 3); otherwise the header, the active pane's body and the footer, with the
// `?` overlay composited on top when it is open (contract §5 frame note 4).
// Nothing here assumes 80x24 beyond the minimum itself.
func (a *App) render() string {
	if a.tooSmall() {
		return strings.Join(tooSmallView(a.deps.Theme, a.width, a.height), "\n")
	}

	bodyH := a.height - 2

	s := a.order[a.cur]
	var content string
	if p, ok := a.panes[s]; ok && p != nil {
		content = p.View(a.width, bodyH)
	} else {
		content = fmt.Sprintf("(%s screen not loaded yet)", screenNames[s])
	}

	lines := make([]string, 0, a.height)
	lines = append(lines, headerLine(a.deps.Theme, a.width, a.vaultName, tabLabels(), screenTabName[s],
		a.pages, a.raw, a.lintErrors,
		headerStage{ID: a.stageID, Ops: a.stageOps, Checks: a.stageChecks, Has: a.hasStage}))
	lines = append(lines, fitPaneLines(content, a.width, bodyH)...)
	lines = append(lines, footerContent(a.deps.Theme, a.deps.Keys, a.activePane(), a.width))

	frame := strings.Join(lines, "\n")
	if !a.overlayOpen {
		return frame
	}

	box, bw, bh, x, y := overlayBox(a.deps.Theme, a.deps.Keys, a.activePane(), a.width, a.height)
	plain := stripFrame(frame)
	return strings.Join(compositeOverlay(a.deps.Theme, plain, box, x, y, bw, bh), "\n")
}

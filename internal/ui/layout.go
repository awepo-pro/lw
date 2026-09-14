// layout.go is the shell's pure string composition: fitting text to an
// exact width, and laying out the title bar, sidebar, active pane and
// footer into one frame (/.dev-notes/PLAN-v1.md §9). Nothing here touches tea.Msg or
// mutates App state — app.go is the only caller, and it is the only place
// that decides what text these functions receive.
package ui

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// sidebarWidth is the shell's SIDEBAR column width (/.dev-notes/PLAN-v1.md §9). It
// shrinks to fit a narrower terminal — see bodyDimensions — so nothing
// assumes 80x24 (s4-tui.md S4-T2 item 4).
const sidebarWidth = 20

// footerBarText is the shell's footer line. It advertises only keys a
// screen actually binds (C-124/TD-8): s4-tui.md S4-T2's original line named
// "[a]sk · [s]tage · [c]ommit · [l]int · [L]og · [?]" — letters nothing in
// the shell or any screen ever matched, plus a help overlay v0.1 never
// built — found live at gate G6 (docs/hotkeys.md has the same fix). Screens
// switch on tab alone; Review's own y/n/C letters are the only
// accept/drop/commit keys that exist.
const footerBarText = "[tab] next screen · [ctrl+r] ask→review · [y/n] accept/drop · [C] commit · [q] quit"

// fitLine returns s clipped or padded to exactly w display columns, so
// composing lines side by side or stacking them can never produce a frame
// wider than the caller asked for. Clipping goes through lipgloss's own
// Style.MaxWidth, which is ANSI-aware — it will not cut a styled pane's
// string mid-escape-sequence — and padding only ever appends plain spaces
// after whatever s already rendered, which is always safe to do to a
// string that may or may not carry ANSI styling. w <= 0 returns "".
func fitLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	if cur := lipgloss.Width(s); cur < w {
		s += strings.Repeat(" ", w-cur)
	}
	return s
}

// fitLines splits s on "\n" and returns exactly n lines, each fitLine'd to
// w: lines beyond n are dropped, missing ones come back blank. Pane.View
// is contracted to already produce w-wide, h-tall output, but the shell
// composes chrome around a pane it does not implement (backbone §12), so
// it enforces its own bound rather than trusting every screen to get this
// right.
func fitLines(s string, w, n int) []string {
	src := strings.Split(s, "\n")
	out := make([]string, n)
	for i := range out {
		var line string
		if i < len(src) {
			line = src[i]
		}
		out[i] = fitLine(line, w)
	}
	return out
}

// titleBarText is /.dev-notes/PLAN-v1.md §9's title line.
func titleBarText(vaultName string, pages, raw, lintErrors int) string {
	return fmt.Sprintf("%s — %d pages · %d raw · ⚠ %d lint", vaultName, pages, raw, lintErrors)
}

// stageLines renders the sidebar's STAGE panel (s4-tui.md S4-T2 item 3):
// the open changeset id and its live op count. An empty changesetID means
// no changeset is open — the sidebar says so rather than showing a blank.
func stageLines(changesetID string, ops int) []string {
	if changesetID == "" {
		return []string{"STAGE", "", "no changeset"}
	}
	return []string{"STAGE", "", changesetID, fmt.Sprintf("%d op(s)", ops)}
}

// bodyDimensions splits width into the sidebar column, the one-column
// separator (present only when there is room for one) and what is left for
// the active pane, and clamps the body's height to width/height that leave
// no room for both the title and footer bars. It is the single place that
// decides these numbers, called once by App.render to size the Pane.View
// call and again by composeFrame to lay the same numbers out — both calls
// are pure functions of (width, height), so they always agree.
func bodyDimensions(width, height int) (sideW, sepW, mainW, bodyH int) {
	if width < 0 {
		width = 0
	}
	bodyH = height - 2
	if bodyH < 0 {
		bodyH = 0
	}
	sideW = sidebarWidth
	if sideW > width {
		sideW = width
	}
	if sideW < width {
		sepW = 1
	}
	mainW = width - sideW - sepW
	if mainW < 0 {
		mainW = 0
	}
	return sideW, sepW, mainW, bodyH
}

// composeFrame lays out one full frame: a title bar, a two-column body (the
// STAGE sidebar beside the active pane's content) and a footer bar, per
// /.dev-notes/PLAN-v1.md §9. It never returns a line wider than width or more lines than
// height, regardless of what theme or the active pane produced — nothing
// may assume 80x24 (s4-tui.md S4-T2 item 4).
func composeFrame(theme Theme, width, height int, title, footer string, sidebar []string, mainContent string) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	sideW, sepW, mainW, bodyH := bodyDimensions(width, height)

	rows := make([]string, 0, height)
	rows = append(rows, theme.StatusBar.Render(fitLine(" "+title, width)))

	if bodyH > 0 {
		sideLines := fitLines(strings.Join(sidebar, "\n"), sideW, bodyH)
		mainLines := fitLines(mainContent, mainW, bodyH)

		sep := ""
		if sepW > 0 {
			sep = theme.Border.Render(fitLine("│", sepW))
		}

		for i := 0; i < bodyH; i++ {
			rows = append(rows, theme.Base.Render(sideLines[i])+sep+mainLines[i])
		}
	}

	if height >= 2 {
		rows = append(rows, theme.StatusBar.Render(fitLine(" "+footer, width)))
	}

	return strings.Join(rows, "\n")
}

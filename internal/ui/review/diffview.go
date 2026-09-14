// diffview.go renders the review screen's two panes: the op list on the
// left and the currently-selected op's diff on the right (s4-tui.md
// S4-T3 item 1). Every function here is a pure string builder over
// already-loaded stage types — no Engine call lives in this file.
package review

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// fitLine returns s clipped or padded to exactly w display columns, so
// composing two columns side by side can never produce a line wider than
// the caller asked for (backbone §12: "no line wider than w"). This
// mirrors internal/ui/layout.go's own fitLine — duplicated here because
// that helper is unexported and screens do not import the shell's
// internals (backbone §12: the shell/screen seam runs through Options and
// Deps only).
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
// w: lines beyond n are dropped, missing ones come back blank padding.
func fitLines(s string, w, n int) []string {
	if n < 0 {
		n = 0
	}
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

// splitLines splits s into its lines without a trailing empty element for
// a final "\n" — the shape fd.Old/fd.New need for line-by-line rendering
// of a stale op's two full versions.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// renderOpList renders the changeset's ops (id, kind, path, state),
// highlighting the one owning the cursor (s4-tui.md S4-T3 item 1) and a
// stale op in Theme.Warn regardless of cursor position, so staleness is
// visible without having to navigate to it.
func renderOpList(theme ui.Theme, ops []stage.Op, currentOpID string, w int) string {
	var b strings.Builder
	b.WriteString(theme.Title.Render(fitLine("OPS", w)))
	if len(ops) == 0 {
		b.WriteString("\n")
		b.WriteString(theme.Faint.Render(fitLine("(no ops)", w)))
		return b.String()
	}
	for _, op := range ops {
		row := fmt.Sprintf("%s %s %s [%s]", op.ID, op.Kind, opDisplayPath(op), op.State)
		style := theme.Base
		switch {
		case op.State == stage.StateStale:
			style = theme.Warn
		case op.ID == currentOpID && currentOpID != "":
			style = theme.Selected
		}
		b.WriteString("\n")
		b.WriteString(style.Render(fitLine(row, w)))
	}
	return b.String()
}

// renderDiffPane renders the FileDiff the cursor currently sits inside:
// its header, the owning op's Rationale and Provenance (s4-tui.md S4-T3
// item 2 — "that is what makes the review meaningful rather than a diff
// review"), and either its hunks (with the cursor's hunk marked, and any
// dropped hunk shown faint with a "dropped" marker — item 4 / item 11) or,
// for a stale op, both full versions and a rebase-or-drop prompt in amber
// (item 4, /docs/design.md §7).
func renderDiffPane(theme ui.Theme, cs *stage.Changeset, d stage.Diff, stops []cursorStop, cursor int, w int) string {
	if len(d.Files) == 0 {
		return theme.Faint.Render(fitLine("(no changes to review)", w))
	}

	fileIdx := 0
	hunkIdx := -1
	if i := clampCursor(cursor, len(stops)); i < len(stops) {
		fileIdx = stops[i].fileIdx
		hunkIdx = stops[i].hunkIdx
	}
	fd := d.Files[fileIdx]

	var op stage.Op
	if cs != nil {
		if p, ok := cs.Op(fd.OpID); ok {
			op = *p
		}
	}

	var b strings.Builder
	headerStyle := theme.Title
	if fd.Stale {
		headerStyle = theme.Warn
	}
	b.WriteString(headerStyle.Render(fitLine(fmt.Sprintf("%s  %s  %s", fd.OpID, fd.Kind, fd.Path), w)))

	if op.Rationale != "" {
		b.WriteString("\n")
		b.WriteString(theme.Muted.Render(fitLine("rationale: "+op.Rationale, w)))
	}
	if len(op.Provenance) > 0 {
		b.WriteString("\n")
		b.WriteString(theme.Muted.Render(fitLine("provenance: "+strings.Join(op.Provenance, ", "), w)))
	}

	if fd.Stale {
		b.WriteString("\n")
		b.WriteString(theme.Warn.Render(fitLine(
			"stale: the working tree changed since this op was proposed — rebase or drop it before committing", w)))
		b.WriteString("\n")
		b.WriteString(theme.Warn.Render(fitLine("--- working tree (old) ---", w)))
		for _, line := range splitLines(fd.Old) {
			b.WriteString("\n")
			b.WriteString(theme.Base.Render(fitLine(line, w)))
		}
		b.WriteString("\n")
		b.WriteString(theme.Warn.Render(fitLine("--- proposed (new) ---", w)))
		for _, line := range splitLines(fd.New) {
			b.WriteString("\n")
			b.WriteString(theme.Base.Render(fitLine(line, w)))
		}
		return b.String()
	}

	if len(fd.Hunks) == 0 {
		b.WriteString("\n")
		b.WriteString(theme.Faint.Render(fitLine("(no hunks — whole-file change)", w)))
		return b.String()
	}

	for hi, h := range fd.Hunks {
		label := fmt.Sprintf("hunk %s", h.ID)
		hstyle := theme.Base
		if hi == hunkIdx {
			label = "> " + label
			hstyle = theme.Selected
		} else {
			label = "  " + label
		}
		if h.Dropped {
			label += " (dropped)"
			hstyle = theme.Faint
		}
		b.WriteString("\n")
		b.WriteString(hstyle.Render(fitLine(label, w)))
		for _, line := range renderHunkLines(theme, h) {
			b.WriteString("\n")
			b.WriteString(fitLine(line, w))
		}
	}
	return b.String()
}

// renderHunkLines renders one Hunk's context, removed and added lines,
// already styled: context in Theme.Muted, removed in Theme.Bad prefixed
// "- ", added in Theme.Good prefixed "+ ".
//
// Hunk.Before carries context AND removed lines together, in original
// order (backbone §5.6 ComputeHunks doc); a hand-built Hunk (as review
// screen tests and cascade construction both do) may leave Before empty
// and carry only Del/Add. Either shape renders correctly here: Before's
// entries that also appear in Del are skipped (they are shown via the Del
// loop instead, so a removed line is never printed twice), and an empty
// Before simply contributes no context lines.
func renderHunkLines(theme ui.Theme, h stage.Hunk) []string {
	delCounts := make(map[string]int, len(h.Del))
	for _, d := range h.Del {
		delCounts[d]++
	}

	var lines []string
	for _, line := range h.Before {
		if delCounts[line] > 0 {
			delCounts[line]--
			continue
		}
		lines = append(lines, theme.Muted.Render("  "+line))
	}
	for _, line := range h.Del {
		lines = append(lines, theme.Bad.Render("- "+line))
	}
	for _, line := range h.Add {
		lines = append(lines, theme.Good.Render("+ "+line))
	}
	return lines
}

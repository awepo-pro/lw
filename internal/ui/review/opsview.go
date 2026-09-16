// opsview.go renders the review screen's two summary panels: Ops (one row
// per op: glyph, kind, basename, directory hint) and Changeset (source, id,
// op/hunk/checks counts), ported from mockgen.ops_panel and
// mockgen.changeset_panel. Both are pure string builders over the loaded
// model — no engine call lives in this file.
package review

import (
	"fmt"
	"path"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// glyphFor is an op's Ops-panel glyph (conventions §4.6; s2-screens.md
// T06): ● proposed (muted) · ◐ some hunks dropped (fg) · ✗ all hunks
// dropped or the op dropped (bad) · ! stale (warn, bold). Dropped-ness
// comes from the op's OpDiff windows that carry a HunkID — an op whose
// state is StateDropped counts as all dropped — so a hunk-only drop (the
// mockup vault's fourth op) is visible without the op itself being
// dropped. Stale wins: it is the state that demands action before any
// hunk can be reviewed at all.
func glyphFor(t ui.Theme, op stage.Op, files []stage.FileOpDiff) (string, lipgloss.Style) {
	if op.State == stage.StateStale {
		return "!", t.Warn.Bold(true)
	}
	if opIsDropped(op, files) {
		return "✗", t.Bad
	}
	if countWindows(files, true) > 0 {
		return "◐", t.Fg
	}
	return "●", t.Muted
}

// opIsDropped reports whether the op presents as dropped: its own state
// is StateDropped, or every window it persists (HunkID != "") is dropped.
// An op with no attributed windows is never "dropped" on window evidence
// alone — its own state decides.
func opIsDropped(op stage.Op, files []stage.FileOpDiff) bool {
	if op.State == stage.StateDropped {
		return true
	}
	total, dropped := countWindows(files, false), countWindows(files, true)
	return total > 0 && dropped == total
}

// countWindows counts the op's DisplayHunks: all of them, or only the
// dropped ones when droppedOnly. Ownerless windows count too — the
// Changeset panel's hunk row must agree with Diff's unified hunks plus
// dropped windows (MASTER §9 C20).
func countWindows(files []stage.FileOpDiff, droppedOnly bool) int {
	n := 0
	for _, f := range files {
		for _, w := range f.Hunks {
			if !droppedOnly || w.Dropped {
				n++
			}
		}
	}
	return n
}

// kindLabel is the five-cell kind an op shows as (mockgen.KIND): the
// three common kinds get their short names, everything else its backbone
// name clipped to 5 (s2-screens.md T06).
func kindLabel(k stage.OpKind) string {
	switch k {
	case stage.OpIngestSource:
		return "src"
	case stage.OpCreatePage:
		return "new"
	case stage.OpPatchPage:
		return "patch"
	}
	s := string(k)
	if len(s) > 5 {
		s = s[:5]
	}
	return s
}

// opsPanel renders the Ops panel at w×h: one row per op in changeset
// order (dropped ops included), the op holding the current cursor stop
// marked with the cursor gutter, and "i of N" on the bottom border
// (mockgen.ops_panel).
func (m *Model) opsPanel(w, h int) []string {
	currentID, _, _ := resolveCursor(m.diff, m.stops, m.cursor)
	lines := make([]string, 0, len(m.ops))
	cursorRow := -1
	for i, op := range m.ops {
		if op.ID == currentID && currentID != "" {
			cursorRow = i
		}
		lines = append(lines, m.opsRow(op, w-4))
	}
	foot := ""
	if len(m.ops) > 0 {
		if cursorRow >= 0 {
			foot = fmt.Sprintf("%d of %d", cursorRow+1, len(m.ops))
		} else {
			foot = fmt.Sprintf("– of %d", len(m.ops))
		}
	}
	return ui.Panel(m.theme, ui.PanelSpec{
		Title:     "Ops",
		Lines:     lines,
		CursorRow: cursorRow,
		FootNote:  foot,
	}, w, h)
}

// opsRow builds one Ops row (mockgen.ops_panel): glyph at content column
// 0, the kind at column 2 padded to 6, the basename at column 8 clipped
// to cw-8, then — when it fits in what remains — the full directory plus
// "/", else its last segment plus "/", else nothing. A dropped op's kind
// is faint and its basename faint with strikethrough.
func (m *Model) opsRow(op stage.Op, cw int) string {
	files := m.opDiffs[op.ID]
	glyph, gstyle := glyphFor(m.theme, op, files)
	dropped := opIsDropped(op, files)

	kstyle, bstyle := m.theme.Muted, m.theme.Fg
	if dropped {
		kstyle, bstyle = m.theme.Faint, m.theme.Faint.Strikethrough(true)
	}

	full := opDisplayPath(op)
	dir, base := path.Split(full) // dir keeps its trailing "/"; both may be ""

	bt := ui.Clip(base, cw-8)
	kind := kindLabel(op.Kind)
	if lipgloss.Width(kind) > 6 {
		kind = ui.Clip(kind, 6)
	}

	// The directory hint's second candidate is dir's last segment plus
	// "/", and there is none when the path has no directory at all
	// (mockgen.ops_panel: d+'/', else d.split('/')[-1]+'/', else none).
	lastDir := ""
	if d := path.Clean(dir); d != "." && d != "/" {
		lastDir = path.Base(d) + "/"
	}

	var b strings.Builder
	b.WriteString(gstyle.Render(glyph))
	b.WriteString(" ")
	b.WriteString(kstyle.Render(kind))
	b.WriteString(strings.Repeat(" ", 6-lipgloss.Width(kind)))
	b.WriteString(bstyle.Render(bt))
	// The hint starts two cells after the basename's end (mockgen.ops_panel
	// draws it at content column 8+len(bt)+2).
	rest := cw - 8 - lipgloss.Width(bt) - 2
	for _, cand := range []string{dir, lastDir} {
		if cand == "" || cand == "/" {
			continue
		}
		if lipgloss.Width(cand) <= rest {
			b.WriteString("  ")
			b.WriteString(m.theme.Faint.Render(cand))
			break
		}
	}
	return b.String()
}

// changesetPanel renders the Changeset panel at w×h (mockgen
// .changeset_panel): source, id, op and hunk counts, and the four
// projected checks.
func (m *Model) changesetPanel(w, h int) []string {
	return ui.Panel(m.theme, ui.PanelSpec{
		Title:     "Changeset",
		Lines:     m.changesetRows(),
		CursorRow: -1,
	}, w, h)
}

// changesetRows computes the Changeset panel's rows from the loaded model
// (MASTER §9 C20: nothing here is a literal — the mockup's numbers are the
// data). Checks are ✓ in Good, ✗ in Bad; labels and separators are muted
// and the row labels faint, exactly as mockgen.changeset_panel styles
// them.
func (m *Model) changesetRows() []string {
	if m.changeset == nil {
		return nil
	}
	t := m.theme
	label := t.Faint.Render
	muted := t.Muted.Render
	good := t.Good.Render
	bad := t.Bad.Render
	fg := t.Fg.Render
	check := func(ok bool) string {
		if ok {
			return good("✓")
		}
		return bad("✗")
	}

	droppedOps := 0
	staleOps := 0
	keptHunks, droppedHunks := 0, 0
	for _, op := range m.ops {
		files := m.opDiffs[op.ID]
		switch {
		case op.State == stage.StateStale:
			staleOps++
		case opIsDropped(op, files):
			droppedOps++
		}
		dropped := countWindows(files, true)
		keptHunks += countWindows(files, false) - dropped
		droppedHunks += dropped
	}

	droppedSeg := muted(fmt.Sprintf("%d dropped", droppedOps))
	if droppedOps > 0 {
		droppedSeg = bad(fmt.Sprintf("%d dropped", droppedOps))
	}
	hunksDroppedSeg := muted(fmt.Sprintf("%d dropped", droppedHunks))
	if droppedHunks > 0 {
		hunksDroppedSeg = bad(fmt.Sprintf("%d dropped", droppedHunks))
	}

	var c stage.Checks
	if m.changeset != nil {
		c = m.changeset.Checks
	}

	return []string{
		label("source  ") + fg(m.sourceLabel()),
		label("id      ") + muted(m.changesetID()),
		label("ops     ") + fg(fmt.Sprintf("%d", len(m.ops))) + muted(" · ") + droppedSeg + muted(fmt.Sprintf(" · %d stale", staleOps)),
		label("hunks   ") + fg(fmt.Sprintf("%d kept", keptHunks)) + muted(" · ") + hunksDroppedSeg,
		"",
		label("checks  ") + check(c.Schema == "pass") + muted(" schema   ") + check(c.Lint == "pass") + muted(" lint"),
		strings.Repeat(" ", 8) + check(c.Orphans == 0) + muted(" orphans  ") + check(c.BrokenLinks == 0) + muted(" links"),
	}
}

// sourceLabel is the Changeset panel's source row: when the intent starts
// with "ingest ", the base name of the rest; otherwise the intent itself
// (s2-screens.md T06).
func (m *Model) sourceLabel() string {
	if m.changeset == nil {
		return ""
	}
	intent := m.changeset.Intent
	if rest, ok := strings.CutPrefix(intent, "ingest "); ok {
		return path.Base(strings.TrimRight(rest, " "))
	}
	return intent
}

// changesetID returns the changeset id, or "" with no changeset open.
func (m *Model) changesetID() string {
	if m.changeset == nil {
		return ""
	}
	return m.changeset.ID
}

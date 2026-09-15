// diffblock.go renders the review screen's Detail panel content: the Diff
// mode (the current op and every later op as attributed OpDiff windows,
// mockgen.diff_block / op_head / detail) and the Preview mode (the
// current op's staged page through the shared markdown renderer,
// mockgen.preview_block). Pure string builders over the loaded model,
// except the Preview's single StagedFile read.
package review

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// panelLine is one line of the Detail panel: its styled text and whether
// it belongs to the cursor's hunk window (the ▌ gutter + cursor tint,
// mockgen.draw_lines' per-line cur flag).
type panelLine struct {
	text   string
	cursor bool
}

// opHead builds an op's header row (mockgen.op_head): kind (muted), two
// spaces, path (bold; faint + strikethrough when the op presents as
// dropped), then — when at least two cells of padding remain — the note
// right-aligned to cw (bad when dropped, muted otherwise).
func opHead(t ui.Theme, op stage.Op, cw int, note string, dropped bool) string {
	pathStyle := t.Fg.Bold(true)
	if dropped {
		pathStyle = t.Faint.Strikethrough(true)
	}
	head := t.Muted.Render(kindLabel(op.Kind)) + "  " + pathStyle.Render(opDisplayPath(op))

	noteStyle := t.Muted
	if dropped {
		noteStyle = t.Bad
	}
	pad := cw - lipgloss.Width(head) - lipgloss.Width(note)
	if pad >= 2 {
		head += strings.Repeat(" ", pad) + noteStyle.Render(note)
	}
	return head
}

// diffLines builds the Diff panel's lines: the current op first, then
// every later op, separated by a blank line, a full-width rule and a
// blank line (mockgen.detail — "the detail keeps scrolling through the
// changeset").
func (m *Model) diffLines(cw int) []panelLine {
	var lines []panelLine
	start := m.currentOpIndex()
	for i, op := range m.ops {
		if i < start {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines,
				panelLine{},
				panelLine{text: m.theme.Border.Render(strings.Repeat("─", cw))},
				panelLine{})
		}
		lines = append(lines, m.opDiffLines(op, cw, i == start)...)
	}
	return lines
}

// opDiffLines builds one op's Diff-mode lines (mockgen.diff_block): the
// op head (`opN · K hunk(s)`, or `opN · dropped`), the rationale wrapped
// to cw and cut at four lines with "…", the provenance hung at 9, then
// one blank line, header and body per OpDiff window. When current is
// set, the window matching the cursor stop's hunk id — and every line of
// it — carries the cursor mark.
func (m *Model) opDiffLines(op stage.Op, cw int, current bool) []panelLine {
	files := m.opDiffs[op.ID]
	dropped := opIsDropped(op, files)

	var note string
	if dropped {
		note = op.ID + " · dropped"
	} else {
		n := countWindows(files, false)
		note = fmt.Sprintf("%s · %d hunk%s", op.ID, n, plural(n))
	}

	out := []panelLine{{text: opHead(m.theme, op, cw, note, dropped)}}

	for _, l := range wrapCut(op.Rationale, cw, 4) {
		out = append(out, panelLine{text: m.theme.Muted.Render(l)})
	}
	out = append(out, m.provenanceLines(op, cw)...)

	var cursorHunkID string
	if current {
		_, cursorHunkID, _ = resolveCursor(m.diff, m.stops, m.cursor)
	}
	for _, f := range files {
		for _, w := range f.Hunks {
			out = append(out, m.windowLines(w, cw, w.HunkID != "" && w.HunkID == cursorHunkID)...)
		}
	}
	return out
}

// windowLines builds one DisplayHunk window: a blank line, the header
// (faint) plus "  hN" (muted; omitted for an ownerless window) plus
// "  dropped" (bad) when dropped, then each line as "<kind> " + text
// wrapped to cw-2 with continuation lines indented two (mockgen
// .diff_block wraps the text alone so a context prefix never vanishes).
// A dropped window is faint with strikethrough throughout.
func (m *Model) windowLines(w stage.DisplayHunk, cw int, cursor bool) []panelLine {
	out := make([]panelLine, 0, len(w.Lines)+2)
	out = append(out, panelLine{})

	header := m.theme.Faint.Render(w.Header)
	if w.HunkID != "" {
		header += "  " + m.theme.Muted.Render(w.HunkID)
	}
	if w.Dropped {
		header += "  " + m.theme.Bad.Render("dropped")
	}
	out = append(out, panelLine{text: header, cursor: cursor})

	for _, dl := range w.Lines {
		style := m.lineStyle(dl.Kind, w.Dropped)
		prefix := string(dl.Kind) + " "
		for i, l := range ui.Wrap(dl.Text, cw-2, 0) {
			if i == 0 {
				out = append(out, panelLine{text: style.Render(prefix) + style.Render(l), cursor: cursor})
				continue
			}
			out = append(out, panelLine{text: "  " + style.Render(l), cursor: cursor})
		}
	}
	return out
}

// lineStyle styles one diff line: '+' good, '-' bad, context muted — or
// faint with strikethrough across a dropped window.
func (m *Model) lineStyle(kind byte, dropped bool) lipgloss.Style {
	if dropped {
		return m.theme.Faint.Strikethrough(true)
	}
	switch kind {
	case '+':
		return m.theme.Good
	case '-':
		return m.theme.Bad
	default:
		return m.theme.Muted
	}
}

// provenanceLines renders `sources  a, b` wrapped to cw with a hang of 9
// (mockgen.diff_block). The wrap is greedy on spaces, so the label's
// second space collapses into the single separator — line 0 is "sources "
// (faint, label plus that separator) and the names (muted) — which is
// also why the faint run on line 0 is len("sources")+1 cells.
func (m *Model) provenanceLines(op stage.Op, cw int) []panelLine {
	if len(op.Provenance) == 0 {
		return nil
	}
	const label = "sources  " // the second space collapses in the wrap
	const faintLen = len(label) - 1
	wrapped := ui.Wrap(label+strings.Join(op.Provenance, ", "), cw, 9)
	out := make([]panelLine, 0, len(wrapped))
	for i, l := range wrapped {
		switch {
		case i == 0 && len(l) > faintLen:
			out = append(out, panelLine{text: m.theme.Faint.Render(l[:faintLen]) + m.theme.Muted.Render(l[faintLen:])})
		case i == 0:
			out = append(out, panelLine{text: m.theme.Faint.Render(l)})
		default:
			out = append(out, panelLine{text: m.theme.Muted.Render(l)})
		}
	}
	return out
}

// previewLines builds the Preview panel's lines: the current op's head
// (`opN · staged page`), a blank line, then the staged page through the
// shared markdown renderer with the op's non-dropped added lines marking
// the ▎ gutter (s2-screens.md T06, mockgen.preview_block). Only the
// current op is shown; a file with no staged content shows as deleted.
func (m *Model) previewLines(cw int) []panelLine {
	out := make([]panelLine, 0, 8)
	op, ok := m.currentOp()
	if !ok {
		return append(out, panelLine{text: m.theme.Faint.Render("(nothing staged)")})
	}

	dropped := opIsDropped(op, m.opDiffs[op.ID])
	out = append(out, panelLine{text: opHead(m.theme, op, cw, op.ID+" · staged page", dropped)})
	out = append(out, panelLine{})

	path := op.Path
	if path == "" {
		path = op.To
	}
	if m.deps.Engine == nil || path == "" {
		return append(out, panelLine{text: m.theme.Faint.Render("(deleted in this changeset)")})
	}
	staged, ok, err := m.deps.Engine.StagedFile(path)
	if err != nil || !ok {
		return append(out, panelLine{text: m.theme.Faint.Render("(deleted in this changeset)")})
	}

	lines, err := m.md.Render(staged, markdown.Options{
		Width:   cw,
		Style:   m.markdownStyle(),
		Changed: addedTexts(m.opDiffs[op.ID]),
	})
	if err != nil {
		return append(out, panelLine{text: m.theme.Warn.Render("preview failed: " + err.Error())})
	}
	for _, l := range lines {
		out = append(out, panelLine{text: l})
	}
	return out
}

// addedTexts collects the text of every '+' line in the op's non-dropped
// windows, blanks excluded — the Changed set the renderer marks the ▎
// gutter from (mockgen.preview_block: `p == '+' and t.strip()`).
func addedTexts(files []stage.FileOpDiff) []string {
	var out []string
	for _, f := range files {
		for _, w := range f.Hunks {
			if w.Dropped {
				continue
			}
			for _, dl := range w.Lines {
				if dl.Kind == '+' && strings.TrimSpace(dl.Text) != "" {
					out = append(out, dl.Text)
				}
			}
		}
	}
	return out
}

// markdownStyle builds the renderer's palette from the theme (contract
// §3: a plain struct literal; markdown.Style and ui.Palette share field
// names on purpose).
func (m *Model) markdownStyle() markdown.Style {
	p := m.theme.Palette
	return markdown.Style{
		Dark:   m.theme.IsDark,
		Fg:     p.Fg,
		Muted:  p.Muted,
		Faint:  p.Faint,
		Border: p.Border,
		Accent: p.Accent,
		Good:   p.Good,
		Warn:   p.Warn,
		Bad:    p.Bad,
	}
}

// wrapCut greedy-wraps s to w and cuts the result at max lines, ending
// the last one with "…" exactly as mockgen.diff_block cuts a rationale.
// Rationales can carry hard newlines (the public fixture's does); ui.Wrap
// splits on spaces alone, so whitespace is normalized first — the same
// collapse wrap applies to space runs anyway, and the pane's exactly-h-
// lines invariant cannot carry embedded newlines.
func wrapCut(s string, w, max int) []string {
	lines := ui.Wrap(strings.Join(strings.Fields(s), " "), w, 0)
	if len(lines) <= max {
		return lines
	}
	lines = lines[:max]
	lines[max-1] += "…"
	return lines
}

// plural is "hunk" vs "hunks" (mockgen.diff_block's `'s' * (n != 1)`).
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

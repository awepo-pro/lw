// transcript.go draws what goes inside the ask screen's Transcript panel:
// the panel itself with its tail-follow and overflow notes, the empty
// state's intro and suggested prompts (mockgen.ask_empty), and the
// conversation turn shape (mockgen.ask_conversation). The frame around it
// is view.go; the inline renderer the assistant's prose goes through is
// inline.go.
package ask

import (
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/ui"
)

// introSentence is the empty transcript's intro (mockgen.ask_empty), muted.
const introSentence = "Ask the wiki a question. Answers cite the pages they come from, and anything the " +
	"agent proposes to change opens in Review. Nothing reaches the vault until you accept it."

// transcriptPanel draws the scrollback panel at w×th. An empty transcript
// shows the intro and prompts from the top (mockgen.ask_empty, overflow as
// the panel's own `↓ N more`); a conversation shows the turn shape,
// tail-following once it outgrows the panel, with `↑ N earlier` on the top
// border (s2-screens.md T08 Overflow).
func (m *Model) transcriptPanel(w, th int) []string {
	cw := w - 4
	note := ""
	cursor := -1

	lines := m.emptyTranscriptLines(cw, th)
	if len(m.entries) > 0 {
		lines, cursor = m.conversationLines(min(cw, 100))
		// inner > 0 keeps the degenerate sizes (th < 3) away from the
		// tail math; Panel clamps those, and View normalizes the result.
		if inner := th - 2; inner > 0 && len(lines) > inner {
			hidden := len(lines) - inner
			note = fmt.Sprintf("↑ %d earlier", hidden)
			lines = lines[hidden:]
			cursor -= hidden
		}
	}
	if cursor < 0 || cursor >= len(lines) {
		cursor = -1
	}

	return ui.Panel(m.theme, ui.PanelSpec{
		Title:     "Transcript",
		Note:      note,
		Lines:     lines,
		CursorRow: cursor,
		// The empty state reports top-overflow through the panel's own
		// FootNote (mockgen.draw_lines + foot); a conversation never
		// overflows the bottom — it follows the tail instead.
		Overflow: len(m.entries) == 0,
	}, w, th)
}

// emptyTranscriptLines is mockgen.ask_empty: blank lead-in space, the
// muted intro wrapped at W, a blank, a bold `Try`, then the three
// suggested prompts as faint `  › ` rows.
func (m *Model) emptyTranscriptLines(cw, th int) []string {
	W := min(cw, 100)

	var lines []string
	for i := 0; i < max(0, (th-2)/4-1); i++ {
		lines = append(lines, "")
	}
	for _, l := range ui.Wrap(introSentence, W, 0) {
		lines = append(lines, m.theme.Muted.Render(l))
	}
	lines = append(lines, "", m.theme.Bold.Render("Try"))
	for _, p := range m.prompts {
		lines = append(lines, ui.Clip(m.theme.Faint.Render("  › ")+p, W))
	}
	return lines
}

// conversationLines renders the entries into the turn shape the frozen
// ask-conversation grids pin: `you` (bold) plus the wrapped question, the
// tool rows, `assistant` (bold) plus the wrapped answer, then the turn
// status. Blanks separate the blocks exactly as mockgen.ask_conversation
// draws them: one after the question, one before `assistant`, one between
// the answer and the status, and one between two turns. cursor is the
// selected tool call's head line, or -1.
func (m *Model) conversationLines(w int) (lines []string, cursor int) {
	cursor = -1
	prev := kindUser // a leading user entry opens the transcript, no blank
	for i, e := range m.entries {
		switch e.kind {
		case kindUser:
			if prev == kindStatus || prev == kindError {
				lines = append(lines, "")
			}
			lines = append(lines, m.theme.Bold.Render("you"))
			lines = append(lines, wrapPlain(e.text, w)...)
			lines = append(lines, "")
		case kindTool:
			if i == m.selected {
				cursor = len(lines) // the head line carries the gutter
			}
			lines = append(lines, m.toolLines(e.tool, w)...)
		case kindAssistant:
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, m.theme.Bold.Render("assistant"))
			lines = append(lines, m.inlineWrap(e.text, w)...)
			lines = append(lines, "")
		case kindStatus:
			for _, l := range wrapPlain(e.text, w) {
				lines = append(lines, m.theme.Faint.Render(l))
			}
		case kindError:
			for _, l := range wrapPlain("error: "+e.text, w) {
				lines = append(lines, m.theme.Bad.Render(l))
			}
		}
		prev = e.kind
	}
	return lines, cursor
}

// toolLines renders one tool call: the collapsed `▸ name args  → result`
// row clipped to W, or — expanded — that row plus the args and the full
// result indented 4, muted (s2-screens.md T08). A failed result keeps the
// visible `✗` mark and the bad colour.
func (m *Model) toolLines(tc *toolCall, w int) []string {
	if tc == nil {
		return nil
	}
	lines := []string{m.toolHeadLine(tc, w)}
	if !tc.expanded {
		return lines
	}

	lines = append(lines, ui.Clip(m.theme.Muted.Render("    "+tc.args), w))
	if !tc.resolved {
		lines = append(lines, ui.Clip(m.theme.Muted.Render("    …"), w))
		return lines
	}
	style := m.theme.Muted
	if tc.isError {
		style = m.theme.Bad
	}
	for _, l := range wrapPlain(tc.content, max(1, w-4)) {
		lines = append(lines, ui.Clip(style.Render("    "+l), w))
	}
	return lines
}

// toolHeadLine is one collapsed tool row (mockgen.ask_conversation):
// `▸ ` faint, name muted, args faint, then `  → ` and the result's first
// line muted — or `…` while the result is still waiting, or a `✗` in bad
// when it failed.
func (m *Model) toolHeadLine(tc *toolCall, w int) string {
	head := m.theme.Faint.Render("▸ ") +
		m.theme.Muted.Render(tc.name) + " " +
		m.theme.Faint.Render(tc.args)
	switch {
	case !tc.resolved:
		head += m.theme.Faint.Render("  → ") + m.theme.Muted.Render("…")
	case tc.isError:
		head += m.theme.Faint.Render("  ✗ ") + m.theme.Bad.Render(firstLine(tc.content))
	default:
		head += m.theme.Faint.Render("  → ") + m.theme.Muted.Render(firstLine(tc.content))
	}
	return ui.Clip(head, w)
}

// firstLine returns s up to its first newline — a collapsed tool row shows
// the result's first line only (s2-screens.md T08).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimRight(s[:i], "\r")
	}
	return s
}

// wrapPlain word-wraps text at w with ui.Wrap, one paragraph per source
// line — ui.Wrap itself knows no newlines. It never returns an empty
// slice: an empty entry still occupies one blank line.
func wrapPlain(text string, w int) []string {
	if w < 1 {
		w = 1
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		out = append(out, ui.Wrap(para, w, 0)...)
	}
	return out
}

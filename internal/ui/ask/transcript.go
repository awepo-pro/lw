// transcript.go draws what goes inside the ask screen's Transcript panel:
// the panel itself with its tail-follow and overflow notes, the empty
// state's intro, unconfigured-web hint and suggested prompts
// (mockgen.ask_empty), and the conversation turn shape
// (mockgen.ask_conversation). The frame around it
// is view.go; assistant prose renders through the shared markdown renderer
// (005 contract §5) with inline.go demoted to the live turn's unwritten
// tail and Ask's own chrome.
package ask

import (
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// introSentence is the empty transcript's intro (mockgen.ask_empty), muted.
const introSentence = "Ask the wiki a question. Answers cite the pages they come from, and anything the " +
	"agent proposes to change opens in Review. Nothing reaches the vault until you accept it."

// transcriptPanel draws the scrollback panel at w×th. An empty transcript
// shows the intro and prompts from the top (mockgen.ask_empty, overflow as
// the panel's own `↓ N more`); a conversation shows the turn shape, with
// the scrollback window `lines[len-inner-back : len-back]` (scroll.go):
// `↑ N earlier` on the top border for the lines above the window (N = the
// window's start, omitted at 0) and `↓ N newer` on the bottom border for
// the lines hidden below it (N = back, omitted at 0) (s2-screens.md T08
// "Scroll", W5 F2/C36).
func (m *Model) transcriptPanel(w, th int) []string {
	cw := w - 4
	note := ""
	footNote := ""
	cursor := -1

	lines := m.emptyTranscriptLines(cw, th)
	if len(m.entries) > 0 {
		lines, cursor = m.conversationLines(min(cw, 100))
		// inner > 0 keeps the degenerate sizes (th < 3) away from the
		// scroll math; Panel clamps those, and View normalizes the result.
		if inner := th - 2; inner > 0 && len(lines) > inner {
			// Clamp here too — the offset is clamped at every render and
			// every key (contract §5 note 10), and an expand/collapse or an
			// in-place result can shrink the line count under a stored
			// back.
			maxBack := len(lines) - inner
			m.back = min(m.back, maxBack)
			start := len(lines) - inner - m.back
			if start > 0 {
				note = fmt.Sprintf("↑ %d earlier", start)
			}
			if m.back > 0 {
				footNote = fmt.Sprintf("↓ %d newer", m.back)
			}
			lines = lines[start : len(lines)-m.back]
			cursor -= start
		}
	}
	if cursor < 0 || cursor >= len(lines) {
		cursor = -1
	}

	return ui.Panel(m.theme, ui.PanelSpec{
		Title:     m.transcriptTitle(),
		Note:      note,
		FootNote:  footNote,
		Lines:     lines,
		CursorRow: cursor,
		// The empty state reports top-overflow through the panel's own
		// FootNote (mockgen.draw_lines + foot); a conversation reports its
		// scroll position through the same slot instead — it never uses
		// `↓ N more`.
		Overflow: len(m.entries) == 0,
	}, w, th)
}

// emptyTranscriptLines is mockgen.ask_empty: blank lead-in space, the
// muted intro wrapped at W, a blank, a bold `Try`, then the three
// suggested prompts as faint `  › ` rows. Between intro and Try, a vault
// whose web lookup is not configured while an agent is wired adds the
// wrapped web-unavailable hint, each line faint (012 contract §3) — an
// empty-state line, never a transcript entry, and never drawn on a
// configured vault, whose empty state stays byte-identical to the mockup's
// (cs-79f2d7).
func (m *Model) emptyTranscriptLines(cw, th int) []string {
	W := min(cw, 100)

	var lines []string
	for i := 0; i < max(0, (th-2)/4-1); i++ {
		lines = append(lines, "")
	}
	for _, l := range ui.Wrap(introSentence, W, 0) {
		lines = append(lines, m.theme.Muted.Render(l))
	}
	if !m.deps.WebSearch && m.deps.Agent != nil {
		lines = append(lines, "")
		for _, l := range ui.Wrap(webHintUnavailable, W, 0) {
			lines = append(lines, m.theme.Faint.Render(l))
		}
	}
	lines = append(lines, "", m.theme.Bold.Render("Try"))
	for _, p := range m.prompts {
		lines = append(lines, ui.Clip(m.theme.Faint.Render("  › ")+p, W))
	}
	return lines
}

// conversationLines renders the entries into the turn shape the frozen
// ask-conversation grids pin: `you` (bold) plus the wrapped question, the
// tool rows, `assistant` (bold) plus the rendered answer, then the turn
// status. Blanks separate the blocks exactly as mockgen.ask_conversation
// draws them: one after the question, one before `assistant`, one between
// the answer and the status, and one between two turns. cursor is the
// selected tool call's head line, or -1. Every line counted here is a
// RENDERED line — the scroll math (scroll.go) and the ↑/↓ notes count
// this list, so markdown's reflow feeds them by construction (005
// contract §5 note 5).
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
			// The turn's live entry renders its settled blocks and keeps the
			// half-written tail plain (D-5A); every earlier entry's buffer is
			// complete and renders whole.
			lines = append(lines, m.assistantLines(e, w, m.turnActive && i == len(m.entries)-1)...)
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

// assistantLines renders one assistant entry's buffer at w. A finished
// entry (its turn has ended, or the turn moved on past it) renders the
// whole buffer through the shared renderer — the same fragment entry
// point a page body goes through, so a heading here is the heading a
// preview draws (005 contract §5 note 2). A live entry — the turn is still
// running and this is its last entry — renders its settled prefix through
// the same renderer and keeps the tail plain via inlineWrap, separated by
// the one blank line renderBody itself puts between blocks: settled+tail
// split never shows a half-open fence, and the blank keeps the live shape
// byte-for-byte the shape the finished render settles into.
func (m *Model) assistantLines(e entry, w int, live bool) []string {
	if !live {
		return m.fragmentLines(e.text, w)
	}
	settled, tail := markdown.SettledPrefix(e.text)
	lines := m.fragmentLines(settled, w)
	if len(lines) > 0 && tail != "" {
		lines = append(lines, "")
	}
	return append(lines, m.inlineWrap(tail, w)...)
}

// fragmentLines renders one markdown buffer as a fragment at w (005
// contract §1). A render error — glamour has no error paths for ordinary
// vault input, but it is not impossible — degrades to the plain inline
// wrap the tail uses rather than dropping the answer's lines on the floor.
func (m *Model) fragmentLines(src string, w int) []string {
	if src == "" {
		return nil
	}
	lines, err := m.md.RenderFragment([]byte(src), markdown.Options{Width: w, Style: m.mdStyle()})
	if err != nil {
		return m.inlineWrap(src, w)
	}
	return lines
}

// mdStyle builds the renderer's palette from this pane's own theme copy
// (contract §3: a plain struct literal; markdown.Style and ui.Palette
// share field names on purpose). Heading and Code ride along (W5 F3):
// without them the renderer sees an empty hex and draws headings in the
// fallback colour instead of the palette's.
func (m *Model) mdStyle() markdown.Style {
	p := m.theme.Palette
	return markdown.Style{
		Dark:    m.theme.IsDark,
		Fg:      p.Fg,
		Muted:   p.Muted,
		Faint:   p.Faint,
		Border:  p.Border,
		Accent:  p.Accent,
		Good:    p.Good,
		Warn:    p.Warn,
		Bad:     p.Bad,
		Heading: p.Heading,
		Code:    p.Code,
	}
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

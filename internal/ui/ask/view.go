// view.go renders the ask screen's frame against the frozen grids
// (s2-screens.md T08, mockgen.py ask): a Transcript panel (not focused)
// over a focused 3-row Message panel. What goes inside the transcript —
// the empty state and the conversation turn shape — is transcript.go; the
// inline renderer that marks up assistant prose is inline.go.
package ask

import (
	"strings"

	"github.com/awepo-pro/lw/internal/ui"
)

// inputPlaceholder is the faint hint in an empty message box.
const inputPlaceholder = "Ask about the wiki…"

// webHintUnavailable is the empty state's notice that this vault's web
// lookup is not configured (012 contract §3, cs-79f2d7): the agent the pane
// drives has no web.search verb, so a question that expects an out-of-vault
// answer would silently get none. emptyTranscriptLines renders it wrapped
// and faint, only while no provider is wired — a configured vault's empty
// state never shows it. The bytes are frozen; improving the wording is out
// of scope.
const webHintUnavailable = "web lookup unavailable — lw config set web.api_key env:TAVILY_API_KEY"

// View renders the ask screen at exactly w by h (backbone §12): the
// Transcript panel (height h-3) over the focused Message panel (height 3).
// Below the height both panels need, the transcript takes the whole pane —
// a message box three cells tall cannot fit, and the size invariants must
// hold at any size (conventions §4 rule 2).
func (m *Model) View(w, h int) string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	// Remember the size the shell drew, so the scroll keys' steps and
	// clamps are expressed in the Transcript panel's real lines (scroll.go)
	// — a key can only arrive between frames, and every frame renders.
	m.vw, m.vh = w, h

	th, ih := paneLayout(w, h)

	rows := m.transcriptPanel(w, th)
	if ih > 0 {
		rows = append(rows, m.messagePanel(w, ih)...)
	}

	// Belt and braces for the degenerate sizes Panel clamps: exactly h
	// lines of exactly w cells, come what may.
	if len(rows) > h {
		rows = rows[:h]
	}
	for len(rows) < h {
		rows = append(rows, strings.Repeat(" ", w))
	}
	for i := range rows {
		rows[i] = ui.Pad(rows[i], w)
	}
	return strings.Join(rows, "\n")
}

// messagePanel draws the focused 3-row Message panel (mockgen.ask): `›` in
// accent at content column 0, the input text from column 2 with the accent
// `█` cursor right after it — or, when the box is empty, the cursor at
// column 2 and the faint placeholder at column 4.
func (m *Model) messagePanel(w, ih int) []string {
	cw := w - 4
	accent := m.theme.Accent

	var content string
	if m.input == "" {
		content = accent.Render("›") + " " + accent.Render("█") + " " + m.theme.Faint.Render(inputPlaceholder)
	} else {
		// Keep the cursor on screen: an input longer than the row shows its
		// tail, the way a terminal does at the right margin.
		in := []rune(m.input)
		if room := cw - 3; room >= 0 && len(in) > room {
			in = in[len(in)-room:]
		}
		content = accent.Render("›") + " " + string(in) + accent.Render("█")
	}

	return ui.Panel(m.theme, ui.PanelSpec{
		Title:   "Message",
		Focused: true,
		Lines:   []string{content},
		// The `█` is this screen's cursor (accent fg, no tint); the panel's
		// gutter cursor is the transcript's, never the input's.
		CursorRow: -1,
	}, w, ih)
}

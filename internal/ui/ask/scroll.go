// scroll.go implements the transcript's tail-follow scrollback (W5 F2/C36,
// s2-screens.md T08 "Scroll"; contract §5 note 10): `back` is the number of
// conversation lines hidden BELOW the Transcript panel — 0 means following
// the tail, which is how the pane has always rendered. The six shell scroll
// bindings and the wheel move back; submitting re-attaches the tail; and
// while the pane is scrolled up, lines appended by new events are absorbed
// into back so the rows on screen never move. transcript.go draws the
// window this state names.
package ask

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui"
)

// wheelLinesPerNotch is how far one wheel notch scrolls (contract §5
// note 10: "one wheel notch moves 3 lines").
const wheelLinesPerNotch = 3

// The pane must satisfy the shell's scroll surface: the `?` overlay shows
// its Scroll group only while the active pane reports true (contract §5).
var _ ui.Scroller = (*Model)(nil)

// ScrollsContent implements ui.Scroller (contract §5, W5 F2/D-3W): the
// transcript is scrollable content.
func (m *Model) ScrollsContent() bool { return true }

// paneLayout splits a w×h pane into the Transcript panel's height and the
// Message panel's height, exactly as View lays them out. The wheel's
// hit-test shares it so a notch can only ever address the panel it landed
// on, and the scroll keys share it for their step sizes.
func paneLayout(w, h int) (transcriptH, messageH int) {
	messageH = 3
	transcriptH = h - messageH
	if transcriptH < 2 {
		// Below the height both panels need, the transcript takes the whole
		// pane (view.go) — and there is no Message panel to hit.
		transcriptH, messageH = h, 0
	}
	return transcriptH, messageH
}

// transcriptInner is the Transcript panel's inner height — the number of
// conversation lines one full window shows — at the size View last
// rendered. Zero before the first render; every scroll entry point treats
// that as "nothing to scroll" rather than guessing a size.
func (m *Model) transcriptInner() int {
	th, _ := paneLayout(m.vw, m.vh)
	return th - 2
}

// transcriptLines renders the full, unwindowed conversation line list at
// the width View last rendered at — transcript.go's conversationLines with
// the same W it clips to. The scroll math counts these lines; it never
// re-wraps at a different width, or a key's clamp and the next render
// would disagree.
func (m *Model) transcriptLines() []string {
	lines, _ := m.conversationLines(min(m.vw-4, 100))
	return lines
}

// scrollBy moves back by delta — positive shows older lines, negative
// newer — clamped to what the transcript currently overflows by. An empty
// transcript or one that fits the panel has nothing to hide, so it does
// not scroll (s2-screens.md T08: "the empty transcript does not scroll").
func (m *Model) scrollBy(delta int) {
	maxBack := m.maxBack()
	if maxBack <= 0 {
		m.back = 0
		return
	}
	m.back = min(max(m.back+delta, 0), maxBack)
}

// maxBack is how many conversation lines are hidden below the panel when
// the pane follows the tail: len(lines) - inner, floored at 0.
func (m *Model) maxBack() int {
	inner := m.transcriptInner()
	if inner <= 0 || len(m.entries) == 0 {
		return 0
	}
	return max(0, len(m.transcriptLines())-inner)
}

// pageStep is one ScrollPageUp/Down step, halfStep one ScrollHalfUp/Down
// step (contract §5 note 10: max(1, inner-1) and max(1, inner/2)).
func pageStep(inner int) int { return max(1, inner-1) }
func halfStep(inner int) int { return max(1, inner/2) }

// scrollKeys applies the shell's six content-scroll bindings to the
// transcript (contract §5 note 10) and reports whether one matched. Each
// is non-printable — no Text — so none of them can type into the input
// box, and they reach Ask while it captures text (ui.TextCapturer) like
// any other key. Matching through m.deps.Keys is what makes a rebound
// hotkeys.toml show in behaviour, not just in the footer.
func (m *Model) scrollKeys(msg tea.KeyPressMsg) bool {
	inner := m.transcriptInner()
	switch {
	case key.Matches(msg, m.deps.Keys.ScrollPageUp):
		m.scrollBy(pageStep(inner))
	case key.Matches(msg, m.deps.Keys.ScrollPageDown):
		m.scrollBy(-pageStep(inner))
	case key.Matches(msg, m.deps.Keys.ScrollHalfUp):
		m.scrollBy(halfStep(inner))
	case key.Matches(msg, m.deps.Keys.ScrollHalfDown):
		m.scrollBy(-halfStep(inner))
	case key.Matches(msg, m.deps.Keys.ScrollTop):
		m.scrollBy(m.maxBack()) // to the first line: back = max
	case key.Matches(msg, m.deps.Keys.ScrollBottom):
		m.back = 0 // re-attach: follow the tail again
	default:
		return false
	}
	return true
}

// wheel is one ui.WheelMsg notch (contract §5 frame note 7). Over the
// Transcript panel it scrolls three lines per notch — Delta -1 is a wheel
// up, which shows older lines; over the Message panel it does nothing. The
// hit-test uses the msg's own W×H, the size the shell passes to View, not
// a remembered frame.
func (m *Model) wheel(msg ui.WheelMsg) {
	th, _ := paneLayout(msg.W, msg.H)
	if msg.Y >= th {
		return
	}
	m.scrollBy(-msg.Delta * wheelLinesPerNotch)
}

// mutateEntries runs mut — anything that appends to or rewrites the
// entries — and, while the pane is scrolled up (back > 0), absorbs any
// lines the transcript grew by into back (W5 F2/C36). The window the
// panel draws is lines[len-inner-back : len-back]; growing both len and
// back by the same amount leaves it naming the same rows, so an event
// that lands mid-scroll never moves what is on screen. Following the tail
// (back == 0) skips the counting entirely.
func (m *Model) mutateEntries(mut func()) {
	pinned := m.back > 0
	var before int
	if pinned {
		before = len(m.transcriptLines())
	}
	mut()
	if pinned {
		if added := len(m.transcriptLines()) - before; added > 0 {
			m.back += added
		}
	}
}

// scroll.go implements the transcript's tail-follow scrollback (W5 F2/C36,
// s2-screens.md T08 "Scroll"; contract §5 note 10): `back` is the number of
// conversation lines hidden BELOW the Transcript panel — 0 means following
// the tail, which is how the pane has always rendered. The six shell scroll
// bindings and the wheel move back; submitting re-attaches the tail; and
// while the pane is scrolled up, entries mutations are accounted for so the
// rows on screen never move — growth at or below the window is absorbed
// into back, growth above it moves the window with the content
// (mutateEntries). transcript.go draws the window this state names.
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
// entries — and, while the pane is scrolled up (back > 0), accounts for
// the lines the mutation added or removed (W5 F2/C36; the above-window
// half is W5d/T34). The window the panel draws is
// lines[len-inner-back : len-back] — named from BOTH ends — so where the
// mutation's first changed line lands relative to the window's start
// decides whether back absorbs the line delta:
//
//   - at or after the window start: back += added, floored at 0. Pinning
//     the start is what keeps the rows the curator is reading still while
//     an answer streams in below them — growing len and back together
//     leaves the window naming the same rows. back+added stays within the
//     new max, since the max moves by the same added.
//   - above the window start: back unchanged. The delta shifted every row
//     the window names by the same amount, and a window named from the end
//     rides that shift on its own — absorbing it here would pin the old
//     indexes over rows that have moved.
//
// A zero delta — the common in-place ToolResEv on a collapsed call — is a
// no-op. The one shape neither branch pins is a mutation straddling the
// window start (a huge expanded entry collapsed across it): its rows
// change no matter the anchor, and the render-side clamp
// (transcript.go) stays the backstop that keeps back in bounds. Following
// the tail (back == 0) skips the counting entirely.
//
// 025: the accounting splits the conversation's tail mounts (022's
// thinking rise+line, 025's sending row — mountedTailLines) out of the
// content delta. They are chrome pinned to the list's very end, always at
// or below the window start, so their delta always absorbs into back.
// Mixed into one content number they defeated both branches: an
// above-window content change skips the absorb (firstChangedLine decides
// from the first differing line, above the window), and the unabsorbed
// mount delta then moved every row the end-named window draws — exactly
// the W5d/T34 shape, first exposed by the sending row.
func (m *Model) mutateEntries(mut func()) {
	if m.back <= 0 {
		mut()
		return
	}
	before := m.transcriptLines()
	mountBefore := m.mountedTailLines()
	start := len(before) - m.transcriptInner() - m.back
	mut()
	after := m.transcriptLines()
	mountAfter := m.mountedTailLines()

	contentBefore := before[:len(before)-mountBefore]
	contentAfter := after[:len(after)-mountAfter]
	if added := len(contentAfter) - len(contentBefore); added != 0 &&
		firstChangedLine(contentBefore, contentAfter) >= start {
		m.back = max(0, m.back+added)
	}
	if mount := mountAfter - mountBefore; mount != 0 {
		m.back = max(0, m.back+mount)
	}
}

// firstChangedLine returns the first index at which a and b differ — the
// line the mutation landed on. When one is a prefix of the other (a pure
// append or a pure tail truncation) that is the first line past the
// shorter slice, which is where the change begins.
func firstChangedLine(a, b []string) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

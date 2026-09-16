// scroll.go is the Detail panel's content scrolling (contract §5 frame
// note 10, W5 F2/D-3W): `off` counts the content lines hidden above the
// panel; the six scroll bindings always target Detail, stepped and
// clamped by the Detail panel's geometry; and a ui.WheelMsg notch is
// hit-tested against the same panel rectangles View draws — Detail
// scrolls three lines, Ops moves the cursor one stop exactly as j/k,
// the Changeset panel ignores it. The offset resets to 0 at every
// cursor-stop change, the `p` mode toggle and every changeset reload
// (s2-screens.md T06 Scroll).
package review

import (
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui"
)

// ScrollsContent is the shell's Scroller hook (contract §5): the Detail
// panel scrolls, so the ? overlay shows Review's Scroll group.
func (m *Model) ScrollsContent() bool { return true }

// resetScroll puts the Detail offset back to the top. Every cursor-stop
// change calls it — j/k/g/G, the y/n advance, a wheel notch over Ops —
// as do the `p` toggle and every changeset reload, because the content
// under the fold belongs to the stop, the mode and the changeset the
// cursor was on (s2-screens.md T06 Scroll).
func (m *Model) resetScroll() { m.off = 0 }

// scrollPage moves the Detail offset by ±max(1, inner-1) lines — a page,
// whichever direction dir carries (+1 down, -1 up).
func (m *Model) scrollPage(dir int) {
	m.addScrollOff(dir * m.pageStep())
}

// scrollHalf moves the Detail offset by ±max(1, inner/2) lines — half a
// page.
func (m *Model) scrollHalf(dir int) {
	m.addScrollOff(dir * m.halfStep())
}

// pageStep is a page: max(1, inner-1) content lines (contract §5 note
// 10); one line when no size is known yet.
func (m *Model) pageStep() int {
	inner, ok := m.detailInner()
	if !ok {
		return 1
	}
	return max(1, inner-1)
}

// halfStep is half a page: max(1, inner/2).
func (m *Model) halfStep() int {
	inner, ok := m.detailInner()
	if !ok {
		return 1
	}
	return max(1, inner/2)
}

// maxScrollOff is the offset's upper clamp at the pane's last known size:
// every content line but the panel's inner height hidden (contract §5
// note 10). ScrollBottom lands here.
func (m *Model) maxScrollOff() int {
	l, ok := m.paneLayout()
	if !ok {
		return 0
	}
	return m.scrollMax(l)
}

// scrollMax is the upper clamp at one layout: the content built at the
// Detail panel's width, less its inner height.
func (m *Model) scrollMax(l reviewLayout) int {
	_, _, lines := m.detailContent(l.detail.w - 4)
	return max(0, len(lines)-(l.detail.h-2))
}

// addScrollOff moves the offset by n lines, clamped to the valid range at
// the pane's last known size — the clamp at every key (contract §5 note
// 10); the render clamps again (detailPanel).
func (m *Model) addScrollOff(n int) {
	l, ok := m.paneLayout()
	if !ok {
		// No geometry yet — the shell's first WindowSizeMsg has not
		// arrived. Step and clamp below only; the next render clamps above.
		m.off = max(0, m.off+n)
		return
	}
	m.off = clampScroll(m.off+n, m.scrollMax(l))
}

// clampScroll clamps v to [0, max]; a max below zero means the content
// fits the panel and the only valid offset is 0.
func clampScroll(v, max int) int {
	if max < 0 {
		max = 0
	}
	if v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

// paneLayout is the screen's panel rectangles at the pane's last known
// size, and whether a usable size is known at all — before the shell's
// first tea.WindowSizeMsg there is no geometry to scroll by.
func (m *Model) paneLayout() (reviewLayout, bool) {
	if m.paneW < 6 || m.paneH < 2 {
		return reviewLayout{}, false
	}
	return m.layout(m.paneW, m.paneH), true
}

// detailInner is the Detail panel's inner height (its rows minus the two
// borders) at the pane's last known size.
func (m *Model) detailInner() (int, bool) {
	l, ok := m.paneLayout()
	if !ok {
		return 0, false
	}
	return l.detail.h - 2, true
}

// handleWheel is one ui.WheelMsg (contract §5 frame note 7): the shell
// delivers pane-local coordinates and the pane's own W×H, so the hit test
// runs the very layout arithmetic View draws. Over Detail a notch scrolls
// three lines; over Ops it moves the cursor one stop exactly as j/k —
// resetting the scroll with it; over the Changeset panel, and anywhere
// else, it is ignored.
func (m *Model) handleWheel(msg ui.WheelMsg) (ui.Pane, tea.Cmd) {
	if !m.hasChangeset {
		return m, nil
	}
	l := m.layout(msg.W, msg.H)
	switch {
	case l.detail.contains(msg.X, msg.Y):
		m.off = clampScroll(m.off+3*msg.Delta, m.scrollMax(l))
	case l.ops.contains(msg.X, msg.Y):
		switch {
		case msg.Delta > 0:
			m.cursor = clampCursor(m.cursor+1, len(m.stops))
		case msg.Delta < 0:
			m.cursor = clampCursor(m.cursor-1, len(m.stops))
		}
		m.resetScroll()
	}
	return m, nil
}

// cursorWindow is the visible Detail lines' cursor window: the index of
// its first and last line, and false when no marked line is on screen —
// the window scrolled above the fold, or Preview mode, which marks
// nothing. The marks are one contiguous run — the cursor's single hunk
// window, mockgen.draw_lines' unit, which TestReviewCursorSpan holds the
// fixture to — so first and last bound the span drawn as cursor rows.
func cursorWindow(lines []panelLine) (first, last int, ok bool) {
	first, last = -1, -1
	for i, l := range lines {
		if l.cursor {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	return first, last, first >= 0
}

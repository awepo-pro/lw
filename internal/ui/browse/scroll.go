// scroll.go is W5 F2/C36's preview scroll (contract §5 note 10,
// s2-screens.md T07 "Preview scroll"): the offset state, the six scroll
// keys — which always target the preview, j/k staying tree movement — the
// hit-tested ui.WheelMsg, and the ui.Scroller report that makes the `?`
// overlay show the Scroll group while Browse is active.
//
// The offset `off` is the number of rendered preview lines hidden above the
// panel. It is clamped to [0, max(0, len(lines) - inner)] (inner = panel
// height - 2) at every render (view.go's previewSpec, which also remembers
// the geometry the key handlers clamp against between frames) and at every
// key here. The initial offset is 0, so every frozen grid is unchanged.
package browse

import (
	"github.com/awepo-pro/lw/internal/ui"
)

// wheelLines is the content lines one wheel notch scrolls (contract §5
// note 10: one wheel notch moves 3 lines).
const wheelLines = 3

// The Model implements ui.Scroller (contract §5/§7, W5 F2): Browse's
// scrollable content is the preview, so the shell's `?` overlay shows the
// Scroll group while this pane is active.
var _ ui.Scroller = (*Model)(nil)

// ScrollsContent reports that Browse has scrollable content (contract §7's
// amendment): the preview panel.
func (m *Model) ScrollsContent() bool { return true }

// previewPage is one page of preview: max(1, inner-1) content rows
// (contract §5 note 10).
func (m *Model) previewPage() int { return max(1, m.previewInner-1) }

// previewHalf is half a page of preview: max(1, inner/2) content rows.
func (m *Model) previewHalf() int { return max(1, m.previewInner/2) }

// maxPreviewOff is the largest offset the last rendered preview can hide:
// len(lines) - inner, never below 0. Before the first render both are 0, so
// the maximum — and the offset — stay 0.
func (m *Model) maxPreviewOff() int {
	return max(0, m.previewCount-m.previewInner)
}

// clampPreviewOff pulls m.off back into [0, maxPreviewOff()] against the
// last render's geometry (contract §5 note 10: clamped at every key).
func (m *Model) clampPreviewOff() {
	if m.off < 0 {
		m.off = 0
	}
	if maxOff := m.maxPreviewOff(); m.off > maxOff {
		m.off = maxOff
	}
}

// scrollPreview moves the preview offset by delta lines (positive down),
// clamped.
func (m *Model) scrollPreview(delta int) {
	m.off += delta
	m.clampPreviewOff()
}

// handleWheel maps one ui.WheelMsg onto the pane layout View draws
// (s2-screens.md T07): the same treeWidth, the Links panel only at
// W >= 180, the preview taking the rest. Over the preview a notch scrolls
// 3 lines; over Pages it moves the tree cursor one row exactly like j/k;
// over Links it does nothing. The caller drops the message entirely while
// the finder is open.
func (m *Model) handleWheel(msg ui.WheelMsg) {
	tw := treeWidth(msg.W)
	lk := 0
	if msg.W >= linksMinWidth {
		lk = linksWidth
	}
	pw := msg.W - tw - lk

	switch {
	case msg.X < tw:
		m.moveCursor(msg.Delta)
	case pw > 0 && msg.X < tw+pw:
		m.scrollPreview(wheelLines * msg.Delta)
	default:
		// Over Links (or a pane too narrow to have a preview): ignored.
	}
}

// selectedPath is the selected node's path, or "" when the visible list is
// empty — the identity the scroll reset compares.
func (m *Model) selectedPath() string {
	if n := m.selectedNode(); n != nil {
		return n.Path
	}
	return ""
}

// scrollResetOnSelection zeroes the preview offset when the tree cursor's
// node changed from before (s2-screens.md T07: the offset resets to 0
// whenever the selected node changes). Every cursor-moving site — j/k/g/G,
// h moving up to the parent, the finder opening a result, OpenPathMsg —
// runs through here.
func (m *Model) scrollResetOnSelection(before string) {
	if m.selectedPath() != before {
		m.off = 0
	}
}

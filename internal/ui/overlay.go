// overlay.go implements the `?` overlay (contract §5 frame note 4,
// mockgen.keys_overlay): a centred "Keys" panel over a fainted copy of the
// frame, with the active pane's own OverlayHelp() entries on the left and
// the shell's fixed "Everywhere" bindings on the right.
package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"github.com/charmbracelet/x/ansi"
)

// everywhereHelp is the overlay's fixed right-hand column: bindings the
// shell itself owns, true regardless of which pane is active (contract §5
// frame note 4).
var everywhereHelp = []HelpEntry{
	{Key: "tab", Desc: "next screen"},
	{Key: "ctrl+r", Desc: "ask → review"},
	{Key: "?", Desc: "close"},
	{Key: "q/ctrl+c", Desc: "quit"},
}

// overlayBox returns the "Keys" panel's lines (bw×bh, clamped to min(64,
// w-4) × min(15, h-2)) and where it belongs, centred in a w×h frame
// (mockgen.keys_overlay). p may be nil, and may implement neither optional
// interface — a pane with no OverlayHelp leaves the left column just its
// (empty) title, and a pane that is not a Scroller (or reports false)
// leaves the Scroll group's rows blank (contract §5 frame note 4).
func overlayBox(t Theme, keys KeyMap, p Pane, w, h int) (lines []string, bw, bh, x, y int) {
	bw = min(64, w-4)
	bh = min(15, h-2)
	x = (w - bw) / 2
	y = (h - bh) / 2

	var title string
	var entries []HelpEntry
	var scrolls bool
	if p != nil {
		if oh, ok := p.(OverlayHelper); ok {
			title, entries = oh.OverlayHelp()
		}
		if sc, ok := p.(Scroller); ok {
			scrolls = sc.ScrollsContent()
		}
	}

	inner := bw - 4
	n := bh - 2

	rows := make([]*row, n)
	for i := range rows {
		rows[i] = newRow(inner)
	}

	// Row index 1 (content-relative; absolute panel row y+2): the two
	// column titles.
	if 1 < n {
		rows[1].put(1, title, t.Bold)
		rows[1].put(32, "Everywhere", t.Bold)
	}
	// Rows 3.. (absolute y+4..): the active pane's own entries, left column.
	for i, e := range entries {
		ri := 3 + i
		if ri >= n {
			break
		}
		keyStyle, descStyle := t.Bold, t.Muted
		if e.Disabled {
			keyStyle, descStyle = t.Faint, t.Faint
		}
		rows[ri].put(1, e.Key, keyStyle)
		rows[ri].put(9, e.Desc, descStyle)
	}
	// Rows 3.. : the fixed "Everywhere" entries, right column.
	for i, e := range everywhereHelp {
		ri := 3 + i
		if ri >= n {
			break
		}
		rows[ri].put(32, e.Key, t.Bold)
		rows[ri].put(42, e.Desc, t.Muted)
	}
	// Rows 8..11 (absolute y+9..y+12): the Scroll group, right column —
	// shown only while the active pane's content actually scrolls. The key
	// labels come from the up bindings' help, so a hotkeys.toml rebind
	// shows (W5 F2/D-3W).
	if scrolls {
		if 8 < n {
			rows[8].put(32, "Scroll", t.Bold)
		}
		for i, b := range []key.Binding{keys.ScrollPageUp, keys.ScrollHalfUp, keys.ScrollTop} {
			ri := 9 + i
			if ri >= n {
				break
			}
			h := b.Help()
			rows[ri].put(32, h.Key, t.Bold)
			rows[ri].put(42, h.Desc, t.Muted)
		}
	}

	content := make([]string, n)
	for i, r := range rows {
		content[i] = r.render()
	}

	spec := PanelSpec{
		Title:     "Keys",
		Note:      "esc to close",
		Focused:   true,
		Lines:     content,
		CursorRow: -1,
	}
	lines = Panel(t, spec, bw, bh)
	return lines, bw, bh, x, y
}

// compositeOverlay splices box (bw×bh, already styled) into plain — h rows
// already exactly w cells and free of ANSI (contract §5 frame note 4:
// "ansi.Strip, then Faint.Render per row") — at (x, y). Cutting happens on
// the plain text, before any styling is applied, so an escape sequence is
// never split; the surrounding text is then rendered Faint and the box's
// own (already styled) rows are spliced in verbatim.
func compositeOverlay(t Theme, plain []string, box []string, x, y, bw, bh int) []string {
	out := make([]string, len(plain))
	for i, line := range plain {
		if i < y || i >= y+bh {
			out[i] = t.Faint.Render(line)
			continue
		}
		cells := []rune(line)
		left := clampIdx(x, len(cells))
		right := clampIdx(x+bw, len(cells))
		boxRow := ""
		if bi := i - y; bi >= 0 && bi < len(box) {
			boxRow = box[bi]
		}
		out[i] = t.Faint.Render(string(cells[:left])) + boxRow + t.Faint.Render(string(cells[right:]))
	}
	return out
}

func clampIdx(v, max int) int {
	if v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

// stripFrame ansi.Strips every row of a rendered frame, so compositeOverlay
// can cut it at cell positions safely (contract §5 frame note 4).
func stripFrame(frameContent string) []string {
	lines := strings.Split(frameContent, "\n")
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

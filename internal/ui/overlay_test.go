package ui

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// reviewOverlayHelp is a fake OverlayHelper standing in for Review's own
// (mockgen.keys_overlay's `left` list plus the disabled split-hunk row, D9).
type reviewOverlayHelp struct{}

func (reviewOverlayHelp) OverlayHelp() (string, []HelpEntry) {
	return "Review", []HelpEntry{
		{Key: "y", Desc: "accept hunk"},
		{Key: "n", Desc: "drop hunk"},
		{Key: "A", Desc: "accept all"},
		{Key: "X", Desc: "reject changeset"},
		{Key: "C", Desc: "commit"},
		{Key: "p", Desc: "preview / diff"},
		{Key: "j/k", Desc: "down / up"},
		{Key: "g/G", Desc: "top / bottom"},
		{Key: "s", Desc: "split hunk · not built", Disabled: true},
	}
}

// overlayGoldenPane adapts reviewOverlayHelp to a full Pane without being a
// Scroller: the frozen artifact shows the overlay for a pane whose content
// does not scroll, so the Scroll group's rows stay blank (contract §5 frame
// note 4).
type overlayGoldenPane struct{ reviewOverlayHelp }

func (overlayGoldenPane) Init() tea.Cmd                    { return nil }
func (p overlayGoldenPane) Update(tea.Msg) (Pane, tea.Cmd) { return p, nil }
func (overlayGoldenPane) View(w, h int) string             { return "" }
func (overlayGoldenPane) Title() string                    { return "Review" }
func (overlayGoldenPane) Help() []key.Binding              { return nil }

// TestOverlayGolden renders the overlay box on a blank 120×40 frame with
// the Review entries (MASTER §5 T03), against
// testdata/frozen/overlay-review-blank-120x40.txt.
func TestOverlayGolden(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	const w, h = 120, 40
	blank := make([]string, h)
	for i := range blank {
		blank[i] = Pad("", w)
	}

	box, bw, bh, x, y := overlayBox(th, defaultKeyMap(), overlayGoldenPane{}, w, h)
	out := compositeOverlay(th, blank, box, x, y, bw, bh)

	assertPlainMatchesFrozen(t, joinLines(out), "overlay-review-blank-120x40.txt")
}

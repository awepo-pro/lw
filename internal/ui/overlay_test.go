package ui

import "testing"

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

	box, bw, bh, x, y := overlayBox(th, reviewOverlayHelp{}, w, h)
	out := compositeOverlay(th, blank, box, x, y, bw, bh)

	assertPlainMatchesFrozen(t, joinLines(out), "overlay-review-blank-120x40.txt")
}

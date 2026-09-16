package ui

import (
	"strconv"
	"testing"
)

// TestTooSmallGolden covers 72×20 and 120×20 (MASTER §5 T03), against
// testdata/frozen/too-small-*.txt.
func TestTooSmallGolden(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	for _, sz := range []struct{ w, h int }{{72, 20}, {120, 20}} {
		got := joinLines(tooSmallView(th, sz.w, sz.h))
		name := "too-small-" + strconv.Itoa(sz.w) + "x" + strconv.Itoa(sz.h) + ".txt"
		assertPlainMatchesFrozen(t, got, name)
	}
}

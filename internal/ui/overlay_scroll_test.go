// overlay_scroll_test.go pins the ? overlay's Scroll group (W5 F2/D-3W,
// contract §5 frame note 4): shown only while the ACTIVE pane implements
// Scroller and reports true, its box rows equal the regenerated
// keys-120x40 frozen grid's box rows (amendment A-3) cell for cell.
package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// scrollOverlayPane is a Pane standing in for Review as W5b will build it:
// Review's frozen overlay entries, and content that scrolls.
type scrollOverlayPane struct {
	reviewOverlayHelp
}

func (scrollOverlayPane) Init() tea.Cmd { return nil }

func (p scrollOverlayPane) Update(msg tea.Msg) (Pane, tea.Cmd) { return p, nil }

func (scrollOverlayPane) View(w, h int) string { return "" }
func (scrollOverlayPane) Title() string        { return "Review" }
func (scrollOverlayPane) Help() []key.Binding  { return nil }
func (scrollOverlayPane) ScrollsContent() bool { return true }

var (
	_ Pane     = scrollOverlayPane{}
	_ Scroller = scrollOverlayPane{}
)

// readOverlayBoxGrid returns testdata/frozen/keys-overlay-120x40-box.txt's
// 15 rows — the Keys box of the regenerated keys-120x40.txt grid (amendment
// A-3), cut at the box's own geometry (bw=64 at x=28, bh=15 at y=12).
func readOverlayBoxGrid(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "frozen", "keys-overlay-120x40-box.txt"))
	if err != nil {
		t.Fatalf("read keys-overlay-120x40-box.txt: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// overlayBoxAt renders the shell with pane active, opens the ? overlay, and
// returns the box's 15 rows cut out of the plain frame at the geometry a
// 120×40 terminal gives it.
func overlayBoxAt(t *testing.T, pane Pane) []string {
	t.Helper()

	const w, h = 120, 40
	a := newWheelApp(t, pane, w, h)

	m, _ := a.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	a = m.(*App)
	if !a.overlayOpen {
		t.Fatal("setup: the ? overlay did not open")
	}

	plain := ansi.Strip(a.View().Content)
	lines := strings.Split(plain, "\n")
	if len(lines) != h {
		t.Fatalf("the frame is %d lines, want %d", len(lines), h)
	}

	bw, bh := min(64, w-4), min(15, h-2)
	x, y := (w-bw)/2, (h-bh)/2

	box := make([]string, 0, bh)
	for _, line := range lines[y : y+bh] {
		cells := []rune(line)
		if len(cells) != w {
			t.Fatalf("frame line %q is %d cells, want %d", line, len(cells), w)
		}
		box = append(box, string(cells[x:x+bw]))
	}
	return box
}

func TestOverlayScrollGroup(t *testing.T) {
	setConfigDir(t)

	t.Run("scroller_pane_matches_keys_overlay_rows", func(t *testing.T) {
		got := overlayBoxAt(t, scrollOverlayPane{})
		want := readOverlayBoxGrid(t)
		if len(got) != len(want) {
			t.Fatalf("the box is %d rows, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("box row %d:\n got %q\nwant %q", i, got[i], want[i])
			}
		}
	})

	t.Run("non_scroller_pane_has_no_group", func(t *testing.T) {
		// A pane that is not a Scroller keeps the grid's pre-W5 shape: the
		// Scroll group's rows stay blank, however much the box has room.
		got := overlayBoxAt(t, overlayGoldenPane{})
		for i, row := range got {
			if strings.Contains(row, "Scroll") || strings.Contains(row, "pgup/pgdn") {
				t.Errorf("box row %d = %q, want no Scroll group for a non-Scroller pane", i, row)
			}
		}
	})
}

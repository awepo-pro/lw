// wheel_test.go pins the shell's half of W5 F2 (contract §5 frame notes
// 7-8): cell-motion mouse mode, the wheel notch converted to a pane-local
// WheelMsg and delivered to the active pane only, every other mouse message
// ignored, and tea.ColorProfileMsg retheming the shell's theme and
// propagating to every pane.
package ui

import (
	"reflect"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// wheelPane is a minimal Pane that records every message the shell
// delivers to it, and can report itself as a Scroller.
type wheelPane struct {
	scrolls bool
	got     []tea.Msg
}

func (p *wheelPane) ScrollsContent() bool { return p.scrolls }

func (p *wheelPane) Init() tea.Cmd { return nil }

func (p *wheelPane) Update(msg tea.Msg) (Pane, tea.Cmd) {
	p.got = append(p.got, msg)
	return p, nil
}

func (p *wheelPane) View(w, h int) string { return "[wheel]" }
func (p *wheelPane) Title() string        { return "wheel" }
func (p *wheelPane) Help() []key.Binding  { return nil }

var (
	_ Pane     = (*wheelPane)(nil)
	_ Scroller = (*wheelPane)(nil)
)

// newWheelApp builds the shell with pane as the active screen's pane, at
// 120×40 unless the test says otherwise.
func newWheelApp(t *testing.T, pane Pane, w, h int) *App {
	t.Helper()
	a := NewApp(Options{
		Deps:  testDeps(t),
		Panes: map[Screen]Pane{ScreenReview: pane},
		Start: ScreenReview,
	})
	m, _ := a.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m.(*App)
}

func TestWheelRouting(t *testing.T) {
	t.Run("wheel_delivered_pane_local", func(t *testing.T) {
		pane := &wheelPane{}
		a := newWheelApp(t, pane, 120, 40)
		pane.got = nil // drop the setup WindowSizeMsg

		m, _ := a.Update(tea.MouseWheelMsg{X: 5, Y: 10, Button: tea.MouseWheelDown})
		a = m.(*App)

		// Y loses the header row, H the header and footer; Delta +1 is
		// wheel down.
		want := []tea.Msg{WheelMsg{X: 5, Y: 9, W: 120, H: 38, Delta: 1}}
		if !reflect.DeepEqual(pane.got, want) {
			t.Fatalf("pane received %#v, want exactly %#v", pane.got, want)
		}

		pane.got = nil
		m, _ = a.Update(tea.MouseWheelMsg{X: 7, Y: 11, Button: tea.MouseWheelUp})
		a = m.(*App)
		want = []tea.Msg{WheelMsg{X: 7, Y: 10, W: 120, H: 38, Delta: -1}}
		if !reflect.DeepEqual(pane.got, want) {
			t.Fatalf("pane received %#v, want exactly %#v", pane.got, want)
		}
	})

	t.Run("header_footer_overlay_and_too_small_ignored", func(t *testing.T) {
		pane := &wheelPane{}
		a := newWheelApp(t, pane, 120, 40)
		pane.got = nil // drop the setup WindowSizeMsg
		wheel := tea.MouseWheelMsg{X: 5, Y: 10, Button: tea.MouseWheelDown}

		// Row 0 is the shell's header.
		if _, cmd := a.Update(tea.MouseWheelMsg{X: 5, Y: 0, Button: tea.MouseWheelDown}); cmd != nil {
			t.Fatal("a wheel notch on the header row was routed on")
		}
		// Row h-1 (39 at height 40) is the shell's footer.
		if _, cmd := a.Update(tea.MouseWheelMsg{X: 5, Y: 39, Button: tea.MouseWheelDown}); cmd != nil {
			t.Fatal("a wheel notch on the footer row was routed on")
		}
		if len(pane.got) != 0 {
			t.Fatalf("pane received %#v on the shell's own rows, want nothing", pane.got)
		}

		// With the ? overlay open, no wheel notch reaches a pane. (The ?
		// itself delivers a ShellKeyMsg — 008 A-801 — which this test's
		// assertions do not cover; drop it.)
		m, _ := a.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
		a = m.(*App)
		if !a.overlayOpen {
			t.Fatal("setup: the ? overlay did not open")
		}
		pane.got = nil
		if _, cmd := a.Update(wheel); cmd != nil {
			t.Fatal("a wheel notch while the overlay is open was routed on")
		}
		if len(pane.got) != 0 {
			t.Fatalf("pane received %#v while the overlay was open, want nothing", pane.got)
		}

		// Below D11's minimum, the shell refuses everything but quit.
		m, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		a = m.(*App)
		m, _ = a.Update(tea.WindowSizeMsg{Width: 72, Height: 20})
		a = m.(*App)
		if !a.tooSmall() {
			t.Fatal("setup: 72x20 is not below the minimum")
		}
		if _, cmd := a.Update(wheel); cmd != nil {
			t.Fatal("a wheel notch while too small was routed on")
		}
		// Any other mouse message is ignored outright, even at full size.
		m, _ = a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		a = m.(*App)
		if _, cmd := a.Update(tea.MouseClickMsg{X: 5, Y: 10, Button: tea.MouseLeft}); cmd != nil {
			t.Fatal("a mouse click was routed on")
		}
		// A wheel message with a button that is not the vertical wheel.
		if _, cmd := a.Update(tea.MouseWheelMsg{X: 5, Y: 10, Button: tea.MouseWheelLeft}); cmd != nil {
			t.Fatal("a horizontal wheel notch was routed on")
		}
		// The pane must have seen nothing but the two resize broadcasts the
		// subtest itself issued — not one wheel or mouse message. The two
		// ShellKeyMsg from the overlay's ? and esc (008 A-801: the shell
		// reports consumed keys to the current pane) are not wheel traffic
		// and are expected.
		for _, msg := range pane.got {
			switch msg.(type) {
			case tea.WindowSizeMsg, ShellKeyMsg:
				continue
			}
			t.Fatalf("pane received %T on a wheel-ignored path, want nothing but resizes", msg)
		}
	})

	t.Run("view_enables_cell_motion", func(t *testing.T) {
		a := newWheelApp(t, &wheelPane{}, 120, 40)

		if got := a.View().MouseMode; got != tea.MouseModeCellMotion {
			t.Fatalf("View().MouseMode = %v, want tea.MouseModeCellMotion", got)
		}
	})
}

// TestShellColorProfileMsg covers contract §5 frame note 8: on
// tea.ColorProfileMsg the shell re-resolves its theme's cursor tint for the
// reported profile and propagates the message to every pane, so a pane
// holding a theme copy rebuilds it exactly as it does for polarity.
func TestShellColorProfileMsg(t *testing.T) {
	t.Run("ansi256_rethemes_and_propagates", func(t *testing.T) {
		pane := &wheelPane{}
		a := newWheelApp(t, pane, 120, 40)

		m, _ := a.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI256})
		a = m.(*App)

		var got bool
		for _, msg := range pane.got {
			if cp, ok := msg.(tea.ColorProfileMsg); ok && cp.Profile == colorprofile.ANSI256 {
				got = true
			}
		}
		if !got {
			t.Fatalf("pane received %#v, want tea.ColorProfileMsg{ANSI256} among them", pane.got)
		}

		// A Panel drawn with the shell's own theme now tints cursor rows
		// with the ANSI256 grey, not the truecolor hex.
		rows := Panel(a.deps.Theme, PanelSpec{Lines: []string{"row"}, CursorRow: 0}, 24, 3)
		assertCursorRowTint(t, rows[1], 24, "48;5;236")
		if strings.Contains(rows[1], "48;2;") {
			t.Fatal("the shell's theme still tints with a truecolor background after ColorProfileMsg")
		}
	})
}

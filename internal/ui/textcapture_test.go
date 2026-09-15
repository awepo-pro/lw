// textcapture_test.go is C27/D-3Q's regression test (contract §5): while
// the active pane is taking text input (ui.TextCapturer), the shell must
// hand it every printable key instead of matching the global bindings —
// `q` and `?` type into the pane; ctrl+c and tab stay global; a pane that
// does not capture keeps today's order exactly. It drives a real NewApp
// with an instrumented pane, the same way app_test.go does.
package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// capturePane is a minimal Pane that records every keypress the shell
// delivers to it and reports whether it is taking text input. captures
// switches it between the two halves of the contract: a text-taking pane
// (Ask's case) and one that does not capture (Review, Lint, Log, Browse).
type capturePane struct {
	captures bool
	got      []tea.KeyPressMsg
}

func (p *capturePane) CapturesText() bool { return p.captures }

func (p *capturePane) Init() tea.Cmd { return nil }

func (p *capturePane) Update(msg tea.Msg) (Pane, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		p.got = append(p.got, k)
	}
	return p, nil
}

func (p *capturePane) View(w, h int) string { return "[capture]" }
func (p *capturePane) Title() string        { return "capture" }
func (p *capturePane) Help() []key.Binding  { return nil }

var (
	_ Pane         = (*capturePane)(nil)
	_ TextCapturer = (*capturePane)(nil)
)

// newCaptureApp builds the shell with pane as the active screen's pane, at
// a size above D11's minimum. Ask is the start screen: it is the screen the
// bug loses questions on.
func newCaptureApp(t *testing.T, captures bool) (*App, *capturePane) {
	t.Helper()
	pane := &capturePane{captures: captures}
	a := NewApp(Options{
		Deps:  testDeps(t),
		Panes: map[Screen]Pane{ScreenAsk: pane},
		Start: ScreenAsk,
	})
	m, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m.(*App), pane
}

// assertQuit runs cmd and fails unless it is tea.Quit — the observable a
// quit request leaves behind (TestQuitKeyReturnsTeaQuit's pattern).
func assertQuit(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("Update returned a nil Cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("Update command produced %T, want tea.QuitMsg", cmd)
	}
}

func TestTextCapturerKeyRouting(t *testing.T) {
	t.Run("q_types_into_capturing_pane", func(t *testing.T) {
		a, pane := newCaptureApp(t, true)

		m, cmd := a.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
		a = m.(*App)

		if len(pane.got) != 1 || pane.got[0].String() != "q" {
			t.Fatalf("pane received %v, want exactly the q keypress — the typed question is lost", pane.got)
		}
		if cmd != nil {
			t.Fatalf("Update(q) returned a Cmd (%v), want nil — typing must not quit the program", cmd)
		}
		if a.quitting {
			t.Fatal("the shell is quitting after q was typed into the pane")
		}
	})

	t.Run("question_mark_types_into_capturing_pane", func(t *testing.T) {
		a, pane := newCaptureApp(t, true)

		m, cmd := a.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
		a = m.(*App)

		if len(pane.got) != 1 || pane.got[0].String() != "?" {
			t.Fatalf("pane received %v, want exactly the ? keypress", pane.got)
		}
		if a.overlayOpen {
			t.Fatal("the keys overlay opened on ? while the pane was taking text")
		}
		if strings.Contains(a.View().Content, "esc to close") {
			t.Fatal("the frame shows the keys overlay")
		}
		if cmd != nil {
			t.Fatalf("Update(?) returned a Cmd (%v), want nil", cmd)
		}
	})

	t.Run("ctrl_c_quits_while_capturing", func(t *testing.T) {
		a, pane := newCaptureApp(t, true)

		m, cmd := a.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'})
		a = m.(*App)

		assertQuit(t, cmd)
		if !a.quitting {
			t.Fatal("quitting flag not set after ctrl+c")
		}
		if len(pane.got) != 0 {
			t.Fatalf("pane received %v, want none — ctrl+c stays a global binding", pane.got)
		}
	})

	t.Run("tab_switches_while_capturing", func(t *testing.T) {
		a, pane := newCaptureApp(t, true)

		m, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		a = m.(*App)

		// screenOrder is Review Ask Lint Log Browse; Ask is active, so tab
		// must land on Lint — the shell's screen, not the pane's input.
		if got := a.order[a.cur]; got != ScreenLint {
			t.Fatalf("active screen after tab = %v, want ScreenLint", got)
		}
		if cmd != nil {
			t.Fatalf("Update(tab) returned a Cmd (%v), want nil", cmd)
		}
		if len(pane.got) != 0 {
			t.Fatalf("pane received %v, want none — tab stays a global binding", pane.got)
		}
	})

	t.Run("q_quits_non_capturing_pane", func(t *testing.T) {
		a, pane := newCaptureApp(t, false)

		m, cmd := a.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
		a = m.(*App)

		assertQuit(t, cmd)
		if !a.quitting {
			t.Fatal("quitting flag not set after q on a non-capturing pane")
		}
		if len(pane.got) != 0 {
			t.Fatalf("pane received %v, want none — the shell consumed q as Quit", pane.got)
		}
	})

	t.Run("question_mark_opens_help_non_capturing_pane", func(t *testing.T) {
		a, pane := newCaptureApp(t, false)

		m, cmd := a.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
		a = m.(*App)

		if !a.overlayOpen {
			t.Fatal("the keys overlay did not open on ? over a non-capturing pane")
		}
		if !strings.Contains(a.View().Content, "esc to close") {
			t.Fatal("overlayOpen is set but the frame shows no keys overlay")
		}
		if cmd != nil {
			t.Fatalf("Update(?) returned a Cmd (%v), want nil", cmd)
		}
		if len(pane.got) != 0 {
			t.Fatalf("pane received %v, want none — the shell consumed ? as Help", pane.got)
		}
	})
}

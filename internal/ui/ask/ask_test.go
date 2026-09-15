// ask_test.go covers the pane's own surface away from the stream pump: the
// ctrl+r screen switch, the tool-call expand toggle, the input box's local
// echo, the theme-copy rule and View's never-panic contract at extreme
// sizes.
package ask

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestCtrlRSwitchesToReview is s4-tui.md S4-T6 pinned item 3.
func TestCtrlRSwitchesToReview(t *testing.T) {
	d := newTestDeps(t)
	pane := New(d)

	_, cmd := pane.Update(specialKey('r', tea.ModCtrl))
	if cmd == nil {
		t.Fatalf("ctrl+r produced no command")
	}
	msg := cmd()
	sw, ok := msg.(ui.SwitchScreenMsg)
	if !ok {
		t.Fatalf("ctrl+r command produced %#v (%T), want ui.SwitchScreenMsg", msg, msg)
	}
	if sw.To != ui.ScreenReview {
		t.Fatalf("SwitchScreenMsg.To = %v, want ui.ScreenReview", sw.To)
	}
}

// TestToolCallExpandToggle proves the collapsed/expanded contract: a
// clipped preview (with the error visibly marked) collapsed, the full args
// and result on expand via enter, and back on a second enter. The 003
// frozen layout changed the shapes this asserts on: a collapsed row clips
// its result to the mockup's W = min(content width, 100) with an ellipsis
// (s2-screens.md T08, mockgen.ask_conversation's clip_cells), and expanding
// needs the call selected first (↑/↓), because the selection no longer
// follows the streaming call.
func TestToolCallExpandToggle(t *testing.T) {
	d := newTestDeps(t)
	m := New(d).(*Model)

	content := strings.Repeat("result-detail ", 10)
	m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"kv cache"}`})
	m.applyEvent(agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: content, IsError: true})

	if m.entries[len(m.entries)-1].tool.expanded {
		t.Fatalf("tool call starts expanded")
	}
	// A tool call arrives unselected (state.go): the frozen
	// ask-conversation grids show a finished transcript with no cursor
	// gutter, so a selection exists only where the curator put it.
	if m.selected != -1 {
		t.Fatalf("tool call auto-selected itself (selected = %d), want -1", m.selected)
	}

	// A generous width: the assertion is about the collapsed row's clip
	// *convention*, not about squeezing it into an arbitrarily narrow line
	// — View's own "fits w" contract at small widths is
	// TestViewNeverPanicsAtExtremeSizes's job.
	const w = 200
	collapsed := m.View(w, 10)
	if !strings.Contains(collapsed, "✗") {
		t.Fatalf("collapsed view does not mark the error result:\n%s", collapsed)
	}
	// The result clips to the collapsed row's W: 4 of the 10 occurrences
	// fit before the ellipsis, so the full result is not on screen.
	if got := strings.Count(collapsed, "result-detail"); got >= 10 {
		t.Fatalf("collapsed view shows the whole result (%d of 10 occurrences):\n%s", got, collapsed)
	}
	if !strings.Contains(collapsed, "…") {
		t.Fatalf("collapsed view does not clip the result with an ellipsis:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "    "+`{"q":"kv cache"}`) {
		t.Fatalf("collapsed view already shows the expanded args block:\n%s", collapsed)
	}

	// Select the call, then expand it (enter on an empty input box).
	m.moveSelection(1)
	pane, _ := m.Update(specialKey(tea.KeyEnter, 0))
	m = pane.(*Model)
	if !m.entries[len(m.entries)-1].tool.expanded {
		t.Fatalf("enter did not expand the selected tool call")
	}

	expanded := m.View(w, 10)
	if !strings.Contains(expanded, "    "+`{"q":"kv cache"}`) {
		t.Fatalf("expanded view missing the full args block indented 4:\n%s", expanded)
	}
	// The head row keeps its clipped preview; the expanded block below it
	// shows the whole result.
	if got := strings.Count(expanded, "result-detail"); got < 10 {
		t.Fatalf("expanded view shows only %d of 10 result occurrences:\n%s", got, expanded)
	}

	pane, _ = m.Update(specialKey(tea.KeyEnter, 0))
	m = pane.(*Model)
	if m.entries[len(m.entries)-1].tool.expanded {
		t.Fatalf("second enter did not collapse the tool call")
	}
}

// TestInputTypingSubmitAndBackspace exercises the input box's own local
// echo: typing, backspace and submit. Deps.Agent is nil here, so the submit
// degrades (S5-T5): the question is still echoed into the scrollback and a
// visible status line explains that ask is off — the input box never goes
// dead.
func TestInputTypingSubmitAndBackspace(t *testing.T) {
	d := newTestDeps(t)
	var pane ui.Pane = New(d)

	for _, r := range "hi" {
		pane, _ = pane.Update(keyPress(r))
	}
	m := pane.(*Model)
	if m.input != "hi" {
		t.Fatalf("input = %q, want %q", m.input, "hi")
	}

	pane, _ = pane.Update(specialKey(tea.KeyBackspace, 0))
	m = pane.(*Model)
	if m.input != "h" {
		t.Fatalf("input after backspace = %q, want %q", m.input, "h")
	}

	pane, cmd := pane.Update(specialKey(tea.KeyEnter, 0))
	m = pane.(*Model)
	if cmd != nil {
		t.Fatalf("submit with no agent produced a command (%#v), want nil", cmd)
	}
	if m.input != "" {
		t.Fatalf("input after enter = %q, want empty", m.input)
	}
	if len(m.entries) != 2 {
		t.Fatalf("entries = %#v, want a user echo plus a status line", m.entries)
	}
	if m.entries[0].kind != kindUser || m.entries[0].text != "h" {
		t.Fatalf("entries[0] = %#v, want kindUser \"h\"", m.entries[0])
	}
	if m.entries[1].kind != kindStatus {
		t.Fatalf("entries[1] = %#v, want a kindStatus degrade notice", m.entries[1])
	}
	if !strings.Contains(m.View(80, 10), "no agent") {
		t.Fatalf("view does not show the nil-agent status line:\n%s", m.View(80, 10))
	}
}

// TestBackgroundColorMsgRebuildsTheme is backbone §12 C-81: d.Theme is a
// copy, so this screen must rebuild its own on tea.BackgroundColorMsg
// rather than relying on the shell's.
func TestBackgroundColorMsgRebuildsTheme(t *testing.T) {
	d := newTestDeps(t)
	m := New(d).(*Model)

	wasDark := m.theme.IsDark
	newColor := color.Black
	if wasDark {
		newColor = color.White
	}

	pane, _ := m.Update(tea.BackgroundColorMsg{Color: newColor})
	m2 := pane.(*Model)
	if m2.theme.IsDark == wasDark {
		t.Fatalf("theme polarity unchanged after BackgroundColorMsg (still IsDark=%v)", wasDark)
	}
}

// TestViewNeverPanicsAtExtremeSizes drives View across a scripted turn at
// sizes from zero/negative up to generous, per backbone §12: "must not
// panic at small or large sizes".
func TestViewNeverPanicsAtExtremeSizes(t *testing.T) {
	d := newTestDeps(t)
	m := New(d).(*Model)
	m.applyEvent(agent.TextDelta{Text: "hello there, this is a reasonably long line of assistant prose to wrap"})
	m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"a very very very long query string indeed"}`})
	m.applyEvent(agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "some result content", IsError: false})
	m.applyEvent(agent.ErrorEv{})

	sizes := [][2]int{{0, 0}, {-3, -3}, {1, 1}, {200, 50}}
	for _, sz := range sizes {
		_ = m.View(sz[0], sz[1])
	}
}

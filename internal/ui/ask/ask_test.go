package ask

import (
	"errors"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// newTestDeps builds ui.Deps with lw's compiled-in Theme/KeyMap and no
// Engine — the ask screen never reads Deps.Engine or Deps.Agent in this
// subtask (backbone §12, D-CN). XDG_CONFIG_HOME points at an empty temp
// dir so these tests never pick up a real user config, mirroring
// internal/ui/review's own (unexported, different-package) helper.
func newTestDeps(t *testing.T) ui.Deps {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	return ui.Deps{Theme: theme, Keys: keys}
}

// keyPress builds a synthetic tea.KeyPressMsg for a single printable rune
// — the shape internal/ui/review's own tests use (C-83: never via
// tea.Program.Run()).
func keyPress(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// specialKey builds a synthetic tea.KeyPressMsg for a non-printable key or
// a modified one (e.g. enter, backspace, ctrl+r): code and modifier only,
// no Text — matching how a real terminal reports these (backbone §12
// C-80).
func specialKey(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

// runCmd executes cmd, if non-nil, against m, expanding any tea.BatchMsg
// it returns into its constituent commands and feeding every resulting
// message back through m.Update — until nothing is left to run —
// recording every message observed along the way into *seen. Headless
// tests drive Update/View directly (C-83); this is the harness's
// stand-in for what tea.Program's event loop would otherwise do, the same
// shape as internal/ui/review_test.go's own runCmd/feedMsg pair. Driving
// the pump this way — rather than calling applyEvent directly — is what
// proves Listen and its re-arm, not just the event-handling logic behind
// it.
func runCmd(t *testing.T, m ui.Pane, cmd tea.Cmd, seen *[]tea.Msg) ui.Pane {
	t.Helper()
	if cmd == nil {
		return m
	}
	return feedMsg(t, m, cmd(), seen)
}

func feedMsg(t *testing.T, m ui.Pane, msg tea.Msg, seen *[]tea.Msg) ui.Pane {
	t.Helper()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = runCmd(t, m, c, seen)
		}
		return m
	}
	*seen = append(*seen, msg)
	updated, cmd := m.Update(msg)
	return runCmd(t, updated, cmd, seen)
}

// TestRendersScriptedStream is s4-tui.md S4-T6's named verification: a
// scripted fake channel, driven entirely through Listen + Update's re-arm,
// proves deltas accumulate in order into one message, the tool call
// renders as one collapsed line, and StageEv reaches the shell as a
// ui.StageChangedMsg carrying the same fields.
func TestRendersScriptedStream(t *testing.T) {
	d := newTestDeps(t)
	m, ok := New(d).(*Model)
	if !ok {
		t.Fatalf("New did not return *Model")
	}

	ch := make(chan agent.Event, 16)
	ch <- agent.TextDelta{Text: "Hello "}
	ch <- agent.TextDelta{Text: "world"}
	ch <- agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"kv cache"}`}
	ch <- agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "kv-cache: a memoization technique for attention", IsError: false}
	ch <- agent.StageEv{ChangesetID: "cs-abc123", Ops: 2}
	ch <- agent.DoneEv{Reason: "stop", Rounds: 1}
	close(ch)
	m.ch = ch

	var seen []tea.Msg
	pane := runCmd(t, m, Listen(ch), &seen)
	m = pane.(*Model)

	// Deltas accumulate, in arrival order, into one growing message —
	// never one entry per delta.
	var assistant []string
	for _, e := range m.entries {
		if e.kind == kindAssistant {
			assistant = append(assistant, e.text)
		}
	}
	if len(assistant) != 1 || assistant[0] != "Hello world" {
		t.Fatalf("assistant entries = %#v, want exactly one entry \"Hello world\"", assistant)
	}

	// Exactly one collapsed tool-call line: the ToolResEv updates the
	// ToolCallEv's own entry rather than appending a second one.
	var tools []*toolCall
	for _, e := range m.entries {
		if e.kind == kindTool {
			tools = append(tools, e.tool)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("tool entries = %d, want exactly 1", len(tools))
	}
	if tools[0].expanded {
		t.Fatalf("tool entry is expanded by default")
	}
	if !tools[0].resolved || tools[0].isError {
		t.Fatalf("tool entry = %#v, want resolved and not an error", tools[0])
	}

	view := m.View(60, 10)
	toolLines := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "▸") {
			toolLines++
			if !strings.Contains(line, "wiki.search") {
				t.Fatalf("tool line %q missing the tool name", line)
			}
		}
	}
	if toolLines != 1 {
		t.Fatalf("rendered %d lines with a collapsed tool marker, want 1:\n%s", toolLines, view)
	}
	if strings.Contains(view, "args: "+`{"q":"kv cache"}`) {
		t.Fatalf("collapsed view already shows the expanded args block:\n%s", view)
	}

	// StageEv is forwarded as ui.StageChangedMsg, fields unchanged.
	var gotStage *ui.StageChangedMsg
	for _, msg := range seen {
		if sc, ok := msg.(ui.StageChangedMsg); ok {
			sc := sc
			gotStage = &sc
		}
	}
	if gotStage == nil {
		t.Fatalf("no ui.StageChangedMsg observed; seen = %#v", seen)
	}
	if gotStage.ChangesetID != "cs-abc123" || gotStage.Ops != 2 {
		t.Fatalf("StageChangedMsg = %+v, want {cs-abc123 2}", *gotStage)
	}

	// The stream ended, and the whole channel was drained through Listen
	// (not bypassed): the pump's own terminal message was observed
	// somewhere among the batch (StageEv's ui.StageChangedMsg and the
	// re-armed Listen race inside one tea.Batch, same as a real
	// tea.Program — order between the two branches of one batch is not
	// guaranteed) and the pane stopped re-arming.
	var sawClosed bool
	for _, msg := range seen {
		if _, ok := msg.(StreamClosedMsg); ok {
			sawClosed = true
		}
	}
	if !sawClosed {
		t.Fatalf("StreamClosedMsg never observed; seen = %#v", seen)
	}
	if m.ch != nil {
		t.Fatalf("m.ch = %v after StreamClosedMsg, want nil", m.ch)
	}
}

// TestPumpDrainsToStreamClosed isolates the Listen/re-arm mechanics from
// rendering: every EventMsg delivered must carry the scripted events in
// order, and the pump must end on StreamClosedMsg.
func TestPumpDrainsToStreamClosed(t *testing.T) {
	d := newTestDeps(t)
	m := New(d).(*Model)

	ch := make(chan agent.Event, 4)
	ch <- agent.TextDelta{Text: "a"}
	ch <- agent.TextDelta{Text: "b"}
	close(ch)
	m.ch = ch

	var seen []tea.Msg
	pane := runCmd(t, m, Listen(ch), &seen)
	m = pane.(*Model)

	if len(seen) == 0 {
		t.Fatalf("no messages observed")
	}
	last := seen[len(seen)-1]
	if _, ok := last.(StreamClosedMsg); !ok {
		t.Fatalf("last message = %#v (%T), want StreamClosedMsg", last, last)
	}
	if m.ch != nil {
		t.Fatalf("m.ch = %v after StreamClosedMsg, want nil", m.ch)
	}

	var texts []string
	for _, msg := range seen {
		if em, ok := msg.(EventMsg); ok {
			if td, ok := em.Ev.(agent.TextDelta); ok {
				texts = append(texts, td.Text)
			}
		}
	}
	if len(texts) != 2 || texts[0] != "a" || texts[1] != "b" {
		t.Fatalf("delivered deltas = %#v, want [a b] in order", texts)
	}
}

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

// TestErrorEventNeverPanics covers both a populated and a nil Err — the
// goal explicitly calls out ErrorEv{Err: nil} as a case that must not
// panic.
func TestErrorEventNeverPanics(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"withError", errors.New("boom")},
		{"nilError", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDeps(t)
			m := New(d).(*Model)

			cmd := m.applyEvent(agent.ErrorEv{Err: tt.err})
			if cmd != nil {
				cmd()
			}
			view := m.View(40, 10)
			if !strings.Contains(view, "error") {
				t.Fatalf("view does not render the error visibly:\n%s", view)
			}
		})
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

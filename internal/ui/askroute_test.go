// askroute_test.go is package ui_test on purpose (the same shape
// internal/ui/ask's ask_external_test.go argues for): the C-117/D-DA
// routing fix lives in ui.App.Update, but the pane whose starvation it
// fixes — ask — imports ui, so an in-package test could not import the real
// ask pane to observe it. This file builds the real shell, the real ask
// pane and nothing but exported API, drives both headlessly the way the
// Bubble Tea runtime would (Update/View, never Program.Run — C-83), and
// asserts the thing a user would have lost: a turn that keeps streaming
// after the screen switches away from Ask.
package ui_test

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/ask"
)

// driveShell executes cmd against app, expanding any tea.BatchMsg into its
// constituent commands, and feeds every resulting message back through
// app.Update — the exact hand-off the runtime performs, and the point of the
// test: the message has to be *routed* by the shell, not handed to a pane
// directly. maxSteps bounds the recursion so a pump that never settles
// fails the test instead of hanging it.
func driveShell(t *testing.T, app *ui.App, cmd tea.Cmd, seen *[]tea.Msg, maxSteps int) {
	t.Helper()
	if cmd == nil {
		return
	}
	if maxSteps <= 0 {
		t.Fatalf("driveShell: exceeded step budget without the pump settling; seen so far = %#v", *seen)
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			driveShell(t, app, c, seen, maxSteps-1)
		}
		return
	}
	*seen = append(*seen, msg)
	_, next := app.Update(msg)
	driveShell(t, app, next, seen, maxSteps-1)
}

// driveScriptedTurn builds the real shell with the real ask pane off screen
// (Review is active), hands ask's pump a buffered, pre-closed channel
// carrying events — exactly what a finished Agent.Send stream looks like to
// the pane, minus the wall clock — and drives everything the shell returns
// back through the shell. It returns the pane's scrollback and every message
// the shell routed, with a count of how many of those were EventMsg and
// StreamClosedMsg.
func driveScriptedTurn(t *testing.T, events []agent.Event) (view string, eventMsgs, closedMsgs int, onScreenUpdates int) {
	t.Helper()

	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	deps := ui.Deps{Theme: theme, Keys: keys}

	// Buffered and closed up front, so the pump drains deterministically
	// with no goroutine of the test's own.
	ch := make(chan agent.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)

	var askPane ui.Pane = ask.New(deps)
	onScreen := &recordingPane{}
	app := ui.NewApp(ui.Options{
		Deps: deps,
		Panes: map[ui.Screen]ui.Pane{
			ui.ScreenReview: onScreen,
			ui.ScreenAsk:    askPane,
		},
		Start: ui.ScreenReview, // Ask is off screen for the entire test
	})
	if app == nil {
		t.Fatal("NewApp returned nil")
	}
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	if before := askPane.View(80, 20); strings.Contains(before, "alpha") {
		t.Fatalf("ask already shows streamed text before any event:\n%s", before)
	}

	// Hand the channel over through the shell, the way tea.Program delivers
	// any other command's message, then run everything the shell gives back.
	_, cmd := app.Update(ask.StreamMsg{Ch: ch})
	var seen []tea.Msg
	driveShell(t, app, cmd, &seen, 20+10*len(events))

	for _, msg := range seen {
		switch msg.(type) {
		case ask.EventMsg:
			eventMsgs++
		case ask.StreamClosedMsg:
			closedMsgs++
		}
	}
	return askPane.View(80, 20), eventMsgs, closedMsgs, onScreen.updates
}

// TestInactiveAskPaneKeepsPumpingItsStream is the C-117/D-DA regression
// test. Ask's pump reaches the shell as three ordinary tea.Cmd results —
// ask.StreamMsg, ask.EventMsg, ask.StreamClosedMsg — and App.Update used to
// hand those to the active pane only. So the moment a turn was running and
// the user jumped away with ctrl+r, the scrollback froze, the pump was
// never re-armed, and past ask's 64-event buffer Agent.Send blocked with
// the pane stuck turnActive until the TUI restarted.
func TestInactiveAskPaneKeepsPumpingItsStream(t *testing.T) {
	scripted := []agent.Event{
		agent.TextDelta{Text: "alpha "},
		agent.TextDelta{Text: "beta"},
		agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"c117"}`},
		agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "a result"},
		agent.DoneEv{Reason: "stop", Rounds: 1},
	}

	view, events, closed, onScreenUpdates := driveScriptedTurn(t, scripted)

	if events != len(scripted) {
		t.Fatalf("the pump delivered %d of %d scripted events while ask was off screen", events, len(scripted))
	}
	if closed != 1 {
		t.Fatalf("saw %d StreamClosedMsg, want exactly one (the turn must close)", closed)
	}
	if !strings.Contains(view, "alpha beta") {
		t.Fatalf("inactive ask scrollback is missing the streamed text:\n%s", view)
	}
	if !strings.Contains(view, "wiki.search") {
		t.Fatalf("inactive ask scrollback is missing the tool call:\n%s", view)
	}
	if !strings.Contains(view, "done: stop (1 round(s))") {
		t.Fatalf("inactive ask scrollback is missing the turn's DoneEv marker:\n%s", view)
	}

	// The broadcast is a fan-out, not a redirect: the screen the user *was*
	// looking at saw the same messages, and ignored them as it should.
	if onScreenUpdates == 0 {
		t.Fatal("the active pane received no Update at all")
	}
}

// TestInactiveAskPaneDrainsStreamLongerThanTheTurnBuffer is the second half
// of C-117: past ask's 64-event buffer, `Send` blocks and the pane stays
// turnActive until the TUI restarts. A stream longer than that buffer must
// therefore drain completely behind a screen switch — which is also what
// distinguishes the fix from a one-event accident.
func TestInactiveAskPaneDrainsStreamLongerThanTheTurnBuffer(t *testing.T) {
	// 70 streamed deltas: comfortably past ask's 64-event buffer, every one
	// addressed so the scrollback can be counted back.
	events := make([]agent.Event, 0, 70)
	for i := 0; i < 70; i++ {
		events = append(events, agent.TextDelta{Text: fmt.Sprintf("w%d ", i)})
	}

	view, got, closed, _ := driveScriptedTurn(t, events)

	if got != len(events) {
		t.Fatalf("the pump delivered %d of %d events; the stream stalled behind the screen switch", got, len(events))
	}
	if closed != 1 {
		t.Fatalf("saw %d StreamClosedMsg, want exactly one", closed)
	}
	if !strings.Contains(view, "w69") {
		t.Fatalf("scrollback is missing the last streamed event:\n%s", view)
	}
}

// recordingPane is the pane the test keeps on screen. It counts the Updates
// it is offered and ignores every message, as a real screen does with
// another package's types.
type recordingPane struct {
	updates int
}

func (p *recordingPane) Init() tea.Cmd { return nil }

func (p *recordingPane) Update(msg tea.Msg) (ui.Pane, tea.Cmd) {
	p.updates++
	return p, nil
}

func (p *recordingPane) View(w, h int) string { return "" }
func (p *recordingPane) Title() string        { return "Review" }
func (p *recordingPane) Help() []key.Binding  { return nil }

// ask_external_test.go is package ask_test on purpose (S4-T6 repair-1): an
// in-package test can always reach the unexported Model.ch field directly,
// which proves nothing about whether the *exported* surface actually lets
// an outside caller start and sustain a stream. This file drives the pane
// with nothing but ui.Pane, agent.Event and ask's own exported types —
// exactly what the real Bubble Tea runtime, and later S5-T5, have to work
// with.
package ask_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/ask"
)

// drive executes cmd, if non-nil, against p, expanding any tea.BatchMsg it
// returns into its constituent commands and feeding every resulting
// message back through p.Update — recording every non-batch message into
// *seen — until nothing is left to run. maxSteps bounds the recursion so a
// pump that never terminates fails the test instead of hanging it.
func drive(t *testing.T, p ui.Pane, cmd tea.Cmd, seen *[]tea.Msg, maxSteps int) ui.Pane {
	t.Helper()
	if cmd == nil {
		return p
	}
	if maxSteps <= 0 {
		t.Fatalf("drive: exceeded step budget without the pump settling; seen so far = %#v", *seen)
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			p = drive(t, p, c, seen, maxSteps-1)
		}
		return p
	}
	*seen = append(*seen, msg)
	updated, next := p.Update(msg)
	return drive(t, updated, next, seen, maxSteps-1)
}

// TestProbeExternalCallerCanDriveTheStream is the orchestrator's repair-1
// probe: a caller holding nothing but the ui.Pane New returns, and ask's
// exported StreamMsg/EventMsg/StreamClosedMsg/Listen, must be able to
// start and sustain a full scripted turn end to end — the exact shape the
// real Bubble Tea runtime drives a Program with, and the shape S5-T5 will
// use to hand the pane a real Agent.Send channel.
func TestProbeExternalCallerCanDriveTheStream(t *testing.T) {
	scripted := []agent.Event{
		agent.TextDelta{Text: "alpha "},
		agent.TextDelta{Text: "beta "},
		agent.TextDelta{Text: "gamma"},
		agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"probe"}`},
		agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: strings.Repeat("r", 400), IsError: false},
		agent.StageEv{ChangesetID: "cs-probe-6", Ops: 3},
		agent.DoneEv{Reason: "stop", Rounds: 1},
	}

	ch := make(chan agent.Event, len(scripted))
	for _, ev := range scripted {
		ch <- ev
	}
	close(ch)

	var p ui.Pane = ask.New(ui.Deps{WebSearch: true}) // configured: fixtures pin the unchanged UI (012 contract §5)

	// The only start sequence the exported contract offers an outside
	// caller: hand the pane the channel through StreamMsg — the same way
	// tea.Program would deliver any other tea.Msg — then drive whatever
	// tea.Cmd comes back.
	p, cmd := p.Update(ask.StreamMsg{Ch: ch})

	var seen []tea.Msg
	p = drive(t, p, cmd, &seen, 50)

	var gotEvents []agent.Event
	var sawClosed bool
	var gotStage *ui.StageChangedMsg
	for _, msg := range seen {
		switch m := msg.(type) {
		case ask.EventMsg:
			gotEvents = append(gotEvents, m.Ev)
		case ask.StreamClosedMsg:
			sawClosed = true
		case ui.StageChangedMsg:
			sc := m
			gotStage = &sc
		}
	}

	if len(gotEvents) != len(scripted) {
		t.Fatalf("scripted %d events; pane consumed %d; saw StreamClosedMsg=%v\nevents=%#v",
			len(scripted), len(gotEvents), sawClosed, gotEvents)
	}
	for i, want := range scripted {
		if gotEvents[i] != want {
			t.Fatalf("event %d = %#v, want %#v", i, gotEvents[i], want)
		}
	}
	if !sawClosed {
		t.Fatalf("StreamClosedMsg never observed after all %d scripted events", len(scripted))
	}
	if gotStage == nil || gotStage.ChangesetID != "cs-probe-6" || gotStage.Ops != 3 {
		t.Fatalf("ui.StageChangedMsg = %#v, want {ChangesetID:cs-probe-6 Ops:3}", gotStage)
	}

	// The rendering side effects of that same run are independently
	// visible through the exported ui.Pane.View alone.
	view := p.View(80, 20)
	if !strings.Contains(view, "alpha beta gamma") {
		t.Fatalf("view missing the accumulated assistant message:\n%s", view)
	}
	if !strings.Contains(view, "wiki.search") {
		t.Fatalf("view missing the tool call line:\n%s", view)
	}

	// View must still never panic, including right after a real stream
	// (backbone §12: "must not panic at small or large sizes").
	for _, sz := range [][2]int{{0, 0}, {-5, -5}, {1, 1}} {
		_ = p.View(sz[0], sz[1])
	}
}

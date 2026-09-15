// turn_stream_test.go covers the stream side of a live turn: ctrl+r jumps
// to Review mid-turn without disturbing the stream, an already-mounted
// stream survives the same jump, and a failed turn renders as a styled
// error entry rather than panicking.
package ask

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestCtrlRDuringTurnJumpsToReviewAndKeepsTheStream is S5-T5's pinned
// behaviour: ctrl+r works mid-turn — it switches to Review — and the jump
// neither cancels the turn nor disturbs the events already received.
func TestCtrlRDuringTurnJumpsToReviewAndKeepsTheStream(t *testing.T) {
	_, engine, csID := liveVault(t)
	release := make(chan struct{})
	ag := &fakeTurnAgent{
		sessions: agent.NewFileSessions(engine.Vault().Root()),
		release:  release,
		script: []agent.Event{
			agent.TextDelta{Text: "streaming along"},
			agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"x"}`},
			agent.StageEv{ChangesetID: csID, Ops: 1},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "go on")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}

	// Mid-turn (the fake has not been released, so no event has arrived):
	// ctrl+r still jumps to Review.
	pane, jump := m.Update(specialKey('r', tea.ModCtrl))
	m = pane.(*Model)
	if jump == nil {
		t.Fatal("ctrl+r produced no command")
	}
	sw, ok := jump().(ui.SwitchScreenMsg)
	if !ok {
		t.Fatalf("ctrl+r produced %#v, want ui.SwitchScreenMsg", sw)
	}
	if sw.To != ui.ScreenReview {
		t.Fatalf("SwitchScreenMsg.To = %v, want ui.ScreenReview", sw.To)
	}
	if !m.turnActive {
		t.Fatal("ctrl+r ended the turn")
	}
	if m.sessionID != csID {
		t.Fatalf("sessionID = %q after ctrl+r, want %q", m.sessionID, csID)
	}

	// The stream survives the jump: the whole script still arrives, in
	// order, and the turn still ends cleanly.
	close(release)
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	if got := assistantText(m); len(got) != 1 || got[0] != "streaming along" {
		t.Fatalf("assistant entries = %#v, want exactly one \"streaming along\"", got)
	}
	if m.turnActive {
		t.Fatal("turn did not end after the jump")
	}
}

// TestCtrlRLeavesALiveStreamMounted proves the narrower half of the same
// rule against a stream that is already mounted and mid-flight: the jump
// must not clear or replace the pane's channel.
func TestCtrlRLeavesALiveStreamMounted(t *testing.T) {
	d := newTestDeps(t)
	m := New(d).(*Model)

	ch := make(chan agent.Event, 4)
	ch <- agent.TextDelta{Text: "streaming"}

	pane, install := m.Update(StreamMsg{Ch: ch})
	m = pane.(*Model)
	if install == nil {
		t.Fatal("StreamMsg produced no command")
	}

	// Consume exactly one event and stop: pump does not chase the re-arm,
	// so the stream stays mounted while the next event is still queued.
	var pending tea.Cmd
	m, pending = pump(t, m, install)
	if m.ch == nil {
		t.Fatal("StreamMsg did not install the channel")
	}
	if !m.turnActive {
		t.Fatal("a streamed delta did not mark the turn active")
	}

	pane, jump := m.Update(specialKey('r', tea.ModCtrl))
	m = pane.(*Model)
	msg := jump()
	if sw, ok := msg.(ui.SwitchScreenMsg); !ok || sw.To != ui.ScreenReview {
		t.Fatalf("ctrl+r produced %#v, want SwitchScreenMsg{ScreenReview}", msg)
	}
	if m.ch == nil {
		t.Fatal("ctrl+r dropped the mounted stream")
	}

	// And the stream still delivers what is left of it after the jump.
	close(ch)
	m, _ = pump(t, m, pending)
	if got := assistantText(m); len(got) != 1 || got[0] != "streaming" {
		t.Fatalf("assistant entries = %#v, want exactly one \"streaming\"", got)
	}
	if m.ch != nil {
		t.Fatal("the closed stream was never reaped")
	}
}

// TestErrorEventRendersAsAStyledErrorEntry is S5-T5's "render ErrorEv
// visibly": a failed turn ends in a kindError entry — the theme's Bad style,
// not the muted status grey a DoneEv gets — and still closes the turn.
func TestErrorEventRendersAsAStyledErrorEntry(t *testing.T) {
	_, engine, _ := liveVault(t)
	ag := &fakeTurnAgent{
		sessions: agent.NewFileSessions(engine.Vault().Root()),
		script: []agent.Event{
			agent.TextDelta{Text: "partial answer"},
			agent.ErrorEv{Err: context.DeadlineExceeded},
		},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "tell me everything")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	if m.turnActive {
		t.Fatal("ErrorEv did not close the turn")
	}
	got := lastEntry(m)
	if got.kind != kindError {
		t.Fatalf("last entry kind = %v, want kindError", got.kind)
	}
	if !strings.Contains(got.text, "context deadline exceeded") {
		t.Fatalf("error entry = %q, want the underlying error's text", got.text)
	}

	view := m.View(80, 20)
	if !strings.Contains(view, "error: context deadline exceeded") {
		t.Fatalf("view does not render the error visibly:\n%s", view)
	}
	// Theme-aware, literally: the same entry, rendered through the pane's
	// own Bad style (view.go renders the boundary as Bad.Render("error: "
	// + text); the pane's clipped wrap must carry that exact styled run).
	want := m.theme.Bad.Render("error: " + got.text)
	if !strings.Contains(view, want) {
		t.Fatalf("error entry is not rendered in the theme's Bad style:\nwant %q in\n%s", want, view)
	}
}

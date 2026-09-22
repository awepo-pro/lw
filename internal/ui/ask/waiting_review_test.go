// waiting_review_test.go is the 025 T2 fresh-context review's pins for the
// waiting state's boundaries and chain hygiene — the cases the delivered
// waiting_state_test.go does not table: a round whose first byte is TEXT
// (no reasoning, the thinking-off default the red pin cannot see — it
// delivers no events at all), an ORPHAN ToolResEv (roundToolInFlight must
// clear with the event instead of suppressing the row for the rest of the
// turn), F.W6's zero-timer half asserted on the arm sites themselves, and
// the wait chain's single-file rule ACROSS windows — a beat still in
// flight when the pane leaves and re-enters waiting is the one beat the
// next window inherits, never a stack. Every beat is injected; no test
// here sleeps.
package ask

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// TestWaitingTextFirstRound pins the text-first round: with reasoning off
// (the DEFAULT config), the round's first byte is a TextDelta — the row
// must hand over to the streamed answer, both status predicates going
// false together (answering is deliberately still, plan 016 §7), and the
// row must come back at the next round boundary.
func TestWaitingTextFirstRound(t *testing.T) {
	m := newWaitingPane(t, false)
	_, plain := uitest.PaneScreen(m, 80, 22)
	if !strings.Contains(plain, "· sending…") {
		t.Fatalf("precondition failed: no waiting row:\n%s", plain)
	}

	if cmd := m.applyEvent(agent.TextDelta{Text: "straight to the answer"}); cmd != nil {
		t.Fatal("TextDelta produced a command")
	}
	if got := m.mascotState(); got != msIdle {
		t.Fatalf("mascotState after a text-first byte = %d, want msIdle (answering is deliberately still)", got)
	}
	if m.waitingVisible() || m.thinkingVisible() {
		t.Fatal("a text-first round left a status predicate live")
	}
	_, plain = uitest.PaneScreen(m, 80, 22)
	if strings.Contains(plain, "· sending…") {
		t.Fatalf("the sending row survived the answer's first byte:\n%s", plain)
	}
	if strings.Contains(plain, "· thinking… (") {
		t.Fatalf("the thinking line showed with no reasoning this round:\n%s", plain)
	}

	// The round boundary hands the window back: the resolved tool starts a
	// fresh nothing-seen round.
	m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
	m.applyEvent(agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "[]"})
	if got := m.mascotState(); got != msWaiting {
		t.Fatalf("mascotState after the round boundary = %d, want msWaiting", got)
	}
}

// TestWaitingOrphanToolRes pins review point 3: a ToolResEv with no
// matching ToolCallEv — resolveToolCall's malformed-stream path. applyEvent
// clears roundToolInFlight BEFORE the resolve, so the orphan must leave the
// flag down and the waiting row up; a stuck flag would suppress the row for
// the rest of the turn.
func TestWaitingOrphanToolRes(t *testing.T) {
	m := newWaitingPane(t, false)
	if cmd := m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`}); cmd != nil {
		t.Fatal("ToolCallEv produced a command")
	}
	if !m.roundToolInFlight {
		t.Fatal("precondition failed: no tool flagged in flight")
	}

	if cmd := m.applyEvent(agent.ToolResEv{ID: "other", Name: "wiki.search", Content: "stray"}); cmd != nil {
		t.Fatal("the orphan ToolResEv produced a command")
	}
	if m.roundToolInFlight {
		t.Fatal("the orphan ToolResEv left roundToolInFlight stuck up — the row would stay suppressed all turn")
	}
	if !m.turnActive {
		t.Fatal("the orphan result dropped the turn's activity (resolveToolCall marks it)")
	}
	if got := m.mascotState(); got != msWaiting {
		t.Fatalf("mascotState after the orphan result = %d, want msWaiting", got)
	}
	_, plain := uitest.PaneScreen(m, 80, 22)
	if !strings.Contains(plain, "· sending…") {
		t.Fatalf("the waiting row did not survive the orphan result:\n%s", plain)
	}
}

// TestWaitingAnimOffArmsNothing pins F.W6's zero-timer half at the arm
// sites themselves: with the anim switch off (every construction, every
// harness), the submit's Update and the turn's own report must leave both
// wait flags down, and the commands they return are the turn's channel
// work — never a clock.
func TestWaitingAnimOffArmsNothing(t *testing.T) {
	d := newTestDeps(t)
	d.Agent = &uitest.FakeAgent{}
	m := New(d).(*Model)
	m, cmd := typeAndSubmit(t, m, "what is a kv cache?")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	if m.waitArmed || m.waitShut {
		t.Fatal("the anim-off submit moved a wait-chain flag (F.W6)")
	}

	// The turn reports in; the flags stay down and the returned command is
	// Listen's re-arm — proven to be a channel read, not a tick, by
	// closing the channel under it and expecting StreamClosedMsg.
	ch := make(chan agent.Event)
	close(ch)
	pane, next := m.Update(turnStartedMsg{sessionID: "s1", ch: ch})
	m = pane.(*Model)
	if m.waitArmed || m.waitShut {
		t.Fatal("turnStartedMsg armed the wait chain with anim off (F.W6)")
	}
	if next == nil {
		t.Fatal("turnStartedMsg produced no re-arm — the stream channel would go unread")
	}
	if msg := next(); msg != (StreamClosedMsg{}) {
		t.Fatalf("the anim-off arm delivered %T, want the channel's StreamClosedMsg", msg)
	}
	if _, cmd := m.Update(StreamClosedMsg{}); cmd != nil {
		t.Fatal("StreamClosedMsg armed a timer with anim off (F.W6)")
	}
	if m.waitArmed || m.waitShut {
		t.Fatal("the cut left a wait flag up with anim off (F.W6)")
	}
}

// TestWaitingChainStaysSingleFileAcrossWindows pins the single-file rule
// across window boundaries: a beat still in flight when the pane leaves
// waiting must be the one beat the next window inherits — animArm defers
// to it (the stale hold's flag is still up), its handler revives or dies,
// and no window ever stacks a second beat beside a live one.
func TestWaitingChainStaysSingleFileAcrossWindows(t *testing.T) {
	m := New(newTestDeps(t)).(*Model)
	m.anim = true
	m.echoUser("q")
	m.turnActive = true // as beginTurn sets it

	if cmd := m.animArm(); cmd == nil {
		t.Fatal("the waiting window armed no wait tick (F.W3)")
	}
	if cmd := m.animArm(); cmd != nil {
		t.Fatal("animArm stacked a second wait tick (F.W3/F.A3)")
	}

	// The beat lands mid-window: shut, one blinkHold in flight.
	if _, cmd := m.Update(waitTickMsg{}); cmd == nil {
		t.Fatal("the wait tick armed no hold (F.W3)")
	}

	// The pane leaves waiting with the hold still flying — a fast round:
	// reasoning, tool call, result, all inside one blinkHold. The stale
	// hold must be the next window's beat: animArm defers, never stacks.
	m.applyEvent(agent.ReasoningDelta{Text: "hmm"})
	m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
	m.applyEvent(agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "[]"})
	if !m.waitingVisible() {
		t.Fatal("precondition failed: the pane is not waiting again")
	}
	if cmd := m.animArm(); cmd != nil {
		t.Fatal("animArm stacked a fresh tick beside the stale hold (single-file)")
	}

	// The stale hold lands: it reopens and hands the window exactly one
	// fresh beat.
	if _, cmd := m.Update(waitOpenMsg{}); cmd == nil {
		t.Fatal("the stale hold did not revive the chain for the new window")
	}
	if cmd := m.animArm(); cmd != nil {
		t.Fatal("the revived chain accepted a second arm (single-file)")
	}

	// And a beat that lands after the window closed dies flagless: the
	// next window arms fresh, once.
	m.applyEvent(agent.ReasoningDelta{Text: "more"})
	if _, cmd := m.Update(waitTickMsg{}); cmd != nil {
		t.Fatal("a wait tick re-armed after the window closed (F.W5)")
	}
	if m.waitShut {
		t.Fatal("the dying tick left the pose shut into the thinking phase")
	}
	m.applyEvent(agent.ToolCallEv{ID: "t2", Name: "wiki.search", Args: `{}`})
	m.applyEvent(agent.ToolResEv{ID: "t2", Name: "wiki.search", Content: "[]"})
	if cmd := m.animArm(); cmd == nil {
		t.Fatal("the fresh window armed no wait tick (F.W3)")
	}
	if cmd := m.animArm(); cmd != nil {
		t.Fatal("the fresh window stacked a second tick (single-file)")
	}
}

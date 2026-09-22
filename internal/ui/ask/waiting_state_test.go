// waiting_state_test.go is workflow 025 T2's own state-level pin for the
// waiting window, complementing mascot_waiting_test.go's frozen render
// contract: it pins the state table (F.W1 — waitingVisible's exact
// predicate through the real flag writes), the mount and its replacement
// by 022's thinking line (F.W4), the tool-round exclusion, the
// StreamClosedMsg drop (the watch-item 023 carried), and the anim-off
// halves of F.W2/F.W3/F.W5 — the row is state and renders motionless, the
// chain behind the pose is its own single-file tick chain. Every beat is
// an injected msg or a real event fold; no test here sleeps.
package ask

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// newWaitingPane submits one question through the real key path with a
// non-nil agent and no engine — the red pin's shape, where beginTurn runs
// synchronously and the startTurn cmd it returns is never run, so no
// stream event (not even turnStartedMsg) ever reaches the pane.
func newWaitingPane(t *testing.T, anim bool) *Model {
	t.Helper()
	d := newTestDeps(t)
	d.Agent = &uitest.FakeAgent{}
	m := New(d).(*Model)
	if anim {
		m.anim = true
	}
	m, _ = typeAndSubmit(t, m, "what is a kv cache?")
	if !m.turnActive {
		t.Fatal("submit did not mark the turn active")
	}
	return m
}

// TestWaitingMountsOnSubmit pins F.W3's no-gate rule at the state level:
// the very Update that takes the submit is the one the waiting state
// mounts on — no delay, no threshold, no stream event needed first — and
// the wait chain arms on that same Update when the anim is on.
func TestWaitingMountsOnSubmit(t *testing.T) {
	m := newWaitingPane(t, false)
	if got := m.mascotState(); got != msWaiting {
		t.Fatalf("mascotState after submit = %d, want msWaiting (F.W1)", got)
	}
	_, plain := uitest.PaneScreen(m, 80, 22)
	if !strings.Contains(plain, "· sending…") {
		t.Fatalf("the waiting row is not mounted on the submit's own Update:\n%s", plain)
	}

	// The anim-on twin: the enter path's arm ran beside the submit —
	// waitArmed is set synchronously, though its tick is never run here.
	m = newWaitingPane(t, true)
	if !m.waitArmed {
		t.Fatal("the submit's Update armed no wait chain (F.W3)")
	}
}

// TestWaitingReplacedNotStacked pins F.W4's replacement rule: the first
// ReasoningDelta swaps `· sending…` for 022's `· thinking… (` in the same
// tail position — both never show together.
func TestWaitingReplacedNotStacked(t *testing.T) {
	m := newWaitingPane(t, false)
	_, plain := uitest.PaneScreen(m, 80, 22)
	if !strings.Contains(plain, "· sending…") {
		t.Fatalf("the waiting row vanished before any event:\n%s", plain)
	}
	if strings.Contains(plain, "· thinking… (") {
		t.Fatalf("the thinking line showed before any reasoning arrived:\n%s", plain)
	}

	if cmd := m.applyEvent(agent.ReasoningDelta{Text: strings.Repeat("hmm", 100)}); cmd != nil {
		t.Fatal("ReasoningDelta produced a command")
	}
	if got := m.mascotState(); got != msThinking {
		t.Fatalf("mascotState after the first reasoning = %d, want msThinking", got)
	}
	_, plain = uitest.PaneScreen(m, 80, 22)
	if !strings.Contains(plain, "· thinking… (300 chars)") {
		t.Fatalf("the 022 thinking line did not replace the waiting row:\n%s", plain)
	}
	if strings.Contains(plain, "· sending…") {
		t.Fatalf("the waiting row stacked beside the thinking line:\n%s", plain)
	}
}

// TestWaitingExcludesToolRound pins F.W1's roundToolInFlight leg: a tool
// executing is NOT the waiting state — tool-round motion is workflow
// 024's question — and the resolved tool hands the round back to it.
func TestWaitingExcludesToolRound(t *testing.T) {
	m := newWaitingPane(t, false)
	if cmd := m.applyEvent(agent.TextDelta{Text: "let me look"}); cmd != nil {
		t.Fatal("TextDelta produced a command")
	}
	if cmd := m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`}); cmd != nil {
		t.Fatal("ToolCallEv produced a command")
	}
	if !m.roundToolInFlight {
		t.Fatal("ToolCallEv left no tool in flight (F.W1)")
	}
	if got := m.mascotState(); got != msIdle {
		t.Fatalf("mascotState with the tool in flight = %d, want msIdle (F.W1)", got)
	}
	_, plain := uitest.PaneScreen(m, 80, 22)
	if strings.Contains(plain, "· sending…") {
		t.Fatalf("the waiting row showed while a tool was executing:\n%s", plain)
	}

	if cmd := m.applyEvent(agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "[]"}); cmd != nil {
		t.Fatal("ToolResEv produced a command")
	}
	if m.roundToolInFlight {
		t.Fatal("ToolResEv left the tool flagged in flight (F.W1)")
	}
	if got := m.mascotState(); got != msWaiting {
		t.Fatalf("mascotState after the resolved tool = %d, want msWaiting (F.W1)", got)
	}
	_, plain = uitest.PaneScreen(m, 80, 22)
	if !strings.Contains(plain, "· sending…") {
		t.Fatalf("the waiting row did not return between rounds:\n%s", plain)
	}
}

// TestStreamCutDropsWaitingPose pins the watch-item 023 carried: a stream
// cut mid-round (StreamClosedMsg) drops the waiting pose — turnActive
// going down is the whole mechanism, and the pane lands as an ordinary
// idle pane, row gone.
func TestStreamCutDropsWaitingPose(t *testing.T) {
	m := newWaitingPane(t, false)
	_, plain := uitest.PaneScreen(m, 80, 22)
	if !strings.Contains(plain, "· sending…") {
		t.Fatalf("precondition failed: no waiting row before the cut:\n%s", plain)
	}

	if _, cmd := m.Update(StreamClosedMsg{}); cmd != nil {
		t.Fatal("StreamClosedMsg produced a command with anim off")
	}
	if m.turnActive {
		t.Fatal("the cut left the turn active")
	}
	if got := m.mascotState(); got != msIdle {
		t.Fatalf("mascotState after the cut = %d, want msIdle", got)
	}
	_, plain = uitest.PaneScreen(m, 80, 22)
	if strings.Contains(plain, "· sending…") {
		t.Fatalf("the waiting pose survived a mid-round stream cut:\n%s", plain)
	}
}

// TestWaitingAnimOffInert pins F.W6's halves for the new state: the
// waiting ROW renders with anim off — it is state, not motion — while the
// BLINK is inert: no flag moves, no chain re-arms, and the compact art
// holds the frozen idle frame byte for byte.
func TestWaitingAnimOffInert(t *testing.T) {
	m := newWaitingPane(t, false) // every harness runs motionless
	_, plain := uitest.PaneScreen(m, 80, 22)
	if !strings.Contains(plain, "· sending…") {
		t.Fatalf("the waiting row is anim-gated; it must not be:\n%s", plain)
	}

	if _, cmd := m.Update(waitTickMsg{}); cmd != nil {
		t.Fatal("a wait tick re-armed with anim off (F.A6/F.W6)")
	}
	if m.waitShut {
		t.Fatal("a wait tick shut the pose with anim off (F.A6/F.W6)")
	}
	if _, cmd := m.Update(waitOpenMsg{}); cmd != nil {
		t.Fatal("a wait open re-armed with anim off (F.A6/F.W6)")
	}
	if got := m.mascotPose(msWaiting, false); got != frameIdle {
		t.Fatalf("waiting pose with anim off = %d, want the frozen idle frame (F.W2)", got)
	}
	// The compact art on the row is the idle frame's own bytes either way.
	if got, want := ansi.Strip(m.renderMascotCompact()), "██▀██▀█ "; got != want {
		t.Fatalf("waiting compact = %q, want the frozen idle frame %q (F.W2)", got, want)
	}
}

// TestWaitingBlinkAlternatesCompact pins F.W2's alternation: the wait
// chain moves the compact form — the frame the sending row renders —
// between the frozen idle and blink frames, body rows and the full form's
// welcome untouched, and the idle blink's own eyesShut never leaks into
// the wait pose.
func TestWaitingBlinkAlternatesCompact(t *testing.T) {
	m := newWaitingPane(t, true)
	// The idle blink's shut flag, forced: it must not move the wait pose.
	m.eyesShut = true
	if got := m.mascotPose(msWaiting, false); got != frameIdle {
		t.Fatalf("eyesShut leaked into the wait pose = %d, want frameIdle (F.W2)", got)
	}
	if _, cmd := m.Update(waitTickMsg{}); cmd == nil {
		t.Fatal("the wait tick armed no hold (F.W3)")
	}
	if got := m.mascotPose(msWaiting, false); got != frameBlink {
		t.Fatalf("waiting pose mid-hold = %d, want frameBlink (F.W2)", got)
	}
	// The compact art the row renders IS the blink frame's bytes now.
	row := m.mascotStatusRow()
	if !strings.Contains(row, "███████ ") {
		t.Fatalf("the sending row's art did not blink: %q (F.W2)", row)
	}
	if _, cmd := m.Update(waitOpenMsg{}); cmd == nil {
		t.Fatal("the wait open re-armed nothing (F.W3)")
	}
	if got := m.mascotPose(msWaiting, false); got != frameIdle {
		t.Fatalf("waiting pose after the hold = %d, want frameIdle (F.W2)", got)
	}
	// The full form is not part of the waiting state's motion (F.W2: the
	// state renders the COMPACT form only): its rows stay the frozen idle
	// art whatever the wait pose — the rise keeps meaning thinking.
	for i, want := range mascotFull[frameIdle] {
		if got := ansi.Strip(m.renderMascotFull()[i]); got != want {
			t.Fatalf("full form row %d = %q, want the frozen idle row %q (F.W2)", i, got, want)
		}
	}
}

// TestWaitingRowFollowsTail pins the scroll interaction the row introduces:
// while the pane follows the tail the row rides at the bottom of the
// conversation; scrolled up, the row is tail chrome hidden below the
// window and `↓ N newer` counts it — the accounting
// (mutateEntries' mount split) keeps the visible rows still either way.
func TestWaitingRowFollowsTail(t *testing.T) {
	m := newWaitingPane(t, false)
	m.vw, m.vh = 80, 22
	inner := m.transcriptInner()

	// Fill enough conversation to make scrolling possible, then scroll up.
	for i := 0; i < 30; i++ {
		m.entries = append(m.entries, entry{kind: kindAssistant, text: "filler line"})
	}
	before := m.transcriptLines()
	if len(before) <= inner {
		t.Fatal("precondition failed: nothing to scroll")
	}
	m.scrollBy(pageStep(inner))
	if m.back <= 0 {
		t.Fatal("precondition failed: the pane did not scroll up")
	}

	// The row is part of the counted lines; it is hidden below the window
	// while scrolled, and back absorbed it at the mutation that mounted it
	// (the submit ran with back == 0, so nothing to absorb there — pin the
	// accounting directly: a follow-tail pane keeps back 0; a scrolled
	// pane hides the row under an honest count).
	if n := m.mountedTailLines(); n != 1 {
		t.Fatalf("mountedTailLines while waiting = %d, want 1", n)
	}
	if got := len(m.transcriptLines()); got != len(before) {
		t.Fatalf("the mount moved the line count across a scroll: %d -> %d", len(before), got)
	}
}

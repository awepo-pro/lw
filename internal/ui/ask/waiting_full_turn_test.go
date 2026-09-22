// waiting_full_turn_test.go is the 025 tier-2 adversarial pass's pins. The
// red pin (mascot_waiting_test.go) and the state-level pins
// (waiting_state_test.go, waiting_review_test.go) all stop at windows the
// SETUP never leaves: the red pin delivers no events at all, and the
// state-level pins drive applyEvent, which bypasses the arm sites. What
// none of them express is the WHOLE TURN — the second waiting window
// across a real round boundary (the exact shape whose sibling killed 023's
// fix), the one-head rule at every frame of it, the ctrl+t view's share of
// that rule, and the chain hygiene of a cut taken mid-wait. Every beat
// here is an injected msg or a real EventMsg through Update — the same
// path the runtime drives — so the arm sites run for real. No test here
// sleeps.
package ask

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// feedEvent drives one agent event through the pane's real Update — the
// EventMsg path, so applyEvent AND animArm both run, exactly as the
// runtime delivers a stream event.
func feedEvent(t *testing.T, m *Model, ev agent.Event) *Model {
	t.Helper()
	pane, _ := m.Update(ui.EventMsg{Ev: ev})
	return pane.(*Model)
}

// startWaitingTurn submits through the real key path and then delivers the
// turnStartedMsg the runtime would — the submit's own command batch is
// deliberately never run (it carries a 1.2s tea.Tick), so the turn's
// channel is a local one the test owns. The pane is left in the first
// waiting window with the wait chain armed, ready for scripted rounds.
func startWaitingTurn(t *testing.T, m *Model, question string) *Model {
	t.Helper()
	m, cmd := typeAndSubmit(t, m, question)
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	if !m.waitArmed {
		t.Fatal("the submit armed no wait chain (F.W3)")
	}
	ch := make(chan agent.Event, 8)
	pane, _ := m.Update(turnStartedMsg{sessionID: "cs-tier2", ch: ch})
	m = pane.(*Model)
	if !m.waitingVisible() {
		t.Fatal("the turn report left the waiting window")
	}
	return m
}

// screenOf renders the pane at the package's canonical checkpoint size.
func screenOf(t *testing.T, m *Model) string {
	t.Helper()
	_, plain := uitest.PaneScreen(m, 80, 22)
	return plain
}

// busyHead reports which slot holds the compact form: "transcript" (a
// status row carries it), "footer" (the morsel), or "" (neither — a lie
// while a turn runs). One head per busy state (A-025-3).
func busyHead(t *testing.T, m *Model) string {
	t.Helper()
	screen := screenOf(t, m)
	hasRow := strings.Contains(screen, "· sending…") || strings.Contains(screen, "· thinking… (")
	if text, _ := m.FooterPrefix(); text != "" {
		if hasRow {
			t.Fatalf("TWO heads for one busy state: morsel %q AND a status row:\n%s", text, screen)
		}
		return "footer"
	}
	if hasRow {
		return "transcript"
	}
	return ""
}

// TestWaitingFullTurnOneHeadPerBusyState drives a full two-round turn —
// waiting → thinking → tool → WAITING AGAIN → thinking → answering →
// done — and pins the pane's head and rows at every frame. The second
// waiting window is the point: the red pin's setup (no events) can never
// reach it, and 023's shipped defect lived exactly one boundary past its
// own pin's reach.
func TestWaitingFullTurnOneHeadPerBusyState(t *testing.T) {
	d := newTestDeps(t)
	d.Agent = &uitest.FakeAgent{}
	m := New(d).(*Model)
	m.anim = true
	m = startWaitingTurn(t, m, "two round question")

	// Frame 1 — the first waiting window: row in the transcript, footer
	// withdrawn, exactly one head.
	if head := busyHead(t, m); head != "transcript" {
		t.Fatalf("first window: head = %q, want transcript", head)
	}

	// Frame 2 — the first reasoning byte swaps the row, never stacks.
	m = feedEvent(t, m, agent.ReasoningDelta{Text: "thinking"})
	if got := m.mascotState(); got != msThinking {
		t.Fatalf("after reasoning = %d, want msThinking", got)
	}
	if head := busyHead(t, m); head != "transcript" {
		t.Fatalf("thinking frame: head = %q, want transcript (the rise's row)", head)
	}

	// Frame 3 — the tool executes: no status row by design (F.W1), so the
	// head must fall back to the footer slot.
	m = feedEvent(t, m, agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
	if !m.roundToolInFlight {
		t.Fatal("the tool call left nothing in flight")
	}
	if head := busyHead(t, m); head != "footer" {
		t.Fatalf("tool-in-flight frame: head = %q, want footer (the row's deliberate absence)", head)
	}

	// Frame 4 — the result lands: the SECOND waiting window mounts the row
	// again and withdraws the morsel again.
	m = feedEvent(t, m, agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "[]"})
	if got := m.mascotState(); got != msWaiting {
		t.Fatalf("the round boundary did not re-enter waiting = %d, want msWaiting", got)
	}
	if head := busyHead(t, m); head != "transcript" {
		t.Fatalf("second window: head = %q, want transcript", head)
	}

	// Frames 5–7 — round 2 thinks, answers, ends: no frame carries two
	// heads (busyHead fails the test on that), and the morsel comes back
	// the moment no status row holds it.
	m = feedEvent(t, m, agent.ReasoningDelta{Text: "more"})
	m = feedEvent(t, m, agent.TextDelta{Text: "the answer"})
	if got := m.mascotState(); got != msIdle {
		t.Fatalf("answering = %d, want the deliberately still msIdle", got)
	}
	if head := busyHead(t, m); head != "footer" {
		t.Fatalf("answering frame: head = %q, want footer", head)
	}
	m = feedEvent(t, m, agent.DoneEv{Reason: "stop", Rounds: 2})
	if head := busyHead(t, m); head != "footer" {
		t.Fatalf("done frame: head = %q, want footer — the morsel must come back", head)
	}
	if got := lastEntry(m); got.kind != kindStatus || got.text != "done · 2 rounds" {
		t.Fatalf("the turn's boundary line = %+v", got)
	}
}

// TestWaitingSecondWindowRearmsTheChain is sequence 1 sharpened to the
// 023 failure shape: the wait chain must DIE when round 1 leaves the
// window and RE-ARM, fresh, when round 2's window opens — a chain that
// died silently at the first boundary would leave every later window
// motionless, which no setup that stops at window 1 can see.
func TestWaitingSecondWindowRearmsTheChain(t *testing.T) {
	d := newTestDeps(t)
	d.Agent = &uitest.FakeAgent{}
	m := New(d).(*Model)
	m.anim = true
	m = startWaitingTurn(t, m, "two round question")

	// Window 1 beats once: shut, hold in flight.
	if _, cmd := m.Update(waitTickMsg{}); cmd == nil {
		t.Fatal("window 1 armed no hold")
	}
	if !m.waitShut {
		t.Fatal("window 1's beat did not shut the pose")
	}

	// Round 1 answers before the hold lands: the stale hold's msg must
	// die (no re-arm) and reset the pose flag on its way through.
	m = feedEvent(t, m, agent.ReasoningDelta{Text: "thinking"})
	if _, cmd := m.Update(waitOpenMsg{}); cmd != nil {
		t.Fatal("a stale wait hold re-armed during thinking (F.W5)")
	}
	if m.waitShut {
		t.Fatal("the stale hold left the pose shut into the thinking phase")
	}

	// Through the tool window the chain stays dead — the ToolCallEv's
	// reset cleared roundToolInFlight into the in-flight leg, and no arm
	// site may fire for a state the pane is not in.
	m = feedEvent(t, m, agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
	if m.waitArmed || m.waitShut {
		t.Fatal("the tool window kept a wait-chain flag up")
	}

	// The boundary: ToolResEv re-enters waiting and the arm site — the
	// real EventMsg's animArm — must arm a FRESH chain (waitArmed was
	// false; this is the re-arm, not a survivor).
	m = feedEvent(t, m, agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "[]"})
	if !m.waitingVisible() {
		t.Fatal("the second window never opened")
	}
	if !m.waitArmed {
		t.Fatal("the second window's own EventMsg re-armed nothing — " +
			"the 023 failure shape: the chain died at the boundary and the " +
			"next window sits frozen")
	}

	// The fresh chain beats: tick → shut → open → re-armed, in window 2.
	if _, cmd := m.Update(waitTickMsg{}); cmd == nil {
		t.Fatal("the second window's beat armed no hold")
	}
	if !m.waitShut {
		t.Fatal("the second window's beat did not shut the pose")
	}
	if _, cmd := m.Update(waitOpenMsg{}); cmd == nil {
		t.Fatal("the second window's hold did not re-open and re-arm")
	}
	if m.waitShut || !m.waitArmed {
		t.Fatalf("the second window's chain is not single-file live: armed=%v shut=%v",
			m.waitArmed, m.waitShut)
	}

	// And a stale scan beat landing in the second window dies without
	// disturbing the wait chain — the scan direction of the single-file
	// rule, which the round-boundary pins never exercised.
	m = feedEvent(t, m, agent.ReasoningDelta{Text: "hmm"})
	if !m.scanArmed {
		t.Fatal("round 2's thinking armed no scan chain")
	}
	m = feedEvent(t, m, agent.ToolCallEv{ID: "t2", Name: "wiki.search", Args: `{}`})
	m = feedEvent(t, m, agent.ToolResEv{ID: "t2", Name: "wiki.search", Content: "[]"})
	thirdArmed, thirdShut := m.waitArmed, m.waitShut
	if _, cmd := m.Update(scanTickMsg{}); cmd != nil {
		t.Fatal("a stale scan tick re-armed inside the waiting window")
	}
	if m.scanArmed {
		t.Fatal("the dying scan tick left its flag up")
	}
	if m.waitArmed != thirdArmed || m.waitShut != thirdShut {
		t.Fatal("the stale scan tick disturbed the wait chain's flags")
	}
}

// TestWaitingCtrlTViewCarriesTheBusyRow pins the ctrl+t half of the
// one-head rule. The toggle replaces conversationLines — the sending
// row's only mount — so the view must carry a busy row of its own while
// the pane waits, or the window that A-025-3 withdraws the footer morsel
// for would show NO busy sign at all: a running turn that looks dead.
// (Found by this pass: before the fix the view showed neither the row nor
// the morsel for the whole cold-start window.)
func TestWaitingCtrlTViewCarriesTheBusyRow(t *testing.T) {
	d := newTestDeps(t)
	d.Agent = &uitest.FakeAgent{}
	m := New(d).(*Model)
	m.anim = true
	m = startWaitingTurn(t, m, "two round question")

	// Toggle open mid-wait, round 1: no reasoning yet + the sending row.
	pane, _ := m.Update(specialKey('t', tea.ModCtrl))
	m = pane.(*Model)
	screen := screenOf(t, m)
	if !strings.Contains(screen, "no reasoning yet this turn") {
		t.Fatalf("the view lost its no-reasoning line:\n%s", screen)
	}
	if !strings.Contains(screen, "· sending…") {
		t.Fatalf("the ctrl+t view dropped the sending row mid-wait — the pane "+
			"shows no busy sign at all (the morsel is withdrawn here too):\n%s", screen)
	}
	if text, _ := m.FooterPrefix(); text != "" {
		t.Fatalf("the footer morsel came back beside the view's row: %q", text)
	}

	// The row follows the round out of the window: thinking shows the
	// thinking row, never both.
	m = feedEvent(t, m, agent.ReasoningDelta{Text: strings.Repeat("hmm ", 60)})
	screen = screenOf(t, m)
	if !strings.Contains(screen, "· thinking… (") {
		t.Fatalf("the view lost the thinking row mid-round:\n%s", screen)
	}
	if strings.Contains(screen, "· sending…") {
		t.Fatalf("the view stacked the sending row beside the thinking row:\n%s", screen)
	}

	// And back into the second window with the view still open: the
	// sending row returns, tail above it.
	m = feedEvent(t, m, agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
	m = feedEvent(t, m, agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "[]"})
	screen = screenOf(t, m)
	if !strings.Contains(screen, "· sending…") {
		t.Fatalf("the view dropped the sending row in the second window:\n%s", screen)
	}
	if !strings.Contains(screen, "hmm") {
		t.Fatalf("the second window's view lost the turn's reasoning tail:\n%s", screen)
	}

	// The toggle closes: the conversation's own row is back, one head.
	pane, _ = m.Update(specialKey('t', tea.ModCtrl))
	m = pane.(*Model)
	if head := busyHead(t, m); head != "transcript" {
		t.Fatalf("after closing the view: head = %q, want transcript", head)
	}
}

// TestWaitingStreamCutKillsEveryChain pins the cut mid-wait with the anim
// ON — the anim-off half is TestStreamCutDropsWaitingPose, but that pin
// cannot see a chain, and the cut is exactly where a surviving tick would
// hide: the pose must drop, the idle blink must take over, and every
// in-flight wait beat must die without re-arming or leaking a flag.
func TestWaitingStreamCutKillsEveryChain(t *testing.T) {
	d := newTestDeps(t)
	d.Agent = &uitest.FakeAgent{}
	m := New(d).(*Model)
	m.anim = true
	m = startWaitingTurn(t, m, "cut me mid-wait")

	// One beat delivered, hold in flight when the cut lands.
	if _, cmd := m.Update(waitTickMsg{}); cmd == nil {
		t.Fatal("precondition: no hold in flight")
	}
	pane, cmd := m.Update(StreamClosedMsg{})
	m = pane.(*Model)
	if cmd == nil {
		t.Fatal("the cut re-armed nothing — the idle blink must take over")
	}
	if !m.blinkArmed {
		t.Fatal("the cut did not arm the idle blink chain")
	}
	if m.turnActive || m.waitingVisible() {
		t.Fatal("the cut left the turn's waiting state live")
	}
	if got := m.mascotState(); got != msIdle {
		t.Fatalf("after the cut = %d, want msIdle", got)
	}
	if head := busyHead(t, m); head != "footer" {
		t.Fatalf("after the cut: head = %q, want footer — the morsel must come back", head)
	}

	// The stale beats land after the cut: both must die flagless.
	if _, cmd := m.Update(waitTickMsg{}); cmd != nil {
		t.Fatal("a stale wait tick survived the cut and re-armed")
	}
	if _, cmd := m.Update(waitOpenMsg{}); cmd != nil {
		t.Fatal("a stale wait hold survived the cut and re-armed")
	}
	if m.waitArmed || m.waitShut {
		t.Fatalf("the cut leaked a wait flag: armed=%v shut=%v", m.waitArmed, m.waitShut)
	}
}

// TestWaitingMountNeverLies pins F.W3's honesty at the paths the enter
// arm does not cover: the waiting state is a claim that a turn is being
// sent, so every path that does NOT start one must leave it unmounted and
// the wait chain unarmed.
func TestWaitingMountNeverLies(t *testing.T) {
	t.Run("empty_input_mounts_nothing", func(t *testing.T) {
		d := newTestDeps(t)
		d.Agent = &uitest.FakeAgent{}
		m := New(d).(*Model)
		m.anim = true
		pane, cmd := m.Update(specialKey(tea.KeyEnter, 0))
		m = pane.(*Model)
		if cmd != nil {
			t.Fatal("enter on an empty box produced a command")
		}
		if m.turnActive || m.waitingVisible() || m.waitArmed {
			t.Fatal("an empty submit mounted the waiting state or armed its chain")
		}
	})

	t.Run("nil_agent_mounts_nothing", func(t *testing.T) {
		d := newTestDeps(t) // Deps.Agent stays nil
		m := New(d).(*Model)
		m.anim = true
		m, cmd := typeAndSubmit(t, m, "no one to ask")
		if m.turnActive || m.waitingVisible() || m.waitArmed || m.waitShut {
			t.Fatal("a nil-agent submit mounted the waiting state or armed its chain")
		}
		// The pane IS idle here — the degrade path never started a turn —
		// so the idle blink may arm; what must never arm is the wait chain
		// the real submit path would have.
		if !m.blinkArmed {
			t.Fatal("the idle pane armed no blink after the refused submit")
		}
		_ = cmd // the degrade batch; its nil-agent arm is nil by animArm's own guard
		screen := screenOf(t, m)
		if strings.Contains(screen, "· sending…") {
			t.Fatalf("a submit with no agent shows a sending row:\n%s", screen)
		}
	})

	t.Run("refused_submit_stacks_no_second_state", func(t *testing.T) {
		d := newTestDeps(t)
		d.Agent = &uitest.FakeAgent{}
		m := New(d).(*Model)
		m.anim = true
		m = startWaitingTurn(t, m, "the running turn")

		// A second submit mid-turn is refused, not queued: the refusal
		// must not stack a second waiting state, must not disturb the
		// running turn's single-file chain, and the refusal notice must
		// land in the scrollback.
		armed, shut := m.waitArmed, m.waitShut
		m, _ = typeAndSubmit(t, m, "a queued question")
		if got := lastEntry(m); got.kind != kindStatus ||
			got.text != "a turn is already running — submit refused, not queued" {
			t.Fatalf("the refusal notice did not land: %+v", got)
		}
		if m.waitArmed != armed || m.waitShut != shut {
			t.Fatalf("the refused submit disturbed the running turn's chain: armed=%v shut=%v",
				m.waitArmed, m.waitShut)
		}
		if n := strings.Count(screenOf(t, m), "· sending…"); n != 1 {
			t.Fatalf("after the refusal the screen carries %d sending rows, want 1", n)
		}
		// The running turn's chain still beats, single file: one more tick
		// is taken, and while its hold is in flight animArm defers — no
		// second beat is ever issued beside a live one. (A second
		// waitTickMsg itself is not a message the runtime can produce —
		// the chain only issues one beat at a time — so the guarantee that
		// matters is at the arm site.)
		if _, cmd := m.Update(waitTickMsg{}); cmd == nil {
			t.Fatal("the running turn's chain stopped beating")
		}
		if cmd := m.animArm(); cmd != nil {
			t.Fatal("animArm issued a beat beside the in-flight hold")
		}
	})
}

// TestWaitingToolFlagCleareances pins roundToolInFlight's two clears
// (sequence 7's malformed shapes and sequence 8's double clear): the
// orphan result, the never-resolved second call, and the turn's own
// reset must each leave the flag honest — and nothing may resurrect a
// waiting state once the turn is over.
func TestWaitingToolFlagCleareances(t *testing.T) {
	t.Run("orphan_result_via_real_update", func(t *testing.T) {
		d := newTestDeps(t)
		d.Agent = &uitest.FakeAgent{}
		m := New(d).(*Model)
		m.anim = true
		m = startWaitingTurn(t, m, "orphan result")
		m = feedEvent(t, m, agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
		if !m.roundToolInFlight {
			t.Fatal("precondition: no tool flagged in flight")
		}
		m = feedEvent(t, m, agent.ToolResEv{ID: "other", Name: "wiki.search", Content: "stray"})
		if m.roundToolInFlight {
			t.Fatal("the orphan left the flag stuck up — the row would stay suppressed all turn")
		}
		if got := m.mascotState(); got != msWaiting {
			t.Fatalf("after the orphan = %d, want msWaiting", got)
		}
	})

	t.Run("second_call_never_resolved", func(t *testing.T) {
		// A malformed stream CAN deliver two calls before any result (the
		// real Loop dispatches sequentially, so this shape is fake-only);
		// the flag is round-scoped, so the first result clears it while
		// the second call is still conceptually running. The row's return
		// there is the documented round-scoped reading — pinned so a later
		// change to per-call accounting lands as a conscious amendment.
		d := newTestDeps(t)
		d.Agent = &uitest.FakeAgent{}
		m := New(d).(*Model)
		m.anim = true
		m = startWaitingTurn(t, m, "two calls")
		m = feedEvent(t, m, agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
		m = feedEvent(t, m, agent.ToolCallEv{ID: "t2", Name: "wiki.search", Args: `{}`})
		if !m.roundToolInFlight {
			t.Fatal("the second call left nothing in flight")
		}
		m = feedEvent(t, m, agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "one"})
		if m.roundToolInFlight {
			t.Fatal("the first result left the flag up")
		}
		if got := m.mascotState(); got != msWaiting {
			t.Fatalf("after the first of two results = %d, want msWaiting (round-scoped flag)", got)
		}
	})

	t.Run("terminal_clear_resurrects_nothing", func(t *testing.T) {
		// Sequence 8's ordering question: resetReasoningRound clears
		// roundToolInFlight a second time on the turn's end. The clear
		// must not resurrect a waiting state — turnActive going down is
		// the gate, and it wins over every flag the reset touches.
		d := newTestDeps(t)
		d.Agent = &uitest.FakeAgent{}
		m := New(d).(*Model)
		m.anim = true
		m = startWaitingTurn(t, m, "end mid-round")
		m = feedEvent(t, m, agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
		m = feedEvent(t, m, agent.DoneEv{Reason: "stop", Rounds: 1})
		if m.turnActive || m.roundToolInFlight || m.waitingVisible() {
			t.Fatalf("the terminal event left a waiting ingredient up: active=%v tool=%v waiting=%v",
				m.turnActive, m.roundToolInFlight, m.waitingVisible())
		}
		if got := m.mascotState(); got != msIdle {
			t.Fatalf("after the terminal clear = %d, want msIdle", got)
		}
		// And the error terminal, whose verdict the waiting predicate must
		// never outrank: turnErrored shows only once turnActive is down,
		// and waitingVisible needs turnActive up.
		m2 := New(d).(*Model)
		m2.anim = true
		m2 = startWaitingTurn(t, m2, "error mid-round")
		m2 = feedEvent(t, m2, agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
		m2 = feedEvent(t, m2, agent.ErrorEv{Err: errCut{}})
		if got := m2.mascotState(); got != msError {
			t.Fatalf("after the error terminal = %d, want msError", got)
		}
		if m2.waitingVisible() {
			t.Fatal("an errored turn ended in the waiting state")
		}
		// A NEW turn masks the stale verdict with its own waiting window
		// (016 F.M4's mask, through 025's table).
		m2 = startWaitingTurn(t, m2, "again")
		if got := m2.mascotState(); got != msWaiting {
			t.Fatalf("the new turn's window = %d, want msWaiting (the stale verdict is masked)", got)
		}
	})
}

// errCut is the error the error-terminal leg hands the pane.
type errCut struct{}

func (errCut) Error() string { return "the stream died" }

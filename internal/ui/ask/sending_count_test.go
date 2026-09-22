// sending_count_test.go is workflow 026 T1's permanent pins (F.C5, D-10C):
// the counted `· sending…` row. F.C1 freezes the elapsed format and the
// bare-at-zero rule, F.C2 makes the clock a fourth tick chain, F.C3 gives
// every window its own generation so each round's wait counts from zero
// and a beat still in flight across a boundary is dropped, and F.C4 keeps
// the anim-off pane inert — the bare row every earlier pin already holds.
// Every beat is an injected sendCountMsg or a real event through Update —
// the arm sites run for real; no test here sleeps.
package ask

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// countingPane builds the pane mid-first-window: anim on, one question
// submitted through the real key path, the turn reported in — the count
// chain's edge fired at the submit's own arm (F.C3), a live generation
// minted for this window.
func countingPane(t *testing.T, question string) *Model {
	t.Helper()
	d := newTestDeps(t)
	d.Agent = &uitest.FakeAgent{}
	m := New(d).(*Model)
	m.anim = true
	return startWaitingTurn(t, m, question)
}

// beat feeds one count tick of the window's current generation through
// Update — the chain's own beat shape, without sleeping through a real
// sendCountEvery.
func beat(t *testing.T, m *Model) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(sendCountMsg{gen: m.sendGen})
	return cmd
}

// bareRow asserts the screen carries the bare `· sending…` — the row
// mounted, no count suffix on it.
func bareRow(t *testing.T, m *Model) {
	t.Helper()
	screen := screenOf(t, m)
	if !strings.Contains(screen, "· sending…") {
		t.Fatalf("the sending row is not mounted:\n%s", screen)
	}
	if strings.Contains(screen, "· sending… (") {
		t.Fatalf("the bare row carries a count suffix:\n%s", screen)
	}
}

func TestSendingCount(t *testing.T) {
	t.Run("format_table", func(t *testing.T) {
		// F.C1, exact: plain seconds under the minute, Mm SS from it up,
		// and zero — never rendered — empty.
		for _, p := range []struct {
			secs int
			want string
		}{
			{0, ""},
			{1, "1s"},
			{59, "59s"},
			{60, "1m00s"},
			{65, "1m05s"},
			{3599, "59m59s"},
			{3600, "60m00s"},
		} {
			if got := formatSendingElapsed(p.secs); got != p.want {
				t.Fatalf("formatSendingElapsed(%d) = %q, want %q (F.C1)", p.secs, got, p.want)
			}
		}
	})

	t.Run("counts_up", func(t *testing.T) {
		m := countingPane(t, "count me")
		bareRow(t, m) // before any beat: bare, never `· sending… (0s)`
		for i := 1; i <= 3; i++ {
			if cmd := beat(t, m); cmd == nil {
				t.Fatalf("beat %d re-armed nothing (F.C2)", i)
			}
		}
		if got := m.sendSecs; got != 3 {
			t.Fatalf("sendSecs after three beats = %d, want 3", got)
		}
		if screen := screenOf(t, m); !strings.Contains(screen, "· sending… (3s)") {
			t.Fatalf("the row does not read `· sending… (3s)` after three beats:\n%s", screen)
		}
	})

	t.Run("stops_on_first_byte", func(t *testing.T) {
		m := countingPane(t, "answer me")
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("precondition: the first beat re-armed nothing")
		}
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("precondition: the second beat re-armed nothing")
		}
		m = feedEvent(t, m, agent.ReasoningDelta{Text: strings.Repeat("hmm", 40)})
		// A beat still in flight when the first reasoning byte lands —
		// even of the surviving generation — finds the window closed and
		// stops by not re-issuing (F.C2).
		if cmd := beat(t, m); cmd != nil {
			t.Fatal("a count beat re-armed after the first reasoning byte (F.C2)")
		}
		screen := screenOf(t, m)
		if !strings.Contains(screen, "· thinking… (") {
			t.Fatalf("the thinking row did not replace the sending row:\n%s", screen)
		}
		if strings.Contains(screen, "· sending…") {
			t.Fatalf("the sending row stacked beside the thinking row:\n%s", screen)
		}
	})

	t.Run("restarts_per_round", func(t *testing.T) {
		m := countingPane(t, "two rounds")
		for i := 1; i <= 4; i++ {
			if cmd := beat(t, m); cmd == nil {
				t.Fatalf("beat %d re-armed nothing", i)
			}
		}
		if got := m.sendSecs; got != 4 {
			t.Fatalf("precondition failed: sendSecs = %d, want 4", got)
		}
		// Round 2's window: the boundary restarted the count from zero —
		// the row is bare again, each wait counting its own seconds.
		m = feedEvent(t, m, agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
		m = feedEvent(t, m, agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "[]"})
		if !m.waitingVisible() {
			t.Fatal("round 2's window never opened")
		}
		if got := m.sendSecs; got != 0 {
			t.Fatalf("the boundary left sendSecs = %d, want 0 (F.C3)", got)
		}
		bareRow(t, m)
		// A beat of the NEW generation counts from one.
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("round 2's beat re-armed nothing (F.C3)")
		}
		if got := m.sendSecs; got != 1 {
			t.Fatalf("round 2's sendSecs = %d, want 1 (F.C3)", got)
		}
		if screen := screenOf(t, m); !strings.Contains(screen, "· sending… (1s)") {
			t.Fatalf("round 2's row does not read `· sending… (1s)`:\n%s", screen)
		}
	})

	t.Run("stale_gen_dropped", func(t *testing.T) {
		m := countingPane(t, "fast round")
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("precondition: the first beat re-armed nothing")
		}
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("precondition: the second beat re-armed nothing")
		}
		stale := m.sendGen
		// Cross the round boundary: the edge resets the count and bumps
		// the generation out from under the beats still in flight (F.C3 —
		// the 023 surviving-chain defect class, closed by construction).
		m = feedEvent(t, m, agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`})
		m = feedEvent(t, m, agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "[]"})
		if m.sendGen == stale {
			t.Fatal("the boundary did not bump the generation (F.C3)")
		}
		// The old window's beat lands late: dropped whole — no increment,
		// no re-arm.
		if _, cmd := m.Update(sendCountMsg{gen: stale}); cmd != nil {
			t.Fatal("a stale-generation beat re-armed the chain (F.C3)")
		}
		if got := m.sendSecs; got != 0 {
			t.Fatalf("a stale beat moved the count: sendSecs = %d, want 0 (F.C3)", got)
		}
	})

	t.Run("restart_after_start_error", func(t *testing.T) {
		// A turn that dies before its stream exists — turnStartedMsg's own
		// error report, runTurn failing before Agent.Send ever ran — ends
		// the turn through endTurnError on a path that must still run
		// animArm: the edge tracker it brings down to date is what the NEXT
		// window's edge reads. Miss the update and the next submit inherits
		// the dead window's seconds — no reset, no gen bump, no chain — the
		// row frozen at the old count from its very first frame (F.C3).
		m := countingPane(t, "first try")
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("precondition: the first beat re-armed nothing")
		}
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("precondition: the second beat re-armed nothing")
		}
		if _, cmd := m.Update(turnStartedMsg{err: errors.New("no changeset")}); cmd != nil {
			t.Fatal("a failed turn's report produced a command")
		}
		if m.turnActive {
			t.Fatal("the failed turn's report left the turn active")
		}
		// The next submit opens a fresh window: the edge must fire — the
		// count restarts from zero under a bumped generation, bare until
		// the first beat.
		m, _ = typeAndSubmit(t, m, "second try")
		if !m.waitingVisible() {
			t.Fatal("the second submit never opened a window")
		}
		if got := m.sendSecs; got != 0 {
			t.Fatalf("the new wait inherited the dead turn's count: sendSecs = %d, want 0 (F.C3)", got)
		}
		bareRow(t, m)
		// And the chain lives: a beat of the new generation counts from one.
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("the new wait armed no count chain (F.C2)")
		}
		if got := m.sendSecs; got != 1 {
			t.Fatalf("the new wait's first beat left sendSecs = %d, want 1 (F.C2)", got)
		}
	})

	t.Run("anim_off_inert", func(t *testing.T) {
		// F.C4: the harness shape — anim off — arms nothing and counts
		// nothing; the row stays the bare `· sending…` every earlier pin
		// already holds.
		d := newTestDeps(t)
		d.Agent = &uitest.FakeAgent{}
		m := New(d).(*Model) // anim stays false
		m, _ = typeAndSubmit(t, m, "motionless")
		if !m.turnActive {
			t.Fatal("submit did not mark the turn active")
		}
		if cmd := m.animArm(); cmd != nil {
			t.Fatal("animArm armed a chain with the anim off (F.C4)")
		}
		if got := m.sendSecs; got != 0 {
			t.Fatalf("sendSecs moved with the anim off: %d (F.C4)", got)
		}
		// Even a hand-fed beat of the current generation is inert: no
		// increment, no re-arm — no sendCountMsg is producible.
		if _, cmd := m.Update(sendCountMsg{gen: m.sendGen}); cmd != nil {
			t.Fatal("a count beat re-armed with the anim off (F.C4)")
		}
		if got := m.sendSecs; got != 0 {
			t.Fatalf("a count beat incremented with the anim off: %d (F.C4)", got)
		}
		bareRow(t, m)
	})

	t.Run("turn_end_stops", func(t *testing.T) {
		// The clean stop: DoneEv ends the turn, and the chain's next beat
		// — same generation, still in flight — finds nothing to count.
		m := countingPane(t, "end cleanly")
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("precondition: the first beat re-armed nothing")
		}
		m = feedEvent(t, m, agent.DoneEv{Reason: "stop", Rounds: 1})
		if cmd := beat(t, m); cmd != nil {
			t.Fatal("a count beat re-armed after DoneEv (F.C2)")
		}
		if strings.Contains(screenOf(t, m), "· sending…") {
			t.Fatal("the sending row survived the turn's clean end")
		}

		// The cut: StreamClosedMsg mid-wait kills the chain the same way —
		// the watch-item shape 023 carried, the count included.
		m = countingPane(t, "cut me")
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("precondition: the first beat re-armed nothing")
		}
		if _, cmd := m.Update(StreamClosedMsg{}); cmd == nil {
			t.Fatal("the cut re-armed nothing — the idle blink must take over")
		}
		if cmd := beat(t, m); cmd != nil {
			t.Fatal("a count beat re-armed after the cut (F.C2)")
		}
		if strings.Contains(screenOf(t, m), "· sending…") {
			t.Fatal("the sending row survived the cut")
		}
	})

	t.Run("ctrl_t_view_counts", func(t *testing.T) {
		// The 025 Tier-2 path: with the reasoning view open over the
		// transcript, the busy row rides the view — counted exactly like
		// the conversation's own, both mounts sharing mascotStatusRow's
		// one composition (F.C1).
		m := countingPane(t, "watch me wait")
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("precondition: the first beat re-armed nothing")
		}
		if cmd := beat(t, m); cmd == nil {
			t.Fatal("precondition: the second beat re-armed nothing")
		}
		if _, cmd := m.Update(specialKey('t', tea.ModCtrl)); cmd != nil {
			t.Fatal("ctrl+t produced a command")
		}
		screen := screenOf(t, m)
		if !strings.Contains(screen, "· sending… (2s)") {
			t.Fatalf("the ctrl+t view does not carry the counted row:\n%s", screen)
		}
	})
}

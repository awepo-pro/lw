// stop_text_test.go is 008 T-G's ask-side evidence (MASTER §5): a clean
// stop keeps the frozen `done · N rounds` boundary, a turn cut off at the
// round limit says `stopped: round limit · N rounds`, and an ErrorEv
// wrapping agent.ErrTruncated ends through the existing error path with the
// one sentence a curator can act on.
package ask

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// feedEvents drives evs through the pane's pump path — EventMsg, exactly as
// the shell routes them — and returns the pane.
func feedEvents(t *testing.T, m ui.Pane, evs ...agent.Event) *Model {
	t.Helper()
	var seen []tea.Msg
	for _, ev := range evs {
		m = feedMsg(t, m, ui.EventMsg{Ev: ev}, &seen)
	}
	mm, ok := m.(*Model)
	if !ok {
		t.Fatalf("pane is %T, want *ask.Model", m)
	}
	return mm
}

func TestStopTexts(t *testing.T) {
	t.Run("stop_says_done", func(t *testing.T) {
		m := feedEvents(t, New(newTestDeps(t)), agent.DoneEv{Reason: "stop", Rounds: 2})

		if m.turnActive {
			t.Fatal("turn still active after DoneEv")
		}
		got := lastEntry(m)
		if got.kind != kindStatus || got.text != "done · 2 rounds" {
			t.Fatalf("turn boundary = %#v, want the frozen \"done · 2 rounds\" status line", got)
		}
	})

	t.Run("max_rounds_says_round_limit", func(t *testing.T) {
		m := feedEvents(t, New(newTestDeps(t)), agent.DoneEv{Reason: "max_rounds", Rounds: 24})

		if m.turnActive {
			t.Fatal("turn still active after the max_rounds DoneEv")
		}
		got := lastEntry(m)
		if got.kind != kindStatus || got.text != "stopped: round limit · 24 rounds" {
			t.Fatalf("turn boundary = %#v, want \"stopped: round limit · 24 rounds\"", got)
		}
	})

	t.Run("truncated_error_says_output_limit", func(t *testing.T) {
		err := fmt.Errorf("%w (finish_reason %q in round %d)", agent.ErrTruncated, "length", 5)
		m := feedEvents(t, New(newTestDeps(t)), agent.ErrorEv{Err: err})

		if m.turnActive {
			t.Fatal("turn still active after the truncated ErrorEv")
		}
		got := lastEntry(m)
		const want = "stopped: output limit reached — nothing after this was proposed; raise llm.max_tokens"
		if got.kind != kindError || got.text != want {
			t.Fatalf("turn boundary = %#v, want the %q error line", got, want)
		}
	})
}

// mascot_waiting_test.go is workflow 025's red-first regression pin for the
// ask cold-start window: between the user's submit and the provider's first
// stream event (measured median 5.80s, p90 7.99s, max 15.45s, recurring
// every round) the pane renders pixel-identically to an idle, unused pane.
// The mechanism this file pins against: thinkingVisible (state.go) needs
// roundSawReasoning, which only ReasoningDelta ever sets, so mascotState
// falls through turnActive to msIdle; blinkEligible (mascot_anim.go) is
// false for the whole turn; and no status line exists before the first
// event. The window owes the curator a visible `· sending…` and a
// rendering that differs from an otherwise-identical idle pane. This file
// pins the EXPECTATION only — it is delivered failing and flips green when
// the feature lands; it implements nothing.
package ask

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// waitingW, waitingH is the size both panes render at — the package's
// canonical checkpoint grid (hintW×hintH, every mascot pin).
const (
	waitingW = 80
	waitingH = 22
)

// buildWaitingAndIdle constructs the comparison pair. The waiting pane
// submits one question through the real key path (typeAndSubmit) with a
// non-nil agent and no engine — the newWebHintModel shape, so beginTurn
// runs and marks the turn active synchronously. The startTurn cmd it
// returns is deliberately never run, so NO stream event reaches the pane —
// not even turnStartedMsg: the exact shape of the cold-start window, where
// the provider has not answered yet. (startTurn's goroutine resolves its
// nil engine into the buffered started channel and exits; the pane never
// sees the message.) The idle twin is built from the same deps with the
// same echoed question and no turn ever started — identical transcript,
// identical input box, turnActive down.
func buildWaitingAndIdle(t *testing.T) (waiting, idle *Model) {
	t.Helper()
	const question = "what is a kv cache?"

	d := newTestDeps(t)
	d.Agent = &uitest.FakeAgent{}

	waiting = New(d).(*Model)
	waiting, cmd := typeAndSubmit(t, waiting, question)
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	if !waiting.turnActive {
		t.Fatal("submit did not mark the turn active")
	}

	idle = New(d).(*Model)
	idle.echoUser(question) // beginTurn's echo, without the turn

	// The pair is only a fair comparison while the transcripts match; a
	// harness drift here would let the renderings differ for the wrong
	// reason and the pin would pass spuriously once the feature lands.
	if lastEntry(waiting) != lastEntry(idle) {
		t.Fatalf("the harness built unlike transcripts: %#v vs %#v",
			lastEntry(waiting), lastEntry(idle))
	}
	return waiting, idle
}

// TestWaitingNotIdleIdentical is the frozen contract: a pane whose submit
// has been answered by nothing yet must not look like a pane nobody used.
func TestWaitingNotIdleIdentical(t *testing.T) {
	t.Run("waiting_pane_differs_from_idle", func(t *testing.T) {
		waiting, idle := buildWaitingAndIdle(t)
		_, waitingPlain := uitest.PaneScreen(waiting, waitingW, waitingH)
		_, idlePlain := uitest.PaneScreen(idle, waitingW, waitingH)
		if waitingPlain == idlePlain {
			t.Fatalf("the post-submit, pre-first-event pane renders IDENTICAL "+
				"to an idle, unused pane (025 cold-start):\n\n--- waiting ---\n%s\n\n"+
				"--- idle ---\n%s", waitingPlain, idlePlain)
		}
	})

	t.Run("waiting_pane_says_sending", func(t *testing.T) {
		waiting, _ := buildWaitingAndIdle(t)
		_, plain := uitest.PaneScreen(waiting, waitingW, waitingH)
		const want = "· sending…"
		if !strings.Contains(plain, want) {
			t.Fatalf("the post-submit, pre-first-event pane never shows %q:\n%s",
				want, plain)
		}
	})
}

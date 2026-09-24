// retry_test.go pins the ask pane's half of 035's stream-integrity
// contract: a RetryEv drops the cut round's partial text, keeps every
// earlier round's, shows one retry status line, and a stream cut twice in
// one round ends the turn with its own terminal sentence. Driven through
// applyEvent with scripted events only — never a real provider.
package ask

import (
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/llm"
)

// newRetryModel builds a headless pane for the retry tests — no engine, no
// agent: the tests feed events through applyEvent themselves.
func newRetryModel(t *testing.T) *Model {
	t.Helper()
	return New(newTestDeps(t)).(*Model)
}

// TestRetryDropsPartialRoundText is frozen block 1: text streamed before
// the cut must not survive into the retry's transcript — the pane shows
// the retried round's full answer and the retry status line, never the
// partial text the provider abandoned.
func TestRetryDropsPartialRoundText(t *testing.T) {
	m := newRetryModel(t)
	for _, ev := range []agent.Event{
		agent.TextDelta{Text: "partial ZEBRA"},
		agent.RetryEv{Round: 1, Attempt: 2, Reason: "stream ended early"},
		agent.TextDelta{Text: "full"},
		agent.DoneEv{Reason: "stop", Rounds: 1},
	} {
		m.applyEvent(ev)
	}

	view := m.View(80, 20)
	if !strings.Contains(view, "full") {
		t.Fatalf("view lost the retried round's answer:\n%s", view)
	}
	if strings.Contains(view, "ZEBRA") {
		t.Fatalf("view still shows the cut round's partial text:\n%s", view)
	}
	const want = "provider stream cut · retrying the round"
	if got := strings.Count(view, want); got != 1 {
		t.Fatalf("view shows the retry status %d time(s), want exactly once:\n%s", got, view)
	}
}

// TestRetryKeepsEarlierRounds is frozen block 2: the drop reaches back
// only to the round's last tool event — text streamed before the round's
// tool call stays in the transcript, the partial text after it goes.
func TestRetryKeepsEarlierRounds(t *testing.T) {
	m := newRetryModel(t)
	for _, ev := range []agent.Event{
		agent.TextDelta{Text: "before"},
		agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"x"}`},
		agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "ok"},
		agent.TextDelta{Text: "partial"},
		agent.RetryEv{Round: 2, Attempt: 2, Reason: "stream ended early"},
		agent.TextDelta{Text: "after"},
		agent.DoneEv{Reason: "stop", Rounds: 2},
	} {
		m.applyEvent(ev)
	}

	got := assistantText(m)
	if len(got) != 2 || got[0] != "before" || got[1] != "after" {
		t.Fatalf("assistant entries = %#v, want [\"before\" \"after\"] — partial dropped, before kept", got)
	}
}

// TestStreamTruncatedErrorSentence is frozen block 3: an ErrorEv whose
// error wraps llm.ErrStreamTruncated gets its own terminal sentence, not
// the generic wrap chain's provider detail and not ErrTruncated's
// output-limit sentence.
func TestStreamTruncatedErrorSentence(t *testing.T) {
	m := newRetryModel(t)
	m.applyEvent(agent.ErrorEv{Err: fmt.Errorf("round 2: %w", llm.ErrStreamTruncated)})

	got := lastEntry(m)
	if got.kind != kindError {
		t.Fatalf("last entry kind = %v, want kindError", got.kind)
	}
	want := "stopped: the provider cut the stream off twice in one round — send again"
	if got.text != want {
		t.Fatalf("terminal line = %q, want %q", got.text, want)
	}
}

// TestRetryDropsTextAroundPaneNotice pins the span rule a pane-local
// notice must not break: a refused submit (ask.go's submitInput) or a
// failed Close lands a kindStatus entry at the round's tail while it
// streams, so the round's partial text continues on BOTH sides of it —
// the drop must skip the notice and take both halves, not stop at the
// first non-assistant entry and strand the text below it.
func TestRetryDropsTextAroundPaneNotice(t *testing.T) {
	m := newRetryModel(t)
	for _, ev := range []agent.Event{
		agent.TextDelta{Text: "before ZEBRA"},
	} {
		m.applyEvent(ev)
	}
	m.appendStatus("a turn is already running — submit refused, not queued")
	m.applyEvent(agent.TextDelta{Text: " after ZEBRA"})
	m.applyEvent(agent.RetryEv{Round: 1, Attempt: 2})
	m.applyEvent(agent.TextDelta{Text: "full"})
	m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})

	view := m.View(80, 20)
	if strings.Contains(view, "ZEBRA") {
		t.Fatalf("partial text survived on one side of the pane notice:\n%s", view)
	}
	if !strings.Contains(view, "a turn is already running — submit refused, not queued") {
		t.Fatalf("the pane notice itself was dropped:\n%s", view)
	}
	if got := assistantText(m); len(got) != 1 || got[0] != "full" {
		t.Fatalf("assistant entries = %#v, want [\"full\"]", got)
	}
}

// TestRetryOutsideTurnDropsNothing pins the guard on the drop: a RetryEv
// outside an active turn — a protocol-violating fake, never the real Loop
// — appends its status line but must not reach back into the finished
// turn's answer above it.
func TestRetryOutsideTurnDropsNothing(t *testing.T) {
	m := newRetryModel(t)
	m.applyEvent(agent.TextDelta{Text: "finished answer"})
	m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
	m.applyEvent(agent.RetryEv{Round: 1, Attempt: 2})

	if got := assistantText(m); len(got) != 1 || got[0] != "finished answer" {
		t.Fatalf("assistant entries = %#v, want the finished turn's answer kept", got)
	}
	if got := lastEntry(m); got.kind != kindStatus || got.text != retryStatusLine {
		t.Fatalf("last entry = %v %q, want the retry status line", got.kind, got.text)
	}
}

// TestRetriedAnswerIsFileable is frozen block 4: a turn that retried and
// then finished cleanly records the FULL answer for ctrl+s — the partial
// text was dropped from the scrollback before recordLastAnswer ran.
func TestRetriedAnswerIsFileable(t *testing.T) {
	m := newRetryModel(t)
	for _, ev := range []agent.Event{
		agent.TextDelta{Text: "partial ZEBRA"},
		agent.RetryEv{Round: 1, Attempt: 2, Reason: "stream ended early"},
		agent.TextDelta{Text: "full"},
		agent.DoneEv{Reason: "stop", Rounds: 1},
	} {
		m.applyEvent(ev)
	}

	if !m.last.set {
		t.Fatal("the retried turn recorded no answer")
	}
	if m.last.answer != "full" {
		t.Fatalf("recorded answer = %q, want the full answer, not the partial one", m.last.answer)
	}
}

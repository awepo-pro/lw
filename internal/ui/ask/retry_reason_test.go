// retry_reason_test.go pins 061 FIX1 A-061-2: the retry status line says WHY.
// 035's line, "provider stream cut · retrying the round", is right for a
// stream the provider cut and wrong for a rate limit — the loop now waits out
// a 429 for up to a minute, and the pane labelled that wait as a cut stream.
// A RetryEv whose Reason is neither empty nor 035's "stream ended early"
// (agent.RetryEv: "provider <code>: <message>; retrying in <N>s") is shown
// as itself; 035's tests (retry_test.go) pin the old line and stay as they are.
package ask

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/agent"
)

func TestRetryStatusShowsProviderReason(t *testing.T) {
	const reason = "provider 429: Rate limit exceeded: free-models-per-min; retrying in 3s"
	m := newRetryModel(t)
	for _, ev := range []agent.Event{
		agent.TextDelta{Text: "partial ZEBRA"},
		agent.RetryEv{Round: 1, Attempt: 1, Reason: reason},
		agent.TextDelta{Text: "full"},
		agent.DoneEv{Reason: "stop", Rounds: 1},
	} {
		m.applyEvent(ev)
	}

	view := m.View(100, 20)
	if got := strings.Count(view, reason); got != 1 {
		t.Fatalf("view shows the reason %d time(s), want exactly once:\n%s", got, view)
	}
	if strings.Contains(view, "provider stream cut") {
		t.Fatalf("a provider refusal is labelled as a cut stream:\n%s", view)
	}
	if strings.Contains(view, "ZEBRA") || !strings.Contains(view, "full") {
		t.Fatalf("the round's text was not dropped/kept as 035 does:\n%s", view)
	}
	var status *entry
	for i := range m.entries {
		if m.entries[i].kind == kindStatus && strings.Contains(m.entries[i].text, "retrying in") {
			status = &m.entries[i]
		}
	}
	if status == nil || status.text != reason {
		t.Fatalf("retry status entry = %+v, want kindStatus with text %q", status, reason)
	}
}

// TestRetryStatusKeeps035Text: the cut-stream reasons — 035's own, and the
// empty one a protocol-violating fake sends — keep the original line.
func TestRetryStatusKeeps035Text(t *testing.T) {
	for _, reason := range []string{"stream ended early", ""} {
		m := newRetryModel(t)
		m.applyEvent(agent.TextDelta{Text: "partial"})
		m.applyEvent(agent.RetryEv{Round: 1, Attempt: 1, Reason: reason})
		if got := lastEntry(m); got.kind != kindStatus || got.text != retryStatusLine {
			t.Errorf("Reason %q: last entry = %v %q, want the 035 line %q", reason, got.kind, got.text, retryStatusLine)
		}
	}
}

// TestRetryStatusReasonIsOneLine: a provider message can carry newlines and
// runs of spaces (an HTML body, pretty-printed JSON); a status line is one.
func TestRetryStatusReasonIsOneLine(t *testing.T) {
	m := newRetryModel(t)
	m.applyEvent(agent.RetryEv{Round: 1, Attempt: 1, Reason: "provider 502: upstream\n  said  no;\tretrying in 2s"})
	want := "provider 502: upstream said no; retrying in 2s"
	if got := lastEntry(m); got.kind != kindStatus || got.text != want {
		t.Fatalf("last entry = %v %q, want kindStatus %q", got.kind, got.text, want)
	}
}

// TestRetryStatusLongReasonStaysInTheFrame: at 80 columns a long reason is
// laid out like any other status line — no row wider than the pane.
func TestRetryStatusLongReasonStaysInTheFrame(t *testing.T) {
	m := newRetryModel(t)
	long := "provider 429: " + strings.Repeat("rate limited upstream ", 12) + "; retrying in 30s"
	m.applyEvent(agent.TextDelta{Text: "x"})
	m.applyEvent(agent.RetryEv{Round: 1, Attempt: 1, Reason: long})
	for i, line := range strings.Split(m.View(80, 20), "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Errorf("view row %d is %d cells, want ≤ 80: %q", i, w, ansi.Strip(line))
		}
	}
	if !strings.Contains(ansi.Strip(m.View(80, 20)), "provider 429: rate limited upstream") {
		t.Errorf("the reason's start is not visible at 80 columns:\n%s", m.View(80, 20))
	}
}

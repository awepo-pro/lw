// reasoning_test.go pins 022 T2's thinking surface: the `· thinking…`
// status line — its exact format, its phase rule (visible while the current
// round has reasoning and no text yet, gone the moment text streams, reset
// with each turn) — and the ctrl+t reasoning-tail view, whose text is pane
// state only and never reaches the scrollback or the session file.
package ask

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// TestThinkingStatusLine feeds reasoning through a scripted turn and pins
// the status line's format at both magnitudes — 1234 chars renders as
// `1.2k`, 950 as the plain integer — the hide-on-text rule, and the
// per-turn reset (the second turn's 950 must not add to the first turn's
// 1234).
func TestThinkingStatusLine(t *testing.T) {
	_, engine, _ := liveVault(t)
	store := agent.NewFileSessions(engine.Vault().Root())

	// Turn 1: reasoning deltas totalling 1234 runes, then text, then done.
	fake := &uitest.FakeAgent{
		Store: store,
		Events: append(uitest.ReasoningTurn(
			[]string{strings.Repeat("a", 600), strings.Repeat("b", 634)},
			"The answer, once thought through.",
		), agent.DoneEv{Reason: "stop", Rounds: 1}),
	}
	m := New(liveDeps(t, engine, fake)).(*Model)

	m, cmd := typeAndSubmit(t, m, "what are you thinking")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	m, next := pump(t, m, cmd) // turnStartedMsg installs the stream

	// First delta: mid-count below 1000, the plain integer format.
	m, next = pump(t, m, next)
	if got, want := m.reasonChars, 600; got != want {
		t.Fatalf("reasonChars = %d after the first delta, want %d", got, want)
	}
	view := m.View(80, 20)
	if !strings.Contains(view, "· thinking… (600 chars)") {
		t.Fatalf("view does not show the mid-count thinking line:\n%s", view)
	}
	// Thinking is not text: no assistant entry may exist while it streams.
	if got := assistantText(m); len(got) != 0 {
		t.Fatalf("reasoning produced assistant entries: %#v", got)
	}

	// Second delta: 1234 total crosses 1000 — the `%.1fk` format.
	m, next = pump(t, m, next)
	if got, want := m.reasonChars, 1234; got != want {
		t.Fatalf("reasonChars = %d after both deltas, want %d", got, want)
	}
	view = m.View(80, 20)
	if !strings.Contains(view, "· thinking… (1.2k chars)") {
		t.Fatalf("view does not show `· thinking… (1.2k chars)` for 1234 chars:\n%s", view)
	}

	// Text streams: the line hides the moment it does.
	m, next = pump(t, m, next)
	view = m.View(80, 20)
	if strings.Contains(view, "thinking…") {
		t.Fatalf("thinking line still visible after TextDelta:\n%s", view)
	}

	// The turn ends: it stays hidden.
	m, next = pump(t, m, next) // DoneEv
	m, _ = pump(t, m, next)    // StreamClosedMsg
	if m.turnActive {
		t.Fatal("turn 1 did not end")
	}
	if strings.Contains(m.View(80, 20), "thinking…") {
		t.Fatal("thinking line visible after the turn ended")
	}

	// Turn 2: 950 runes — plain format again, and crucially NOT 1234+950:
	// the count resets when a new turn starts.
	m.deps.Agent = &uitest.FakeAgent{
		Store: store,
		Events: append(uitest.ReasoningTurn(
			[]string{strings.Repeat("c", 950)},
			"Short answer.",
		), agent.DoneEv{Reason: "stop", Rounds: 1}),
	}
	m, cmd = typeAndSubmit(t, m, "again, but brief")
	if cmd == nil {
		t.Fatal("second submit produced no command")
	}
	m, next = pump(t, m, cmd)
	m, next = pump(t, m, next)
	if got, want := m.reasonChars, 950; got != want {
		t.Fatalf("turn 2 reasonChars = %d, want %d (the count must reset per turn)", got, want)
	}
	view = m.View(80, 20)
	if !strings.Contains(view, "· thinking… (950 chars)") {
		t.Fatalf("view does not show `· thinking… (950 chars)` for 950 chars:\n%s", view)
	}
	if strings.Contains(view, "1.2k") {
		t.Fatalf("turn 2 carries turn 1's count:\n%s", view)
	}
}

// TestReasoningToggle pins the ctrl+t view: it overlays the transcript area
// with the turn's recent reasoning, dimmed, with the status line still
// visible under it, and a second ctrl+t brings the conversation back —
// while the reasoning itself never enters the scrollback entries or the
// session file.
func TestReasoningToggle(t *testing.T) {
	root, engine, csID := liveVault(t)
	store := agent.NewFileSessions(engine.Vault().Root())

	const r1 = "considering the vault layout and the question's scope. "
	const r2 = "settling on the answer now."
	fake := &uitest.FakeAgent{
		Store: store,
		Events: append(uitest.ReasoningTurn([]string{r1, r2}, "The visible answer."),
			agent.DoneEv{Reason: "stop", Rounds: 1}),
	}
	m := New(liveDeps(t, engine, fake)).(*Model)

	m, cmd := typeAndSubmit(t, m, "show me your thinking")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	m, next := pump(t, m, cmd) // turnStartedMsg
	m, next = pump(t, m, next) // ReasoningDelta r1
	m, next = pump(t, m, next) // ReasoningDelta r2

	// Baseline: the transcript shows the conversation, never the thinking.
	view := m.View(80, 20)
	if strings.Contains(view, "vault layout") {
		t.Fatalf("reasoning text visible before ctrl+t:\n%s", view)
	}
	if !strings.Contains(view, "· thinking… (") {
		t.Fatalf("status line not visible while the round is thinking:\n%s", view)
	}

	// ctrl+t: the reasoning tail view, over the transcript area. The tail
	// text shows, dimmed (the pane's Faint style), and the status line
	// stays visible while the view is open.
	inputBefore := m.input
	pane, _ := m.Update(specialKey('t', tea.ModCtrl))
	m = pane.(*Model)
	if !m.showReasoning {
		t.Fatal("ctrl+t did not open the reasoning view")
	}
	view = m.View(80, 20)
	if !strings.Contains(view, "vault layout") {
		t.Fatalf("reasoning view does not show the turn's reasoning tail:\n%s", view)
	}
	wrapped := wrapPlain(m.reasonTail, 76)
	if len(wrapped) == 0 {
		t.Fatal("reasoning tail wrapped to no lines")
	}
	if want := m.theme.Faint.Render(wrapped[0]); !strings.Contains(view, want) {
		t.Fatalf("reasoning tail is not rendered dimmed:\nwant %q in\n%s", want, view)
	}
	if !strings.Contains(view, "· thinking… (") {
		t.Fatalf("status line vanished while the reasoning view is open:\n%s", view)
	}
	if m.input != inputBefore {
		t.Fatalf("ctrl+t leaked into the input line: %q -> %q", inputBefore, m.input)
	}

	// ctrl+t again: the previous view — the conversation — returns.
	pane, _ = m.Update(specialKey('t', tea.ModCtrl))
	m = pane.(*Model)
	if m.showReasoning {
		t.Fatal("a second ctrl+t did not restore the transcript")
	}
	view = m.View(80, 20)
	if strings.Contains(view, "vault layout") {
		t.Fatalf("reasoning view still open after the second ctrl+t:\n%s", view)
	}
	if !strings.Contains(view, "you") {
		t.Fatalf("transcript not restored after the second ctrl+t:\n%s", view)
	}

	// Let the turn finish, then prove the reasoning stayed pane state only:
	// no scrollback entry carries it, and the session file does not either.
	m, next = pump(t, m, next) // TextDelta
	m, next = pump(t, m, next) // DoneEv
	m, _ = pump(t, m, next)    // StreamClosedMsg
	if m.turnActive {
		t.Fatal("turn did not end")
	}
	if got := assistantText(m); len(got) != 1 || got[0] != "The visible answer." {
		t.Fatalf("assistant entries = %#v, want exactly the streamed text", got)
	}
	for i, e := range m.entries {
		if strings.Contains(e.text, "vault layout") || strings.Contains(e.text, r2) {
			t.Fatalf("entry %d carries reasoning text; the tail is pane state only: %+v", i, e)
		}
	}
	if b, err := os.ReadFile(sessionPath(root, csID)); err == nil {
		if strings.Contains(string(b), "vault layout") {
			t.Fatal("session.ndjson carries reasoning text; reasoning is display-only in the pane")
		}
	}
}

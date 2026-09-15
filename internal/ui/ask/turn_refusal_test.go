// turn_refusal_test.go pins the submit path's refusals and boundaries: a
// changeset already open at submit is never rejected even when the turn
// stages nothing, a nil agent degrades to a visible status line, and a
// second submit during a live turn is refused, not queued.
package ask

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestSubmitWithChangesetAlreadyOpenNeverRejectsEvenIfEmpty is D-DH's other
// boundary: a changeset already open at submit is reused, and a turn that
// stages nothing under it is left alone — this pane never rejects a
// changeset it did not open itself.
func TestSubmitWithChangesetAlreadyOpenNeverRejectsEvenIfEmpty(t *testing.T) {
	_, engine, csID := liveVault(t) // a changeset is already open (case d)
	ag := &fakeTurnAgent{
		sessions: agent.NewFileSessions(engine.Vault().Root()),
		script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "anything staged?")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	// A pre-existing changeset is known synchronously at submit — this pane
	// never has to wait on the turn to learn its own bookkeeping for it.
	if m.sessionID != csID {
		t.Fatalf("sessionID after submit = %q, want the pre-existing %q", m.sessionID, csID)
	}

	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	cs, err := engine.Current()
	if err != nil {
		t.Fatalf("Current after the turn = %v, want the pre-existing changeset still open", err)
	}
	if cs.ID != csID {
		t.Fatalf("Current().ID = %q, want the pre-existing %q", cs.ID, csID)
	}
	if len(cs.Live()) != 0 {
		t.Fatalf("changeset unexpectedly has live ops: %v", cs.Live())
	}
	for _, msg := range seen {
		if sc, ok := msg.(ui.StageChangedMsg); ok && sc.ChangesetID == "" {
			t.Fatalf("empty ui.StageChangedMsg observed though the changeset predates this turn")
		}
	}
	if ag.gotSess != csID {
		t.Fatalf("Send ran with session %q, want %q", ag.gotSess, csID)
	}
}

// TestSubmitNilAgentDegradesToStatusLine is S5-T5's degrade requirement: a
// TUI that opened without a provider still opens, and asking it a question
// says so instead of dying — the echo and the status line are both visible,
// and no turn is started.
func TestSubmitNilAgentDegradesToStatusLine(t *testing.T) {
	_, engine, _ := liveVault(t) // a changeset IS open: only the agent is missing

	m := New(liveDeps(t, engine, nil)).(*Model)

	m, cmd := typeAndSubmit(t, m, "hello?")
	if cmd != nil {
		t.Fatalf("submit with a nil agent produced a command (%#v), want nil", cmd)
	}
	if m.turnActive {
		t.Fatal("nil-agent submit started a turn")
	}
	if len(m.entries) != 2 {
		t.Fatalf("entries = %#v, want the echo plus the status line", m.entries)
	}
	if m.entries[0].kind != kindUser || m.entries[0].text != "hello?" {
		t.Fatalf("entries[0] = %#v, want the user echo", m.entries[0])
	}
	if m.entries[1].kind != kindStatus || !strings.Contains(m.entries[1].text, "no agent") {
		t.Fatalf("entries[1] = %#v, want a \"no agent\" status line", m.entries[1])
	}
	if !strings.Contains(m.View(80, 10), "no agent") {
		t.Fatalf("view does not show the degrade status line:\n%s", m.View(80, 10))
	}
}

// TestSubmitWhileTurnActiveIsRefusedNotQueued documents the chosen
// concurrency rule: a second submit during a live turn is REFUSED, not
// queued — a queued question would fire at the agent mid-turn, and the
// answer that came back could not be attributed to either question. The
// refused text stays in the box so nothing the curator wrote is lost.
func TestSubmitWhileTurnActiveIsRefusedNotQueued(t *testing.T) {
	_, engine, csID := liveVault(t)
	release := make(chan struct{})
	entered := make(chan struct{})
	ag := &fakeTurnAgent{
		sessions: agent.NewFileSessions(engine.Vault().Root()),
		release:  release,
		entered:  entered,
		script: []agent.Event{
			agent.TextDelta{Text: "working"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "first question")
	if cmd == nil {
		t.Fatal("first submit produced no command")
	}
	if !m.turnActive {
		t.Fatal("first submit did not start a turn")
	}
	// Wait until that turn has really been entered — its session exists by
	// then — so the refusal below is measured against a genuinely running
	// turn rather than a race with a goroutine start.
	<-entered

	// A second submit while that turn is still running (the fake has not
	// even been released yet) must be refused, not queued.
	m, second := typeAndSubmit(t, m, "second question")
	if second != nil {
		t.Fatalf("submit during a live turn produced a command (%#v), want nil", second)
	}
	if m.input != "second question" {
		t.Fatalf("input = %q, want the refused text kept in the box", m.input)
	}
	if got := lastEntry(m); got.kind != kindStatus || !strings.Contains(got.text, "already running") {
		t.Fatalf("last entry = %#v, want a refusal status line", got)
	}
	if got := ag.sendCount(); got != 1 {
		t.Fatalf("Send called %d times, want exactly the first turn's 1", got)
	}

	// Let the first turn finish; it must stream whole and end the turn.
	close(release)
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	if got := ag.sendCount(); got != 1 {
		t.Fatalf("Send called %d times, want exactly 1", got)
	}
	if m.turnActive {
		t.Fatal("turn still active after DoneEv")
	}
	if ag.gotMsg != "first question" || ag.gotSess != csID {
		t.Fatalf("Send got (%q, %q), want (first question, %s)", ag.gotMsg, ag.gotSess, csID)
	}
	if got := assistantText(m); len(got) != 1 || got[0] != "working" {
		t.Fatalf("assistant entries = %#v, want exactly one \"working\"", got)
	}

	// And the box still holds the refused question, ready to send.
	if m.input != "second question" {
		t.Fatalf("input after the turn = %q, want the refused text still there", m.input)
	}
}

// file_hint_test.go is 009 contract §3.2's evidence: a cleanly answered turn
// whose answer cites a vault source marker leaves the ctrl+s hint under its
// terminal line (after the kept hint, when one is owed), and nothing else
// does — not an error turn, not a max_rounds turn, not an unsourced answer,
// and never a filing turn's own answer.
package ask

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/index"
)

// hintTurn runs one real turn on a fresh vault (nothing opens, and since 057
// nothing is rejected, when it stages nothing) whose scripted events are evs,
// and drains it fully.
func hintTurn(t *testing.T, evs ...agent.Event) *Model {
	t.Helper()
	_, engine := carryVault(t)
	ag := &fakeTurnAgent{sessions: agent.NewFileSessions(engine.Vault().Root()), script: evs}
	m := New(liveDeps(t, engine, ag)).(*Model)
	return submitAndDrain(t, m, "what is a kv cache?")
}

// countStatusEntries counts the kindStatus entries whose text equals text.
func countStatusEntries(m *Model, text string) int {
	n := 0
	for _, e := range m.entries {
		if e.kind == kindStatus && e.text == text {
			n++
		}
	}
	return n
}

func TestFileHint(t *testing.T) {
	t.Run("raw_marker_is_fileable", func(t *testing.T) {
		m := hintTurn(t,
			agent.TextDelta{Text: "It is the attention cache.^[raw/articles/kv-cache-explained.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)
		got := lastEntry(m)
		if got.kind != kindStatus || got.text != fileHint {
			t.Fatalf("last entry = %#v, want the file hint status line", got)
		}
	})

	t.Run("wiki_marker_is_fileable", func(t *testing.T) {
		m := hintTurn(t,
			agent.TextDelta{Text: "See the comparison page.^[wiki/concepts/y.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)
		got := lastEntry(m)
		if got.kind != kindStatus || got.text != fileHint {
			t.Fatalf("last entry = %#v, want the file hint status line", got)
		}
	})

	t.Run("no_marker_no_hint", func(t *testing.T) {
		m := hintTurn(t,
			agent.TextDelta{Text: "It is the attention cache, but nothing here is cited."},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)
		if got := countStatusEntries(m, fileHint); got != 0 {
			t.Fatalf("unsourced answer produced %d file hints, want none", got)
		}
	})

	t.Run("error_turn_no_hint", func(t *testing.T) {
		m := hintTurn(t,
			agent.TextDelta{Text: "Half an answer.^[raw/articles/kv-cache-explained.md]"},
			agent.ErrorEv{Err: errors.New("provider boom")},
		)
		if got := countStatusEntries(m, fileHint); got != 0 {
			t.Fatalf("errored turn produced %d file hints, want none", got)
		}
		// 057: the errored turn opened no changeset, so it owes no kept hint —
		// the scrollback ends with the error line itself.
		last := lastEntry(m)
		if last.kind != kindError || !strings.Contains(last.text, "provider boom") {
			t.Fatalf("last entry = %#v, want the error line (no kept hint follows it)", last)
		}
	})

	t.Run("max_rounds_no_hint", func(t *testing.T) {
		m := hintTurn(t,
			agent.TextDelta{Text: "Cut off mid-work.^[raw/articles/kv-cache-explained.md]"},
			agent.DoneEv{Reason: "max_rounds", Rounds: 24},
		)
		if got := countStatusEntries(m, fileHint); got != 0 {
			t.Fatalf("max_rounds turn produced %d file hints, want none", got)
		}
	})

	t.Run("hint_directly_after_terminal_line", func(t *testing.T) {
		// A fresh vault: the turn stages nothing and opens no changeset, so it
		// owes no kept hint (057) and the file hint lands directly under the
		// terminal line (009 contract §3.2). Was hint_after_kept_hint.
		m := hintTurn(t,
			agent.TextDelta{Text: "It is the attention cache.^[raw/articles/kv-cache-explained.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)
		if n := len(m.entries); n < 2 {
			t.Fatalf("scrollback holds %d entries, want the turn's full tail", n)
		}
		done := m.entries[len(m.entries)-2]
		hint := m.entries[len(m.entries)-1]
		if done.kind != kindStatus || done.text != "done · 1 rounds" {
			t.Fatalf("second-to-last entry = %#v, want the \"done · 1 rounds\" terminal line", done)
		}
		if hint.kind != kindStatus || hint.text != fileHint {
			t.Fatalf("last entry = %#v, want the file hint directly after the terminal line", hint)
		}
		for _, e := range m.entries {
			if strings.Contains(e.text, "nothing staged") || strings.Contains(e.text, "lw session show") {
				t.Fatalf("entry %#v is a kept hint; an empty ask no longer leaves a session to point at", e)
			}
		}
	})

	t.Run("filing_turn_answer_not_fileable", func(t *testing.T) {
		m := hintTurn(t,
			agent.TextDelta{Text: "It is the attention cache.^[raw/articles/kv-cache-explained.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)
		if got := countStatusEntries(m, fileHint); got != 1 {
			t.Fatalf("setup: first turn produced %d file hints, want exactly 1", got)
		}

		// ctrl+s starts the filing turn; its own scripted answer carries a
		// marker too, but a filing turn's answer is never fileable.
		fa, ok := m.deps.Agent.(*fakeTurnAgent)
		if !ok {
			t.Fatalf("agent is %T, want *fakeTurnAgent", m.deps.Agent)
		}
		fa.script = []agent.Event{
			agent.TextDelta{Text: "Filed. See ^[wiki/queries/kv-cache.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		}
		pane, cmd := m.Update(specialKey('s', tea.ModCtrl))
		m = pane.(*Model)
		if cmd == nil {
			t.Fatal("ctrl+s produced no command")
		}
		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model)

		if got := countStatusEntries(m, fileHint); got != 1 {
			t.Fatalf("scrollback holds %d file hints after the filing turn, want only the first turn's one", got)
		}
		// The filing turn's tail is its done line, with no file hint after it
		// (and, since 057, no kept hint: it opened no changeset either).
		last := lastEntry(m)
		if last.kind != kindStatus || last.text != "done · 1 rounds" {
			t.Fatalf("last entry = %#v, want the filing turn's done line (and no file hint after it)", last)
		}
	})

	// failed_filing_turn_keeps_answer is C-908's case: a filing turn that
	// dies with ErrorEv keeps the pair it was filing, so ctrl+s retries it
	// — on byte for byte the message the first filing turn ran on. An
	// ordinary turn's error still clears (error_turn_no_hint stays).
	t.Run("failed_filing_turn_keeps_answer", func(t *testing.T) {
		_, engine, _ := queryVault(t)
		ag := &fakeTurnAgent{sessions: agent.NewFileSessions(engine.Vault().Root())}
		m := New(liveDeps(t, engine, ag)).(*Model)

		// Turn 1 answers with a marker: fileable, exactly one hint. Its
		// records persist, so the filing turn has a conversation to seed.
		ag.script, ag.persist = turnScript(fileKeyQuestion, fileKeyAnswer)
		m = submitAndDrain(t, m, fileKeyQuestion)
		if got := countStatusEntries(m, fileHint); got != 1 {
			t.Fatalf("setup: first turn produced %d file hints, want exactly 1", got)
		}

		// ctrl+s starts the filing turn, which dies mid-stream with ErrorEv.
		ag.script = []agent.Event{
			agent.TextDelta{Text: "Staging the page."},
			agent.ErrorEv{Err: errors.New("provider boom")},
		}
		pane, cmd := pressCtrlS(m)
		m = pane.(*Model)
		if cmd == nil {
			t.Fatal("ctrl+s produced no command")
		}
		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model)
		if m.turnActive || m.filingTurn {
			t.Fatalf("the failed filing turn left turnActive %v / filingTurn %v, want both false", m.turnActive, m.filingTurn)
		}
		if !m.last.set || m.last.filing {
			t.Fatalf("recorded pair after the failed filing turn = %#v, want the pre-filing pair kept, not a filing turn's own", m.last)
		}

		// The first filing turn ran on fileMessage's exact bytes; the retry
		// must run on the same ones.
		first := ag.sentMsgs()[1]
		hits := engine.Index().Search(fileKeyQuestion, index.Options{Type: "query", Limit: 3})
		if want := fileMessage(fileKeyQuestion, fileKeyAnswer, hits); first != want {
			t.Fatalf("setup: first filing message:\n got  %q\n want %q", first, want)
		}

		pane, cmd = pressCtrlS(m)
		m = pane.(*Model)
		if cmd == nil {
			t.Fatal("retry ctrl+s produced no command")
		}
		var retried []tea.Msg
		m = runCmd(t, m, cmd, &retried).(*Model)

		msgs := ag.sentMsgs()
		if len(msgs) != 3 {
			t.Fatalf("Send ran %d times, want the question turn and two filing turns", len(msgs))
		}
		if msgs[2] != first {
			t.Fatalf("retry filing message:\n got  %q\n want the first one byte for byte: %q", msgs[2], first)
		}
	})

	// failed_filing_start_does_not_leak is C-907's case: a filing turn that
	// fails before its stream exists (turnStartedMsg's error — no
	// StreamClosedMsg can ever arrive) must drop its filing marker, or the
	// next ordinary sourced answer is permanently unfileable. Since 057 the
	// failure at start is the conversation not opening (there is no seed to
	// fail any more).
	t.Run("failed_filing_start_does_not_leak", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := askStore(root)
		ag := &fakeTurnAgent{sessions: sessions}
		m := New(liveDeps(t, engine, ag)).(*Model)

		// Turn 1 answers with a marker: fileable, exactly one hint. Its
		// records persist in the conversation.
		ag.script, ag.persist = turnScript("what is a kv cache?",
			"It is the attention cache.^[raw/articles/kv-cache-explained.md]")
		m = submitAndDrain(t, m, "what is a kv cache?")
		if got := countStatusEntries(m, fileHint); got != 1 {
			t.Fatalf("setup: first turn produced %d file hints, want exactly 1", got)
		}

		// ctrl+s starts the filing turn, but its store cannot open the
		// conversation, so the turn ends at start — before Send, with no
		// stream to close.
		ag.sessions = openFailSessions{SessionStore: sessions}
		ag.script, ag.persist = nil, nil
		pane, cmd := pressCtrlS(m)
		m = pane.(*Model)
		if cmd == nil {
			t.Fatal("ctrl+s produced no command")
		}
		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model)

		got := lastEntry(m)
		if got.kind != kindError || !strings.HasPrefix(got.text, "ask: open the conversation ") {
			t.Fatalf("filing turn's last entry = %#v, want the open failure's error line", got)
		}
		if m.turnActive || m.filingTurn {
			t.Fatalf("the failed filing turn left turnActive %v / filingTurn %v, want both false", m.turnActive, m.filingTurn)
		}

		// Turn 3 is ordinary, and its sourced answer must still be fileable.
		ag.sessions = sessions
		ag.script = []agent.Event{
			agent.TextDelta{Text: "It decodes without recomputing.^[raw/articles/kv-cache-explained.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		}
		m = submitAndDrain(t, m, "why does decoding reuse the cache?")
		if last := lastEntry(m); last.kind != kindStatus || last.text != fileHint {
			t.Fatalf("turn 3's last entry = %#v, want the file hint status line", last)
		}
	})
}

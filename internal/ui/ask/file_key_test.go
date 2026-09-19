// file_key_test.go is 009 contract §3.3's evidence: ctrl+s refuses in rule
// order (turn running, nothing recorded, answer unsourced), and otherwise
// starts exactly one filing turn whose message is fileMessage's — the
// existing query pages the search returns ride along as candidates.
package ask

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

// fileKeyQuestion is the question the query pages below are written to
// match, and the question the filing scenarios ask first.
const fileKeyQuestion = "when was the kv cache invented?"

// fileKeyAnswer is turn 1's answer, citing the raw source the minimal
// fixture carries.
const fileKeyAnswer = "The kv cache was invented alongside attention.^[raw/articles/kv-cache-explained.md]"

// queryPages is the setup MASTER's starts_filing_turn_with_message asks
// for: two type: query pages, both matching fileKeyQuestion, so
// Engine.Index().Search with Type "query" returns two hits.
var queryPages = map[string]string{
	"wiki/queries/kv-cache-history.md": "---\ntitle: When was the KV cache invented\n" +
		"created: 2026-08-20\nupdated: 2026-08-20\ntype: query\ntags: [inference]\nsources: []\nconfidence: high\n---\n\n" +
		"# When was the KV cache invented\n\nThe kv cache was invented when attention made reusing past\n" +
		"keys and values cheaper than recomputing them every token.\n",
	"wiki/queries/cache-invention.md": "---\ntitle: The invention of the cache\n" +
		"created: 2026-08-20\nupdated: 2026-08-20\ntype: query\ntags: [inference]\nsources: []\nconfidence: high\n---\n\n" +
		"# The invention of the cache\n\nWhen the kv cache was invented, decoding stopped recomputing\n" +
		"the keys and values of every earlier token.\n",
}

// queryVault copies the minimal fixture, writes the two query pages in
// before the engine opens (so the index is built over them), and opens one
// changeset the way liveVault does.
func queryVault(t *testing.T) (string, *stage.Engine, string) {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	for path, page := range queryPages {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(page), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", full, err)
		}
	}
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	cs, err := engine.OpenChangeset("ask file-key test", stage.Author{Kind: "human"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	return root, engine, cs.ID
}

// pressCtrlS delivers the file key the way a real terminal reports it: code
// and modifier only, no text — the printable path must never see it.
func pressCtrlS(m *Model) (ui.Pane, tea.Cmd) {
	return m.Update(specialKey('s', tea.ModCtrl))
}

func TestFileKey(t *testing.T) {
	t.Run("turn_active_refused", func(t *testing.T) {
		_, engine, _ := liveVault(t)
		release := make(chan struct{})
		entered := make(chan struct{})
		ag := &fakeTurnAgent{
			sessions: agent.NewFileSessions(engine.Vault().Root()),
			release:  release,
			entered:  entered,
			script:   []agent.Event{agent.TextDelta{Text: "working"}, agent.DoneEv{Reason: "stop", Rounds: 1}},
		}
		m := New(liveDeps(t, engine, ag)).(*Model)

		m, cmd := typeAndSubmit(t, m, "first question")
		if cmd == nil {
			t.Fatal("submit produced no command")
		}
		<-entered

		pane, fileCmd := pressCtrlS(m)
		m = pane.(*Model)
		if fileCmd != nil {
			t.Fatalf("ctrl+s during a live turn produced a command (%#v), want nil", fileCmd)
		}
		if got := lastEntry(m); got.kind != kindStatus ||
			got.text != "a turn is already running — submit refused, not queued" {
			t.Fatalf("last entry = %#v, want the existing turn-active refusal", got)
		}

		close(release)
		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model)
		if got := ag.sendCount(); got != 1 {
			t.Fatalf("Send ran %d times, want only the first turn's 1", got)
		}
	})

	t.Run("nothing_to_file_yet", func(t *testing.T) {
		m := New(newTestDeps(t)).(*Model)

		pane, cmd := pressCtrlS(m)
		m = pane.(*Model)
		if cmd != nil {
			t.Fatalf("ctrl+s on a fresh pane produced a command (%#v), want nil", cmd)
		}
		if m.turnActive {
			t.Fatal("ctrl+s on a fresh pane started a turn")
		}
		if got := lastEntry(m); got.kind != kindStatus ||
			got.text != "nothing to file yet: ask a question first" {
			t.Fatalf("last entry = %#v, want the nothing-recorded refusal", got)
		}
	})

	t.Run("unsourced_refused", func(t *testing.T) {
		m := feedEvents(t, New(newTestDeps(t)),
			agent.TextDelta{Text: "A plain answer with no source marker."},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)

		pane, cmd := pressCtrlS(m)
		m = pane.(*Model)
		if cmd != nil {
			t.Fatalf("ctrl+s on an unsourced answer produced a command (%#v), want nil", cmd)
		}
		if m.turnActive {
			t.Fatal("ctrl+s on an unsourced answer started a turn")
		}
		if got := lastEntry(m); got.kind != kindStatus ||
			got.text != "this answer cites no vault source; nothing to file" {
			t.Fatalf("last entry = %#v, want the unsourced refusal", got)
		}
	})

	t.Run("starts_filing_turn_with_message", func(t *testing.T) {
		_, engine, _ := queryVault(t)
		sessions := agent.NewFileSessions(engine.Vault().Root())
		ag := &fakeTurnAgent{
			sessions: sessions,
			script: []agent.Event{
				agent.TextDelta{Text: fileKeyAnswer},
				agent.DoneEv{Reason: "stop", Rounds: 1},
			},
		}
		m := New(liveDeps(t, engine, ag)).(*Model)
		m = submitAndDrain(t, m, fileKeyQuestion)

		// The candidates the pane will read off the index on the Update
		// thread: two query pages, per the frozen setup.
		hits := engine.Index().Search(fileKeyQuestion, index.Options{Type: "query", Limit: 3})
		if len(hits) != 2 {
			t.Fatalf("setup: search returned %d hits, want the two query pages:\n%+v", len(hits), hits)
		}

		pane, cmd := pressCtrlS(m)
		m = pane.(*Model)
		if cmd == nil {
			t.Fatal("ctrl+s produced no command")
		}
		if !m.turnActive || !m.filingTurn {
			t.Fatalf("ctrl+s did not start a filing turn (turnActive %v, filingTurn %v)", m.turnActive, m.filingTurn)
		}
		// The echo lands before the turn reports back, exactly as submit's
		// own echo does.
		if got := lastEntry(m); got.kind != kindUser || got.text != "file the last answer as a query page" {
			t.Fatalf("last entry = %#v, want the filing echo", got)
		}
		if m.input != "" {
			t.Fatalf("input = %q after ctrl+s, want empty", m.input)
		}

		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model)

		msgs := ag.sentMsgs()
		if len(msgs) != 2 {
			t.Fatalf("Send ran %d times, want exactly the question turn and the filing turn", len(msgs))
		}
		want := fileMessage(fileKeyQuestion, fileKeyAnswer, hits)
		if got := msgs[1]; got != want {
			t.Fatalf("filing message:\n got  %q\n want %q", got, want)
		}
		if ag.gotMsg != want {
			t.Fatalf("Send's last message = %q, want fileMessage's bytes", ag.gotMsg)
		}
	})

	t.Run("no_agent_status", func(t *testing.T) {
		// A pane with no agent records the answer from a scripted stream
		// (Deps.Agent is nil, so no real turn could have run), then ctrl+s
		// degrades exactly as submit does.
		m := feedEvents(t, New(newTestDeps(t)),
			agent.TextDelta{Text: fileKeyAnswer},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)

		pane, cmd := pressCtrlS(m)
		m = pane.(*Model)
		if cmd != nil {
			t.Fatalf("ctrl+s with no agent produced a command (%#v), want nil", cmd)
		}
		if m.turnActive {
			t.Fatal("ctrl+s with no agent started a turn")
		}
		echo := m.entries[len(m.entries)-2]
		status := lastEntry(m)
		if echo.kind != kindUser || echo.text != "file the last answer as a query page" {
			t.Fatalf("second-to-last entry = %#v, want the filing echo", echo)
		}
		if status.kind != kindStatus || !strings.Contains(status.text, "no agent is configured") {
			t.Fatalf("last entry = %#v, want the existing no-agent degrade line", status)
		}
	})
}

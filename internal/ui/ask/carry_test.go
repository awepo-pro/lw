// carry_test.go is 009 contract §3.1's evidence: a turn that runs in a
// session the pane just created is seeded with the conversation of the pane's
// most recent turn (convID), so follow-up questions see the answers they
// follow even though the previous session was archived with its changeset.
// Every turn here runs through the real pane, a real engine and a real
// NewFileSessions store; the fake agent is turn_submit_test.go's, extended
// with record persistence (liveharness_test.go).
package ask

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// carryVault copies the minimal fixture and opens a real engine on it with
// NOTHING open — the state turn 1 of every carry scenario starts from (its
// changeset is openedHere, and its auto-reject is what archives the session
// the next turn seeds from).
func carryVault(t *testing.T) (string, *stage.Engine) {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return root, engine
}

// submitAndDrain types q into the pane, submits, and drains every command
// the turn produces — the pane comes back with the turn fully over.
func submitAndDrain(t *testing.T, m *Model, q string) *Model {
	t.Helper()
	m, cmd := typeAndSubmit(t, m, q)
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	var seen []tea.Msg
	return runCmd(t, m, cmd, &seen).(*Model)
}

// sessionRecords reads id's records back through the real store, wherever
// its changeset now sits (open, committed or rejected).
func sessionRecords(t *testing.T, ss agent.SessionStore, id string) []agent.Record {
	t.Helper()
	sess, err := ss.Get(id)
	if err != nil {
		t.Fatalf("Get %s: %v", id, err)
	}
	return sess.Records
}

// turnScript builds the scripted events and the persisted records of one
// fake turn that was asked q and answered a — the user and assistant
// records Loop.Send would have written into the session store.
func turnScript(q, a string) ([]agent.Event, []agent.Record) {
	return []agent.Event{
		agent.TextDelta{Text: a},
		agent.DoneEv{Reason: "stop", Rounds: 1},
	}, []agent.Record{
		{Role: "user", Content: q},
		{Role: "assistant", Content: a},
	}
}

// stageKeepOp stages one real page through the public stage API, so a
// turn's self-opened changeset has a live op and forwardTurn does not
// reject it — the same trick TestSubmitWithNoOpenChangesetKeepsItWhenSomethingIsStaged
// uses, with the validated create_page shape session_test.go commits with.
func stageKeepOp(t *testing.T, engine *stage.Engine) {
	t.Helper()
	_, err := engine.Append(stage.Op{
		Kind: stage.OpCreatePage,
		Path: "wiki/concepts/carry-keep-page.md",
		Content: []byte("---\ntitle: Carry Keep Page\ncreated: 2026-08-29\nupdated: 2026-08-29\ntype: concept\n" +
			"tags: [inference]\nconfidence: medium\n---\n\n" +
			"# Carry Keep Page\n\nSee [[kv-cache]] and [[gpt-4]] for background.\n"),
		Rationale:  "keep the turn's changeset open",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// seedFailSessions refuses every Append — the store whose Append fails
// during the seed, so the turn must end before Send.
type seedFailSessions struct {
	agent.SessionStore
}

func (seedFailSessions) Append(id string, r agent.Record) error {
	return errors.New("append refused")
}

// wantCarried asserts one record's role, text and carried flag.
func wantCarried(t *testing.T, label string, r agent.Record, role, content string, carried bool) {
	t.Helper()
	if r.Role != role || r.Content != content || r.Carried != carried {
		t.Fatalf("%s = {%s %q carried:%v}, want {%s %q carried:%v}", label, r.Role, r.Content, r.Carried, role, content, carried)
	}
}

func TestConversationCarry(t *testing.T) {
	t.Run("answered_turn_carries_into_next", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := agent.NewFileSessions(root)
		ag := &fakeTurnAgent{sessions: sessions}
		m := New(liveDeps(t, engine, ag)).(*Model)

		ag.script, ag.persist = turnScript("q1", "A1")
		m = submitAndDrain(t, m, "q1")

		cs1 := m.convID
		if cs1 == "" {
			t.Fatal("turn 1 left convID empty")
		}
		if m.sessionID != "" {
			t.Fatalf("sessionID = %q after the auto-reject, want empty", m.sessionID)
		}

		ag.script, ag.persist = turnScript("q2", "A2")
		m = submitAndDrain(t, m, "q2")

		cs2 := m.convID
		if cs2 == "" || cs2 == cs1 {
			t.Fatalf("convID after turn 2 = %q, want a fresh session id (turn 1 ran under %q)", cs2, cs1)
		}

		recs := sessionRecords(t, sessions, cs2)
		if len(recs) != 4 {
			t.Fatalf("session %s holds %d records, want turn 1's q1/A1 carried ahead of turn 2's own q2/A2:\n%+v", cs2, len(recs), recs)
		}
		wantCarried(t, "record 0", recs[0], "user", "q1", true)
		wantCarried(t, "record 1", recs[1], "assistant", "A1", true)
		wantCarried(t, "record 2", recs[2], "user", "q2", false)
		wantCarried(t, "record 3", recs[3], "assistant", "A2", false)
	})

	t.Run("chain_carries_all_turns", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := agent.NewFileSessions(root)
		ag := &fakeTurnAgent{sessions: sessions}
		m := New(liveDeps(t, engine, ag)).(*Model)

		type turn struct{ q, a string }
		ids := make([]string, 0, 3)
		for _, tt := range []turn{{"q1", "A1"}, {"q2", "A2"}, {"q3", "A3"}} {
			ag.script, ag.persist = turnScript(tt.q, tt.a)
			m = submitAndDrain(t, m, tt.q)
			ids = append(ids, m.convID)
		}

		recs := sessionRecords(t, sessions, ids[2])
		if len(recs) != 6 {
			t.Fatalf("third session holds %d records, want turns 1 and 2 carried ahead of turn 3's own two:\n%+v", len(recs), recs)
		}
		wantCarried(t, "record 0", recs[0], "user", "q1", true)
		wantCarried(t, "record 1", recs[1], "assistant", "A1", true)
		wantCarried(t, "record 2", recs[2], "user", "q2", true)
		wantCarried(t, "record 3", recs[3], "assistant", "A2", true)
		wantCarried(t, "record 4", recs[4], "user", "q3", false)
		wantCarried(t, "record 5", recs[5], "assistant", "A3", false)
	})

	t.Run("tool_records_not_carried", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := agent.NewFileSessions(root)
		ag := &fakeTurnAgent{sessions: sessions}
		m := New(liveDeps(t, engine, ag)).(*Model)

		ag.script = []agent.Event{
			agent.TextDelta{Text: "searching"},
			agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"query":"kv cache"}`},
			agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "kv-cache: a memoization technique"},
			agent.TextDelta{Text: "A1"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		}
		ag.persist = []agent.Record{
			{Role: "user", Content: "q1"},
			{Role: "tool", Tool: "wiki.search", Args: `{"query":"kv cache"}`, Result: "kv-cache: a memoization technique"},
			{Role: "assistant", Content: "A1"},
		}
		m = submitAndDrain(t, m, "q1")

		ag.script, ag.persist = turnScript("q2", "A2")
		m = submitAndDrain(t, m, "q2")

		recs := sessionRecords(t, sessions, m.convID)
		if len(recs) != 4 {
			t.Fatalf("session holds %d records, want the two carried text records plus turn 2's own two:\n%+v", len(recs), recs)
		}
		wantCarried(t, "record 0", recs[0], "user", "q1", true)
		wantCarried(t, "record 1", recs[1], "assistant", "A1", true)
		wantCarried(t, "record 2", recs[2], "user", "q2", false)
		wantCarried(t, "record 3", recs[3], "assistant", "A2", false)
		for i, r := range recs {
			if r.Role == "tool" || r.Tool != "" {
				t.Fatalf("record %d carried tool state ({role %s tool %q}) — only conversation text is carried", i, r.Role, r.Tool)
			}
		}
	})

	t.Run("reused_changeset_not_seeded", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := agent.NewFileSessions(root)
		release := make(chan struct{})
		entered := make(chan struct{})
		ag := &fakeTurnAgent{
			sessions: sessions,
			release:  release,
			entered:  entered,
		}
		ag.script, ag.persist = turnScript("q1", "A1")
		m := New(liveDeps(t, engine, ag)).(*Model)

		m, cmd := typeAndSubmit(t, m, "q1")
		if cmd == nil {
			t.Fatal("submit produced no command")
		}
		<-entered
		stageKeepOp(t, engine)
		close(release)
		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model)

		cs1 := m.convID
		if cs1 == "" || m.sessionID != cs1 {
			t.Fatalf("convID %q / sessionID %q after a turn that staged something, want both %q", cs1, m.sessionID, cs1)
		}
		if got := len(sessionRecords(t, sessions, cs1)); got != 2 {
			t.Fatalf("session %s holds %d records after turn 1, want 2", cs1, got)
		}

		// Turn 2 finds the changeset open at submit, so it reuses cs1's
		// existing session — and a reused session is never seeded.
		ag.release = nil
		ag.entered = nil
		ag.script, ag.persist = turnScript("q2", "A2")
		m = submitAndDrain(t, m, "q2")

		if m.convID != cs1 {
			t.Fatalf("convID after turn 2 = %q, want the reused %q", m.convID, cs1)
		}
		recs := sessionRecords(t, sessions, cs1)
		if len(recs) != 4 {
			t.Fatalf("session holds %d records, want turn 1's two plus turn 2's own two:\n%+v", len(recs), recs)
		}
		for i, r := range recs {
			if r.Carried {
				t.Fatalf("record %d ({%s %q}) is Carried — a reused session is never seeded", i, r.Role, r.Content)
			}
		}
	})

	t.Run("foreign_session_not_seeded", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := agent.NewFileSessions(root)
		ag := &fakeTurnAgent{sessions: sessions}
		m := New(liveDeps(t, engine, ag)).(*Model)

		ag.script, ag.persist = turnScript("q1", "A1")
		m = submitAndDrain(t, m, "q1")

		// Another verb (`lw ingest`) opens a changeset and creates its
		// session between the pane's turns.
		cs2, err := engine.OpenChangeset("lw ingest", stage.Author{Kind: "agent"})
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		if _, err := sessions.Create(cs2.ID); err != nil {
			t.Fatalf("Create: %v", err)
		}

		ag.script, ag.persist = turnScript("q2", "A2")
		m = submitAndDrain(t, m, "q2")

		if m.convID != cs2.ID {
			t.Fatalf("convID after turn 2 = %q, want the ingest changeset %q", m.convID, cs2.ID)
		}
		recs := sessionRecords(t, sessions, cs2.ID)
		if len(recs) != 2 {
			t.Fatalf("session holds %d records, want only turn 2's own two:\n%+v", len(recs), recs)
		}
		wantCarried(t, "record 0", recs[0], "user", "q2", false)
		wantCarried(t, "record 1", recs[1], "assistant", "A2", false)
	})

	t.Run("seed_error_fails_turn", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := agent.NewFileSessions(root)
		ag := &fakeTurnAgent{sessions: sessions}
		m := New(liveDeps(t, engine, ag)).(*Model)

		ag.script, ag.persist = turnScript("q1", "A1")
		m = submitAndDrain(t, m, "q1")

		// Turn 2's store refuses every Append, so seeding its fresh session
		// from turn 1's cannot write: the turn must end before Send.
		ag.sessions = seedFailSessions{SessionStore: agent.NewFileSessions(root)}
		ag.script, ag.persist = nil, nil
		sendsBefore := ag.sendCount()

		m = submitAndDrain(t, m, "q2")

		if got := ag.sendCount(); got != sendsBefore {
			t.Fatalf("Send ran %d more times after the seed failure (had %d), want no Send at all", got-sendsBefore, sendsBefore)
		}
		got := lastEntry(m)
		if got.kind != kindError {
			t.Fatalf("last entry = %#v, want the seed failure's error line", got)
		}
		if !strings.HasPrefix(got.text, "ask: carry the conversation into ") {
			t.Fatalf("error line = %q, want the \"ask: carry the conversation into \" prefix", got.text)
		}
		if m.turnActive {
			t.Fatal("the failed turn left the pane marked active")
		}
	})

	// model_sees_previous_answer runs the real agent.Loop over a fake
	// provider (carry_loop_test.go) and asserts the second request's
	// messages contain turn 1's answer — the wire-level point of seeding.
	t.Run("model_sees_previous_answer", func(t *testing.T) {
		modelSeesPreviousAnswer(t)
	})
}

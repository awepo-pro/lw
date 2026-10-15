// carry_test.go is 009 contract §3.1's evidence, restated for 057: follow-up
// questions see the answers they follow. Under 009 each turn ran in a session
// the pane created and seeded from the previous turn's, because the previous
// session was archived with its changeset; since 057 every turn runs in the
// pane's ONE in-memory conversation (agent.NewAskSessions), whose history view
// is that same prose, so nothing is seeded and nothing is lost. Every turn here
// runs through the real pane, a real engine and the real store; the fake agent
// is turn_submit_test.go's, extended with record persistence
// (liveharness_test.go).
package ask

import (
	"errors"
	"fmt"
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

// openFailSessions cannot open a conversation: Get finds nothing and Create
// refuses — 057's version of the store whose seed failed, a turn that must end
// before Send.
type openFailSessions struct {
	agent.SessionStore
}

func (openFailSessions) Get(id string) (*agent.Session, error) {
	return nil, errors.New("no such conversation")
}

func (openFailSessions) Create(id string) (*agent.Session, error) {
	return nil, errors.New("create refused")
}

// askStore is the production store shape over root: the in-memory ask
// conversation above the file store.
func askStore(root string) agent.SessionStore {
	return agent.NewAskSessions(agent.NewFileSessions(root))
}

// turnRecords lists the conversation's full log, tool records included — what
// the adopt step reads — where sessionRecords on this store returns the prose
// view the model is sent.
func turnRecords(t *testing.T, ss agent.SessionStore, id string) []agent.Record {
	t.Helper()
	recs, err := agent.RawRecords(ss, id)
	if err != nil {
		t.Fatalf("RawRecords %s: %v", id, err)
	}
	return recs
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
		sessions := askStore(root)
		ag := &fakeTurnAgent{sessions: sessions}
		m := New(liveDeps(t, engine, ag)).(*Model)

		ag.script, ag.persist = turnScript("q1", "A1")
		m = submitAndDrain(t, m, "q1")

		cs1 := m.convID
		if cs1 == "" {
			t.Fatal("turn 1 left convID empty")
		}

		ag.script, ag.persist = turnScript("q2", "A2")
		m = submitAndDrain(t, m, "q2")

		// One conversation for both turns (057): no second session, no seed.
		if m.convID != cs1 {
			t.Fatalf("convID after turn 2 = %q, want the same conversation %q", m.convID, cs1)
		}
		raw := turnRecords(t, sessions, cs1)
		if len(raw) != 4 {
			t.Fatalf("conversation %s holds %d records, want turn 1's q1/A1 then turn 2's own q2/A2:\n%+v", cs1, len(raw), raw)
		}
		wantCarried(t, "record 0", raw[0], "user", "q1", false)
		wantCarried(t, "record 1", raw[1], "assistant", "A1", false)
		wantCarried(t, "record 2", raw[2], "user", "q2", false)
		wantCarried(t, "record 3", raw[3], "assistant", "A2", false)

		// What the next turn's model reads is the prose view: the same four
		// records 009's seeding would have carried.
		view := sessionRecords(t, sessions, cs1)
		if len(view) != 4 {
			t.Fatalf("history view holds %d records, want 4:\n%+v", len(view), view)
		}
		wantCarried(t, "view 0", view[0], "user", "q1", true)
		wantCarried(t, "view 1", view[1], "assistant", "A1", true)
		wantCarried(t, "view 2", view[2], "user", "q2", true)
		wantCarried(t, "view 3", view[3], "assistant", "A2", true)
	})

	t.Run("chain_carries_all_turns", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := askStore(root)
		ag := &fakeTurnAgent{sessions: sessions}
		m := New(liveDeps(t, engine, ag)).(*Model)

		type turn struct{ q, a string }
		ids := make([]string, 0, 3)
		for _, tt := range []turn{{"q1", "A1"}, {"q2", "A2"}, {"q3", "A3"}} {
			ag.script, ag.persist = turnScript(tt.q, tt.a)
			m = submitAndDrain(t, m, tt.q)
			ids = append(ids, m.convID)
		}
		if ids[0] == "" || ids[1] != ids[0] || ids[2] != ids[0] {
			t.Fatalf("convIDs across three turns = %v, want one conversation", ids)
		}

		recs := sessionRecords(t, sessions, ids[2])
		if len(recs) != 6 {
			t.Fatalf("history view holds %d records, want all three turns:\n%+v", len(recs), recs)
		}
		for i, want := range []struct{ role, content string }{
			{"user", "q1"}, {"assistant", "A1"}, {"user", "q2"}, {"assistant", "A2"}, {"user", "q3"}, {"assistant", "A3"},
		} {
			wantCarried(t, fmt.Sprintf("record %d", i), recs[i], want.role, want.content, true)
		}
	})

	t.Run("tool_records_not_carried", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := askStore(root)
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

		// The conversation keeps the tool record (the adopt step needs it) ...
		if raw := turnRecords(t, sessions, m.convID); len(raw) != 5 {
			t.Fatalf("conversation holds %d records, want q1, the tool record, A1, q2, A2:\n%+v", len(raw), raw)
		}
		// ... and the history the model is sent never replays it (046's pairs
		// would grow the context without bound; Compact never shrinks them).
		recs := sessionRecords(t, sessions, m.convID)
		if len(recs) != 4 {
			t.Fatalf("history view holds %d records, want the four text records:\n%+v", len(recs), recs)
		}
		wantCarried(t, "record 0", recs[0], "user", "q1", true)
		wantCarried(t, "record 1", recs[1], "assistant", "A1", true)
		wantCarried(t, "record 2", recs[2], "user", "q2", true)
		wantCarried(t, "record 3", recs[3], "assistant", "A2", true)
		for i, r := range recs {
			if r.Role == "tool" || r.Tool != "" {
				t.Fatalf("record %d carried tool state ({role %s tool %q}) — only conversation text is carried", i, r.Role, r.Tool)
			}
		}
	})

	// reused_changeset_not_seeded: a turn that stages is adopted into its
	// changeset's session, and a later turn staging into the SAME changeset
	// appends to that session — it is never re-seeded with the conversation
	// (009's "a reused session is never seeded", as 057 keeps it).
	t.Run("reused_changeset_not_seeded", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := askStore(root)
		ag := &fakeTurnAgent{sessions: sessions}
		var csID string
		ag.mid = func() []agent.Event {
			if csID == "" {
				cs, _, err := engine.OpenOrJoin("ask: q1", stage.Author{Kind: "agent"})
				if err != nil {
					t.Errorf("OpenOrJoin: %v", err)
				}
				csID = cs.ID
				stageKeepOp(t, engine)
			}
			return []agent.Event{agent.StageEv{ChangesetID: csID, Ops: 1}}
		}
		ag.script, ag.persist = turnScript("q1", "A1")
		m := New(liveDeps(t, engine, ag)).(*Model)
		m = submitAndDrain(t, m, "q1")

		if csID == "" || m.stagedID != csID {
			t.Fatalf("stagedID %q after a turn that staged into %q", m.stagedID, csID)
		}
		first := sessionRecords(t, agent.NewFileSessions(root), csID)
		if len(first) != 2 {
			t.Fatalf("changeset session holds %d records after turn 1, want the turn's 2:\n%+v", len(first), first)
		}

		// Turn 2 stages into the same still-open changeset.
		ag.script, ag.persist = turnScript("q2", "A2")
		m = submitAndDrain(t, m, "q2")

		recs := sessionRecords(t, agent.NewFileSessions(root), csID)
		if len(recs) != 4 {
			t.Fatalf("changeset session holds %d records, want turn 1's two plus turn 2's own two:\n%+v", len(recs), recs)
		}
		for i, r := range recs {
			if r.Carried {
				t.Fatalf("record %d ({%s %q}) is Carried — an existing session is never seeded", i, r.Role, r.Content)
			}
		}
		wantCarried(t, "record 2", recs[2], "user", "q2", false)
		wantCarried(t, "record 3", recs[3], "assistant", "A2", false)
	})

	t.Run("foreign_session_not_seeded", func(t *testing.T) {
		root, engine := carryVault(t)
		sessions := askStore(root)
		ag := &fakeTurnAgent{sessions: sessions}
		m := New(liveDeps(t, engine, ag)).(*Model)

		ag.script, ag.persist = turnScript("q1", "A1")
		m = submitAndDrain(t, m, "q1")

		// Another verb (`lw ingest`) opens a changeset and creates its
		// session between the pane's turns, and writes its own record.
		cs2, err := engine.OpenChangeset("lw ingest", stage.Author{Kind: "agent"})
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		if _, err := sessions.Create(cs2.ID); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := sessions.Append(cs2.ID, agent.Record{Role: "user", Content: "ingest a.md"}); err != nil {
			t.Fatalf("Append: %v", err)
		}

		// Turn 2 stages into that changeset: it joins the ingest's session,
		// appending only its own records.
		ag.script, ag.persist = turnScript("q2", "A2")
		ag.script = append([]agent.Event{agent.StageEv{ChangesetID: cs2.ID, Ops: 1}}, ag.script...)
		m = submitAndDrain(t, m, "q2")

		recs := sessionRecords(t, agent.NewFileSessions(root), cs2.ID)
		if len(recs) != 3 {
			t.Fatalf("session holds %d records, want the ingest's one and only turn 2's own two:\n%+v", len(recs), recs)
		}
		wantCarried(t, "record 0", recs[0], "user", "ingest a.md", false)
		wantCarried(t, "record 1", recs[1], "user", "q2", false)
		wantCarried(t, "record 2", recs[2], "assistant", "A2", false)
	})

	// conversation_open_error_fails_turn replaces 009's seed_error_fails_turn:
	// there is no seed to fail any more, but a store that cannot open the
	// conversation must still end the turn before Send with its error line.
	t.Run("conversation_open_error_fails_turn", func(t *testing.T) {
		root, engine := carryVault(t)
		ag := &fakeTurnAgent{sessions: openFailSessions{SessionStore: askStore(root)}}
		m := New(liveDeps(t, engine, ag)).(*Model)

		m = submitAndDrain(t, m, "q1")

		if got := ag.sendCount(); got != 0 {
			t.Fatalf("Send ran %d times after the open failure, want no Send at all", got)
		}
		got := lastEntry(m)
		if got.kind != kindError {
			t.Fatalf("last entry = %#v, want the open failure's error line", got)
		}
		if !strings.HasPrefix(got.text, "ask: open the conversation ") {
			t.Fatalf("error line = %q, want the \"ask: open the conversation \" prefix", got.text)
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

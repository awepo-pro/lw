// turn_submit_test.go drives the submit path (C-124/D-DH, as amended by 057): a
// submitted question starts one real turn in the pane's in-memory conversation;
// a turn with no open changeset opens none, whether or not it answers, and a
// turn that stages keeps the changeset it staged into. (C-124/D-DH had the turn
// open its own changeset and reject it when it staged nothing; 057 A-057-1
// rewrote these four tests to the contract that replaced it.)
package ask

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestSubmitStartsRealTurnAndStreamsScript is S5-T5's headline: submitting a
// question starts one turn on the real Deps.Agent, and its events reach the
// pane as tea.Cmds in order — deltas accumulate, the tool call renders as
// one collapsed line, StageEv reaches the shell as ui.StageChangedMsg, and
// DoneEv closes the turn.
func TestSubmitStartsRealTurnAndStreamsScript(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewAskSessions(agent.NewFileSessions(root))}
	ag := &fakeTurnAgent{
		sessions: sessions,
		script: []agent.Event{
			agent.TextDelta{Text: "hello "},
			agent.TextDelta{Text: "world"},
			agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"kv cache"}`},
			agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "kv-cache: a memoization technique", IsError: false},
			agent.StageEv{ChangesetID: csID, Ops: 1},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "what is a kv cache?")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	if !m.turnActive {
		t.Fatal("submit did not mark the turn active")
	}
	if got := lastEntry(m); got.kind != kindUser || got.text != "what is a kv cache?" {
		t.Fatalf("last entry = %#v, want the user echo", got)
	}

	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	// The agent was called once, with the question and the pane's one
	// conversation (057 D3) — not the open changeset's id, which names no
	// session any more.
	if got := ag.sendCount(); got != 1 {
		t.Fatalf("Send called %d times, want 1", got)
	}
	if ag.gotMsg != "what is a kv cache?" {
		t.Fatalf("Send got msg %q, want the submitted question", ag.gotMsg)
	}
	if !strings.HasPrefix(ag.gotSess, "ask-") || ag.gotSess != m.convID || ag.gotSess == csID {
		t.Fatalf("Send got session %q, want the pane's ask- conversation %q (not the changeset %q)", ag.gotSess, m.convID, csID)
	}

	// The pane created that conversation through the store before sending.
	if _, err := sessions.Get(ag.gotSess); err != nil {
		t.Fatalf("the conversation was not created: %v", err)
	}
	// The script's StageEv names the open changeset: the turn staged into it, so
	// the adopt step (D4) put the turn into its session.ndjson.
	if _, err := os.Stat(sessionPath(root, csID)); err != nil {
		t.Fatalf("the turn that staged was not adopted into the changeset's session: %v", err)
	}
	if got := sessions.closedIDs(); len(got) != 0 {
		t.Fatalf("session closed %v before any commit/reject, want no close", got)
	}

	// Deltas accumulated into exactly one assistant message, in order.
	if got := assistantText(m); len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("assistant entries = %#v, want exactly one \"hello world\"", got)
	}

	// The tool call is one resolved, still-collapsed line.
	var tools []*toolCall
	for _, e := range m.entries {
		if e.kind == kindTool {
			tools = append(tools, e.tool)
		}
	}
	if len(tools) != 1 || !tools[0].resolved || tools[0].isError || tools[0].expanded {
		t.Fatalf("tool entries = %#v, want one resolved collapsed call", tools)
	}

	// StageEv surfaced as ui.StageChangedMsg, fields unchanged.
	var gotStage *ui.StageChangedMsg
	for _, msg := range seen {
		if sc, ok := msg.(ui.StageChangedMsg); ok {
			sc := sc
			gotStage = &sc
		}
	}
	if gotStage == nil || gotStage.ChangesetID != csID || gotStage.Ops != 1 {
		t.Fatalf("ui.StageChangedMsg = %#v, want {%s 1}", gotStage, csID)
	}

	// DoneEv closed the turn with its status line — the frozen
	// turn-boundary text, `done · N rounds` from DoneEv.Rounds
	// (s2-screens.md T08).
	if m.turnActive {
		t.Fatal("turn still active after DoneEv")
	}
	if got := lastEntry(m); got.kind != kindStatus || got.text != "done · 1 rounds" {
		t.Fatalf("last entry = %#v, want the \"done · 1 rounds\" status line", got)
	}
}

// TestSubmitWithNoOpenChangesetOpensNoneDuringTheTurn is C-124/D-DH's headline
// turned over by 057: a fresh vault with no open changeset still does not refuse
// the submit, and the turn no longer opens one either — mid-flight, with Send
// running, nothing is open and nothing is on disk, because a changeset exists
// only once a turn stages.
func TestSubmitWithNoOpenChangesetOpensNoneDuringTheTurn(t *testing.T) {
	root, engine := carryVault(t)

	if _, err := engine.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Fatalf("fixture already has an open changeset: %v", err)
	}

	release := make(chan struct{})
	entered := make(chan struct{})
	ag := &fakeTurnAgent{
		sessions: agent.NewAskSessions(agent.NewFileSessions(root)),
		release:  release,
		entered:  entered,
		script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "what should I stage?")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	if !m.turnActive {
		t.Fatal("submit did not mark the turn active")
	}

	<-entered // runTurn opened the conversation and Agent.Send has started

	if cs, err := engine.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Fatalf("Current mid-turn = %+v, %v; want ErrNoChangeset (the turn opens a changeset only when it stages)", cs, err)
	}
	if dirs := changesetDirNames(t, root); len(dirs) != 0 {
		t.Fatalf("changeset directories mid-turn = %v, want none", dirs)
	}
	if !strings.HasPrefix(ag.gotSess, "ask-") || ag.gotSess != m.convID {
		t.Fatalf("Send ran with session %q, want the pane's ask- conversation %q", ag.gotSess, m.convID)
	}

	close(release)
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)
	if m.turnActive {
		t.Fatal("turn still active after DoneEv")
	}
}

// TestSubmitWithNoOpenChangesetLeavesNothingWhenNothingStaged is D-DH's cleanup
// half under 057: a turn that stages nothing leaves nothing to clean up — no
// changeset in any state, no session file, no journal byte, no StageChangedMsg
// and no hint — so a curious "what's in this vault?" costs the vault nothing,
// instead of an opened/rejected pair, a rejected/ directory and a sync push.
func TestSubmitWithNoOpenChangesetLeavesNothingWhenNothingStaged(t *testing.T) {
	root, engine := carryVault(t)
	journal := journalBytes(t, root)

	sessions := &recordingSessions{SessionStore: agent.NewAskSessions(agent.NewFileSessions(root))}
	ag := &fakeTurnAgent{
		sessions: sessions,
		script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "nothing to stage here")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	if _, err := engine.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Fatalf("Current after an empty turn = %v, want ErrNoChangeset", err)
	}
	if dirs := changesetDirNames(t, root); len(dirs) != 0 {
		t.Fatalf("an empty turn left changeset directories %v, want none (was: one rejected/ directory)", dirs)
	}
	if after := journalBytes(t, root); !bytes.Equal(after, journal) {
		t.Fatalf("an empty turn wrote to the journal:\n before %q\n after  %q", journal, after)
	}
	if got := sessions.closedIDs(); len(got) != 0 {
		t.Fatalf("closed = %v, want nothing closed (the conversation outlives the turn)", got)
	}
	// Was: the empty ui.StageChangedMsg the auto-reject broadcast. There is no
	// reject any more, so nothing is broadcast.
	if sc := stageChangedMsgs(seen); len(sc) != 0 {
		t.Fatalf("an empty turn broadcast ui.StageChangedMsg %+v, want none", sc)
	}
	if m.turnActive {
		t.Fatal("turn still marked active after DoneEv")
	}
	// R-509's `nothing staged` hint is gone with the changeset it described, so
	// the status line this test has always seen is the last entry again.
	if got := lastEntry(m); got.kind != kindStatus || got.text != "done · 1 rounds" {
		t.Fatalf("last entry = %#v, want the \"done · 1 rounds\" status line", got)
	}
	if m.convID == "" || m.stagedID != "" {
		t.Fatalf("convID %q / stagedID %q after an empty turn, want a conversation and no staged changeset", m.convID, m.stagedID)
	}
}

// TestSubmitWithNoOpenChangesetKeepsItWhenSomethingIsStaged is the other half
// of D-DH's rule under 057: a turn that stages something — the changeset opened
// mid-flight the way the loop's lazy open does it (simulated here with
// OpenOrJoin and a direct Engine.Append while the turn is parked) — leaves that
// changeset open for review, exactly as if it had been open all along, and the
// pane remembers it as the changeset the turn staged into.
func TestSubmitWithNoOpenChangesetKeepsItWhenSomethingIsStaged(t *testing.T) {
	root, engine := carryVault(t)

	release := make(chan struct{})
	reached := make(chan struct{})
	ag := &gatedAgent{
		sessions: agent.NewAskSessions(agent.NewFileSessions(root)),
		reached:  reached,
		release:  release,
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "stage a small edit for me")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	<-reached

	opened, _, err := engine.OpenOrJoin("ask: stage a small edit for me", stage.Author{Kind: "agent"})
	if err != nil {
		t.Fatalf("OpenOrJoin: %v", err)
	}
	stageKVCachePatch(t, engine)
	ag.rest = []agent.Event{
		agent.StageEv{ChangesetID: opened.ID, Ops: 1},
		agent.DoneEv{Reason: "stop", Rounds: 1},
	}

	close(release)
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	cs, err := engine.Current()
	if err != nil {
		t.Fatalf("Current after a turn that staged something = %v, want the changeset still open", err)
	}
	if len(cs.Live()) == 0 {
		t.Fatal("changeset has no live ops after staging one")
	}
	for _, msg := range seen {
		if sc, ok := msg.(ui.StageChangedMsg); ok && sc.ChangesetID == "" {
			t.Fatalf("empty ui.StageChangedMsg observed though the turn staged something")
		}
	}
	if m.stagedID != cs.ID {
		t.Fatalf("pane stagedID = %q, want the changeset the turn staged into %q", m.stagedID, cs.ID)
	}
	if !strings.HasPrefix(m.convID, "ask-") {
		t.Fatalf("pane convID = %q, want the ask- conversation", m.convID)
	}
}

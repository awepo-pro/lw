// turn_submit_test.go drives the submit path's changeset resolution
// (C-124/D-DH): a submitted question starts one real turn; a turn with no
// open changeset opens one mid-flight, rejects it when it staged nothing,
// and keeps it when something was staged.
package ask

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestSubmitStartsRealTurnAndStreamsScript is S5-T5's headline: submitting a
// question starts one turn on the real Deps.Agent, and its events reach the
// pane as tea.Cmds in order — deltas accumulate, the tool call renders as
// one collapsed line, StageEv reaches the shell as ui.StageChangedMsg, and
// DoneEv closes the turn.
func TestSubmitStartsRealTurnAndStreamsScript(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewFileSessions(root)}
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

	// The agent was called once, with the question and the changeset's own
	// session id (backbone §9, C-102 — never an ephemeral session).
	if got := ag.sendCount(); got != 1 {
		t.Fatalf("Send called %d times, want 1", got)
	}
	if ag.gotMsg != "what is a kv cache?" {
		t.Fatalf("Send got msg %q, want the submitted question", ag.gotMsg)
	}
	if ag.gotSess != csID {
		t.Fatalf("Send got session %q, want the open changeset %q", ag.gotSess, csID)
	}

	// The pane created that session through the real store before sending.
	if _, err := os.Stat(sessionPath(root, csID)); err != nil {
		t.Fatalf("session file was not created beside the changeset: %v", err)
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

// TestSubmitWithNoOpenChangesetOpensOneDuringTheTurn is C-124/D-DH's
// headline: a fresh vault with no open changeset no longer refuses the
// submit. The turn itself opens one, and it is genuinely open — with a
// session file beside it — while Send is still running, not merely after
// the fact.
func TestSubmitWithNoOpenChangesetOpensOneDuringTheTurn(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer engine.Close()

	if _, err := engine.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Fatalf("fixture already has an open changeset: %v", err)
	}

	release := make(chan struct{})
	entered := make(chan struct{})
	ag := &fakeTurnAgent{
		sessions: agent.NewFileSessions(root),
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

	<-entered // runTurn resolved a changeset and Agent.Send has started

	cs, err := engine.Current()
	if err != nil {
		t.Fatalf("Current mid-turn: %v", err)
	}
	if !strings.HasPrefix(cs.Intent, "ask: ") {
		t.Fatalf("Intent = %q, want an \"ask: \" prefix (C-124/D-DH)", cs.Intent)
	}
	if cs.Author.Kind != "agent" {
		t.Fatalf("Author.Kind = %q, want \"agent\"", cs.Author.Kind)
	}
	if ag.gotSess != cs.ID {
		t.Fatalf("Send ran with session %q, want the opened changeset %q", ag.gotSess, cs.ID)
	}
	if _, err := os.Stat(sessionPath(root, cs.ID)); err != nil {
		t.Fatalf("session file was not created beside the opened changeset: %v", err)
	}

	close(release)
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)
	if m.turnActive {
		t.Fatal("turn still active after DoneEv")
	}
}

// TestSubmitWithNoOpenChangesetRejectsWhenNothingStaged is D-DH's cleanup
// half: a turn that opened its own changeset and staged nothing rejects it
// once Send returns, so a curious "what's in this vault?" question never
// leaves an empty changeset sitting in changesets/open/ for review to have
// to notice and reject by hand.
func TestSubmitWithNoOpenChangesetRejectsWhenNothingStaged(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer engine.Close()

	sessions := &recordingSessions{SessionStore: agent.NewFileSessions(root)}
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
		t.Fatalf("Current after an empty self-opened turn = %v, want ErrNoChangeset", err)
	}

	rejectedDir := filepath.Join(root, ".llmwiki", "changesets", "rejected")
	entries, err := os.ReadDir(rejectedDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", rejectedDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("changesets/rejected/ has %d entries, want exactly 1: %v", len(entries), entries)
	}
	rejectedID := entries[0].Name()
	if _, err := os.Stat(filepath.Join(rejectedDir, rejectedID, "session.ndjson")); err != nil {
		t.Fatalf("session did not travel with the rejected changeset: %v", err)
	}
	if got := sessions.closedIDs(); len(got) != 1 || got[0] != rejectedID {
		t.Fatalf("closed = %v, want exactly [%s]", got, rejectedID)
	}

	// The badge refresh: the same empty ui.StageChangedMsg review's own
	// Commit/Reject broadcasts (backbone §12) was observed here too, and it
	// arrived before the turn's own terminal status line.
	var gotEmptyStage bool
	for _, msg := range seen {
		if sc, ok := msg.(ui.StageChangedMsg); ok && sc.ChangesetID == "" {
			gotEmptyStage = true
		}
	}
	if !gotEmptyStage {
		t.Fatalf("no empty ui.StageChangedMsg observed after the auto-reject; seen = %#v", seen)
	}
	if m.turnActive {
		t.Fatal("turn still marked active after DoneEv")
	}
	// R-509: the `nothing staged` hint follows the turn's terminal line, so
	// the status line this test has always seen last is now second to last.
	if n := len(m.entries); n < 2 {
		t.Fatalf("the scrollback holds %d entries, want at least the done line and its hint", n)
	}
	done := m.entries[len(m.entries)-2]
	if done.kind != kindStatus || done.text != "done · 1 rounds" {
		t.Fatalf("entry before the last = %#v, want the \"done · 1 rounds\" status line", done)
	}
	if m.sessionID != "" {
		t.Fatalf("pane sessionID = %q after the reject, want empty", m.sessionID)
	}
}

// TestSubmitWithNoOpenChangesetKeepsItWhenSomethingIsStaged is the other
// half of D-DH's rule: a turn that opened its own changeset but DID stage
// something (simulated here the same way review's own tests do — a direct
// Engine.Append while the turn is parked mid-flight) leaves that changeset
// open for review, exactly as if it had been open all along.
func TestSubmitWithNoOpenChangesetKeepsItWhenSomethingIsStaged(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer engine.Close()

	release := make(chan struct{})
	entered := make(chan struct{})
	ag := &fakeTurnAgent{
		sessions: agent.NewFileSessions(root),
		release:  release,
		entered:  entered,
		script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "stage a small edit for me")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	<-entered

	page, ok := engine.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	oldFlash := "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newFlash := "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
	if !strings.Contains(page.Body, oldFlash) {
		t.Fatalf("fixture body does not contain the expected line:\n%s", page.Body)
	}
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldFlash, newFlash, 1)
	if _, err := engine.Append(stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      page.Path,
		Section:   "## Related",
		Before:    page.SHA256(),
		Content:   rewritten.Serialize(),
		Rationale: "the turn staged this before finishing",
		Hunks: []stage.Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldFlash}, Add: []string{newFlash}},
		},
	}); err != nil {
		t.Fatalf("Append: %v", err)
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
	if m.sessionID != cs.ID {
		t.Fatalf("pane sessionID = %q, want the still-open changeset %q", m.sessionID, cs.ID)
	}
}

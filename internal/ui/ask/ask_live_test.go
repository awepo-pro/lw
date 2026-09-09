// ask_live_test.go drives the real submit path (s5-agent-loop.md S5-T5): a
// scripted agent.Agent standing in for agent.Loop, a real file-backed
// SessionStore beside a real *stage.Engine, and no network, no LLM and no
// provider key anywhere. Everything here is local-disk and deterministic —
// the only asynchronous actor is the goroutine startTurn spawns, and the
// channel contract it depends on (backbone §9, C-105: the caller makes the
// channel, Send closes it on every exit path) is what makes the runs
// orderable.
package ask

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

// fakeTurnAgent is the scripted agent.Agent these tests install as
// Deps.Agent. Send records the turn it was given, waits on release when one
// is set — the pause a "mid-turn" assertion needs — then replays script on
// out and closes it, exactly the ownership contract Loop.Send honours
// (backbone §9, C-105). Sessions returns the store the test built, so the
// pane exercises the real file-backed session store rather than a stub of
// it.
//
// entered is closed as Send's very first action. runTurn resolves the
// session before it calls Send, so a test waiting on entered knows the
// turn's session file exists — the synchronisation that keeps a test's
// commit/reject from racing the goroutine startTurn spawned.
type fakeTurnAgent struct {
	mu       sync.Mutex
	script   []agent.Event
	release  chan struct{}
	entered  chan struct{}
	sends    int
	gotMsg   string
	gotSess  string
	sessions agent.SessionStore
}

func (f *fakeTurnAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	f.mu.Lock()
	f.sends++
	f.gotMsg = msg
	f.gotSess = sessionID
	script := append([]agent.Event(nil), f.script...)
	f.mu.Unlock()

	if f.entered != nil {
		close(f.entered)
	}
	if f.release != nil {
		<-f.release
	}

	for _, ev := range script {
		select {
		case out <- ev:
		case <-ctx.Done():
			close(out)
			return ctx.Err()
		}
	}
	close(out)
	return nil
}

func (f *fakeTurnAgent) Sessions() agent.SessionStore { return f.sessions }

// sendCount reports how many times Send has been entered.
func (f *fakeTurnAgent) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends
}

// recordingSessions wraps a SessionStore and records every Close, so a test
// can prove the pane archives a turn's session at the right moment — Close
// itself is only a directory fsync, with nothing to assert on afterwards.
type recordingSessions struct {
	agent.SessionStore

	mu     sync.Mutex
	closed []string
	errs   []error
}

func (r *recordingSessions) Close(id string) error {
	r.mu.Lock()
	r.closed = append(r.closed, id)
	r.mu.Unlock()

	err := r.SessionStore.Close(id)

	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
	return err
}

// closedIDs returns every id Close was called with, in order.
func (r *recordingSessions) closedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.closed...)
}

// closeErr returns the error the i'th Close call returned.
func (r *recordingSessions) closeErr(i int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.errs) {
		return nil
	}
	return r.errs[i]
}

// liveVault copies the minimal fixture, opens a real engine on it and opens
// one changeset, so a submitted question has a session to run under
// (backbone §9, C-102: a session is keyed by its changeset).
func liveVault(t *testing.T) (root string, e *stage.Engine, csID string) {
	t.Helper()
	root = testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })

	cs, err := engine.OpenChangeset("ask pane live test", stage.Author{Kind: "human"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	return root, engine, cs.ID
}

// liveDeps is newTestDeps plus the engine and agent a live turn needs.
func liveDeps(t *testing.T, e *stage.Engine, ag agent.Agent) ui.Deps {
	t.Helper()
	d := newTestDeps(t)
	d.Engine = e
	d.Agent = ag
	return d
}

// typeAndSubmit types msg into the pane's input box and presses enter,
// returning the model and the tea.Cmd the submit produced (nil when the
// submit was refused).
func typeAndSubmit(t *testing.T, m *Model, msg string) (*Model, tea.Cmd) {
	t.Helper()
	var pane ui.Pane = m
	for _, r := range msg {
		var cmd tea.Cmd
		pane, cmd = pane.Update(keyPress(r))
		if cmd != nil {
			t.Fatalf("typing %q produced a command", string(r))
		}
	}
	pane, cmd := pane.Update(specialKey(tea.KeyEnter, 0))
	return pane.(*Model), cmd
}

// lastEntry returns the scrollback's last entry.
func lastEntry(m *Model) entry { return m.entries[len(m.entries)-1] }

// assistantText returns every kindAssistant entry's text, in order.
func assistantText(m *Model) []string {
	var out []string
	for _, e := range m.entries {
		if e.kind == kindAssistant {
			out = append(out, e.text)
		}
	}
	return out
}

// sessionPath is where the file-backed store puts id's transcript (backbone
// §9, C-102) — the changeset's own directory, beside changeset.json.
func sessionPath(root, id string) string {
	return filepath.Join(root, ".llmwiki", "changesets", "open", id, "session.ndjson")
}

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

	// DoneEv closed the turn with its status line.
	if m.turnActive {
		t.Fatal("turn still active after DoneEv")
	}
	if got := lastEntry(m); got.kind != kindStatus || !strings.Contains(got.text, "done: stop") {
		t.Fatalf("last entry = %#v, want a \"done: stop\" status line", got)
	}
}

// TestSubmitRefusedWithNoOpenChangeset pins the third refusal: a session is
// keyed by its changeset (backbone §9, C-102), so with no changeset open
// there is nothing for a turn to run in — the submit is refused with a
// visible status line, never silently and never by starting a turn.
func TestSubmitRefusedWithNoOpenChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer engine.Close()

	ag := &fakeTurnAgent{sessions: agent.NewFileSessions(root)}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "anyone there?")
	if cmd != nil {
		t.Fatalf("submit with no open changeset produced a command (%#v), want nil", cmd)
	}
	if ag.sendCount() != 0 {
		t.Fatalf("Send called %d times, want 0", ag.sendCount())
	}
	if m.turnActive {
		t.Fatal("refused submit left a turn marked active")
	}
	got := lastEntry(m)
	if got.kind != kindStatus || !strings.Contains(got.text, "no open changeset") {
		t.Fatalf("last entry = %#v, want a \"no open changeset\" status line", got)
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

// TestCtrlRDuringTurnJumpsToReviewAndKeepsTheStream is S5-T5's pinned
// behaviour: ctrl+r works mid-turn — it switches to Review — and the jump
// neither cancels the turn nor disturbs the events already received.
func TestCtrlRDuringTurnJumpsToReviewAndKeepsTheStream(t *testing.T) {
	_, engine, csID := liveVault(t)
	release := make(chan struct{})
	ag := &fakeTurnAgent{
		sessions: agent.NewFileSessions(engine.Vault().Root()),
		release:  release,
		script: []agent.Event{
			agent.TextDelta{Text: "streaming along"},
			agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"x"}`},
			agent.StageEv{ChangesetID: csID, Ops: 1},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "go on")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}

	// Mid-turn (the fake has not been released, so no event has arrived):
	// ctrl+r still jumps to Review.
	pane, jump := m.Update(specialKey('r', tea.ModCtrl))
	m = pane.(*Model)
	if jump == nil {
		t.Fatal("ctrl+r produced no command")
	}
	sw, ok := jump().(ui.SwitchScreenMsg)
	if !ok {
		t.Fatalf("ctrl+r produced %#v, want ui.SwitchScreenMsg", sw)
	}
	if sw.To != ui.ScreenReview {
		t.Fatalf("SwitchScreenMsg.To = %v, want ui.ScreenReview", sw.To)
	}
	if !m.turnActive {
		t.Fatal("ctrl+r ended the turn")
	}
	if m.sessionID != csID {
		t.Fatalf("sessionID = %q after ctrl+r, want %q", m.sessionID, csID)
	}

	// The stream survives the jump: the whole script still arrives, in
	// order, and the turn still ends cleanly.
	close(release)
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	if got := assistantText(m); len(got) != 1 || got[0] != "streaming along" {
		t.Fatalf("assistant entries = %#v, want exactly one \"streaming along\"", got)
	}
	if m.turnActive {
		t.Fatal("turn did not end after the jump")
	}
}

// TestCtrlRLeavesALiveStreamMounted proves the narrower half of the same
// rule against a stream that is already mounted and mid-flight: the jump
// must not clear or replace the pane's channel.
func TestCtrlRLeavesALiveStreamMounted(t *testing.T) {
	d := newTestDeps(t)
	m := New(d).(*Model)

	ch := make(chan agent.Event, 4)
	ch <- agent.TextDelta{Text: "streaming"}

	pane, install := m.Update(StreamMsg{Ch: ch})
	m = pane.(*Model)
	if install == nil {
		t.Fatal("StreamMsg produced no command")
	}

	// Consume exactly one event and stop: pump does not chase the re-arm,
	// so the stream stays mounted while the next event is still queued.
	var pending tea.Cmd
	m, pending = pump(t, m, install)
	if m.ch == nil {
		t.Fatal("StreamMsg did not install the channel")
	}
	if !m.turnActive {
		t.Fatal("a streamed delta did not mark the turn active")
	}

	pane, jump := m.Update(specialKey('r', tea.ModCtrl))
	m = pane.(*Model)
	msg := jump()
	if sw, ok := msg.(ui.SwitchScreenMsg); !ok || sw.To != ui.ScreenReview {
		t.Fatalf("ctrl+r produced %#v, want SwitchScreenMsg{ScreenReview}", msg)
	}
	if m.ch == nil {
		t.Fatal("ctrl+r dropped the mounted stream")
	}

	// And the stream still delivers what is left of it after the jump.
	close(ch)
	m, _ = pump(t, m, pending)
	if got := assistantText(m); len(got) != 1 || got[0] != "streaming" {
		t.Fatalf("assistant entries = %#v, want exactly one \"streaming\"", got)
	}
	if m.ch != nil {
		t.Fatal("the closed stream was never reaped")
	}
}

// TestErrorEventRendersAsAStyledErrorEntry is S5-T5's "render ErrorEv
// visibly": a failed turn ends in a kindError entry — the theme's Bad style,
// not the muted status grey a DoneEv gets — and still closes the turn.
func TestErrorEventRendersAsAStyledErrorEntry(t *testing.T) {
	_, engine, _ := liveVault(t)
	ag := &fakeTurnAgent{
		sessions: agent.NewFileSessions(engine.Vault().Root()),
		script: []agent.Event{
			agent.TextDelta{Text: "partial answer"},
			agent.ErrorEv{Err: context.DeadlineExceeded},
		},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "tell me everything")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	if m.turnActive {
		t.Fatal("ErrorEv did not close the turn")
	}
	got := lastEntry(m)
	if got.kind != kindError {
		t.Fatalf("last entry kind = %v, want kindError", got.kind)
	}
	if !strings.Contains(got.text, "context deadline exceeded") {
		t.Fatalf("error entry = %q, want the underlying error's text", got.text)
	}

	view := m.View(80, 20)
	if !strings.Contains(view, "error: context deadline exceeded") {
		t.Fatalf("view does not render the error visibly:\n%s", view)
	}
	// Theme-aware, literally: the same entry, rendered through the pane's
	// own Bad style.
	want := renderPrefixed("error: ", got.text, 80, m.theme.Bad)
	if !strings.Contains(view, strings.TrimRight(want[0], " ")) {
		t.Fatalf("error entry is not rendered in the theme's Bad style:\nwant %q in\n%s", want[0], view)
	}
}

// TestSessionClosesWhenReviewRejects pins the S5-T5 session lifecycle: the
// empty ui.StageChangedMsg is how review reports "no changeset any more"
// (Commit and Reject both emit it), and that is when this pane archives the
// session its turn ran under — once, and only its own.
func TestSessionClosesWhenReviewRejects(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewFileSessions(root)}
	ag := &fakeTurnAgent{
		sessions: sessions,
		script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "stage something")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)
	if m.sessionID != csID {
		t.Fatalf("sessionID = %q, want %q", m.sessionID, csID)
	}
	if got := sessions.closedIDs(); len(got) != 0 {
		t.Fatalf("session closed %v before any commit/reject, want none", got)
	}

	// A populated StageChangedMsg is a change, not a removal: it must leave
	// the session alone.
	if _, cmd := m.Update(ui.StageChangedMsg{ChangesetID: csID, Ops: 2}); cmd != nil {
		t.Fatalf("populated StageChangedMsg produced a command (%#v), want nil", cmd)
	}
	if got := sessions.closedIDs(); len(got) != 0 {
		t.Fatalf("populated StageChangedMsg closed %v, want none", got)
	}

	// Engine.Reject is what review's X runs; the message it broadcasts
	// afterwards is the empty one. Closing must succeed against the real
	// store even though the changeset's directory has moved to rejected/.
	if err := engine.Reject("rejected in test"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	pane, closeCmd := m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	if closeCmd == nil {
		t.Fatal("empty StageChangedMsg produced no Close command")
	}
	outcome := closeCmd()
	sc, ok := outcome.(sessionClosedMsg)
	if !ok {
		t.Fatalf("Close command produced %#v (%T), want sessionClosedMsg", outcome, outcome)
	}
	if sc.err != nil {
		t.Fatalf("Close failed after Reject: %v", sc.err)
	}
	if got := sessions.closedIDs(); len(got) != 1 || got[0] != csID {
		t.Fatalf("closed = %v, want exactly [%s]", got, csID)
	}
	if m.sessionID != "" {
		t.Fatalf("sessionID = %q after the changeset went, want empty", m.sessionID)
	}

	// And a second empty broadcast has nothing of ours left to close.
	if _, cmd := m.Update(ui.StageChangedMsg{}); cmd != nil {
		t.Fatalf("second empty StageChangedMsg produced a command (%#v), want nil", cmd)
	}
	if got := sessions.closedIDs(); len(got) != 1 {
		t.Fatalf("closed = %v after a repeated broadcast, want exactly one close", got)
	}
}

// TestSessionClosesAfterARealCommit is the same lifecycle through
// Engine.Commit, whose directory move lands the session under
// changesets/committed/ before the broadcast arrives.
func TestSessionClosesAfterARealCommit(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewFileSessions(root)}
	ag := &fakeTurnAgent{
		sessions: sessions,
		script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "stage something")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	if _, err := engine.Commit("test commit"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".llmwiki", "changesets", "committed", csID, "session.ndjson")); err != nil {
		t.Fatalf("session did not travel with the committed changeset: %v", err)
	}

	pane, closeCmd := m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	if closeCmd == nil {
		t.Fatal("empty StageChangedMsg produced no Close command")
	}
	if sc, ok := closeCmd().(sessionClosedMsg); !ok {
		t.Fatalf("Close command produced a %#T, want sessionClosedMsg", closeCmd())
	} else if sc.err != nil {
		t.Fatalf("Close failed after Commit: %v", sc.err)
	}
	if got := sessions.closedIDs(); len(got) != 1 || got[0] != csID {
		t.Fatalf("closed = %v, want exactly [%s]", got, csID)
	}
}

// TestChangesetGoneMidTurnCancelsTheTurn pins the other half of the session
// lifecycle: a turn still running when its changeset is committed or
// rejected is cancelled rather than left writing records into an archived
// session, and its stream still closes out cleanly (backbone §9, C-105).
func TestChangesetGoneMidTurnCancelsTheTurn(t *testing.T) {
	root, engine, csID := liveVault(t)
	sessions := &recordingSessions{SessionStore: agent.NewFileSessions(root)}
	release := make(chan struct{})
	entered := make(chan struct{})
	ag := &fakeTurnAgent{
		sessions: sessions,
		release:  release,
		entered:  entered,
		script: []agent.Event{
			agent.TextDelta{Text: "still working"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		},
	}
	m := New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "long question")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	if m.cancel == nil {
		t.Fatal("submit left the turn's context uncancellable")
	}
	<-entered // the turn is genuinely running, session and all

	// Review rejects while the turn is parked mid-flight.
	if err := engine.Reject("rejected mid-turn"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	pane, closeCmd := m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	if closeCmd == nil {
		t.Fatal("empty StageChangedMsg produced no Close command")
	}
	if m.cancel != nil {
		t.Fatal("changesetGone did not cancel the running turn")
	}
	if sc, ok := closeCmd().(sessionClosedMsg); !ok {
		t.Fatalf("Close command produced a %#T, want sessionClosedMsg", closeCmd())
	} else if sc.err != nil {
		t.Fatalf("Close failed after Reject: %v", sc.err)
	}
	close(release)

	// The channel closes out cleanly and the pane's pump terminates.
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)
	if m.turnActive {
		t.Fatal("cancelled turn left the pane marked active")
	}
	var sawClosed bool
	for _, msg := range seen {
		if _, ok := msg.(StreamClosedMsg); ok {
			sawClosed = true
		}
	}
	if !sawClosed {
		t.Fatalf("StreamClosedMsg never observed; seen = %#v", seen)
	}
	if got := sessions.closedIDs(); len(got) != 1 || got[0] != csID {
		t.Fatalf("closed = %v, want exactly [%s]", got, csID)
	}
}

// pump runs cmd and feeds every message it produces — expanding tea.BatchMsg
// — to m, but deliberately does not chase the command Update returns: it
// hands that command back instead. It is runCmd with the recursion cut one
// link short, which is what lets a test leave a turn's stream mounted on a
// still-open channel instead of blocking inside it.
func pump(t *testing.T, m ui.Pane, cmd tea.Cmd) (*Model, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		mm, ok := m.(*Model)
		if !ok {
			t.Fatalf("pump: pane is %T, want *Model", m)
		}
		return mm, nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m, _ = pump(t, m, c)
		}
		return m.(*Model), nil
	}
	updated, next := m.Update(msg)
	mm, ok := updated.(*Model)
	if !ok {
		t.Fatalf("pump: pane is %T, want *Model", updated)
	}
	return mm, next
}

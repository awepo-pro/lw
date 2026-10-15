// askconv_test.go is 057's frozen block for the Ask pane: a question that only
// reads opens no changeset and leaves no journal line, the conversation is one
// in-memory session that survives a Review commit or reject, and a turn that
// does stage lands in a changeset whose session.ndjson carries the conversation
// that led to it. Every turn here runs the REAL agent.Loop over a scripted
// loopback provider, a real engine and the real agent.NewAskSessions store —
// before 057 the pane opened a changeset for every question and rejected it
// unread (half of a real vault's changesets), so the absence of one is the
// thing under test. Permanent regression tests (D-10C).
package ask

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
	"github.com/awepo-pro/lw/internal/ui"
)

// scriptedProvider is the fake model: every POST is answered with the next
// scripted SSE body, and each request's decoded messages are kept so a test can
// read the history a turn sent.
type scriptedProvider struct {
	mu     sync.Mutex
	rounds []string
	reqs   [][]llm.Message
	srv    *httptest.Server
}

func newScriptedProvider(t *testing.T, rounds ...string) *scriptedProvider {
	t.Helper()
	p := &scriptedProvider{rounds: rounds}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *scriptedProvider) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var wire struct {
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		http.Error(w, "decode request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	n := len(p.reqs)
	p.reqs = append(p.reqs, wire.Messages)
	round := textRound("(unscripted)")
	if n < len(p.rounds) {
		round = p.rounds[n]
	}
	p.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, round)
}

// request returns the messages of the n'th request.
func (p *scriptedProvider) request(n int) []llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n >= len(p.reqs) {
		return nil
	}
	return p.reqs[n]
}

func (p *scriptedProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// sseData renders one SSE data event whose JSON payload is v.
func sseData(v any) string {
	b, _ := json.Marshal(v)
	return "data: " + string(b) + "\n\n"
}

// textRound is a round that answers in prose and stops.
func textRound(text string) string {
	return sseData(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}, "finish_reason": nil}}}) +
		sseData(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}) +
		"data: [DONE]\n\n"
}

// toolRound is a round that calls one tool and waits for its result.
func toolRound(id, name, args string) string {
	call := map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}
	return sseData(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}, "finish_reason": nil}}}) +
		sseData(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}}) +
		"data: [DONE]\n\n"
}

// askLoop is the production agent over p: a real Loop on the in-memory ask store
// wrapped around file — exactly what cmd/lw's tuiAgent builds.
func askLoop(t *testing.T, engine *stage.Engine, file agent.SessionStore, p *scriptedProvider) *agent.Loop {
	t.Helper()
	reg := tools.NewRegistry(tools.Deps{
		Vault:  engine.Vault(),
		Index:  engine.Index(),
		Engine: engine,
		Author: stage.Author{Kind: "agent", Model: "lw-test"},
	})
	client := llm.New(llm.Config{BaseURL: p.srv.URL, Model: "lw-test"})
	cfg := agent.LoopConfig{TraceMeta: trace.Meta{Model: "lw-test"}}
	return agent.NewLoop(client, reg, agent.NewAskSessions(file), engine, cfg)
}

// journalBytes reads the journal.
func journalBytes(t *testing.T, root string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".llmwiki", "journal.ndjson"))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return b
}

// changesetDirNames lists every changeset directory in every state, as
// "<state>/<id>".
func changesetDirNames(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, state := range []string{"open", "committed", "rejected"} {
		entries, err := os.ReadDir(filepath.Join(root, ".llmwiki", "changesets", state))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("ReadDir %s: %v", state, err)
		}
		for _, e := range entries {
			out = append(out, state+"/"+e.Name())
		}
	}
	return out
}

// hasMessage reports whether msgs holds a message of role with exactly content.
func hasMessage(msgs []llm.Message, role, content string) bool {
	for _, m := range msgs {
		if m.Role == role && m.Content == content {
			return true
		}
	}
	return false
}

// stageChangedMsgs returns the ui.StageChangedMsg values among seen.
func stageChangedMsgs(seen []tea.Msg) []ui.StageChangedMsg {
	var out []ui.StageChangedMsg
	for _, msg := range seen {
		if sc, ok := msg.(ui.StageChangedMsg); ok {
			out = append(out, sc)
		}
	}
	return out
}

// submitCollect is submitAndDrain that also returns every message the turn
// produced.
func submitCollect(t *testing.T, m *Model, q string) (*Model, []tea.Msg) {
	t.Helper()
	m, cmd := typeAndSubmit(t, m, q)
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	var seen []tea.Msg
	return runCmd(t, m, cmd, &seen).(*Model), seen
}

const (
	citedAnswer1 = "A kv cache memoizes keys and values.^[raw/papers/leviathan-2023.md]"
	citedAnswer2 = "Flash attention is a kernel design.^[raw/papers/leviathan-2023.md]"
)

// createPageArgs is a known-valid stage.create_page payload for the minimal
// fixture (internal/agent's wireCreatePageArgs, copied from the conformance
// fixtures' own create_page step).
const createPageArgs = `{"path":"wiki/queries/kv-cache-answer.md","title":"Kv Cache Answer","type":"query","tags":["inference"],"sources":["raw/papers/leviathan-2023.md"],"confidence":"low","contested":false,"body":"# Kv Cache Answer\n\nSee [[kv-cache]] and [[gpt-4]].","rationale":"file the answer"}`

// TestAskTurnNoChangesetNoJournal is the headline: with nothing open, two
// questions that only read leave no changeset directory, not one journal byte,
// no StageChangedMsg and no hint — and the second still sees the first, because
// the conversation is one session, not a changeset's.
func TestAskTurnNoChangesetNoJournal(t *testing.T) {
	root, engine := carryVault(t)
	p := newScriptedProvider(t,
		toolRound("call-1", "wiki_search", `{"q":"kv cache"}`),
		textRound(citedAnswer1),
		textRound(citedAnswer2),
	)
	loop := askLoop(t, engine, agent.NewFileSessions(root), p)
	m := New(liveDeps(t, engine, loop)).(*Model)
	journal := journalBytes(t, root)

	m, seen := submitCollect(t, m, "what is a kv cache?")

	if dirs := changesetDirNames(t, root); len(dirs) != 0 {
		t.Fatalf("a read-only ask left changeset directories %v, want none", dirs)
	}
	if after := journalBytes(t, root); !bytes.Equal(after, journal) {
		t.Fatalf("a read-only ask wrote to the journal:\n before %q\n after  %q", journal, after)
	}
	if _, err := engine.Current(); err == nil {
		t.Fatal("a changeset is open after a read-only ask")
	}
	if sc := stageChangedMsgs(seen); len(sc) != 0 {
		t.Fatalf("a read-only ask broadcast ui.StageChangedMsg %+v, want none", sc)
	}
	if got := assistantText(m); len(got) != 1 || got[0] != citedAnswer1 {
		t.Fatalf("assistant entries = %#v, want the model's answer", got)
	}
	for _, e := range m.entries {
		if strings.Contains(e.text, "nothing staged") || strings.Contains(e.text, "lw session show") {
			t.Fatalf("entry %#v hints about a session an empty ask no longer leaves", e)
		}
	}
	if m.turnActive {
		t.Fatal("the turn is still active")
	}

	// Turn 2 runs in the same conversation: its request carries turn 1's
	// question and answer as plain history, and no tool call of turn 1's.
	m, seen = submitCollect(t, m, "and flash attention?")
	if p.requestCount() != 3 {
		t.Fatalf("the provider saw %d requests, want 3 (turn 1's two rounds and turn 2's one)", p.requestCount())
	}
	second := p.request(2)
	if !hasMessage(second, "user", "what is a kv cache?") || !hasMessage(second, "assistant", citedAnswer1) || !hasMessage(second, "user", "and flash attention?") {
		t.Fatalf("turn 2's request lacks turn 1's question+answer as history:\n%+v", second)
	}
	for _, msg := range second {
		if msg.Role == "tool" || len(msg.ToolCalls) != 0 {
			t.Fatalf("turn 2's request replays turn 1's tool call (%+v); history is prose only", msg)
		}
	}
	if dirs := changesetDirNames(t, root); len(dirs) != 0 {
		t.Fatalf("two read-only asks left changeset directories %v", dirs)
	}
	if after := journalBytes(t, root); !bytes.Equal(after, journal) {
		t.Fatalf("two read-only asks wrote to the journal")
	}
	if sc := stageChangedMsgs(seen); len(sc) != 0 {
		t.Fatalf("the second ask broadcast ui.StageChangedMsg %+v", sc)
	}
}

// TestAskTurnThatStagesAdopts: the ctrl+s filing turn is where a changeset
// finally opens — lazily, at the model's first stage call, with the question as
// its intent — and the changeset's session.ndjson then holds the conversation
// that led to it plus the filing turn's own records, so `lw session show`
// reads as it did when every ask had a changeset.
func TestAskTurnThatStagesAdopts(t *testing.T) {
	root, engine := carryVault(t)
	p := newScriptedProvider(t,
		textRound(citedAnswer1),
		toolRound("call-1", "stage_create_page", createPageArgs),
		textRound("Filed."),
	)
	loop := askLoop(t, engine, agent.NewFileSessions(root), p)
	m := New(liveDeps(t, engine, loop)).(*Model)

	m, _ = submitCollect(t, m, "what is a kv cache?")
	if dirs := changesetDirNames(t, root); len(dirs) != 0 {
		t.Fatalf("the read-only turn left %v", dirs)
	}
	convID := m.convID
	if !strings.HasPrefix(convID, "ask-") {
		t.Fatalf("convID = %q, want an ask- conversation id", convID)
	}

	pane, cmd := pressCtrlS(m)
	m = pane.(*Model)
	if cmd == nil {
		t.Fatal("ctrl+s produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)

	cs, err := engine.Current()
	if err != nil {
		t.Fatalf("Current after the filing turn: %v, want the lazily opened changeset", err)
	}
	if !strings.HasPrefix(cs.Intent, "ask: File this answer as a query page.") {
		t.Fatalf("Intent = %q, want the filing turn's \"ask: File this answer…\"", cs.Intent)
	}
	if cs.Author.Session != convID || cs.Author.Kind != "agent" || cs.Author.Model != "lw-test" {
		t.Fatalf("Author = %+v, want the agent, the model and session %s", cs.Author, convID)
	}
	if len(cs.Live()) != 1 {
		t.Fatalf("the changeset holds %d live ops, want the filed page", len(cs.Live()))
	}
	if dirs := changesetDirNames(t, root); len(dirs) != 1 || dirs[0] != "open/"+cs.ID {
		t.Fatalf("changeset directories = %v, want only open/%s", dirs, cs.ID)
	}
	var sawOpen bool
	for _, sc := range stageChangedMsgs(seen) {
		if sc.ChangesetID == cs.ID {
			sawOpen = true
		}
	}
	if !sawOpen {
		t.Fatalf("no ui.StageChangedMsg named the opened changeset %s: %+v", cs.ID, stageChangedMsgs(seen))
	}

	// The audit trail: the conversation's earlier prose, Carried, then this
	// turn's records — the filing message, the staging call (Staged), the
	// closing answer.
	sess, err := agent.NewFileSessions(root).Get(cs.ID)
	if err != nil {
		t.Fatalf("Get(%s): %v", cs.ID, err)
	}
	recs := sess.Records
	if len(recs) != 5 {
		t.Fatalf("the adopted session holds %d records, want the carried pair plus the filing turn's user, tool and answer records:\n%+v", len(recs), recs)
	}
	wantCarried(t, "record 0", recs[0], "user", "what is a kv cache?", true)
	wantCarried(t, "record 1", recs[1], "assistant", citedAnswer1, true)
	if recs[2].Carried || recs[2].Role != "user" || !strings.HasPrefix(recs[2].Content, "File this answer as a query page.") {
		t.Fatalf("record 2 = %+v, want the filing turn's user message", recs[2])
	}
	var sawStaged bool
	for _, r := range recs[2:] {
		if r.Carried {
			t.Fatalf("record %+v of the filing turn is marked Carried", r)
		}
		if r.Role == "tool" && r.Tool == "stage.create_page" && r.Staged {
			sawStaged = true
		}
	}
	if !sawStaged {
		t.Fatalf("the staging call's tool record is missing from the adopted session:\n%+v", recs)
	}
	if last := recs[len(recs)-1]; last.Role != "assistant" || last.Content != "Filed." {
		t.Fatalf("the last adopted record = %+v, want the filing turn's closing answer", last)
	}
	turn := recs[2].Turn
	if turn == "" {
		t.Fatal("the adopted records carry no turn id")
	}
	for _, r := range recs[2:] {
		if r.Turn != turn {
			t.Fatalf("record %+v belongs to another turn than %s", r, turn)
		}
	}

	// The filing turn's request carried the conversation (prose only) too.
	filing := p.request(1)
	if !hasMessage(filing, "user", "what is a kv cache?") || !hasMessage(filing, "assistant", citedAnswer1) {
		t.Fatalf("the filing turn's request lacks the conversation:\n%+v", filing)
	}
}

// TestChangesetGoneKeepsConversation: Review committing or rejecting a
// changeset says nothing about the conversation. Before 057 the pane closed the
// session with its changeset and the next turn re-seeded a fresh one; now the
// conversation is its own, so the next question still carries the earlier
// prose.
func TestChangesetGoneKeepsConversation(t *testing.T) {
	root, engine := carryVault(t)
	p := newScriptedProvider(t, textRound(citedAnswer1), textRound(citedAnswer2))
	loop := askLoop(t, engine, agent.NewFileSessions(root), p)
	m := New(liveDeps(t, engine, loop)).(*Model)

	m, _ = submitCollect(t, m, "what is a kv cache?")
	convID := m.convID
	if dirs := changesetDirNames(t, root); len(dirs) != 0 {
		t.Fatalf("the read-only turn left %v, want no changeset", dirs)
	}

	// A changeset appears later — an `lw ingest` left it — and
	// the curator commits it in Review while the pane is idle.
	stageKeepOp(t, mustOpen(t, engine))
	if _, err := engine.Commit("review accepted"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	pane, cmd := m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	if cmd != nil {
		// Only the title's state lookup may be owed, and only for an id the
		// pane had shown; this pane never showed one.
		if msg := cmd(); msg != nil {
			if _, ok := msg.(titleFateMsg); !ok {
				t.Fatalf("the Review broadcast produced %#v, want at most a title lookup", msg)
			}
		}
	}
	if m.convID != convID {
		t.Fatalf("convID = %q after the broadcast, want the conversation %q kept", m.convID, convID)
	}

	m, _ = submitCollect(t, m, "and flash attention?")
	if got := lastEntry(m); got.kind != kindStatus {
		t.Fatalf("last entry = %#v, want the turn's done line (an error here means the conversation was closed)", got)
	}
	second := p.request(1)
	if !hasMessage(second, "user", "what is a kv cache?") || !hasMessage(second, "assistant", citedAnswer1) {
		t.Fatalf("the turn after the commit lost the conversation:\n%+v", second)
	}
	if m.convID != convID {
		t.Fatalf("convID changed across the commit: %q -> %q", convID, m.convID)
	}

	// The same through a reject.
	stageKeepOpRejectable(t, engine)
	if err := engine.Reject("review declined"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	pane, _ = m.Update(ui.StageChangedMsg{})
	m = pane.(*Model)
	if m.convID != convID {
		t.Fatalf("convID changed across the reject: %q -> %q", convID, m.convID)
	}
}

// mustOpen opens a changeset the way an `lw ingest` would have left one.
func mustOpen(t *testing.T, engine *stage.Engine) *stage.Engine {
	t.Helper()
	if _, err := engine.OpenChangeset("lw ingest a.md", stage.Author{Kind: "agent"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	return engine
}

// stageKeepOpRejectable opens a changeset holding a real op, for a reject.
func stageKeepOpRejectable(t *testing.T, engine *stage.Engine) {
	t.Helper()
	mustOpen(t, engine)
	stageKVCachePatch(t, engine)
}

// gatedAgent is a fake agent whose turn can be held between two halves: it
// sends first, announces it did, waits for release (or cancellation), then
// sends rest. cancelled records whether the turn's ctx ended before release.
type gatedAgent struct {
	sessions agent.SessionStore
	first    []agent.Event
	rest     []agent.Event
	reached  chan struct{}
	release  chan struct{}

	mu        sync.Mutex
	cancelled bool
}

func (g *gatedAgent) Sessions() agent.SessionStore { return g.sessions }

func (g *gatedAgent) wasCancelled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cancelled
}

func (g *gatedAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	defer close(out)
	for _, ev := range g.first {
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	close(g.reached)
	select {
	case <-g.release:
	case <-ctx.Done():
		g.mu.Lock()
		g.cancelled = true
		g.mu.Unlock()
		return ctx.Err()
	}
	for _, ev := range g.rest {
		select {
		case out <- ev:
		case <-ctx.Done():
			g.mu.Lock()
			g.cancelled = true
			g.mu.Unlock()
			return ctx.Err()
		}
	}
	return nil
}

// nextEvent reads one event off the pane's mounted stream and applies it, the
// way the runtime delivers Listen's result.
func nextEvent(t *testing.T, m *Model) *Model {
	t.Helper()
	if m.ch == nil {
		t.Fatal("no stream is mounted")
	}
	pane, _ := m.Update(Listen(m.ch)())
	return pane.(*Model)
}

// TestChangesetGoneCancelsOnlyStagingTurn: a Review verdict used to cancel
// whatever turn was running, because the turn and the changeset were one thing.
// Now only a turn that staged into the changeset that went away is stopped; a
// question still being answered when an unrelated changeset is rejected goes on.
func TestChangesetGoneCancelsOnlyStagingTurn(t *testing.T) {
	t.Run("turn_that_has_not_staged_survives", func(t *testing.T) {
		root, engine := carryVault(t)
		mustOpen(t, engine) // an `lw ingest` changeset, unrelated to this turn
		stageKVCachePatch(t, engine)
		ag := &gatedAgent{
			sessions: agent.NewAskSessions(agent.NewFileSessions(root)),
			first:    []agent.Event{agent.TextDelta{Text: "reading the vault"}},
			rest:     []agent.Event{agent.TextDelta{Text: " and answering"}, agent.DoneEv{Reason: "stop", Rounds: 1}},
			reached:  make(chan struct{}),
			release:  make(chan struct{}),
		}
		m := New(liveDeps(t, engine, ag)).(*Model)

		m, cmd := typeAndSubmit(t, m, "what is in the vault?")
		if cmd == nil {
			t.Fatal("submit produced no command")
		}
		m, _ = pump(t, m, cmd) // turnStartedMsg
		<-ag.reached
		m = nextEvent(t, m) // the first TextDelta

		if err := engine.Reject("rejected in Review"); err != nil {
			t.Fatalf("Reject: %v", err)
		}
		pane, _ := m.Update(ui.StageChangedMsg{})
		m = pane.(*Model)
		if m.cancel == nil {
			t.Fatal("a Review reject cancelled a turn that had staged nothing")
		}

		close(ag.release)
		var seen []tea.Msg
		m = runCmd(t, m, Listen(m.ch), &seen).(*Model)
		if ag.wasCancelled() {
			t.Fatal("the running turn's context was cancelled by an unrelated reject")
		}
		if got := assistantText(m); len(got) != 1 || got[0] != "reading the vault and answering" {
			t.Fatalf("assistant entries = %#v, want the whole answer", got)
		}
		if got := lastEntry(m); got.kind != kindStatus || got.text != "done · 1 rounds" {
			t.Fatalf("last entry = %#v, want the done line", got)
		}
	})

	t.Run("turn_that_staged_is_cancelled", func(t *testing.T) {
		root, engine := carryVault(t)
		mustOpen(t, engine)
		stageKVCachePatch(t, engine)
		cs, err := engine.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		ag := &gatedAgent{
			sessions: agent.NewAskSessions(agent.NewFileSessions(root)),
			first: []agent.Event{
				agent.TextDelta{Text: "staging"},
				agent.StageEv{ChangesetID: cs.ID, Ops: 1},
			},
			rest:    []agent.Event{agent.TextDelta{Text: " never delivered"}, agent.DoneEv{Reason: "stop", Rounds: 1}},
			reached: make(chan struct{}),
			release: make(chan struct{}),
		}
		m := New(liveDeps(t, engine, ag)).(*Model)

		m, cmd := typeAndSubmit(t, m, "stage something")
		if cmd == nil {
			t.Fatal("submit produced no command")
		}
		m, _ = pump(t, m, cmd) // turnStartedMsg
		<-ag.reached
		m = nextEvent(t, m) // the TextDelta
		m = nextEvent(t, m) // the StageEv: the turn staged into cs

		if err := engine.Reject("rejected in Review"); err != nil {
			t.Fatalf("Reject: %v", err)
		}
		pane, _ := m.Update(ui.StageChangedMsg{})
		m = pane.(*Model)
		if m.cancel != nil {
			t.Fatal("a Review reject of the changeset this turn staged into left the turn running")
		}

		close(ag.release)
		var seen []tea.Msg
		m = runCmd(t, m, Listen(m.ch), &seen).(*Model)
		if !ag.wasCancelled() {
			t.Fatal("the turn was never cancelled")
		}
		if m.turnActive {
			t.Fatal("the cancelled turn left the pane marked active")
		}
		for _, text := range assistantText(m) {
			if strings.Contains(text, "never delivered") {
				t.Fatalf("a cancelled turn kept streaming: %q", text)
			}
		}
		// The cancelled turn adopted nothing into a changeset that is gone.
		if _, err := os.Stat(filepath.Join(root, ".llmwiki", "changesets", "open", cs.ID)); !os.IsNotExist(err) {
			t.Fatalf("open/%s exists after the reject (%v): the adopt step fabricated a gone changeset", cs.ID, err)
		}
	})
}

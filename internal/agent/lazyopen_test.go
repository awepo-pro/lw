package agent

// lazyopen_test.go is 057 D2's frozen block: nothing opens a changeset for a
// turn until the turn's first staging call, and then the loop opens it — with
// the intent the caller put on the ctx — instead of every ask opening one it
// would reject unread (50 % of the changesets in a real vault were exactly
// that: a journal pair, a rejected/ directory and a sync push each). Permanent
// regression tests (D-10C); every engine, registry and store is real, only the
// model is scripted.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// lazyFixture is a Loop over the in-memory ask store and a vault with NOTHING
// open — the state every TUI ask starts in since 057.
type lazyFixture struct {
	root   string
	engine *stage.Engine
	reg    *tools.Registry
	store  SessionStore
	fake   *fakeStreamer
	loop   *Loop
}

const lazySession = "ask-lazy"

func newLazyFixture(t *testing.T, rounds [][]llm.Chunk) *lazyFixture {
	t.Helper()
	return newLazyFixtureWeb(t, rounds, false)
}

// newLazyFixtureWeb is newLazyFixture over a registry that may offer web.search
// (a stub provider), which is what makes an ask turn's tool set include
// stage.ingest_source (039's askWebTools) — the TUI's web flow.
func newLazyFixtureWeb(t *testing.T, rounds [][]llm.Chunk, web bool) *lazyFixture {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	deps := tools.Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Extract: extract.NewFile(), Author: stage.Author{Kind: "agent", Model: "registry-model"}}
	if web {
		deps.Search = stubSearchProvider{}
	}
	reg := tools.NewRegistry(deps)
	store := NewAskSessions(NewFileSessions(root))
	if _, err := store.Create(lazySession); err != nil {
		t.Fatalf("Create(%s): %v", lazySession, err)
	}
	fake := &fakeStreamer{rounds: rounds}
	cfg := LoopConfig{TraceMeta: trace.Meta{Model: "loop-model"}}
	return &lazyFixture{root: root, engine: e, reg: reg, store: store, fake: fake, loop: newLoop(fake, reg, store, e, cfg)}
}

// send runs one turn of lazySession under verb and returns its events.
func (f *lazyFixture) send(t *testing.T, ctx context.Context, verb string) []Event {
	t.Helper()
	out := make(chan Event, 64)
	if err := f.loop.Send(trace.WithVerb(ctx, verb), lazySession, "file this answer", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return drain(out)
}

// journalBytes is the journal as it stands: the "byte-equal before and after"
// half of a turn that must leave no event behind.
func (f *lazyFixture) journalBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, ".llmwiki", "journal.ndjson"))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return b
}

// changesetDirs counts the changeset directories in every state directory.
func (f *lazyFixture) changesetDirs(t *testing.T) int {
	t.Helper()
	n := 0
	for _, state := range []string{"open", "committed", "rejected"} {
		entries, err := os.ReadDir(filepath.Join(f.root, ".llmwiki", "changesets", state))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("ReadDir %s: %v", state, err)
		}
		n += len(entries)
	}
	return n
}

// secondCreatePageArgs is wireCreatePageArgs for another path, so a second
// staging call in one turn succeeds beside the first.
var secondCreatePageArgs = strings.NewReplacer("wire-name-test", "wire-name-test-two", "Wire Name Test", "Wire Name Test Two").Replace(wireCreatePageArgs)

func TestLazyOpenOnFirstStagingCall(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{toolCallChunk("call-2", "stage_create_page", secondCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "Filed."}, {Finish: "stop"}},
	})
	if _, err := f.engine.Current(); err == nil {
		t.Fatal("precondition: a changeset is open before the turn")
	}

	const intent = "ask: File this answer as a query page."
	events := f.send(t, WithOpenIntent(context.Background(), intent), trace.VerbFile)

	var results []ToolResEv
	var staged []StageEv
	for _, ev := range events {
		switch e := ev.(type) {
		case ToolResEv:
			results = append(results, e)
		case StageEv:
			staged = append(staged, e)
		}
	}
	if len(results) != 2 || results[0].IsError || results[1].IsError {
		t.Fatalf("tool results = %+v, want two successful stage.create_page calls", results)
	}

	cs, err := f.engine.Current()
	if err != nil {
		t.Fatalf("Current after the turn: %v, want the lazily opened changeset", err)
	}
	if cs.Intent != intent {
		t.Fatalf("Intent = %q, want the ctx intent %q", cs.Intent, intent)
	}
	want := stage.Author{Kind: "agent", Model: "loop-model", Session: lazySession}
	if cs.Author != want {
		t.Fatalf("Author = %+v, want %+v", cs.Author, want)
	}
	if len(cs.Live()) != 2 {
		t.Fatalf("the changeset holds %d live ops, want both calls' pages", len(cs.Live()))
	}
	// A second staging call in the same turn joined nothing and opened nothing:
	// one directory, one changeset_opened, no changeset_joined.
	if n := f.changesetDirs(t); n != 1 {
		t.Fatalf("%d changeset directories after the turn, want exactly 1", n)
	}
	journal := string(f.journalBytes(t))
	if n := strings.Count(journal, `"changeset_opened"`); n != 1 {
		t.Fatalf("journal holds %d changeset_opened events, want 1:\n%s", n, journal)
	}
	if strings.Contains(journal, "changeset_joined") {
		t.Fatalf("journal holds a changeset_joined event; the second call must not open or join:\n%s", journal)
	}
	if len(staged) != 2 || staged[0].ChangesetID != cs.ID || staged[1].Ops != 2 {
		t.Fatalf("StageEvs = %+v, want the opened changeset's id and ops 1 then 2", staged)
	}
}

// TestLazyOpenAskWebFlow: the TUI's web flow — an ask turn offered
// stage.ingest_source (010/017, 039's askWebTools) — never calls stage.open: the
// prompts do not mention it. Its first stage.ingest_source arrives at an engine
// with nothing open, which is why the pane used to open a changeset for every
// question; now the loop opens one, with the question as its intent, and the
// 040 flow (ingest, read the chunk, close) runs to its summary.
func TestLazyOpenAskWebFlow(t *testing.T) {
	src := filepath.Join(t.TempDir(), "lazy-source.md")
	if err := os.WriteFile(src, []byte("# Lazy Source\n\nA tiny source the web flow ingests.\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	f := newLazyFixtureWeb(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_ingest_source", fmt.Sprintf(`{"uri":%q,"kind":"article"}`, src)), {Finish: "tool_calls"}},
		{toolCallChunk("call-2", "raw_get", `{"source":"raw/articles/lazy-source.md"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-3", "stage_close", `{}`), {Finish: "tool_calls"}},
		{{Text: "Saved the source for review."}, {Finish: "stop"}},
	}, true)

	events := f.send(t, WithOpenIntent(context.Background(), "ask: what does the web say about lazy sources?"), trace.VerbAsk)

	for _, ev := range events {
		if r, ok := ev.(ToolResEv); ok && r.IsError {
			t.Fatalf("tool %s failed: %s", r.Name, r.Content)
		}
	}
	cs, err := f.engine.Current()
	if err != nil {
		t.Fatalf("Current: %v, want the lazily opened changeset", err)
	}
	if cs.Intent != "ask: what does the web say about lazy sources?" {
		t.Fatalf("Intent = %q, want the question the pane put on the ctx", cs.Intent)
	}
	if len(cs.Live()) != 1 || cs.Live()[0].Kind != stage.OpIngestSource {
		t.Fatalf("live ops = %+v, want the one ingested source", cs.Live())
	}
	if n := strings.Count(string(f.journalBytes(t)), `"changeset_opened"`); n != 1 {
		t.Fatalf("journal holds %d changeset_opened events, want 1", n)
	}
}

// TestLazyOpenOnlyWhenTheCallerOptedIn (was TestLazyOpenDefaultIntent, which
// gave a caller with no intent the default "agent: <verb>"): the lazy open is
// the Ask pane's, which puts the question on the ctx (WithOpenIntent). A caller
// that sets none — `lw ingest` and `lint --fix` pre-open their own changeset —
// keeps today's tool error when that changeset is not there, instead of the
// loop quietly conjuring one with a made-up intent (057 FIX2 R3).
func TestLazyOpenOnlyWhenTheCallerOptedIn(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "ok"}, {Finish: "stop"}},
	})
	before := f.journalBytes(t)

	events := f.send(t, context.Background(), trace.VerbIngest)

	var res []ToolResEv
	for _, ev := range events {
		if r, ok := ev.(ToolResEv); ok {
			res = append(res, r)
		}
	}
	if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content, "no open changeset") {
		t.Fatalf("tool result = %+v, want today's no-open-changeset tool error", res)
	}
	if n := f.changesetDirs(t); n != 0 {
		t.Fatalf("%d changeset directories after a staging call from a caller that set no intent", n)
	}
	if after := f.journalBytes(t); string(after) != string(before) {
		t.Fatalf("the journal changed:\n before %q\n after  %q", before, after)
	}
}

// TestLazyOpenNotForOpenClose: stage.open opens its own (it is the model's
// explicit ask) and stage.close on nothing is today's tool error — neither may
// conjure a changeset from the loop.
func TestLazyOpenNotForOpenClose(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_close", `{}`), {Finish: "tool_calls"}},
		{{Text: "nothing to close."}, {Finish: "stop"}},
	})
	before := f.journalBytes(t)

	events := f.send(t, WithOpenIntent(context.Background(), "ask: x"), trace.VerbFile)

	var res []ToolResEv
	for _, ev := range events {
		if r, ok := ev.(ToolResEv); ok {
			res = append(res, r)
		}
	}
	if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content, "no open changeset") {
		t.Fatalf("stage.close result = %+v, want today's no-open-changeset tool error", res)
	}
	if n := f.changesetDirs(t); n != 0 {
		t.Fatalf("%d changeset directories after stage.close on nothing, want none", n)
	}
	if after := f.journalBytes(t); string(after) != string(before) {
		t.Fatalf("the journal changed:\n before %q\n after  %q", before, after)
	}
}

// TestReadOnlyTurnOpensNothing is the headline: an ask-mode turn that only
// reads leaves no changeset directory in any state and not one journal byte.
func TestReadOnlyTurnOpensNothing(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "wiki_search", `{"q":"kv cache"}`), {Finish: "tool_calls"}},
		{{Text: "The kv cache memoizes keys and values."}, {Finish: "stop"}},
	})
	before := f.journalBytes(t)

	events := f.send(t, WithOpenIntent(context.Background(), "ask: what is a kv cache?"), trace.VerbAsk)

	var sawSearch, sawDone bool
	for _, ev := range events {
		switch e := ev.(type) {
		case ToolResEv:
			sawSearch = sawSearch || (e.Name == "wiki.search" && !e.IsError)
		case DoneEv:
			sawDone = true
		}
	}
	if !sawSearch || !sawDone {
		t.Fatalf("the turn did not read and finish (search %v, done %v): %#v", sawSearch, sawDone, events)
	}
	if n := f.changesetDirs(t); n != 0 {
		t.Fatalf("%d changeset directories after a read-only turn, want none", n)
	}
	if after := f.journalBytes(t); string(after) != string(before) {
		t.Fatalf("a read-only turn wrote to the journal:\n before %q\n after  %q", before, after)
	}
	if _, err := f.engine.Current(); err == nil {
		t.Fatal("a changeset is open after a read-only turn")
	}
}

// TestPreOpenedVerbUnaffected: `lw ingest` and `lint --fix` open their changeset
// before the loop runs, so a staging call finds one and opens nothing — the
// changeset keeps the intent its verb gave it, and the journal gets no event
// beyond the ops.
func TestPreOpenedVerbUnaffected(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "staged."}, {Finish: "stop"}},
	})
	opened, err := f.engine.OpenChangeset("lw ingest a.md", stage.Author{Kind: "agent", Model: "ingest-model"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	journalBefore := string(f.journalBytes(t))

	f.send(t, WithOpenIntent(context.Background(), "ask: must be ignored"), trace.VerbIngest)

	cs, err := f.engine.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cs.ID != opened.ID || cs.Intent != "lw ingest a.md" || cs.Author.Model != "ingest-model" || cs.Author.Session != "" {
		t.Fatalf("the pre-opened changeset changed: %+v", cs)
	}
	if n := f.changesetDirs(t); n != 1 {
		t.Fatalf("%d changeset directories, want only the pre-opened one", n)
	}
	added := strings.TrimPrefix(string(f.journalBytes(t)), journalBefore)
	if strings.Contains(added, "changeset_opened") || strings.Contains(added, "changeset_joined") {
		t.Fatalf("the staging call opened or joined a changeset:\n%s", added)
	}
	if len(cs.Live()) != 1 {
		t.Fatalf("the changeset holds %d live ops, want the one staged page", len(cs.Live()))
	}
}

// TestLazyOpenRefusedAndMalformedCallsOpenNothing: only a call that is going to
// run opens a changeset. A staging tool an ask turn is not offered is refused
// before dispatch (039), and a call whose arguments are not JSON is the model's
// to fix — neither earns a changeset.
func TestLazyOpenRefusedAndMalformedCallsOpenNothing(t *testing.T) {
	t.Run("refused_in_ask_mode", func(t *testing.T) {
		f := newLazyFixture(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "answered."}, {Finish: "stop"}},
		})
		f.send(t, WithOpenIntent(context.Background(), "ask: x"), trace.VerbAsk)
		if n := f.changesetDirs(t); n != 0 {
			t.Fatalf("a refused staging call left %d changeset directories", n)
		}
	})
	t.Run("malformed_arguments", func(t *testing.T) {
		f := newLazyFixture(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", `{not json`), {Finish: "tool_calls"}},
			{{Text: "sorry."}, {Finish: "stop"}},
		})
		f.send(t, WithOpenIntent(context.Background(), "ask: x"), trace.VerbFile)
		if n := f.changesetDirs(t); n != 0 {
			t.Fatalf("a malformed staging call left %d changeset directories", n)
		}
	})
	t.Run("unknown_stage_tool", func(t *testing.T) {
		f := newLazyFixture(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_nonsense", `{}`), {Finish: "tool_calls"}},
			{toolCallChunk("call-2", "stage_nonsense", `{}`), {Finish: "tool_calls"}},
			{{Text: "x"}, {Finish: "stop"}},
		})
		out := make(chan Event, 64)
		_ = f.loop.Send(trace.WithVerb(WithOpenIntent(context.Background(), "ask: x"), trace.VerbFile), lazySession, "q", out)
		drain(out)
		if n := f.changesetDirs(t); n != 0 {
			t.Fatalf("an unknown stage.* tool left %d changeset directories", n)
		}
	})
}

// TestLazyOpenFailureIsAToolError: when the engine cannot open the changeset the
// call is answered with the frozen error and the turn goes on — the model sees
// what happened, nothing panics, nothing is staged.
func TestLazyOpenFailureIsAToolError(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "could not stage."}, {Finish: "stop"}},
	})
	// A directory where the journal file should be: the changeset's
	// changeset_opened event cannot be written, so OpenOrJoin fails.
	journal := filepath.Join(f.root, ".llmwiki", "journal.ndjson")
	if err := os.Remove(journal); err != nil {
		t.Fatalf("Remove journal: %v", err)
	}
	if err := os.Mkdir(journal, 0o755); err != nil {
		t.Fatalf("Mkdir journal: %v", err)
	}

	events := f.send(t, WithOpenIntent(context.Background(), "ask: x"), trace.VerbFile)

	var res []ToolResEv
	var done bool
	for _, ev := range events {
		switch e := ev.(type) {
		case ToolResEv:
			res = append(res, e)
		case DoneEv:
			done = true
		}
	}
	if len(res) != 1 || !res[0].IsError || !strings.HasPrefix(res[0].Content, "stage: could not open a changeset for this call: ") {
		t.Fatalf("tool result = %+v, want the frozen could-not-open error", res)
	}
	if !done {
		t.Fatalf("the turn did not go on to its answer: %#v", events)
	}
}

// refusedCreatePageArgs is valid JSON that stage.create_page refuses (no
// rationale, no body): the call dispatches, runs, and comes back IsError, having
// staged nothing.
const refusedCreatePageArgs = `{"path":"wiki/concepts/refused.md"}`

// rejectedDirs lists the ids under changesets/rejected/.
func (f *lazyFixture) rejectedDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.root, ".llmwiki", "changesets", "rejected"))
	if err != nil {
		t.Fatalf("ReadDir rejected: %v", err)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.Name())
	}
	return ids
}

// hookStreamer calls before(n) at the start of the n'th Stream call (1-based),
// then answers from the scripted fake: the place a test changes the engine
// between two rounds of one turn.
type hookStreamer struct {
	inner  *fakeStreamer
	before func(call int)
	n      int
}

func (h *hookStreamer) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	h.n++
	if h.before != nil {
		h.before(h.n)
	}
	return h.inner.Stream(ctx, req)
}

// TestLazyOpenRefusedFirstCallRejectsEmpty: the loop opened a changeset for a
// staging call that then staged nothing (the call came back IsError), and the
// turn ended without any other op. Nothing is left to review, and the old pane
// rejected exactly such a changeset (forwardTurn, "ask turn staged nothing");
// the lazy open must too, or a model that fumbles its first stage.create_page
// leaves an empty changeset in the curator's Review. Rejected through
// Engine.Reject like any rejection: journalled, moved to rejected/, with no
// session adopted into it.
func TestLazyOpenRefusedFirstCallRejectsEmpty(t *testing.T) {
	const reason = "ask turn staged nothing"
	for _, tc := range []struct {
		name   string
		rounds [][]llm.Chunk
		mutate func(f *lazyFixture)
	}{
		{"answer_after_the_refusal", [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", refusedCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "I could not stage that."}, {Finish: "stop"}},
		}, nil},
		{"round_limit_after_the_refusal", [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", refusedCreatePageArgs), {Finish: "tool_calls"}},
		}, func(f *lazyFixture) { f.loop.cfg.MaxToolRounds = 1 }},
		{"provider_error_after_the_refusal", [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", refusedCreatePageArgs), {Finish: "tool_calls"}},
			{{Err: errors.New("provider exploded")}},
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLazyFixture(t, tc.rounds)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			out := make(chan Event, 64)
			_ = f.loop.Send(trace.WithVerb(WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile), lazySession, "file this answer", out)
			events := drain(out)

			var refused bool
			for _, ev := range events {
				if r, ok := ev.(ToolResEv); ok && r.Name == "stage.create_page" && r.IsError {
					refused = true
				}
			}
			if !refused {
				t.Fatalf("the staging call was not refused; the test proves nothing: %#v", events)
			}
			if cs, err := f.engine.Current(); err == nil {
				t.Fatalf("changeset %s is still open with %d live ops after a turn that staged nothing", cs.ID, len(cs.Live()))
			}
			rejected := f.rejectedDirs(t)
			if len(rejected) != 1 {
				t.Fatalf("rejected/ holds %v, want exactly the one lazily opened changeset", rejected)
			}
			if _, err := os.Stat(filepath.Join(f.root, ".llmwiki", "changesets", "rejected", rejected[0], "session.ndjson")); !os.IsNotExist(err) {
				t.Fatalf("a session.ndjson was written into the rejected changeset (%v); nothing is adopted into it", err)
			}
			journal := string(f.journalBytes(t))
			if n := strings.Count(journal, `"changeset_opened"`); n != 1 {
				t.Fatalf("journal holds %d changeset_opened events, want 1:\n%s", n, journal)
			}
			if !strings.Contains(journal, `"changeset_rejected"`) || !strings.Contains(journal, `"message":"`+reason+`"`) {
				t.Fatalf("journal has no changeset_rejected with the reason %q:\n%s", reason, journal)
			}
			// The shell hears the changeset is gone (the empty StageEv the old
			// pane streamed) BEFORE the turn's terminal event, so a consumer that
			// stops at the terminal event has seen it.
			emptyAt, terminalAt := -1, -1
			for i, ev := range events {
				switch e := ev.(type) {
				case StageEv:
					if e.ChangesetID == "" {
						emptyAt = i
					}
				case DoneEv, ErrorEv:
					terminalAt = i
				}
			}
			if emptyAt < 0 || terminalAt < 0 || emptyAt > terminalAt {
				t.Fatalf("empty StageEv at %d, terminal event at %d; want the StageEv first:\n%#v", emptyAt, terminalAt, events)
			}
		})
	}
}

// TestLazyOpenedWithOpsKept: a lazily opened changeset with at least one live op
// is the curator's to review and is left exactly as it was — whether the first
// call staged, or a refused first call was followed by one that did.
func TestLazyOpenedWithOpsKept(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rounds [][]llm.Chunk
	}{
		{"first_call_stages", [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "Filed."}, {Finish: "stop"}},
		}},
		{"refused_then_stages", [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", refusedCreatePageArgs), {Finish: "tool_calls"}},
			{toolCallChunk("call-2", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "Filed on the second try."}, {Finish: "stop"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLazyFixture(t, tc.rounds)
			events := f.send(t, WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile)

			cs, err := f.engine.Current()
			if err != nil {
				t.Fatalf("Current: %v, want the changeset kept", err)
			}
			if len(cs.Live()) != 1 {
				t.Fatalf("the changeset holds %d live ops, want the staged page", len(cs.Live()))
			}
			if rejected := f.rejectedDirs(t); len(rejected) != 0 {
				t.Fatalf("rejected/ holds %v, want nothing rejected", rejected)
			}
			if strings.Contains(string(f.journalBytes(t)), "changeset_rejected") {
				t.Fatal("the journal holds a changeset_rejected event for a changeset with a live op")
			}
			for _, ev := range events {
				if s, ok := ev.(StageEv); ok && s.ChangesetID == "" {
					t.Fatalf("an empty StageEv was streamed though the changeset is open: %#v", events)
				}
			}
		})
	}
}

// TestJoinedChangesetNeverRejected: only a changeset THIS turn's lazy open
// created is the turn's to reject. One that was open before the turn (an
// `lw ingest` left it), and one that took its place while the turn ran, are
// someone else's work, empty or not.
func TestJoinedChangesetNeverRejected(t *testing.T) {
	t.Run("opened_before_the_turn", func(t *testing.T) {
		f := newLazyFixture(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", refusedCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "could not."}, {Finish: "stop"}},
		})
		opened, err := f.engine.OpenChangeset("lw ingest a.md", stage.Author{Kind: "agent", Model: "ingest-model"})
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		before := string(f.journalBytes(t))

		f.send(t, WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile)

		cs, err := f.engine.Current()
		if err != nil || cs.ID != opened.ID || cs.Intent != "lw ingest a.md" {
			t.Fatalf("Current = %+v, %v; want the pre-opened changeset untouched", cs, err)
		}
		if rejected := f.rejectedDirs(t); len(rejected) != 0 {
			t.Fatalf("rejected/ holds %v; a pre-opened changeset must never be rejected", rejected)
		}
		if added := strings.TrimPrefix(string(f.journalBytes(t)), before); strings.Contains(added, "changeset_") {
			t.Fatalf("the turn wrote changeset events into the journal:\n%s", added)
		}
	})

	// joined_by_another_verb: `lw ingest` opens a changeset in the instant between
	// the loop's "nothing is open" check and its open. OpenOrJoin joins it
	// instead of creating one, so the changeset is not the turn's — whatever the
	// refused call leaves in it.
	t.Run("joined_by_another_verb", func(t *testing.T) {
		f := newLazyFixture(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", refusedCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "could not."}, {Finish: "stop"}},
		})
		var other *stage.Changeset
		f.loop.beforeLazyOpen = func() {
			cs, err := f.engine.OpenChangeset("lw ingest b.md", stage.Author{Kind: "agent", Model: "ingest-model"})
			if err != nil {
				t.Errorf("OpenChangeset: %v", err)
			}
			other = cs
		}

		f.send(t, WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile)

		if other == nil {
			t.Fatal("the hook never ran")
		}
		cs, err := f.engine.Current()
		if err != nil || cs.ID != other.ID {
			t.Fatalf("Current = %+v, %v; want the other verb's changeset %s kept", cs, err, other.ID)
		}
		if !strings.HasPrefix(cs.Intent, "lw ingest b.md + ask: file it") {
			t.Fatalf("Intent = %q, want the join recorded on the other verb's changeset", cs.Intent)
		}
		if rejected := f.rejectedDirs(t); len(rejected) != 0 {
			t.Fatalf("rejected/ holds %v; a changeset the turn joined must never be rejected", rejected)
		}
	})

	t.Run("replaced_while_the_turn_ran", func(t *testing.T) {
		f := newLazyFixture(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", refusedCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "could not."}, {Finish: "stop"}},
		})
		var other *stage.Changeset
		hook := &hookStreamer{inner: f.fake, before: func(call int) {
			if call != 2 { // between the refused call and the closing answer
				return
			}
			// Review rejects the lazily opened changeset and an `lw ingest` opens
			// its own: empty, and not ours.
			if err := f.engine.Reject("rejected in Review"); err != nil {
				t.Errorf("Reject: %v", err)
			}
			cs, err := f.engine.OpenChangeset("lw ingest later.md", stage.Author{Kind: "agent"})
			if err != nil {
				t.Errorf("OpenChangeset: %v", err)
			}
			other = cs
		}}
		f.loop = newLoop(hook, f.reg, f.store, f.engine, LoopConfig{})

		f.send(t, WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile)

		if other == nil {
			t.Fatal("the hook never ran")
		}
		cs, err := f.engine.Current()
		if err != nil || cs.ID != other.ID {
			t.Fatalf("Current = %+v, %v; want the other verb's changeset %s untouched", cs, err, other.ID)
		}
		if rejected := f.rejectedDirs(t); len(rejected) != 1 {
			t.Fatalf("rejected/ holds %v, want only the one Review rejected", rejected)
		}
		if n := strings.Count(string(f.journalBytes(t)), `"changeset_rejected"`); n != 1 {
			t.Fatalf("journal holds %d changeset_rejected events, want only Review's", n)
		}
	})
}

// TestLazyOpenCancelledTurnRejectsEmpty: a turn cut short (the pane cancelled it,
// ctrl+c) sends no terminal event, so there is no event for the rejection to
// precede — but the empty changeset its lazy open made is just as empty, and
// is rejected as Send returns.
func TestLazyOpenCancelledTurnRejectsEmpty(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", refusedCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "never reached"}, {Finish: "stop"}},
	})
	ctx, cancel := context.WithCancel(trace.WithVerb(WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile))
	defer cancel()
	f.loop = newLoop(&hookStreamer{inner: f.fake, before: func(call int) {
		if call == 2 {
			cancel() // the pane cancels the turn after the refused call
		}
	}}, f.reg, f.store, f.engine, LoopConfig{})

	out := make(chan Event, 64)
	_ = f.loop.Send(ctx, lazySession, "file this answer", out)
	for _, ev := range drain(out) {
		if _, ok := ev.(DoneEv); ok {
			t.Fatal("the cancelled turn ended with a DoneEv; the test proves nothing")
		}
	}

	if cs, err := f.engine.Current(); err == nil {
		t.Fatalf("changeset %s is still open after a cancelled turn that staged nothing", cs.ID)
	}
	if rejected := f.rejectedDirs(t); len(rejected) != 1 {
		t.Fatalf("rejected/ holds %v, want the lazily opened changeset", rejected)
	}
}

// readChangesetIntent reads the intent out of <state>/<id>/changeset.json.
func (f *lazyFixture) readChangesetIntent(t *testing.T, state, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, ".llmwiki", "changesets", state, id, "changeset.json"))
	if err != nil {
		t.Fatalf("read changeset.json: %v", err)
	}
	var cs struct {
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatalf("decode changeset.json: %v", err)
	}
	return cs.Intent
}

// TestStageOpenCreatedInAskTurnIsSettled (057 FIX2 R1): the ask web flow still
// offers stage.open, and a model that calls it opens a changeset the lazy open
// never saw. A changeset that call CREATED is the turn's, so it is settled like
// a lazily opened one — rejected if the turn ends with nothing in it — and keeps
// the intent the model gave it. One it only joined is someone else's.
func TestStageOpenCreatedInAskTurnIsSettled(t *testing.T) {
	const reason = "ask turn staged nothing"

	t.Run("opened_and_abandoned_is_rejected_keeping_the_model_intent", func(t *testing.T) {
		f := newLazyFixtureWeb(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_open", `{"intent":"look into x"}`), {Finish: "tool_calls"}},
			{{Text: "Nothing to ingest; answered from the vault."}, {Finish: "stop"}},
		}, true)
		events := f.send(t, WithOpenIntent(context.Background(), "ask: what is x?"), trace.VerbAsk)

		if cs, err := f.engine.Current(); err == nil {
			t.Fatalf("changeset %s (intent %q, %d live ops) is left open by an ask turn that staged nothing", cs.ID, cs.Intent, len(cs.Live()))
		}
		rejected := f.rejectedDirs(t)
		if len(rejected) != 1 {
			t.Fatalf("rejected/ holds %v, want the changeset stage.open created", rejected)
		}
		if got := f.readChangesetIntent(t, "rejected", rejected[0]); got != "look into x" {
			t.Fatalf("Intent = %q, want the model's own %q (nothing is stamped over it)", got, "look into x")
		}
		if journal := string(f.journalBytes(t)); !strings.Contains(journal, `"message":"`+reason+`"`) {
			t.Fatalf("journal lacks the reject reason %q:\n%s", reason, journal)
		}
		// Nothing was staged, so no trail was started: an abandoned changeset has
		// no session.ndjson (the tee begins at the first staged op, not at open).
		if _, ok := f.sessionAt(t, "rejected", rejected[0]); ok {
			t.Fatalf("rejected/%s holds a session.ndjson though no op was ever staged", rejected[0])
		}
		emptyAt, doneAt := -1, -1
		for i, ev := range events {
			switch e := ev.(type) {
			case StageEv:
				if e.ChangesetID == "" {
					emptyAt = i
				}
			case DoneEv:
				doneAt = i
			}
		}
		if emptyAt < 0 || doneAt < 0 || emptyAt > doneAt {
			t.Fatalf("empty StageEv at %d, DoneEv at %d; want the StageEv first", emptyAt, doneAt)
		}
	})

	t.Run("opened_then_staged_is_kept", func(t *testing.T) {
		f := newLazyFixtureWeb(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_open", `{"intent":"file x"}`), {Finish: "tool_calls"}},
			{toolCallChunk("call-2", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "Filed."}, {Finish: "stop"}},
		}, true)
		f.send(t, WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile)

		cs, err := f.engine.Current()
		if err != nil {
			t.Fatalf("Current: %v, want the changeset kept", err)
		}
		if cs.Intent != "file x" || len(cs.Live()) != 1 {
			t.Fatalf("changeset = intent %q, %d live ops; want the model's intent and the staged page", cs.Intent, len(cs.Live()))
		}
		if rejected := f.rejectedDirs(t); len(rejected) != 0 {
			t.Fatalf("rejected/ holds %v", rejected)
		}
	})

	// The instant between the baseline check and stage.open's OpenOrJoin: another
	// verb opens a changeset, stage.open JOINS it (rewriting its intent to
	// "<existing> + <intent>"), and the turn must not claim it.
	t.Run("joined_in_the_race_is_not_ours", func(t *testing.T) {
		f := newLazyFixtureWeb(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_open", `{"intent":"look into x"}`), {Finish: "tool_calls"}},
			{{Text: "answered."}, {Finish: "stop"}},
		}, true)
		var other *stage.Changeset
		f.loop.beforeLazyOpen = func() {
			cs, err := f.engine.OpenChangeset("lw ingest b.md", stage.Author{Kind: "agent"})
			if err != nil {
				t.Errorf("OpenChangeset: %v", err)
			}
			other = cs
		}

		f.send(t, WithOpenIntent(context.Background(), "ask: what is x?"), trace.VerbAsk)

		if other == nil {
			t.Fatal("the hook never ran")
		}
		cs, err := f.engine.Current()
		if err != nil || cs.ID != other.ID {
			t.Fatalf("Current = %+v, %v; want the other verb's changeset, still open", cs, err)
		}
		if !strings.HasPrefix(cs.Intent, "lw ingest b.md + look into x") {
			t.Fatalf("Intent = %q, want the join recorded (the premise of the test)", cs.Intent)
		}
		if rejected := f.rejectedDirs(t); len(rejected) != 0 {
			t.Fatalf("rejected/ holds %v; a changeset stage.open joined must never be rejected", rejected)
		}
	})

	t.Run("joined_is_not_ours", func(t *testing.T) {
		f := newLazyFixtureWeb(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_open", `{"intent":"look into x"}`), {Finish: "tool_calls"}},
			{{Text: "answered."}, {Finish: "stop"}},
		}, true)
		opened, err := f.engine.OpenChangeset("lw ingest a.md", stage.Author{Kind: "agent"})
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		f.send(t, WithOpenIntent(context.Background(), "ask: what is x?"), trace.VerbAsk)

		cs, err := f.engine.Current()
		if err != nil || cs.ID != opened.ID {
			t.Fatalf("Current = %+v, %v; want the other verb's changeset, still open", cs, err)
		}
		if rejected := f.rejectedDirs(t); len(rejected) != 0 {
			t.Fatalf("rejected/ holds %v; a changeset stage.open only joined must never be rejected", rejected)
		}
	})

	// A caller that set no intent did not opt in (R3): its stage.open is the
	// model's own business, as it was before 057.
	t.Run("not_settled_without_caller_opt_in", func(t *testing.T) {
		f := newLazyFixtureWeb(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_open", `{"intent":"look into x"}`), {Finish: "tool_calls"}},
			{{Text: "answered."}, {Finish: "stop"}},
		}, true)
		f.send(t, context.Background(), trace.VerbAsk)
		if _, err := f.engine.Current(); err != nil {
			t.Fatalf("Current: %v, want the changeset left as before 057", err)
		}
	})
}

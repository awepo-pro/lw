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
	return &lazyFixture{root: root, engine: e, store: store, fake: fake, loop: newLoop(fake, reg, store, e, cfg)}
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

// TestLazyOpenDefaultIntent: a caller that sets no intent still gets a
// readable one in `lw log` — the verb names the turn.
func TestLazyOpenDefaultIntent(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "ok"}, {Finish: "stop"}},
	})
	f.send(t, context.Background(), trace.VerbFile)
	cs, err := f.engine.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cs.Intent != "agent: file" {
		t.Fatalf("Intent = %q, want %q", cs.Intent, "agent: file")
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

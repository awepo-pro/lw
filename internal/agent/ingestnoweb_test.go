package agent

// ingestnoweb_test.go is 056's frozen block: an ingest turn is not offered
// web.search (TD-16). After 048/051/053 every wiki discovery tool in an ingest
// turn is bounded but web.search is not, and a curator turn is offered every
// registered tool. The user's real config has [web] (Tavily, 1,000 credits a
// month): 8 of 8 real ingest turns were offered web_search, none of their 104
// calls used it, and each earlier cap had displaced the loop into the next
// unbounded tool — this one spends money. Ingest compiles a source the user
// already supplied; web lookup belongs to ask. So the loop withholds the tool
// (request and prompt) and refuses a call to it before dispatch. Permanent
// regression tests (D-10C).
//
// It reuses 039's toolResults/oneRound (askmode_test.go) and 048's rbCleanStop
// (readbudget_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
	"github.com/awepo-pro/lw/internal/web"
)

// inwRefusal is the frozen refusal, byte for byte. It is a literal copy of the
// design's bytes, not built from the loop's own constants, so a drift in them
// fails here.
const inwRefusal = "tool web.search is not available in an ingest turn: the source is already in hand — read it with raw.get and stage pages from it"

// inwSearch is a web.SearchProvider that counts the calls that reach it: the
// provider is the one observer of "was web.search dispatched".
type inwSearch struct {
	mu    sync.Mutex
	calls int
}

func (s *inwSearch) Search(ctx context.Context, query string, max int) ([]web.SearchHit, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return []web.SearchHit{{Title: "hit", URL: "https://example.com/hit", Snippet: "a hit"}}, nil
}

func (s *inwSearch) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// inwFixture is newTestLoopFixture over a registry that offers web.search
// through a counting provider, or — web false — one that does not.
type inwFixture struct {
	*testLoopFixture
	loop   *Loop
	fake   *fakeStreamer
	search *inwSearch
}

func newInwFixture(t *testing.T, withWeb bool, rounds [][]llm.Chunk) *inwFixture {
	t.Helper()
	fx := newTestLoopFixture(t)
	f := &inwFixture{testLoopFixture: fx, search: &inwSearch{}}
	d := tools.Deps{Vault: fx.engine.Vault(), Index: fx.engine.Index(), Engine: fx.engine, Author: stage.Author{Kind: "agent", Model: "test-model"}}
	if withWeb {
		d.Search = f.search
	}
	fx.reg = tools.NewRegistry(d)
	f.fake = &fakeStreamer{rounds: rounds}
	f.loop = newLoop(f.fake, fx.reg, fx.store, fx.engine, LoopConfig{MaxToolRounds: 50})
	return f
}

// send runs one turn under verb and returns its events and error.
func (f *inwFixture) send(t *testing.T, verb string) ([]Event, error) {
	t.Helper()
	ctx := context.Background()
	if verb != "" {
		ctx = trace.WithVerb(ctx, verb)
	}
	out := make(chan Event, 256)
	err := f.loop.Send(ctx, f.csID, "ingest this source", out)
	return drain(out), err
}

// inwWebRound is a round in which the model calls web_search.
func inwWebRound(id string) []llm.Chunk {
	return []llm.Chunk{toolCallChunk(id, "web_search", `{"query":"kv cache"}`), {Finish: "tool_calls"}}
}

func inwStopRound() []llm.Chunk { return []llm.Chunk{{Text: "done"}, {Finish: "stop"}} }

// inwNames is the wire names of defs, in order.
func inwNames(defs []llm.ToolDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
}

// inwJSON is v's JSON, for a byte comparison that covers every field.
func inwJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// inwWithout is defs minus the web_search definition, in the same order.
func inwWithout(defs []llm.ToolDef) []llm.ToolDef {
	var out []llm.ToolDef
	for _, d := range defs {
		if d.Name != "web_search" {
			out = append(out, d)
		}
	}
	return out
}

// TestIngestTurnOmitsWebSearch: over a registry that offers web.search, an
// ingest turn's request advertises Definitions() minus web_search — same order,
// same bytes per definition — on every round, and its system prompt is the one
// a vault with no web provider sends, so the 010/017 "search the web with
// web.search before you answer" paragraphs never reach an ingest. Everything
// else in the request is what a lint turn (the other curator turn) sends.
func TestIngestTurnOmitsWebSearch(t *testing.T) {
	f := newInwFixture(t, true, [][]llm.Chunk{inwWebRound("call-1"), inwStopRound()})
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	full := f.reg.Definitions()
	want := inwWithout(full)
	if len(want) != len(full)-1 {
		t.Fatalf("Definitions() has %d tools and %d without web_search; the registry must offer web_search or this test is vacuous: %v", len(full), len(want), inwNames(full))
	}
	reqs := f.fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Stream called %d times, want 2", len(reqs))
	}
	for i, req := range reqs {
		if got, w := inwJSON(t, req.Tools), inwJSON(t, want); got != w {
			t.Fatalf("round %d tools = %v, want Definitions() minus web_search = %v", i+1, inwNames(req.Tools), inwNames(want))
		}
		for _, d := range req.Tools {
			if d.Name == "web_search" {
				t.Fatalf("round %d advertises web_search", i+1)
			}
		}
		if req.Messages[0].Role != "system" || req.Messages[0].Content != systemPromptFor(false) {
			t.Fatalf("round %d message 0 is not systemPromptFor(false):\n%q", i+1, req.Messages[0].Content)
		}
		if strings.Contains(req.Messages[0].Content, "web.search") {
			t.Fatalf("round %d system prompt still mentions web.search", i+1)
		}
	}

	// Otherwise byte-identical: the other three system parts and the user
	// message of the first request are what a lint turn over the same registry
	// sends — the only differences are the web_search definition and the web
	// paragraphs of message 0.
	g := newInwFixture(t, true, [][]llm.Chunk{inwStopRound()})
	if _, err := g.send(t, "lint"); err != nil {
		t.Fatalf("lint Send: %v", err)
	}
	lint := g.fake.Requests()[0]
	if lint.Messages[0].Content != systemPromptFor(true) {
		t.Fatalf("control: the lint turn's prompt is not systemPromptFor(true)")
	}
	if got, w := inwJSON(t, reqs[0].Messages[1:]), inwJSON(t, lint.Messages[1:]); got != w {
		t.Errorf("an ingest request differs from a lint request beyond message 0:\n got  %s\n want %s", got, w)
	}
}

// TestIngestTurnRefusesWebSearch: the model can still name web_search — it saw
// it in session history, or invents it — and the call is refused before
// dispatch: the frozen text as an error result in the event, the wire message
// and the session record, the provider never called, and the turn goes on to its
// own stop.
func TestIngestTurnRefusesWebSearch(t *testing.T) {
	f := newInwFixture(t, true, [][]llm.Chunk{inwWebRound("call-1"), inwStopRound()})
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if len(res) != 1 {
		t.Fatalf("got %d tool results, want 1: %#v", len(res), res)
	}
	if r := res[0]; r.ID != "call-1" || r.Name != "web.search" || !r.IsError || r.Content != inwRefusal {
		t.Fatalf("tool result = %+v, want IsError web.search carrying the frozen refusal %q", r, inwRefusal)
	}
	if n := f.search.count(); n != 0 {
		t.Fatalf("the provider was called %d time(s); a refused call must never reach it", n)
	}
	if done, ok := events[len(events)-1].(DoneEv); !ok || done.Reason != "stop" || done.Rounds != 2 {
		t.Fatalf("last event = %#v, want DoneEv{stop, 2}", events[len(events)-1])
	}

	// On the wire the refusal is the tool message answering call-1, the last
	// message of the request that follows it.
	reqs := f.fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Stream called %d times, want 2 (the turn must continue past the refusal)", len(reqs))
	}
	msgs := reqs[1].Messages
	wire := msgs[len(msgs)-1]
	if wire.Role != "tool" || wire.ToolCallID != "call-1" || wire.Content != inwRefusal {
		t.Fatalf("round 2 does not end with the refusal as call-1's tool message: %+v", wire)
	}
	if err := validChat(msgs); err != nil {
		t.Errorf("round 2's request is not a valid chat: %v", err)
	}

	// The session record is written as for any tool error: the refusal, not
	// staged.
	sess, gerr := f.store.Get(f.csID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	var rec *Record
	for i := range sess.Records {
		if sess.Records[i].Role == "tool" && sess.Records[i].Result == inwRefusal {
			rec = &sess.Records[i]
		}
	}
	if rec == nil || rec.Tool != "web.search" || rec.Staged {
		t.Fatalf("no session record holds the refusal as an unstaged web.search result: %+v", sess.Records)
	}
}

// TestIngestWebRefusalNotBadCall: a refusal is feedback, not a malformed call
// (A-039-1's reasoning again). Three refused web_search calls in a row, then one
// malformed call, then the model's own stop: the refusals neither count toward
// the two-in-a-row abort nor — the malformed call being only the first in a row —
// does the turn end on it.
func TestIngestWebRefusalNotBadCall(t *testing.T) {
	f := newInwFixture(t, true, [][]llm.Chunk{
		inwWebRound("call-1"), inwWebRound("call-2"), inwWebRound("call-3"),
		{toolCallChunk("call-4", "wiki_get", `{not json`), {Finish: "tool_calls"}},
		inwStopRound(),
	})
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if len(res) != 4 {
		t.Fatalf("got %d tool results, want 3 refusals and 1 malformed-call error: %#v", len(res), res)
	}
	for i := 0; i < 3; i++ {
		if res[i].Content != inwRefusal || !res[i].IsError {
			t.Fatalf("result %d = %+v, want the frozen refusal", i+1, res[i])
		}
	}
	if res[3].Name != "wiki.get" || !res[3].IsError || res[3].Content == inwRefusal {
		t.Fatalf("result 4 = %+v, want the malformed-call error for wiki.get", res[3])
	}
	if n := f.search.count(); n != 0 {
		t.Fatalf("the provider was called %d time(s)", n)
	}
}

// TestIngestWebRefusalDoesNotResetBadCalls is the other half: a refusal between
// two malformed calls is not a good call, so it must not reset the budget — the
// second malformed call is still the second in a row and ends the turn.
func TestIngestWebRefusalDoesNotResetBadCalls(t *testing.T) {
	f := newInwFixture(t, true, [][]llm.Chunk{
		{toolCallChunk("call-1", "wiki_get", `{not json`), {Finish: "tool_calls"}},
		inwWebRound("call-2"),
		{toolCallChunk("call-3", "wiki_get", `{not json`), {Finish: "tool_calls"}},
		inwStopRound(),
	})
	_, err := f.send(t, "ingest")
	if err == nil || !strings.Contains(err.Error(), "two consecutive unusable calls") {
		t.Fatalf("Send = %v, want the turn to end on the second malformed call (the refusal between them must not reset the budget)", err)
	}
	if got := len(f.fake.Requests()); got != 3 {
		t.Fatalf("Stream called %d times, want 3", got)
	}
}

// TestIngestUnknownToolStillBadCall: withholding web_search must not turn the
// ingest turn into an offered-set turn. The turn stays a curator turn — offered
// is nil — so a name no tool is registered under still reaches the registry and
// fails there as tools.ErrUnknownTool, inside the bad-call budget: two in a row
// end the turn, exactly as before 056.
func TestIngestUnknownToolStillBadCall(t *testing.T) {
	f := newInwFixture(t, true, [][]llm.Chunk{
		{toolCallChunk("call-1", "bash", `{"cmd":"ls"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-2", "bash", `{"cmd":"ls"}`), {Finish: "tool_calls"}},
		inwStopRound(),
	})
	events, err := f.send(t, "ingest")
	if err == nil || !strings.Contains(err.Error(), "two consecutive unusable calls") || !errors.Is(err, tools.ErrUnknownTool) {
		t.Fatalf("Send = %v, want the turn to end on the second unknown-tool call, wrapping tools.ErrUnknownTool", err)
	}
	if got := len(f.fake.Requests()); got != 2 {
		t.Fatalf("Stream called %d times, want 2", got)
	}
	res := toolResults(events)
	if len(res) != 2 {
		t.Fatalf("got %d tool results, want 2 unknown-tool errors: %#v", len(res), res)
	}
	for i, r := range res {
		if !r.IsError || r.Content == inwRefusal || strings.Contains(r.Content, "is not available in") {
			t.Fatalf("result %d = %+v, want the registry's unknown-tool error, not a refusal", i+1, r)
		}
	}
}

// TestIngestWebSearchWithoutProviderIsUnknownTool: a vault with no web
// provider has no web.search to withhold, so a call to it is an unregistered
// name like any other and keeps today's path — the registry's ErrUnknownTool,
// inside the bad-call budget — rather than the refusal, which would say a tool
// is unavailable in a turn that never had it.
func TestIngestWebSearchWithoutProviderIsUnknownTool(t *testing.T) {
	f := newInwFixture(t, false, [][]llm.Chunk{inwWebRound("call-1"), inwWebRound("call-2"), inwStopRound()})
	events, err := f.send(t, "ingest")
	if err == nil || !strings.Contains(err.Error(), "two consecutive unusable calls") || !errors.Is(err, tools.ErrUnknownTool) {
		t.Fatalf("Send = %v, want the turn to end on the second unknown-tool call, wrapping tools.ErrUnknownTool", err)
	}
	for i, r := range toolResults(events) {
		if r.Content == inwRefusal {
			t.Fatalf("result %d is the 056 refusal; a vault without web has no tool to withhold", i+1)
		}
	}
}

// TestNonIngestVerbsKeepWebSearch: 056 changes the ingest verb and nothing
// else. lint, file and a verb-less turn are curator turns that keep every tool
// and the web paragraphs; a TUI ask turn keeps askTools plus askWebTools and the
// web ask prompt; query keeps its read-only set. And in each of the web-offering
// ones a web_search call still dispatches — the refusal is ingest's alone.
func TestNonIngestVerbsKeepWebSearch(t *testing.T) {
	for _, verb := range []string{"lint", "file", ""} {
		t.Run("curator_"+verb, func(t *testing.T) {
			f := newInwFixture(t, true, [][]llm.Chunk{inwWebRound("call-1"), inwStopRound()})
			events, err := f.send(t, verb)
			rbCleanStop(t, events, err)

			full := f.reg.Definitions()
			var has bool
			for _, d := range full {
				has = has || d.Name == "web_search"
			}
			if !has {
				t.Fatalf("the registry does not offer web_search; this test is vacuous: %v", inwNames(full))
			}
			for i, req := range f.fake.Requests() {
				if got, w := inwJSON(t, req.Tools), inwJSON(t, full); got != w {
					t.Fatalf("round %d tools = %v, want Definitions() = %v", i+1, inwNames(req.Tools), inwNames(full))
				}
				if req.Messages[0].Content != systemPromptFor(true) {
					t.Fatalf("round %d message 0 is not systemPromptFor(true)", i+1)
				}
			}
			res := toolResults(events)
			if len(res) != 1 || res[0].IsError || res[0].Content == inwRefusal {
				t.Fatalf("web_search result = %+v, want a dispatched, successful call", res)
			}
			if n := f.search.count(); n != 1 {
				t.Fatalf("the provider was called %d time(s), want 1", n)
			}
		})
	}

	t.Run("ask_with_web", func(t *testing.T) {
		f := newInwFixture(t, true, [][]llm.Chunk{inwWebRound("call-1"), inwStopRound()})
		events, err := f.send(t, "ask")
		rbCleanStop(t, events, err)

		want := f.reg.DefinitionsOf(append(append([]string(nil), askTools...), askWebTools...))
		wantWire := make([]string, len(wantAskWebTools))
		for i, n := range wantAskWebTools {
			wantWire[i] = tools.WireName(n)
		}
		for i, req := range f.fake.Requests() {
			if got, w := inwJSON(t, req.Tools), inwJSON(t, want); got != w {
				t.Fatalf("round %d tools = %v, want askTools + askWebTools = %v", i+1, inwNames(req.Tools), inwNames(want))
			}
			if got := strings.Join(inwNames(req.Tools), ","); got != strings.Join(wantWire, ",") {
				t.Fatalf("round %d tools = %s, want the frozen ask+web list %s", i+1, got, strings.Join(wantWire, ","))
			}
			if req.Messages[0].Content != askPromptFor(true) {
				t.Fatalf("round %d message 0 is not askPromptFor(true)", i+1)
			}
		}
		res := toolResults(events)
		if len(res) != 1 || res[0].IsError || res[0].Content == inwRefusal {
			t.Fatalf("web_search result = %+v, want a dispatched, successful call", res)
		}
		if n := f.search.count(); n != 1 {
			t.Fatalf("the provider was called %d time(s), want 1", n)
		}
	})

	t.Run("query_with_web_registry", func(t *testing.T) {
		f := newInwFixture(t, true, [][]llm.Chunk{inwStopRound()})
		if _, err := f.send(t, "query"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		req := f.fake.Requests()[0]
		wantWire := make([]string, len(wantQueryTools))
		for i, n := range wantQueryTools {
			wantWire[i] = tools.WireName(n)
		}
		if got := strings.Join(inwNames(req.Tools), ","); got != strings.Join(wantWire, ",") {
			t.Fatalf("query tools = %s, want %s", got, strings.Join(wantWire, ","))
		}
		if req.Messages[0].Content != askPromptFor(false) {
			t.Fatalf("query message 0 is not askPromptFor(false)")
		}
	})
}

// TestIngestNoWebRegistryByteIdentical: a vault with no web provider has no
// web_search to withhold, so its ingest request is exactly the pre-056 shape —
// Definitions() whole, systemPromptFor(false) — and, as a curator turn
// indistinguishable from a lint turn over the same registry, equal to a lint
// request in every byte. lweval's registry has no web tool, so none of its
// ingest numbers move.
func TestIngestNoWebRegistryByteIdentical(t *testing.T) {
	f := newInwFixture(t, false, [][]llm.Chunk{inwStopRound()})
	if _, err := f.send(t, "ingest"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := f.fake.Requests()[0]
	full := f.reg.Definitions()
	if got, w := inwJSON(t, req.Tools), inwJSON(t, full); got != w {
		t.Fatalf("tools = %v, want Definitions() = %v", inwNames(req.Tools), inwNames(full))
	}
	if req.Messages[0].Content != systemPromptFor(false) {
		t.Fatalf("message 0 is not systemPromptFor(false)")
	}

	g := newInwFixture(t, false, [][]llm.Chunk{inwStopRound()})
	if _, err := g.send(t, "lint"); err != nil {
		t.Fatalf("lint Send: %v", err)
	}
	if got, w := inwJSON(t, req), inwJSON(t, g.fake.Requests()[0]); got != w {
		t.Errorf("an ingest request over a registry without web differs from a lint request:\n got  %s\n want %s", got, w)
	}
}

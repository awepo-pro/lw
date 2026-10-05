package agent

// askmode_test.go is 039 T1's frozen block: the ask/query turn gets its own
// system prompt and only the tools it may use, chosen by one source of truth
// (the turn's ctx verb), while every curator turn stays byte-identical to
// what v2.25.0 sent. Permanent regression tests (D-10C).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// wantAskBase is 039's frozen ask prompt, everything askPromptBase holds: the
// opening, the grounding paragraph and the evidence-citation paragraph, with
// the blank line that joins it to whatever follows. It is a literal copy of
// the design's bytes, not a reference to askPromptBase — the test must be
// able to fail when the const drifts.
const wantAskBase = `You are the llmwiki curator answering a question about this vault. You have no filesystem verbs — no write, edit,
delete or shell access, not denied but simply never offered.

Ground the answer in the vault. Find the relevant pages with wiki.search and read them with wiki.get. A wiki page
is a summary: each of its claims carries a provenance marker naming the raw source it came from. When a page is
thin, ambiguous, or lacks the detail the question needs, read the cited source with raw.get.

Cite evidence, not summaries. End every claim you draw from the vault with the provenance marker of the raw source
that supports it, e.g. "^[raw/papers/x.md]": copy it from the page you read, or write it from the raw passage you
read. Cite a page only when raw.get's header for that source names pages: then cite the page the claim comes from,
e.g. "^[raw/papers/x.md p.12]", or "p.12-13" for a claim that crosses a page break; the claim's page is the nearest
"<!-- page N -->" line above it. A source whose header names no pages is cited without a page. Never cite a wiki
page path as evidence, and never write a marker for a source you did not see cited or read.

`

// wantAskTail is askPromptTail's frozen bytes: the outside-vault sentence and
// the answer-voice sentence — copied from promptBase's own bytes, which
// carried_test.go and narration_rule_test.go pin as outsideVaultRule and
// noNarrationLine — joined by one newline, then a final newline.
const wantAskTail = outsideVaultRule + "\n" + noNarrationLine + "\n"

// TestAskPromptBytes pins the ask prompt byte for byte: askPromptFor(false)
// is askPromptBase + askPromptTail, and askPromptFor(true) inserts the two 010
// web paragraphs — unchanged, joined by one blank line — between them.
func TestAskPromptBytes(t *testing.T) {
	if askPromptBase != wantAskBase {
		t.Fatalf("askPromptBase drifted from the frozen bytes:\n got  %q\n want %q", askPromptBase, wantAskBase)
	}
	if askPromptTail != wantAskTail {
		t.Fatalf("askPromptTail drifted from the frozen bytes:\n got  %q\n want %q", askPromptTail, wantAskTail)
	}

	if got, want := askPromptFor(false), wantAskBase+wantAskTail; got != want {
		t.Fatalf("askPromptFor(false):\n got  %q\n want %q", got, want)
	}
	if got, want := askPromptFor(true), wantAskBase+webSearchRule+"\n\n"+webInjectionRule+"\n\n"+wantAskTail; got != want {
		t.Fatalf("askPromptFor(true):\n got  %q\n want %q", got, want)
	}

	// The ask prompt is not the curator prompt trimmed: it carries none of the
	// ingest policy, and promises web.search only on the web assembly.
	for _, banned := range []string{"Page thresholds", "stage.split_page", "stage.retract", "SCHEMA.md", "index.md", "## Abstract", "Lint is the engine"} {
		if strings.Contains(askPromptFor(true), banned) {
			t.Errorf("the ask prompt still carries curator policy %q", banned)
		}
	}
	if strings.Contains(askPromptFor(false), "web.search") {
		t.Error("askPromptFor(false) promises web.search")
	}
	// The two sentences the ask prompt shares with the curator prompt are the
	// same consts, so they cannot drift apart.
	for _, p := range []string{systemPromptFor(false), systemPromptFor(true)} {
		if !strings.Contains(p, promptOutsideVault+"\n"+promptAnswerVoice+"\n\n") {
			t.Error("the curator prompt no longer carries the shared outside-vault + answer-voice sentences")
		}
	}
	// A-039-3: the no-pages rule is the curator prompt's own sentence, byte for
	// byte, so the two prompts agree on when a page may be cited.
	const noPagesRule = "A source whose header names no pages is cited without a page."
	if !strings.Contains(systemPromptFor(false), noPagesRule) || !strings.Contains(askPromptFor(false), noPagesRule) {
		t.Error("the ask prompt and the curator prompt must both carry the no-pages citation rule")
	}
	if !strings.HasSuffix(askPromptFor(false), wantAskTail) || !strings.HasSuffix(askPromptFor(true), wantAskTail) {
		t.Error("an ask prompt does not end with the shared tail")
	}
}

// TestModeFromVerb pins the one source of truth: the turn's ctx verb decides
// the mode. ask and query are ask mode; every other verb — ingest, lint, the
// filing turn's file, an unset verb and one nobody has heard of — is curator
// mode, so a new verb defaults to the full curator behaviour, never to a
// silently read-only turn.
func TestModeFromVerb(t *testing.T) {
	for _, tc := range []struct {
		verb string
		want turnMode
	}{
		{"ask", modeAsk},
		{"query", modeAsk},
		{"ingest", modeCurator},
		{"lint", modeCurator},
		{"file", modeCurator},
		{"", modeCurator},
		{"xyz", modeCurator},
	} {
		if got := modeFromVerb(tc.verb); got != tc.want {
			t.Errorf("modeFromVerb(%q) = %v, want %v", tc.verb, got, tc.want)
		}
	}
}

// modeFixture is newTestLoopFixture with a registry that may offer web.search
// (a stub provider), so a test can drive a Loop over either shape of vault.
type modeFixture struct {
	*testLoopFixture
	loop *Loop
	fake *fakeStreamer
}

func newModeFixture(t *testing.T, web bool, rounds [][]llm.Chunk) *modeFixture {
	t.Helper()
	fx := newTestLoopFixture(t)
	d := tools.Deps{Vault: fx.engine.Vault(), Index: fx.engine.Index(), Engine: fx.engine, Author: stage.Author{Kind: "agent", Model: "test-model"}}
	if web {
		d.Search = stubSearchProvider{}
	}
	fx.reg = tools.NewRegistry(d)
	fake := &fakeStreamer{rounds: rounds}
	return &modeFixture{testLoopFixture: fx, loop: newLoop(fake, fx.reg, fx.store, fx.engine, LoopConfig{}), fake: fake}
}

// send runs one turn under verb and returns its events and error.
func (m *modeFixture) send(t *testing.T, verb string) ([]Event, error) {
	t.Helper()
	ctx := context.Background()
	if verb != "" {
		ctx = trace.WithVerb(ctx, verb)
	}
	out := make(chan Event, 64)
	err := m.loop.Send(ctx, m.csID, "what is the kv cache?", out)
	return drain(out), err
}

// oneRound is the cheapest scripted turn: the model answers in prose.
func oneRound() [][]llm.Chunk {
	return [][]llm.Chunk{{{Text: "ok"}, {Finish: "stop"}}}
}

// TestBuildUsesAskPromptForAsk asserts on the request the fake client really
// receives through Send: message 0 is the ask prompt for an ask or query turn
// and the curator prompt for every other verb. query never carries the web
// paragraphs, even over a registry that offers web.search; ask carries them
// exactly when it does.
func TestBuildUsesAskPromptForAsk(t *testing.T) {
	for _, tc := range []struct {
		name string
		verb string
		web  bool
		want string
	}{
		{"query", "query", false, askPromptFor(false)},
		{"query_web_registry_still_no_web", "query", true, askPromptFor(false)},
		{"ask", "ask", false, askPromptFor(false)},
		{"ask_with_web", "ask", true, askPromptFor(true)},
		{"ingest", "ingest", false, systemPromptFor(false)},
		{"ingest_with_web", "ingest", true, systemPromptFor(true)},
		{"lint", "lint", false, systemPromptFor(false)},
		{"file", "file", true, systemPromptFor(true)},
		{"no_verb", "", false, systemPromptFor(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModeFixture(t, tc.web, oneRound())
			if _, err := m.send(t, tc.verb); err != nil {
				t.Fatalf("Send: %v", err)
			}
			reqs := m.fake.Requests()
			if len(reqs) != 1 {
				t.Fatalf("Stream called %d times, want 1", len(reqs))
			}
			msgs := reqs[0].Messages
			if msgs[0].Role != "system" || msgs[0].Content != tc.want {
				t.Fatalf("message 0 (verb %q, web %v) is not the expected prompt:\n got  %q\n want %q", tc.verb, tc.web, msgs[0].Content, tc.want)
			}
		})
	}
}

// Frozen tool lists, canonical dotted names in the registry's sorted order.
var (
	wantQueryTools = []string{
		"raw.get", "raw.list", "vault.orient",
		"wiki.backlinks", "wiki.get", "wiki.lint", "wiki.neighbors", "wiki.search",
	}
	wantAskWebTools = []string{
		"raw.get", "raw.list", "stage.close", "stage.ingest_source", "stage.open", "vault.orient", "web.search",
		"wiki.backlinks", "wiki.get", "wiki.lint", "wiki.neighbors", "wiki.search",
	}
	wantCuratorTools = []string{
		"raw.get", "raw.list",
		"stage.add_link", "stage.close", "stage.create_page", "stage.ingest_source", "stage.merge_pages",
		"stage.open", "stage.patch_page", "stage.rename_page", "stage.retract", "stage.split_page",
		"vault.orient",
		"wiki.backlinks", "wiki.get", "wiki.lint", "wiki.neighbors", "wiki.search",
	}
	wantCuratorWebTools = []string{
		"raw.get", "raw.list",
		"stage.add_link", "stage.close", "stage.create_page", "stage.ingest_source", "stage.merge_pages",
		"stage.open", "stage.patch_page", "stage.rename_page", "stage.retract", "stage.split_page",
		"vault.orient", "web.search",
		"wiki.backlinks", "wiki.get", "wiki.lint", "wiki.neighbors", "wiki.search",
	}
)

// TestToolSetsPerMode asserts the tools the request advertises: exactly the
// frozen lists, in wire spelling and in the registry's sorted order, for query,
// ask (without and with web) and the curator — which keeps every tool, as
// before 039.
func TestToolSetsPerMode(t *testing.T) {
	wire := func(canonical []string) []string {
		out := make([]string, len(canonical))
		for i, n := range canonical {
			out[i] = tools.WireName(n)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		verb string
		web  bool
		want []string
	}{
		{"query", "query", false, wantQueryTools},
		{"query_web_registry_stays_read_only", "query", true, wantQueryTools},
		{"ask_without_web", "ask", false, wantQueryTools},
		{"ask_with_web", "ask", true, wantAskWebTools},
		{"curator_ingest", "ingest", false, wantCuratorTools},
		{"curator_ingest_with_web", "ingest", true, wantCuratorWebTools},
		{"curator_lint", "lint", false, wantCuratorTools},
		{"curator_file", "file", false, wantCuratorTools},
		{"curator_file_with_web", "file", true, wantCuratorWebTools},
		{"curator_no_verb", "", false, wantCuratorTools},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModeFixture(t, tc.web, oneRound())
			if _, err := m.send(t, tc.verb); err != nil {
				t.Fatalf("Send: %v", err)
			}
			reqs := m.fake.Requests()
			if len(reqs) != 1 {
				t.Fatalf("Stream called %d times, want 1", len(reqs))
			}
			var got []string
			for _, d := range reqs[0].Tools {
				got = append(got, d.Name)
			}
			want := wire(tc.want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("tools advertised for verb %q (web %v):\n got  %v\n want %v", tc.verb, tc.web, got, want)
			}
			if modeFromVerb(tc.verb) == modeCurator {
				// The curator set is Definitions() itself: schemas included.
				full := m.reg.Definitions()
				if len(full) != len(reqs[0].Tools) {
					t.Fatalf("curator request carries %d tools, Definitions() has %d", len(reqs[0].Tools), len(full))
				}
				for i := range full {
					if full[i].Name != reqs[0].Tools[i].Name || full[i].Description != reqs[0].Tools[i].Description || string(full[i].Parameters) != string(reqs[0].Tools[i].Parameters) {
						t.Fatalf("curator tool %d differs from Definitions(): %+v vs %+v", i, reqs[0].Tools[i], full[i])
					}
				}
			}
		})
	}
}

// refusalFor is the frozen message a call to a tool this turn does not offer
// gets back: the canonical name asked for, then the canonical names offered,
// sorted and comma-separated.
func refusalFor(called string, offered []string) string {
	return "tool " + called + " is not available in this turn; use one of: " + strings.Join(offered, ", ")
}

// toolResults collects every ToolResEv of a turn, in order.
func toolResults(events []Event) []ToolResEv {
	var out []ToolResEv
	for _, ev := range events {
		if r, ok := ev.(ToolResEv); ok {
			out = append(out, r)
		}
	}
	return out
}

// TestUnofferedToolRefused is the hard half of the tool sets: a prompt and a
// definitions list only ask the model not to call a tool, and a model can call
// any name it has seen in history. In a query turn the model calls
// stage_create_page with a payload that would stage a page if it were
// dispatched; the result is the frozen refusal, nothing is staged — the
// registry was never reached — and the turn goes on to its next round.
func TestUnofferedToolRefused(t *testing.T) {
	rounds := func(name string) [][]llm.Chunk {
		return [][]llm.Chunk{
			{toolCallChunk("call-1", name, wireCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "Answered without staging."}, {Finish: "stop"}},
		}
	}

	t.Run("query_turn", func(t *testing.T) {
		m := newModeFixture(t, false, rounds("stage_create_page"))
		events, err := m.send(t, "query")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		res := toolResults(events)
		if len(res) != 1 {
			t.Fatalf("got %d tool results, want 1: %#v", len(res), events)
		}
		want := refusalFor("stage.create_page", wantQueryTools)
		if !res[0].IsError || res[0].Content != want || res[0].Name != "stage.create_page" {
			t.Fatalf("tool result = %+v, want IsError with the frozen refusal %q", res[0], want)
		}
		cs, err := m.engine.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if len(cs.Ops) != 0 {
			t.Fatalf("the refused call staged %d op(s); the registry must never be reached: %+v", len(cs.Ops), cs.Ops)
		}
		// The turn continued: a second request carries the refusal back to the
		// model, as the tool message answering call-1, and the turn ends on the
		// model's own answer.
		reqs := m.fake.Requests()
		if len(reqs) != 2 {
			t.Fatalf("Stream called %d times, want 2 (the turn must continue past the refusal)", len(reqs))
		}
		last := reqs[1].Messages[len(reqs[1].Messages)-1]
		if last.Role != "tool" || last.ToolCallID != "call-1" || last.Content != want {
			t.Fatalf("round 2 does not end with the refusal as call-1's tool message: %+v", last)
		}
		if done, ok := events[len(events)-1].(DoneEv); !ok || done.Reason != "stop" || done.Rounds != 2 {
			t.Fatalf("last event = %#v, want DoneEv{stop, 2}", events[len(events)-1])
		}
	})

	t.Run("ask_with_web_lists_the_web_tools", func(t *testing.T) {
		m := newModeFixture(t, true, rounds("stage_create_page"))
		events, err := m.send(t, "ask")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		res := toolResults(events)
		want := refusalFor("stage.create_page", wantAskWebTools)
		if len(res) != 1 || !res[0].IsError || res[0].Content != want {
			t.Fatalf("tool results = %+v, want one IsError result %q", res, want)
		}
		if cs, _ := m.engine.Current(); len(cs.Ops) != 0 {
			t.Fatalf("an ask turn staged %d op(s) through a tool it was not offered", len(cs.Ops))
		}
	})

	t.Run("a_name_the_registry_never_had_is_refused_too", func(t *testing.T) {
		m := newModeFixture(t, false, rounds("bash"))
		events, err := m.send(t, "query")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		res := toolResults(events)
		want := refusalFor("bash", wantQueryTools)
		if len(res) != 1 || !res[0].IsError || res[0].Content != want {
			t.Fatalf("tool results = %+v, want one IsError result %q", res, want)
		}
	})

	// A-039-1: a refusal is feedback, not a malformed call. The retry budget
	// (maxConsecutiveBadCalls) exists to stop a model that cannot form a usable
	// call; a model that tried a stage.* verb in a read-only turn has formed a
	// perfectly good call to a tool it was not offered, and ending the turn on
	// it would throw away the answer it was about to give. max_rounds already
	// bounds a model that never stops asking.
	t.Run("two_refusals_in_a_row_still_get_an_answer", func(t *testing.T) {
		m := newModeFixture(t, false, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
			{toolCallChunk("call-2", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "done"}, {Finish: "stop"}},
		})
		events, err := m.send(t, "query")
		if err != nil {
			t.Fatalf("Send = %v, want the turn to complete: two refusals in a row must not end it", err)
		}
		var text string
		for _, ev := range events {
			switch e := ev.(type) {
			case ErrorEv:
				t.Fatalf("turn emitted ErrorEv: %v", e.Err)
			case TextDelta:
				text += e.Text
			}
		}
		if done, ok := events[len(events)-1].(DoneEv); !ok || done.Reason != "stop" || done.Rounds != 3 {
			t.Fatalf("last event = %#v, want DoneEv{stop, 3}", events[len(events)-1])
		}
		if text != "done" {
			t.Fatalf("answer = %q, want %q", text, "done")
		}
		want := refusalFor("stage.create_page", wantQueryTools)
		res := toolResults(events)
		if len(res) != 2 || !res[0].IsError || !res[1].IsError || res[0].Content != want || res[1].Content != want {
			t.Fatalf("tool results = %+v, want two IsError refusals %q", res, want)
		}
		if cs, _ := m.engine.Current(); len(cs.Ops) != 0 {
			t.Fatalf("a refused call staged %d op(s)", len(cs.Ops))
		}
		// On the wire: round 3's request carries both refusals as the tool
		// messages answering call-1 and call-2.
		reqs := m.fake.Requests()
		if len(reqs) != 3 {
			t.Fatalf("Stream called %d times, want 3", len(reqs))
		}
		got := map[string]string{}
		for _, msg := range reqs[2].Messages {
			if msg.Role == "tool" {
				got[msg.ToolCallID] = msg.Content
			}
		}
		if len(got) != 2 || got["call-1"] != want || got["call-2"] != want {
			t.Fatalf("round 3 tool messages = %v, want call-1 and call-2 both the refusal %q", got, want)
		}
	})

	t.Run("two_refusals_in_one_round_still_get_an_answer", func(t *testing.T) {
		m := newModeFixture(t, false, [][]llm.Chunk{
			{
				toolCallChunk("call-1", "stage_open", `{"intent":"x"}`),
				toolCallChunk("call-2", "stage_create_page", wireCreatePageArgs),
				{Finish: "tool_calls"},
			},
			{{Text: "done"}, {Finish: "stop"}},
		})
		events, err := m.send(t, "query")
		if err != nil {
			t.Fatalf("Send = %v, want the turn to complete", err)
		}
		if res := toolResults(events); len(res) != 2 || !res[0].IsError || !res[1].IsError {
			t.Fatalf("tool results = %+v, want two refusals", res)
		}
		if done, ok := events[len(events)-1].(DoneEv); !ok || done.Reason != "stop" {
			t.Fatalf("last event = %#v, want DoneEv{stop}", events[len(events)-1])
		}
	})

	// The other half of A-039-1: a refusal must not RESET the budget either.
	// The budget counts consecutive malformed calls; a refusal between two of
	// them is not a good call, so the second malformed call is still the second
	// in a row and ends the turn.
	t.Run("a_refusal_does_not_reset_the_malformed_call_budget", func(t *testing.T) {
		m := newModeFixture(t, false, [][]llm.Chunk{
			{toolCallChunk("call-1", "wiki_get", `{not json`), {Finish: "tool_calls"}},
			{toolCallChunk("call-2", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
			{toolCallChunk("call-3", "wiki_get", `{not json`), {Finish: "tool_calls"}},
			{{Text: "never reached"}, {Finish: "stop"}},
		})
		_, err := m.send(t, "query")
		if err == nil || !strings.Contains(err.Error(), "two consecutive unusable calls") {
			t.Fatalf("Send = %v, want the turn to end on the second malformed call (the refusal between them must not reset the budget)", err)
		}
		if got := len(m.fake.Requests()); got != 3 {
			t.Fatalf("Stream called %d times, want 3 (the turn ends on round 3's malformed call)", got)
		}
	})

	// And a refusal between two malformed calls must not be what ends the
	// turn: one malformed call, then one refusal, then a good call, is a
	// healthy turn.
	t.Run("a_refusal_after_one_malformed_call_does_not_end_the_turn", func(t *testing.T) {
		m := newModeFixture(t, false, [][]llm.Chunk{
			{toolCallChunk("call-1", "wiki_get", `{not json`), {Finish: "tool_calls"}},
			{toolCallChunk("call-2", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
			{{Text: "done"}, {Finish: "stop"}},
		})
		if _, err := m.send(t, "query"); err != nil {
			t.Fatalf("Send = %v, want the turn to complete", err)
		}
	})

	t.Run("an_offered_tool_still_dispatches", func(t *testing.T) {
		m := newModeFixture(t, false, [][]llm.Chunk{
			{toolCallChunk("call-1", "wiki_search", `{"q":"kv cache"}`), {Finish: "tool_calls"}},
			{{Text: "Found it."}, {Finish: "stop"}},
		})
		events, err := m.send(t, "query")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		res := toolResults(events)
		if len(res) != 1 || res[0].IsError {
			t.Fatalf("tool results = %+v, want one successful wiki.search result", res)
		}
	})

	t.Run("the_curator_turn_is_not_restricted", func(t *testing.T) {
		// The control: the very call refused above is a real, dispatchable call
		// in a curator turn — the refusal is the mode's doing, not the payload's.
		m := newModeFixture(t, false, rounds("stage_create_page"))
		events, err := m.send(t, "ingest")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		res := toolResults(events)
		if len(res) != 1 || res[0].IsError {
			t.Fatalf("curator stage.create_page result = %+v, want success", res)
		}
		if cs, _ := m.engine.Current(); len(cs.Ops) != 1 {
			t.Fatalf("curator turn staged %d op(s), want 1", len(cs.Ops))
		}
	})
}

// TestCuratorPromptUnchanged pins the curator system prompt's exact bytes
// across 039. systemPromptFor(true) and (false) are the prompts every ingest,
// lint and filing turn sends; 039 factors two sentences of promptBase into
// named consts so the ask prompt can share them, and that refactor must
// change nothing a curator turn sends. The expected sha256 values were
// computed from systemPromptFor on 7623a3d (v2.25.0's tree) before the first
// 039 edit — pinned as hashes, not copied bytes, because a copy of the prompt
// here would drift with the prompt and the pin would be only a mirror.
func TestCuratorPromptUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hasSearch bool
		wantLen   int
		wantSHA   string
	}{
		{"with_search", true, 6092, "c397f2920a2b47bd1e986b7a250877167ba98202a52e87555e7d65e0a9ff74e1"},
		{"without_search", false, 5224, "7583b11185aa976ffbc674d9d22cbc98efd239a303d8134696dd13da8ca95584"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := systemPromptFor(tc.hasSearch)
			sum := sha256.Sum256([]byte(got))
			if hex.EncodeToString(sum[:]) != tc.wantSHA || len(got) != tc.wantLen {
				t.Fatalf("systemPromptFor(%v) changed: sha256 %x len %d, want sha256 %s len %d — a curator turn must send the v2.25.0 bytes",
					tc.hasSearch, sum, len(got), tc.wantSHA, tc.wantLen)
			}
		})
	}
}

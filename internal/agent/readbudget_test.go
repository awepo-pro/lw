package agent

// readbudget_test.go is 048's frozen block: an ingest turn that has read six
// wiki pages since it last staged a page change has its next read refused by
// the loop, before the registry sees it. The measured failure was a real
// ingest that called wiki.get on all 37 pages and staged none, and 18 eval
// ingest runs that read 14-40 pages before their first write; the prompt line
// and the round-budget nudge did not move it, so the engine enforces the bound.
// Permanent regression tests (D-10C).

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
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// rbRefusalFmt is the frozen refusal, byte for byte, with %s the canonical tool
// name asked for. It is a literal copy of the design's bytes, not built from
// the loop's own constants, so a drift in them fails here.
const rbRefusalFmt = "%s refused: this ingest has read 6 wiki pages since it last staged a change. Stage the pages for the source now (stage.create_page / stage.patch_page) from what you have read; wiki reads are allowed again after a change is staged."

// rbPages is how many wiki pages the fixture is seeded with, rb-page-01 ..
// rb-page-NN. The shipped fixture has four; the longest scripted turn below
// reads thirteen distinct ones.
const rbPages = 16

// rbFixture is newTestLoopFixture over a vault with rbPages extra pages and a
// registry that carries the local-file extractor, so stage.ingest_source can
// really succeed. Every read it scripts is dispatched to this real registry or
// refused by the loop: the registry is the one observer of "was it dispatched".
type rbFixture struct {
	*testLoopFixture
	loop *Loop
	fake *fakeStreamer
	src  string // a local markdown file stage.ingest_source can read
}

func newRBFixture(t *testing.T, rounds [][]llm.Chunk) *rbFixture {
	t.Helper()
	fx := newTestLoopFixture(t)
	root := fx.engine.Vault().Root()
	for i := 1; i <= rbPages; i++ {
		name := fmt.Sprintf("rb-page-%02d", i)
		page := "---\ntitle: " + name + "\ncreated: 2026-10-05\nupdated: 2026-10-05\ntype: concept" +
			"\ntags: [inference]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: high\n---\n\n# " + name + "\n\nBody of " + name + ".\n"
		if err := os.WriteFile(filepath.Join(root, "wiki", "concepts", name+".md"), []byte(page), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := fx.engine.Vault().Reload(); err != nil {
		t.Fatalf("Vault.Reload: %v", err)
	}
	src := filepath.Join(t.TempDir(), "rb-source.md")
	if err := os.WriteFile(src, []byte("# RB Source\n\nA tiny source for the read budget tests.\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	fx.reg = tools.NewRegistry(tools.Deps{
		Vault:   fx.engine.Vault(),
		Index:   fx.engine.Index(),
		Engine:  fx.engine,
		Author:  stage.Author{Kind: "agent", Model: "test-model"},
		Extract: extract.NewFile(),
	})
	fake := &fakeStreamer{rounds: rounds}
	// A cap far above any script here, so neither max_rounds nor 040's round
	// nudge (which edits the last tool result of the final four rounds) touches
	// what a test reads back.
	loop := newLoop(fake, fx.reg, fx.store, fx.engine, LoopConfig{MaxToolRounds: 200})
	return &rbFixture{testLoopFixture: fx, loop: loop, fake: fake, src: src}
}

// send runs one turn under verb and returns its events and error.
func (f *rbFixture) send(t *testing.T, verb string) ([]Event, error) {
	t.Helper()
	ctx := context.Background()
	if verb != "" {
		ctx = trace.WithVerb(ctx, verb)
	}
	out := make(chan Event, 1024)
	err := f.loop.Send(ctx, f.csID, "ingest this source", out)
	return drain(out), err
}

// rbScript builds the scripted rounds: one tool call per round, ids unique
// across the script, and a closing round that answers in prose.
type rbScript struct {
	rounds [][]llm.Chunk
	prefix string // goes before every call id: a second turn's ids must not repeat the first's
}

func (s *rbScript) call(name, args string) *rbScript {
	id := fmt.Sprintf("%scall-%d", s.prefix, len(s.rounds)+1)
	s.rounds = append(s.rounds, []llm.Chunk{toolCallChunk(id, name, args), {Finish: "tool_calls"}})
	return s
}

// get is a wiki_get on seeded page n (1-based): distinct pages, as a crawl is.
func (s *rbScript) get(n int) *rbScript {
	return s.call("wiki_get", fmt.Sprintf(`{"page":"rb-page-%02d"}`, n))
}

// gets is wiki_get on pages from..to inclusive.
func (s *rbScript) gets(from, to int) *rbScript {
	for n := from; n <= to; n++ {
		s.get(n)
	}
	return s
}

func (s *rbScript) stop() [][]llm.Chunk {
	return append(s.rounds, []llm.Chunk{{Text: "done"}, {Finish: "stop"}})
}

// rbOutcomes classifies every tool result of the named tool, in call order:
// "refused" for exactly the frozen refusal (which must also be IsError), "ok"
// for a result the registry produced without IsError, "err" for one it produced
// with IsError. The seeded pages all exist, so a dispatched read is "ok".
func rbOutcomes(res []ToolResEv, name string) []string {
	refusal := fmt.Sprintf(rbRefusalFmt, name)
	var out []string
	for _, r := range res {
		if r.Name != name {
			continue
		}
		switch {
		case r.Content == refusal && r.IsError:
			out = append(out, "refused")
		case r.Content == refusal:
			out = append(out, "refused-without-IsError")
		case r.IsError:
			out = append(out, "err")
		default:
			out = append(out, "ok")
		}
	}
	return out
}

// rbWant is n copies of ok followed by the extra outcomes.
func rbWant(n int, extra ...string) []string {
	out := make([]string, 0, n+len(extra))
	for i := 0; i < n; i++ {
		out = append(out, "ok")
	}
	return append(out, extra...)
}

func rbEqual(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// rbCleanStop asserts the turn ended on its own stop: no ErrorEv anywhere and
// DoneEv{Reason: "stop"} last.
func rbCleanStop(t *testing.T, events []Event, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Send = %v, want a clean turn", err)
	}
	for _, ev := range events {
		if e, ok := ev.(ErrorEv); ok {
			t.Fatalf("turn emitted ErrorEv: %v", e.Err)
		}
	}
	if done, ok := events[len(events)-1].(DoneEv); !ok || done.Reason != "stop" {
		t.Fatalf("last event = %#v, want DoneEv{Reason: stop}", events[len(events)-1])
	}
}

// TestIngestReadBudgetRefusesSeventhRead is the frozen turn: an ingest whose
// model reads seven distinct pages in a row. The registry serves the first six;
// the seventh is answered with the frozen refusal — as an error result, in the
// event, the wire message and the session record — and the turn goes on to its
// own stop.
func TestIngestReadBudgetRefusesSeventhRead(t *testing.T) {
	f := newRBFixture(t, new(rbScript).gets(1, 7).stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := rbOutcomes(res, "wiki.get"); !rbEqual(got, rbWant(6, "refused")) {
		t.Fatalf("wiki.get outcomes = %v, want 6 dispatched then 1 refused", got)
	}
	want := fmt.Sprintf(rbRefusalFmt, "wiki.get")
	last := res[len(res)-1]
	if last.ID != "call-7" || last.Name != "wiki.get" || !last.IsError || last.Content != want {
		t.Fatalf("7th ToolResEv = %+v, want IsError wiki.get carrying the frozen refusal %q", last, want)
	}

	// On the wire the refusal is the tool message answering call-7, the last
	// message of the request that follows it.
	reqs := f.fake.Requests()
	if len(reqs) != 8 {
		t.Fatalf("Stream called %d times, want 8 (the turn must continue past the refusal)", len(reqs))
	}
	msgs := reqs[7].Messages
	wire := msgs[len(msgs)-1]
	if wire.Role != "tool" || wire.ToolCallID != "call-7" || wire.Content != want {
		t.Fatalf("round 8 does not end with the refusal as call-7's tool message: %+v", wire)
	}
	if err := validChat(msgs); err != nil {
		t.Errorf("round 8's request is not a valid chat: %v", err)
	}

	// The session record is written as for any tool error: the refusal, not
	// staged.
	sess, gerr := f.store.Get(f.csID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	var rec *Record
	for i := range sess.Records {
		if sess.Records[i].Role == "tool" && sess.Records[i].Result == want {
			rec = &sess.Records[i]
		}
	}
	if rec == nil || rec.Tool != "wiki.get" || rec.Staged {
		t.Fatalf("no session record holds the refusal as an unstaged wiki.get result: %+v", sess.Records)
	}
}

// TestIngestReadBudgetCountsNeighborsBacklinks: the three page reads share one
// count. Two of each, then a seventh read of any kind is refused under its own
// name — and the count stays where it is, so the next read of every kind is
// refused too.
func TestIngestReadBudgetCountsNeighborsBacklinks(t *testing.T) {
	s := new(rbScript)
	s.get(1).get(2)
	s.call("wiki_neighbors", `{"page":"kv-cache"}`).call("wiki_neighbors", `{"page":"gpt-4"}`)
	s.call("wiki_backlinks", `{"page":"kv-cache"}`).call("wiki_backlinks", `{"page":"gpt-4"}`)
	s.call("wiki_backlinks", `{"page":"flash-attention"}`) // 7th
	s.call("wiki_neighbors", `{"page":"kv-cache"}`).get(3) // still refused: the count did not move
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := rbOutcomes(res, "wiki.get"); !rbEqual(got, []string{"ok", "ok", "refused"}) {
		t.Errorf("wiki.get outcomes = %v, want ok ok refused", got)
	}
	if got := rbOutcomes(res, "wiki.neighbors"); !rbEqual(got, []string{"ok", "ok", "refused"}) {
		t.Errorf("wiki.neighbors outcomes = %v, want ok ok refused", got)
	}
	if got := rbOutcomes(res, "wiki.backlinks"); !rbEqual(got, []string{"ok", "ok", "refused"}) {
		t.Errorf("wiki.backlinks outcomes = %v, want ok ok refused", got)
	}
	// The 7th call, in call order, is the backlinks one: the frozen text names it.
	seventh := res[6]
	if want := fmt.Sprintf(rbRefusalFmt, "wiki.backlinks"); seventh.Name != "wiki.backlinks" || !seventh.IsError || seventh.Content != want {
		t.Errorf("7th ToolResEv = %+v, want IsError wiki.backlinks carrying %q", seventh, want)
	}
}

// TestIngestReadBudgetResetsOnPageChange: a successful stage.create_page
// resets the count — six more reads are dispatched, and the seventh after it
// is refused again.
func TestIngestReadBudgetResetsOnPageChange(t *testing.T) {
	s := new(rbScript).gets(1, 6)
	s.call("stage_create_page", wireCreatePageArgs)
	s.gets(7, 12).get(13)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := rbOutcomes(res, "stage.create_page"); !rbEqual(got, []string{"ok"}) {
		t.Fatalf("stage.create_page outcomes = %v, want one successful call — the reset is vacuous otherwise", got)
	}
	if got := rbOutcomes(res, "wiki.get"); !rbEqual(got, rbWant(12, "refused")) {
		t.Fatalf("wiki.get outcomes = %v, want 12 dispatched (6 before the page change, 6 after) then 1 refused", got)
	}
}

// TestIngestReadBudgetNoResetOnNonChange: three stage calls that stage no page
// — ingest_source (the raw source), open (joins the open changeset) and close
// (a summary) — and a stage.patch_page that FAILED reset nothing: the read
// after them is still refused. The raw.get between ingest_source and close is
// 040's doing (close refuses over an unread source) and is not a wiki read.
func TestIngestReadBudgetNoResetOnNonChange(t *testing.T) {
	f := newRBFixture(t, nil) // the script needs f.src and the staged source path
	s := new(rbScript).gets(1, 6)
	s.call("stage_ingest_source", fmt.Sprintf(`{"uri":%q,"kind":"article"}`, f.src))
	s.call("raw_get", `{"source":"raw/articles/rb-source.md"}`)
	s.call("stage_open", `{"intent":"read budget"}`)
	s.call("stage_close", `{}`)
	s.call("stage_patch_page", `{}`) // valid JSON, no path or op: an IsError result
	s.get(7)
	f.fake.rounds = s.stop()
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	// raw.get names the path stage.ingest_source reported; if the naming rule
	// ever moves, this fails here rather than as an opaque close refusal.
	if got := rbOutcomes(res, "stage.ingest_source"); !rbEqual(got, []string{"ok"}) || !strings.Contains(res[6].Content, "raw/articles/rb-source.md") {
		t.Fatalf("stage.ingest_source did not stage raw/articles/rb-source.md: %+v", res)
	}
	for _, name := range []string{"stage.ingest_source", "raw.get", "stage.open", "stage.close"} {
		if got := rbOutcomes(res, name); !rbEqual(got, []string{"ok"}) {
			t.Fatalf("%s outcomes = %v, want one successful call — the pin is vacuous otherwise: %+v", name, got, res)
		}
	}
	if got := rbOutcomes(res, "stage.patch_page"); !rbEqual(got, []string{"err"}) {
		t.Fatalf("stage.patch_page outcomes = %v, want one IsError result: %+v", got, res)
	}
	if got := rbOutcomes(res, "wiki.get"); !rbEqual(got, rbWant(6, "refused")) {
		t.Fatalf("wiki.get outcomes = %v, want 6 dispatched then the 7th refused: none of those stage calls is a page change", got)
	}
}

// TestIngestReadBudgetSkipsSearchRawOrient: wiki.search, raw.get and
// vault.orient are not page reads. Ten of each, interleaved, spend nothing:
// the seven wiki.get calls after them are six dispatched and one refused.
func TestIngestReadBudgetSkipsSearchRawOrient(t *testing.T) {
	s := new(rbScript)
	for i := 0; i < 10; i++ {
		s.call("wiki_search", `{"q":"cache"}`)
		s.call("raw_get", `{"source":"raw/papers/leviathan-2023.md"}`)
		s.call("vault_orient", `{}`)
	}
	s.gets(1, 7)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	for _, name := range []string{"wiki.search", "raw.get", "vault.orient"} {
		if got := rbOutcomes(res, name); !rbEqual(got, rbWant(10)) {
			t.Errorf("%s outcomes = %v, want 10 dispatched", name, got)
		}
	}
	if got := rbOutcomes(res, "wiki.get"); !rbEqual(got, rbWant(6, "refused")) {
		t.Errorf("wiki.get outcomes = %v, want 6 dispatched then 1 refused: the thirty calls before them did not count", got)
	}
}

// TestIngestReadBudgetOtherVerbsUntouched: the budget is the ingest verb's
// alone. Under ask, query, file, lint and no verb at all, ten wiki.get calls in
// a row are all dispatched and none is refused — lint and a filing turn are
// curator turns that never crawled, and the ask pane's reading is its job.
func TestIngestReadBudgetOtherVerbsUntouched(t *testing.T) {
	for _, verb := range []string{"ask", "query", "file", "lint", ""} {
		t.Run("verb_"+verb, func(t *testing.T) {
			f := newRBFixture(t, new(rbScript).gets(1, 10).stop())
			events, err := f.send(t, verb)
			rbCleanStop(t, events, err)
			if got := rbOutcomes(toolResults(events), "wiki.get"); !rbEqual(got, rbWant(10)) {
				t.Fatalf("verb %q: wiki.get outcomes = %v, want 10 dispatched and none refused", verb, got)
			}
		})
	}
}

// TestIngestReadBudgetRefusalNotBadCall: a refusal is feedback, not a malformed
// call, so it sits outside the two-in-a-row retry budget (the same treatment
// 039 gave an unoffered tool). Six reads, then three refused reads in a row —
// which, were each counted as a bad call, would end the turn on the second —
// then ONE malformed call, which is the turn's first bad call, then a stop.
func TestIngestReadBudgetRefusalNotBadCall(t *testing.T) {
	s := new(rbScript).gets(1, 6).gets(7, 9)
	s.call("wiki_search", `{not json`)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := rbOutcomes(res, "wiki.get"); !rbEqual(got, rbWant(6, "refused", "refused", "refused")) {
		t.Fatalf("wiki.get outcomes = %v, want 6 dispatched then 3 refused", got)
	}
	bad := res[len(res)-1]
	if bad.Name != "wiki.search" || !bad.IsError || !strings.Contains(bad.Content, "malformed tool arguments") {
		t.Fatalf("last result = %+v, want the malformed-arguments error: the single bad call was meant to be the first", bad)
	}
}

// TestIngestReadBudgetRefusalDoesNotResetBadCalls is the other half: a refusal
// between two malformed calls is not a good call, so the second malformed call
// is still the second in a row and ends the turn — exactly as 039's refusal of
// an unoffered tool behaves.
func TestIngestReadBudgetRefusalDoesNotResetBadCalls(t *testing.T) {
	s := new(rbScript).gets(1, 6)
	s.call("wiki_search", `{not json`)
	s.get(7) // refused
	s.call("wiki_search", `{not json`)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	if err == nil || !strings.Contains(err.Error(), "two consecutive unusable calls") {
		t.Fatalf("Send = %v, want the turn to end on the second malformed call (the refusal between them must not reset the budget)", err)
	}
	if got := rbOutcomes(toolResults(events), "wiki.get"); !rbEqual(got, rbWant(6, "refused")) {
		t.Fatalf("wiki.get outcomes = %v, want 6 dispatched then 1 refused", got)
	}
}

// TestIngestReadBudgetRefusalPrecedesParsing: the refusal comes before the
// arguments are looked at, so an over-budget read with malformed JSON is
// refused with the frozen text, not answered with a parse error that would
// count toward the retry budget.
func TestIngestReadBudgetRefusalPrecedesParsing(t *testing.T) {
	s := new(rbScript).gets(1, 6)
	s.call("wiki_get", `{not json`)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)
	if got := rbOutcomes(toolResults(events), "wiki.get"); !rbEqual(got, rbWant(6, "refused")) {
		t.Fatalf("wiki.get outcomes = %v, want 6 dispatched then the malformed 7th refused (not a parse error)", got)
	}
}

// TestIngestReadBudgetPerTurn: the count is the turn's, not the Loop's. One
// Loop, two ingest turns on the same session: turn 1 spends the whole budget,
// and turn 2 starts again from zero — its first six reads are dispatched, its
// seventh refused.
func TestIngestReadBudgetPerTurn(t *testing.T) {
	rounds := new(rbScript).gets(1, 6).stop()
	rounds = append(rounds, (&rbScript{prefix: "t2-"}).gets(1, 7).stop()...)
	f := newRBFixture(t, rounds)

	events1, err := f.send(t, "ingest")
	rbCleanStop(t, events1, err)
	if got := rbOutcomes(toolResults(events1), "wiki.get"); !rbEqual(got, rbWant(6)) {
		t.Fatalf("turn 1 wiki.get outcomes = %v, want 6 dispatched", got)
	}

	events2, err := f.send(t, "ingest")
	rbCleanStop(t, events2, err)
	if got := rbOutcomes(toolResults(events2), "wiki.get"); !rbEqual(got, rbWant(6, "refused")) {
		t.Fatalf("turn 2 wiki.get outcomes = %v, want 6 dispatched then 1 refused: the first turn's count leaked into the Loop", got)
	}
}

// TestIngestReadBudgetRefusalIsLogged: every refusal leaves one "agent read
// budget refusal" line in the file log with the tool and the count, so a
// stalled ingest can be read back for where the model was told to stop.
func TestIngestReadBudgetRefusalIsLogged(t *testing.T) {
	logPath := installFileLog(t)
	s := new(rbScript).gets(1, 6)
	s.call("wiki_backlinks", `{"page":"kv-cache"}`)
	s.get(7)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	log := readLog(t, logPath)
	if n := strings.Count(log, `msg="agent read budget refusal"`); n != 2 {
		t.Errorf("log holds %d refusal lines, want 2:\n%s", n, log)
	}
	for _, want := range []string{"name=wiki.backlinks", "name=wiki.get", "reads=6"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
}

// TestIngestReadBudgetPageChangeSet pins which stage.* calls reset the count:
// every one but the three that stage no page.
func TestIngestReadBudgetPageChangeSet(t *testing.T) {
	for name, want := range map[string]bool{
		"stage.create_page":   true,
		"stage.patch_page":    true,
		"stage.rename_page":   true,
		"stage.merge_pages":   true,
		"stage.split_page":    true,
		"stage.add_link":      true,
		"stage.retract":       true,
		"stage.ingest_source": false,
		"stage.open":          false,
		"stage.close":         false,
		"wiki.get":            false,
		"raw.get":             false,
	} {
		if got := isPageChange(name); got != want {
			t.Errorf("isPageChange(%q) = %v, want %v", name, got, want)
		}
	}
	// And the registry offers exactly the ten stage tools the table lists, so a
	// new one is a decision made here and not a silent default.
	var stageTools []string
	for _, tool := range tools.NewRegistry(tools.Deps{}).List() {
		if strings.HasPrefix(tool.Name, "stage.") {
			stageTools = append(stageTools, tool.Name)
		}
	}
	if len(stageTools) != 10 {
		t.Errorf("the registry has %d stage tools %v, want the 10 this table covers", len(stageTools), stageTools)
	}
}

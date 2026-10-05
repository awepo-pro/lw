package agent

// searchbudget_test.go is 053's frozen block: an ingest turn that has dispatched
// ten wiki.search calls since it last staged a page change has its next search
// refused by the loop, before the registry sees it. The measured failure was
// agent-memory-pair — a vault with no page about agent memory — searching 38
// and 54 times in two of three eval runs (about 48 searches over rounds 8-20 of
// one 051-candidate run, "agent memory" x 4 type filters x limit 1..12) while
// every other ingest run made 2-10; 048 had bounded reading and the crawl moved
// into the one wiki tool with no budget. Permanent regression tests (D-10C).
//
// It reuses 048's fakes (readbudget_test.go): rbFixture, rbScript, rbOutcomes.

import (
	"fmt"
	"strings"
	"testing"
)

// sbRefusal is the frozen refusal, byte for byte. It is a literal copy of the
// design's bytes, not built from the loop's own constants, so a drift in them
// fails here. wiki.search is the only tool it names, so it has no verbs.
const sbRefusal = "wiki.search refused: this ingest has searched the wiki 10 times since it last staged a change. The wiki has nothing closer than what you have found; stage the pages for the source now (stage.create_page / stage.patch_page) — a new page is right when nothing related exists. Searches are allowed again after a change is staged."

// sbRepeatFmt is 051's frozen refusal for an identical repeat, with %d the round
// of the call that ran. TestIngestSearchBudgetRepeatNotCounted needs it to tell
// "refused by 051" from "refused by 053".
const sbRepeatFmt = "wiki.search refused: this exact call already ran in round %d of this turn and its result is still above, unchanged. Use that result, or call with different arguments."

// search is a wiki_search of query n: distinct queries, as a hunt for a page
// that does not exist makes them — and so no call here is 051's repeat. The
// query is the one 048's own tests send, which the fixture answers without
// IsError.
func (s *rbScript) search(n int) *rbScript {
	return s.call("wiki_search", fmt.Sprintf(`{"q":"cache %d"}`, n))
}

// searches is wiki_search of queries from..to inclusive.
func (s *rbScript) searches(from, to int) *rbScript {
	for n := from; n <= to; n++ {
		s.search(n)
	}
	return s
}

// sbOutcomes classifies every wiki.search result of a turn, in call order:
// "refused" for exactly 053's frozen refusal (which must also be IsError),
// "refused-without-IsError" for that text without the flag, "repeat" for any of
// 051's refusals, "budget" for 048's, "ok" for a result the registry produced
// without IsError and "err" for one it produced with IsError.
func sbOutcomes(res []ToolResEv) []string {
	var out []string
	for _, r := range res {
		if r.Name != "wiki.search" {
			continue
		}
		switch {
		case r.Content == sbRefusal && r.IsError:
			out = append(out, "refused")
		case r.Content == sbRefusal:
			out = append(out, "refused-without-IsError")
		case strings.HasPrefix(r.Content, "wiki.search refused: this exact call already ran"):
			out = append(out, "repeat")
		case strings.HasPrefix(r.Content, "wiki.search refused: this ingest has read"):
			out = append(out, "budget")
		case r.IsError:
			out = append(out, "err")
		default:
			out = append(out, "ok")
		}
	}
	return out
}

// sbN is n copies of word.
func sbN(word string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = word
	}
	return out
}

// TestIngestSearchBudgetRefusesEleventh is the frozen turn: an ingest whose
// model searches eleven distinct queries in a row. The registry serves the first
// ten; the eleventh is answered with the frozen refusal — as an error result, in
// the event, the wire message and the session record — and the turn goes on to
// its own stop.
func TestIngestSearchBudgetRefusesEleventh(t *testing.T) {
	f := newRBFixture(t, new(rbScript).searches(1, 11).stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := sbOutcomes(res); !rbEqual(got, rbWant(10, "refused")) {
		t.Fatalf("wiki.search outcomes = %v, want 10 dispatched then 1 refused", got)
	}
	last := res[len(res)-1]
	if last.ID != "call-11" || last.Name != "wiki.search" || !last.IsError || last.Content != sbRefusal {
		t.Fatalf("11th ToolResEv = %+v, want IsError wiki.search carrying the frozen refusal %q", last, sbRefusal)
	}

	// On the wire the refusal is the tool message answering call-11, the last
	// message of the request that follows it, and the turn continued past it.
	reqs := f.fake.Requests()
	if len(reqs) != 12 {
		t.Fatalf("Stream called %d times, want 12 (the turn must continue past the refusal)", len(reqs))
	}
	msgs := reqs[11].Messages
	wire := msgs[len(msgs)-1]
	if wire.Role != "tool" || wire.ToolCallID != "call-11" || wire.Content != sbRefusal {
		t.Fatalf("round 12 does not end with the refusal as call-11's tool message: %+v", wire)
	}
	if err := validChat(msgs); err != nil {
		t.Errorf("round 12's request is not a valid chat: %v", err)
	}

	// The session record is written as for any tool error: the refusal, not
	// staged.
	sess, gerr := f.store.Get(f.csID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	var rec *Record
	for i := range sess.Records {
		if sess.Records[i].Role == "tool" && sess.Records[i].Result == sbRefusal {
			rec = &sess.Records[i]
		}
	}
	if rec == nil || rec.Tool != "wiki.search" || rec.Staged {
		t.Fatalf("no session record holds the refusal as an unstaged wiki.search result: %+v", sess.Records)
	}
}

// TestIngestSearchBudgetResetsOnPageChange: a successful stage.create_page
// resets the count — ten more searches are dispatched, and the eleventh after it
// is refused again.
func TestIngestSearchBudgetResetsOnPageChange(t *testing.T) {
	s := new(rbScript).searches(1, 10)
	s.call("stage_create_page", wireCreatePageArgs)
	s.searches(11, 20).search(21)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := rbOutcomes(res, "stage.create_page"); !rbEqual(got, []string{"ok"}) {
		t.Fatalf("stage.create_page outcomes = %v, want one successful call — the reset is vacuous otherwise", got)
	}
	if got := sbOutcomes(res); !rbEqual(got, rbWant(20, "refused")) {
		t.Fatalf("wiki.search outcomes = %v, want 20 dispatched (10 before the page change, 10 after) then 1 refused", got)
	}
}

// TestIngestSearchBudgetNoResetOnNonChange: three stage calls that stage no page
// — ingest_source (the raw source), open (joins the open changeset) and close
// (a summary) — and a stage.patch_page that FAILED reset nothing: the search
// after them is still refused. The raw.get between ingest_source and close is
// 040's doing (close refuses over an unread source) and is not a search.
func TestIngestSearchBudgetNoResetOnNonChange(t *testing.T) {
	f := newRBFixture(t, nil) // the script needs f.src and the staged source path
	s := new(rbScript).searches(1, 10)
	s.call("stage_ingest_source", fmt.Sprintf(`{"uri":%q,"kind":"article"}`, f.src))
	s.call("raw_get", `{"source":"raw/articles/rb-source.md"}`)
	s.call("stage_open", `{"intent":"search budget"}`)
	s.call("stage_close", `{}`)
	s.call("stage_patch_page", `{}`) // valid JSON, no path or op: an IsError result
	s.search(11)
	f.fake.rounds = s.stop()
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	for _, name := range []string{"stage.ingest_source", "raw.get", "stage.open", "stage.close"} {
		if got := rbOutcomes(res, name); !rbEqual(got, []string{"ok"}) {
			t.Fatalf("%s outcomes = %v, want one successful call — the pin is vacuous otherwise: %+v", name, got, res)
		}
	}
	if got := rbOutcomes(res, "stage.patch_page"); !rbEqual(got, []string{"err"}) {
		t.Fatalf("stage.patch_page outcomes = %v, want one IsError result: %+v", got, res)
	}
	if got := sbOutcomes(res); !rbEqual(got, rbWant(10, "refused")) {
		t.Fatalf("wiki.search outcomes = %v, want 10 dispatched then the 11th refused: none of those stage calls is a page change", got)
	}
}

// TestIngestSearchBudgetIndependentOf048: the two budgets are separate counts.
// Six wiki.get calls (048 at its limit) and ten searches (053 at its limit) are
// all dispatched — neither spends the other's — and then a read gets 048's
// refusal while a search gets 053's.
func TestIngestSearchBudgetIndependentOf048(t *testing.T) {
	s := new(rbScript).gets(1, 6).searches(1, 10)
	s.get(7)
	s.search(11)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := rbOutcomes(res, "wiki.get"); !rbEqual(got, rbWant(6, "refused")) {
		t.Fatalf("wiki.get outcomes = %v, want 6 dispatched then 048's refusal", got)
	}
	if got := sbOutcomes(res); !rbEqual(got, rbWant(10, "refused")) {
		t.Fatalf("wiki.search outcomes = %v, want 10 dispatched then 053's refusal", got)
	}
	// Each refusal carries its own text, not the other's.
	for _, r := range res {
		switch r.Name {
		case "wiki.get":
			if r.IsError && r.Content != fmt.Sprintf(rbRefusalFmt, "wiki.get") {
				t.Errorf("wiki.get refusal = %q, want 048's frozen text", r.Content)
			}
		case "wiki.search":
			if r.IsError && r.Content != sbRefusal {
				t.Errorf("wiki.search refusal = %q, want 053's frozen text", r.Content)
			}
		}
	}
}

// TestIngestSearchBudgetRepeatNotCounted: 051 answers an identical repeat before
// 053 sees it, so the repeat spends nothing. Nine distinct searches, a repeat of
// the ninth (refused with 051's text, naming round 9), then a tenth distinct
// search — dispatched, since the count is nine — and an eleventh, which is 053's.
func TestIngestSearchBudgetRepeatNotCounted(t *testing.T) {
	s := new(rbScript).searches(1, 9)
	s.search(9) // an identical repeat of call-9
	s.search(10)
	s.search(11)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := sbOutcomes(res); !rbEqual(got, rbWant(9, "repeat", "ok", "refused")) {
		t.Fatalf("wiki.search outcomes = %v, want 9 dispatched, 051's repeat refusal, the 10th dispatched, then 053's refusal", got)
	}
	if want := fmt.Sprintf(sbRepeatFmt, 9); res[9].Content != want || !res[9].IsError {
		t.Fatalf("10th ToolResEv = %+v, want IsError carrying 051's text %q", res[9], want)
	}
}

// TestIngestSearchBudgetRepeatAtLimit pins the order the other way: with the
// budget spent, an identical repeat of an answered search is still 051's to
// refuse — it is answered with "that result is above", the true reason, and not
// with 053's "stop searching".
func TestIngestSearchBudgetRepeatAtLimit(t *testing.T) {
	s := new(rbScript).searches(1, 10)
	s.search(10) // an identical repeat of call-10, with the budget at its limit
	s.search(11) // a new query: 053's
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := sbOutcomes(res); !rbEqual(got, rbWant(10, "repeat", "refused")) {
		t.Fatalf("wiki.search outcomes = %v, want 10 dispatched, 051's repeat refusal, then 053's", got)
	}
	if want := fmt.Sprintf(sbRepeatFmt, 10); res[10].Content != want {
		t.Fatalf("11th ToolResEv = %q, want 051's text %q: 051 runs before 053", res[10].Content, want)
	}
}

// TestIngestSearchBudgetOtherVerbsUntouched: the budget is the ingest verb's
// alone. Under ask, query, file, lint and no verb at all, fifteen distinct
// wiki.search calls in a row are all dispatched and none is refused — the ask
// pane's searching is its job, and a filing or lint turn never crawled.
func TestIngestSearchBudgetOtherVerbsUntouched(t *testing.T) {
	for _, verb := range []string{"ask", "query", "file", "lint", ""} {
		t.Run("verb_"+verb, func(t *testing.T) {
			f := newRBFixture(t, new(rbScript).searches(1, 15).stop())
			events, err := f.send(t, verb)
			rbCleanStop(t, events, err)
			if got := sbOutcomes(toolResults(events)); !rbEqual(got, rbWant(15)) {
				t.Fatalf("verb %q: wiki.search outcomes = %v, want 15 dispatched and none refused", verb, got)
			}
		})
	}
}

// TestIngestSearchBudgetNotBadCall: a refusal is feedback, not a malformed call,
// so it sits outside the two-in-a-row retry budget (the same treatment 039 gave
// an unoffered tool and 048 a read). Ten searches, then three refused searches in
// a row — which, were each counted as a bad call, would end the turn on the
// second — then ONE malformed call, which is the turn's first bad call, then a
// stop. The malformed call is a wiki.get: a malformed wiki.search would itself be
// refused, before its arguments are looked at.
func TestIngestSearchBudgetNotBadCall(t *testing.T) {
	s := new(rbScript).searches(1, 10).searches(11, 13)
	s.call("wiki_get", `{not json`)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := sbOutcomes(res); !rbEqual(got, rbWant(10, "refused", "refused", "refused")) {
		t.Fatalf("wiki.search outcomes = %v, want 10 dispatched then 3 refused", got)
	}
	bad := res[len(res)-1]
	if bad.Name != "wiki.get" || !bad.IsError || !strings.Contains(bad.Content, "malformed tool arguments") {
		t.Fatalf("last result = %+v, want the malformed-arguments error: the single bad call was meant to be the first", bad)
	}
}

// TestIngestSearchBudgetPerTurn: the count is the turn's, not the Loop's. One
// Loop, two ingest turns on the same session: turn 1 spends the whole budget,
// and turn 2 starts again from zero — its first ten searches are dispatched, its
// eleventh refused.
func TestIngestSearchBudgetPerTurn(t *testing.T) {
	rounds := new(rbScript).searches(1, 10).stop()
	rounds = append(rounds, (&rbScript{prefix: "t2-"}).searches(1, 11).stop()...)
	f := newRBFixture(t, rounds)

	events1, err := f.send(t, "ingest")
	rbCleanStop(t, events1, err)
	if got := sbOutcomes(toolResults(events1)); !rbEqual(got, rbWant(10)) {
		t.Fatalf("turn 1 wiki.search outcomes = %v, want 10 dispatched", got)
	}

	events2, err := f.send(t, "ingest")
	rbCleanStop(t, events2, err)
	if got := sbOutcomes(toolResults(events2)); !rbEqual(got, rbWant(10, "refused")) {
		t.Fatalf("turn 2 wiki.search outcomes = %v, want 10 dispatched then 1 refused: the first turn's count leaked into the Loop", got)
	}
}

// TestIngestSearchBudgetRefusalPrecedesParsing: the refusal comes before the
// arguments are looked at, so an over-budget search with malformed JSON is
// refused with the frozen text, not answered with a parse error that would count
// toward the retry budget.
func TestIngestSearchBudgetRefusalPrecedesParsing(t *testing.T) {
	s := new(rbScript).searches(1, 10)
	s.call("wiki_search", `{not json`)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)
	if got := sbOutcomes(toolResults(events)); !rbEqual(got, rbWant(10, "refused")) {
		t.Fatalf("wiki.search outcomes = %v, want 10 dispatched then the malformed 11th refused (not a parse error)", got)
	}
}

// TestIngestSearchBudgetCountsFailedSearch: it is the dispatched call that is
// counted, not what it returned — as with 048's reads. Ten searches that each
// came back IsError (no q) spend the budget, and the eleventh is refused.
func TestIngestSearchBudgetCountsFailedSearch(t *testing.T) {
	s := new(rbScript)
	for i := 0; i < 11; i++ {
		s.call("wiki_search", `{}`)
	}
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)
	if got := sbOutcomes(toolResults(events)); !rbEqual(got, append(sbN("err", 10), "refused")) {
		t.Fatalf("wiki.search outcomes = %v, want 10 IsError results then 1 refused", got)
	}
}

// TestIngestSearchBudgetRefusalIsLogged: every refusal leaves one "agent search
// budget refusal" line in the file log with the count, so a stalled ingest can
// be read back for where the model was told to stop.
func TestIngestSearchBudgetRefusalIsLogged(t *testing.T) {
	logPath := installFileLog(t)
	f := newRBFixture(t, new(rbScript).searches(1, 12).stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	log := readLog(t, logPath)
	if n := strings.Count(log, `msg="agent search budget refusal"`); n != 2 {
		t.Errorf("log holds %d refusal lines, want 2:\n%s", n, log)
	}
	if !strings.Contains(log, "searches=10") {
		t.Errorf("log missing searches=10:\n%s", log)
	}
}

// TestIngestSearchBudgetOnlyWikiSearch: the budget counts and refuses
// wiki.search and nothing else. Ten searches, then each tool a crawl or a
// writer might call is dispatched: a wiki read (048's own count is far from its
// limit), the orient digest and a raw read.
func TestIngestSearchBudgetOnlyWikiSearch(t *testing.T) {
	f := newRBFixture(t, nil)
	s := new(rbScript).searches(1, 10)
	s.get(1)
	s.call("vault_orient", `{}`)
	s.call("raw_get", `{"source":"raw/articles/kv-cache-explained.md"}`)
	f.fake.rounds = s.stop()
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	for _, name := range []string{"wiki.get", "vault.orient", "raw.get"} {
		if got := rbOutcomes(res, name); !rbEqual(got, rbWant(1)) {
			t.Errorf("%s outcomes = %v, want 1 dispatched: the search budget is wiki.search's alone", name, got)
		}
	}
}

// TestIngestSearchBudgetUnit pins the budget's own methods: nil is the budget of
// every turn that has none, only wiki.search spends and is refused, and a page
// change resets the count. It needs no Loop.
func TestIngestSearchBudgetUnit(t *testing.T) {
	var none *readBudget
	none.noteSearch("wiki.search")
	none.notePageChange("stage.create_page")
	if text, refused := none.searchRefusal("wiki.search"); refused || text != "" {
		t.Errorf("nil budget refused a search: %q", text)
	}

	if b := newReadBudget(planFor("ask")); b != nil {
		t.Errorf("newReadBudget(ask) = %+v, want nil", b)
	}
	b := newReadBudget(planFor("ingest"))
	for i := 0; i < ingestSearchBudget; i++ {
		if _, refused := b.searchRefusal("wiki.search"); refused {
			t.Fatalf("search %d refused, want the first %d allowed", i+1, ingestSearchBudget)
		}
		b.noteSearch("wiki.search")
	}
	if ingestSearchBudget != 10 {
		t.Errorf("ingestSearchBudget = %d, want 10", ingestSearchBudget)
	}
	text, refused := b.searchRefusal("wiki.search")
	if !refused || text != sbRefusal {
		t.Errorf("search %d = (%q, %v), want the frozen refusal", ingestSearchBudget+1, text, refused)
	}
	for _, name := range []string{"wiki.get", "web.search", "raw.get", "vault.orient", "stage.create_page"} {
		if _, refused := b.searchRefusal(name); refused {
			t.Errorf("searchRefusal(%q) refused: only wiki.search is a search", name)
		}
		b.noteSearch(name)
	}
	if b.searches != ingestSearchBudget {
		t.Errorf("searches = %d after non-search calls, want %d", b.searches, ingestSearchBudget)
	}
	b.notePageChange("stage.ingest_source")
	if b.searches != ingestSearchBudget {
		t.Errorf("stage.ingest_source reset the search count to %d", b.searches)
	}
	b.notePageChange("stage.create_page")
	if b.searches != 0 {
		t.Errorf("searches = %d after a page change, want 0", b.searches)
	}
}

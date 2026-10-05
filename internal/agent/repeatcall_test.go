package agent

// repeatcall_test.go is 051's frozen block: a read call whose identical twin
// already ran in this turn, whose result is still in the model's context and
// whose answer cannot have changed, is refused by the loop before the registry
// sees it. The measured failure was an eval ingest that sent the identical
// wiki.search twice per round for rounds 4-20 and staged only after the round
// nudge (74 exact-duplicate calls in 845), the crawl 048's read budget had
// pushed into wiki.search, which is not a budgeted read. Prompt text does not
// bound this model; a refusal does. Permanent regression tests (D-10C).

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
)

// rcRefusalFmt is the frozen refusal, byte for byte: %s the canonical tool name,
// %d the round of the earlier call. A literal copy of the design's bytes, not
// built from the loop's own constant, so a drift in it fails here.
const rcRefusalFmt = "%s refused: this exact call already ran in round %d of this turn and its result is still above, unchanged. Use that result, or call with different arguments."

// rcOutcome classifies one tool result: "repeat@N" for exactly the frozen
// refusal naming round N (which must also be IsError), "budget" for 048's frozen
// refusal, "ok" for a result the registry produced without IsError and "err"
// for one it produced with IsError. A refusal that is not IsError, or not
// byte-exact, is reported as a defect instead of being folded into "ok"/"err",
// so a wrong refusal can never pass for a dispatch.
func rcOutcome(r ToolResEv) string {
	prefix := r.Name + " refused: this exact call already ran in round "
	if strings.HasPrefix(r.Content, prefix) {
		rest := strings.TrimPrefix(r.Content, prefix)
		digits := rest[:len(rest)-len(strings.TrimLeft(rest, "0123456789"))]
		n, err := strconv.Atoi(digits)
		if err != nil || r.Content != fmt.Sprintf(rcRefusalFmt, r.Name, n) {
			return "malformed-repeat-refusal: " + r.Content
		}
		if !r.IsError {
			return "repeat-without-IsError"
		}
		return "repeat@" + digits
	}
	switch {
	case r.Content == fmt.Sprintf(rbRefusalFmt, r.Name) && r.IsError:
		return "budget"
	case r.IsError:
		return "err"
	}
	return "ok"
}

// rcOutcomes classifies every result of the named tool, in call order.
func rcOutcomes(res []ToolResEv, name string) []string {
	var out []string
	for _, r := range res {
		if r.Name == name {
			out = append(out, rcOutcome(r))
		}
	}
	return out
}

// rcWant fails the test unless the named tool's outcomes are exactly want.
func rcWant(t *testing.T, events []Event, name string, want ...string) {
	t.Helper()
	if got := rcOutcomes(toolResults(events), name); !rbEqual(got, want) {
		t.Fatalf("%s outcomes = %v, want %v", name, got, want)
	}
}

// rcRepeat is n copies of repeat@round, for a want list.
func rcRepeat(round, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("repeat@%d", round)
	}
	return out
}

// rcSearch is the scripted wiki_search every test repeats.
const rcSearch = `{"q":"x"}`

// TestRepeatCallRefused is the frozen turn: round 1 searches, round 2 sends the
// identical call, round 3 stops. The registry serves the first; the second is
// answered with the frozen refusal naming wiki.search and round 1 — as an error
// result in the event, the wire message and the session record — and the turn
// goes on to its own stop.
func TestRepeatCallRefused(t *testing.T) {
	f := newRBFixture(t, new(rbScript).call("wiki_search", rcSearch).call("wiki_search", rcSearch).stop())
	events, err := f.send(t, "")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := rcOutcomes(res, "wiki.search"); !rbEqual(got, []string{"ok", "repeat@1"}) {
		t.Fatalf("wiki.search outcomes = %v, want 1 dispatched then 1 refused", got)
	}
	if res[0].Content == "" {
		t.Fatalf("the first call's result is empty: %+v — the registry was not reached", res[0])
	}
	want := fmt.Sprintf(rcRefusalFmt, "wiki.search", 1)
	if res[1].ID != "call-2" || res[1].Name != "wiki.search" || !res[1].IsError || res[1].Content != want {
		t.Fatalf("2nd ToolResEv = %+v, want IsError wiki.search carrying the frozen refusal %q", res[1], want)
	}
	if done := events[len(events)-1].(DoneEv); done.Rounds != 3 {
		t.Fatalf("DoneEv = %+v, want the stop to come in round 3: the refusal must not end the turn", done)
	}

	// On the wire the refusal is the tool message answering call-2, the last
	// message of the request that follows it.
	reqs := f.fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("Stream called %d times, want 3", len(reqs))
	}
	msgs := reqs[2].Messages
	wire := msgs[len(msgs)-1]
	if wire.Role != "tool" || wire.ToolCallID != "call-2" || wire.Content != want {
		t.Fatalf("round 3 does not end with the refusal as call-2's tool message: %+v", wire)
	}
	if err := validChat(msgs); err != nil {
		t.Errorf("round 3's request is not a valid chat: %v", err)
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
	if rec == nil || rec.Tool != "wiki.search" || rec.Staged {
		t.Fatalf("no session record holds the refusal as an unstaged wiki.search result: %+v", sess.Records)
	}
}

// TestRepeatCallWhitespaceSameSig: the identity is A-004-2's callSignature, so
// the spacing a model varies between rounds does not make the same call look
// like two.
func TestRepeatCallWhitespaceSameSig(t *testing.T) {
	f := newRBFixture(t, new(rbScript).call("wiki_search", `{"q":"x"}`).call("wiki_search", `{ "q" : "x" }`).stop())
	events, err := f.send(t, "")
	rbCleanStop(t, events, err)
	rcWant(t, events, "wiki.search", "ok", "repeat@1")
}

// TestRepeatCallEmptyArgsSameSig: a call with no arguments is {} to the
// dispatcher (backbone §9 item 7), so a model that spells it "" once and {} the
// next time has still sent the same call twice.
func TestRepeatCallEmptyArgsSameSig(t *testing.T) {
	f := newRBFixture(t, new(rbScript).call("vault_orient", ``).call("vault_orient", `{}`).call("vault_orient", ` `).stop())
	events, err := f.send(t, "")
	rbCleanStop(t, events, err)
	rcWant(t, events, "vault.orient", "ok", "repeat@1", "repeat@1")
}

// TestRepeatCallDifferentArgsAllowed: another query is another call.
func TestRepeatCallDifferentArgsAllowed(t *testing.T) {
	f := newRBFixture(t, new(rbScript).call("wiki_search", `{"q":"x"}`).call("wiki_search", `{"q":"y"}`).stop())
	events, err := f.send(t, "")
	rbCleanStop(t, events, err)
	rcWant(t, events, "wiki.search", "ok", "ok")
}

// TestRepeatCallSameRoundDuplicate: two identical calls in one round — the
// measured failure sent them twice per round — and the second is refused with
// that round's own number. Round 2, not round 1, so a constant 1 cannot pass.
func TestRepeatCallSameRoundDuplicate(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "wiki_get", `{"page":"rb-page-01"}`), {Finish: "tool_calls"}},
		{
			toolCallChunk("call-2a", "wiki_search", rcSearch),
			toolCallChunk("call-2b", "wiki_search", rcSearch),
			{Finish: "tool_calls"},
		},
		{{Text: "done"}, {Finish: "stop"}},
	}
	f := newRBFixture(t, rounds)
	events, err := f.send(t, "")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := rcOutcomes(res, "wiki.search"); !rbEqual(got, []string{"ok", "repeat@2"}) {
		t.Fatalf("wiki.search outcomes = %v, want the first dispatched and the second refused naming round 2", got)
	}
	want := fmt.Sprintf(rcRefusalFmt, "wiki.search", 2)
	if second := res[2]; second.ID != "call-2b" || second.Content != want || !second.IsError {
		t.Fatalf("call-2b's result = %+v, want the frozen refusal naming round 2: %q", second, want)
	}
	// One assistant message carries both calls and both answers follow it, in
	// call order: the refusal must not disturb the round's wire shape.
	msgs := f.fake.Requests()[2].Messages
	if err := validChat(msgs); err != nil {
		t.Errorf("round 3's request is not a valid chat: %v", err)
	}
	n := len(msgs)
	if msgs[n-2].ToolCallID != "call-2a" || msgs[n-1].ToolCallID != "call-2b" || msgs[n-1].Content != want {
		t.Errorf("round 2's answers are not call-2a then the refusal for call-2b: %+v", msgs[n-2:])
	}
}

// TestRepeatCallErrorRetryAllowed: a call that came back IsError left the model
// with nothing worth keeping, so the identical call is a retry and is
// dispatched — however many times the model makes it. wiki.get on a page that
// does not exist is the registry's own IsError.
func TestRepeatCallErrorRetryAllowed(t *testing.T) {
	missing := `{"page":"no-such-page"}`
	f := newRBFixture(t, new(rbScript).call("wiki_get", missing).call("wiki_get", missing).call("wiki_get", missing).stop())
	events, err := f.send(t, "")
	rbCleanStop(t, events, err)
	rcWant(t, events, "wiki.get", "err", "err", "err")
}

// TestRepeatCallAfterStageAllowed: a successful stage call that changes the
// changeset makes every earlier answer eligible again — a search or a page read
// may return something different now — but stage.open and stage.close change
// nothing, and a stage call that failed changed nothing either.
func TestRepeatCallAfterStageAllowed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage func(s *rbScript, f *rbFixture)
		tool  string   // the stage tool the case runs, whose own result must be ok (or err for the failed one)
		want  []string // wiki.search outcomes
		stg   string   // expected outcome of the stage call
	}{
		{"create_page", func(s *rbScript, f *rbFixture) { s.call("stage_create_page", wireCreatePageArgs) }, "stage.create_page", []string{"ok", "ok"}, "ok"},
		{"ingest_source", func(s *rbScript, f *rbFixture) {
			s.call("stage_ingest_source", fmt.Sprintf(`{"uri":%q,"kind":"article"}`, f.src))
		}, "stage.ingest_source", []string{"ok", "ok"}, "ok"},
		{"close", func(s *rbScript, f *rbFixture) { s.call("stage_close", `{}`) }, "stage.close", []string{"ok", "repeat@1"}, "ok"},
		{"open", func(s *rbScript, f *rbFixture) { s.call("stage_open", `{"intent":"repeat call"}`) }, "stage.open", []string{"ok", "repeat@1"}, "ok"},
		{"failed_patch", func(s *rbScript, f *rbFixture) { s.call("stage_patch_page", `{}`) }, "stage.patch_page", []string{"ok", "repeat@1"}, "err"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRBFixture(t, nil) // the script needs f.src
			s := new(rbScript).call("wiki_search", rcSearch)
			tc.stage(s, f)
			s.call("wiki_search", rcSearch)
			f.fake.rounds = s.stop()
			events, err := f.send(t, "")
			rbCleanStop(t, events, err)

			rcWant(t, events, tc.tool, tc.stg) // the stage call really ran, or the case is vacuous
			rcWant(t, events, "wiki.search", tc.want...)
		})
	}

	// After a state change the call that was dispatched again is the one on
	// record: a further identical call is a repeat of THAT one, round 3.
	t.Run("repeat_after_the_change_names_the_new_round", func(t *testing.T) {
		s := new(rbScript).call("wiki_search", rcSearch).call("stage_create_page", wireCreatePageArgs).
			call("wiki_search", rcSearch).call("wiki_search", rcSearch)
		f := newRBFixture(t, s.stop())
		events, err := f.send(t, "")
		rbCleanStop(t, events, err)
		rcWant(t, events, "wiki.search", "ok", "ok", "repeat@3")
	})
}

// TestRepeatCallAfterElisionAllowed: a result boundContext elided is no longer
// above, so the model may read it again (A-004-2 requires that it can). The
// control run at a huge budget has nothing elided and refuses the third call;
// at a budget that fits about one 20000-byte page, round 1's result is elided
// when round 3 starts and the same script's third call is dispatched.
func TestRepeatCallAfterElisionAllowed(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-a1", "wiki_get", `{"page":"a"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-b1", "wiki_get", `{"page":"b"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-a2", "wiki_get", `{"page":"a"}`), {Finish: "tool_calls"}},
		{{Text: "I have both pages now"}, {Finish: "stop"}},
	}

	// Control: no pressure, nothing elided, so a2 re-sends what is still above.
	ctlFake, ctlEvents := runRereadTurn(t, rounds, LoopConfig{ContextTokens: 1000000})
	assertStopTurn(t, ctlEvents, 4)
	rcWant(t, ctlEvents, "wiki.get", "ok", "ok", "repeat@1")
	base := wireEstimate(ctlFake.Requests()[0].Messages)

	fake, events := runRereadTurn(t, rounds, LoopConfig{ContextTokens: base + 5500})
	assertStopTurn(t, events, 4)
	reqs := fake.Requests()
	if len(reqs) != 4 {
		t.Fatalf("Stream called %d times, want 4", len(reqs))
	}
	first := resultByCall(t, events, "call-a1")
	if msg := toolMsgByID(t, reqs[2].Messages, "call-a1"); msg.Content != probePlaceholder("wiki.get", len(first)) {
		t.Fatalf("call-a1's result was not elided when round 3 began — sizing broke, test vacuous: %q", msg.Content)
	}
	rcWant(t, events, "wiki.get", "ok", "ok", "ok")
	if again := resultByCall(t, events, "call-a2"); again != first {
		t.Errorf("the re-read of page a returned %q, want the page again", again)
	}
}

// TestRepeatCallRereadAfterElisionThenRefused: the re-read that elision
// allowed is itself above, intact (A-004-2 pins it), so a further identical
// call is refused again — naming the round of the re-read, not the round whose
// result was elided.
func TestRepeatCallRereadAfterElisionThenRefused(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-a1", "wiki_get", `{"page":"a"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-b1", "wiki_get", `{"page":"b"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-a2", "wiki_get", `{"page":"a"}`), {Finish: "tool_calls"}},
		{toolCallChunk("call-a3", "wiki_get", `{"page":"a"}`), {Finish: "tool_calls"}},
		{{Text: "I have both pages now"}, {Finish: "stop"}},
	}
	ctlFake, _ := runRereadTurn(t, rounds, LoopConfig{ContextTokens: 1000000})
	base := wireEstimate(ctlFake.Requests()[0].Messages)

	fake, events := runRereadTurn(t, rounds, LoopConfig{ContextTokens: base + 5500})
	assertStopTurn(t, events, 5)
	first := resultByCall(t, events, "call-a1")
	if msg := toolMsgByID(t, fake.Requests()[2].Messages, "call-a1"); msg.Content != probePlaceholder("wiki.get", len(first)) {
		t.Fatalf("call-a1's result was not elided when round 3 began — sizing broke, test vacuous: %q", msg.Content)
	}
	rcWant(t, events, "wiki.get", "ok", "ok", "ok", "repeat@3")
}

// TestRepeatCallStageUntouched: the guard is for reads. Two identical
// stage_add_link calls both reach the registry — whatever the second one
// answers — and so do two failing stage_patch_page and two stage_close calls.
func TestRepeatCallStageUntouched(t *testing.T) {
	link := `{"from":"wiki/concepts/kv-cache.md","to":"wiki/entities/gpt-4.md"}`
	for _, tc := range []struct{ wire, name, args string }{
		{"stage_add_link", "stage.add_link", link},
		{"stage_patch_page", "stage.patch_page", `{}`},
		{"stage_close", "stage.close", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRBFixture(t, new(rbScript).call(tc.wire, tc.args).call(tc.wire, tc.args).stop())
			events, err := f.send(t, "")
			rbCleanStop(t, events, err)
			got := rcOutcomes(toolResults(events), tc.name)
			if len(got) != 2 {
				t.Fatalf("%s outcomes = %v, want 2 results", tc.name, got)
			}
			for _, o := range got {
				if strings.HasPrefix(o, "repeat") || strings.HasPrefix(o, "malformed") {
					t.Fatalf("%s outcomes = %v, want both dispatched: a stage tool is never refused as a repeat", tc.name, got)
				}
			}
		})
	}
}

// TestRepeatCallNotBadCall: a refusal is feedback, not a malformed call, so it
// sits outside the two-in-a-row retry budget (the same treatment 039 and 048
// gave theirs). One search, then three refused repeats in a row — which, were
// each counted as a bad call, would end the turn on the second — then ONE
// malformed call, the turn's first bad call, then a stop.
func TestRepeatCallNotBadCall(t *testing.T) {
	s := new(rbScript).call("wiki_search", rcSearch)
	for i := 0; i < 3; i++ {
		s.call("wiki_search", rcSearch)
	}
	s.call("wiki_search", `{not json`)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "")
	rbCleanStop(t, events, err)

	res := toolResults(events)
	if got := rcOutcomes(res, "wiki.search"); !rbEqual(got, append(append([]string{"ok"}, rcRepeat(1, 3)...), "err")) {
		t.Fatalf("wiki.search outcomes = %v, want 1 dispatched, 3 refused, then the malformed call", got)
	}
	bad := res[len(res)-1]
	if !bad.IsError || !strings.Contains(bad.Content, "malformed tool arguments") {
		t.Fatalf("last result = %+v, want the malformed-arguments error: the single bad call was meant to be the first", bad)
	}
	if done := events[len(events)-1].(DoneEv); done.Rounds != 6 {
		t.Fatalf("DoneEv = %+v, want a stop in round 6", done)
	}
}

// TestRepeatCallRefusalDoesNotResetBadCalls is the other half: a refusal
// between two malformed calls is not a good call, so the second malformed call
// is still the second in a row and ends the turn.
func TestRepeatCallRefusalDoesNotResetBadCalls(t *testing.T) {
	s := new(rbScript).call("wiki_search", rcSearch)
	s.call("wiki_search", `{not json`)
	s.call("wiki_search", rcSearch) // refused
	s.call("wiki_search", `{not json`)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "")
	if err == nil || !strings.Contains(err.Error(), "two consecutive unusable calls") {
		t.Fatalf("Send = %v, want the turn to end on the second malformed call (the refusal between them must not reset the budget)", err)
	}
	if got := rcOutcomes(toolResults(events), "wiki.search"); !rbEqual(got, []string{"ok", "err", "repeat@1", "err"}) {
		t.Fatalf("wiki.search outcomes = %v, want ok, err, repeat@1, err", got)
	}
}

// TestRepeatCallRefusalPrecedesParsing: the guard runs before the arguments are
// parsed, as 048's does — but a malformed call can never match an earlier
// success, so it is answered with its parse error and counts as a bad call.
func TestRepeatCallRefusalPrecedesParsing(t *testing.T) {
	s := new(rbScript).call("wiki_search", rcSearch).call("wiki_search", `{"q":"x"`)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "")
	rbCleanStop(t, events, err)
	res := toolResults(events)
	if got := rcOutcomes(res, "wiki.search"); !rbEqual(got, []string{"ok", "err"}) {
		t.Fatalf("wiki.search outcomes = %v, want ok then the malformed call's own error", got)
	}
	if !strings.Contains(res[1].Content, "malformed tool arguments") {
		t.Fatalf("2nd result = %q, want the parse error", res[1].Content)
	}
}

// TestRepeatCallNotARead048: a refused repeat spends none of 048's read budget.
// An ingest turn reads six distinct pages — the whole budget — repeats the
// sixth (refused by THIS guard, which runs before 048's, with its own text) and
// then asks for a seventh distinct page, which 048 refuses with its text.
func TestRepeatCallNotARead048(t *testing.T) {
	s := new(rbScript).gets(1, 6).get(6).get(7)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)
	rcWant(t, events, "wiki.get", "ok", "ok", "ok", "ok", "ok", "ok", "repeat@6", "budget")

	res := toolResults(events)
	if want := fmt.Sprintf(rbRefusalFmt, "wiki.get"); res[7].Content != want {
		t.Fatalf("7th distinct read answered %q, want 048's frozen refusal %q", res[7].Content, want)
	}

	// The converse that makes the count visible: five reads, a refused repeat,
	// then a SIXTH distinct page — dispatched, because the repeat was not the
	// sixth read — and only the seventh refused.
	t.Run("sixth_distinct_read_after_a_repeat_is_dispatched", func(t *testing.T) {
		s := new(rbScript).gets(1, 5).get(5).get(6).get(7)
		f := newRBFixture(t, s.stop())
		events, err := f.send(t, "ingest")
		rbCleanStop(t, events, err)
		rcWant(t, events, "wiki.get", "ok", "ok", "ok", "ok", "ok", "repeat@5", "ok", "budget")
	})
}

// TestRepeatCallEveryVerb: the guard applies to every verb, ask and query
// (which are offered only the read tools) and ingest, lint, file and no verb at
// all (which are offered everything).
func TestRepeatCallEveryVerb(t *testing.T) {
	for _, verb := range []string{"ask", "query", "ingest", "lint", "file", ""} {
		t.Run("verb_"+verb, func(t *testing.T) {
			f := newRBFixture(t, new(rbScript).call("wiki_search", rcSearch).call("wiki_search", rcSearch).stop())
			events, err := f.send(t, verb)
			rbCleanStop(t, events, err)
			rcWant(t, events, "wiki.search", "ok", "repeat@1")
		})
	}
}

// TestRepeatCallPerTurn: the record of what ran is the turn's, not the Loop's.
// One Loop, two Sends on one session: turn 2's first call is identical to turn
// 1's, and is dispatched — turn 1's result is history by then, not "above".
func TestRepeatCallPerTurn(t *testing.T) {
	rounds := new(rbScript).call("wiki_search", rcSearch).stop()
	rounds = append(rounds, (&rbScript{prefix: "t2-"}).call("wiki_search", rcSearch).call("wiki_search", rcSearch).stop()...)
	f := newRBFixture(t, rounds)

	events1, err := f.send(t, "")
	rbCleanStop(t, events1, err)
	rcWant(t, events1, "wiki.search", "ok")

	events2, err := f.send(t, "")
	rbCleanStop(t, events2, err)
	rcWant(t, events2, "wiki.search", "ok", "repeat@1")
}

// TestRepeatCallEveryGuardedTool: each of the guarded tools is refused on an
// identical repeat, and wiki.lint, which is not on the list, is not. Every "ok"
// is a real registry result, so a refusal cannot pass for a dispatch.
func TestRepeatCallEveryGuardedTool(t *testing.T) {
	for _, tc := range []struct {
		wire, name, args string
		guarded          bool
	}{
		{"wiki_search", "wiki.search", `{"q":"cache"}`, true},
		{"wiki_get", "wiki.get", `{"page":"kv-cache"}`, true},
		{"wiki_neighbors", "wiki.neighbors", `{"page":"kv-cache"}`, true},
		{"wiki_backlinks", "wiki.backlinks", `{"page":"kv-cache"}`, true},
		{"raw_get", "raw.get", `{"source":"raw/papers/leviathan-2023.md"}`, true},
		{"raw_list", "raw.list", `{}`, true},
		{"vault_orient", "vault.orient", `{}`, true},
		{"wiki_lint", "wiki.lint", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRBFixture(t, new(rbScript).call(tc.wire, tc.args).call(tc.wire, tc.args).stop())
			events, err := f.send(t, "")
			rbCleanStop(t, events, err)
			want := []string{"ok", "repeat@1"}
			if !tc.guarded {
				want = []string{"ok", "ok"}
			}
			rcWant(t, events, tc.name, want...)
		})
	}
}

// TestRepeatCallRefusalNamesTheRoundThatRan: the round in the text is the round
// of the call that actually ran, however many refusals have come since — a
// refused repeat is not an earlier call, and does not become the one on record.
func TestRepeatCallRefusalNamesTheRoundThatRan(t *testing.T) {
	s := new(rbScript).call("wiki_get", `{"page":"rb-page-01"}`).call("wiki_search", rcSearch)
	s.call("wiki_search", rcSearch).call("wiki_search", rcSearch)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "")
	rbCleanStop(t, events, err)
	rcWant(t, events, "wiki.search", "ok", "repeat@2", "repeat@2")
}

// TestRepeatCallRefusalIsLogged: every refusal leaves one "agent repeat call
// refusal" line in the file log with the tool and the round of the earlier
// call, so a stalled turn can be read back for where the model was told to
// stop repeating.
func TestRepeatCallRefusalIsLogged(t *testing.T) {
	logPath := installFileLog(t)
	s := new(rbScript).call("wiki_get", `{"page":"rb-page-01"}`).call("wiki_search", rcSearch)
	s.call("wiki_search", rcSearch).call("wiki_get", `{"page":"rb-page-01"}`)
	f := newRBFixture(t, s.stop())
	events, err := f.send(t, "")
	rbCleanStop(t, events, err)

	log := readLog(t, logPath)
	if n := strings.Count(log, `msg="agent repeat call refusal"`); n != 2 {
		t.Errorf("log holds %d refusal lines, want 2:\n%s", n, log)
	}
	for _, want := range []string{"name=wiki.search", "name=wiki.get", "first_round=2", "first_round=1"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
}

// TestRepeatCallGuardedSet pins which tools the guard covers: the eight reads,
// and nothing else. The guard is handed a call's wire name, as dispatch has it.
// web.search is not in a bare registry, so it is checked on the guard itself;
// every tool the registry does list is accounted for, so a tool added later is
// a decision made here and not a silent default.
func TestRepeatCallGuardedSet(t *testing.T) {
	g := newRepeatGuard()
	for name, want := range map[string]bool{
		"wiki.search": true, "wiki.get": true, "wiki.neighbors": true, "wiki.backlinks": true,
		"raw.get": true, "raw.list": true, "vault.orient": true, "web.search": true,
		"wiki_lint":         false,
		"stage_create_page": false, "stage_close": false, "stage_open": false,
	} {
		g.noteResult(name, "{}", "id-"+name, 1, false)
		_, _, refused := g.refusal(name, "{}")
		if refused != want {
			t.Errorf("refusal(%q) after a success = %v, want %v", name, refused, want)
		}
	}
	for _, tool := range tools.NewRegistry(tools.Deps{}).List() {
		if strings.HasPrefix(tool.Name, "stage.") {
			continue
		}
		if want := tool.Name != "wiki.lint"; guardedReads[tool.Name] != want {
			t.Errorf("guardedReads[%q] = %v, want %v", tool.Name, guardedReads[tool.Name], want)
		}
	}
}

// TestRepeatCallStateChangeSet pins which stage.* calls make earlier answers
// eligible again: every one but stage.open and stage.close. Unlike 048's
// isPageChange this includes stage.ingest_source — a staged source changes
// what raw.list and raw.get return.
func TestRepeatCallStateChangeSet(t *testing.T) {
	for name, want := range map[string]bool{
		"stage.create_page":   true,
		"stage.patch_page":    true,
		"stage.rename_page":   true,
		"stage.merge_pages":   true,
		"stage.split_page":    true,
		"stage.add_link":      true,
		"stage.retract":       true,
		"stage.ingest_source": true,
		"stage.open":          false,
		"stage.close":         false,
		"wiki.get":            false,
		"raw.get":             false,
	} {
		if got := isStateChange(name); got != want {
			t.Errorf("isStateChange(%q) = %v, want %v", name, got, want)
		}
	}
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

// TestRepeatCallReusedIDFailsOpen: the guard finds a result to forget by its
// tool call id, so a provider that reuses one id for every call must not make it
// forget the wrong read. Calls a, b, a all carry id "x", and the budget elides a's
// first result before the third call: the third call is dispatched. Left keyed by
// the id alone, b's record overwrote a's mapping, the elision of a's result then
// dropped b's entry instead, and a's stayed — so the re-read of an elided page was
// refused as "still above". When an id turns out to be shared, the older call it
// named is forgotten at once (fail open: an extra read is cheap, a refusal of a
// read the model cannot see is not).
func TestRepeatCallReusedIDFailsOpen(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("x", "wiki_get", `{"page":"a"}`), {Finish: "tool_calls"}},
		{toolCallChunk("x", "wiki_get", `{"page":"b"}`), {Finish: "tool_calls"}},
		{toolCallChunk("x", "wiki_get", `{"page":"a"}`), {Finish: "tool_calls"}},
		{{Text: "I have both pages now"}, {Finish: "stop"}},
	}
	ctlFake, _ := runRereadTurn(t, rounds, LoopConfig{ContextTokens: 1000000})
	base := wireEstimate(ctlFake.Requests()[0].Messages)

	fake, events := runRereadTurn(t, rounds, LoopConfig{ContextTokens: base + 5500})
	assertStopTurn(t, events, 4)
	res := toolResults(events)

	// Non-vacuous: when round 3 began, a's result (the first tool message with
	// the shared id) was elided and b's (the second) was not.
	var onWire []llm.Message
	for _, m := range fake.Requests()[2].Messages {
		if m.Role == "tool" && m.ToolCallID == "x" {
			onWire = append(onWire, m)
		}
	}
	if len(onWire) != 2 || onWire[0].Content != probePlaceholder("wiki.get", len(res[0].Content)) || onWire[1].Content != res[1].Content {
		t.Fatalf("round 3's request does not carry a's result elided and b's intact — sizing broke, test vacuous: %q", onWire)
	}
	rcWant(t, events, "wiki.get", "ok", "ok", "ok")
	if res[2].Content != res[0].Content {
		t.Errorf("the re-read of page a returned %q, want the page again", res[2].Content)
	}
}

// TestRepeatCallNilGuardIsInert: a nil *repeatGuard is the guard of a turn that
// has none, the way a nil *readBudget is the budget of every verb but ingest:
// every method is a no-op on it, so a call site never has to ask.
func TestRepeatCallNilGuardIsInert(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a method panicked on a nil guard: %v", r)
		}
	}()
	var g *repeatGuard
	g.noteResult("wiki_search", rcSearch, "id-1", 1, false)
	if text, round, refused := g.refusal("wiki_search", rcSearch); refused || text != "" || round != 0 {
		t.Errorf("refusal on a nil guard = (%q, %d, %v), want none", text, round, refused)
	}
	g.syncElided([]llm.Message{{Role: "tool", ToolCallID: "id-1"}}, 0, map[int]bool{0: true})
}

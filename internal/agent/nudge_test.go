package agent

// nudge_test.go pins A-040-3, the round-budget nudge. The baseline traces
// showed a DeepSeek ingest read its source correctly and then opened every
// wiki page, one call a round, until max_rounds — nothing staged, stage.close
// never called. Nothing in the request told the model the turn was about to
// end, so when the rounds are nearly gone the loop says so: the last tool
// result of each of the final four rounds gets one line appended, on the wire
// only. The session records, and so every later turn's history, are unchanged.
// Permanent regression tests (D-10C).

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/trace"
)

// The two nudge texts, byte for byte, with the round count left as %d. They
// are copied from the amendment, not built from the loop's own constants, so
// a drift in the constants fails here.
const (
	nudgeCuratorFmt = "\n\n[lw: %d round(s) left in this turn — stop reading; stage the pages you have now and call stage.close]"
	nudgeAskFmt     = "\n\n[lw: %d round(s) left in this turn — stop searching; answer now from what you have read]"
)

// nudgeMarker is what every nudge starts with after the blank line.
const nudgeMarker = "[lw: "

// readOnlyRounds scripts n rounds in which the model only reads: each calls
// vault.orient, which both a curator and an ask turn are offered.
func readOnlyRounds(n int) [][]llm.Chunk {
	out := make([][]llm.Chunk, n)
	for i := range out {
		out[i] = []llm.Chunk{toolCallChunk(fmt.Sprintf("call-%d", i+1), "vault_orient", `{}`), {Finish: "tool_calls"}}
	}
	return out
}

// nudgeTurn runs one turn of verb over a Loop capped at maxRounds and
// scripted with rounds, and returns the requests Stream saw, the events and
// the fixture (for its session store).
func nudgeTurn(t *testing.T, verb string, maxRounds int, rounds [][]llm.Chunk) ([]llm.Request, []Event, *testLoopFixture) {
	t.Helper()
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{MaxToolRounds: maxRounds})
	ctx := context.Background()
	if verb != "" {
		ctx = trace.WithVerb(ctx, verb)
	}
	out := make(chan Event, 256)
	if err := l.Send(ctx, fx.csID, "look around", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return fake.Requests(), drain(out), fx
}

// countNudges is how many messages of msgs carry a nudge line.
func countNudges(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += strings.Count(m.Content, nudgeMarker)
	}
	return n
}

// TestRoundBudgetNudgeIngest is the frozen turn: an ingest capped at 6 rounds
// whose model only reads. The nudge fires after round 2 (4 left) and each
// round after it, so requests 3..6 carry it with k = 4, 3, 2, 1 — on the last
// message, which is the round's last tool result — and the earlier two carry
// none. Every request stays a valid chat, and the turn still ends at
// max_rounds: the nudge asks, it does not stop anything.
func TestRoundBudgetNudgeIngest(t *testing.T) {
	reqs, events, fx := nudgeTurn(t, "ingest", 6, readOnlyRounds(6))
	if len(reqs) != 6 {
		t.Fatalf("Stream called %d times, want 6", len(reqs))
	}
	for i, req := range reqs {
		round := i + 1
		if err := validChat(req.Messages); err != nil {
			t.Errorf("request %d is not a valid chat: %v", round, err)
		}
		last := req.Messages[len(req.Messages)-1]
		if round < 3 {
			if n := countNudges(req.Messages); n != 0 {
				t.Errorf("request %d carries %d nudge(s), want none before the last four rounds", round, n)
			}
			continue
		}
		k := 7 - round // rounds left after the round that produced this request's last result
		if want := fmt.Sprintf(nudgeCuratorFmt, k); last.Role != "tool" || !strings.HasSuffix(last.Content, want) {
			t.Errorf("request %d: last message (%s) does not end with the k=%d nudge %q:\n%q", round, last.Role, k, want, last.Content)
		}
		// One nudge per round that had one, persisting in place: the earlier
		// rounds' stay where they were put, which keeps the history a stable
		// prefix of the next request (the provider's cache) and the line in
		// its own round's result.
		if n := countNudges(req.Messages); n != round-2 {
			t.Errorf("request %d carries %d nudge(s), want %d (one per nudged round so far)", round, n, round-2)
		}
	}
	// The wire is not the record: no session record holds the nudge, so no
	// later turn replays it.
	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatal(err)
	}
	tools := 0
	for _, r := range sess.Records {
		if strings.Contains(r.Content, nudgeMarker) || strings.Contains(r.Result, nudgeMarker) {
			t.Errorf("session record carries the nudge: %+v", r)
		}
		if r.Role == "tool" {
			tools++
		}
	}
	if tools != 6 {
		t.Errorf("session holds %d tool records, want 6", tools)
	}
	if done, ok := events[len(events)-1].(DoneEv); !ok || done.Reason != "max_rounds" || done.Rounds != 6 {
		t.Errorf("last event = %#v, want DoneEv{max_rounds, 6}", events[len(events)-1])
	}
}

// TestRoundBudgetNudgeEarlierRequestsUntouched: the nudge a later round adds
// must not show up in a request already sent — the fake keeps each request's
// message slice, and the loop's own slice grows in place, so a nudge written
// to the wrong element would leak backwards.
func TestRoundBudgetNudgeEarlierRequestsUntouched(t *testing.T) {
	reqs, _, _ := nudgeTurn(t, "ingest", 6, readOnlyRounds(6))
	for i, req := range reqs {
		round := i + 1
		want := 0
		if round >= 3 {
			want = round - 2
		}
		if n := countNudges(req.Messages); n != want {
			t.Errorf("request %d, inspected after the whole turn, carries %d nudge(s), want %d", round, n, want)
		}
	}
}

// TestRoundBudgetNudgeModes: the text follows the turn's mode (039's
// modeFromVerb). ask and query are told to stop searching and answer; every
// other verb — ingest, lint, file, none — is told to stage and close.
func TestRoundBudgetNudgeModes(t *testing.T) {
	for _, tc := range []struct {
		verb string
		fmt  string
	}{
		{"query", nudgeAskFmt},
		{"ask", nudgeAskFmt},
		{"ingest", nudgeCuratorFmt},
		{"lint", nudgeCuratorFmt},
		{"file", nudgeCuratorFmt},
		{"", nudgeCuratorFmt},
	} {
		t.Run("verb_"+tc.verb, func(t *testing.T) {
			// Cap 5: round 1 leaves 4, so request 2 is the first nudged.
			reqs, _, _ := nudgeTurn(t, tc.verb, 5, readOnlyRounds(5))
			if len(reqs) != 5 {
				t.Fatalf("Stream called %d times, want 5", len(reqs))
			}
			if n := countNudges(reqs[0].Messages); n != 0 {
				t.Errorf("request 1 carries %d nudge(s)", n)
			}
			for i := 1; i < 5; i++ {
				last := reqs[i].Messages[len(reqs[i].Messages)-1]
				if want := fmt.Sprintf(tc.fmt, 5-i); !strings.HasSuffix(last.Content, want) {
					t.Errorf("verb %q request %d: last message does not end with %q:\n%q", tc.verb, i+1, want, last.Content)
				}
			}
		})
	}
}

// TestRoundBudgetNudgeShortTurn: with fewer than five rounds in all, the
// nudge starts at the first round whose count left is four or fewer — round 1
// of a cap of 3 leaves 2.
func TestRoundBudgetNudgeShortTurn(t *testing.T) {
	reqs, _, _ := nudgeTurn(t, "ingest", 3, readOnlyRounds(3))
	if len(reqs) != 3 {
		t.Fatalf("Stream called %d times, want 3", len(reqs))
	}
	if n := countNudges(reqs[0].Messages); n != 0 {
		t.Errorf("request 1 carries %d nudge(s)", n)
	}
	for i, k := range []int{2, 1} {
		last := reqs[i+1].Messages[len(reqs[i+1].Messages)-1]
		if want := fmt.Sprintf(nudgeCuratorFmt, k); !strings.HasSuffix(last.Content, want) {
			t.Errorf("request %d: last message does not end with the k=%d nudge:\n%q", i+2, k, last.Content)
		}
	}
}

// TestRoundBudgetNudgeAbsentWhenFinishingEarly: a model that answers before
// the last four rounds never sees a nudge, and neither does a turn whose only
// round answers in prose.
func TestRoundBudgetNudgeAbsentWhenFinishingEarly(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "vault_orient", `{}`), {Finish: "tool_calls"}},
		{{Text: "done"}, {Finish: "stop"}},
	}
	reqs, events, _ := nudgeTurn(t, "ingest", 6, rounds)
	if len(reqs) != 2 {
		t.Fatalf("Stream called %d times, want 2", len(reqs))
	}
	for i, req := range reqs {
		if n := countNudges(req.Messages); n != 0 {
			t.Errorf("request %d carries %d nudge(s); the model finished with %d rounds to spare", i+1, n, 6-2)
		}
	}
	if done, ok := events[len(events)-1].(DoneEv); !ok || done.Reason != "stop" {
		t.Errorf("last event = %#v, want a stop", events[len(events)-1])
	}

	reqs, _, _ = nudgeTurn(t, "ingest", 1, [][]llm.Chunk{{{Text: "hi"}, {Finish: "stop"}}})
	if len(reqs) != 1 || countNudges(reqs[0].Messages) != 0 {
		t.Errorf("a one-round prose turn carried a nudge: %d request(s)", len(reqs))
	}
}

// TestRoundBudgetNudgeLastToolResultOnly: a round that makes several tool
// calls gets the nudge on its LAST tool result and no other — the model reads
// the end of a round last, and one line per round is the contract.
func TestRoundBudgetNudgeLastToolResultOnly(t *testing.T) {
	rounds := [][]llm.Chunk{
		{
			toolCallChunk("call-1", "vault_orient", `{}`),
			toolCallChunk("call-2", "raw_list", `{}`),
			toolCallChunk("call-3", "vault_orient", `{}`),
			{Finish: "tool_calls"},
		},
		{{Text: "done"}, {Finish: "stop"}},
	}
	reqs, _, _ := nudgeTurn(t, "ingest", 3, rounds) // round 1 leaves 2: nudged
	if len(reqs) != 2 {
		t.Fatalf("Stream called %d times, want 2", len(reqs))
	}
	msgs := reqs[1].Messages
	var results []llm.Message
	for _, m := range msgs {
		if m.Role == "tool" {
			results = append(results, m)
		}
	}
	if len(results) != 3 {
		t.Fatalf("request 2 holds %d tool results, want 3", len(results))
	}
	for i, m := range results {
		has := strings.Contains(m.Content, nudgeMarker)
		if want := i == 2; has != want {
			t.Errorf("tool result %d (%s): carries nudge = %v, want %v", i+1, m.ToolCallID, has, want)
		}
	}
	if err := validChat(msgs); err != nil {
		t.Errorf("request 2 is not a valid chat: %v", err)
	}
}

// TestRoundBudgetNudgeOnErrorResult: a round whose last tool result is an
// error — here an unknown tool, the correctable path — is nudged like any
// other, and the request stays a valid chat.
func TestRoundBudgetNudgeOnErrorResult(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "no_such_tool", `{}`), {Finish: "tool_calls"}},
		{{Text: "done"}, {Finish: "stop"}},
	}
	reqs, _, _ := nudgeTurn(t, "ingest", 3, rounds)
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if want := fmt.Sprintf(nudgeCuratorFmt, 2); last.Role != "tool" || !strings.HasSuffix(last.Content, want) {
		t.Errorf("error result does not end with the nudge %q:\n%q", want, last.Content)
	}
	if err := validChat(reqs[1].Messages); err != nil {
		t.Errorf("request 2 is not a valid chat: %v", err)
	}
}

// TestRoundBudgetNudgeNotDuplicatedOnRetry: a cut stream re-sends the
// identical request (035 B). The nudge was put on the history by the PREVIOUS
// round, outside the retry loop, so the re-send carries it exactly once and is
// byte-identical to the request that was cut; the next round's nudge is added
// once, not twice.
func TestRoundBudgetNudgeNotDuplicatedOnRetry(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "vault_orient", `{}`), {Finish: "tool_calls"}}, // round 1: 5 left, no nudge
		{toolCallChunk("call-2", "vault_orient", `{}`), {Finish: "tool_calls"}}, // round 2: 4 left, nudge k=4
		{cutChunk()}, // round 3, attempt 1: cut before any call
		{toolCallChunk("call-3", "vault_orient", `{}`), {Finish: "tool_calls"}}, // round 3, attempt 2: 3 left, nudge k=3
		{{Text: "done"}, {Finish: "stop"}},
	}
	reqs, _, _ := nudgeTurn(t, "ingest", 6, rounds)
	if len(reqs) != 5 {
		t.Fatalf("Stream called %d times, want 5 (the cut round re-sent once)", len(reqs))
	}
	if !reflect.DeepEqual(reqs[2].Messages, reqs[3].Messages) {
		t.Errorf("the re-sent request differs from the cut one:\n cut    %#v\n resent %#v", reqs[2].Messages, reqs[3].Messages)
	}
	if n := countNudges(reqs[3].Messages); n != 1 {
		t.Errorf("the re-sent request carries %d nudge(s), want exactly 1 (round 2's)", n)
	}
	if n := countNudges(reqs[4].Messages); n != 2 {
		t.Errorf("request after the retried round carries %d nudge(s), want 2 (rounds 2 and 3)", n)
	}
	last := reqs[4].Messages[len(reqs[4].Messages)-1]
	if want := fmt.Sprintf(nudgeCuratorFmt, 3); !strings.HasSuffix(last.Content, want) {
		t.Errorf("last message does not end with the k=3 nudge:\n%q", last.Content)
	}
}

// TestRoundBudgetNudgeIsLogged: every nudge leaves one "agent round budget
// nudge" line in the file log, with the round, the rounds left and the mode,
// so a stuck turn's log shows when the model was told.
func TestRoundBudgetNudgeIsLogged(t *testing.T) {
	logPath := installFileLog(t)
	nudgeTurn(t, "query", 5, readOnlyRounds(5))
	log := readLog(t, logPath)
	if n := strings.Count(log, `msg="agent round budget nudge"`); n != 4 {
		t.Errorf("log holds %d nudge lines, want 4:\n%s", n, log)
	}
	for _, want := range []string{"rounds_left=4", "rounds_left=1", "mode=ask"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
}

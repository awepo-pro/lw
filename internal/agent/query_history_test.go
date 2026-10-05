package agent

// query_history_test.go is 039's review pin on the one place the two halves of
// ask mode meet the history: an ask or query turn is offered only read tools,
// but the session it runs in (the pane's, or a resumed changeset's) can hold
// earlier curator turns' stage.* calls. Those are replayed as history pairs
// (046), and a replayed pair names a tool the request's Tools no longer lists.
// The chat must still be valid — every tool message answers an assistant
// tool_call id — because a strict provider validates the history, not the
// tool list. Permanent regression test (D-10C).

import (
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
)

// TestQueryTurnReplaysStagedHistoryAsValidChat seeds a session with an earlier
// filing turn — stage_create_page and stage_close pairs, two of each, one
// create_page carrying a page body — then sends a query turn through Send. The
// request that goes out must be a valid chat, must replay all four calls as
// history pairs (a test that found none would pass vacuously), and must
// advertise no stage_* tool, exactly the read-only set a query is offered.
func TestQueryTurnReplaysStagedHistoryAsValidChat(t *testing.T) {
	m := newModeFixture(t, false, oneRound())
	ts := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	seed := []Record{
		{TS: ts, Role: "user", Content: "file that answer as a page"},
		{TS: ts.Add(1 * time.Second), Role: "tool", Tool: "stage.create_page", Staged: true,
			Args:   `{"path":"wiki/concepts/answer.md","title":"Answer","type":"concept","body":"# Answer\n\n` + strings.Repeat("A filed answer. ", 40) + `"}`,
			Result: "proposed op1 (create_page)"},
		{TS: ts.Add(2 * time.Second), Role: "tool", Tool: "stage.close", Staged: true, Args: `{}`, Result: "changeset cs-x\nintent: file\noperations: 1"},
		{TS: ts.Add(3 * time.Second), Role: "assistant", Content: "Filed."},
		{TS: ts.Add(4 * time.Second), Role: "tool", Tool: "stage.create_page", Staged: true,
			Args:   `{"path":"wiki/concepts/answer-two.md","title":"Answer Two","type":"concept","body":"# Answer Two"}`,
			Result: "proposed op2 (create_page)"},
		{TS: ts.Add(5 * time.Second), Role: "tool", Tool: "stage.close", Staged: true, Args: `{}`, Result: "changeset cs-x\nintent: file\noperations: 2"},
	}
	for _, r := range seed {
		if err := m.store.Append(m.csID, r); err != nil {
			t.Fatalf("seed session: %v", err)
		}
	}

	if _, err := m.send(t, "query"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	reqs := m.fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Stream called %d times, want 1", len(reqs))
	}
	req := reqs[0]

	if err := validChat(req.Messages); err != nil {
		t.Fatalf("a query turn over a session with staged history is not a valid chat: %v", err)
	}

	replayed := map[string]int{}
	for _, msg := range req.Messages {
		for _, tc := range msg.ToolCalls {
			if strings.HasPrefix(tc.ID, "hist_") {
				replayed[tc.Function.Name]++
			}
		}
	}
	if replayed["stage_create_page"] != 2 || replayed["stage_close"] != 2 {
		t.Fatalf("history replayed %v, want 2 stage_create_page and 2 stage_close pairs", replayed)
	}

	// The tools advertised are the query set, wire-spelled and in order, with
	// not one stage tool among them — the history names calls the turn may not
	// make.
	var got []string
	for _, d := range req.Tools {
		got = append(got, d.Name)
		if strings.HasPrefix(d.Name, "stage_") {
			t.Errorf("a query turn advertises %s", d.Name)
		}
	}
	var want []string
	for _, c := range wantQueryTools {
		want = append(want, tools.WireName(c))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("a query turn's tools:\n got  %v\n want %v", got, want)
	}
}

// TestQueryTurnReplayedStageCallIsRefusedNotDispatched completes the pair
// above: the model, seeing stage_create_page in its history, calls it. The
// turn refuses the call with the tool-not-available result — never dispatching
// it, so nothing is staged — and the very next request is still a valid chat.
func TestQueryTurnReplayedStageCallIsRefusedNotDispatched(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_close", `{}`), {Finish: "tool_calls"}},
		{{Text: "answer"}, {Finish: "stop"}},
	}
	m := newModeFixture(t, false, rounds)
	ts := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	for _, r := range []Record{
		{TS: ts, Role: "tool", Tool: "stage.close", Staged: true, Args: `{}`, Result: "changeset cs-x\noperations: 1"},
	} {
		if err := m.store.Append(m.csID, r); err != nil {
			t.Fatal(err)
		}
	}
	events, err := m.send(t, "query")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	reqs := m.fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Stream called %d times, want 2", len(reqs))
	}
	if err := validChat(reqs[1].Messages); err != nil {
		t.Errorf("request 2 (after the refused call) is not a valid chat: %v", err)
	}
	refused := false
	for _, ev := range events {
		if res, ok := ev.(ToolResEv); ok && res.IsError && strings.Contains(res.Content, "is not available in this turn") {
			refused = true
		}
		if _, ok := ev.(StageEv); ok {
			t.Errorf("a refused stage call produced a StageEv: the call was dispatched")
		}
	}
	if !refused {
		t.Errorf("the replayed stage call was not refused: %#v", events)
	}
}

package agent

// reasoning_test.go pins 005's recording of the round's thinking (contract
// §3, amendment C-501, resolution D-5G): the loop persists reasoning onto
// the round's assistant Record, a reasoning-only round still records,
// replayed history never carries reasoning back to the wire, and
// compaction never budgets bytes the provider never sees. The persistence
// half — the pre-005 fixture round trip and the API-key assertion — lives
// in reasoning_persist_test.go.

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
)

// sendAndCollect drives one full turn through a scripted loop and returns
// the session it persisted.
func sendAndCollect(t *testing.T, rounds [][]llm.Chunk) (*testLoopFixture, []Record) {
	t.Helper()
	l, fx, _ := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "one turn about reasoning records", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	return fx, sess.Records
}

func TestReasoningRecorded(t *testing.T) {
	t.Run("one_assistant_record_carries_text_and_reasoning", func(t *testing.T) {
		const wantReasoning = "The user wants a summary. I should read the page first."
		const wantText = "Reading the page now. Then I will summarize."
		rounds := [][]llm.Chunk{
			{
				{Reasoning: "The user wants a summary. "},
				{Reasoning: "I should read the page first."},
				{Text: "Reading the page now. "},
				{Text: "Then I will summarize."},
				toolCallChunk("call-1", "stage.close", ""),
				{Finish: "tool_calls"},
			},
			{
				{Text: "done"},
				{Finish: "stop"},
			},
		}
		_, recs := sendAndCollect(t, rounds)

		var asst []Record
		for _, r := range recs {
			if r.Role == "assistant" {
				asst = append(asst, r)
			}
		}
		// Not two records, and not one per delta: exactly one assistant
		// record for the round, plus the final round's plain answer.
		if len(asst) != 2 {
			t.Fatalf("got %d assistant records, want 2 (round 1's merged record + the final answer): %+v", len(asst), recs)
		}
		if asst[0].Content != wantText {
			t.Errorf("round 1 assistant Content = %q, want %q (the round's full text)", asst[0].Content, wantText)
		}
		if asst[0].Reasoning != wantReasoning {
			t.Errorf("round 1 assistant Reasoning = %q, want %q (the round's full thinking)", asst[0].Reasoning, wantReasoning)
		}
	})

	t.Run("reasoning_only_round_records_empty_content", func(t *testing.T) {
		const wantReasoning = "No text needed — just close the changeset."
		rounds := [][]llm.Chunk{
			{
				{Reasoning: wantReasoning},
				toolCallChunk("call-1", "stage.close", ""),
				{Finish: "tool_calls"},
			},
			{
				{Text: "done"},
				{Finish: "stop"},
			},
		}
		_, recs := sendAndCollect(t, rounds)

		var asst []Record
		for _, r := range recs {
			if r.Role == "assistant" {
				asst = append(asst, r)
			}
		}
		if len(asst) != 2 {
			t.Fatalf("got %d assistant records, want 2 (the round's record + the final answer): %+v", len(asst), recs)
		}
		if asst[0].Content != "" {
			t.Errorf("round 1 assistant Content = %q, want \"\" (the round streamed no text)", asst[0].Content)
		}
		if asst[0].Reasoning != wantReasoning {
			t.Errorf("round 1 assistant Reasoning = %q, want %q — C-501: a reasoning-only round must still record", asst[0].Reasoning, wantReasoning)
		}
	})

	t.Run("role_is_assistant_never_thinking", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{
				{Reasoning: "thinking about the call"},
				{Text: "brief note before the call"},
				toolCallChunk("call-1", "stage.close", ""),
				{Finish: "tool_calls"},
			},
			{
				{Reasoning: "thinking about the answer"},
				{Text: "done"},
				{Finish: "stop"},
			},
		}
		_, recs := sendAndCollect(t, rounds)

		if len(recs) == 0 {
			t.Fatal("no records at all")
		}
		for i, r := range recs {
			switch r.Role {
			case "user", "assistant", "tool":
				// The only three roles that may ever enter session.ndjson.
			default:
				t.Errorf("record %d has Role %q — no new role string may enter session.ndjson (contract §3 note 2): %+v", i, r.Role, r)
			}
		}
		// And the thinking actually reached the records it belongs to, so
		// the role check above is not passing vacuously.
		var sawReasoning bool
		for _, r := range recs {
			if r.Reasoning != "" {
				sawReasoning = true
				if r.Role != "assistant" {
					t.Errorf("record with Reasoning has Role %q, want assistant: %+v", r.Role, r)
				}
			}
		}
		if !sawReasoning {
			t.Fatal("no record carried reasoning; the role assertion proved nothing")
		}
	})

	t.Run("two_rounds_do_not_share_reasoning", func(t *testing.T) {
		const round1Reasoning = "round one thinking"
		const round2Reasoning = "round two thinking"
		rounds := [][]llm.Chunk{
			{
				{Reasoning: round1Reasoning},
				toolCallChunk("call-1", "stage.close", ""),
				{Finish: "tool_calls"},
			},
			{
				{Reasoning: round2Reasoning},
				toolCallChunk("call-2", "stage.close", ""),
				{Finish: "tool_calls"},
			},
			{
				{Text: "done"},
				{Finish: "stop"},
			},
		}
		_, recs := sendAndCollect(t, rounds)

		var asst []Record
		for _, r := range recs {
			if r.Role == "assistant" {
				asst = append(asst, r)
			}
		}
		if len(asst) != 3 {
			t.Fatalf("got %d assistant records, want 3 (one per round): %+v", len(asst), recs)
		}
		if asst[0].Reasoning != round1Reasoning {
			t.Errorf("round 1 record Reasoning = %q, want %q", asst[0].Reasoning, round1Reasoning)
		}
		if asst[1].Reasoning != round2Reasoning {
			t.Errorf("round 2 record Reasoning = %q, want %q only", asst[1].Reasoning, round2Reasoning)
		}
		if strings.Contains(asst[1].Reasoning, round1Reasoning) {
			t.Errorf("round 2 record carries round 1's thinking: %q", asst[1].Reasoning)
		}
		if asst[2].Reasoning != "" {
			t.Errorf("round 3 record Reasoning = %q, want \"\" (that round streamed none)", asst[2].Reasoning)
		}
	})
}

func TestReasoningNeverReplayed(t *testing.T) {
	t.Run("record_to_message_drops_reasoning", func(t *testing.T) {
		const reasoning = "SECRET-THINKING the provider must never see again"
		r := Record{TS: time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC), Role: "assistant", Content: "visible reply", Reasoning: reasoning}

		m := recordToMessage(r)
		if m.Role != "assistant" {
			t.Errorf("Role = %q, want assistant", m.Role)
		}
		if m.Content != "visible reply" {
			t.Errorf("Content = %q, want the record's Content only", m.Content)
		}
		if m.ReasoningContent != "" {
			t.Errorf("ReasoningContent = %q, want \"\" — D-5G: replayed history stays byte-identical to v1.0.0's", m.ReasoningContent)
		}
		if strings.Contains(m.Content, reasoning) {
			t.Errorf("the reasoning text leaked into the message Content: %q", m.Content)
		}
	})

	t.Run("empty_assistant_record_is_skipped", func(t *testing.T) {
		v, _ := newTestVault(t)
		reg := tools.NewRegistry(tools.Deps{Vault: v})
		b := NewContextBuilder(v, reg, 100_000)

		ts := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
		const reasoning = "thought about it, wrote no prose"
		s := &Session{
			ID:          "cs-skip",
			ChangesetID: "cs-skip",
			Records: []Record{
				rec(ts, "user", "first turn"),
				{TS: ts.Add(time.Second), Role: "assistant", Content: "", Reasoning: reasoning},
			},
		}

		msgs, err := b.Build(s, "next question")
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		for i, m := range msgs {
			if m.Role == "assistant" && m.Content == "" {
				t.Errorf("msgs[%d] is an empty assistant message — D-5G: some providers reject it, Build must skip the record: %+v", i, msgs)
			}
			if strings.Contains(m.Content, reasoning) {
				t.Errorf("msgs[%d].Content carries the reasoning text; reasoning must never re-enter the wire: %+v", i, m)
			}
		}
		// The guard skips exactly the empty record, not its neighbours: the
		// user history record and the new user message must both survive.
		var sawHistory, sawTurn bool
		for _, m := range msgs {
			if m.Role == "user" && m.Content == "first turn" {
				sawHistory = true
			}
			if m.Role == "user" && m.Content == "next question" {
				sawTurn = true
			}
		}
		if !sawHistory || !sawTurn {
			t.Errorf("Build dropped legitimate history alongside the empty record: %+v", msgs)
		}
	})
}

func TestReasoningCompaction(t *testing.T) {
	ts := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	longProse := strings.Repeat("ordinary conversational prose long enough to dominate any budget. ", 8)

	t.Run("reasoning_record_is_prose", func(t *testing.T) {
		reasoningOnly := Record{TS: ts, Role: "assistant", Content: "", Reasoning: "thinking only, no prose"}
		if !isProse(reasoningOnly) {
			t.Fatalf("isProse(reasoning-only record) = false, want true (contract §3 note 3: prose, collapsible, never Staged-protected)")
		}

		// And Compact may actually collapse it: a tight budget over a run
		// of prose leaves one placeholder, not the reasoning record.
		recs := []Record{
			rec(ts, "user", longProse),
			reasoningOnly,
			rec(ts.Add(time.Second), "assistant", longProse),
		}
		got := Compact(recs, 10)
		if len(got) != 1 {
			t.Fatalf("Compact left %d records, want 1 placeholder: %+v", len(got), got)
		}
		for _, r := range got {
			if strings.Contains(r.Content, "thinking only") {
				t.Errorf("the reasoning-only record survived a budget that collapses prose: %+v", got)
			}
		}
	})

	t.Run("staged_records_still_never_dropped", func(t *testing.T) {
		staged := Record{TS: ts.Add(time.Second), Role: "tool", Tool: "stage.create_page", Args: `{"op":"keep-me"}`, Result: "proposed", Staged: true}
		recs := []Record{
			rec(ts, "user", longProse),
			staged,
			rec(ts.Add(2*time.Second), "assistant", longProse),
		}
		got := Compact(recs, 1) // impossibly small budget
		var found *Record
		for i := range got {
			if got[i].Staged {
				found = &got[i]
			}
		}
		if found == nil {
			t.Fatalf("Compact dropped the staged record under budget pressure: %+v", got)
		}
		if !reflect.DeepEqual(*found, staged) {
			t.Errorf("staged record was altered: got %+v, want %+v", *found, staged)
		}
	})

	t.Run("reasoning_does_not_shrink_the_budget", func(t *testing.T) {
		reasoning := strings.Repeat("reasoning bytes the provider never sees. ", 64) // ~2.6 KB
		answer := strings.Repeat("the visible answer prose. ", 20)                   // ~100 estimated tokens
		withReasoning := []Record{
			rec(ts, "user", "hello"),
			{TS: ts.Add(time.Second), Role: "assistant", Content: answer, Reasoning: reasoning},
		}
		withoutReasoning := []Record{
			rec(ts, "user", "hello"),
			{TS: ts.Add(time.Second), Role: "assistant", Content: answer},
		}

		// The direct pin on D-5G(c): the two records measure identically,
		// so reasoning can never push a session over budget.
		if recordTokens(withReasoning[1]) != recordTokens(withoutReasoning[1]) {
			t.Fatalf("recordTokens counts Reasoning: %d with vs %d without", recordTokens(withReasoning[1]), recordTokens(withoutReasoning[1]))
		}

		// stripReasoning erases the one field the two sessions may differ
		// in, so DeepEqual below compares the compaction DECISION only.
		stripReasoning := func(recs []Record) []Record {
			out := make([]Record, len(recs))
			copy(out, recs)
			for i := range out {
				out[i].Reasoning = ""
			}
			return out
		}

		// 150 would keep both sessions intact; if Reasoning were budgeted,
		// the reasoning-carrying session (~750 tokens) would collapse there
		// while the plain one (~101) would not — exactly the divergence
		// this test must catch. 5 forces the same collapse on both.
		for _, budget := range []int{1_000_000, 150, 5} {
			a := Compact(withReasoning, budget)
			b := Compact(withoutReasoning, budget)
			if !reflect.DeepEqual(stripReasoning(a), b) {
				t.Errorf("budget %d: Compact diverged on reasoning:\n with: %+v\n without: %+v", budget, a, b)
			}
		}
	})
}

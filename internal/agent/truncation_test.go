package agent

// truncation_test.go pins 008's U1 fix (contract §1). A live GLM round
// ended with finish_reason "length" — the output-token cap — carrying no
// content and no tool call, and lw reported the turn as a clean finish, so
// 000006 committed an ingest of zero pages (MASTER §6 W0). The rule now:
// a round whose last non-empty Chunk.Finish is anything but "", "stop" or
// "tool_calls", and that completed no tool call, ends the turn with exactly
// one ErrorEv wrapping ErrTruncated — never a DoneEv — after writing one
// assistant Record carrying the round's pending text, reasoning and the
// finish reason.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
)

// runTurn drives one full turn through a scripted loop and returns the
// fixture, every drained event, and Send's own return — which for the
// truncation tests is expected to be the error the terminal ErrorEv
// carried, not nil.
func runTurn(t *testing.T, rounds [][]llm.Chunk) (*testLoopFixture, []Event, error) {
	t.Helper()
	l, fx, _ := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	err := l.Send(context.Background(), fx.csID, "one turn that may be truncated", out)
	events := drain(out)
	return fx, events, err
}

// sessionNDJSON reads the turn's on-disk transcript, so assertions cover
// the marshalled bytes and not just the in-memory Records view.
func sessionNDJSON(t *testing.T, fx *testLoopFixture) []byte {
	t.Helper()
	p := filepath.Join(fx.engine.Vault().Root(), ".llmwiki", "changesets", "open", fx.csID, "session.ndjson")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read session.ndjson: %v", err)
	}
	return raw
}

func TestTruncatedTurn(t *testing.T) {
	t.Run("length_without_tool_call_is_an_error", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{
				{Reasoning: "Collecting sources. "},
				{Reasoning: "Still collecting — no answer yet."},
				{Finish: "length"},
			},
		}
		_, events, err := runTurn(t, rounds)

		if err == nil {
			t.Fatal("Send = nil, want ErrTruncated — a length finish with no tool call is not a clean stop (U1)")
		}
		if !errors.Is(err, ErrTruncated) {
			t.Fatalf("Send error = %v, want errors.Is(err, ErrTruncated)", err)
		}
		const want = `agent: the model stopped before finishing its turn (finish_reason "length" in round 1)`
		if err.Error() != want {
			t.Fatalf("Send error text = %q, want %q", err.Error(), want)
		}
		if len(events) == 0 {
			t.Fatal("no events at all; want a terminal ErrorEv")
		}
	})
	t.Run("exactly_one_terminal_event", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{
				{Reasoning: "Truncated mid-thought."},
				{Finish: "length"},
			},
		}
		_, events, err := runTurn(t, rounds)
		if err == nil {
			t.Fatal("Send = nil, want ErrTruncated")
		}

		errEvs := 0
		for i, ev := range events {
			switch e := ev.(type) {
			case DoneEv:
				t.Errorf("event %d is a DoneEv — a truncated turn must never end clean (C-801): %+v", i, e)
			case ErrorEv:
				errEvs++
				if e.Err != err {
					t.Errorf("ErrorEv.Err = %v, Send returned %v; want the exact same error (backbone §9, C-105)", e.Err, err)
				}
				if i != len(events)-1 {
					t.Errorf("ErrorEv at %d of %d events; the terminal event must be last", i, len(events))
				}
			}
		}
		if errEvs != 1 {
			t.Fatalf("got %d ErrorEv in %#v, want exactly one", errEvs, events)
		}
	})
	t.Run("round_number_is_the_truncated_round", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{
				toolCallChunk("call-1", "stage.close", ""),
				{Finish: "tool_calls"},
			},
			{
				{Reasoning: "Round two hit the cap."},
				{Finish: "length"},
			},
		}
		_, _, err := runTurn(t, rounds)
		if !errors.Is(err, ErrTruncated) {
			t.Fatalf("Send error = %v, want ErrTruncated", err)
		}
		const want = `agent: the model stopped before finishing its turn (finish_reason "length" in round 2)`
		if err.Error() != want {
			t.Fatalf("Send error text = %q, want %q — the round that truncated, not round 1", err.Error(), want)
		}
	})
	t.Run("length_with_tool_call_proceeds", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{
				toolCallChunk("call-1", "stage.close", ""),
				{Finish: "length"},
			},
			{
				{Text: "Closed, despite the round-one cap."},
				{Finish: "stop"},
			},
		}
		_, events, err := runTurn(t, rounds)
		if err != nil {
			t.Fatalf("Send: %v — a round that completed a tool call proceeds whatever its finish reason", err)
		}

		sawCall, sawRes := false, false
		for _, ev := range events {
			switch ev.(type) {
			case ToolCallEv:
				sawCall = true
			case ToolResEv:
				sawRes = true
			}
		}
		if !sawCall || !sawRes {
			t.Fatalf("tool call not dispatched (call=%t res=%t): %#v", sawCall, sawRes, events)
		}
		last := events[len(events)-1]
		done, ok := last.(DoneEv)
		if !ok || done.Reason != "stop" || done.Rounds != 2 {
			t.Fatalf("last event = %#v, want DoneEv{Reason:stop, Rounds:2}", last)
		}
	})
	t.Run("empty_finish_is_a_clean_stop", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{{Text: "plain answer, and the stream closes with no finish chunk"}},
		}
		_, events, err := runTurn(t, rounds)
		if err != nil {
			t.Fatalf("Send: %v — \"\" stays a clean end (contract §1)", err)
		}
		last := events[len(events)-1]
		done, ok := last.(DoneEv)
		if !ok || done.Reason != "stop" || done.Rounds != 1 {
			t.Fatalf("last event = %#v, want DoneEv{Reason:stop, Rounds:1}", last)
		}
	})
	t.Run("unknown_finish_is_truncation", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{
				{Text: "a filter stopped this answer"},
				{Finish: "content_filter"},
			},
		}
		_, _, err := runTurn(t, rounds)
		if !errors.Is(err, ErrTruncated) {
			t.Fatalf("Send error = %v, want ErrTruncated", err)
		}
		if !strings.Contains(err.Error(), `"content_filter"`) {
			t.Fatalf("error text %q does not name the finish reason", err.Error())
		}
	})
}

func TestFinishRecord(t *testing.T) {
	t.Run("truncated_round_records_finish_and_reasoning", func(t *testing.T) {
		const think = "The budget ran out while I was still collecting sources."
		const wantText = "Partial answer before the cap: sources one and two"
		rounds := [][]llm.Chunk{
			{
				{Reasoning: think},
				{Text: "Partial answer before the cap: "},
				{Text: "sources one and two"},
				{Finish: "length"},
			},
		}
		fx, _, err := runTurn(t, rounds)
		if !errors.Is(err, ErrTruncated) {
			t.Fatalf("Send error = %v, want ErrTruncated", err)
		}

		sess, gerr := fx.store.Get(fx.csID)
		if gerr != nil {
			t.Fatalf("Get session: %v", gerr)
		}
		if len(sess.Records) != 2 {
			t.Fatalf("session records = %+v, want 2 (user + the round's one assistant record)", sess.Records)
		}
		last := sess.Records[len(sess.Records)-1]
		if last.Role != "assistant" {
			t.Fatalf("last record Role = %q, want assistant: %+v", last.Role, last)
		}
		if last.Reasoning != think {
			t.Errorf("last record Reasoning = %q, want %q", last.Reasoning, think)
		}
		if last.Content != wantText {
			t.Errorf("last record Content = %q, want %q (the round's pending text)", last.Content, wantText)
		}
		if last.Finish != "length" {
			t.Errorf("last record Finish = %q, want %q", last.Finish, "length")
		}
	})
	t.Run("finish_record_written_with_empty_buffers", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{{Finish: "length"}},
		}
		fx, _, err := runTurn(t, rounds)
		if !errors.Is(err, ErrTruncated) {
			t.Fatalf("Send error = %v, want ErrTruncated", err)
		}

		sess, gerr := fx.store.Get(fx.csID)
		if gerr != nil {
			t.Fatalf("Get session: %v", gerr)
		}
		if len(sess.Records) != 2 {
			t.Fatalf("session records = %+v, want 2 (user + one assistant record even with empty buffers)", sess.Records)
		}
		last := sess.Records[len(sess.Records)-1]
		if last.Role != "assistant" {
			t.Fatalf("last record Role = %q, want assistant: %+v", last.Role, last)
		}
		if last.Content != "" || last.Reasoning != "" {
			t.Errorf("last record Content/Reasoning = %q/%q, want both empty", last.Content, last.Reasoning)
		}
		if last.Finish != "length" {
			t.Errorf("last record Finish = %q, want %q — the record is the only trace of the round", last.Finish, "length")
		}
	})
	t.Run("normal_rounds_have_no_finish", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{
				toolCallChunk("call-1", "stage.close", ""),
				{Finish: "tool_calls"},
			},
			{
				{Text: "All done."},
				{Finish: "stop"},
			},
		}
		fx, _, err := runTurn(t, rounds)
		if err != nil {
			t.Fatalf("Send: %v", err)
		}

		sess, gerr := fx.store.Get(fx.csID)
		if gerr != nil {
			t.Fatalf("Get session: %v", gerr)
		}
		for i, r := range sess.Records {
			if r.Finish != "" {
				t.Errorf("record %d carries Finish %q; a clean turn must never set it: %+v", i, r.Finish, r)
			}
			line, merr := json.Marshal(r)
			if merr != nil {
				t.Fatalf("marshal record %d: %v", i, merr)
			}
			if bytes.Contains(line, []byte(`"finish"`)) {
				t.Errorf("record %d marshals with a %q key: %s", i, "finish", line)
			}
		}
		if raw := sessionNDJSON(t, fx); bytes.Contains(raw, []byte(`"finish"`)) {
			t.Fatalf("session.ndjson carries a %q key after a clean two-round turn:\n%s", "finish", raw)
		}
	})
	t.Run("record_to_message_drops_finish", func(t *testing.T) {
		ts := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
		with := Record{TS: ts, Role: "assistant", Content: "visible reply", Reasoning: "thinking", Finish: "length"}
		without := with
		without.Finish = ""

		got, want := recordToMessage(with), recordToMessage(without)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("recordToMessage(with Finish) = %+v, without = %+v; Finish must never reach the wire (contract §1)", got, want)
		}
	})
}

func TestPromptRawRules(t *testing.T) {
	t.Run("prompt_contains_rule_verbatim", func(t *testing.T) {
		const want = "A raw source you were asked to ingest is the only source for that ingest: never read, cite or patch from a different raw file in its place. If stage.ingest_source fails, stop and report the error instead of working around it; use raw.list to find a raw source whose path you do not know."
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("systemPrompt does not contain contract §1's raw-source rule byte for byte:\n%s", systemPrompt)
		}
		// Its own paragraph: the sentence block is delimited by newlines on
		// both sides, not glued onto a neighbouring rule.
		i := strings.Index(systemPrompt, want)
		if i == -1 {
			t.Fatal("unreachable: Contains above proved the text present")
		}
		if i > 0 && systemPrompt[i-1] != '\n' {
			t.Error("the rule does not start its own paragraph")
		}
		if end := i + len(want); end < len(systemPrompt) && systemPrompt[end] != '\n' {
			t.Error("the rule does not end its own paragraph")
		}
	})
}

// TestSystemPromptIndexRule pins A-805's index.md rule (G5b P4): the
// prompt must say, byte for byte and on a line of its own, that index.md
// is derived — agents kept patching it by hand.
func TestSystemPromptIndexRule(t *testing.T) {
	const want = "index.md is derived by the engine: every stage.create_page adds its index line automatically, so never patch or create index.md."
	if !strings.Contains(systemPrompt, want) {
		t.Fatalf("systemPrompt does not contain A-805's index.md rule byte for byte:\n%s", systemPrompt)
	}
	// Its own line: delimited by newlines on both sides, never glued onto
	// a neighbouring rule.
	i := strings.Index(systemPrompt, want)
	if i == -1 {
		t.Fatal("unreachable: Contains above proved the text present")
	}
	if i > 0 && systemPrompt[i-1] != '\n' {
		t.Error("the index.md rule does not start its own line")
	}
	if end := i + len(want); end < len(systemPrompt) && systemPrompt[end] != '\n' {
		t.Error("the index.md rule does not end its own line")
	}
}

// TestSystemPromptOutsideVaultRule pins A-806 (G5b P2): when neither the
// wiki nor the raw sources answer a question, the curator must say so and
// then answer from its own knowledge under the exact "Not from your
// vault:" label — a bare "nothing in the vault" left the question
// unanswered. Byte for byte, on a line of its own, immediately after the
// A-805 index.md rule.
func TestSystemPromptOutsideVaultRule(t *testing.T) {
	const want = "If neither the wiki nor the raw sources answer a question, say so in one sentence, then answer from your own knowledge under a first line that reads exactly \"Not from your vault:\"; carry no provenance marker on those claims, and say plainly when the topic may be newer than your training data."
	if !strings.Contains(systemPrompt, want) {
		t.Fatalf("systemPrompt does not contain A-806's out-of-vault rule byte for byte:\n%s", systemPrompt)
	}
	// Its own line: delimited by newlines on both sides, never glued onto
	// a neighbouring rule.
	i := strings.Index(systemPrompt, want)
	if i == -1 {
		t.Fatal("unreachable: Contains above proved the text present")
	}
	if i > 0 && systemPrompt[i-1] != '\n' {
		t.Error("the out-of-vault rule does not start its own line")
	}
	if end := i + len(want); end < len(systemPrompt) && systemPrompt[end] != '\n' {
		t.Error("the out-of-vault rule does not end its own line")
	}
	// And placed straight after A-805's index.md rule, per the contract.
	const indexRule = "index.md is derived by the engine: every stage.create_page adds its index line automatically, so never patch or create index.md."
	j := strings.Index(systemPrompt, indexRule)
	if j == -1 {
		t.Fatal("precondition: the A-805 index.md rule is missing from systemPrompt")
	}
	if gap := systemPrompt[j+len(indexRule) : i]; gap != "\n" {
		t.Errorf("the out-of-vault rule is not immediately after the index.md rule (gap %q)", gap)
	}
}

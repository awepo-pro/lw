package agent

// reasoning_delta_test.go pins 022 T2's live reasoning stream: a round that
// thinks emits one ReasoningDelta per non-empty reasoning chunk, in stream
// order and always before the round's text, while a round that streams no
// reasoning emits none — and the new event changes nothing about what the
// turn persists. The records-side contracts (005's persistence, D-5G's
// replay rules) keep their own tests; this file only proves the delta
// stream is a pure addition.

import (
	"context"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
)

func TestLoopEmitsReasoningDelta(t *testing.T) {
	t.Run("reasoning_chunks_emit_deltas_in_order_before_text", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{
				{Reasoning: "first thought. "},
				{Reasoning: "second thought."},
				{Text: "The answer"},
				{Text: " is five."},
				{Finish: "stop"},
			},
		}
		l, fx, _ := newTestLoop(t, rounds, LoopConfig{})

		out := make(chan Event, 64)
		if err := l.Send(context.Background(), fx.csID, "one thinking round", out); err != nil {
			t.Fatalf("Send: %v", err)
		}
		events := drain(out)

		var deltas []string
		var texts []string
		lastDelta, firstText := -1, len(events)
		for i, ev := range events {
			switch e := ev.(type) {
			case ReasoningDelta:
				deltas = append(deltas, e.Text)
				lastDelta = i
			case TextDelta:
				texts = append(texts, e.Text)
				if i < firstText {
					firstText = i
				}
			}
		}
		// One delta per non-empty reasoning chunk, verbatim, in stream
		// order — and every one of them before the round's first text.
		if len(deltas) != 2 {
			t.Fatalf("got %d ReasoningDelta events, want 2 (one per reasoning chunk): %#v", len(deltas), events)
		}
		if deltas[0] != "first thought. " || deltas[1] != "second thought." {
			t.Errorf("deltas = %q, want the chunks' reasoning verbatim in stream order", deltas)
		}
		if lastDelta > firstText {
			t.Errorf("ReasoningDelta at %d arrived after TextDelta at %d; thinking must precede the round's text: %#v", lastDelta, firstText, events)
		}
		if got := strings.Join(texts, ""); got != "The answer is five." {
			t.Errorf("text deltas joined = %q, want the round's text untouched", got)
		}
	})

	t.Run("no_reasoning_emits_no_deltas", func(t *testing.T) {
		rounds := [][]llm.Chunk{
			{{Text: "plain answer"}, {Finish: "stop"}},
		}
		l, fx, _ := newTestLoop(t, rounds, LoopConfig{})

		out := make(chan Event, 64)
		if err := l.Send(context.Background(), fx.csID, "one plain round", out); err != nil {
			t.Fatalf("Send: %v", err)
		}
		events := drain(out)

		for i, ev := range events {
			if _, ok := ev.(ReasoningDelta); ok {
				t.Errorf("events[%d] is a ReasoningDelta; a stream with no reasoning must emit none: %#v", i, events)
			}
		}

		// And such a round's records carry no reasoning either — nothing
		// about a reasoning-less turn changed.
		sess, err := fx.store.Get(fx.csID)
		if err != nil {
			t.Fatalf("Get session: %v", err)
		}
		for i, r := range sess.Records {
			if r.Reasoning != "" {
				t.Errorf("record %d carries reasoning %q; a stream with none must record none: %+v", i, r.Reasoning, r)
			}
		}
	})

	t.Run("records_keep_their_pinned_shape", func(t *testing.T) {
		// The delta stream is display-only: the turn persists exactly what
		// it persisted before 022 — the user record and ONE assistant
		// record carrying the round's full text and reasoning (005's
		// pinned shape), never one record per delta.
		rounds := [][]llm.Chunk{
			{
				{Reasoning: "first thought. "},
				{Reasoning: "second thought."},
				{Text: "The answer"},
				{Text: " is five."},
				{Finish: "stop"},
			},
		}
		l, fx, _ := newTestLoop(t, rounds, LoopConfig{})

		out := make(chan Event, 64)
		if err := l.Send(context.Background(), fx.csID, "records unchanged by the delta stream", out); err != nil {
			t.Fatalf("Send: %v", err)
		}
		drain(out)

		sess, err := fx.store.Get(fx.csID)
		if err != nil {
			t.Fatalf("Get session: %v", err)
		}
		if len(sess.Records) != 2 {
			t.Fatalf("got %d records, want 2 (user, assistant): %+v", len(sess.Records), sess.Records)
		}
		if sess.Records[0].Role != "user" {
			t.Errorf("record 0 Role = %q, want user", sess.Records[0].Role)
		}
		asst := sess.Records[1]
		if asst.Role != "assistant" {
			t.Fatalf("record 1 Role = %q, want assistant: %+v", asst.Role, asst)
		}
		if asst.Content != "The answer is five." {
			t.Errorf("assistant Content = %q, want the round's full text", asst.Content)
		}
		if asst.Reasoning != "first thought. second thought." {
			t.Errorf("assistant Reasoning = %q, want the round's full thinking, joined from the same chunks the deltas streamed", asst.Reasoning)
		}
	})
}

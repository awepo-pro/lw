// streamcut_test.go pins the CLI's half of 035's stream-integrity
// contract: runAgentTurn marks a retried round without letting the cut
// round's partial text run into the retry, and agentErrorHint gives a
// doubly-cut turn its own sentence. Scripted fake agents only — never a
// real provider.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/llm"
)

// TestRunAgentTurnRetryMarker is frozen block 5: on RetryEv the marker
// lands on its own line (a newline is written only when the partial text
// did not already end on one), a round with no text written writes
// nothing, and a retry after dispatched tool events leaves the round
// separator rule untouched.
func TestRunAgentTurnRetryMarker(t *testing.T) {
	toolCall := agent.ToolCallEv{ID: "1", Name: "stage.ingest_source", Args: "{}"}
	toolRes := agent.ToolResEv{ID: "1", Name: "stage.ingest_source", Content: "ok"}

	cases := []struct {
		name   string
		script []any
		want   string
	}{
		{"partial_text_gets_a_newline", []any{"partial", agent.RetryEv{Round: 1, Attempt: 2}, "full"},
			"partial\n[stream cut by the provider — retrying]\nfull"},
		{"text_already_ends_on_newline", []any{"line\n", agent.RetryEv{Round: 1, Attempt: 2}, "full"},
			"line\n[stream cut by the provider — retrying]\nfull"},
		{"no_text_writes_nothing", []any{agent.RetryEv{Round: 1, Attempt: 2}, "full"},
			"full"},
		{"after_tool_events_round_rule_stands", []any{"a", toolCall, toolRes, agent.RetryEv{Round: 2, Attempt: 2}, "b"},
			"a\n\nb"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ag := newScriptedEventAgent(c.script...)
			var buf strings.Builder
			if err := runAgentTurn(context.Background(), ag, "sess-1", "go", &buf); err != nil {
				t.Fatalf("runAgentTurn: %v", err)
			}
			if got := buf.String(); got != c.want {
				t.Errorf("runAgentTurn() output = %q, want %q", got, c.want)
			}
		})
	}
}

// TestAgentErrorHintStreamTruncated is frozen block 6: a Send error
// wrapping llm.ErrStreamTruncated (the round was cut twice) names the
// retry in its own sentence, adds the rejection sentence for ingest only,
// and stays an llm.ErrStreamTruncated through the wrap.
func TestAgentErrorHintStreamTruncated(t *testing.T) {
	sendErr := fmt.Errorf("round 1: %w", llm.ErrStreamTruncated)

	got := agentErrorHint(sendErr, 8192, false)
	want := "agent turn: round 1: llm: provider stream ended early; the round was retried once"
	if got.Error() != want {
		t.Errorf("agentErrorHint() =\n\t%q\nwant\n\t%q", got, want)
	}
	if !errors.Is(got, llm.ErrStreamTruncated) {
		t.Errorf("agentErrorHint() lost llm.ErrStreamTruncated through the wrap")
	}

	got = agentErrorHint(sendErr, 8192, true)
	want = "agent turn: round 1: llm: provider stream ended early; the round was retried once; the changeset was rejected"
	if got.Error() != want {
		t.Errorf("agentErrorHint(rejected) =\n\t%q\nwant\n\t%q", got, want)
	}
	if !errors.Is(got, llm.ErrStreamTruncated) {
		t.Errorf("agentErrorHint(rejected) lost llm.ErrStreamTruncated through the wrap")
	}
}

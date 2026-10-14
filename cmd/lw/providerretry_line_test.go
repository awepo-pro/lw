package main

// providerretry_line_test.go pins what `lw ingest` shows while the agent loop
// waits out a provider refusal (061 D5): the RetryEv's own reason, on a line
// of its own. 035's "stream cut" marker is a different event with different
// words and is pinned by streamcut_test.go; this file only adds the other.

import (
	"context"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
)

// TestRunAgentTurnProviderRetryLine: a retry the loop makes because the
// provider refused the round is a wait of seconds, and silence for that long
// reads as a hang — so the reason is printed, in brackets, on its own line,
// whether or not the abandoned attempt had streamed any text.
func TestRunAgentTurnProviderRetryLine(t *testing.T) {
	const reason = "provider 429: slow down; retrying in 3s"
	toolCall := agent.ToolCallEv{ID: "1", Name: "stage.ingest_source", Args: "{}"}
	toolRes := agent.ToolResEv{ID: "1", Name: "stage.ingest_source", Content: "ok"}
	retry := agent.RetryEv{Round: 1, Attempt: 1, Reason: reason}

	cases := []struct {
		name   string
		script []any
		want   string
	}{
		{"no_text_before", []any{retry, "full"},
			"[" + reason + "]\nfull"},
		{"partial_text_gets_a_newline", []any{"partial", retry, "full"},
			"partial\n[" + reason + "]\nfull"},
		{"text_already_ends_on_newline", []any{"line\n", retry, "full"},
			"line\n[" + reason + "]\nfull"},
		{"after_a_finished_round", []any{"a", toolCall, toolRes, retry, "b"},
			"a\n[" + reason + "]\nb"},
		{"reason_is_one_line", []any{agent.RetryEv{Round: 1, Attempt: 1, Reason: "provider 500: two\nlines  here; retrying in 2s"}, "x"},
			"[provider 500: two lines here; retrying in 2s]\nx"},
		{"two_retries_two_lines", []any{retry, agent.RetryEv{Round: 1, Attempt: 2, Reason: "provider 429: slow down; retrying in 4s"}, "ok"},
			"[" + reason + "]\n[provider 429: slow down; retrying in 4s]\nok"},
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

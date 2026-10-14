package main

// providerretry_line_test.go pins what `lw ingest` shows while the agent loop
// waits out a provider refusal (061 D5): the RetryEv's own reason, on a line
// of its own. 035's "stream cut" marker is a different event with different
// words and is pinned by streamcut_test.go; this file only adds the other.
//
// FIX2 R8 moved the notice from stdout to STDERR: stdout is the turn's answer
// and a script that pipes it (`lw ingest … > log`) should not find "[provider
// 429; retrying in 3s]" between two halves of a sentence. The abandoned
// attempt's partial text is still disowned on stdout — it ends on a newline, so
// the retry's text starts on a line of its own — only the words moved.

import (
	"context"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
)

// TestRunAgentTurnProviderRetryLine: a retry the loop makes because the
// provider refused the round is a wait of seconds, and silence for that long
// reads as a hang — so the reason is printed, in brackets, on its own line,
// whether or not the abandoned attempt had streamed any text. FIX2 R8
// amendment: it was asserted on the one writer (stdout); it is now asserted on
// two — the notice on the notices stream, stdout carrying only the turn's text.
func TestRunAgentTurnProviderRetryLine(t *testing.T) {
	const reason = "provider 429: slow down; retrying in 3s"
	toolCall := agent.ToolCallEv{ID: "1", Name: "stage.ingest_source", Args: "{}"}
	toolRes := agent.ToolResEv{ID: "1", Name: "stage.ingest_source", Content: "ok"}
	retry := agent.RetryEv{Round: 1, Attempt: 1, Reason: reason}

	cases := []struct {
		name       string
		script     []any
		wantStdout string
		wantNotice string
	}{
		// was: "[" + reason + "]\nfull" on stdout
		{"no_text_before", []any{retry, "full"},
			"full", "[" + reason + "]\n"},
		// was: "partial\n[" + reason + "]\nfull"
		{"partial_text_gets_a_newline", []any{"partial", retry, "full"},
			"partial\nfull", "[" + reason + "]\n"},
		// was: "line\n[" + reason + "]\nfull"
		{"text_already_ends_on_newline", []any{"line\n", retry, "full"},
			"line\nfull", "[" + reason + "]\n"},
		// was: "a\n[" + reason + "]\nb". Stdout is untouched by the notice (the
		// round-separating blank line stands); stderr starts its line first,
		// because the terminal cursor is still at the end of "a".
		{"after_a_finished_round", []any{"a", toolCall, toolRes, retry, "b"},
			"a\n\nb", "\n[" + reason + "]\n"},
		// was: "[provider 500: two lines here; retrying in 2s]\nx"
		{"reason_is_one_line", []any{agent.RetryEv{Round: 1, Attempt: 1, Reason: "provider 500: two\nlines  here; retrying in 2s"}, "x"},
			"x", "[provider 500: two lines here; retrying in 2s]\n"},
		// was: both notices on stdout between the text
		{"two_retries_two_lines", []any{retry, agent.RetryEv{Round: 1, Attempt: 2, Reason: "provider 429: slow down; retrying in 4s"}, "ok"},
			"ok", "[" + reason + "]\n[provider 429: slow down; retrying in 4s]\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ag := newScriptedEventAgent(c.script...)
			var out, notices strings.Builder
			if err := runAgentTurnTo(context.Background(), ag, "sess-1", "go", &out, &notices); err != nil {
				t.Fatalf("runAgentTurnTo: %v", err)
			}
			if got := out.String(); got != c.wantStdout {
				t.Errorf("stdout = %q, want %q", got, c.wantStdout)
			}
			if got := notices.String(); got != c.wantNotice {
				t.Errorf("notices = %q, want %q", got, c.wantNotice)
			}
		})
	}
}

// TestRunAgentTurnNoticesGoToStderr: runAgentTurn itself sends the notice to
// the process's stderr and nothing of it to the writer it was given — while
// 035's "stream cut" marker stays on that writer, where its own tests
// (streamcut_test.go, untouched) pin it.
func TestRunAgentTurnNoticesGoToStderr(t *testing.T) {
	retry := agent.RetryEv{Round: 1, Attempt: 1, Reason: "provider 503: busy; retrying in 2s"}
	cut := agent.RetryEv{Round: 1, Attempt: 2, Reason: "stream ended early"}

	var out strings.Builder
	_, stderr, _ := captureRun(t, func() int {
		ag := newScriptedEventAgent("partial", retry, "full", cut, "more")
		if err := runAgentTurn(context.Background(), ag, "sess-1", "go", &out); err != nil {
			t.Errorf("runAgentTurn: %v", err)
			return 1
		}
		return 0
	})
	if stderr != "[provider 503: busy; retrying in 2s]\n" {
		t.Errorf("stderr = %q, want exactly the provider notice", stderr)
	}
	if strings.Contains(out.String(), "provider 503") {
		t.Errorf("stdout carries the provider notice: %q", out.String())
	}
	if !strings.Contains(out.String(), retryMarker) {
		t.Errorf("stdout = %q, want 035's stream-cut marker where it has always been", out.String())
	}
}

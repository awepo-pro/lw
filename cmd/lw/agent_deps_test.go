package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// captureExtractor swaps newIngestAgent for a spy that records the
// extract.Extractor handed to it, so a test can assert on the chain
// newAgent passes through without any LLM, engine or network at all.
func captureExtractor(t *testing.T) *extract.Extractor {
	t.Helper()
	got := new(extract.Extractor)
	orig := newIngestAgent
	newIngestAgent = func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		*got = ex
		return nil, nil
	}
	t.Cleanup(func() { newIngestAgent = orig })
	return got
}

// writtenSource writes content to a fresh temp file named name and returns
// its path — the shape `lw ingest` takes as a local source argument.
func writtenSource(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestAgentExtractors pins U7: every agent the CLI constructs — newAgent
// for query, lint --fix and the TUI, mcpDeps for the MCP server — hands
// its tools the same agentExtractors chain, which handles remote HTML and
// local files alike. Before 008, newAgent passed a bare extract.NewFile(),
// so a model told to read a saved .html page had no extractor that could.
func TestAgentExtractors(t *testing.T) {
	t.Run("new_agent_handles_local_html", func(t *testing.T) {
		got := captureExtractor(t)
		if _, err := newAgent(nil, nil, nil); err != nil {
			t.Fatalf("newAgent: %v", err)
		}
		ex := *got // read only after newAgent ran
		if ex == nil {
			t.Fatal("newAgent passed no extractor to newIngestAgent")
		}
		if !ex.CanHandle(writtenSource(t, "page.html", "<html><body>x</body></html>")) {
			t.Error("newAgent's extractor cannot handle a local .html file")
		}
	})

	t.Run("markdown_still_handled", func(t *testing.T) {
		got := captureExtractor(t)
		if _, err := newAgent(nil, nil, nil); err != nil {
			t.Fatalf("newAgent: %v", err)
		}
		ex := *got // read only after newAgent ran
		if ex == nil {
			t.Fatal("newAgent passed no extractor to newIngestAgent")
		}
		if !ex.CanHandle(writtenSource(t, "note.md", "# Note\n")) {
			t.Error("newAgent's extractor cannot handle a local .md file")
		}
	})

	t.Run("mcp_deps_use_the_same_chain", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		deps := mcpDeps(openEngine(t, root))
		if deps.Extract == nil {
			t.Fatal("mcpDeps' Extract is nil")
		}
		if !deps.Extract.CanHandle("https://example.org/page.html") {
			t.Error("mcpDeps' extractor cannot handle an HTML URL")
		}
	})
}

// truncatedSendErr builds the error a truncated Send returns, wrapped
// exactly the way internal/agent's loop wraps it (008 contract §1).
func truncatedSendErr() error {
	return fmt.Errorf("%w (finish_reason %q in round %d)", agent.ErrTruncated, "length", 3)
}

// TestAgentErrorHint pins the one shared helper that turns a failed agent
// turn into the error ingest, query and lint --fix return (008 contract
// §6, C-808): a truncated turn names the output budget and the fix, the
// ingest form alone adds the rejection sentence, and any other error keeps
// the pre-008 "agent turn: ..." wording untouched.
func TestAgentErrorHint(t *testing.T) {
	const raiseTail = ". Raise it with: lw config set llm.max_tokens 32768"

	t.Run("ingest_hint_names_rejection_and_budget", func(t *testing.T) {
		got := agentErrorHint(truncatedSendErr(), 8192, true)
		want := `agent turn: agent: the model stopped before finishing its turn (finish_reason "length" in round 3)` +
			`: the output limit (llm.max_tokens = 8192) was reached before the agent finished; the changeset was rejected` +
			raiseTail
		if got.Error() != want {
			t.Errorf("agentErrorHint() =\n\t%q\nwant\n\t%q", got, want)
		}
	})

	t.Run("query_hint_omits_rejection", func(t *testing.T) {
		got := agentErrorHint(truncatedSendErr(), 8192, false)
		wantSuffix := ": the output limit (llm.max_tokens = 8192) was reached before the agent finished" + raiseTail
		if !strings.HasSuffix(got.Error(), wantSuffix) {
			t.Errorf("agentErrorHint() = %q, want it to end with %q", got, wantSuffix)
		}
		if strings.Contains(got.Error(), "rejected") {
			t.Errorf("agentErrorHint() = %q, want no rejection sentence outside ingest", got)
		}
	})

	t.Run("non_truncation_errors_unchanged", func(t *testing.T) {
		got := agentErrorHint(errors.New("boom"), 8192, true)
		if got.Error() != "agent turn: boom" {
			t.Errorf("agentErrorHint() = %q, want the pre-008 %q", got, "agent turn: boom")
		}
		if strings.Contains(got.Error(), "output limit") {
			t.Errorf("agentErrorHint() = %q, want no output-limit clause for a non-truncation error", got)
		}
	})
}

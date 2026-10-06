package main

import (
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

// The 054 S1-a frozen tests: the changeset summary a verb prints after its
// turn says "joined" when the verb joined an open changeset and "opened" only
// when it opened one. Before 054 stderr said "joined open changeset …" while
// stdout, a line later, still said "opened changeset …" — the two lines of
// one run contradicted each other.

// summaryFirstLine returns the "<opened|joined> changeset …" line of the
// summary in stdout — the one line 054 pins byte for byte — or "" when
// stdout holds none.
func summaryFirstLine(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "opened changeset ") || strings.HasPrefix(line, "joined changeset ") {
			return line
		}
	}
	return ""
}

// wantSummaryFirstLine is the exact first line the summary must print for the
// changeset the vault holds after the run: verb is "opened" or "joined".
func wantSummaryFirstLine(t *testing.T, root, verb string) string {
	t.Helper()
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	return fmt.Sprintf("%s changeset %s: %s (%d op(s))", verb, cs.ID, cs.Intent, len(cs.Live()))
}

func TestIngestSummaryJoined(t *testing.T) {
	run1 := func(t *testing.T, root string) (stdout string) {
		t.Helper()
		src := writtenSource(t, "note.md", "# Note\n\nBody.\n")
		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return newToolCallingFakeAgent(e, cfg, sessions, ex), nil
		})
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, src})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		return stdout
	}

	t.Run("joined", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		stagePreExistingPatch(t, root)

		stdout := run1(t, root)

		want := wantSummaryFirstLine(t, root, "joined")
		if got := summaryFirstLine(stdout); got != want {
			t.Errorf("summary first line = %q, want %q\nstdout:\n%s", got, want, stdout)
		}
		if strings.Contains(stdout, "opened changeset") {
			t.Errorf("a joined run's stdout says \"opened changeset\":\n%s", stdout)
		}
	})

	t.Run("opened", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")

		stdout := run1(t, root)

		want := wantSummaryFirstLine(t, root, "opened")
		if got := summaryFirstLine(stdout); got != want {
			t.Errorf("summary first line = %q, want %q\nstdout:\n%s", got, want, stdout)
		}
		if strings.Contains(stdout, "joined changeset") {
			t.Errorf("an opening run's stdout says \"joined changeset\":\n%s", stdout)
		}
	})
}

func TestLintFixSummaryJoined(t *testing.T) {
	// One fixable finding in exactly one bucket (lint_fix_join_test.go's
	// fixture), so the run is a single round.
	vault := func(t *testing.T) string {
		t.Helper()
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		const slug = "aaa-join"
		writeBrokenPage(t, root, slug)
		idx := filepath.Join(root, "index.md")
		b, err := os.ReadFile(idx)
		if err != nil {
			t.Fatalf("read index.md: %v", err)
		}
		line := fmt.Sprintf("- [[%s]] — deliberately mis-dated fixture page.\n", slug)
		if err := os.WriteFile(idx, append(b, []byte(line)...), 0o644); err != nil {
			t.Fatalf("append index.md: %v", err)
		}
		return root
	}
	fix := func(t *testing.T, root string) (stdout string) {
		t.Helper()
		withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
			return &roundRecordingAgent{e: e, sessions: sessions}, nil
		})
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"lint", "--fix", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		return stdout
	}

	t.Run("joined", func(t *testing.T) {
		root := vault(t)
		stagePreExistingPatch(t, root)

		stdout := fix(t, root)

		want := wantSummaryFirstLine(t, root, "joined")
		if got := summaryFirstLine(stdout); got != want {
			t.Errorf("summary first line = %q, want %q\nstdout:\n%s", got, want, stdout)
		}
		if strings.Contains(stdout, "opened changeset") {
			t.Errorf("a joined run's stdout says \"opened changeset\":\n%s", stdout)
		}
	})

	t.Run("opened", func(t *testing.T) {
		root := vault(t)

		stdout := fix(t, root)

		want := wantSummaryFirstLine(t, root, "opened")
		if got := summaryFirstLine(stdout); got != want {
			t.Errorf("summary first line = %q, want %q\nstdout:\n%s", got, want, stdout)
		}
		if strings.Contains(stdout, "joined changeset") {
			t.Errorf("an opening run's stdout says \"joined changeset\":\n%s", stdout)
		}
	})
}

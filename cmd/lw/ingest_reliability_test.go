package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// msgRecordingAgent wraps fakeStageAgent and records the user message the
// turn was handed, so tests can assert on the scratch paths cmdIngest
// described to the agent without any LLM involved.
type msgRecordingAgent struct {
	fakeStageAgent
	gotMsg string
}

func (f *msgRecordingAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	f.gotMsg = msg
	return f.fakeStageAgent.Send(ctx, sessionID, msg, out)
}

// withRecordingIngestAgent swaps newIngestAgent for a msgRecordingAgent
// that stages ops (or fails with failWith) through the real engine
// cmdIngest hands the seam, and returns the recorder.
func withRecordingIngestAgent(t *testing.T, ops []stage.Op, failWith error) *msgRecordingAgent {
	t.Helper()
	rec := &msgRecordingAgent{}
	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		rec.fakeStageAgent = fakeStageAgent{e: e, sessions: sessions, ops: ops, failWith: failWith}
		return rec, nil
	})
	return rec
}

// maxTokens8192Env points config.Load at a temp config carrying
// max_tokens = 8192 — the explicitly-set budget the 008 message tests pin
// (never the user's real config: XDG_CONFIG_HOME is always redirected).
// Every cmdIngest test here calls it, message-pinning or not: cmdIngest
// loads the config before swapping in the fake agent, so without the pin
// a test reads the user's real ~/.config/lw/config.toml and inherits
// whatever the ambient environment points it at (00-conventions.md §1.7).
func maxTokens8192Env(t *testing.T) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeConfigFile(t, dir, "[llm]\nmax_tokens = 8192\n")
}

// scratchPaths returns the scratch file paths the agent was told to ingest,
// in buildIngestMessage's order.
func scratchPaths(t *testing.T, rec *msgRecordingAgent) []string {
	t.Helper()
	paths := parseIngestPaths(rec.gotMsg)
	if len(paths) == 0 {
		t.Fatalf("the agent was given no scratch paths; message:\n%s", rec.gotMsg)
	}
	return paths
}

// oneIngestOp is the single ingest_source op the success-path fakes stage,
// so the ingest succeeds and leaves its changeset open for review.
func oneIngestOp() []stage.Op {
	return []stage.Op{{
		Kind:      stage.OpIngestSource,
		Path:      "raw/articles/ingest-reliability-test.md",
		Content:   []byte("ingested body\n"),
		Extractor: "test-fake",
	}}
}

// TestIngestScratchName pins U6's CLI half: the scratch file the agent is
// told to ingest is named after the ORIGINAL source's base name (slugified,
// numbered per source), never after the extracted title (008 contract §6).
func TestIngestScratchName(t *testing.T) {
	t.Run("local_path_uses_source_basename", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		maxTokens8192Env(t) // pin config.Load; the value is inert here
		// A frontmatter title and no H1: a name derived from the Doc would
		// follow the title, so this catches the pre-008 SuggestPath naming.
		src := writtenSource(t, "Quaternion 四元數簡介.md", "---\ntitle: 四元數入門\n---\n\nQuaternion math, briefly.\n")
		rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, src})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		paths := scratchPaths(t, rec)
		if got := filepath.Base(paths[0]); got != "01-quaternion.md" {
			t.Errorf("scratch base name = %q, want %q", got, "01-quaternion.md")
		}
	})

	t.Run("url_uses_last_path_segment", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		maxTokens8192Env(t) // pin config.Load; the value is inert here
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<html><body><h1>GLM 5.2 summary</h1><p>Body.</p></body></html>")
		}))
		defer srv.Close()
		rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, srv.URL + "/posts/glm52-summary.html"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		paths := scratchPaths(t, rec)
		if got := filepath.Base(paths[0]); got != "01-glm52-summary.md" {
			t.Errorf("scratch base name = %q, want %q", got, "01-glm52-summary.md")
		}
	})

	t.Run("empty_slug_falls_back_to_suggest_path", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		maxTokens8192Env(t) // pin config.Load; the value is inert here
		// 四元數 slugifies to nothing under the [a-z0-9] rule, so the name
		// falls back to SuggestPath's base — built from the H1 title.
		src := writtenSource(t, "四元數.md", "# Rotations\n\nBody text.\n")
		rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, src})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		paths := scratchPaths(t, rec)
		if got := filepath.Base(paths[0]); got != "01-rotations.md" {
			t.Errorf("scratch base name = %q, want %q", got, "01-rotations.md")
		}
	})

	t.Run("second_source_gets_02", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		maxTokens8192Env(t) // pin config.Load; the value is inert here
		a := writtenSource(t, "a.md", "# A\n\nBody.\n")
		b := writtenSource(t, "b.md", "# B\n\nBody.\n")
		rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, a, b})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		paths := scratchPaths(t, rec)
		if len(paths) != 2 {
			t.Fatalf("the agent was told about %d scratch paths, want 2", len(paths))
		}
		if got := filepath.Base(paths[0]); got != "01-a.md" {
			t.Errorf("first scratch base name = %q, want %q", got, "01-a.md")
		}
		if got := filepath.Base(paths[1]); got != "02-b.md" {
			t.Errorf("second scratch base name = %q, want %q", got, "02-b.md")
		}
	})
}

// TestIngestTruncatedTurn pins U1's CLI half: a turn the provider cut off
// at the output cap is an error that names the budget and the fix, and the
// changeset it opened is rejected, never left open (008 contract §6).
func TestIngestTruncatedTurn(t *testing.T) {
	const wantErr = `agent turn: agent: the model stopped before finishing its turn (finish_reason "length" in round 3)` +
		`: the output limit (llm.max_tokens = 8192) was reached before the agent finished; the changeset was rejected` +
		". Raise it with: lw config set llm.max_tokens 32768"

	t.Run("error_names_budget_and_rejection", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		src := writtenSource(t, "note.md", "# Note\n\nBody.\n")
		withRecordingIngestAgent(t, nil, truncatedSendErr())

		err := cmdIngest([]string{"--vault", root, src})
		if err == nil {
			t.Fatal("cmdIngest returned nil for a truncated turn")
		}
		if err.Error() != wantErr {
			t.Errorf("cmdIngest error =\n\t%q\nwant\n\t%q", err, wantErr)
		}
	})

	t.Run("changeset_is_rejected", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		src := writtenSource(t, "note.md", "# Note\n\nBody.\n")
		withRecordingIngestAgent(t, nil, truncatedSendErr())

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, src})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
		}

		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()
		if _, err := e.Current(); !errors.Is(err, stage.ErrNoChangeset) {
			t.Fatalf("Current after a truncated turn = %v, want ErrNoChangeset", err)
		}
		if got := countChangesets(t, root, "rejected"); got != 1 {
			t.Errorf("rejected changesets = %d, want exactly 1 (the truncated attempt)", got)
		}
	})
}

// TestIngestNothingProposed pins 008's zero-op invariant: a turn that
// finishes cleanly but stages nothing is not a success — the empty
// changeset is rejected with the exact contract message.
func TestIngestNothingProposed(t *testing.T) {
	t.Run("zero_live_ops_rejects_with_message", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		maxTokens8192Env(t) // pin config.Load; the value is inert here
		src := writtenSource(t, "note.md", "# Note\n\nBody.\n")
		withRecordingIngestAgent(t, nil, nil) // a clean turn that stages nothing

		const want = "agent proposed nothing for this ingest; the changeset was rejected"
		err := cmdIngest([]string{"--vault", root, src})
		if err == nil {
			t.Fatalf("cmdIngest returned nil for a turn that proposed nothing, want %q", want)
		}
		if err.Error() != want {
			t.Errorf("cmdIngest error = %q, want %q", err, want)
		}
		if got := countChangesets(t, root, "rejected"); got != 1 {
			t.Errorf("rejected changesets = %d, want exactly 1 (the empty attempt)", got)
		}
	})
}

// TestIngestRawOnlyWarns pins the raw-only half: an ingest whose agent
// staged only raw sources — no create_page — still succeeds and leaves the
// changeset open, but warns on stdout after the summary so a scripted
// caller sees the changeset proposes no pages.
func TestIngestRawOnlyWarns(t *testing.T) {
	t.Run("warning_line_after_summary_exit_zero", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		maxTokens8192Env(t) // pin config.Load; the value is inert here
		src := writtenSource(t, "note.md", "# Note\n\nBody.\n")
		withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, src})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		const wantLine = "warning: 0 pages proposed — only raw source(s) staged; review before committing\n"
		if !strings.HasSuffix(stdout, wantLine) {
			t.Errorf("stdout does not end with the raw-only warning; got:\n%q", stdout)
		}
		if !strings.Contains(stdout, "opened changeset cs-") {
			t.Errorf("stdout = %q, want the summary line before the warning", stdout)
		}

		// The changeset stays open — the warning must not have rejected it.
		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current after a raw-only ingest = %v, want the changeset left open", err)
		}
		if got := len(cs.Live()); got != 1 {
			t.Errorf("live ops = %d, want the one staged ingest_source", got)
		}
	})
}

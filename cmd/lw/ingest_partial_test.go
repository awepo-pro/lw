package main

// ingest_partial_test.go holds 061's frozen contract for an ingest turn the
// PROVIDER failed after the agent had already staged work (s47: a transient
// OpenRouter error in round 7 rejected the whole changeset and threw away two
// staged pages, 96 s and 157 k input tokens). Such a turn now keeps its own
// ops, marks the changeset partial and says so; every other failure — the
// user's cancel, the output cap, an internal error, a provider failure that
// staged nothing — keeps today's rollback and today's exact texts.
//
// The provider is scripted as a fake agent.Agent that stages through the real
// engine and then fails with the very error chain the real loop returns
// (agent: stream: %w around the typed llm error) — the seam every other
// ingest test here uses.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// partialOps is round 1 of the scripted turn: the raw source and one page
// written from it — two live ops. The page is the shape the fixture vault
// validates (it cites a raw source the fixture already holds).
func partialOps() []stage.Op {
	page := stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/partial-ingest-test.md",
		Content:    []byte("---\ntitle: Partial ingest test\ncreated: 2026-09-09\nupdated: 2026-09-09\ntype: concept\ntags: [inference, decoding]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: high\n---\n\nA page staged before the provider failed, linking [[kv-cache]] and [[flash-attention]].\n\nProvenance: ^[raw/articles/kv-cache-explained.md]\n"),
		Rationale:  "round 1 of a turn the provider fails in round 2",
		Provenance: []string{"^[raw/articles/kv-cache-explained.md]"},
	}
	return append(oneIngestOp(), page)
}

// providerErrAfterStream is what a turn's Send returns when the provider
// failed it: the loop's own wrap around the typed error.
func providerErrAfterStream(status int, msg string) error {
	return fmt.Errorf("agent: stream: %w", &llm.ProviderError{Status: status, Message: msg, InStream: true})
}

// shortSource writes a source file under a SHORT directory: the changeset
// intent is bounded to 200 runes, and a test name's temp directory would eat
// the room the partial mark needs.
func shortSource(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "p")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return writtenSourceIn(t, dir, "note.md", "# Note\n\nBody.\n")
}

func writtenSourceIn(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// stagingThenFailing fakes the ingest agent: it stages ops and then fails the
// turn with failWith — the shape every test below needs.
func stagingThenFailing(t *testing.T, ops []stage.Op, failWith error) {
	t.Helper()
	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		return &stageThenFailAgent{e: e, sessions: sessions, ops: ops, failWith: failWith}, nil
	})
}

// ingestOnce runs cmdIngest with the real dispatch's output capture and
// returns stdout and the verb's error.
func ingestOnce(t *testing.T, root, src string) (stdout string, err error) {
	t.Helper()
	stdout, _, _ = captureRun(t, func() int {
		err = cmdIngest([]string{"--vault", root, src})
		if err != nil {
			return 1
		}
		return 0
	})
	return stdout, err
}

// currentOf returns the open changeset of the vault at root.
func currentOf(t *testing.T, root string) *stage.Changeset {
	t.Helper()
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current = %v, want the changeset still open", err)
	}
	return cs
}

func assertNoOpen(t *testing.T, root string) {
	t.Helper()
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	if _, err := e.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Fatalf("Current = %v, want ErrNoChangeset", err)
	}
}

// TestIngestProviderFailureKeepsOps is the headline: round 1 staged the raw
// source and a page, round 2 the provider failed. Nothing is rejected, the
// changeset is open with both ops live, its intent says it is partial, the
// summary a clean ingest prints is on stdout, and the error (exit 1) names
// what was kept and where to review it.
func TestIngestProviderFailureKeepsOps(t *testing.T) {
	maxTokens8192Env(t)
	root := testutil.CopyFixture(t, "minimal")
	src := shortSource(t)
	sendErr := providerErrAfterStream(502, "Upstream error from Nvidia: overloaded")
	stagingThenFailing(t, partialOps(), sendErr)

	stdout, err := ingestOnce(t, root, src)
	if err == nil {
		t.Fatal("cmdIngest returned nil for a failed turn, want exit 1")
	}

	cs := currentOf(t, root)
	if got := len(cs.Live()); got != 2 {
		t.Fatalf("live ops = %d, want the 2 the turn staged", got)
	}
	// FIX1 A-061-1: the mark is a prefix (it was `ingest <src> [partial: the
	// agent turn failed (provider 502) after staging 2 op(s)]`).
	wantIntent := "[partial: provider 502 after 2 op(s)] ingest " + src
	if cs.Intent != wantIntent {
		t.Errorf("Intent =\n\t%q\nwant\n\t%q", cs.Intent, wantIntent)
	}
	if got := countChangesets(t, root, "open"); got != 1 {
		t.Errorf("open changesets = %d, want 1", got)
	}
	if got := countChangesets(t, root, "rejected"); got != 0 {
		t.Errorf("rejected changesets = %d, want 0 — a provider failure after staging keeps its work", got)
	}

	wantErr := "agent turn: agent: stream: llm: provider error in stream (502): Upstream error from Nvidia: overloaded" +
		"; kept the 2 op(s) this ingest staged in changeset " + cs.ID +
		" (partial: the turn did not finish) — review them in lw tui"
	if err.Error() != wantErr {
		t.Errorf("error =\n\t%q\nwant\n\t%q", err, wantErr)
	}
	for _, part := range []string{"kept the 2 op(s) this ingest staged in changeset cs-", "(partial: the turn did not finish)"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not contain %q", err, part)
		}
	}
	if !errors.As(err, new(*llm.ProviderError)) {
		t.Errorf("error %q lost the *llm.ProviderError from its chain", err)
	}

	// The summary is the one a clean ingest prints, after the turn's own
	// output: the changeset's id, its (now partial) intent and its live ops.
	wantSummary := "opened changeset " + cs.ID + ": " + wantIntent + " (2 op(s))\n"
	if !strings.Contains(stdout, wantSummary) {
		t.Errorf("stdout lacks the changeset summary %q:\n%s", wantSummary, stdout)
	}
	for _, op := range cs.Live() {
		if line := "  " + op.ID + " " + string(op.Kind) + " " + op.Path + "\n"; !strings.Contains(stdout, line) {
			t.Errorf("stdout lacks the op line %q:\n%s", line, stdout)
		}
	}
}

// TestIngestProviderFailureJoinedKeepsOps is the joined shape: the changeset
// already held one op; the ingest staged two more and the provider failed.
// All three are live, nothing was dropped, and the shared changeset is marked.
func TestIngestProviderFailureJoinedKeepsOps(t *testing.T) {
	maxTokens8192Env(t)
	root := testutil.CopyFixture(t, "minimal")
	patchID := stagePreExistingPatch(t, root)
	src := shortSource(t)
	stagingThenFailing(t, partialOps(), providerErrAfterStream(503, "busy"))

	stdout, err := ingestOnce(t, root, src)
	if err == nil {
		t.Fatal("cmdIngest returned nil for a failed turn, want exit 1")
	}

	cs := currentOf(t, root)
	if got := len(cs.Live()); got != 3 {
		t.Fatalf("live ops = %d, want 3 (the pre-existing patch and the 2 the turn staged)", got)
	}
	for _, op := range cs.Ops {
		if op.State == stage.StateDropped || op.State == stage.StateRejected {
			t.Errorf("op %s (%s) is %s, want none dropped or rejected", op.ID, op.Kind, op.State)
		}
	}
	if patch := findOpInChangeset(t, cs, stage.OpPatchPage); patch.ID != patchID {
		t.Errorf("patch op = %s, want the pre-existing %s", patch.ID, patchID)
	}
	if !strings.HasPrefix(cs.Intent, "[partial: provider 503 after 2 op(s)] ") {
		t.Errorf("Intent = %q, want it to start with the partial mark naming the 2 ops THIS ingest staged", cs.Intent)
	}
	if got := countChangesets(t, root, "rejected"); got != 0 {
		t.Errorf("rejected changesets = %d, want 0", got)
	}
	wantTail := "; kept the 2 op(s) this ingest staged in changeset " + cs.ID + " (partial: the turn did not finish) — review them in lw tui"
	if !strings.HasSuffix(err.Error(), wantTail) {
		t.Errorf("error %q does not end with %q", err, wantTail)
	}
	if strings.Contains(err.Error(), "dropped") {
		t.Errorf("error %q mentions a drop, but nothing was dropped", err)
	}
	if want := "joined changeset " + cs.ID + ": "; !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks the joined summary line %q:\n%s", want, stdout)
	}
}

// TestIngestProviderFailureZeroOpsUnchanged: a provider failure BEFORE the
// agent staged anything has nothing to keep, so today's path stands, texts
// byte for byte — a rejected changeset when this ingest opened it, an
// untouched one (and the scoped-drop clause) when it joined.
func TestIngestProviderFailureZeroOpsUnchanged(t *testing.T) {
	sendErr := providerErrAfterStream(502, "overloaded")
	const base = "agent turn: agent: stream: llm: provider error in stream (502): overloaded"

	t.Run("opened_is_rejected", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		src := shortSource(t)
		stagingThenFailing(t, nil, sendErr)

		_, err := ingestOnce(t, root, src)
		if err == nil {
			t.Fatal("cmdIngest returned nil for a failed turn")
		}
		if err.Error() != base {
			t.Errorf("error =\n\t%q\nwant\n\t%q", err, base)
		}
		assertNoOpen(t, root)
		if got := countChangesets(t, root, "rejected"); got != 1 {
			t.Errorf("rejected changesets = %d, want exactly 1 (the failed attempt)", got)
		}
	})

	t.Run("joined_is_untouched", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		patchID := stagePreExistingPatch(t, root)
		src := shortSource(t)
		stagingThenFailing(t, nil, sendErr)

		_, err := ingestOnce(t, root, src)
		if err == nil {
			t.Fatal("cmdIngest returned nil for a failed turn")
		}
		cs := currentOf(t, root)
		want := base + "; its own ops were dropped (the open changeset " + cs.ID + " keeps the rest)"
		if err.Error() != want {
			t.Errorf("error =\n\t%q\nwant\n\t%q", err, want)
		}
		if got := len(cs.Live()); got != 1 || cs.Live()[0].ID != patchID {
			t.Errorf("live ops = %+v, want only the pre-existing %s", cs.Live(), patchID)
		}
		if strings.Contains(cs.Intent, "[partial") {
			t.Errorf("Intent = %q, want no partial mark — this ingest staged nothing", cs.Intent)
		}
		if got := countChangesets(t, root, "rejected"); got != 0 {
			t.Errorf("rejected changesets = %d, want 0", got)
		}
	})
}

// TestIngestCancelStillRollsBack: the user's cancel is not the provider
// failing — whatever was staged is rolled back as it always was (the opened
// changeset rejected; a joined one loses only its own ops).
func TestIngestCancelStillRollsBack(t *testing.T) {
	canceled := fmt.Errorf("agent: %w", context.Canceled)
	const base = "agent turn: agent: context canceled"

	t.Run("opened_is_rejected", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		src := shortSource(t)
		stagingThenFailing(t, partialOps(), canceled)

		_, err := ingestOnce(t, root, src)
		if err == nil || err.Error() != base {
			t.Fatalf("error = %v, want %q", err, base)
		}
		assertNoOpen(t, root)
		if got := countChangesets(t, root, "rejected"); got != 1 {
			t.Errorf("rejected changesets = %d, want 1", got)
		}
	})

	t.Run("joined_drops_its_own", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		patchID := stagePreExistingPatch(t, root)
		src := shortSource(t)
		stagingThenFailing(t, partialOps(), canceled)

		_, err := ingestOnce(t, root, src)
		if err == nil {
			t.Fatal("cmdIngest returned nil for a canceled turn")
		}
		cs := currentOf(t, root)
		want := base + "; its own ops were dropped (the open changeset " + cs.ID + " keeps the rest)"
		if err.Error() != want {
			t.Errorf("error =\n\t%q\nwant\n\t%q", err, want)
		}
		if got := len(cs.Live()); got != 1 || cs.Live()[0].ID != patchID {
			t.Errorf("live ops = %+v, want only the pre-existing %s", cs.Live(), patchID)
		}
		if strings.Contains(cs.Intent, "[partial") {
			t.Errorf("Intent = %q, want no partial mark", cs.Intent)
		}
	})
}

// TestIngestTruncatedOutputStillRollsBack: ErrTruncated is the OUTPUT cap — a
// config problem the user fixes, not a provider failure — so a turn that
// staged work before hitting it still rolls back, with the 008 text.
func TestIngestTruncatedOutputStillRollsBack(t *testing.T) {
	const wantOpened = `agent turn: agent: the model stopped before finishing its turn (finish_reason "length" in round 3)` +
		`: the output limit (llm.max_tokens = 8192) was reached before the agent finished; the changeset was rejected` +
		". Raise it with: lw config set llm.max_tokens 32768"

	t.Run("opened_is_rejected", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		src := shortSource(t)
		stagingThenFailing(t, partialOps(), truncatedSendErr())

		_, err := ingestOnce(t, root, src)
		if err == nil || err.Error() != wantOpened {
			t.Fatalf("error = %v, want\n\t%q", err, wantOpened)
		}
		assertNoOpen(t, root)
		if got := countChangesets(t, root, "rejected"); got != 1 {
			t.Errorf("rejected changesets = %d, want 1", got)
		}
	})

	t.Run("joined_drops_its_own", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		patchID := stagePreExistingPatch(t, root)
		src := shortSource(t)
		stagingThenFailing(t, partialOps(), truncatedSendErr())

		_, err := ingestOnce(t, root, src)
		if err == nil {
			t.Fatal("cmdIngest returned nil for a truncated turn")
		}
		cs := currentOf(t, root)
		want := wantOpened + "; its own ops were dropped (the open changeset " + cs.ID + " keeps the rest)"
		if err.Error() != want {
			t.Errorf("error =\n\t%q\nwant\n\t%q", err, want)
		}
		if got := len(cs.Live()); got != 1 || cs.Live()[0].ID != patchID {
			t.Errorf("live ops = %+v, want only the pre-existing %s", cs.Live(), patchID)
		}
	})
}

// TestIngestPartialCauseForms: the note names the cause in the shortest
// honest words — the provider's code, a stream that ended early, a stall.
func TestIngestPartialCauseForms(t *testing.T) {
	cases := []struct {
		name    string
		sendErr error
		cause   string
	}{
		{"provider_status", providerErrAfterStream(429, "slow down"), "provider 429"},
		{"provider_string_code",
			fmt.Errorf("agent: stream: %w", &llm.ProviderError{Code: "server_error", Message: "gone", InStream: true}),
			"provider server_error"},
		{"provider_no_code",
			fmt.Errorf("agent: stream: %w", &llm.ProviderError{Message: "gone", InStream: true}),
			"provider unknown"},
		{"stream_ended_early", fmt.Errorf("agent: stream: %w: x", llm.ErrStreamTruncated), "stream ended early"},
		{"stalled", fmt.Errorf("agent: stream: %w: no response bytes for 1m0s", llm.ErrStalled), "stalled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			maxTokens8192Env(t)
			root := testutil.CopyFixture(t, "minimal")
			src := shortSource(t)
			stagingThenFailing(t, oneIngestOp(), c.sendErr)

			_, err := ingestOnce(t, root, src)
			if err == nil {
				t.Fatal("cmdIngest returned nil for a failed turn")
			}
			cs := currentOf(t, root)
			want := "[partial: " + c.cause + " after 1 op(s)] "
			if !strings.HasPrefix(cs.Intent, want) {
				t.Errorf("Intent = %q, want it to start with %q", cs.Intent, want)
			}
			if got := len(cs.Live()); got != 1 {
				t.Errorf("live ops = %d, want the 1 the turn staged", got)
			}
			if !strings.Contains(err.Error(), "; kept the 1 op(s) this ingest staged in changeset "+cs.ID) {
				t.Errorf("error %q does not say it kept the 1 op", err)
			}
		})
	}
}

// TestProviderFailureClassifier pins what counts as the provider failing: a
// typed provider error, a twice-cut stream and a stall — and nothing else.
func TestProviderFailureClassifier(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"http_refusal", fmt.Errorf("agent: stream: %w", &llm.ProviderError{Status: 401, Message: "no"}), true},
		{"in_stream_error", providerErrAfterStream(502, "x"), true},
		{"stream_truncated", fmt.Errorf("agent: stream: %w", llm.ErrStreamTruncated), true},
		{"stalled", fmt.Errorf("agent: stream: %w", llm.ErrStalled), true},
		{"output_cap", truncatedSendErr(), false},
		{"canceled", fmt.Errorf("agent: %w", context.Canceled), false},
		{"deadline", fmt.Errorf("agent: %w", context.DeadlineExceeded), false},
		{"internal", errors.New("agent: append user record: disk full"), false},
		{"tool_failure", errors.New("agent: call stage.create_page: boom"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := providerFailure(c.err); got != c.want {
				t.Errorf("providerFailure(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestRecompileProviderFailureKeepsOps: `lw ingest --recompile` shares the
// tail — a recompile turn stages pages, never a raw, and the pages it staged
// before the provider failed are kept the same way.
func TestRecompileProviderFailureKeepsOps(t *testing.T) {
	root, _ := rcVault(t)
	stagingThenFailing(t, []stage.Op{rcPageOp(rcRawA)}, providerErrAfterStream(502, "overloaded"))

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", rcRawA)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q stdout=%q", code, stderr, stdout)
	}
	cs := rcOpenChangeset(t, root)
	if got := rcCount(cs, stage.OpCreatePage); got != 1 || len(cs.Live()) != 1 {
		t.Errorf("live ops = %+v, want exactly the 1 create_page the turn staged", cs.Live())
	}
	if want := "[partial: provider 502 after 1 op(s)] "; !strings.HasPrefix(cs.Intent, want) {
		t.Errorf("Intent = %q, want it to start with %q", cs.Intent, want)
	}
	if want := "; kept the 1 op(s) this ingest staged in changeset " + cs.ID + " (partial: the turn did not finish) — review them in lw tui"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	if got := countChangesets(t, root, "rejected"); got != 0 {
		t.Errorf("rejected changesets = %d, want 0", got)
	}
}

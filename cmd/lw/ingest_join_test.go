package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// The 019 T1 frozen tests: `lw ingest` JOINS an already-open changeset
// instead of refusing, prints a stderr notice saying so before the agent
// turn, and — failing — rolls back ONLY its own ops (Engine.DropOps, never
// Reject), so the other work in the changeset survives for review.

// stagePreExistingPatch gives the vault at root one live patch_page op in an
// open changeset — the "other work" every joined-verb test below must see
// survive. It is the internal/stage helpers_test.go stageKVCachePatch shape,
// rebuilt here on the exported API because cmd/lw cannot read that
// package's test helpers.
func stagePreExistingPatch(t *testing.T, root string) string {
	t.Helper()
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	if _, err := e.OpenChangeset("019 join test: pre-existing work", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	const oldLine = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newLine := "- [[flash-attention]] — a kernel design that reduces the memory cost"
	if !strings.Contains(page.Body, oldLine) {
		t.Fatalf("fixture body does not contain the hunk's old line:\n%s", page.Body)
	}
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)
	opID, err := e.Append(stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      page.Path,
		Section:   "## Related",
		Before:    page.SHA256(),
		Content:   rewritten.Serialize(),
		Rationale: "019 join test: pre-existing work the joined verb must keep",
		Hunks: []stage.Hunk{{
			ID:   "h1",
			Path: page.Path,
			Del:  []string{oldLine},
			Add:  []string{newLine},
		}},
	})
	if err != nil {
		t.Fatalf("Append patch_page: %v", err)
	}
	return opID
}

// stageThenFailAgent stages its ops through the real engine (the
// fakeStageAgent shape) and THEN fails the turn — the joined-failure shape:
// the verb staged work before the turn went wrong, and the scoped rollback
// must take exactly that work back out while the changeset stays open.
type stageThenFailAgent struct {
	e        *stage.Engine
	sessions agent.SessionStore
	ops      []stage.Op
	failWith error
}

func (f *stageThenFailAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	defer close(out)
	for _, op := range f.ops {
		if _, err := f.e.Append(op); err != nil {
			sendErrEv(ctx, out, err)
			return err
		}
	}
	sendErrEv(ctx, out, f.failWith)
	return f.failWith
}

func (f *stageThenFailAgent) Sessions() agent.SessionStore { return f.sessions }

// findOpInChangeset returns the op of kind kind, failing the test when
// there is none.
func findOpInChangeset(t *testing.T, cs *stage.Changeset, kind stage.OpKind) stage.Op {
	t.Helper()
	for _, op := range cs.Ops {
		if op.Kind == kind {
			return op
		}
	}
	t.Fatalf("changeset %s has no %s op; ops=%+v", cs.ID, kind, cs.Ops)
	return stage.Op{}
}

// raceSessionStore simulates the Get-then-Create race verbSession must
// survive: the first Get misses (the changeset has no session yet), Create
// loses to another process's Create, and the second Get finds the session
// that process made. Create's error is the plain-text one
// fileSessions.Create produces — errSessionExists is deliberately
// unexported (00-conventions §2) — which is why the fallback retries Get
// on any Create failure rather than errors.Is-ing a sentinel.
type raceSessionStore struct {
	gets    int
	creates int
	sess    *agent.Session
}

func (s *raceSessionStore) Get(id string) (*agent.Session, error) {
	s.gets++
	if s.gets == 1 {
		return nil, fmt.Errorf("agent: get session %s: not found", id)
	}
	return s.sess, nil
}

func (s *raceSessionStore) Create(id string) (*agent.Session, error) {
	s.creates++
	return nil, fmt.Errorf("agent: session already exists: %s", id)
}

func (s *raceSessionStore) Append(id string, r agent.Record) error { return nil }
func (s *raceSessionStore) Close(id string) error                  { return nil }
func (s *raceSessionStore) List() ([]string, error)                { return nil, nil }

// TestVerbSessionJoinedNeverFailsOnErrSessionExists pins 019's frozen
// session clause for the race window: a joined verb whose Create loses to
// a concurrent Create falls back to the session that beat it, instead of
// failing. An opened-here verb has no such fallback — its Create error is
// real and propagates.
func TestVerbSessionJoinedNeverFailsOnErrSessionExists(t *testing.T) {
	want := &agent.Session{ID: "cs-race", ChangesetID: "cs-race"}

	joined := &raceSessionStore{sess: want}
	s, err := verbSession(joined, "cs-race", true)
	if err != nil {
		t.Fatalf("joined verbSession = %v, want the winner's session %s (the Get-then-Create race)", err, want.ID)
	}
	if s == nil || s.ID != want.ID {
		t.Fatalf("joined verbSession session = %+v, want %s", s, want.ID)
	}
	if joined.gets != 2 || joined.creates != 1 {
		t.Fatalf("gets=%d creates=%d, want the shape Get-miss → Create-loses → Get-hit (2/1)", joined.gets, joined.creates)
	}

	// Opened here: Create's failure is real — no retry can conjure a
	// session for a changeset this process just opened.
	opened := &raceSessionStore{sess: want}
	if s, err := verbSession(opened, "cs-race", false); err == nil {
		t.Fatalf("opened verbSession = %+v, want the Create error propagated", s)
	}
	if opened.gets != 0 || opened.creates != 1 {
		t.Fatalf("opened-here gets=%d creates=%d, want 0/1 (Create alone, no Get fallback)", opened.gets, opened.creates)
	}
}

func TestIngestJoinsOpenChangeset(t *testing.T) {
	t.Run("joins_and_appends", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		stagePreExistingPatch(t, root)
		src := writtenSource(t, "note.md", "# Note\n\nBody.\n")

		// The fake calls stage.ingest_source with the scratch path the
		// ingest message names (C-817) — the real tool handler, no LLM.
		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return newToolCallingFakeAgent(e, cfg, sessions, ex), nil
		})

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, src})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if !strings.Contains(stderr, "joined open changeset cs-") {
			t.Errorf("stderr = %q, want the joined notice", stderr)
		}
		if !strings.Contains(stderr, "(1 op(s) already staged; they will be reviewed and committed together)") {
			t.Errorf("stderr = %q, want the notice's live-op count and promise", stderr)
		}

		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		live := cs.Live()
		if len(live) != 2 {
			t.Fatalf("the changeset holds %d live op(s), want 2 (the pre-existing patch and the new ingest)", len(live))
		}
		patch := findOpInChangeset(t, cs, stage.OpPatchPage)
		ing := findOpInChangeset(t, cs, stage.OpIngestSource)
		if patch.State != stage.StateProposed || ing.State != stage.StateProposed {
			t.Errorf("states = %s/%s, want both proposed", patch.State, ing.State)
		}
		if got := countChangesets(t, root, "open"); got != 1 {
			t.Errorf("open changesets = %d, want exactly 1 (the joined one)", got)
		}
	})

	t.Run("joined_nothing_proposed_keeps_rest", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		patchID := stagePreExistingPatch(t, root)
		src := writtenSource(t, "note.md", "# Note\n\nBody.\n")

		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return &fakeStageAgent{e: e, sessions: sessions}, nil // a clean turn that stages nothing
		})

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, src})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if !strings.Contains(stderr, "agent proposed nothing for this ingest; nothing was added") {
			t.Errorf("stderr = %q, want the joined zero-ops message", stderr)
		}
		if !strings.Contains(stderr, "(the open changeset cs-") || !strings.Contains(stderr, "keeps its 1 op(s))") {
			t.Errorf("stderr = %q, want the kept-op count clause", stderr)
		}

		// The pre-existing patch is still live, and the changeset is still
		// open — never rejected: the joined verb must not destroy the other
		// work in the changeset.
		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current = %v, want the changeset still open", err)
		}
		patch := findOpInChangeset(t, cs, stage.OpPatchPage)
		if patch.ID != patchID || patch.State != stage.StateProposed {
			t.Errorf("patch op = %s (%s), want %s live (proposed)", patch.ID, patch.State, patchID)
		}
		if got := len(cs.Live()); got != 1 {
			t.Errorf("live ops = %d, want exactly the pre-existing patch", got)
		}
		if got := countChangesets(t, root, "open"); got != 1 {
			t.Errorf("open changesets = %d, want 1 (not rejected)", got)
		}
		if got := countChangesets(t, root, "rejected"); got != 0 {
			t.Errorf("rejected changesets = %d, want 0 — a joined verb never Rejects", got)
		}
	})

	t.Run("joined_failure_drops_only_own", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		patchID := stagePreExistingPatch(t, root)
		src := writtenSource(t, "note.md", "# Note\n\nBody.\n")

		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return &stageThenFailAgent{e: e, sessions: sessions, ops: oneIngestOp(), failWith: errBoom}, nil
		})

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, src})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if !strings.Contains(stderr, "; its own ops were dropped (the open changeset cs-") {
			t.Errorf("stderr = %q, want the scoped-drop clause", stderr)
		}
		if !strings.Contains(stderr, "keeps the rest)") {
			t.Errorf("stderr = %q, want the keeps-the-rest clause", stderr)
		}

		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current = %v, want the changeset still open", err)
		}
		ing := findOpInChangeset(t, cs, stage.OpIngestSource)
		if ing.State != stage.StateDropped {
			t.Errorf("the failed turn's ingest op state = %s, want dropped", ing.State)
		}
		patch := findOpInChangeset(t, cs, stage.OpPatchPage)
		if patch.ID != patchID || patch.State != stage.StateProposed {
			t.Errorf("patch op = %s (%s), want %s still live (proposed)", patch.ID, patch.State, patchID)
		}
		if got := countChangesets(t, root, "open"); got != 1 {
			t.Errorf("open changesets = %d, want 1 (not rejected)", got)
		}
		if got := countChangesets(t, root, "rejected"); got != 0 {
			t.Errorf("rejected changesets = %d, want 0 — a joined verb never Rejects", got)
		}
	})
}

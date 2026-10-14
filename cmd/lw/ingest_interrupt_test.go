package main

// ingest_interrupt_test.go pins 061 FIX2 R3: Ctrl-C reaches the agent turn.
// cmdIngest ran its turn under context.Background(), so an interrupt killed the
// process mid-turn — no rollback, a half-staged changeset left open. The policy
// is the user's: a PROVIDER error keeps the work (it is the provider's fault),
// a deliberate ABORT rolls it back. The turn's ctx is therefore derived from
// the interrupt (notifyInterrupt), and an interrupt takes the existing cancel
// path: the opened changeset rejected, a joined one losing only its own ops.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// interruptAgent stages ops, then makes the interrupt happen (fire) and waits
// for the ctx it was handed to be canceled by it. If the ctx never cancels the
// agent says so instead of hanging the suite. After the interrupt it fails the
// turn with failWith (nil: the ctx's own error, the shape a canceled Send has).
type interruptAgent struct {
	e        *stage.Engine
	sessions agent.SessionStore
	ops      []stage.Op
	fire     func()
	failWith error

	sawCancel bool
}

func (a *interruptAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	defer close(out)
	for _, op := range a.ops {
		if _, err := a.e.Append(op); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return errors.New("the turn's ctx was already canceled before the interrupt")
	}
	a.fire()
	select {
	case <-ctx.Done():
		a.sawCancel = true
	case <-time.After(5 * time.Second):
		return errors.New("the interrupt never canceled the turn's ctx")
	}
	if a.failWith != nil {
		return a.failWith
	}
	return fmt.Errorf("agent: %w", ctx.Err())
}

func (a *interruptAgent) Sessions() agent.SessionStore { return a.sessions }

// withInterruptAgent installs an interruptAgent as the ingest agent and returns
// it for the test to inspect.
func withInterruptAgent(t *testing.T, ops []stage.Op, fire func(), failWith error) *interruptAgent {
	t.Helper()
	ag := &interruptAgent{ops: ops, fire: fire, failWith: failWith}
	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		ag.e, ag.sessions = e, sessions
		return ag, nil
	})
	return ag
}

// swapInterrupt replaces notifyInterrupt with a cancel the test holds — "the
// signal arrives" is calling it — and returns the pointer to read it through.
func swapInterrupt(t *testing.T) *context.CancelFunc {
	t.Helper()
	var cancel context.CancelFunc = func() {}
	orig := notifyInterrupt
	notifyInterrupt = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, c := context.WithCancel(parent)
		cancel = c
		return ctx, c
	}
	t.Cleanup(func() { notifyInterrupt = orig })
	return &cancel
}

// TestIngestInterruptRollsBack: an interrupt during the turn, after two ops
// were staged, rolls the work back — the same path a canceled turn always took.
func TestIngestInterruptRollsBack(t *testing.T) {
	const wantErr = "agent turn: agent: context canceled"

	t.Run("opened_is_rejected", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		src := shortSource(t)
		cancel := swapInterrupt(t)
		ag := withInterruptAgent(t, partialOps(), func() { (*cancel)() }, nil)

		_, err := ingestOnce(t, root, src)
		if err == nil || err.Error() != wantErr {
			t.Fatalf("error = %v, want %q", err, wantErr)
		}
		if !ag.sawCancel {
			t.Error("the turn's ctx was not the interrupt-derived one")
		}
		assertNoOpen(t, root)
		if got := countChangesets(t, root, "rejected"); got != 1 {
			t.Errorf("rejected changesets = %d, want 1 — a deliberate abort rolls back", got)
		}
	})

	t.Run("joined_drops_its_own", func(t *testing.T) {
		maxTokens8192Env(t)
		root := testutil.CopyFixture(t, "minimal")
		patchID := stagePreExistingPatch(t, root)
		src := shortSource(t)
		cancel := swapInterrupt(t)
		withInterruptAgent(t, partialOps(), func() { (*cancel)() }, nil)

		_, err := ingestOnce(t, root, src)
		if err == nil {
			t.Fatal("cmdIngest returned nil for an interrupted turn")
		}
		cs := currentOf(t, root)
		if want := wantErr + "; its own ops were dropped (the open changeset " + cs.ID + " keeps the rest)"; err.Error() != want {
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

// TestIngestInterruptBeatsProviderError: the user's abort and the provider's
// error can land together; the abort wins. A ProviderError returned from a turn
// whose ctx the user canceled is still a deliberate abort — keeping the ops
// would be the opposite of what was asked.
func TestIngestInterruptBeatsProviderError(t *testing.T) {
	maxTokens8192Env(t)
	root := testutil.CopyFixture(t, "minimal")
	src := shortSource(t)
	cancel := swapInterrupt(t)
	withInterruptAgent(t, partialOps(), func() { (*cancel)() }, providerErrAfterStream(502, "overloaded"))

	_, err := ingestOnce(t, root, src)
	if err == nil || !errors.As(err, new(*llm.ProviderError)) {
		t.Fatalf("error = %v, want the provider error in the chain", err)
	}
	assertNoOpen(t, root)
	if got := countChangesets(t, root, "rejected"); got != 1 {
		t.Errorf("rejected changesets = %d, want 1 — the abort rolls back even when the turn died on a provider error", got)
	}
	if strings.Contains(err.Error(), "kept the") {
		t.Errorf("error %q claims the ops were kept", err)
	}
}

// TestIngestRealSIGINTRollsBack: the same, with a real SIGINT delivered to this
// process while the turn runs and NO seam swapped — the proof that the
// production notifyInterrupt cancels the turn's ctx. The test registers its own
// Notify first, so the signal can never take the test binary down even if the
// verb installed nothing.
func TestIngestRealSIGINTRollsBack(t *testing.T) {
	guard := make(chan os.Signal, 4)
	signal.Notify(guard, os.Interrupt)
	t.Cleanup(func() { signal.Stop(guard) })

	maxTokens8192Env(t)
	root := testutil.CopyFixture(t, "minimal")
	src := shortSource(t)
	ag := withInterruptAgent(t, partialOps(), func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Errorf("kill SIGINT: %v", err)
		}
	}, nil)

	_, err := ingestOnce(t, root, src)
	if err == nil || err.Error() != "agent turn: agent: context canceled" {
		t.Fatalf("error = %v, want the canceled-turn text", err)
	}
	if !ag.sawCancel {
		t.Fatal("a real SIGINT did not cancel the turn's ctx")
	}
	assertNoOpen(t, root)
	if got := countChangesets(t, root, "rejected"); got != 1 {
		t.Errorf("rejected changesets = %d, want 1", got)
	}
}

// TestQueryInterruptCancelsTheTurn: `lw query` runs its turn under the same
// interrupt-derived ctx, so ^C unwinds through its cleanup (a changeset the
// turn opened is rejected) instead of killing the process in the middle of it.
func TestQueryInterruptCancelsTheTurn(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	cancel := swapInterrupt(t)
	var sawCancel bool
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		return &ctxWaitAgent{sessions: sessions, fire: func() { (*cancel)() }, saw: &sawCancel}, nil
	})

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"query", "--vault", root, "what is the kv cache?"})
	})
	if code != 1 || !strings.Contains(stderr, "context canceled") {
		t.Fatalf("exit %d, stderr %q, want exit 1 naming the cancel", code, stderr)
	}
	if !sawCancel {
		t.Error("the query turn's ctx was not the interrupt-derived one")
	}
}

// ctxWaitAgent fires an interrupt and waits for its ctx to notice.
type ctxWaitAgent struct {
	sessions agent.SessionStore
	fire     func()
	saw      *bool
}

func (a *ctxWaitAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	defer close(out)
	a.fire()
	select {
	case <-ctx.Done():
		*a.saw = true
		return fmt.Errorf("agent: %w", ctx.Err())
	case <-time.After(5 * time.Second):
		return errors.New("the interrupt never canceled the turn's ctx")
	}
}

func (a *ctxWaitAgent) Sessions() agent.SessionStore { return a.sessions }

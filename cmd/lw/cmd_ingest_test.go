package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// fakeStageAgent is a minimal agent.Agent for tests: Send proposes a fixed
// list of ops directly through the same *stage.Engine a real tool call
// would use — no LLM, no network, no internal/agent.Loop involved at all —
// then reports exactly one terminal event, per backbone §9's "who owns
// out" contract (C-105): DoneEv on success, or ErrorEv (and the same
// error returned) when failWith is set.
type fakeStageAgent struct {
	e        *stage.Engine
	sessions agent.SessionStore
	ops      []stage.Op
	failWith error
}

func (f *fakeStageAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	defer close(out)

	if f.failWith != nil {
		select {
		case out <- agent.ErrorEv{Err: f.failWith}:
		case <-ctx.Done():
		}
		return f.failWith
	}

	for _, op := range f.ops {
		if _, err := f.e.Append(op); err != nil {
			select {
			case out <- agent.ErrorEv{Err: err}:
			case <-ctx.Done():
			}
			return err
		}
	}

	select {
	case out <- agent.DoneEv{Reason: "stop", Rounds: 1}:
	case <-ctx.Done():
	}
	return nil
}

func (f *fakeStageAgent) Sessions() agent.SessionStore { return f.sessions }

// withFakeAgent swaps the package-level newAgent seam for the duration of
// one test, restoring the original on cleanup.
func withFakeAgent(t *testing.T, fn func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error)) {
	t.Helper()
	orig := newAgent
	newAgent = fn
	t.Cleanup(func() { newAgent = orig })
}

// snapshotVaultFiles returns every wiki/ and raw/ file under root, mapped
// to its exact bytes — used to prove `lw ingest` and `lw lint --fix` touch
// nothing in the working tree, since neither commits.
func snapshotVaultFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, sub := range []string{"wiki", "raw"} {
		dir := filepath.Join(root, sub)
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				t.Fatalf("rel %s: %v", path, rerr)
			}
			out[filepath.ToSlash(rel)] = string(b)
			return nil
		})
	}
	return out
}

func TestCmdIngestFakeAgentLeavesOpenChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	before := snapshotVaultFiles(t, root)

	localSrc := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(localSrc, []byte("# A Local Note\n\nSome body text.\n"), 0o644); err != nil {
		t.Fatalf("write local source: %v", err)
	}

	const wantOps = 1
	var capturedEngine *stage.Engine
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		capturedEngine = e
		return &fakeStageAgent{
			e:        e,
			sessions: sessions,
			ops: []stage.Op{
				{
					Kind:      stage.OpIngestSource,
					Path:      "raw/articles/fake-ingest-test.md",
					Content:   []byte("ingested body\n"),
					Extractor: "test-fake",
				},
			},
		}, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, localSrc})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if capturedEngine == nil {
		t.Fatal("newAgent was never called")
	}
	if !strings.Contains(stdout, "opened changeset cs-") {
		t.Fatalf("stdout = %q, want it to mention an opened changeset", stdout)
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
	if len(cs.Ops) != wantOps {
		t.Fatalf("len(cs.Ops) = %d, want %d", len(cs.Ops), wantOps)
	}
	if cs.Ops[0].Kind != stage.OpIngestSource {
		t.Errorf("cs.Ops[0].Kind = %q, want %q", cs.Ops[0].Kind, stage.OpIngestSource)
	}
	if cs.Ops[0].Path != "raw/articles/fake-ingest-test.md" {
		t.Errorf("cs.Ops[0].Path = %q, want %q", cs.Ops[0].Path, "raw/articles/fake-ingest-test.md")
	}
	if !strings.Contains(stdout, cs.ID) {
		t.Errorf("stdout = %q, want it to contain the changeset id %q", stdout, cs.ID)
	}

	after := snapshotVaultFiles(t, root)
	if len(before) != len(after) {
		t.Fatalf("vault file count changed: before=%d after=%d", len(before), len(after))
	}
	for path, want := range before {
		got, ok := after[path]
		if !ok {
			t.Fatalf("%s disappeared from the vault", path)
		}
		if got != want {
			t.Fatalf("%s changed on disk even though nothing committed", path)
		}
	}
}

func TestCmdIngestNoSources(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root})
	})

	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

func TestCmdIngestExtractErrorOpensNoChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		t.Fatal("newAgent should never be called when extraction fails")
		return nil, nil
	})

	missing := filepath.Join(t.TempDir(), "does-not-exist.md")
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, missing})
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	if _, err := e.Current(); err == nil {
		t.Fatal("a changeset was opened despite extraction failing")
	}
}

func TestCmdIngestAgentErrorStillLeavesPartialChangesetOpen(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	localSrc := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(localSrc, []byte("# Note\n\nBody.\n"), 0o644); err != nil {
		t.Fatalf("write local source: %v", err)
	}

	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		return &fakeStageAgent{e: e, sessions: sessions, failWith: errBoom}, nil
	})

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, localSrc})
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "boom") {
		t.Fatalf("stderr = %q, want it to contain the agent's error", stderr)
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	// The changeset opened before the agent turn failed is still left
	// open — this subtask never rejects or commits on the caller's
	// behalf; that is the review screen's job.
	if _, err := e.Current(); err != nil {
		t.Fatalf("Current: %v, want the changeset opened before the failure to still be open", err)
	}
}

// errBoom is a fixed sentinel so assertions can match its message exactly.
var errBoom = errors.New("boom")

func TestMemSessionStoreIsolated(t *testing.T) {
	s := newMemSessionStore()
	sess, err := s.Create("q1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.ID != "q1" || sess.ChangesetID != "q1" {
		t.Fatalf("Create() session = %+v, want ID/ChangesetID %q", sess, "q1")
	}

	if err := s.Append("q1", agent.Record{Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Get("q1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Records) != 1 || got.Records[0].Content != "hi" {
		t.Fatalf("Get() records = %+v, want one record with Content %q", got.Records, "hi")
	}

	if _, err := s.Create("q1"); err == nil {
		t.Fatal("Create() a second time for the same id should fail")
	}

	ids, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !sort.StringsAreSorted(ids) || len(ids) != 1 || ids[0] != "q1" {
		t.Fatalf("List() = %v, want sorted [%q]", ids, "q1")
	}

	if err := s.Close("q1"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Get("q1"); err == nil {
		t.Fatal("Get() after Close should fail: the session is gone")
	}
}

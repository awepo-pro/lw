package main

// turn_verb_test.go pins the one-word tag each CLI verb puts on its agent
// turn's ctx (trace.WithVerb). Since 039 that tag is no longer only a trace
// label: agent.Send reads it to decide how the turn is run — an "ask" or
// "query" turn is sent the ask prompt and only the read tools, every other verb
// the curator's whole surface. So a call site that stops setting it, or sets it
// wrong, does not lose a trace field: `lw query` silently starts running as a
// curator turn (and 039's structural guard in cmd_query.go, which exists to
// undo exactly that, becomes the only thing standing). The agent package pins
// what each verb does; this pins that each command says the right one.
// Permanent regression test (D-10C).

import (
	"context"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/trace"
)

// verbRecordingAgent delegates to inner and records the verb each Send's ctx
// carried, in call order.
type verbRecordingAgent struct {
	inner agent.Agent

	mu    sync.Mutex
	verbs []string
}

func (v *verbRecordingAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	v.mu.Lock()
	v.verbs = append(v.verbs, trace.VerbFrom(ctx))
	v.mu.Unlock()
	return v.inner.Send(ctx, sessionID, msg, out)
}

func (v *verbRecordingAgent) Sessions() agent.SessionStore { return v.inner.Sessions() }

func (v *verbRecordingAgent) seen() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.verbs...)
}

// wantVerbs fails the test unless got is exactly n turns, all tagged want.
func wantVerbs(t *testing.T, got []string, n int, want string) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("the agent was sent %d turn(s), want %d (verbs %q)", len(got), n, got)
	}
	for i, v := range got {
		if v != want {
			t.Errorf("turn %d carried verb %q, want %q", i+1, v, want)
		}
	}
}

// TestQuerySendCarriesQueryVerb is the 039 review's finding: cmdQuery's Send
// ctx must say "query", the word agent.modeFromVerb turns into the read-only
// ask mode.
func TestQuerySendCarriesQueryVerb(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	var rec *verbRecordingAgent
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		rec = &verbRecordingAgent{inner: &fakeTextAgent{sessions: sessions, reply: "an answer"}}
		return rec, nil
	})
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"query", "--vault", root, "what is the kv cache?"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%q stdout=%q", code, stderr, stdout)
	}
	wantVerbs(t, rec.seen(), 1, "query")
}

// TestLintFixSendCarriesLintVerb: every one of --fix's per-page rounds is a
// curator turn tagged lint, never an ask-mode one — it has to stage repairs.
func TestLintFixSendCarriesLintVerb(t *testing.T) {
	maxTokens8192Env(t)
	root := threeBrokenPagesFixture(t)
	var rec *verbRecordingAgent
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		rec = &verbRecordingAgent{inner: &roundRecordingAgent{e: e, sessions: sessions}}
		return rec, nil
	})
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%q stdout=%q", code, stderr, stdout)
	}
	wantVerbs(t, rec.seen(), 3, "lint")
}

// TestIngestSendCarriesIngestVerb: the ingest turn is tagged ingest.
func TestIngestSendCarriesIngestVerb(t *testing.T) {
	maxTokens8192Env(t)
	root := testutil.CopyFixture(t, "minimal")
	src := writtenSource(t, "verb-source.md", "# Verb source\n\nA body to ingest.\n")
	var rec *verbRecordingAgent
	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		rec = &verbRecordingAgent{inner: &fakeStageAgent{e: e, sessions: sessions, ops: oneIngestOp()}}
		return rec, nil
	})
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, src})
	})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%q stdout=%q", code, stderr, stdout)
	}
	wantVerbs(t, rec.seen(), 1, "ingest")
}

package main

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
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// The 020 T-D frozen tests: `lw lint --fix` groups the report's findings
// by page and drives ONE agent round per page inside the single changeset
// and session the command already opens — built from exactly the pieces
// the ingest tests use (fakeStageAgent, withFakeAgent, maxTokens8192Env).

// lintPathsInVault runs the real lint engine over the vault at root and
// returns the distinct finding paths in report order — the precondition
// helper proving a fixture spans exactly the paths its test claims.
func lintPathsInVault(t *testing.T, root string) []string {
	t.Helper()
	v, err := vault.Open(root)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	ctx := &lint.Context{Vault: v, Index: index.Build(v), Graph: v.Graph()}
	report := lint.Run(ctx, nil)

	seen := map[string]bool{}
	var paths []string
	for _, f := range report.Findings {
		if !seen[f.Path] {
			seen[f.Path] = true
			paths = append(paths, f.Path)
		}
	}
	return paths
}

// writeBrokenPage adds one deliberately mis-dated wiki page to the vault
// at root: created after updated, so the fm-dates check fires on the
// page's own path and nothing else. The page carries the abstract, tag,
// outbound links and index line the other checks want, so it contributes
// findings to exactly one bucket — its own.
func writeBrokenPage(t *testing.T, root, slug string) {
	t.Helper()
	page := fmt.Sprintf(`---
title: %s
created: 2026-09-10
updated: 2026-09-01
type: concept
tags: [inference]
confidence: high
---

# %s

## Abstract

A deliberately mis-dated page. It exists to prove that lint --fix groups
findings per page and drives exactly one agent round per page.

## Related

- [[kv-cache]] — an existing page to link out to.
- [[flash-attention]] — the second outbound link the schema asks for.
`, slug, strings.ReplaceAll(slug, "-", " "))

	if err := os.WriteFile(filepath.Join(root, "wiki", "concepts", slug+".md"), []byte(page), 0o644); err != nil {
		t.Fatalf("write %s: %v", slug, err)
	}
}

// threeBrokenPagesFixture copies the minimal fixture and adds three
// mis-dated pages whose slugs fix their bucket order (aaa < bbb < ccc in
// the Path-sorted report). It fails the test unless the finished fixture
// spans exactly those three finding paths — the precondition every
// per-page round test below leans on.
func threeBrokenPagesFixture(t *testing.T) string {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")

	slugs := []string{"aaa-fix-round", "bbb-fix-round", "ccc-fix-round"}
	var indexAdd strings.Builder
	for _, slug := range slugs {
		writeBrokenPage(t, root, slug)
		fmt.Fprintf(&indexAdd, "- [[%s]] — deliberately mis-dated fixture page.\n", slug)
	}
	idx := filepath.Join(root, "index.md")
	b, err := os.ReadFile(idx)
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}
	if err := os.WriteFile(idx, append(b, []byte(indexAdd.String())...), 0o644); err != nil {
		t.Fatalf("append index.md: %v", err)
	}

	want := []string{
		"wiki/concepts/aaa-fix-round.md",
		"wiki/concepts/bbb-fix-round.md",
		"wiki/concepts/ccc-fix-round.md",
	}
	got := lintPathsInVault(t, root)
	if len(got) != len(want) {
		t.Fatalf("fixture precondition: lint spans %d paths (%v), want exactly %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fixture precondition: finding path %d = %q, want %q", i, got[i], want[i])
		}
	}
	return root
}

// roundRecordingAgent is the msgRecordingAgent pattern extended to the
// per-page loop: it records the session ID and message of EVERY Send, and
// stages one distinct repair op per round through the real engine, so the
// final changeset shows each round's work landing in the same buffer.
// failOn (1-based round number) makes that one round fail the way
// fakeStageAgent.failWith does — an ErrorEv on the channel and the same
// error returned.
type roundRecordingAgent struct {
	e        *stage.Engine
	sessions agent.SessionStore
	failOn   int
	failWith error

	rounds  int
	sessIDs []string
	msgs    []string
}

// sendErrEv reports one terminal ErrorEv unless ctx is already done —
// the fakeStageAgent failure shape, shared by both failure paths below.
func sendErrEv(ctx context.Context, out chan<- agent.Event, err error) {
	select {
	case out <- agent.ErrorEv{Err: err}:
	case <-ctx.Done():
	}
}

func (f *roundRecordingAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	defer close(out)
	f.rounds++
	f.sessIDs = append(f.sessIDs, sessionID)
	f.msgs = append(f.msgs, msg)

	if f.failOn == f.rounds {
		sendErrEv(ctx, out, f.failWith)
		return f.failWith
	}

	op := stage.Op{
		Kind:      stage.OpIngestSource,
		Path:      fmt.Sprintf("raw/articles/lint-fix-round-%d.md", f.rounds),
		Content:   []byte(fmt.Sprintf("repair proposed in round %d\n", f.rounds)),
		Extractor: "test-fake",
	}
	if _, err := f.e.Append(op); err != nil {
		sendErrEv(ctx, out, err)
		return err
	}
	select {
	case out <- agent.DoneEv{Reason: "stop", Rounds: 1}:
	case <-ctx.Done():
	}
	return nil
}

func (f *roundRecordingAgent) Sessions() agent.SessionStore { return f.sessions }

// onceAgent wraps fakeStageAgent for the multi-round --fix loop: it stages
// its ops on the FIRST Send only and answers later rounds with a bare
// DoneEv, so a fake written for the old single-message turn does not stack
// one copy of the same repair op per page round.
type onceAgent struct {
	fakeStageAgent
	staged bool
}

func (o *onceAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	if o.staged {
		defer close(out)
		select {
		case out <- agent.DoneEv{Reason: "stop", Rounds: 1}:
		case <-ctx.Done():
		}
		return nil
	}
	o.staged = true
	return o.fakeStageAgent.Send(ctx, sessionID, msg, out)
}

func TestLintFixGroupsFindingsPerPage(t *testing.T) {
	report := lint.Report{Findings: []lint.Finding{
		{Check: "index-sync", Path: "a.md", Line: 0, Severity: lint.SevError, Message: "a has no index line"},
		{Check: "fm-dates", Path: "a.md", Line: 4, Severity: lint.SevWarn, Message: "created is after updated"},
		{Check: "link-orphan", Path: "b.md", Line: 9, Severity: lint.SevWarn, Message: "b is orphaned"},
		{Check: "log-rotate", Path: "", Line: 0, Severity: lint.SevInfo, Message: "505 entries exceeds the threshold"},
		{Check: "fm-dates", Path: "c.md", Line: 2, Severity: lint.SevWarn, Message: "created is after updated"},
	}}

	msgs := lintFixRoundMessages(report)
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4 (one per distinct path, first appearance):\n%q",
			len(msgs), msgs)
	}

	// Each round carries exactly its own bucket's findings: the per-finding
	// lines its page owns, the vault-level wording for the empty-path
	// round, and never another page's findings.
	cases := []struct {
		round    int
		contains []string
		mustNot  []string
	}{
		{1, []string{"a.md:0:", "a.md:4:", "(index-sync)", "(fm-dates)"}, []string{"b.md", "c.md"}},
		{2, []string{"b.md:9:", "(link-orphan)"}, []string{"a.md", "c.md"}},
		{3, []string{"vault-level", "(log-rotate)"}, []string{"a.md", "b.md", "c.md"}},
		{4, []string{"c.md:2:", "(fm-dates)"}, []string{"a.md", "b.md"}},
	}
	for _, tc := range cases {
		for _, want := range tc.contains {
			if !strings.Contains(msgs[tc.round-1], want) {
				t.Errorf("round %d is missing %q:\n%s", tc.round, want, msgs[tc.round-1])
			}
		}
		for _, no := range tc.mustNot {
			if strings.Contains(msgs[tc.round-1], no) {
				t.Errorf("round %d names %q:\n%s", tc.round, no, msgs[tc.round-1])
			}
		}
	}

	// Only the final round asks for stage.close.
	for i := 0; i < 3; i++ {
		if strings.Contains(msgs[i], "stage.close") {
			t.Errorf("round %d asks for stage.close; only the final round may:\n%s", i+1, msgs[i])
		}
	}
	if !strings.Contains(msgs[3], "stage.close") {
		t.Errorf("final round does not ask for stage.close:\n%s", msgs[3])
	}
}

func TestLintFixDrivesOneRoundPerPage(t *testing.T) {
	maxTokens8192Env(t)
	root := threeBrokenPagesFixture(t)

	var rec *roundRecordingAgent
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		rec = &roundRecordingAgent{e: e, sessions: sessions}
		return rec, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if len(rec.msgs) != 3 {
		t.Fatalf("the agent received %d messages, want 3 (one per page); got:\n%q",
			len(rec.msgs), rec.msgs)
	}

	// ONE session: every round rode the same session ID.
	if rec.sessIDs[0] == "" {
		t.Error("session ID is empty")
	}
	for i, id := range rec.sessIDs[1:] {
		if id != rec.sessIDs[0] {
			t.Errorf("round %d rode session %q, round 1 rode %q", i+2, id, rec.sessIDs[0])
		}
	}

	// Each message covers only its own page; only the last asks for close.
	wantPages := []string{"aaa-fix-round", "bbb-fix-round", "ccc-fix-round"}
	for i, msg := range rec.msgs {
		if !strings.Contains(msg, wantPages[i]) {
			t.Errorf("round %d does not name its page %s:\n%s", i+1, wantPages[i], msg)
		}
		for _, other := range wantPages[i+1:] {
			if strings.Contains(msg, other) {
				t.Errorf("round %d names a later page %s:\n%s", i+1, other, msg)
			}
		}
		if i < 2 && strings.Contains(msg, "stage.close") {
			t.Errorf("round %d asks for stage.close; only the final round may:\n%s", i+1, msg)
		}
	}
	if !strings.Contains(rec.msgs[2], "stage.close") {
		t.Errorf("final round does not ask for stage.close:\n%s", rec.msgs[2])
	}

	// Progress: one line per round as it starts.
	for i, page := range wantPages {
		want := fmt.Sprintf("%s (%d/3)", "wiki/concepts/"+page+".md", i+1)
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout has no progress line %q; stdout:\n%s", want, stdout)
		}
	}

	// ONE changeset: all three rounds' repairs sit in the single open
	// changeset the command opened before the first round.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got := len(cs.Live()); got != 3 {
		t.Fatalf("the open changeset holds %d live op(s), want 3 (one per round, one changeset)", got)
	}
}

func TestLintFixContinuesPastFailedPage(t *testing.T) {
	maxTokens8192Env(t)
	root := threeBrokenPagesFixture(t)

	var rec *roundRecordingAgent
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		rec = &roundRecordingAgent{
			e:        e,
			sessions: sessions,
			failOn:   2,
			failWith: errors.New("injected: round 2 (bbb) failed"),
		}
		return rec, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (a page failed); stderr=%q stdout=%q", code, stderr, stdout)
	}
	if len(rec.msgs) != 3 {
		t.Fatalf("the agent received %d messages, want 3 — the loop must continue past the failed page:\n%q",
			len(rec.msgs), rec.msgs)
	}

	// The failed page is named, with its error.
	if !strings.Contains(stdout, "bbb-fix-round") {
		t.Errorf("stdout does not name the failed page; stdout:\n%s", stdout)
	}
	if !strings.Contains(stdout, "injected: round 2 (bbb) failed") {
		t.Errorf("stdout does not carry the failed page's error; stdout:\n%s", stdout)
	}

	// The changeset summary still printed, holding rounds 1 and 3's ops
	// — round 2 staged nothing.
	if !strings.Contains(stdout, "opened changeset cs-") {
		t.Errorf("stdout does not name the changeset; stdout:\n%s", stdout)
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
	if got := len(cs.Live()); got != 2 {
		t.Fatalf("the open changeset holds %d live op(s), want 2 (rounds 1 and 3)", got)
	}
	var paths []string
	for _, op := range cs.Live() {
		paths = append(paths, op.Path)
	}
	if strings.Contains(strings.Join(paths, ","), "lint-fix-round-2") {
		t.Errorf("the failed round's op is in the changeset: %v", paths)
	}
	for _, want := range []string{"lint-fix-round-1", "lint-fix-round-3"} {
		if !strings.Contains(strings.Join(paths, ","), want) {
			t.Errorf("changeset is missing %s; live paths: %v", want, paths)
		}
	}
}

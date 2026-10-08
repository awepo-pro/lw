package main

// ingest_recompile_test.go pins 055: `lw ingest --recompile` writes pages from
// a raw source the vault already holds. The real failure (2026-10-08): three
// HTTP articles re-ingested after their first ingest had staged raw only, and
// every one printed "skipped …: already in the vault" — A-807's dedupe runs
// before any turn — so a committed raw cited by no page had no path to pages.
// Every test runs through the real run() dispatch with the newIngestAgent seam
// swapped for a fake that records what the command handed it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// The two turn-message paragraphs of D3, byte for byte as the spec froze them,
// so a drifted production constant cannot pass by being compared to itself.
const (
	rcWantHead = "These sources are already in the vault as committed raw files; compile wiki pages from them. The changeset for this work is already open, so do not call stage.open, and do not call stage.ingest_source — each source already has the raw/ path given below. For each source: read it with raw.get, then create or update wiki pages that faithfully reflect it, following the schema and citing that raw path. Read every chunk of each source, requesting several raw.get chunks in one response. Read only the existing pages that wiki.search shows are related to the source — do not survey the whole wiki; when pages already cite a source, patching them usually serves it better than a new page. Write pages as soon as you have what they need, and stage several pages or patches in one response when they do not depend on each other. When you are done, call stage.close to summarize the proposed changeset.\n\n"
	rcWantTail = "\nThese sources are already in the vault as committed raw files: do not call stage.ingest_source for them. Read each with raw.get and create or update wiki pages from it, citing its raw/ path, under the same rules.\n\n"
)

const (
	rcRawA     = "raw/articles/a.md"
	rcRawABody = "# Alpha Source\n\nAlpha body, a committed raw source nothing cites yet.\n"
)

// rcCommitRaw writes a committed raw source under root — a file the vault
// parses, which is all "committed" means to Vault().RawSource — and returns
// its bytes so a test can prove a recompile never touched them.
func rcCommitRaw(t *testing.T, root, rel, body string) []byte {
	t.Helper()
	ingested, err := vault.ParseDate("2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	content := (&vault.RawSource{
		SourceURL: "https://example.test/" + strings.TrimSuffix(filepath.Base(rel), ".md"),
		Ingested:  ingested,
		SHA256:    vault.BodySHA256(body),
		Body:      body,
	}).Serialize()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return content
}

// rcWritePage writes a committed wiki page whose frontmatter lists sources
// and whose body is body.
func rcWritePage(t *testing.T, root, rel string, sources []string, body string) {
	t.Helper()
	page := "---\ntitle: " + strings.TrimSuffix(filepath.Base(rel), ".md") +
		"\ncreated: 2026-10-01\nupdated: 2026-10-01\ntype: concept\ntags: [inference]\nsources: [" +
		strings.Join(sources, ", ") + "]\nconfidence: medium\n---\n\n# Page\n\n" + body
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
}

// rcPageOp is the create_page the fake turn stages: a page that cites raw.
func rcPageOp(raw string) stage.Op {
	content := "---\ntitle: Recompile Test\ncreated: 2026-10-08\nupdated: 2026-10-08\ntype: concept\ntags: [inference]\nsources: [" + raw +
		"]\nconfidence: medium\n---\n\n# Recompile Test\n\nCompiled from a committed raw source.^[" + raw + "]\n\nSee [[kv-cache]] and [[flash-attention]].\n"
	return stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/recompile-test.md",
		Content:    []byte(content),
		Rationale:  "055 test: a page written from a committed raw",
		Provenance: []string{raw},
	}
}

// rcSpy is the fake the recompile tests swap in for newIngestAgent: it records
// every construction (with the recompile slice it was handed) and every Send.
type rcSpy struct {
	mu        sync.Mutex
	built     int
	recompile []string
	msgs      []string
}

func (s *rcSpy) record(recompile []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.built++
	s.recompile = append([]string(nil), recompile...)
}

func (s *rcSpy) message() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.msgs) == 0 {
		return ""
	}
	return s.msgs[len(s.msgs)-1]
}

// rcAgent stages ops through the real engine (fakeStageAgent) and records the
// message of each turn it is sent.
type rcAgent struct {
	fakeStageAgent
	spy *rcSpy
}

func (a *rcAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	a.spy.mu.Lock()
	a.spy.msgs = append(a.spy.msgs, msg)
	a.spy.mu.Unlock()
	return a.fakeStageAgent.Send(ctx, sessionID, msg, out)
}

// withRcSpy swaps newIngestAgent — with its 055 trailing recompile parameter —
// for a spy that stages ops when it is built over an engine.
func withRcSpy(t *testing.T, ops []stage.Op) *rcSpy {
	t.Helper()
	spy := &rcSpy{}
	orig := newIngestAgent
	newIngestAgent = func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor, recompile []string) (agent.Agent, error) {
		spy.record(recompile)
		return &rcAgent{fakeStageAgent: fakeStageAgent{e: e, sessions: sessions, ops: ops}, spy: spy}, nil
	}
	t.Cleanup(func() { newIngestAgent = orig })
	return spy
}

// rcItem is one target's item block, restated here from D3 so the production
// format string is not its own oracle.
func rcItem(raw string, chunks int, title, citedBy string) string {
	return fmt.Sprintf("- raw: %s\n  chunks: %d\n  title: %s\n  cited by: %s\n", raw, chunks, title, citedBy)
}

// rcOpenChangeset reads back the one open changeset of root.
func rcOpenChangeset(t *testing.T, root string) *stage.Changeset {
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
	return cs
}

// rcCount returns how many live ops of kind cs holds.
func rcCount(cs *stage.Changeset, kind stage.OpKind) int {
	n := 0
	for _, op := range cs.Live() {
		if op.Kind == kind {
			n++
		}
	}
	return n
}

// rcVault is the common start: the minimal fixture plus raw/articles/a.md, a
// committed raw that no page cites, and a pinned config.
func rcVault(t *testing.T) (root string, rawBytes []byte) {
	t.Helper()
	root = testutil.CopyFixture(t, "minimal")
	maxTokens8192Env(t)
	return root, rcCommitRaw(t, root, rcRawA, rcRawABody)
}

func rcRun(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return captureRun(t, func() int { return run(append([]string{"ingest"}, args...)) })
}

// TestRecompileMessageTextFrozen pins D3's two paragraphs byte for byte against
// the production constants.
func TestRecompileMessageTextFrozen(t *testing.T) {
	if recompileHead != rcWantHead {
		t.Errorf("recompileHead =\n%q\nwant\n%q", recompileHead, rcWantHead)
	}
	if recompileTail != rcWantTail {
		t.Errorf("recompileTail =\n%q\nwant\n%q", recompileTail, rcWantTail)
	}
}

// TestRecompileRawPathStagesNoRawOp is the headline: a committed, uncited raw
// named by its vault path becomes a recompile target — the fake turn stages one
// create_page, the changeset holds no ingest_source, the agent is built with
// the target path (so stage.close can guard its chunks), and the raw file on
// disk is exactly as it was.
func TestRecompileRawPathStagesNoRawOp(t *testing.T) {
	root, rawBytes := rcVault(t)
	before := snapshotVaultFiles(t, root)
	spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", rcRawA)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "recompiling raw/articles/a.md\n") {
		t.Errorf("stdout = %q, want the line `recompiling raw/articles/a.md`", stdout)
	}
	if got := strings.Join(spy.recompile, ","); got != rcRawA || spy.built != 1 {
		t.Errorf("newIngestAgent built %d time(s) with recompile %q, want once with [%s]", spy.built, spy.recompile, rcRawA)
	}
	if len(spy.msgs) != 1 {
		t.Fatalf("Send ran %d time(s), want 1", len(spy.msgs))
	}
	want := rcWantHead + rcItem(rcRawA, 1, "Alpha Source", "no page")
	if spy.msgs[0] != want {
		t.Errorf("message =\n%q\nwant\n%q", spy.msgs[0], want)
	}

	cs := rcOpenChangeset(t, root)
	if n := rcCount(cs, stage.OpIngestSource); n != 0 {
		t.Errorf("changeset has %d ingest_source op(s), want 0", n)
	}
	if n := rcCount(cs, stage.OpCreatePage); n != 1 {
		t.Errorf("changeset has %d create_page op(s), want 1", n)
	}
	if cs.Intent != "recompile raw/articles/a.md" {
		t.Errorf("intent = %q, want %q", cs.Intent, "recompile raw/articles/a.md")
	}

	// Raw is immutable: a recompile writes, moves and re-stages nothing of it.
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rcRawA)))
	if err != nil || string(got) != string(rawBytes) {
		t.Errorf("raw file changed (err %v):\n%q\nwant\n%q", err, got, rawBytes)
	}
	after := snapshotVaultFiles(t, root)
	if len(after) != len(before) {
		t.Errorf("working tree has %d wiki/raw files, had %d", len(after), len(before))
	}
	for p, b := range before {
		if after[p] != b {
			t.Errorf("%s changed on disk though nothing committed", p)
		}
	}
}

// TestRecompileByContentMatch: a file whose extracted body hashes to the
// committed raw is today's "already in the vault" skip — with the flag it is a
// recompile target for that raw instead, and the line says which argument led
// there.
func TestRecompileByContentMatch(t *testing.T) {
	root, _ := rcVault(t)
	file := writtenSource(t, "download.md", rcRawABody)
	spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", file)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	want := "recompiling raw/articles/a.md (same content as " + file + ")\n"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, want)
	}
	if strings.Contains(stdout, "skipped") {
		t.Errorf("stdout = %q, want no skip line for a recompile target", stdout)
	}
	if got := strings.Join(spy.recompile, ","); got != rcRawA {
		t.Errorf("recompile = %q, want [%s]", spy.recompile, rcRawA)
	}
	if got, want := spy.message(), rcWantHead+rcItem(rcRawA, 1, "Alpha Source", "no page"); got != want {
		t.Errorf("message =\n%q\nwant\n%q", got, want)
	}
	cs := rcOpenChangeset(t, root)
	if cs.Intent != "recompile raw/articles/a.md" {
		t.Errorf("intent = %q, want %q", cs.Intent, "recompile raw/articles/a.md")
	}
	if n := rcCount(cs, stage.OpIngestSource); n != 0 {
		t.Errorf("changeset has %d ingest_source op(s), want 0", n)
	}
}

// TestRecompileAbsolutePathInsideVault: an absolute path that resolves inside
// the vault to raw/… is a vault path too, not a file to extract.
func TestRecompileAbsolutePathInsideVault(t *testing.T) {
	root, _ := rcVault(t)
	spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

	abs := filepath.Join(root, "raw", "articles", "a.md")
	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", abs)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "recompiling raw/articles/a.md\n") {
		t.Errorf("stdout = %q, want the line `recompiling raw/articles/a.md`", stdout)
	}
	if got := strings.Join(spy.recompile, ","); got != rcRawA {
		t.Errorf("recompile = %q, want [%s]", spy.recompile, rcRawA)
	}
}

// TestRecompileMixed: one new file and one committed raw ride ONE turn. The
// message is today's ingest message for the new source, the recompile tail, and
// the target's item block; the intent names both halves.
func TestRecompileMixed(t *testing.T) {
	root, _ := rcVault(t)
	newFile := writtenSource(t, "fresh.md", "# Fresh Source\n\nFresh body.\n")
	spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", newFile, rcRawA)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if len(spy.msgs) != 1 {
		t.Fatalf("Send ran %d time(s), want exactly 1", len(spy.msgs))
	}
	msg := spy.msgs[0]
	paths := parseIngestPaths(msg)
	kinds := originalKinds(t, msg)
	if len(paths) != 1 || len(kinds) != 1 {
		t.Fatalf("message lists paths %q kinds %q, want one each:\n%s", paths, kinds, msg)
	}
	want := buildIngestMessage([]ingestItem{{path: paths[0], kind: kinds[0], title: "Fresh Source", source: newFile}}) +
		rcWantTail + rcItem(rcRawA, 1, "Alpha Source", "no page")
	if msg != want {
		t.Errorf("message =\n%q\nwant\n%q", msg, want)
	}
	if got := strings.Join(spy.recompile, ","); got != rcRawA {
		t.Errorf("recompile = %q, want [%s]", spy.recompile, rcRawA)
	}
	if !strings.Contains(stdout, "recompiling raw/articles/a.md\n") {
		t.Errorf("stdout = %q, want the recompiling line", stdout)
	}
	cs := rcOpenChangeset(t, root)
	if want := "ingest " + newFile + "; recompile raw/articles/a.md"; cs.Intent != want {
		t.Errorf("intent = %q, want %q", cs.Intent, want)
	}
}

// rcNormalized replaces a run's random parts — the scratch directory and the
// changeset id — so two runs of the same command compare byte for byte.
func rcNormalized(s, scratch, csID string) string {
	s = strings.ReplaceAll(s, scratch, "<scratch>")
	return strings.ReplaceAll(s, csID, "<cs>")
}

// TestRecompileFlagWithOnlyNewSourcesByteIdentical: the flag changes nothing
// for an invocation with no recompile target — the turn message (the request
// bytes of every ordinary ingest), the intent and stdout all match the same
// command without it.
func TestRecompileFlagWithOnlyNewSourcesByteIdentical(t *testing.T) {
	srcA := writtenSource(t, "one.md", "# One\n\nFirst new source.\n")
	srcB := writtenSource(t, "two.md", "# Two\n\nSecond new source.\n")

	type result struct{ msg, intent, stdout string }
	once := func(flag bool) result {
		root := testutil.CopyFixture(t, "minimal")
		maxTokens8192Env(t)
		spy := withRcSpy(t, []stage.Op{rcPageOp("raw/articles/kv-cache-explained.md")})
		args := []string{"--vault", root}
		if flag {
			args = append(args, "--recompile")
		}
		stdout, stderr, code := rcRun(t, append(args, srcA, srcB)...)
		if code != 0 {
			t.Fatalf("flag=%v: exit code = %d, want 0; stderr=%q stdout=%q", flag, code, stderr, stdout)
		}
		if len(spy.recompile) != 0 {
			t.Errorf("flag=%v: recompile = %q, want none", flag, spy.recompile)
		}
		cs := rcOpenChangeset(t, root)
		paths := parseIngestPaths(spy.message())
		if len(paths) != 2 {
			t.Fatalf("flag=%v: message lists %d path(s), want 2:\n%s", flag, len(paths), spy.message())
		}
		scratch := filepath.Dir(filepath.Dir(paths[0]))
		return result{
			msg:    rcNormalized(spy.message(), scratch, cs.ID),
			intent: cs.Intent,
			stdout: rcNormalized(stdout, scratch, cs.ID),
		}
	}
	plain, flagged := once(false), once(true)
	if plain != flagged {
		t.Errorf("flagged run differs from the plain run:\n plain   %+v\n flagged %+v", plain, flagged)
	}
	if want := "ingest " + srcA + ", " + srcB; plain.intent != want {
		t.Errorf("intent = %q, want today's %q", plain.intent, want)
	}
	if !strings.HasPrefix(plain.msg, "New source material has been extracted and saved locally") {
		t.Errorf("message does not open with today's ingest paragraph:\n%s", plain.msg)
	}
}

// TestRecompileCitedByList: a target cited by seven committed pages — some by
// their sources: list, some by a body marker only, one marker paged — shows the
// first five sorted and counts the rest.
func TestRecompileCitedByList(t *testing.T) {
	root, _ := rcVault(t)
	// Written out of order on purpose: the list is sorted by path, not by
	// whatever order the pages were created or the vault walked them in.
	rcWritePage(t, root, "wiki/concepts/rc-07.md", nil, "Cited in the body.^[raw/articles/a.md]\n")
	rcWritePage(t, root, "wiki/concepts/rc-03.md", []string{rcRawA}, "Listed in sources only.\n")
	rcWritePage(t, root, "wiki/concepts/rc-05.md", nil, "Cited with a page.^[raw/articles/a.md p.2]\n")
	rcWritePage(t, root, "wiki/concepts/rc-01.md", []string{rcRawA}, "Listed and cited.^[raw/articles/a.md]\n")
	rcWritePage(t, root, "wiki/concepts/rc-06.md", []string{rcRawA}, "Listed in sources only.\n")
	rcWritePage(t, root, "wiki/concepts/rc-02.md", nil, "Cited in the body.^[raw/articles/a.md]\n")
	rcWritePage(t, root, "wiki/concepts/rc-04.md", []string{rcRawA}, "Listed in sources only.\n")
	// Not citing: a different raw, and the target named only inside a code span.
	rcWritePage(t, root, "wiki/concepts/rc-08.md", []string{"raw/articles/kv-cache-explained.md"}, "Other.^[raw/articles/kv-cache-explained.md]\n")
	rcWritePage(t, root, "wiki/concepts/rc-09.md", nil, "Only in code: `^[raw/articles/a.md]`\n")
	spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", rcRawA)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	citedBy := "wiki/concepts/rc-01.md, wiki/concepts/rc-02.md, wiki/concepts/rc-03.md, wiki/concepts/rc-04.md, wiki/concepts/rc-05.md and 2 more"
	if got, want := spy.message(), rcWantHead+rcItem(rcRawA, 1, "Alpha Source", citedBy); got != want {
		t.Errorf("message =\n%q\nwant\n%q", got, want)
	}
}

// TestRecompileCitedByFewPagesHasNoMore: five or fewer citing pages are listed
// whole, with no " and N more" tail.
func TestRecompileCitedByFewPagesHasNoMore(t *testing.T) {
	root, _ := rcVault(t)
	for i := 1; i <= 5; i++ {
		rcWritePage(t, root, fmt.Sprintf("wiki/concepts/rc-%02d.md", i), []string{rcRawA}, "Listed.\n")
	}
	spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

	if stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", rcRawA); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	citedBy := "wiki/concepts/rc-01.md, wiki/concepts/rc-02.md, wiki/concepts/rc-03.md, wiki/concepts/rc-04.md, wiki/concepts/rc-05.md"
	if got, want := spy.message(), rcWantHead+rcItem(rcRawA, 1, "Alpha Source", citedBy); got != want {
		t.Errorf("message =\n%q\nwant\n%q", got, want)
	}
}

// TestRecompileUnknownRawPath: a vault path that names no committed raw fails
// before anything is opened — no agent is built (so no key is resolved) and no
// changeset appears.
func TestRecompileUnknownRawPath(t *testing.T) {
	root, _ := rcVault(t)
	spy := withRcSpy(t, nil)

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", "raw/articles/nope.md")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if want := "lw: ingest: recompile raw/articles/nope.md: no committed raw source at that path\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if spy.built != 0 {
		t.Errorf("newIngestAgent was built %d time(s); a bad vault path must resolve no key", spy.built)
	}
	noChangesetsAnywhere(t, root)

	// The same refusal when a good target rides along: nothing is half-done.
	_, stderr, code = rcRun(t, "--vault", root, "--recompile", rcRawA, "./raw/articles/nope.md")
	if code != 1 || !strings.Contains(stderr, "recompile raw/articles/nope.md: no committed raw source at that path") {
		t.Errorf("good + bad target: code %d, stderr %q", code, stderr)
	}
	if spy.built != 0 {
		t.Errorf("newIngestAgent was built %d time(s) after a mixed good/bad invocation", spy.built)
	}
	noChangesetsAnywhere(t, root)
}

// TestRecompileDuplicateTarget: a target reached twice keeps the first
// argument; the later one prints today's duplicate line naming it.
func TestRecompileDuplicateTarget(t *testing.T) {
	t.Run("vault_path_then_file", func(t *testing.T) {
		root, _ := rcVault(t)
		file := writtenSource(t, "dup.md", rcRawABody)
		spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

		stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", rcRawA, file)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if want := "skipped " + file + ": same content as raw/articles/a.md\n"; !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
		if got := strings.Count(spy.message(), "- raw: "); got != 1 {
			t.Errorf("message carries %d item block(s), want 1:\n%s", got, spy.message())
		}
		if got := strings.Join(spy.recompile, ","); got != rcRawA {
			t.Errorf("recompile = %q, want one target [%s]", spy.recompile, rcRawA)
		}
		if strings.Count(stdout, "recompiling ") != 1 {
			t.Errorf("stdout = %q, want exactly one recompiling line", stdout)
		}
	})

	t.Run("file_then_vault_path", func(t *testing.T) {
		root, _ := rcVault(t)
		file := writtenSource(t, "dup.md", rcRawABody)
		spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

		stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", file, rcRawA)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if want := "recompiling raw/articles/a.md (same content as " + file + ")\n"; !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
		if want := "skipped raw/articles/a.md: same content as " + file + "\n"; !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
		if got := strings.Count(spy.message(), "- raw: "); got != 1 {
			t.Errorf("message carries %d item block(s), want 1", got)
		}
	})

	t.Run("two_files_one_raw", func(t *testing.T) {
		root, _ := rcVault(t)
		first := writtenSource(t, "first.md", rcRawABody)
		second := writtenSource(t, "second.md", rcRawABody)
		spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

		stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", first, second)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if want := "skipped " + second + ": same content as " + first + "\n"; !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
		if len(spy.recompile) != 1 {
			t.Errorf("recompile = %q, want one target", spy.recompile)
		}
	})
}

// TestRecompileDryRun: --dry-run lists a target after the would-ingest lines,
// counts it in the limits line, and opens nothing.
func TestRecompileDryRun(t *testing.T) {
	root, _ := rcVault(t)
	ingestLimitsEnv(t, 0)
	newFile := writtenSource(t, "fresh.md", "# Fresh\n\nFresh body.\n")
	noAgentEver(t)

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", "--dry-run", newFile, rcRawA)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	want := "would ingest " + newFile + "\n" +
		"would recompile raw/articles/a.md\n" +
		"within limits: 2 files, 1 KB (limit 10 files, 282 KB)\n"
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
	noChangesetsAnywhere(t, root)

	// A dry run with only a target is still a dry run.
	stdout, _, code = rcRun(t, "--vault", root, "--recompile", "--dry-run", rcRawA)
	if want := "would recompile raw/articles/a.md\nwithin limits: 1 files, 1 KB (limit 10 files, 282 KB)\n"; code != 0 || stdout != want {
		t.Errorf("target-only dry run: code %d, stdout %q, want %q", code, stdout, want)
	}
	noChangesetsAnywhere(t, root)
}

// TestRecompileCountsTowardLimits: a target's raw body counts toward the byte
// cap and the target is a candidate for "largest" — two small committed raws
// that together pass the cap are refused with today's limit error, naming the
// bigger one, before anything is opened.
func TestRecompileCountsTowardLimits(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 40) // capBytes = 40 × 4 × 75% = 120
	body := func(n int) string { return "# T\n\n" + strings.Repeat("x", n-6) + "\n" }
	rcCommitRaw(t, root, "raw/articles/small.md", body(70))
	rcCommitRaw(t, root, "raw/articles/bigger.md", body(80))
	noAgentEver(t)

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", "raw/articles/small.md", "raw/articles/bigger.md")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q stdout=%q", code, stderr, stdout)
	}
	want := "2 files (1 KB) to ingest; the limit is 10 files and 1 KB per ingest" +
		" (llm.limits.context_tokens 40 × 4 × 75%); largest: bigger.md (1 KB)." +
		" Nothing was opened — ingest fewer files, or raise llm.limits.context_tokens."
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	noChangesetsAnywhere(t, root)

	// Either raw alone is under the cap: the refusal is the sum, not the count.
	spy := withRcSpy(t, []stage.Op{rcPageOp("raw/articles/small.md")})
	if stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", "raw/articles/small.md"); code != 0 {
		t.Fatalf("one small target: exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if spy.built != 1 {
		t.Errorf("one small target built the agent %d time(s), want 1", spy.built)
	}
}

// TestRecompileJoinsOpenChangeset: with a changeset already open (019) the
// recompile joins it — the notice goes to stderr, its own ops are appended and
// the work already there is left alone.
func TestRecompileJoinsOpenChangeset(t *testing.T) {
	root, _ := rcVault(t)
	patchID := stagePreExistingPatch(t, root)
	before := rcOpenChangeset(t, root)
	spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", rcRawA)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stderr, "joined open changeset "+before.ID) {
		t.Errorf("stderr = %q, want the joined notice for %s", stderr, before.ID)
	}
	// 019: a join folds the verb's intent into the changeset's, "<old> + <new>".
	joinedIntent := before.Intent + " + recompile raw/articles/a.md"
	if !strings.Contains(stdout, "joined changeset "+before.ID+": "+joinedIntent+" (2 op(s))") {
		t.Errorf("stdout = %q, want the joined summary line for %q", stdout, joinedIntent)
	}
	if spy.built != 1 {
		t.Errorf("newIngestAgent built %d time(s), want 1", spy.built)
	}

	after := rcOpenChangeset(t, root)
	if after.ID != before.ID || after.Intent != joinedIntent {
		t.Errorf("changeset %s %q became %s %q; a join keeps the id and folds the intent in as %q", before.ID, before.Intent, after.ID, after.Intent, joinedIntent)
	}
	if len(after.Ops) != 2 || after.Ops[0].ID != patchID || after.Ops[0].Kind != stage.OpPatchPage || after.Ops[0].State == stage.StateDropped {
		t.Errorf("ops = %+v, want the pre-existing patch %s first and still live", after.Ops, patchID)
	}
	if after.Ops[1].Kind != stage.OpCreatePage {
		t.Errorf("second op = %s, want the turn's create_page", after.Ops[1].Kind)
	}
}

// TestRecompileProposedNothing: a recompile turn that stages nothing is the
// same failure an ingest turn that stages nothing is — the changeset it opened
// is rejected, not left empty for the user to find.
func TestRecompileProposedNothing(t *testing.T) {
	root, _ := rcVault(t)
	withRcSpy(t, nil)

	_, stderr, code := rcRun(t, "--vault", root, "--recompile", rcRawA)
	if code != 1 || !strings.Contains(stderr, "agent proposed nothing for this ingest; the changeset was rejected") {
		t.Errorf("code %d, stderr %q, want today's proposed-nothing failure", code, stderr)
	}
	if got := countChangesets(t, root, "open"); got != 0 {
		t.Errorf("open changesets = %d, want 0", got)
	}
}

// rcSafeArg fails the test unless arg survives shell quoting unchanged, so a
// test that expects the hint's <q> to equal the argument is not silently
// wrong about a temp path with an odd byte in it.
func rcSafeArg(t *testing.T, arg string) {
	t.Helper()
	if q := shellQuoteArg(arg); q != arg {
		t.Fatalf("test path %q needs shell quoting (%s); pick another", arg, q)
	}
}

// TestIngestRawPathWithoutFlagSkips pins 055 S1b. Without --recompile, an
// argument that names a committed raw by its vault path is the raw's OWN file —
// extracting it would stage a second raw whose body still carries the first
// one's frontmatter (the dedupe hashes the whole file, which never equals the
// body hash the vault stores). So it is never extracted: it is skipped exactly
// like a source whose content matched that raw, with D4's lines and the
// argument as typed. A skip does not count toward the limits, and a command
// whose every argument is skipped opens nothing and builds no agent.
func TestIngestRawPathWithoutFlagSkips(t *testing.T) {
	const nothing = "nothing to ingest: every source is already in the vault\n"

	t.Run("uncited_names_the_flag", func(t *testing.T) {
		root, _ := rcVault(t)
		chdir(t, t.TempDir()) // a relative raw/… must not depend on the working directory
		spy := withRcSpy(t, nil)

		stdout, stderr, code := rcRun(t, "--vault", root, rcRawA)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		want := "skipped raw/articles/a.md: already in the vault at raw/articles/a.md, cited by no page — run lw ingest --recompile raw/articles/a.md to write pages from it\n" + nothing
		if stdout != want {
			t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
		}
		if spy.built != 0 {
			t.Errorf("newIngestAgent built %d time(s), want 0", spy.built)
		}
		noChangesetsAnywhere(t, root)
	})

	t.Run("cited_keeps_the_plain_line", func(t *testing.T) {
		root, _ := rcVault(t)
		rcWritePage(t, root, "wiki/concepts/cites-a.md", []string{rcRawA}, "A claim.^[raw/articles/a.md]\n")
		chdir(t, t.TempDir())
		spy := withRcSpy(t, nil)

		stdout, stderr, code := rcRun(t, "--vault", root, rcRawA)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		want := "skipped raw/articles/a.md: already in the vault at raw/articles/a.md\n" + nothing
		if stdout != want {
			t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
		}
		if spy.built != 0 {
			t.Errorf("newIngestAgent built %d time(s), want 0", spy.built)
		}
		noChangesetsAnywhere(t, root)
	})

	t.Run("argument_is_quoted_as_typed", func(t *testing.T) {
		root, _ := rcVault(t)
		chdir(t, t.TempDir())
		withRcSpy(t, nil)

		// The line names the argument the user typed, not the normalized path.
		stdout, _, code := rcRun(t, "--vault", root, "./raw//articles/a.md")
		want := "skipped ./raw//articles/a.md: already in the vault at raw/articles/a.md, cited by no page — run lw ingest --recompile ./raw//articles/a.md to write pages from it\n" + nothing
		if code != 0 || stdout != want {
			t.Errorf("code %d, stdout =\n%q\nwant\n%q", code, stdout, want)
		}
	})

	t.Run("a_skip_counts_toward_no_limit_and_the_rest_ingests", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 40) // capBytes = 120
		// 200 bytes: over the cap on its own, so counting the skip would refuse.
		big := "# Big\n\n" + strings.Repeat("x", 192) + "\n"
		rcCommitRaw(t, root, "raw/articles/big.md", big)
		fresh := writtenSource(t, "fresh.md", "# Fresh\n\nFresh body.\n")
		chdir(t, t.TempDir())
		spy := withRcSpy(t, []stage.Op{rcPageOp("raw/articles/kv-cache-explained.md")})

		stdout, stderr, code := rcRun(t, "--vault", root, "raw/articles/big.md", fresh)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if want := "skipped raw/articles/big.md: already in the vault at raw/articles/big.md, cited by no page — run lw ingest --recompile raw/articles/big.md to write pages from it\n"; !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
		wantSourcesEqual(t, originalSources(t, spy.message()), []string{fresh})
		if len(spy.recompile) != 0 {
			t.Errorf("recompile = %q, want none: a skip is not a target", spy.recompile)
		}
		if cs := rcOpenChangeset(t, root); cs.Intent != "ingest raw/articles/big.md, "+fresh {
			// Today's intent lists every argument, skipped ones included.
			t.Errorf("intent = %q, want today's ingestIntent over every argument", cs.Intent)
		}
	})
}

// TestIngestAbsRawPathWithoutFlagSkips: the same skip for an absolute path that
// resolves into <root>/raw/ — what a shell completion or a file manager hands
// over. The line names the argument as typed.
func TestIngestAbsRawPathWithoutFlagSkips(t *testing.T) {
	root, _ := rcVault(t)
	spy := withRcSpy(t, nil)

	abs := filepath.Join(root, "raw", "articles", "a.md")
	rcSafeArg(t, abs)
	stdout, stderr, code := rcRun(t, "--vault", root, abs)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	want := "skipped " + abs + ": already in the vault at raw/articles/a.md, cited by no page — run lw ingest --recompile " + abs + " to write pages from it\n" +
		"nothing to ingest: every source is already in the vault\n"
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
	if spy.built != 0 {
		t.Errorf("newIngestAgent built %d time(s), want 0", spy.built)
	}
	noChangesetsAnywhere(t, root)
}

// TestIngestUncommittedRawPathWithoutFlagExtracts: a raw/… argument that names
// NO committed raw is not special without the flag — it is a path like any
// other and extracted as one: a missing file fails exactly as before 055, and a
// real file of that relative name in the working directory is ingested.
func TestIngestUncommittedRawPathWithoutFlagExtracts(t *testing.T) {
	t.Run("missing_file_fails_as_before", func(t *testing.T) {
		root, _ := rcVault(t)
		chdir(t, t.TempDir())
		spy := withRcSpy(t, nil)

		_, stderr, code := rcRun(t, "--vault", root, "raw/articles/nope.md")
		if code != 1 || !strings.Contains(stderr, "extract raw/articles/nope.md:") {
			t.Errorf("code %d, stderr %q, want the pre-055 extract failure", code, stderr)
		}
		if spy.built != 0 {
			t.Errorf("newIngestAgent built %d time(s), want 0", spy.built)
		}
		// Nothing was opened: the engine may have been (the vault had to be
		// asked), a changeset must not exist.
		for _, state := range []string{"open", "committed", "rejected"} {
			if entries, err := os.ReadDir(filepath.Join(root, ".llmwiki", "changesets", state)); err == nil && len(entries) != 0 {
				t.Errorf("changesets/%s holds %d entr(ies), want none", state, len(entries))
			}
		}
	})

	t.Run("file_in_the_working_directory_is_ingested", func(t *testing.T) {
		root, _ := rcVault(t)
		cwd := t.TempDir()
		dirFile(t, cwd, "raw/articles/notes.md", "# Local Notes\n\nA file that only looks like a vault path.\n")
		chdir(t, cwd)
		spy := withRcSpy(t, []stage.Op{rcPageOp("raw/articles/kv-cache-explained.md")})

		stdout, stderr, code := rcRun(t, "--vault", root, "raw/articles/notes.md")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		wantSourcesEqual(t, originalSources(t, spy.message()), []string{"raw/articles/notes.md"})
		if strings.Contains(stdout, "skipped") {
			t.Errorf("stdout = %q, want no skip line for a path naming no committed raw", stdout)
		}
	})
}

// TestIngestSkipLineUncitedNamesFlag: without the flag, a source whose body is
// already in the vault as a raw nothing cites gets the hint naming the flag —
// the command that would have written its pages — with the argument quoted for
// a shell when it needs it.
func TestIngestSkipLineUncitedNamesFlag(t *testing.T) {
	t.Run("plain_argument", func(t *testing.T) {
		root, _ := rcVault(t)
		file := writtenSource(t, "download.md", rcRawABody)
		noAgentEver(t)

		stdout, stderr, code := rcRun(t, "--vault", root, file)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		want := "skipped " + file + ": already in the vault at raw/articles/a.md, cited by no page — run lw ingest --recompile " + file + " to write pages from it\n" +
			"nothing to ingest: every source is already in the vault\n"
		if stdout != want {
			t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
		}
	})

	t.Run("space_and_quote_are_quoted", func(t *testing.T) {
		root, _ := rcVault(t)
		dir := t.TempDir()
		file := dirFile(t, dir, "it's a note.md", rcRawABody)
		noAgentEver(t)

		stdout, stderr, code := rcRun(t, "--vault", root, file)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		quoted := "'" + dir + "/it'\\''s a note.md'"
		want := "skipped " + file + ": already in the vault at raw/articles/a.md, cited by no page — run lw ingest --recompile " + quoted + " to write pages from it\n"
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout =\n%q\nwant it to contain\n%q", stdout, want)
		}
	})
}

// TestIngestSkipLineCitedUnchanged: a source already in the vault as a raw that
// IS cited — by a page's sources: list alone, and separately by a body marker
// alone — keeps today's skip line byte for byte, with no hint.
func TestIngestSkipLineCitedUnchanged(t *testing.T) {
	cases := []struct {
		name    string
		sources []string
		body    string
	}{
		{"sources_list_only", []string{"raw/articles/b.md"}, "A claim with no marker.\n"},
		{"body_marker_only", nil, "A claim.^[raw/articles/b.md]\n"},
		{"paged_body_marker_only", nil, "A claim.^[raw/articles/b.md p.3]\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := testutil.CopyFixture(t, "minimal")
			maxTokens8192Env(t)
			const body = "# Bravo Source\n\nBravo body.\n"
			rcCommitRaw(t, root, "raw/articles/b.md", body)
			rcWritePage(t, root, "wiki/concepts/cites-b.md", c.sources, c.body)
			file := writtenSource(t, "bravo.md", body)
			noAgentEver(t)

			stdout, stderr, code := rcRun(t, "--vault", root, file)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
			}
			want := "skipped " + file + ": already in the vault at raw/articles/b.md\n" +
				"nothing to ingest: every source is already in the vault\n"
			if stdout != want {
				t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
			}
		})
	}
}

// TestRecompileVaultPath: the D1 classifier — which arguments name a raw in
// the vault rather than a file to read.
func TestRecompileVaultPath(t *testing.T) {
	root := t.TempDir()
	notes := filepath.Join(root, "notes")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, notes)

	tests := []struct {
		arg  string
		want string // "" = not a vault path
	}{
		{"raw/articles/a.md", "raw/articles/a.md"},
		{"./raw/articles/a.md", "raw/articles/a.md"},
		{"raw//articles/./a.md", "raw/articles/a.md"},
		{"raw/articles/../papers/p.md", "raw/papers/p.md"},
		{filepath.Join(root, "raw", "articles", "a.md"), "raw/articles/a.md"},
		{"../raw/articles/a.md", "raw/articles/a.md"}, // cwd is root/notes
		{filepath.Join(root, "wiki", "concepts", "x.md"), ""},
		{filepath.Join(root, "notes", "raw", "a.md"), ""},
		{filepath.Join(filepath.Dir(root), "elsewhere", "raw", "a.md"), ""},
		{"notes/raw/a.md", ""},
		{"raw", ""},
		{"raw/", ""},
		{"rawfile.md", ""},
		{"https://example.test/raw/a.md", ""},
		{"../../raw/a.md", ""},
	}
	for _, tc := range tests {
		got, ok := recompileVaultPath(root, tc.arg)
		if tc.want == "" {
			if ok {
				t.Errorf("recompileVaultPath(%q) = %q, true; want not a vault path", tc.arg, got)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("recompileVaultPath(%q) = %q, %v; want %q, true", tc.arg, got, ok, tc.want)
		}
	}
}

// TestRecompileVaultPathURLBesideRaw: a URL argument is never a vault path,
// even when the working directory is the vault's raw/ (a relative "https:/…"
// would otherwise resolve under it).
func TestRecompileVaultPathURLBesideRaw(t *testing.T) {
	root := t.TempDir()
	raw := filepath.Join(root, "raw")
	if err := os.MkdirAll(raw, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, raw)
	if got, ok := recompileVaultPath(root, "https://example.test/a.md"); ok {
		t.Errorf("recompileVaultPath(URL) = %q, true; want not a vault path", got)
	}
}

// TestRcShellQuote: the hint's <q>. Safe bytes pass through; anything else —
// including the empty string — is single-quoted, an embedded quote closed,
// escaped and reopened.
func TestRcShellQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/tmp/notes/a.md", "/tmp/notes/a.md"},
		{"https://example.test/a_b-c.html?x", `'https://example.test/a_b-c.html?x'`},
		{"a+b=c,d@e%f:g.h/i_j-k", "a+b=c,d@e%f:g.h/i_j-k"},
		{"has space.md", `'has space.md'`},
		{"it's.md", `'it'\''s.md'`},
		{"$HOME/x", `'$HOME/x'`},
		{"é.md", `'é.md'`},
		{"", `''`},
	}
	for _, tc := range tests {
		if got := shellQuoteArg(tc.in); got != tc.want {
			t.Errorf("shellQuoteArg(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRawCitations: which committed pages cite which raw — a sources: entry or
// a body marker (any page suffix) counts; a marker in code, a wiki/ marker and
// an unrelated raw do not. The result is sorted by page path.
func TestRawCitations(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	rcCommitRaw(t, root, "raw/articles/a.md", rcRawABody)
	rcWritePage(t, root, "wiki/concepts/z-list.md", []string{rcRawA}, "Listed.\n")
	rcWritePage(t, root, "wiki/concepts/m-mark.md", nil, "Marked.^[raw/articles/a.md]\n")
	rcWritePage(t, root, "wiki/concepts/b-paged.md", nil, "Paged.^[raw/articles/a.md p.4-6]\n")
	rcWritePage(t, root, "wiki/concepts/c-code.md", nil, "Code `^[raw/articles/a.md]` and\n\n```\n^[raw/articles/a.md]\n```\n")
	rcWritePage(t, root, "wiki/concepts/d-wiki.md", nil, "Link.^[wiki/concepts/kv-cache.md]\n")
	rcWritePage(t, root, "wiki/concepts/e-other.md", []string{"raw/articles/other.md"}, "Other.^[raw/articles/other.md]\n")
	// Enough more citing pages that a result left in map order would almost
	// never come out sorted by accident.
	for _, n := range []string{"q-4", "q-1", "q-6", "q-3", "q-5", "q-2"} {
		rcWritePage(t, root, "wiki/concepts/"+n+".md", []string{rcRawA}, "Listed.\n")
	}
	e := openEngine(t, root)

	got := rawCitations(e.Vault())
	want := []string{
		"wiki/concepts/b-paged.md", "wiki/concepts/m-mark.md",
		"wiki/concepts/q-1.md", "wiki/concepts/q-2.md", "wiki/concepts/q-3.md", "wiki/concepts/q-4.md", "wiki/concepts/q-5.md", "wiki/concepts/q-6.md",
		"wiki/concepts/z-list.md",
	}
	if g := got[rcRawA]; strings.Join(g, ",") != strings.Join(want, ",") {
		t.Errorf("citations of %s = %q, want %q", rcRawA, g, want)
	}
	if g := got["raw/articles/other.md"]; len(g) != 1 || g[0] != "wiki/concepts/e-other.md" {
		t.Errorf("citations of other.md = %q, want [e-other.md]", g)
	}
	if g, ok := got["wiki/concepts/kv-cache.md"]; ok {
		t.Errorf("a wiki/ marker was recorded as a raw citation: %q", g)
	}
	// The fixture's own pages cite their raws; a raw nobody cites is absent.
	if g := got["raw/articles/kv-cache-explained.md"]; len(g) == 0 {
		t.Errorf("fixture raw kv-cache-explained.md has no citing page, want some")
	}
	if g, ok := got["raw/articles/never.md"]; ok {
		t.Errorf("citations of an uncited raw = %q, want absent", g)
	}
}

// TestRecompileCitedByLine pins the cited-by wording on its own: no page,
// up to five pages, and the " and N more" tail.
func TestRecompileCitedByLine(t *testing.T) {
	page := func(n int) []string {
		var out []string
		for i := 1; i <= n; i++ {
			out = append(out, fmt.Sprintf("wiki/p%d.md", i))
		}
		return out
	}
	tests := []struct {
		pages []string
		want  string
	}{
		{nil, "no page"},
		{page(1), "wiki/p1.md"},
		{page(5), "wiki/p1.md, wiki/p2.md, wiki/p3.md, wiki/p4.md, wiki/p5.md"},
		{page(6), "wiki/p1.md, wiki/p2.md, wiki/p3.md, wiki/p4.md, wiki/p5.md and 1 more"},
		{page(12), "wiki/p1.md, wiki/p2.md, wiki/p3.md, wiki/p4.md, wiki/p5.md and 7 more"},
	}
	for _, tc := range tests {
		if got := citedByText(tc.pages); got != tc.want {
			t.Errorf("citedByText(%d pages) = %q, want %q", len(tc.pages), got, tc.want)
		}
	}
}

// TestIngestRawPathShadowedByLocalFile pins 055 S1c. The working directory is
// a second vault holding its own raw/articles/a.md, with content the target
// vault does not have. Without --recompile that argument is the local file:
// it is ingested as a new source, not skipped as "already in the vault" (the
// target vault's raw at the same path holds different bytes). With the flag,
// D1 holds: a relative raw/… is the target vault's path whatever the cwd.
func TestIngestRawPathShadowedByLocalFile(t *testing.T) {
	t.Run("no_flag_ingests_the_local_file", func(t *testing.T) {
		root, _ := rcVault(t)
		cwd := t.TempDir()
		dirFile(t, cwd, rcRawA, "# Other Vault\n\nNot the target vault's a.md.\n")
		chdir(t, cwd)
		spy := withRcSpy(t, []stage.Op{rcPageOp("raw/articles/kv-cache-explained.md")})

		stdout, stderr, code := rcRun(t, "--vault", root, rcRawA)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if strings.Contains(stdout, "skipped") {
			t.Errorf("stdout = %q, want no skip line: the local file is not the vault's raw", stdout)
		}
		wantSourcesEqual(t, originalSources(t, spy.message()), []string{rcRawA})
		if len(spy.recompile) != 0 {
			t.Errorf("recompile = %q, want none without the flag", spy.recompile)
		}
	})

	t.Run("flag_still_names_the_vault_raw", func(t *testing.T) {
		root, _ := rcVault(t)
		cwd := t.TempDir()
		dirFile(t, cwd, rcRawA, "# Other Vault\n\nNot the target vault's a.md.\n")
		chdir(t, cwd)
		spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

		stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", rcRawA)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if !strings.Contains(stdout, "recompiling "+rcRawA+"\n") {
			t.Errorf("stdout = %q, want the recompiling line", stdout)
		}
		if got := strings.Join(spy.recompile, ","); got != rcRawA {
			t.Errorf("recompile = %q, want [%s]", spy.recompile, rcRawA)
		}
	})
}

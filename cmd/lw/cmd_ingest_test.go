package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
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
// one test, restoring the original on cleanup. cmd_query.go, cmd_lint.go
// and cmd_tui.go all call newAgent unchanged, and their own tests use this
// helper directly — it is NOT what cmdIngest calls (see withFakeIngestAgent
// below), so it stays untouched by C-123's fix.
func withFakeAgent(t *testing.T, fn func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error)) {
	t.Helper()
	orig := newAgent
	newAgent = fn
	t.Cleanup(func() { newAgent = orig })
}

// withFakeIngestAgent swaps the package-level newIngestAgent seam — the
// one cmdIngest itself calls (C-123) — for the duration of one test,
// restoring the original on cleanup.
func withFakeIngestAgent(t *testing.T, fn func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error)) {
	t.Helper()
	orig := newIngestAgent
	newIngestAgent = fn
	t.Cleanup(func() { newIngestAgent = orig })
}

// toolCallingFakeAgent is a fake agent.Agent that — unlike fakeStageAgent,
// which proposes ops directly through *stage.Engine — drives the real
// *tools.Registry via Registry.Call for stage.ingest_source. This is what
// proves C-123's fix end to end: the registry is built over exactly the
// extract.Extractor cmdIngest wired (a chain fronted by preExtracted), and
// stage.ingest_source's handler (internal/tools/stage_source.go, not owned
// by this subtask) actually re-extracts each scratch path through it,
// rather than the test asserting on preExtracted in isolation and hoping
// the wiring matches. It reads which scratch paths to ingest straight out
// of the message buildIngestMessage produced, the same "- path: <path>"
// lines a real tool-calling model would read.
type toolCallingFakeAgent struct {
	reg      *tools.Registry
	sessions agent.SessionStore
}

// newToolCallingFakeAgent builds a toolCallingFakeAgent whose registry is
// constructed the same way newIngestAgent's would be, over the extractor
// ex the caller (cmdIngest, via the swapped newIngestAgent seam) supplies.
func newToolCallingFakeAgent(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) *toolCallingFakeAgent {
	return &toolCallingFakeAgent{
		reg: tools.NewRegistry(tools.Deps{
			Vault:   e.Vault(),
			Index:   e.Index(),
			Engine:  e,
			Extract: ex,
			Author:  stage.Author{Kind: "agent", Model: cfg.LLM.Model},
		}),
		sessions: sessions,
	}
}

func (f *toolCallingFakeAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	defer close(out)
	for _, path := range parseIngestPaths(msg) {
		args, err := json.Marshal(map[string]string{"uri": path})
		if err != nil {
			select {
			case out <- agent.ErrorEv{Err: err}:
			case <-ctx.Done():
			}
			return err
		}
		res, err := f.reg.Call(ctx, "stage.ingest_source", args)
		if err != nil {
			select {
			case out <- agent.ErrorEv{Err: err}:
			case <-ctx.Done():
			}
			return err
		}
		if res.IsError {
			cerr := fmt.Errorf("stage.ingest_source: %s", res.Content)
			select {
			case out <- agent.ErrorEv{Err: cerr}:
			case <-ctx.Done():
			}
			return cerr
		}
	}
	select {
	case out <- agent.DoneEv{Reason: "stop", Rounds: 1}:
	case <-ctx.Done():
	}
	return nil
}

func (f *toolCallingFakeAgent) Sessions() agent.SessionStore { return f.sessions }

// parseIngestPaths extracts every scratch path buildIngestMessage listed,
// in the order they appear, from its "- path: <path>" lines.
func parseIngestPaths(msg string) []string {
	var paths []string
	for _, line := range strings.Split(msg, "\n") {
		if p, ok := strings.CutPrefix(line, "- path: "); ok {
			paths = append(paths, p)
		}
	}
	return paths
}

// findIngestOp returns the one stage.OpIngestSource op in cs, failing the
// test if there is none.
func findIngestOp(t *testing.T, cs *stage.Changeset) stage.Op {
	t.Helper()
	for _, op := range cs.Ops {
		if op.Kind == stage.OpIngestSource {
			return op
		}
	}
	t.Fatalf("changeset %s has no ingest_source op; ops=%+v", cs.ID, cs.Ops)
	return stage.Op{}
}

// findFileDiffNew returns the projected New content stage.Engine.Diff
// computed for path, failing the test if path is not among the diff's
// files — this is the exact bytes stage.ingest_source proposed staging,
// read back the same way `lw diff` would.
func findFileDiffNew(t *testing.T, d stage.Diff, path string) string {
	t.Helper()
	for _, f := range d.Files {
		if f.Path == path {
			return f.New
		}
	}
	t.Fatalf("diff has no file entry for %s; files=%+v", path, d.Files)
	return ""
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
	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
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

	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		t.Fatal("newIngestAgent should never be called when extraction fails")
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

// countChangesets returns how many entries sit under
// <root>/.llmwiki/changesets/<state> — one of "open", "committed" or
// "rejected" — used to pin C-113's fix: a failed ingest must reject what it
// opened rather than leave it stuck.
func countChangesets(t *testing.T, root, state string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".llmwiki", "changesets", state))
	if err != nil {
		t.Fatalf("read changesets/%s: %v", state, err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

// TestCmdIngestAgentErrorRejectsChangeset is S5-T6's regression test for
// C-113: G5's live run measured cmdIngest opening its changeset before the
// agent turn and never rolling it back, so a mid-turn failure left an
// orphan in changesets/open/ that bricked every subsequent `lw ingest`
// with "stage: a changeset is already open" (there is no CLI verb to clear
// one — TD-7). A failed turn must instead land the attempt in
// changesets/rejected/ — never delete it — leaving zero open and exactly
// one rejected changeset behind.
func TestCmdIngestAgentErrorRejectsChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	localSrc := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(localSrc, []byte("# Note\n\nBody.\n"), 0o644); err != nil {
		t.Fatalf("write local source: %v", err)
	}

	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
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

	if got := countChangesets(t, root, "open"); got != 0 {
		t.Errorf("open changesets = %d, want 0 (the failed attempt must be rejected, not left open)", got)
	}
	if got := countChangesets(t, root, "rejected"); got != 1 {
		t.Errorf("rejected changesets = %d, want exactly 1", got)
	}
	if got := countChangesets(t, root, "committed"); got != 0 {
		t.Errorf("committed changesets = %d, want 0 (ingest never commits)", got)
	}

	// A second ingest attempt must not be bricked by the first's failure —
	// exactly the symptom C-113 measured live: "stage: a changeset is
	// already open" on retry.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	if _, err := e.Current(); err == nil {
		t.Fatal("Current: want no open changeset after the rejection, got one")
	}
}

// errBoom is a fixed sentinel so assertions can match its message exactly.
var errBoom = errors.New("boom")

// TestCmdIngestSourceURLLocalFile is C-123's regression test: a real
// `lw ingest <local file>` run, driven end to end through the real
// stage.ingest_source tool handler (toolCallingFakeAgent, no LLM and no
// network), must record the raw file's source_url as the ORIGINAL
// argument's absolute path — never the scratch path cmdIngest wrote it to
// and later deletes. The argument given on the command line is
// deliberately relative, so a fix that merely forwarded the argument
// unchanged (already correct for an absolute path by coincidence) would
// not pass this.
func TestCmdIngestSourceURLLocalFile(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	localSrc := filepath.Join(t.TempDir(), "gemini-note.md")
	if err := os.WriteFile(localSrc, []byte("# A Gemini Note\n\nSome body text about kv-cache.\n"), 0o644); err != nil {
		t.Fatalf("write local source: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	relSrc, err := filepath.Rel(cwd, localSrc)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if relSrc == localSrc {
		t.Fatalf("relSrc = %q, want it to differ from the absolute path so this test exercises the abs-path correction", relSrc)
	}

	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		return newToolCallingFakeAgent(e, cfg, sessions, ex), nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, relSrc})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
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
	op := findIngestOp(t, cs)

	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	content := findFileDiffNew(t, d, op.Path)

	want := fmt.Sprintf("source_url: %s\n", localSrc)
	if !strings.Contains(content, want) {
		t.Fatalf("staged raw file %s does not contain %q; got:\n%s", op.Path, want, content)
	}
	if strings.Contains(content, "lw-ingest-") {
		t.Fatalf("staged raw file %s still names the deleted scratch directory; got:\n%s", op.Path, content)
	}
}

// TestCmdIngestSourceURLRemote is C-123's regression test for a URL
// source: `lw ingest <url>` served by a local httptest server (no live
// network to any provider) must record source_url as that exact URL, not
// the scratch path stage.ingest_source's re-extraction would otherwise
// see.
func TestCmdIngestSourceURLRemote(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><h1>Remote Note</h1><p>Some body text about kv-cache.</p></body></html>")
	}))
	defer srv.Close()

	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		return newToolCallingFakeAgent(e, cfg, sessions, ex), nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, srv.URL})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
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
	op := findIngestOp(t, cs)

	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	content := findFileDiffNew(t, d, op.Path)

	want := fmt.Sprintf("source_url: %s\n", srv.URL)
	if !strings.Contains(content, want) {
		t.Fatalf("staged raw file %s does not contain %q; got:\n%s", op.Path, want, content)
	}
}

// TestPreExtractedResolvesScratchPathToStagedDoc unit-tests preExtracted
// directly (independent of the whole CLI plumbing exercised above): the
// exact Extractor contract stage.ingest_source's Deps.Extract relies on
// for C-123's fix — CanHandle true only for staged paths, Extract
// returning an independent copy of the staged Doc, and a clear error for
// an unstaged path.
func TestPreExtractedResolvesScratchPathToStagedDoc(t *testing.T) {
	pre := newPreExtracted()
	doc := extract.Doc{
		Title:     "T",
		SourceURL: "https://example.com/a",
		Markdown:  "# T\n\nBody.\n",
		Kind:      "article",
		Extractor: "go/html",
	}
	pre.stage("/tmp/scratch/01-t.md", doc)

	if !pre.CanHandle("/tmp/scratch/01-t.md") {
		t.Error("CanHandle(staged path) = false, want true")
	}
	if pre.CanHandle("/tmp/scratch/02-other.md") {
		t.Error("CanHandle(unstaged path) = true, want false")
	}

	got, err := pre.Extract(context.Background(), "/tmp/scratch/01-t.md")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if *got != doc {
		t.Fatalf("Extract() = %+v, want %+v", *got, doc)
	}

	// The returned *Doc must be an independent copy: mutating it must not
	// alter what a second Extract call on the same path returns.
	got.SourceURL = "mutated"
	got2, err := pre.Extract(context.Background(), "/tmp/scratch/01-t.md")
	if err != nil {
		t.Fatalf("second Extract: %v", err)
	}
	if got2.SourceURL != doc.SourceURL {
		t.Fatalf("second Extract().SourceURL = %q, want %q (unaffected by the first caller's mutation)", got2.SourceURL, doc.SourceURL)
	}

	if _, err := pre.Extract(context.Background(), "/tmp/scratch/nope.md"); err == nil {
		t.Fatal("Extract(unstaged path) error = nil, want non-nil")
	}
}

func TestIsURLSource(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{"https://example.com/a", true},
		{"http://example.com/a", true},
		{"/tmp/lw-ingest-1234/01-note.md", false},
		{"note.md", false},
		{"ftp://example.com/a", false},
	}
	for _, c := range cases {
		if got := isURLSource(c.src); got != c.want {
			t.Errorf("isURLSource(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}

// TestBuildIngestMessageStatesChangesetAlreadyOpen pins the second half of
// C-123's live observation: cmdIngest opens the changeset before this
// message is ever sent (see cmdIngest), so the agent's first instinct to
// call stage.open fails with "a changeset is already open" — harmless, but
// avoidable. The message must say so, name stage.ingest_source's own
// result as the way to find the raw/ path for raw.get, and keep the
// "original source:" line the agent needs to write correct provenance.
func TestBuildIngestMessageStatesChangesetAlreadyOpen(t *testing.T) {
	items := []ingestItem{{path: "/tmp/x/01-note.md", kind: "article", title: "Note", source: "note.md"}}
	msg := buildIngestMessage(items)

	if !strings.Contains(msg, "already open") {
		t.Errorf("buildIngestMessage() = %q, want it to state the changeset is already open", msg)
	}
	if !strings.Contains(msg, "stage.open") {
		t.Errorf("buildIngestMessage() = %q, want it to name stage.open", msg)
	}
	if !strings.Contains(msg, "raw.get") {
		t.Errorf("buildIngestMessage() = %q, want it to mention raw.get for reading back the staged source", msg)
	}
	if !strings.Contains(msg, "original source: note.md") {
		t.Errorf("buildIngestMessage() = %q, want it to keep the \"original source:\" line", msg)
	}
}

// scriptedEventAgent is a fake agent.Agent whose Send emits a fixed script
// of agent.Event values, then a DoneEv, then closes out — used to drive
// runAgentTurn directly (S6-C130) without any real LLM or stage.Engine
// involved. A plain string in the script is shorthand for
// agent.TextDelta{Text: s}.
type scriptedEventAgent struct {
	script []agent.Event
}

// newScriptedEventAgent builds a scriptedEventAgent from a mix of
// agent.Event values and bare strings (each turned into an
// agent.TextDelta), finishing the script with agent.DoneEv.
func newScriptedEventAgent(items ...any) *scriptedEventAgent {
	script := make([]agent.Event, 0, len(items)+1)
	for _, it := range items {
		if s, ok := it.(string); ok {
			script = append(script, agent.TextDelta{Text: s})
			continue
		}
		script = append(script, it.(agent.Event))
	}
	script = append(script, agent.DoneEv{Reason: "stop", Rounds: 1})
	return &scriptedEventAgent{script: script}
}

func (f *scriptedEventAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	defer close(out)
	for _, ev := range f.script {
		select {
		case out <- ev:
		case <-ctx.Done():
			return nil
		}
	}
	return nil
}

func (f *scriptedEventAgent) Sessions() agent.SessionStore { return nil }

// TestRunAgentTurnSeparatesRounds is S6-C130's regression test: text from
// two different agent rounds — separated by at least one tool event — must
// land on its own paragraph, never run together on one line the way a live
// URL ingest did ("...orientation ritual.The vault is empty..."). Deltas
// within one round, with no tool event between them, must stay exactly as
// they were before this fix.
func TestRunAgentTurnSeparatesRounds(t *testing.T) {
	toolCall := agent.ToolCallEv{ID: "1", Name: "stage.ingest_source", Args: "{}"}
	toolRes := agent.ToolResEv{ID: "1", Name: "stage.ingest_source", Content: "ok"}

	cases := []struct {
		name   string
		script []any
		want   string
	}{
		{"deltas_within_one_round_untouched", []any{"Hel", "lo"}, "Hello"},
		{"tool_between_rounds_no_newline", []any{"A.", toolCall, toolRes, "B."}, "A.\n\nB."},
		{"tool_between_rounds_one_newline", []any{"A.\n", toolCall, "B."}, "A.\n\nB."},
		{"tool_between_rounds_already_blank", []any{"A.\n\n", toolRes, "B."}, "A.\n\nB."},
		{"no_leading_separator_before_first_text", []any{toolCall, toolRes, "B."}, "B."},
		{"empty_delta_changes_nothing", []any{"A.", toolCall, "", "B"}, "A.\n\nB"},
		{"two_tool_gaps_two_separators", []any{"A.", toolCall, "B.", toolCall, "C."}, "A.\n\nB.\n\nC."},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ag := newScriptedEventAgent(c.script...)
			var buf strings.Builder
			err := runAgentTurn(context.Background(), ag, "sess-1", "go", &buf)
			if err != nil {
				t.Fatalf("runAgentTurn: %v", err)
			}
			if got := buf.String(); got != c.want {
				t.Errorf("runAgentTurn() output = %q, want %q", got, c.want)
			}
		})
	}
}

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

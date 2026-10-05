package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/trace"
	"github.com/awepo-pro/lw/internal/vault"
)

// helperEnv makes the test binary act as a stand-in lw instead of running
// tests: TestRunnerOSExec points Runner.LW at os.Args[0] and sets it, so the
// default (os/exec) ExecFunc runs a real subprocess without any dependence
// on a built lw or a shell.
const helperEnv = "EVAL_TEST_HELPER_LW"

// helperGuardEnv is set (process-wide) by TestRunnerOSExec for its whole
// run. A child that carries it but not helperEnv is the test binary started
// as "lw" without the Runner.Env that makes it a stand-in — a Runner that
// dropped its Env — and must stop at once instead of running the suite again
// and spawning the next one.
const helperGuardEnv = "EVAL_TEST_HELPER_GUARD"

// TestMain keeps the engine's INFO records (vault open, index load) out of
// the test binary's stderr: every fake lw run opens a few engines. It also
// serves the helper-process mode helperEnv switches on.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) != "" {
		os.Exit(helperLW(os.Args[1:]))
	}
	if os.Getenv(helperGuardEnv) != "" {
		fmt.Fprintln(os.Stderr, "eval test binary started as lw without "+helperEnv+": the Runner lost its Env")
		os.Exit(3)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// helperLW is the stand-in lw of TestRunnerOSExec: `version` prints a
// version, `query --vault <dir> <q>` answers on stdout, reports on stderr
// what it can see of its environment and exits 7 for the question "fail".
func helperLW(args []string) int {
	switch {
	case len(args) == 1 && args[0] == "version":
		fmt.Println("lw vhelper-1.2.3")
		return 0
	case len(args) == 4 && args[0] == "query" && args[1] == "--vault":
		if _, err := os.Stat(filepath.Join(args[2], "SCHEMA.md")); err != nil {
			fmt.Fprintln(os.Stderr, "no vault:", err)
			return 2
		}
		fmt.Printf("answer to %q\n", args[3])
		fmt.Fprintf(os.Stderr, "parent=%s variant=%s\n", os.Getenv("EVAL_TEST_PARENT"), os.Getenv("EVAL_TEST_VARIANT"))
		if args[3] == "fail" {
			return 7
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "helper lw: unexpected args", args)
	return 2
}

// runSetCases is the cases.toml every runner test shares: three runnable
// cases (two ask, one ingest) and two holdout cases the default run must
// skip. The snapshot hash is spliced in by newRunSet.
const runSetCases = `version = 1
snapshot = "vault-20261005.tar.gz"
snapshot_sha256 = "%s"

[[ask]]
id = "kv-cache"
kind = "covered"
q = "How does the KV cache work?"
facts = [["keys"], ["values"]]
cite_any = ["wiki/concepts/kv-cache.md"]

[[ask]]
id = "no-topic"
kind = "absent"
q = "Who won the 2030 cup?"

[[ask]]
id = "held-ask"
kind = "multi-hop"
q = "How do flash attention and the KV cache interact?"
facts = [["memory"]]
holdout = true

[[ingest]]
id = "paper-text"
input = "inputs/paper.txt"
facts = [["speculative"]]

[[ingest]]
id = "held-ingest"
input = "inputs/paper.txt"
facts = [["x"]]
holdout = true
`

// baselineOrphan is a page the snapshot vault already carries that nothing
// links to: lint reports it on every projected report, on a path no ingest
// stages — the baseline noise lint.json must leave out.
const baselineOrphan = "---\ntitle: Baseline Orphan\ncreated: 2026-08-29\nupdated: 2026-08-29\ntype: concept\n" +
	"tags: [inference]\nconfidence: medium\n---\n\n# Baseline Orphan\n\nSee [[kv-cache]] and [[gpt-4]]; nothing links back here.\n"

// newRunSet snapshots a synthetic vault (a private copy of the minimal
// fixture plus one orphan page, opened once so it carries a real .llmwiki/,
// plus trace and log files the snapshot must drop) into a set directory and
// loads it.
func newRunSet(t *testing.T) *Set {
	t.Helper()
	vaultDir := testutil.CopyFixture(t, "minimal")
	if err := os.WriteFile(filepath.Join(vaultDir, "wiki", "concepts", "baseline-orphan.md"), []byte(baselineOrphan), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := stage.OpenEngine(vaultDir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".llmwiki/traces/20260101T000000Z-aaaa/events.ndjson", ".llmwiki/logs/lw.log"} {
		full := filepath.Join(vaultDir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("not part of the snapshot\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	setDir := t.TempDir()
	sha, err := Snapshot(vaultDir, filepath.Join(setDir, "vault-20261005.tar.gz"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(setDir, "inputs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setDir, "inputs", "paper.txt"), []byte("Speculative decoding drafts tokens.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setDir, "cases.toml"), []byte(fmt.Sprintf(runSetCases, sha)), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := LoadSet(setDir)
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}
	return set
}

// evidence is what the fake lw left in one scratch vault, captured before
// the runner removes it — the oracle the artifacts are compared with.
type evidence struct {
	turnID     string
	traceFiles map[string][]byte // path relative to the turn dir -> bytes
	csID       string
	staged     map[string][]byte // StagedFile bytes by path
	report     lint.Report
}

// fakeLW stands in for the lw binary: an ExecFunc that writes what the real
// one would leave in the scratch vault — one trace through the real
// trace.Start, and for ingest a changeset through the real stage engine.
type fakeLW struct {
	t       *testing.T
	version string // stdout of `lw version`

	// exit decides one attempt's exit code; nil means every attempt exits 0.
	exit func(key string, attempt int) int
	// rendezvous makes the first two job calls wait for each other, so a
	// test can prove two sessions really ran at once.
	rendezvous bool
	// hold keeps every job call open this long, widening the window in
	// which an over-wide pool would show.
	hold time.Duration

	seq atomic.Int64

	mu        sync.Mutex
	calls     []Cmd
	attempts  map[string]int
	times     map[string][]time.Time // call start times per key
	evidence  map[string]*evidence   // last attempt per key
	turnIDs   map[string][]string    // every attempt's turn id per key
	active    int
	maxActive int
	arrived   int
}

func newFakeLW(t *testing.T) *fakeLW {
	return &fakeLW{
		t: t, version: "lw v9.9.9-test\n",
		attempts: map[string]int{}, times: map[string][]time.Time{},
		evidence: map[string]*evidence{}, turnIDs: map[string][]string{},
	}
}

// Exec is the ExecFunc.
func (f *fakeLW) Exec(ctx context.Context, c Cmd) (Output, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if len(c.Args) == 1 && c.Args[0] == "version" {
		return Output{Stdout: []byte(f.version)}, nil
	}
	if len(c.Args) != 4 || c.Args[1] != "--vault" {
		f.t.Errorf("unexpected lw args %q", c.Args)
		return Output{ExitCode: 2}, nil
	}
	verb, scratch, arg := c.Args[0], c.Args[2], c.Args[3]
	key := filepath.Base(scratch)

	f.mu.Lock()
	f.attempts[key]++
	attempt := f.attempts[key]
	f.times[key] = append(f.times[key], time.Now())
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	f.arrived++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.rendezvous {
		f.meet()
	}
	if f.hold > 0 {
		time.Sleep(f.hold)
	}

	// The scratch the runner hands over is a pristine extraction of the
	// snapshot: the vault is there, and nothing from an earlier attempt or
	// from the live vault's traces came along.
	if _, err := os.Stat(filepath.Join(scratch, "SCHEMA.md")); err != nil {
		f.t.Errorf("%s: scratch vault lacks SCHEMA.md: %v", key, err)
	}
	if _, err := os.Stat(filepath.Join(scratch, ".llmwiki", "traces")); !os.IsNotExist(err) {
		f.t.Errorf("%s attempt %d: scratch already holds .llmwiki/traces (%v)", key, attempt, err)
	}

	ev := &evidence{}
	ev.turnID, ev.traceFiles = f.writeTrace(ctx, scratch, verb)
	if verb == "ingest" {
		f.stageIngest(scratch, arg, ev)
	}

	f.mu.Lock()
	f.evidence[key] = ev
	f.turnIDs[key] = append(f.turnIDs[key], ev.turnID)
	f.mu.Unlock()

	code := 0
	if f.exit != nil {
		code = f.exit(key, attempt)
	}
	return Output{
		Stdout:   []byte(fmt.Sprintf("answer %s attempt %d\n", key, attempt)),
		Stderr:   []byte(fmt.Sprintf("stderr %s attempt %d\n", key, attempt)),
		ExitCode: code,
	}, nil
}

// meet blocks until two job calls have arrived, failing the test when the
// second never comes (a pool that cannot run two at once).
func (f *fakeLW) meet() {
	deadline := time.After(10 * time.Second)
	for {
		f.mu.Lock()
		n := f.arrived
		f.mu.Unlock()
		if n >= 2 {
			return
		}
		select {
		case <-deadline:
			f.t.Error("two job calls never overlapped")
			return
		case <-time.After(time.Millisecond):
		}
	}
}

// writeTrace records one turn through the real trace package, with a
// request body that is json.Marshal of an llm-shaped request, and returns
// the turn id and every file of the finished turn dir.
func (f *fakeLW) writeTrace(ctx context.Context, scratch, verb string) (string, map[string][]byte) {
	f.t.Helper()
	dir := filepath.Join(scratch, ".llmwiki", "traces")
	id := trace.NewID(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Add(time.Duration(f.seq.Add(1)) * time.Second))
	_, rec := trace.Start(ctx, dir, id, trace.Meta{
		Verb: verb, Session: "query", Version: "v9.9.9-test", Model: "glm-test",
		Server: "api.example.test", Thinking: "off", MaxRounds: 6, ContextTokens: 32000,
	}, 0)
	if rec == nil {
		f.t.Fatalf("trace.Start failed under %s", dir)
	}
	body, err := json.Marshal(struct {
		Model    string        `json:"model"`
		Messages []llm.Message `json:"messages"`
		Stream   bool          `json:"stream"`
	}{
		Model: "glm-test", Stream: true,
		Messages: []llm.Message{{Role: "system", Content: "You are the curator."}, {Role: "user", Content: "question"}},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	rec.BeginRequest(1, 1, 2, 0)
	rec.Request(body)
	rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "stop", Text: "the answer", Usage: &llm.Usage{InputTokens: 100, OutputTokens: 10}})
	rec.Done(trace.Done{Reason: "stop", Rounds: 1, WallMS: 5})
	return id, readDirBytes(f.t, filepath.Join(dir, id))
}

// stageIngest leaves an open changeset in scratch the way `lw ingest`
// does: a raw source, a page created from it and patched again (two ops on
// one path), a page that is created and then dropped, and a patch of a
// vault-root file — so the artifacts have to pick the right paths and the
// right state.
func (f *fakeLW) stageIngest(scratch, input string, ev *evidence) {
	f.t.Helper()
	e, err := stage.OpenEngine(scratch)
	if err != nil {
		f.t.Fatalf("OpenEngine(%s): %v", scratch, err)
	}
	defer e.Close()
	cs, err := e.OpenChangeset("ingest "+filepath.Base(input), stage.Author{Kind: "agent", Model: "glm-test"})
	if err != nil {
		f.t.Fatalf("OpenChangeset (a retry must start from a clean scratch): %v", err)
	}
	ev.csID = cs.ID

	d, _ := vault.ParseDate("2026-10-05")
	raw := (&vault.RawSource{
		SourceURL: "https://example.test/new-source", Ingested: d,
		SHA256: vault.BodySHA256("Fake ingested body.\n"), Body: "Fake ingested body.\n",
	}).Serialize()
	mustAppend := func(op stage.Op) string {
		id, err := e.Append(op)
		if err != nil {
			f.t.Fatalf("Append %s %s: %v", op.Kind, op.Path, err)
		}
		return id
	}
	mustAppend(stage.Op{Kind: stage.OpIngestSource, Path: "raw/articles/new-source.md", Extractor: "go/text", Content: raw})

	page := func(title, notes string) []byte {
		return []byte("---\ntitle: " + title + "\ncreated: 2026-10-05\nupdated: 2026-10-05\ntype: concept\n" +
			"tags: [inference]\nconfidence: medium\n---\n\n# " + title + "\n\nSee [[kv-cache]] and [[gpt-4]].\n\n## Notes\n\n" + notes)
	}
	const newPage = "wiki/concepts/new-page.md"
	first := page("New Page", "First note.\n")
	mustAppend(stage.Op{
		Kind: stage.OpCreatePage, Path: newPage, Content: first,
		Rationale: "the source covers it", Provenance: []string{"raw/articles/new-source.md"},
	})
	parsed, err := vault.ParsePage(newPage, first)
	if err != nil {
		f.t.Fatal(err)
	}
	patched := *parsed
	patched.Body += "\nSecond note.\n"
	mustAppend(stage.Op{
		Kind: stage.OpPatchPage, Path: newPage, Section: "## Notes", Before: parsed.SHA256(), Content: patched.Serialize(),
		Hunks: []stage.Hunk{{ID: "h1", Path: newPage, Section: "## Notes", Add: []string{"Second note."}}},
	})
	scrap := mustAppend(stage.Op{
		Kind: stage.OpCreatePage, Path: "wiki/concepts/scrap.md", Content: page("Scrap", "Not wanted.\n"),
		Rationale: "second thoughts", Provenance: []string{"raw/articles/new-source.md"},
	})
	if err := e.DropOp(scrap); err != nil {
		f.t.Fatalf("DropOp: %v", err)
	}
	memory, err := os.ReadFile(filepath.Join(scratch, "curator-memory.md"))
	if err != nil {
		f.t.Fatal(err)
	}
	mustAppend(stage.Op{
		Kind: stage.OpPatchPage, Path: "curator-memory.md", Before: vault.BodySHA256(string(memory)),
		Content: append(slices.Clone(memory), []byte("- Keep notes short. (2026-10-05)\n")...),
		Hunks:   []stage.Hunk{{ID: "h1", Path: "curator-memory.md", Add: []string{"- Keep notes short. (2026-10-05)"}}},
	})

	ev.staged = map[string][]byte{}
	for _, p := range []string{"raw/articles/new-source.md", newPage, "wiki/concepts/scrap.md", "curator-memory.md"} {
		if b, ok, err := e.StagedFile(p); err != nil {
			f.t.Fatal(err)
		} else if ok {
			ev.staged[p] = b
		}
	}
	if ev.report, err = e.ProjectedReport(); err != nil {
		f.t.Fatalf("ProjectedReport: %v", err)
	}
}

// readDirBytes returns every file under dir by slash path relative to dir.
func readDirBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	return out
}

// listDir returns the sorted names directly inside dir.
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// stepClock is an injected clock: every read advances one second, safe for
// the runner's concurrent jobs.
func stepClock() func() time.Time {
	var mu sync.Mutex
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Second)
		return now
	}
}

func readJSON(t *testing.T, path string, v any) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return b
}

func newRunner(set *Set, f *fakeLW, out string) *Runner {
	return &Runner{
		LW: "lw-fake", Set: set, N: 1, Parallel: 1, Out: out, Exec: f.Exec,
		Backoff: time.Millisecond, now: stepClock(),
	}
}

// TestRunnerArtifacts pins 037 T1 C3 end to end: the commands the runner
// issues, and every artifact one run leaves — meta, stdout/stderr, a byte
// copy of the traces, and for ingest the changeset, the staged files and
// the filtered lint report — with no scratch vault left behind.
func TestRunnerArtifacts(t *testing.T) {
	set := newRunSet(t)
	f := newFakeLW(t)
	out := filepath.Join(t.TempDir(), "runs", "20261005T120000Z-v9.9.9-test")
	r := newRunner(set, f, out)
	r.N, r.Parallel = 2, 2
	r.Note = "thinking=on"
	r.Env = []string{"EVAL_VARIANT=secret-variant-value"}

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 1 `lw version` + (2 ask + 1 ingest) x N=2 job commands.
	if len(f.calls) != 7 {
		t.Fatalf("%d lw invocations, want 7: %v", len(f.calls), f.calls)
	}
	if v := f.calls[0]; v.Path != "lw-fake" || !reflect.DeepEqual(v.Args, []string{"version"}) {
		t.Errorf("first call = %+v, want `lw version`", v)
	}
	for _, c := range f.calls {
		if c.Path != "lw-fake" {
			t.Errorf("call path %q, want the Runner.LW", c.Path)
		}
		if n := len(c.Env); n == 0 || c.Env[n-1] != "EVAL_VARIANT=secret-variant-value" {
			t.Errorf("Runner.Env is not the last Env entry of %v", c.Args)
		}
		if !slices.ContainsFunc(c.Env, func(e string) bool { return strings.HasPrefix(e, "PATH=") }) {
			t.Errorf("the parent environment is not inherited by %v", c.Args)
		}
	}

	type want struct {
		id, verb, kind, arg string
	}
	cases := []want{
		{"kv-cache", "query", "covered", "How does the KV cache work?"},
		{"no-topic", "query", "absent", "Who won the 2030 cup?"},
		{"paper-text", "ingest", "ingest", filepath.Join(set.Dir, "inputs", "paper.txt")},
	}
	if got := listDir(t, out); !reflect.DeepEqual(got, []string{"kv-cache", "no-topic", "paper-text", "run.json"}) {
		t.Fatalf("run dir holds %v (a .work dir or a holdout case leaked?)", got)
	}

	var run RunInfo
	runRaw := readJSON(t, filepath.Join(out, "run.json"), &run)
	if strings.Contains(string(runRaw), "secret-variant-value") {
		t.Error("run.json leaks an Env value; only names may be recorded")
	}
	caseToml, err := os.ReadFile(filepath.Join(set.Dir, "cases.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wantRun := RunInfo{
		ID: "20261005T120000Z-v9.9.9-test", LW: "lw-fake", LWVersion: "lw v9.9.9-test",
		SetSHA256: sha256Hex(caseToml), SnapshotSHA256: set.SnapshotSHA256,
		N: 2, Parallel: 2, Note: "thinking=on", EnvKeys: []string{"EVAL_VARIANT"},
		Started: run.Started, Finished: run.Finished,
	}
	if !reflect.DeepEqual(run, wantRun) {
		t.Errorf("run.json\n got %+v\nwant %+v", run, wantRun)
	}
	if run.Started.IsZero() || !run.Finished.After(run.Started) || run.Started.Location() != time.UTC {
		t.Errorf("run.json times: started %v finished %v (want UTC, finished after started)", run.Started, run.Finished)
	}

	for _, c := range cases {
		for i := 1; i <= 2; i++ {
			key := c.id + "-" + strconv.Itoa(i)
			dir := filepath.Join(out, c.id, strconv.Itoa(i))
			scratch := filepath.Join(out, ".work", key)
			ev := f.evidence[key]
			if ev == nil {
				t.Fatalf("%s: the fake never ran", key)
			}

			var call *Cmd
			for j := range f.calls {
				if len(f.calls[j].Args) == 4 && f.calls[j].Args[2] == scratch {
					call = &f.calls[j]
				}
			}
			if call == nil {
				t.Fatalf("%s: no command ran on scratch %s", key, scratch)
			}
			if want := []string{c.verb, "--vault", scratch, c.arg}; !reflect.DeepEqual(call.Args, want) {
				t.Errorf("%s: args %q, want %q", key, call.Args, want)
			}

			var meta RunMeta
			raw := readJSON(t, filepath.Join(dir, "meta.json"), &meta)
			if !strings.HasPrefix(string(raw), "{\n  \"case\": ") || !strings.HasSuffix(string(raw), "}\n") {
				t.Errorf("%s: meta.json is not 2-space indented JSON with a trailing newline:\n%s", key, raw)
			}
			wantMeta := RunMeta{
				Case: c.id, Verb: c.verb, Kind: c.kind, Index: i, Attempts: 1, ExitCode: 0, Failed: false,
				Started: meta.Started, WallMS: meta.WallMS, Traces: []string{ev.turnID},
			}
			if !reflect.DeepEqual(meta, wantMeta) {
				t.Errorf("%s: meta\n got %+v\nwant %+v", key, meta, wantMeta)
			}
			if meta.Started.Before(run.Started) || meta.Started.After(run.Finished) || meta.WallMS < 0 {
				t.Errorf("%s: meta started %v wall %dms outside the run %v..%v", key, meta.Started, meta.WallMS, run.Started, run.Finished)
			}

			wantOut := fmt.Sprintf("answer %s attempt 1\n", key)
			wantErr := fmt.Sprintf("stderr %s attempt 1\n", key)
			if b, _ := os.ReadFile(filepath.Join(dir, "stdout.txt")); string(b) != wantOut {
				t.Errorf("%s: stdout.txt = %q, want %q", key, b, wantOut)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "stderr.txt")); string(b) != wantErr {
				t.Errorf("%s: stderr.txt = %q, want %q", key, b, wantErr)
			}

			if got := listDir(t, filepath.Join(dir, "traces")); !reflect.DeepEqual(got, []string{ev.turnID}) {
				t.Errorf("%s: traces/ holds %v, want [%s]", key, got, ev.turnID)
			}
			if got := readDirBytes(t, filepath.Join(dir, "traces", ev.turnID)); !reflect.DeepEqual(got, ev.traceFiles) {
				t.Errorf("%s: the copied trace is not a byte copy of the turn dir", key)
			}

			if c.verb == "query" {
				if got := listDir(t, dir); !reflect.DeepEqual(got, []string{"meta.json", "stderr.txt", "stdout.txt", "traces"}) {
					t.Errorf("%s: a query case dir holds %v", key, got)
				}
				continue
			}
			if got := listDir(t, dir); !reflect.DeepEqual(got, []string{"changeset.json", "lint.json", "meta.json", "staged", "stderr.txt", "stdout.txt", "traces"}) {
				t.Errorf("%s: an ingest case dir holds %v", key, got)
			}
			checkIngestArtifacts(t, key, dir, ev)
		}
	}
	if _, err := os.Stat(filepath.Join(out, ".work")); !os.IsNotExist(err) {
		t.Errorf(".work was left behind: %v", err)
	}
}

// checkIngestArtifacts pins the ingest-only artifacts: changeset.json lists
// every op in order (the dropped one too, as dropped), staged/ holds the
// last state of each path under wiki/ or raw/ that is not dropped, and
// lint.json is the projected report cut down to those paths.
func checkIngestArtifacts(t *testing.T, key, dir string, ev *evidence) {
	t.Helper()
	var cs struct {
		ID  string `json:"id"`
		Ops []struct {
			ID      string `json:"id"`
			Op      string `json:"op"`
			Path    string `json:"path"`
			Section string `json:"section"`
			State   string `json:"state"`
		} `json:"ops"`
	}
	raw := readJSON(t, filepath.Join(dir, "changeset.json"), &cs)
	if !strings.HasPrefix(string(raw), "{\n  \"id\": ") {
		t.Errorf("%s: changeset.json is not 2-space indented:\n%s", key, raw)
	}
	if cs.ID != ev.csID {
		t.Errorf("%s: changeset id %q, want %q", key, cs.ID, ev.csID)
	}
	type op struct{ id, op, path, section, state string }
	var gotOps []op
	for _, o := range cs.Ops {
		gotOps = append(gotOps, op{o.ID, o.Op, o.Path, o.Section, o.State})
	}
	wantOps := []op{
		{"op1", "ingest_source", "raw/articles/new-source.md", "", "proposed"},
		{"op2", "create_page", "wiki/concepts/new-page.md", "", "proposed"},
		{"op3", "patch_page", "wiki/concepts/new-page.md", "## Notes", "proposed"},
		{"op4", "create_page", "wiki/concepts/scrap.md", "", "dropped"},
		{"op5", "patch_page", "curator-memory.md", "", "proposed"},
	}
	if !reflect.DeepEqual(gotOps, wantOps) {
		t.Errorf("%s: changeset ops\n got %v\nwant %v", key, gotOps, wantOps)
	}

	wantStaged := map[string][]byte{
		"raw/articles/new-source.md": ev.staged["raw/articles/new-source.md"],
		"wiki/concepts/new-page.md":  ev.staged["wiki/concepts/new-page.md"],
	}
	if len(wantStaged["raw/articles/new-source.md"]) == 0 || !strings.Contains(string(wantStaged["wiki/concepts/new-page.md"]), "Second note.") {
		t.Fatalf("%s: the fake did not stage what the test expects: %v", key, ev.staged)
	}
	if got := readDirBytes(t, filepath.Join(dir, "staged")); !reflect.DeepEqual(got, wantStaged) {
		var names []string
		for p := range got {
			names = append(names, p)
		}
		sort.Strings(names)
		t.Errorf("%s: staged/ holds %v; want exactly the non-dropped wiki/ and raw/ paths with their last state", key, names)
	}

	var lf struct {
		Errors   int `json:"errors"`
		Warns    int `json:"warns"`
		Findings []struct {
			Check    string `json:"check"`
			Path     string `json:"path"`
			Line     int    `json:"line"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"findings"`
	}
	readJSON(t, filepath.Join(dir, "lint.json"), &lf)
	type finding struct {
		check, path, severity, message string
		line                           int
	}
	var got, want []finding
	var wantErrors, wantWarns, dropped int
	for _, fd := range ev.report.Findings {
		if _, ok := wantStaged[fd.Path]; !ok {
			dropped++
			continue
		}
		want = append(want, finding{fd.Check, fd.Path, string(fd.Severity), fd.Message, fd.Line})
		switch fd.Severity {
		case lint.SevError:
			wantErrors++
		case lint.SevWarn:
			wantWarns++
		}
	}
	for _, fd := range lf.Findings {
		got = append(got, finding{fd.Check, fd.Path, fd.Severity, fd.Message, fd.Line})
	}
	if !reflect.DeepEqual(got, want) || lf.Errors != wantErrors || lf.Warns != wantWarns {
		t.Errorf("%s: lint.json\n got errors=%d warns=%d %v\nwant errors=%d warns=%d %v", key, lf.Errors, lf.Warns, got, wantErrors, wantWarns, want)
	}
	if len(want) == 0 || dropped == 0 {
		t.Errorf("%s: the projected report has %d findings on staged paths and %d elsewhere; the filter is untested unless both are > 0", key, len(want), dropped)
	}
}

// TestRunnerRetryOnce pins the one retry: a first attempt that exits
// non-zero is retried once after Backoff, on a clean scratch vault; a case
// that fails twice is recorded as failed with the second attempt's output.
func TestRunnerRetryOnce(t *testing.T) {
	set := newRunSet(t)
	const backoff = 40 * time.Millisecond

	t.Run("fails then succeeds", func(t *testing.T) {
		f := newFakeLW(t)
		f.exit = func(key string, attempt int) int {
			if key == "kv-cache-1" && attempt == 1 {
				return 1
			}
			return 0
		}
		out := filepath.Join(t.TempDir(), "run")
		r := newRunner(set, f, out)
		r.Only = "kv-cache"
		r.Backoff = backoff
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		var meta RunMeta
		readJSON(t, filepath.Join(out, "kv-cache", "1", "meta.json"), &meta)
		if meta.Attempts != 2 || meta.Failed || meta.ExitCode != 0 {
			t.Errorf("meta = %+v, want Attempts 2, Failed false, ExitCode 0", meta)
		}
		if b, _ := os.ReadFile(filepath.Join(out, "kv-cache", "1", "stdout.txt")); string(b) != "answer kv-cache-1 attempt 2\n" {
			t.Errorf("stdout.txt = %q, want the second attempt's", b)
		}
		ts := f.times["kv-cache-1"]
		if len(ts) != 2 {
			t.Fatalf("%d attempts ran, want 2", len(ts))
		}
		if gap := ts[1].Sub(ts[0]); gap < backoff {
			t.Errorf("retry came %v after the failure, want at least the %v Backoff", gap, backoff)
		}
		// Only the retry's own turn is in the artifacts: its scratch was
		// rebuilt, so the failed attempt's trace is not copied.
		ids := f.turnIDs["kv-cache-1"]
		if len(ids) != 2 || !reflect.DeepEqual(meta.Traces, ids[1:]) {
			t.Errorf("meta.Traces = %v, want just the retry's turn %v", meta.Traces, ids[1:])
		}
	})

	t.Run("fails twice", func(t *testing.T) {
		f := newFakeLW(t)
		f.exit = func(key string, attempt int) int {
			if key == "kv-cache-1" {
				return 1 + attempt // 2, then 3: tells the attempts apart
			}
			return 0
		}
		out := filepath.Join(t.TempDir(), "run")
		r := newRunner(set, f, out)
		r.Only = "kv-cache"
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: a failed case is data, not a Run error: %v", err)
		}
		var meta RunMeta
		readJSON(t, filepath.Join(out, "kv-cache", "1", "meta.json"), &meta)
		if meta.Attempts != 2 || !meta.Failed || meta.ExitCode != 3 {
			t.Errorf("meta = %+v, want Attempts 2, Failed true, ExitCode 3 (the last attempt's)", meta)
		}
		if b, _ := os.ReadFile(filepath.Join(out, "kv-cache", "1", "stderr.txt")); string(b) != "stderr kv-cache-1 attempt 2\n" {
			t.Errorf("stderr.txt = %q, want the second attempt's", b)
		}
		if n := len(f.times["kv-cache-1"]); n != 2 {
			t.Errorf("%d attempts ran, want exactly 2 (one retry, never more)", n)
		}
	})

	t.Run("first attempt succeeds", func(t *testing.T) {
		f := newFakeLW(t)
		out := filepath.Join(t.TempDir(), "run")
		r := newRunner(set, f, out)
		r.Only = "no-topic"
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		var meta RunMeta
		readJSON(t, filepath.Join(out, "no-topic", "1", "meta.json"), &meta)
		if meta.Attempts != 1 || meta.Failed {
			t.Errorf("meta = %+v, want Attempts 1, Failed false", meta)
		}
	})

	t.Run("an ingest retry starts without the failed attempt's changeset", func(t *testing.T) {
		f := newFakeLW(t)
		f.exit = func(key string, attempt int) int {
			if attempt == 1 {
				return 1 // leaves an open changeset behind, like a crashed ingest
			}
			return 0
		}
		out := filepath.Join(t.TempDir(), "run")
		r := newRunner(set, f, out)
		r.Only = "paper-text"
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err) // the fake fails the test itself if OpenChangeset finds a leftover
		}
		var meta RunMeta
		readJSON(t, filepath.Join(out, "paper-text", "1", "meta.json"), &meta)
		if meta.Attempts != 2 || meta.Failed {
			t.Errorf("meta = %+v, want Attempts 2, Failed false", meta)
		}
	})
}

// ranCases returns the case ids the fake saw, sorted and de-duplicated.
func ranCases(f *fakeLW) []string {
	seen := map[string]bool{}
	for key := range f.attempts {
		seen[key[:strings.LastIndex(key, "-")]] = true
	}
	var ids []string
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// TestRunnerOnlyAndHoldout pins case selection: Only narrows to a verb or
// to one case id, and a holdout case runs only when Holdout is set.
func TestRunnerOnlyAndHoldout(t *testing.T) {
	set := newRunSet(t)
	tests := []struct {
		name    string
		only    string
		holdout bool
		want    []string
		wantErr string
	}{
		{"everything but holdouts", "", false, []string{"kv-cache", "no-topic", "paper-text"}, ""},
		{"everything with holdouts", "", true, []string{"held-ask", "held-ingest", "kv-cache", "no-topic", "paper-text"}, ""},
		{"only ask", "ask", false, []string{"kv-cache", "no-topic"}, ""},
		{"only ask with holdouts", "ask", true, []string{"held-ask", "kv-cache", "no-topic"}, ""},
		{"only ingest", "ingest", false, []string{"paper-text"}, ""},
		{"one case", "no-topic", false, []string{"no-topic"}, ""},
		{"one holdout case with Holdout", "held-ask", true, []string{"held-ask"}, ""},
		{"one holdout case without Holdout", "held-ask", false, nil, `case "held-ask" is a holdout case`},
		{"unknown case", "zzz", false, nil, `only "zzz": no such case`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeLW(t)
			out := filepath.Join(t.TempDir(), "run")
			r := newRunner(set, f, out)
			r.Only, r.Holdout = tc.only, tc.holdout
			err := r.Run(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Run = %v, want an error containing %q", err, tc.wantErr)
				}
				if len(f.attempts) != 0 {
					t.Errorf("Run ran %v despite refusing", ranCases(f))
				}
				return
			}
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := ranCases(f); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ran %v, want %v", got, tc.want)
			}
			var run RunInfo
			readJSON(t, filepath.Join(out, "run.json"), &run)
			if run.Only != tc.only || run.Holdout != tc.holdout {
				t.Errorf("run.json only=%q holdout=%v, want %q %v", run.Only, run.Holdout, tc.only, tc.holdout)
			}
		})
	}
}

// TestRunnerParallelBound pins the z.ai concurrency ceiling: Parallel 3 is
// refused before anything runs, and Parallel 2 really runs two at once
// without ever running three.
func TestRunnerParallelBound(t *testing.T) {
	set := newRunSet(t)

	t.Run("three is refused", func(t *testing.T) {
		f := newFakeLW(t)
		out := filepath.Join(t.TempDir(), "run")
		r := newRunner(set, f, out)
		r.Parallel = 3
		err := r.Run(context.Background())
		const want = "parallel 3: at most 2 (z.ai 429s at 3 concurrent sessions)"
		if err == nil || err.Error() != want {
			t.Fatalf("Run = %v, want exactly %q", err, want)
		}
		if len(f.calls) != 0 {
			t.Errorf("Run issued %d commands despite refusing", len(f.calls))
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Errorf("Run created %s despite refusing", out)
		}
	})

	t.Run("three is refused whatever else is unset", func(t *testing.T) {
		err := (&Runner{Parallel: 3}).Run(context.Background())
		const want = "parallel 3: at most 2 (z.ai 429s at 3 concurrent sessions)"
		if err == nil || err.Error() != want {
			t.Fatalf("Run = %v, want exactly %q", err, want)
		}
	})

	t.Run("two run at once, never three", func(t *testing.T) {
		f := newFakeLW(t)
		f.rendezvous = true
		f.hold = 5 * time.Millisecond
		r := newRunner(set, f, filepath.Join(t.TempDir(), "run"))
		r.N, r.Parallel = 3, 2
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if f.maxActive != 2 {
			t.Errorf("max concurrent calls = %d, want 2", f.maxActive)
		}
	})

	t.Run("one runs strictly one at a time", func(t *testing.T) {
		f := newFakeLW(t)
		f.hold = 5 * time.Millisecond
		r := newRunner(set, f, filepath.Join(t.TempDir(), "run"))
		r.N, r.Parallel = 2, 1
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if f.maxActive != 1 {
			t.Errorf("max concurrent calls = %d, want 1", f.maxActive)
		}
	})
}

// TestRunnerOSExec pins the production ExecFunc (Exec nil) against a real
// subprocess: the arguments reach it, its stdout, stderr and exit code come
// back verbatim, the parent's environment is inherited with Runner.Env on
// top, and a non-zero exit is a recorded result, not a Run error.
func TestRunnerOSExec(t *testing.T) {
	set := newRunSet(t)
	// A set whose only cases are two questions, one of which the helper
	// lw fails on.
	dir := t.TempDir()
	snap := filepath.Join(dir, set.Snapshot)
	b, err := os.ReadFile(filepath.Join(set.Dir, set.Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snap, b, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := fmt.Sprintf("version = 1\nsnapshot = %q\nsnapshot_sha256 = %q\n"+
		"\n[[ask]]\nid = \"good\"\nkind = \"covered\"\nq = \"What is up?\"\nfacts = [[\"x\"]]\n"+
		"\n[[ask]]\nid = \"bad\"\nkind = \"absent\"\nq = \"fail\"\n", set.Snapshot, set.SnapshotSHA256)
	if err := os.WriteFile(filepath.Join(dir, "cases.toml"), []byte(cases), 0o644); err != nil {
		t.Fatal(err)
	}
	osSet, err := LoadSet(dir)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("EVAL_TEST_PARENT", "from-the-parent")
	t.Setenv(helperGuardEnv, "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "run")
	r := &Runner{
		LW: exe, Set: osSet, N: 1, Parallel: 2, Out: out, Backoff: time.Millisecond,
		Env: []string{helperEnv + "=1", "EVAL_TEST_VARIANT=from-runner-env"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var run RunInfo
	readJSON(t, filepath.Join(out, "run.json"), &run)
	if run.LWVersion != "lw vhelper-1.2.3" {
		t.Errorf("run.json lw_version = %q, want the helper's `lw version` output, trimmed", run.LWVersion)
	}
	if want := []string{helperEnv, "EVAL_TEST_VARIANT"}; !reflect.DeepEqual(run.EnvKeys, []string{"EVAL_TEST_HELPER_LW", "EVAL_TEST_VARIANT"}) {
		t.Errorf("env_keys = %v, want %v", run.EnvKeys, want)
	}

	var good, bad RunMeta
	readJSON(t, filepath.Join(out, "good", "1", "meta.json"), &good)
	readJSON(t, filepath.Join(out, "bad", "1", "meta.json"), &bad)
	if good.Attempts != 1 || good.Failed || good.ExitCode != 0 {
		t.Errorf("good meta = %+v, want one clean attempt", good)
	}
	if bad.Attempts != 2 || !bad.Failed || bad.ExitCode != 7 {
		t.Errorf("bad meta = %+v, want 2 attempts ending in exit 7", bad)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "good", "1", "stdout.txt")); string(b) != "answer to \"What is up?\"\n" {
		t.Errorf("stdout.txt = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "good", "1", "stderr.txt")); string(b) != "parent=from-the-parent variant=from-runner-env\n" {
		t.Errorf("stderr.txt = %q, want the inherited and the Runner.Env variables", b)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "bad", "1", "stdout.txt")); string(b) != "answer to \"fail\"\n" {
		t.Errorf("failed case stdout.txt = %q, want it kept", b)
	}
}

// TestRunnerRefusals pins the configuration errors Run reports before it
// writes or runs anything.
func TestRunnerRefusals(t *testing.T) {
	set := newRunSet(t)
	tests := []struct {
		name   string
		mutate func(r *Runner)
		want   string
	}{
		{"n is zero", func(r *Runner) { r.N = 0 }, "n 0: at least 1"},
		{"parallel is negative", func(r *Runner) { r.Parallel = -1 }, "parallel -1: at least 1"},
		{"no lw binary", func(r *Runner) { r.LW = "" }, "no lw binary"},
		{"no set", func(r *Runner) { r.Set = nil }, "no set"},
		{"no out dir", func(r *Runner) { r.Out = "" }, "no out dir"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeLW(t)
			r := newRunner(set, f, filepath.Join(t.TempDir(), "run"))
			tc.mutate(r)
			err := r.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run = %v, want an error containing %q", err, tc.want)
			}
			if len(f.calls) != 0 {
				t.Errorf("Run issued %d commands despite refusing", len(f.calls))
			}
		})
	}

	t.Run("an out dir that already holds a run", func(t *testing.T) {
		f := newFakeLW(t)
		out := filepath.Join(t.TempDir(), "run")
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "run.json"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := newRunner(set, f, out).Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "already holds a run") {
			t.Fatalf("Run = %v, want a refusal to overwrite a run", err)
		}
		if b, _ := os.ReadFile(filepath.Join(out, "run.json")); string(b) != "{}\n" {
			t.Errorf("the earlier run.json was overwritten: %q", b)
		}
	})

	t.Run("lw version fails", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "run")
		r := newRunner(set, newFakeLW(t), out)
		r.Exec = func(ctx context.Context, c Cmd) (Output, error) {
			return Output{Stderr: []byte("no such subcommand\n"), ExitCode: 2}, nil
		}
		err := r.Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "lw version") {
			t.Fatalf("Run = %v, want an error naming `lw version`", err)
		}
	})
}

// TestRunnerJobErrorKeepsTheRest pins that an infrastructure failure in one
// job (here: the command cannot start) does not throw away the others: the
// rest of the run's artifacts and run.json are written, and Run reports the
// failure.
func TestRunnerJobErrorKeepsTheRest(t *testing.T) {
	set := newRunSet(t)
	f := newFakeLW(t)
	out := filepath.Join(t.TempDir(), "run")
	r := newRunner(set, f, out)
	r.Exec = func(ctx context.Context, c Cmd) (Output, error) {
		if len(c.Args) == 4 && filepath.Base(c.Args[2]) == "kv-cache-1" {
			return Output{}, errors.New("fork/exec lw-fake: no such file or directory")
		}
		return f.Exec(ctx, c)
	}
	err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "kv-cache") || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("Run = %v, want an error naming the kv-cache case", err)
	}
	for _, p := range []string{"no-topic/1/meta.json", "paper-text/1/meta.json", "run.json"} {
		if _, err := os.Stat(filepath.Join(out, p)); err != nil {
			t.Errorf("%s was not written: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "kv-cache", "1", "meta.json")); !os.IsNotExist(err) {
		t.Errorf("the failed job wrote a meta.json (it marks a complete case): %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, ".work")); !os.IsNotExist(err) {
		t.Errorf(".work was left behind: %v", err)
	}
	var run RunInfo
	readJSON(t, filepath.Join(out, "run.json"), &run)
	if run.Finished.IsZero() {
		t.Error("run.json has no Finished time after a failed run")
	}
}

// TestRunnerStopsOnCancel pins that a cancelled context ends the run
// instead of running every remaining case.
func TestRunnerStopsOnCancel(t *testing.T) {
	set := newRunSet(t)
	f := newFakeLW(t)
	ctx, cancel := context.WithCancel(context.Background())
	r := newRunner(set, f, filepath.Join(t.TempDir(), "run"))
	r.N = 3
	r.Exec = func(ctx context.Context, c Cmd) (Output, error) {
		out, err := f.Exec(ctx, c)
		if len(c.Args) == 4 {
			cancel() // the first job command cancels the run
		}
		return out, err
	}
	err := r.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want an error wrapping context.Canceled", err)
	}
	if n := len(f.calls); n > 2 {
		t.Errorf("%d commands ran after the cancel, want the run to stop (version + the first job)", n)
	}
}

// TestRunID pins the run-id format: a UTC timestamp, a dash, and the lw
// version with everything outside [A-Za-z0-9._-] made safe for a directory
// name.
func TestRunID(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 30, 45, 0, time.FixedZone("x", 3600))
	tests := []struct{ version, want string }{
		{"lw v2.25.0", "20261005T113045Z-v2.25.0"},
		{"lw v2.25.0-3-g1234abc-dirty", "20261005T113045Z-v2.25.0-3-g1234abc-dirty"},
		{"lw 0.0.0-unknown", "20261005T113045Z-0.0.0-unknown"},
		{"v1.0 (build / 7)", "20261005T113045Z-v1.0-build-7"},
		{"", "20261005T113045Z-unknown"},
		{"lw ///", "20261005T113045Z-unknown"},
	}
	for _, tc := range tests {
		if got := RunID(at, tc.version); got != tc.want {
			t.Errorf("RunID(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
}

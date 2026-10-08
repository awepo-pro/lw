package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
)

// Cmd is one command the runner asks an ExecFunc to run: the lw binary, its
// arguments, and the full environment it runs under.
type Cmd struct {
	Path string
	Args []string
	Env  []string
}

// Output is what a finished command left behind. A non-zero ExitCode is a
// normal result (the case failed); an ExecFunc's error return is reserved
// for a command that could not run at all.
type Output struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// ExecFunc runs one command to completion. It exists so tests drive the
// runner with a fake lw; production passes nil and gets os/exec.
type ExecFunc func(ctx context.Context, c Cmd) (Output, error)

// maxParallel is the most sessions the provider tolerates at once: z.ai
// answers 429 at three concurrent sessions, and a run full of 429s measures
// the rate limiter, not the model.
const maxParallel = 2

// defaultBackoff is the pause before a failed command's one retry.
const defaultBackoff = 30 * time.Second

// Runner re-runs a Set's cases against the live provider and keeps what each
// run left behind (037 T1). It scores nothing: the artifacts are the product,
// and a scorer reads them later, so a scoring change never needs a new run.
type Runner struct {
	LW       string // lw binary
	Set      *Set
	N        int           // runs per case, >= 1
	Parallel int           // 1 or 2
	Out      string        // <set>/runs/<run-id>
	Only     string        // "" | "ask" | "ingest" | a case id
	Holdout  bool          // include holdout cases
	Note     string        // free-text label, e.g. "thinking=on"
	Env      []string      // appended to every command's env, e.g. XDG_CONFIG_HOME=<scratch> for a variant
	Exec     ExecFunc      // nil = os/exec, parent env inherited
	Backoff  time.Duration // before the one retry; 0 = 30s

	// JobTimeout bounds ONE attempt of one lw command; 0 = no bound. An
	// attempt that outlives it is cancelled and counts as a failed attempt —
	// it gets the one retry like any other — so a provider that hangs costs a
	// case 2×JobTimeout instead of the whole run. lweval defaults to 20
	// minutes (A-037-9).
	JobTimeout time.Duration

	// now is the clock behind every timestamp and wall time; nil is
	// time.Now. Unexported: only this package's tests inject one.
	now func() time.Time
}

// RunMeta is <Out>/<case>/<i>/meta.json: how one run of one case went.
// Started and WallMS describe the last attempt, like ExitCode, so a retried
// case's numbers are those of the attempt whose output the other files hold.
type RunMeta struct {
	Case     string `json:"case"`
	Verb     string `json:"verb"`      // "query" | "ingest"
	Kind     string `json:"kind"`      // ask kind, or "ingest"
	Index    int    `json:"index"`     // 1..N
	Attempts int    `json:"attempts"`  // 1 or 2
	ExitCode int    `json:"exit_code"` // of the last attempt
	Failed   bool   `json:"failed"`    // last attempt exited non-zero
	// TimedOut marks a last attempt that was cut off by Runner.JobTimeout
	// (its ExitCode is then non-zero, -1 for a killed process). Absent for
	// every other case, so a meta.json written before A-037-9 reads the same.
	TimedOut bool      `json:"timed_out,omitempty"`
	Started  time.Time `json:"started"`
	WallMS   int64     `json:"wall_ms"` // of the last attempt
	Traces   []string  `json:"traces"`  // turn ids copied, sorted
}

// RunInfo is <Out>/run.json: what was run, with what, and when. Env values
// never appear — only their names — because a variant's environment is where
// an API key tends to live.
type RunInfo struct {
	ID             string    `json:"id"`
	LW             string    `json:"lw"`
	LWVersion      string    `json:"lw_version"` // trimmed stdout of `lw version`
	SetSHA256      string    `json:"set_sha256"` // sha256 of cases.toml bytes
	SnapshotSHA256 string    `json:"snapshot_sha256"`
	N              int       `json:"n"`
	Parallel       int       `json:"parallel"`
	Only           string    `json:"only"`
	Holdout        bool      `json:"holdout"`
	Note           string    `json:"note"`
	EnvKeys        []string  `json:"env_keys"` // names only from Runner.Env, never values
	Started        time.Time `json:"started"`
	Finished       time.Time `json:"finished"`
}

// RunID formats a run id: the start time as a UTC yyyymmddThhmmssZ, a dash,
// and the lw version made safe for a directory name — a leading "lw " (what
// `lw version` prints before the version) dropped, every run of characters
// outside [A-Za-z0-9._-] turned into one dash. A caller names Out with it;
// Run never invents the directory itself.
func RunID(started time.Time, lwVersion string) string {
	v := strings.TrimSpace(lwVersion)
	v = strings.TrimPrefix(v, "lw ")
	v = strings.Trim(unsafeIDChars.ReplaceAllString(v, "-"), "-")
	if v == "" {
		v = "unknown"
	}
	return started.UTC().Format("20060102T150405Z") + "-" + v
}

var unsafeIDChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// LWVersion runs `lw version` through run (nil = os/exec) and returns its
// trimmed stdout — the string RunInfo.LWVersion records and RunID is built
// from. A failing or silent `lw version` is an error: a run that cannot say
// which lw produced it is not worth starting.
func LWVersion(ctx context.Context, lw string, run ExecFunc, env []string) (string, error) {
	if run == nil {
		run = osExec
	}
	out, err := run(ctx, Cmd{Path: lw, Args: []string{"version"}, Env: env})
	if err != nil {
		return "", fmt.Errorf("eval: lw version: %w", err)
	}
	if out.ExitCode != 0 {
		return "", fmt.Errorf("eval: lw version: exit %d: %s", out.ExitCode, strings.TrimSpace(string(out.Stderr)))
	}
	v := strings.TrimSpace(string(out.Stdout))
	if v == "" {
		return "", errors.New("eval: lw version: printed nothing")
	}
	return v, nil
}

// osExec is the production ExecFunc: the command runs with c.Env as its
// whole environment (nil inherits the parent's), no stdin, and its output
// captured. A non-zero exit is a result, not an error.
func osExec(ctx context.Context, c Cmd) (Output, error) {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Env = c.Env
	// A killed lw must not hold Run hostage through a pipe a stray
	// grandchild still has open.
	cmd.WaitDelay = 10 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		out.ExitCode = ee.ExitCode()
		return out, nil
	}
	return out, err
}

// job is one (case, index) pair: a single lw invocation's worth of work.
type job struct {
	id    string
	verb  string   // "query" | "ingest"
	kind  string   // ask kind, or "ingest"
	args  []string // the question; or, for ingest, every absolute input path in order (049) — or, when recompile, every vault path (055)
	index int

	// recompile marks an ingest job that runs `lw ingest --recompile` over
	// args as vault paths of raws the snapshot already holds (055).
	recompile bool
}

// runState is what every job of one Run shares.
type runState struct {
	out        string
	env        []string
	exec       ExecFunc
	backoff    time.Duration
	jobTimeout time.Duration
	now        func() time.Time
}

// Run runs the selected cases N times each, at most Parallel at a time, and
// writes run.json and the C3 artifact tree under Out (037 T1).
//
// Every refusal that can be known up front — N, Parallel, an unknown Only, a
// holdout case named without Holdout, an Out that already holds a run, a
// failing `lw version` — comes before Out is created or any provider call is
// made. After that a run is best-effort: a command that exits non-zero is
// data (retried once, then recorded as failed), and a job that cannot be
// carried out at all (the scratch vault will not extract, a command that
// will not start) is reported at the end without stopping the others, so one
// bad case never throws away the artifacts of the rest of an expensive run.
// A cancelled ctx stops dispatching and is reported too.
func (r *Runner) Run(ctx context.Context) error {
	parallel, err := r.check()
	if err != nil {
		return err
	}
	out, err := filepath.Abs(r.Out)
	if err != nil {
		return fmt.Errorf("eval: out: %w", err)
	}
	if _, err := os.Stat(filepath.Join(out, "run.json")); err == nil {
		return fmt.Errorf("eval: out %s already holds a run (run.json exists); pick a new run directory", r.Out)
	}
	jobs, err := r.selectJobs()
	if err != nil {
		return err
	}
	setSHA, err := fileSHA256(filepath.Join(r.Set.Dir, "cases.toml"))
	if err != nil {
		return fmt.Errorf("eval: read cases.toml: %w", err)
	}

	env := append(os.Environ(), r.Env...)
	runExec := r.Exec
	if runExec == nil {
		runExec = osExec
	}
	version, err := LWVersion(ctx, r.LW, runExec, env)
	if err != nil {
		return err
	}

	now := r.now
	if now == nil {
		now = time.Now
	}
	info := RunInfo{
		ID: filepath.Base(out), LW: r.LW, LWVersion: version,
		SetSHA256: setSHA, SnapshotSHA256: r.Set.SnapshotSHA256,
		N: r.N, Parallel: parallel, Only: r.Only, Holdout: r.Holdout, Note: r.Note,
		EnvKeys: envKeys(r.Env), Started: now().UTC(),
	}
	work := filepath.Join(out, ".work")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("eval: out: %w", err)
	}
	// A crashed earlier run in this directory may have left scratch vaults;
	// they are never evidence.
	if err := removeAll(work); err != nil {
		return fmt.Errorf("eval: clear %s: %w", work, err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return fmt.Errorf("eval: out: %w", err)
	}
	// run.json goes down before the first case: it marks the directory as a
	// run (the refusal above) and records the parameters even if the process
	// dies mid-run. The Finished stamp is rewritten at the end.
	if err := writeJSON(filepath.Join(out, "run.json"), info); err != nil {
		return err
	}

	st := &runState{out: out, env: env, exec: runExec, backoff: r.Backoff, jobTimeout: r.JobTimeout, now: now}
	if st.backoff == 0 {
		st.backoff = defaultBackoff
	}
	errs := r.runJobs(ctx, st, jobs, parallel)

	var errList []error
	for i, e := range errs {
		if e == nil || (ctx.Err() != nil && errors.Is(e, ctx.Err())) {
			continue
		}
		errList = append(errList, fmt.Errorf("eval: case %s run %d: %w", jobs[i].id, jobs[i].index, e))
	}
	if cerr := ctx.Err(); cerr != nil {
		errList = append(errList, fmt.Errorf("eval: run stopped: %w", cerr))
	}
	if err := removeAll(work); err != nil {
		errList = append(errList, fmt.Errorf("eval: clear %s: %w", work, err))
	}
	info.Finished = now().UTC()
	if err := writeJSON(filepath.Join(out, "run.json"), info); err != nil {
		errList = append(errList, err)
	}
	return errors.Join(errList...)
}

// check validates the Runner's own settings and returns the effective
// Parallel (the zero value means 1). The Parallel ceiling's message is a
// frozen text, and Parallel is checked first so that refusal never depends
// on what else the caller left unset.
func (r *Runner) check() (parallel int, err error) {
	parallel = r.Parallel
	if parallel == 0 {
		parallel = 1
	}
	if parallel < 1 {
		return 0, fmt.Errorf("parallel %d: at least 1", r.Parallel)
	}
	if parallel > maxParallel {
		return 0, fmt.Errorf("parallel %d: at most %d (z.ai 429s at %d concurrent sessions)", parallel, maxParallel, maxParallel+1)
	}
	switch {
	case r.JobTimeout < 0:
		return 0, fmt.Errorf("job timeout %v: must not be negative", r.JobTimeout)
	case r.N < 1:
		return 0, fmt.Errorf("n %d: at least 1", r.N)
	case r.Set == nil:
		return 0, errors.New("eval: no set")
	case r.LW == "":
		return 0, errors.New("eval: no lw binary")
	case r.Out == "":
		return 0, errors.New("eval: no out dir")
	}
	return parallel, nil
}

// selectJobs expands the Set into jobs — ask cases then ingest cases, each
// in file order, N runs per case — after applying Only and Holdout. Asking
// by name for a holdout case without Holdout is an error rather than an
// empty run: the holdout flag is a guard against tuning on held-back cases,
// and quietly running nothing would hide that it was tripped.
func (r *Runner) selectJobs() ([]job, error) {
	type entry struct {
		job
		holdout bool
		isAsk   bool
	}
	var all []entry
	for _, c := range r.Set.Ask {
		all = append(all, entry{job{id: c.ID, verb: "query", kind: c.Kind, args: []string{c.Q}}, c.Holdout, true})
	}
	for _, c := range r.Set.Ingest {
		var paths []string
		for _, p := range c.Paths() {
			if c.Recompile {
				// 055: a vault path names a raw in the scratch vault, not a
				// file beside cases.toml — joined onto the set directory it
				// would name nothing lw could read.
				paths = append(paths, p)
				continue
			}
			paths = append(paths, filepath.Join(r.Set.Dir, filepath.FromSlash(p)))
		}
		all = append(all, entry{job{id: c.ID, verb: "ingest", kind: "ingest", args: paths, recompile: c.Recompile}, c.Holdout, false})
	}

	group := r.Only == "" || r.Only == "ask" || r.Only == "ingest"
	if !group {
		found := false
		for _, e := range all {
			found = found || e.id == r.Only
		}
		if !found {
			return nil, fmt.Errorf("only %q: no such case (want ask, ingest or a case id)", r.Only)
		}
	}

	var jobs []job
	for _, e := range all {
		switch {
		case r.Only == "ask" && !e.isAsk, r.Only == "ingest" && e.isAsk:
			continue
		case !group && e.id != r.Only:
			continue
		}
		if e.holdout && !r.Holdout {
			if !group {
				return nil, fmt.Errorf("only %q: case %q is a holdout case; set Holdout to run it", r.Only, e.id)
			}
			continue
		}
		for i := 1; i <= r.N; i++ {
			j := e.job
			j.index = i
			jobs = append(jobs, j)
		}
	}
	if len(jobs) == 0 {
		return nil, errors.New("eval: no cases selected")
	}
	return jobs, nil
}

// runJobs runs jobs on a pool of parallel workers and returns each job's
// error, index-aligned with jobs. Dispatch is in job order and stops when
// ctx is cancelled.
func (r *Runner) runJobs(ctx context.Context, st *runState, jobs []job, parallel int) []error {
	errs := make([]error, len(jobs))
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				errs[i] = r.runJob(ctx, st, jobs[i])
			}
		}()
	}
dispatch:
	for i := range jobs {
		select {
		case idx <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(idx)
	wg.Wait()
	return errs
}

// runJob runs one job: extract a fresh scratch vault, run lw on it, retry
// once if it exits non-zero, then write the case's artifacts and remove the
// scratch. meta.json is written last — a case directory without one is a
// job that did not finish.
//
// The retry starts from a freshly extracted scratch vault, not the one the
// failed attempt left: a failed ingest leaves its half-built changeset
// behind, and the retry would join it instead of starting clean. The price
// is that the failed attempt's traces are not kept — everything in the case
// directory describes the last attempt only.
func (r *Runner) runJob(ctx context.Context, st *runState, j job) error {
	if err := ctx.Err(); err != nil {
		return err // a cancelled run starts nothing new
	}
	caseDir := filepath.Join(st.out, j.id, strconv.Itoa(j.index))
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		return err
	}
	scratch := filepath.Join(st.out, ".work", j.id+"-"+strconv.Itoa(j.index))
	defer removeAll(scratch)
	snapshot := filepath.Join(r.Set.Dir, r.Set.Snapshot)

	var (
		res      Output
		started  time.Time
		wall     time.Duration
		attempts int
		timedOut bool
	)
	for attempts < 2 {
		attempts++
		if err := removeAll(scratch); err != nil {
			return err
		}
		if err := Extract(snapshot, scratch); err != nil {
			return err
		}
		// started is read once and kept as read: UTC() would strip the
		// monotonic reading, and the wall time is subtracted from it.
		t0 := st.now()
		started = t0.UTC()
		var err error
		actx, cancel := ctx, context.CancelFunc(func() {})
		if st.jobTimeout > 0 {
			actx, cancel = context.WithTimeout(ctx, st.jobTimeout)
		}
		// One lw call per job, whatever its inputs: a multi-file ingest case
		// is the files of ONE `lw ingest`, in the order the case lists them.
		argv := []string{j.verb, "--vault", scratch}
		if j.recompile {
			argv = append(argv, "--recompile")
		}
		argv = append(argv, j.args...)
		res, err = st.exec(actx, Cmd{Path: r.LW, Args: argv, Env: st.env})
		// A cut-off attempt is the job timeout, not a run error: the deadline
		// fired on the attempt's own context while the run's was still live,
		// and the command did not finish cleanly. One that exited 0 just as
		// the deadline passed did its work and keeps it.
		timedOut = st.jobTimeout > 0 && ctx.Err() == nil &&
			errors.Is(actx.Err(), context.DeadlineExceeded) && (err != nil || res.ExitCode != 0)
		cancel()
		wall = st.now().Sub(t0)
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if timedOut {
			err = nil
			if res.ExitCode == 0 {
				res.ExitCode = -1
			}
		}
		if err != nil {
			return err
		}
		if res.ExitCode == 0 || attempts == 2 {
			break
		}
		select {
		case <-time.After(st.backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := os.WriteFile(filepath.Join(caseDir, "stdout.txt"), res.Stdout, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(caseDir, "stderr.txt"), res.Stderr, 0o644); err != nil {
		return err
	}
	traces, err := copyTraces(scratch, caseDir)
	if err != nil {
		return err
	}
	if j.verb == "ingest" {
		if err := collectIngest(scratch, caseDir); err != nil {
			return err
		}
		if j.recompile {
			if err := keepRecompiledRaws(scratch, caseDir, j.args); err != nil {
				return err
			}
		}
	}
	return writeJSON(filepath.Join(caseDir, "meta.json"), RunMeta{
		Case: j.id, Verb: j.verb, Kind: j.kind, Index: j.index,
		Attempts: attempts, ExitCode: res.ExitCode, Failed: res.ExitCode != 0, TimedOut: timedOut,
		Started: started, WallMS: wall.Milliseconds(), Traces: traces,
	})
}

// envKeys returns the sorted, de-duplicated names of env's KEY=VALUE
// entries; values are dropped here so they cannot reach run.json.
func envKeys(env []string) []string {
	seen := map[string]bool{}
	keys := []string{}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// copyTraces copies every turn directory under the scratch vault's
// .llmwiki/traces into caseDir/traces, byte for byte, and returns the turn
// ids, sorted. A scratch vault with no traces copies nothing and returns an
// empty list.
func copyTraces(scratch, caseDir string) ([]string, error) {
	src := filepath.Join(scratch, ".llmwiki", "traces")
	entries, err := os.ReadDir(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	ids := []string{}
	for _, e := range entries { // ReadDir sorts by name
		if !e.IsDir() {
			continue
		}
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(caseDir, "traces", e.Name())); err != nil {
			return nil, err
		}
		ids = append(ids, e.Name())
	}
	return ids, nil
}

// copyTree copies the regular files and directories under src to dst, with
// their permission bits: a trace holds request bodies, as private as the
// vault itself, and the copy should be no more readable than the original.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, info.Mode().Perm())
		}
		return nil // nothing else is ever written into a turn directory
	})
}

// changesetFile is changeset.json: the open changeset's top-level ops in op
// order, reduced to what a scorer needs. Cascade sub-ops are left out — an
// ingest proposes none, and a rename's rewrites are the engine's, not the
// model's.
type changesetFile struct {
	ID  string        `json:"id"`
	Ops []changesetOp `json:"ops"`
}

type changesetOp struct {
	ID      string `json:"id"`
	Op      string `json:"op"`
	Path    string `json:"path"`
	Section string `json:"section"`
	State   string `json:"state"`
}

// lintFile is lint.json: the projected lint report of the open changeset,
// cut down to the pages the ingest staged. Errors and Warns count the
// findings listed, not the whole vault's — a baseline the snapshot already
// carried says nothing about what this ingest did.
type lintFile struct {
	Errors   int           `json:"errors"`
	Warns    int           `json:"warns"`
	Findings []lintFinding `json:"findings"`
}

type lintFinding struct {
	Check    string `json:"check"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// collectIngest writes the ingest-only artifacts from the scratch vault's
// open changeset: changeset.json, staged/<path> and lint.json. No open
// changeset (the ingest failed, or the model staged nothing) writes none of
// them — absent files, not empty ones, say "nothing was staged".
//
// A staged file is the last state of its path (StagedFile resolves a
// create_page that a later patch_page rewrote to the rewrite), for every
// op that is not dropped and sits under wiki/ or raw/.
func collectIngest(scratch, caseDir string) error {
	e, err := stage.OpenEngine(scratch)
	if err != nil {
		return fmt.Errorf("open scratch vault: %w", err)
	}
	defer e.Close()
	cs, err := e.Current()
	if errors.Is(err, stage.ErrNoChangeset) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the open changeset: %w", err)
	}

	file := changesetFile{ID: cs.ID, Ops: []changesetOp{}}
	for _, op := range cs.Ops {
		file.Ops = append(file.Ops, changesetOp{ID: op.ID, Op: string(op.Kind), Path: op.Path, Section: op.Section, State: string(op.State)})
	}
	if err := writeJSON(filepath.Join(caseDir, "changeset.json"), file); err != nil {
		return err
	}

	want := map[string]bool{}
	collectStagedPaths(cs.Ops, want)
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	stagedDir := filepath.Join(caseDir, "staged")
	if err := os.MkdirAll(stagedDir, 0o755); err != nil {
		return err
	}
	written := map[string]bool{}
	for _, p := range paths {
		b, ok, err := e.StagedFile(p)
		if err != nil {
			return fmt.Errorf("read staged %s: %w", p, err)
		}
		if !ok {
			continue // an op kind StagedFile does not serve (retract, split)
		}
		if !filepath.IsLocal(filepath.FromSlash(p)) {
			return fmt.Errorf("staged path %q escapes the case directory", p)
		}
		target := filepath.Join(stagedDir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		written[p] = true
	}

	report, err := e.ProjectedReport()
	if err != nil {
		return fmt.Errorf("projected lint report: %w", err)
	}
	lf := lintFile{Findings: []lintFinding{}}
	for _, f := range report.Findings {
		if !written[f.Path] {
			continue
		}
		switch f.Severity {
		case lint.SevError:
			lf.Errors++
		case lint.SevWarn:
			lf.Warns++
		}
		lf.Findings = append(lf.Findings, lintFinding{
			Check: f.Check, Path: f.Path, Line: f.Line, Severity: string(f.Severity), Message: f.Message,
		})
	}
	return writeJSON(filepath.Join(caseDir, "lint.json"), lf)
}

// keepRecompiledRaws copies each raw a recompile job was run over (055) from
// the scratch vault into caseDir/staged/<path>, byte for byte — the place a
// staged source's file lives, so scoring resolves a recompile target and an
// ingest_source op the same way and chunk_coverage counts reads against the
// bytes the model could have read. The raw is committed in the snapshot, so
// nothing is staged: the copy is evidence, not a changeset file, and
// changeset.json does not list it. A raw the scratch vault lacks is skipped —
// lw then failed on the path and the job is scored as failed, and the
// scorer's snapshot fallback still answers chunk_coverage.
func keepRecompiledRaws(scratch, caseDir string, paths []string) error {
	for _, p := range paths {
		if !filepath.IsLocal(filepath.FromSlash(p)) {
			return fmt.Errorf("recompile path %q escapes the case directory", p)
		}
		b, err := os.ReadFile(filepath.Join(scratch, filepath.FromSlash(p)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		target := filepath.Join(caseDir, "staged", filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// collectStagedPaths adds to into every path under wiki/ or raw/ that a
// live op names, walking cascades; a dropped or rejected op takes its whole
// subtree out, as the engine's own projection does.
func collectStagedPaths(ops []stage.Op, into map[string]bool) {
	for _, op := range ops {
		if op.State == stage.StateDropped || op.State == stage.StateRejected {
			continue
		}
		if strings.HasPrefix(op.Path, "wiki/") || strings.HasPrefix(op.Path, "raw/") {
			into[op.Path] = true
		}
		collectStagedPaths(op.Cascade, into)
	}
}

// writeJSON writes v to path as 2-space-indented JSON with a trailing
// newline and no HTML escaping — these files are read by people diffing
// runs, and "<" in a lint message helps none of them.
func writeJSON(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("eval: encode %s: %w", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("eval: write %s: %w", filepath.Base(path), err)
	}
	return nil
}

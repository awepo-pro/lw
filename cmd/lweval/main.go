// Command lweval is the dev-only accuracy harness's command line (037 T3):
// it freezes a vault into an eval set, re-runs the set's cases against the
// live lw binary and provider, scores what the runs left behind, and compares
// two runs against their own run-to-run noise.
//
// It is built on demand (`go build ./cmd/lweval`) and never installed:
// `make install` builds ./cmd/lw only, and no shipped package imports
// internal/eval.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/awepo-pro/lw/internal/eval"
)

// usageText is the whole help. It names every subcommand: a tool whose usage
// has to be looked up in the source is a tool nobody runs twice.
const usageText = `usage: lweval <command> [flags] [args]

commands:
  snapshot <vault> <set>       freeze a vault into <set>/vault-<yyyymmdd>.tar.gz and print its sha256
  run [flags]                  run the set's cases against live lw, then score the run
  score [--set DIR] <run-dir>  (re)write <run-dir>/results.json from the run's artifacts
  show <run-dir>               print the scorecard
  compare <run-a> <run-b>      print per-metric A, B, delta, floor and verdict

run flags:
  --set DIR             the set directory (default $LW_EVAL_SET)
  --lw PATH             the lw binary (default: lw on PATH)
  --n N                 runs per case (default 3)
  --parallel N          cases at once, 1 or 2 (default 2)
  --job-timeout D       longest one lw command may run, e.g. 20m (default 20m; 0 = no limit);
                        a command that outlives it counts as a failed attempt and is retried once
  --only X              ask, ingest, or one case id
  --holdout             include the holdout cases
  --note S              a label recorded in run.json
  --set-config K=V      run under a private copy of config.toml with that key
                        rewritten (e.g. llm.thinking=on); repeatable. The note
                        defaults to "set-config:" and the key=value list, with
                        the value left out for a key that looks secret
`

// app is one lweval process's world. Everything the commands reach for —
// streams, environment, clock, the lw launcher — is a field, so the tests run
// the real commands against a fake lw and a fixed clock.
type app struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	now            func() time.Time
	exec           eval.ExecFunc                     // nil = os/exec
	lookPath       func(string) (string, error)      // nil = exec.LookPath
	sweep          func(time.Time) ([]string, error) // nil = eval.SweepVariantDirs
}

// defaultJobTimeout is how long one lw command may run before `run` gives up
// on that attempt: generous for a 300-page PDF ingest, short enough that a
// provider that stops answering costs a case 40 minutes, not the night.
const defaultJobTimeout = 20 * time.Minute

// shutdownSignals are the signals that cancel a run instead of killing
// lweval. SIGHUP is among them — a closed terminal or a dropped ssh session
// would otherwise end the process with the variant's config copy (and the API
// key in it) still on disk.
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

func main() {
	// A shutdown signal cancels the context rather than killing the process:
	// a --set-config run must get to delete its private config copy (it can
	// hold an API key), and a cancelled runner kills lw and returns. SIGKILL
	// cannot be caught; the next run's startup sweep clears what it leaves.
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	a := &app{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, now: time.Now, lookPath: exec.LookPath, sweep: eval.SweepVariantDirs}
	code := a.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

// run dispatches args and returns the exit code: 0 ok, 1 error, 2 usage.
func (a *app) run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		io.WriteString(a.stderr, usageText)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		io.WriteString(a.stdout, usageText)
		return 0
	case "snapshot":
		return a.cmdSnapshot(args[1:])
	case "run":
		return a.cmdRun(ctx, args[1:])
	case "score":
		return a.cmdScore(args[1:])
	case "show":
		return a.cmdShow(args[1:])
	case "compare":
		return a.cmdCompare(args[1:])
	}
	fmt.Fprintf(a.stderr, "lweval: unknown command %q\n%s", args[0], usageText)
	return 2
}

// fail reports an error and returns exit code 1.
func (a *app) fail(err error) int {
	fmt.Fprintf(a.stderr, "lweval: %v\n", err)
	return 1
}

// usageErr reports a misuse and returns exit code 2.
func (a *app) usageErr(format string, args ...any) int {
	fmt.Fprintf(a.stderr, "lweval: "+format+"\n", args...)
	return 2
}

// newFlagSet makes a flag set that reports through usageErr instead of
// printing its own text.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// cmdSnapshot freezes a vault into the set directory.
//
// It refuses to overwrite a snapshot: cases.toml pins the tarball by hash, so
// replacing a file a set already names would silently turn every run of that
// set into a run against a different vault. Re-snapshotting the same day is
// a deliberate remove-then-run.
func (a *app) cmdSnapshot(args []string) int {
	if len(args) != 2 {
		return a.usageErr("snapshot: want <vault> <set>")
	}
	vault, set := args[0], args[1]
	out := filepath.Join(set, "vault-"+a.now().UTC().Format("20060102")+".tar.gz")
	if _, err := os.Stat(out); err == nil {
		return a.fail(fmt.Errorf("snapshot: %s already exists; cases.toml pins it by hash, so remove it first if you mean to replace it", out))
	}
	sha, err := eval.Snapshot(vault, out)
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stderr, "lweval: wrote %s\n", out)
	fmt.Fprintf(a.stdout, "snapshot_sha256 = %q\n", sha)
	return 0
}

// setConfigFlag collects every --set-config key=value.
type setConfigFlag []string

func (s *setConfigFlag) String() string { return strings.Join(*s, " ") }

// Set collects v unchecked. Checking it here would make the flag package
// fail with `invalid value "<v>" for flag -set-config`, echoing the value; a
// malformed pair may be nothing but a secret whose "key=" was forgotten, so
// cmdRun validates the pairs itself, after Parse, and never repeats one.
func (s *setConfigFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// cmdRun runs the set against live lw and scores the result.
func (a *app) cmdRun(ctx context.Context, args []string) int {
	fs := newFlagSet("run")
	setDir := fs.String("set", "", "")
	lw := fs.String("lw", "lw", "")
	n := fs.Int("n", 3, "")
	parallel := fs.Int("parallel", 2, "")
	only := fs.String("only", "", "")
	holdout := fs.Bool("holdout", false, "")
	note := fs.String("note", "", "")
	jobTimeout := fs.Duration("job-timeout", defaultJobTimeout, "")
	var sets setConfigFlag
	fs.Var(&sets, "set-config", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			io.WriteString(a.stdout, usageText)
			return 0
		}
		return a.usageErr("run: %v", err)
	}
	if fs.NArg() > 0 {
		return a.usageErr("run: unexpected argument %q", fs.Arg(0))
	}
	for _, s := range sets {
		if k, _, ok := strings.Cut(s, "="); !ok || strings.TrimSpace(k) == "" {
			return a.usageErr("--set-config: want key=value")
		}
	}
	if *jobTimeout < 0 {
		return a.usageErr("run: --job-timeout %v: must not be negative", *jobTimeout)
	}
	dir := *setDir
	if dir == "" {
		dir = a.getenv("LW_EVAL_SET")
	}
	if dir == "" {
		return a.usageErr("no set: pass --set or export LW_EVAL_SET")
	}

	a.sweepStale()
	set, err := eval.LoadSet(dir)
	if err != nil {
		return a.fail(err)
	}
	look := a.lookPath
	if look == nil {
		look = exec.LookPath
	}
	resolved, err := look(*lw)
	if err != nil {
		return a.fail(fmt.Errorf("lw: %w", err))
	}
	if resolved, err = filepath.Abs(resolved); err != nil {
		return a.fail(fmt.Errorf("lw: %w", err))
	}

	label := *note
	if label == "" {
		label = eval.VariantNote(sets)
	}
	job := runJob{
		set: set, lw: resolved, n: *n, parallel: *parallel,
		only: *only, holdout: *holdout, note: label, jobTimeout: *jobTimeout,
	}
	if len(sets) == 0 {
		return a.doRun(ctx, job, nil)
	}
	// The variant's config copy exists only inside this call: it is deleted
	// when it returns, when an error leaves it, and when ctx is cancelled.
	code := 0
	err = eval.WithVariant(ctx, eval.DefaultConfigPath(), sets, func(env []string) error {
		code = a.doRun(ctx, job, env)
		return nil
	})
	if err != nil {
		return a.fail(err)
	}
	return code
}

// sweepStale clears the config copies a killed variant run left in $TMPDIR
// (eval.SweepVariantDirs) before a new run starts. It is best effort: a
// failure is mentioned and never stops the run.
func (a *app) sweepStale() {
	sweep := a.sweep
	if sweep == nil {
		sweep = eval.SweepVariantDirs
	}
	removed, err := sweep(a.now())
	if n := len(removed); n > 0 {
		noun := "copy"
		if n > 1 {
			noun = "copies"
		}
		fmt.Fprintf(a.stderr, "lweval: removed %d stale config %s left by a killed run\n", n, noun)
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "lweval: sweeping stale config copies: %v\n", err)
	}
}

// runJob is what `lweval run` resolved before any lw process starts.
type runJob struct {
	set         *eval.Set
	lw          string
	n, parallel int
	only        string
	holdout     bool
	note        string
	jobTimeout  time.Duration
}

// doRun asks the lw binary for its version (the run id carries it), runs the
// cases, scores the run and prints the scorecard. env is the variant's extra
// environment, applied to `lw version` too: the version a variant reports must
// be the version the variant's cases run under.
func (a *app) doRun(ctx context.Context, j runJob, env []string) int {
	version, err := eval.LWVersion(ctx, j.lw, a.exec, append(os.Environ(), env...))
	if err != nil {
		return a.fail(err)
	}
	out := filepath.Join(j.set.Dir, "runs", eval.RunID(a.now(), version))
	r := &eval.Runner{
		LW: j.lw, Set: j.set, N: j.n, Parallel: j.parallel, Out: out,
		Only: j.only, Holdout: j.holdout, Note: j.note, Env: env, Exec: a.exec, JobTimeout: j.jobTimeout,
	}
	if err := r.Run(ctx); err != nil {
		code := a.fail(err)
		if _, serr := os.Stat(filepath.Join(out, "run.json")); serr == nil {
			fmt.Fprintf(a.stderr, "lweval: the artifacts so far are kept in %s; delete any case directory without a meta.json (that job did not finish), then `lweval score %s` scores the rest\n", out, out)
		}
		return code
	}
	res, err := eval.ScoreSet(out, j.set)
	if err != nil {
		return a.fail(err)
	}
	if err := res.WriteScorecard(a.stdout); err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "\nresults: %s\n", filepath.Join(out, "results.json"))
	return 0
}

// cmdScore (re)writes a run's results.json. The set is the one the run
// directory sits in (<set>/runs/<id>) unless --set names another — a run kept
// elsewhere, or scored against a corrected copy of the set.
func (a *app) cmdScore(args []string) int {
	fs := newFlagSet("score")
	setDir := fs.String("set", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			io.WriteString(a.stdout, usageText)
			return 0
		}
		return a.usageErr("score: %v", err)
	}
	if fs.NArg() != 1 {
		return a.usageErr("score: want [--set DIR] <run-dir>")
	}
	dir := fs.Arg(0)
	var (
		res *eval.Results
		err error
	)
	if *setDir == "" {
		res, err = eval.Score(dir)
	} else {
		var set *eval.Set
		if set, err = eval.LoadSet(*setDir); err == nil {
			res, err = eval.ScoreSet(dir, set)
		}
	}
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "wrote %s (%d runs scored)\n", filepath.Join(dir, "results.json"), len(res.Results))
	return 0
}

// cmdShow prints a scored run's scorecard.
func (a *app) cmdShow(args []string) int {
	if len(args) != 1 {
		return a.usageErr("show: want <run-dir>")
	}
	res, err := eval.LoadResults(args[0])
	if err != nil {
		return a.fail(err)
	}
	if err := res.WriteScorecard(a.stdout); err != nil {
		return a.fail(err)
	}
	return 0
}

// cmdCompare prints run A against run B.
func (a *app) cmdCompare(args []string) int {
	if len(args) != 2 {
		return a.usageErr("compare: want <run-a> <run-b>")
	}
	ra, err := eval.LoadResults(args[0])
	if err != nil {
		return a.fail(err)
	}
	rb, err := eval.LoadResults(args[1])
	if err != nil {
		return a.fail(err)
	}
	if err := eval.WriteComparison(a.stdout, eval.Compare(ra, rb)); err != nil {
		return a.fail(err)
	}
	return 0
}

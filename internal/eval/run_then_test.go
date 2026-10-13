package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
)

// thenFake is the stand-in lw of the joined-call tests (060). It records every
// job command per scratch vault in the order issued, leaves a note in the scratch
// on each call and reports what it found there first — so a test can tell a call
// that ran in the SAME scratch as the one before it from a retry that started on
// a freshly extracted one — and lets a script decide what each call returns.
type thenFake struct {
	t *testing.T
	// script decides one job command's result. n is the 1-based count of job
	// commands for that scratch so far, across attempts; argv is the whole
	// argument list. nil means every call exits 0 printing "out<n>\n" on stdout
	// and "err<n>\n" on stderr.
	script func(ctx context.Context, key string, n int, argv []string) (Output, error)

	mu        sync.Mutex
	argvs     map[string][][]string  // scratch name -> job argv, in call order
	before    map[string][]string    // scratch name -> what the scratch's notes held when each call began
	deadlines map[string][]time.Time // scratch name -> each call's context deadline (zero when none)
}

func newThenFake(t *testing.T) *thenFake {
	return &thenFake{t: t, argvs: map[string][][]string{}, before: map[string][]string{}, deadlines: map[string][]time.Time{}}
}

func (f *thenFake) exec(ctx context.Context, c Cmd) (Output, error) {
	if len(c.Args) == 1 && c.Args[0] == "version" {
		return Output{Stdout: []byte("lw v9.9.9-test\n")}, nil
	}
	scratch := c.Args[2]
	key := filepath.Base(scratch)
	notes := filepath.Join(scratch, "calls.txt")
	seen, _ := os.ReadFile(notes) // absent on a fresh scratch

	f.mu.Lock()
	f.argvs[key] = append(f.argvs[key], append([]string(nil), c.Args...))
	n := len(f.argvs[key])
	f.before[key] = append(f.before[key], string(seen))
	dl, _ := ctx.Deadline()
	f.deadlines[key] = append(f.deadlines[key], dl)
	f.mu.Unlock()

	if err := os.WriteFile(notes, append(seen, []byte(fmt.Sprintf("call %d\n", n))...), 0o644); err != nil {
		f.t.Errorf("%s: write notes: %v", key, err)
	}
	if f.script != nil {
		return f.script(ctx, key, n, c.Args)
	}
	return Output{Stdout: []byte(fmt.Sprintf("out%d\n", n)), Stderr: []byte(fmt.Sprintf("err%d\n", n))}, nil
}

// thenSet writes the inputs the joined-call cases name into a fresh run set,
// installs body as its cases.toml and loads it.
func thenSet(t *testing.T, body string) *Set {
	t.Helper()
	set := newRunSet(t)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(set.Dir, "inputs", name), []byte("Article "+name+".\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scRewriteCases(t, set, body)
	set, err := LoadSet(set.Dir)
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}
	return set
}

// thenRunner is a Runner over set on f, one run of every case, one at a time.
func thenRunner(set *Set, f *thenFake, out string) *Runner {
	return &Runner{
		LW: "lw-fake", Set: set, N: 1, Parallel: 1, Out: out, Exec: f.exec,
		Backoff: time.Millisecond, now: stepClock(),
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// thenJoinedCase is the cases.toml of the 10-08 shape: two sources, then a
// joined second call with a third.
const thenJoinedCase = `[[ingest]]
id = "joined"
inputs = ["inputs/a.txt", "inputs/b.txt"]
then = ["inputs/c.txt"]
facts = [["x"]]
`

// TestRunThenTwoCalls pins 060's runner half: a case with `then` is TWO
// `lw ingest` calls on the SAME scratch vault, the first with the case's inputs
// and the second with its then, in that order — so the second joins the changeset
// the first left open (019), the shape of the 10-08 failure the eval never ran. A
// recompile case passes --recompile to both. The attempt's stdout and stderr are
// call 1's bytes followed by call 2's, with nothing between them.
func TestRunThenTwoCalls(t *testing.T) {
	set := thenSet(t, thenJoinedCase+`
[[ingest]]
id = "recompiled"
recompile = true
inputs = ["raw/papers/leviathan-2023.md"]
then = ["raw/articles/kv-cache-explained.md"]
facts = [["x"]]

[[ingest]]
id = "single"
input = "inputs/a.txt"
then = ["inputs/b.txt", "inputs/c.txt"]
facts = [["x"]]
`)
	f := newThenFake(t)
	out := filepath.Join(t.TempDir(), "runs", "20261013T120000Z-v9.9.9-test")
	if err := thenRunner(set, f, out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	abs := func(name string) string { return filepath.Join(set.Dir, "inputs", name) }
	scratch := func(key string) string { return filepath.Join(out, ".work", key) }
	want := map[string][][]string{
		"joined-1": {
			{"ingest", "--vault", scratch("joined-1"), abs("a.txt"), abs("b.txt")},
			{"ingest", "--vault", scratch("joined-1"), abs("c.txt")},
		},
		"recompiled-1": {
			{"ingest", "--vault", scratch("recompiled-1"), "--recompile", "raw/papers/leviathan-2023.md"},
			{"ingest", "--vault", scratch("recompiled-1"), "--recompile", "raw/articles/kv-cache-explained.md"},
		},
		"single-1": {
			{"ingest", "--vault", scratch("single-1"), abs("a.txt")},
			{"ingest", "--vault", scratch("single-1"), abs("b.txt"), abs("c.txt")},
		},
	}
	if !reflect.DeepEqual(f.argvs, want) {
		t.Errorf("argv by scratch vault\n got %q\nwant %q", f.argvs, want)
	}

	for _, key := range []string{"joined", "recompiled", "single"} {
		dir := filepath.Join(out, key, "1")
		if got := readFile(t, filepath.Join(dir, "stdout.txt")); got != "out1\nout2\n" {
			t.Errorf("%s: stdout.txt = %q, want call 1's bytes then call 2's, no separator", key, got)
		}
		if got := readFile(t, filepath.Join(dir, "stderr.txt")); got != "err1\nerr2\n" {
			t.Errorf("%s: stderr.txt = %q, want call 1's bytes then call 2's, no separator", key, got)
		}
		var meta RunMeta
		readJSON(t, filepath.Join(dir, "meta.json"), &meta)
		if meta.Attempts != 1 || meta.ExitCode != 0 || meta.Failed || meta.TimedOut {
			t.Errorf("%s: meta = %+v, want one clean attempt", key, meta)
		}
	}

	// The second call ran in the very scratch the first left behind; the first
	// began on a fresh one.
	for key, before := range f.before {
		if len(before) != 2 || before[0] != "" || before[1] != "call 1\n" {
			t.Errorf("%s: the notes found at the start of each call = %q, want [\"\" \"call 1\\n\"] (call 2 in call 1's scratch)", key, before)
		}
	}
}

// TestRunThenSkippedWhenFirstFails pins the failure rules of a joined case: the
// second call only runs when the first exited 0 — joining the changeset of a
// crashed ingest would measure the wreck — and the attempt's exit code is call
// 1's when that is non-zero, else call 2's. A retry is the one retry of a job: it
// re-extracts the snapshot and reruns BOTH calls, and what the case directory
// holds is the last attempt's alone.
func TestRunThenSkippedWhenFirstFails(t *testing.T) {
	set := thenSet(t, thenJoinedCase)
	// run executes the set with call n exiting exits[n] (0 when absent) and
	// printing "out<n>" and "err<n>".
	run := func(t *testing.T, exits map[int]int) (*thenFake, RunMeta, string) {
		t.Helper()
		f := newThenFake(t)
		f.script = func(_ context.Context, _ string, n int, _ []string) (Output, error) {
			return Output{Stdout: []byte(fmt.Sprintf("out%d\n", n)), Stderr: []byte(fmt.Sprintf("err%d\n", n)), ExitCode: exits[n]}, nil
		}
		out := filepath.Join(t.TempDir(), "run")
		if err := thenRunner(set, f, out).Run(context.Background()); err != nil {
			t.Fatalf("Run: a failed case is data, not a Run error: %v", err)
		}
		var meta RunMeta
		readJSON(t, filepath.Join(out, "joined", "1", "meta.json"), &meta)
		return f, meta, filepath.Join(out, "joined", "1")
	}
	// kinds names each recorded call: "A" is the case's inputs, "B" its then.
	kinds := func(f *thenFake) string {
		var s []string
		for _, argv := range f.argvs["joined-1"] {
			if strings.HasSuffix(argv[len(argv)-1], "c.txt") {
				s = append(s, "B")
			} else {
				s = append(s, "A")
			}
		}
		return strings.Join(s, "")
	}

	t.Run("call 1 fails: no call 2, the retry reruns call 1 and then call 2", func(t *testing.T) {
		f, meta, dir := run(t, map[int]int{1: 1})
		if got := kinds(f); got != "AAB" {
			t.Errorf("calls = %s, want AAB: call 1 (failed), the retry's call 1, then its call 2", got)
		}
		if meta.Attempts != 2 || meta.Failed || meta.ExitCode != 0 {
			t.Errorf("meta = %+v, want 2 attempts, not failed, exit 0", meta)
		}
		if got := readFile(t, filepath.Join(dir, "stdout.txt")); got != "out2\nout3\n" {
			t.Errorf("stdout.txt = %q, want the retry's two calls only", got)
		}
		if got := readFile(t, filepath.Join(dir, "stderr.txt")); got != "err2\nerr3\n" {
			t.Errorf("stderr.txt = %q, want the retry's two calls only", got)
		}
		if got := f.before["joined-1"]; len(got) != 3 || got[0] != "" || got[1] != "" || got[2] != "call 2\n" {
			t.Errorf("notes at the start of each call = %q; the retry must start on a fresh scratch and its call 2 must follow its own call 1", got)
		}
	})

	t.Run("call 1 fails twice: call 2 never runs and the exit is call 1's", func(t *testing.T) {
		f, meta, dir := run(t, map[int]int{1: 1, 2: 2})
		if got := kinds(f); got != "AA" {
			t.Errorf("calls = %s, want AA: the one retry of call 1 and never a call 2", got)
		}
		if meta.Attempts != 2 || !meta.Failed || meta.ExitCode != 2 {
			t.Errorf("meta = %+v, want 2 attempts, failed, exit 2 (the last attempt's call 1)", meta)
		}
		if got := readFile(t, filepath.Join(dir, "stdout.txt")); got != "out2\n" {
			t.Errorf("stdout.txt = %q, want call 1's bytes alone", got)
		}
	})

	t.Run("call 2 fails: its code is the attempt's and the retry reruns both calls", func(t *testing.T) {
		f, meta, dir := run(t, map[int]int{2: 3})
		if got := kinds(f); got != "ABAB" {
			t.Errorf("calls = %s, want ABAB: both calls rerun on the retry", got)
		}
		if meta.Attempts != 2 || meta.Failed || meta.ExitCode != 0 {
			t.Errorf("meta = %+v, want 2 attempts, not failed, exit 0 (the retry passed)", meta)
		}
		if got := readFile(t, filepath.Join(dir, "stdout.txt")); got != "out3\nout4\n" {
			t.Errorf("stdout.txt = %q, want the retry's two calls only", got)
		}
		if got := f.before["joined-1"]; len(got) != 4 || got[2] != "" || got[3] != "call 3\n" {
			t.Errorf("notes at the start of each call = %q; the retry must re-extract the snapshot, not reuse the failed attempt's scratch", got)
		}
	})

	t.Run("call 2 fails twice: the case failed with call 2's code and the last attempt's bytes", func(t *testing.T) {
		f, meta, dir := run(t, map[int]int{2: 3, 4: 4})
		if got := kinds(f); got != "ABAB" {
			t.Errorf("calls = %s, want ABAB: one retry, never a third attempt", got)
		}
		if meta.Attempts != 2 || !meta.Failed || meta.ExitCode != 4 {
			t.Errorf("meta = %+v, want 2 attempts, failed, exit 4 (the last attempt's call 2)", meta)
		}
		if got := readFile(t, filepath.Join(dir, "stdout.txt")); got != "out3\nout4\n" {
			t.Errorf("stdout.txt = %q, want the last attempt's two calls", got)
		}
	})

	t.Run("both calls pass first time", func(t *testing.T) {
		f, meta, _ := run(t, nil)
		if got := kinds(f); got != "AB" {
			t.Errorf("calls = %s, want AB", got)
		}
		if meta.Attempts != 1 || meta.Failed {
			t.Errorf("meta = %+v, want one clean attempt", meta)
		}
	})
}

// TestRunThenTimeout pins that the job timeout bounds the WHOLE attempt: both
// calls run under one context with one deadline, so a provider that hangs in the
// second call cannot cost a joined case twice the budget, and a first call that
// spends the budget leaves no second call to start. Either way the attempt is a
// timed-out one, retried once like any other (A-037-9).
func TestRunThenTimeout(t *testing.T) {
	set := thenSet(t, thenJoinedCase)
	const timeout = 80 * time.Millisecond

	t.Run("both calls share one deadline", func(t *testing.T) {
		f := newThenFake(t)
		out := filepath.Join(t.TempDir(), "run")
		r := thenRunner(set, f, out)
		r.JobTimeout = time.Hour
		if err := r.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		dls := f.deadlines["joined-1"]
		if len(dls) != 2 || dls[0].IsZero() || !dls[0].Equal(dls[1]) {
			t.Errorf("deadlines %v, want the same non-zero instant for both calls (one context per attempt)", dls)
		}

		f = newThenFake(t)
		r = thenRunner(set, f, filepath.Join(t.TempDir(), "run"))
		if err := r.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, dl := range f.deadlines["joined-1"] {
			if !dl.IsZero() {
				t.Errorf("a call carries a deadline %v although JobTimeout is 0", dl)
			}
		}
	})

	t.Run("a hung second call times the attempt out and keeps call 1's output", func(t *testing.T) {
		f := newThenFake(t)
		f.script = func(ctx context.Context, _ string, n int, _ []string) (Output, error) {
			if n%2 == 1 {
				return Output{Stdout: []byte(fmt.Sprintf("out%d\n", n))}, nil
			}
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				t.Errorf("call %d was never cancelled", n)
			}
			return Output{Stdout: []byte("partial\n")}, context.DeadlineExceeded
		}
		out := filepath.Join(t.TempDir(), "run")
		r := thenRunner(set, f, out)
		r.JobTimeout = timeout
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: a timed-out case is data, not a run error: %v", err)
		}
		var meta RunMeta
		readJSON(t, filepath.Join(out, "joined", "1", "meta.json"), &meta)
		if meta.Attempts != 2 || !meta.Failed || !meta.TimedOut || meta.ExitCode == 0 {
			t.Errorf("meta = %+v, want 2 attempts, failed, TimedOut, a non-zero exit", meta)
		}
		if got := len(f.argvs["joined-1"]); got != 4 {
			t.Errorf("%d calls, want 4: both calls of both attempts", got)
		}
		if got := readFile(t, filepath.Join(out, "joined", "1", "stdout.txt")); got != "out3\npartial\n" {
			t.Errorf("stdout.txt = %q, want the last attempt's call 1 then what call 2 printed before the cut", got)
		}
	})

	t.Run("a first call that spends the budget leaves no second call", func(t *testing.T) {
		f := newThenFake(t)
		f.script = func(ctx context.Context, _ string, n int, _ []string) (Output, error) {
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				t.Errorf("call %d was never cancelled", n)
			}
			return Output{Stdout: []byte("one\n")}, nil // exits 0 as the deadline passes
		}
		out := filepath.Join(t.TempDir(), "run")
		r := thenRunner(set, f, out)
		r.JobTimeout = timeout
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		var meta RunMeta
		readJSON(t, filepath.Join(out, "joined", "1", "meta.json"), &meta)
		if meta.Attempts != 2 || !meta.Failed || !meta.TimedOut {
			t.Errorf("meta = %+v, want 2 attempts, failed, TimedOut: the attempt did not finish its second call", meta)
		}
		if got := len(f.argvs["joined-1"]); got != 2 {
			t.Errorf("%d calls, want 2: call 1 of each attempt and never a call 2 under a spent deadline", got)
		}
	})

	t.Run("cancelling the run is not a timeout", func(t *testing.T) {
		f := newThenFake(t)
		ctx, cancel := context.WithCancel(context.Background())
		f.script = func(ctx context.Context, _ string, n int, _ []string) (Output, error) {
			if n == 1 {
				cancel() // the run is cancelled between the calls
			}
			return Output{Stdout: []byte("x\n")}, nil
		}
		out := filepath.Join(t.TempDir(), "run")
		r := thenRunner(set, f, out)
		r.JobTimeout = time.Hour
		err := r.Run(ctx)
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("Run err = %v, want the cancellation", err)
		}
		if _, serr := os.Stat(filepath.Join(out, "joined", "1", "meta.json")); !os.IsNotExist(serr) {
			t.Errorf("a cancelled job wrote a meta.json (%v): it did not finish", serr)
		}
	})
}

// TestRunNoThenUnchanged pins the other half of 060: a case without `then` runs
// byte-identically to before — one lw call whose argv is the one every earlier run
// was made with, one retry of that one call, and a meta.json with no field the
// joined call could have added.
func TestRunNoThenUnchanged(t *testing.T) {
	set := thenSet(t, `[[ask]]
id = "kv-cache"
kind = "covered"
q = "How does the KV cache work?"
facts = [["keys"]]

[[ingest]]
id = "one"
input = "inputs/a.txt"
facts = [["x"]]

[[ingest]]
id = "three"
inputs = ["inputs/c.txt", "inputs/a.txt", "inputs/b.txt"]
facts = [["x"]]

[[ingest]]
id = "recompiled"
recompile = true
inputs = ["raw/papers/leviathan-2023.md", "raw/articles/kv-cache-explained.md"]
facts = [["x"]]
`)
	f := newThenFake(t)
	out := filepath.Join(t.TempDir(), "runs", "20261013T120000Z-v9.9.9-test")
	if err := thenRunner(set, f, out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	abs := func(name string) string { return filepath.Join(set.Dir, "inputs", name) }
	scratch := func(key string) string { return filepath.Join(out, ".work", key) }
	want := map[string][][]string{
		"kv-cache-1":   {{"query", "--vault", scratch("kv-cache-1"), "How does the KV cache work?"}},
		"one-1":        {{"ingest", "--vault", scratch("one-1"), abs("a.txt")}},
		"three-1":      {{"ingest", "--vault", scratch("three-1"), abs("c.txt"), abs("a.txt"), abs("b.txt")}},
		"recompiled-1": {{"ingest", "--vault", scratch("recompiled-1"), "--recompile", "raw/papers/leviathan-2023.md", "raw/articles/kv-cache-explained.md"}},
	}
	if !reflect.DeepEqual(f.argvs, want) {
		t.Errorf("argv by scratch vault: exactly one exec each\n got %q\nwant %q", f.argvs, want)
	}

	// The case directory is what a one-call run always left: stdout and stderr
	// are the one call's, and meta.json has exactly its old fields.
	for _, key := range []string{"kv-cache", "one", "three", "recompiled"} {
		dir := filepath.Join(out, key, "1")
		if got := readFile(t, filepath.Join(dir, "stdout.txt")); got != "out1\n" {
			t.Errorf("%s: stdout.txt = %q, want the one call's", key, got)
		}
		var fields map[string]json.RawMessage
		readJSON(t, filepath.Join(dir, "meta.json"), &fields)
		var names []string
		for k := range fields {
			names = append(names, k)
		}
		sort.Strings(names)
		wantNames := []string{"attempts", "case", "exit_code", "failed", "index", "kind", "started", "traces", "verb", "wall_ms"}
		if !reflect.DeepEqual(names, wantNames) {
			t.Errorf("%s: meta.json fields %v, want exactly %v", key, names, wantNames)
		}
	}

	t.Run("a failing one-call case retries one call", func(t *testing.T) {
		f := newThenFake(t)
		f.script = func(_ context.Context, _ string, n int, _ []string) (Output, error) {
			return Output{Stdout: []byte(fmt.Sprintf("out%d\n", n)), ExitCode: map[int]int{1: 1}[n]}, nil
		}
		out := filepath.Join(t.TempDir(), "run")
		r := thenRunner(set, f, out)
		r.Only = "one"
		if err := r.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		argvs := f.argvs["one-1"]
		if len(argvs) != 2 || !reflect.DeepEqual(argvs[0][:3], argvs[1][:3]) || argvs[0][len(argvs[0])-1] != abs("a.txt") || argvs[1][len(argvs[1])-1] != abs("a.txt") {
			t.Errorf("calls %q, want the same single call twice", argvs)
		}
		if got := readFile(t, filepath.Join(out, "one", "1", "stdout.txt")); got != "out2\n" {
			t.Errorf("stdout.txt = %q, want the retry's", got)
		}
	})
}

// thenStage stages one page in the scratch vault the way an ingest turn does,
// joining the open changeset when an earlier call left one (019) and opening it
// when none is: a stand-in for what the real second `lw ingest` process does.
// It reports failure as an error, not t.Fatal — it runs on the runner's worker
// goroutine, where a Goexit would leave the dispatcher with no one to hand jobs to.
func thenStage(scratch, pagePath, title string) error {
	e, err := stage.OpenEngine(scratch)
	if err != nil {
		return fmt.Errorf("OpenEngine: %w", err)
	}
	defer e.Close()
	if _, err := e.Current(); errors.Is(err, stage.ErrNoChangeset) {
		if _, err := e.OpenChangeset("ingest joined", stage.Author{Kind: "agent", Model: "glm-test"}); err != nil {
			return fmt.Errorf("OpenChangeset: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("Current: %w", err)
	}
	content := "---\ntitle: " + title + "\ncreated: 2026-10-13\nupdated: 2026-10-13\ntype: concept\n" +
		"tags: [inference]\nconfidence: medium\n---\n\n# " + title + "\n\nSee [[kv-cache]] and [[gpt-4]].\n"
	if _, err := e.Append(stage.Op{
		Kind: stage.OpCreatePage, Path: pagePath, Content: []byte(content),
		Rationale: "the source covers it", Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		return fmt.Errorf("Append %s: %w", pagePath, err)
	}
	return nil
}

// TestRunThenArtifacts pins that a joined case leaves ONE case directory for the
// whole attempt: the traces of both turns, and the changeset the second call
// joined — both calls' pages — collected once, after the second call, from the
// scratch vault the two calls shared. A recompile case keeps the raws of its then
// beside those of its first call, where the scorer looks for the bytes the model
// could read.
func TestRunThenArtifacts(t *testing.T) {
	set := thenSet(t, thenJoinedCase+`
[[ingest]]
id = "recompiled"
recompile = true
inputs = ["raw/papers/leviathan-2023.md"]
then = ["raw/articles/kv-cache-explained.md"]
facts = [["x"]]
`)
	base := newFakeLW(t)
	f := newThenFake(t)
	turnIDs := map[string][]string{} // scratch name -> turn id of each call
	var mu sync.Mutex
	f.script = func(ctx context.Context, key string, n int, argv []string) (Output, error) {
		scratch := argv[2]
		id, _ := base.writeTrace(ctx, scratch, "ingest")
		mu.Lock()
		turnIDs[key] = append(turnIDs[key], id)
		mu.Unlock()
		if strings.HasPrefix(key, "joined") {
			page := map[int]string{1: "wiki/concepts/first-page.md", 2: "wiki/concepts/second-page.md"}[n]
			if err := thenStage(scratch, page, strings.TrimSuffix(filepath.Base(page), ".md")); err != nil {
				t.Errorf("%s call %d: %v", key, n, err)
				return Output{ExitCode: 9}, nil
			}
		}
		return Output{Stdout: []byte(fmt.Sprintf("out%d\n", n))}, nil
	}
	out := filepath.Join(t.TempDir(), "run")
	if err := thenRunner(set, f, out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	dir := filepath.Join(out, "joined", "1")
	var meta RunMeta
	readJSON(t, filepath.Join(dir, "meta.json"), &meta)
	want := append([]string(nil), turnIDs["joined-1"]...)
	sort.Strings(want)
	if len(want) != 2 || !reflect.DeepEqual(meta.Traces, want) {
		t.Errorf("meta.Traces = %v, want both calls' turns %v", meta.Traces, want)
	}
	for _, id := range want {
		if _, err := os.Stat(filepath.Join(dir, "traces", id, "events.ndjson")); err != nil {
			t.Errorf("traces/%s is not in the case directory: %v", id, err)
		}
	}
	var cs changesetFile
	readJSON(t, filepath.Join(dir, "changeset.json"), &cs)
	var paths []string
	for _, op := range cs.Ops {
		paths = append(paths, op.Path)
	}
	if !reflect.DeepEqual(paths, []string{"wiki/concepts/first-page.md", "wiki/concepts/second-page.md"}) {
		t.Errorf("changeset.json ops = %q, want the first call's page then the joined call's", paths)
	}
	for _, p := range paths {
		if _, err := os.Stat(filepath.Join(dir, "staged", filepath.FromSlash(p))); err != nil {
			t.Errorf("staged/%s is missing: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "lint.json")); err != nil {
		t.Errorf("lint.json is missing: %v", err)
	}

	// The recompile case keeps both calls' raws, byte for byte.
	rdir := filepath.Join(out, "recompiled", "1")
	for _, p := range []string{"raw/papers/leviathan-2023.md", "raw/articles/kv-cache-explained.md"} {
		got, err := os.ReadFile(filepath.Join(rdir, "staged", filepath.FromSlash(p)))
		if err != nil {
			t.Errorf("the recompile case dir does not keep %s: %v", p, err)
			continue
		}
		if len(got) == 0 {
			t.Errorf("the kept %s is empty", p)
		}
	}
}

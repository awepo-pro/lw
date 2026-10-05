package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/awepo-pro/lw/internal/eval"
	"github.com/awepo-pro/lw/internal/testutil"
)

// newSet builds a set directory the way an author would: a snapshot of a
// synthetic vault, and a cases.toml with one ask case that pins its hash.
func newSet(t *testing.T) string {
	t.Helper()
	vaultDir := testutil.CopyFixture(t, "minimal")
	setDir := t.TempDir()
	sha, err := eval.Snapshot(vaultDir, filepath.Join(setDir, "vault-20261005.tar.gz"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	cases := fmt.Sprintf(`version = 1
snapshot = "vault-20261005.tar.gz"
snapshot_sha256 = %q

[[ask]]
id = "kv-cache"
kind = "covered"
q = "How does the KV cache work?"
facts = [["keys"], ["values"]]
`, sha)
	if err := os.WriteFile(filepath.Join(setDir, "cases.toml"), []byte(cases), 0o644); err != nil {
		t.Fatal(err)
	}
	return setDir
}

// seenConfig is what the fake lw found of its config while it ran.
type seenConfig struct {
	xdg     string
	bytes   []byte
	fileErr error
	mode    os.FileMode
	dirMode os.FileMode
}

// fakeLW is the stand-in lw of the CLI tests: `version`, and `query`, which
// answers and records the config it would have read.
type fakeLW struct {
	mu    sync.Mutex
	calls []eval.Cmd
	seen  []seenConfig
	// limits is, per query command, the time left on its context when it
	// started — 0 with ok false when the context carried no deadline.
	limits []deadline
}

type deadline struct {
	left time.Duration
	ok   bool
}

func lastEnv(env []string, key string) (string, bool) {
	v, ok := "", false
	for _, kv := range env {
		if k, val, found := strings.Cut(kv, "="); found && k == key {
			v, ok = val, true
		}
	}
	return v, ok
}

func (f *fakeLW) exec(ctx context.Context, c eval.Cmd) (eval.Output, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if len(c.Args) == 1 && c.Args[0] == "version" {
		return eval.Output{Stdout: []byte("lw v9.9.9-test\n")}, nil
	}
	dl, hasDL := ctx.Deadline()
	f.mu.Lock()
	f.limits = append(f.limits, deadline{time.Until(dl), hasDL})
	f.mu.Unlock()
	if xdg, ok := lastEnv(c.Env, "XDG_CONFIG_HOME"); ok {
		s := seenConfig{xdg: xdg}
		file := filepath.Join(xdg, "lw", "config.toml")
		s.bytes, s.fileErr = os.ReadFile(file)
		if fi, err := os.Stat(file); err == nil {
			s.mode = fi.Mode().Perm()
		}
		if fi, err := os.Stat(xdg); err == nil {
			s.dirMode = fi.Mode().Perm()
		}
		f.mu.Lock()
		f.seen = append(f.seen, s)
		f.mu.Unlock()
	}
	return eval.Output{Stdout: []byte("Keys and values are cached.\n")}, nil
}

// newApp wires an app to buffers, a fixed clock, the fake lw and an
// environment of exactly env.
func newApp(f *fakeLW, env map[string]string) (*app, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	a := &app{
		stdout: &out, stderr: &errb,
		getenv:   func(k string) string { return env[k] },
		now:      func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) },
		lookPath: func(s string) (string, error) { return s, nil },
		// The real sweep reads the real $TMPDIR against a fixed test clock;
		// only the tests that mean to sweep turn it on.
		sweep: func(time.Time) ([]string, error) { return nil, nil },
	}
	if f != nil {
		a.exec = f.exec
	}
	return a, &out, &errb
}

// TestLwevalUsage pins the two usage refusals: no command names all five
// subcommands, and `run` with no set says how to give one (037 T3).
func TestLwevalUsage(t *testing.T) {
	ctx := context.Background()

	t.Run("no arguments", func(t *testing.T) {
		a, out, errb := newApp(nil, nil)
		if code := a.run(ctx, nil); code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
		for _, sub := range []string{"snapshot", "run", "score", "show", "compare"} {
			if !strings.Contains(errb.String(), sub) {
				t.Errorf("usage does not name %q:\n%s", sub, errb.String())
			}
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want nothing", out.String())
		}
	})

	t.Run("run with no set", func(t *testing.T) {
		a, _, errb := newApp(nil, map[string]string{"LW_EVAL_SET": ""})
		if code := a.run(ctx, []string{"run"}); code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
		if want := "lweval: no set: pass --set or export LW_EVAL_SET\n"; errb.String() != want {
			t.Errorf("stderr = %q, want %q", errb.String(), want)
		}
	})

	t.Run("an unknown command", func(t *testing.T) {
		a, _, errb := newApp(nil, nil)
		if code := a.run(ctx, []string{"frobnicate"}); code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
		if !strings.Contains(errb.String(), `lweval: unknown command "frobnicate"`) || !strings.Contains(errb.String(), "compare") {
			t.Errorf("stderr = %q, want the unknown-command line and the usage", errb.String())
		}
	})

	t.Run("help is not an error", func(t *testing.T) {
		for _, h := range []string{"help", "-h", "--help"} {
			a, out, errb := newApp(nil, nil)
			if code := a.run(ctx, []string{h}); code != 0 {
				t.Errorf("%s: exit = %d, want 0", h, code)
			}
			if !strings.Contains(out.String(), "snapshot") || errb.Len() != 0 {
				t.Errorf("%s: stdout %q, stderr %q; want the usage on stdout only", h, out.String(), errb.String())
			}
		}
	})

	t.Run("wrong argument counts and bad flags", func(t *testing.T) {
		for _, args := range [][]string{
			{"snapshot"}, {"snapshot", "only-one"}, {"snapshot", "a", "b", "c"},
			{"score"}, {"score", "a", "b"}, {"show"}, {"show", "a", "b"},
			{"compare"}, {"compare", "a"}, {"compare", "a", "b", "c"},
			{"run", "--bogus"}, {"run", "--n", "x"}, {"run", "stray-arg"}, {"run", "--job-timeout", "soon"},
			{"run", "--set-config", "no-equals", "--set", "x"},
			{"run", "--set-config", "=value-only", "--set", "x"},
		} {
			a, _, errb := newApp(nil, map[string]string{"LW_EVAL_SET": "/x"})
			if code := a.run(ctx, args); code != 2 {
				t.Errorf("%v: exit = %d, want 2 (stderr %q)", args, code, errb.String())
			}
			if !strings.HasPrefix(errb.String(), "lweval: ") {
				t.Errorf("%v: stderr = %q, want a lweval: line", args, errb.String())
			}
		}
	})

	// A-037-7: a malformed --set-config is refused without echoing it — a
	// pasted secret with its "key=" forgotten must not land in a terminal log.
	t.Run("a malformed --set-config is not echoed", func(t *testing.T) {
		for _, bad := range []string{"sk-SECRET-123", "=sk-SECRET-123"} {
			a, _, errb := newApp(nil, map[string]string{"LW_EVAL_SET": "/x"})
			if code := a.run(ctx, []string{"run", "--set-config", bad}); code != 2 {
				t.Errorf("%q: exit = %d, want 2", bad, code)
			}
			if strings.Contains(errb.String(), "SECRET") {
				t.Errorf("%q: stderr echoes the value: %q", bad, errb.String())
			}
			if want := "lweval: --set-config: want key=value\n"; errb.String() != want {
				t.Errorf("%q: stderr = %q, want %q", bad, errb.String(), want)
			}
		}
	})
}

// TestSetConfigVariant pins --set-config end to end through `run`: lw runs
// under a private XDG_CONFIG_HOME holding a 0600 copy of the user's config
// with ONLY the named key changed, that directory is gone when run returns,
// and run.json records the variable's name and never its value. An unknown
// key is refused before lw is ever started (037 T3).
func TestSetConfigVariant(t *testing.T) {
	const config = "[llm]\nmodel = \"m\"\nthinking = \"off\"\napi_key = \"k\"\n"

	setup := func(t *testing.T) (set string, env map[string]string) {
		t.Helper()
		xdg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(xdg, "lw"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(xdg, "lw", "config.toml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", xdg)
		t.Setenv("TMPDIR", t.TempDir())
		return newSet(t), map[string]string{"XDG_CONFIG_HOME": xdg}
	}

	t.Run("the rewritten copy", func(t *testing.T) {
		set, env := setup(t)
		f := &fakeLW{}
		a, out, errb := newApp(f, env)
		code := a.run(context.Background(), []string{
			"run", "--set", set, "--lw", "lw-fake", "--n", "1", "--parallel", "1",
			"--set-config", "llm.thinking=on",
		})
		if code != 0 {
			t.Fatalf("exit = %d\nstderr: %s\nstdout: %s", code, errb.String(), out.String())
		}
		if len(f.seen) != 1 {
			t.Fatalf("lw ran %d times with a config override, want 1", len(f.seen))
		}
		s := f.seen[0]
		if s.fileErr != nil {
			t.Fatalf("lw could not read its config: %v", s.fileErr)
		}

		// Decoded, the copy equals the original except for that one key.
		var want, got map[string]any
		if _, err := toml.Decode(config, &want); err != nil {
			t.Fatal(err)
		}
		if _, err := toml.Decode(string(s.bytes), &got); err != nil {
			t.Fatalf("the copy is not TOML: %v\n%s", err, s.bytes)
		}
		want["llm"].(map[string]any)["thinking"] = "on"
		if !reflect.DeepEqual(got, want) {
			t.Errorf("config copy decodes to %v, want %v", got, want)
		}
		if want := "[llm]\nmodel = \"m\"\nthinking = \"on\"\napi_key = \"k\"\n"; string(s.bytes) != want {
			t.Errorf("config copy bytes = %q, want %q", s.bytes, want)
		}
		if s.mode != 0o600 || s.dirMode != 0o700 {
			t.Errorf("modes: file %v, dir %v; want 0600 and 0700", s.mode, s.dirMode)
		}
		if env["XDG_CONFIG_HOME"] == s.xdg {
			t.Error("lw ran under the user's own config dir")
		}
		if _, err := os.Stat(s.xdg); !os.IsNotExist(err) {
			t.Errorf("the variant config dir %s survived run (%v)", s.xdg, err)
		}
		// `lw version` also runs under the variant: it must see the same
		// config the cases do.
		for _, c := range f.calls {
			if v, ok := lastEnv(c.Env, "XDG_CONFIG_HOME"); !ok || v != s.xdg {
				t.Errorf("%v ran with XDG_CONFIG_HOME=%q, want %q", c.Args, v, s.xdg)
			}
		}

		runs, err := filepath.Glob(filepath.Join(set, "runs", "*", "run.json"))
		if err != nil || len(runs) != 1 {
			t.Fatalf("run.json files = %v (%v), want one", runs, err)
		}
		raw, err := os.ReadFile(runs[0])
		if err != nil {
			t.Fatal(err)
		}
		var info eval.RunInfo
		if err := json.Unmarshal(raw, &info); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(info.EnvKeys, []string{"XDG_CONFIG_HOME"}) {
			t.Errorf("env_keys = %v, want [XDG_CONFIG_HOME]", info.EnvKeys)
		}
		if strings.Contains(string(raw), s.xdg) {
			t.Errorf("run.json records the variant dir's path:\n%s", raw)
		}
		if info.Note != "set-config: llm.thinking=on" {
			t.Errorf("note = %q, want %q", info.Note, "set-config: llm.thinking=on")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(runs[0]), "results.json")); err != nil {
			t.Errorf("run did not score: %v", err)
		}
	})

	t.Run("an explicit note wins", func(t *testing.T) {
		set, env := setup(t)
		a, _, errb := newApp(&fakeLW{}, env)
		if code := a.run(context.Background(), []string{
			"run", "--set", set, "--lw", "lw-fake", "--n", "1", "--parallel", "1",
			"--set-config", "llm.thinking=on", "--note", "my label",
		}); code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
		runs, _ := filepath.Glob(filepath.Join(set, "runs", "*", "run.json"))
		var info eval.RunInfo
		raw, err := os.ReadFile(runs[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			t.Fatal(err)
		}
		if info.Note != "my label" {
			t.Errorf("note = %q, want %q", info.Note, "my label")
		}
	})

	t.Run("an unknown key is refused before any run", func(t *testing.T) {
		set, env := setup(t)
		f := &fakeLW{}
		a, out, errb := newApp(f, env)
		code := a.run(context.Background(), []string{
			"run", "--set", set, "--lw", "lw-fake", "--n", "1", "--set-config", "llm.nope=1",
		})
		if code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if want := "lweval: --set-config llm.nope: no such key in config.toml\n"; errb.String() != want {
			t.Errorf("stderr = %q, want %q", errb.String(), want)
		}
		if len(f.calls) != 0 {
			t.Errorf("lw was started %d times for a refused variant: %v", len(f.calls), f.calls)
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want nothing", out.String())
		}
		if _, err := os.Stat(filepath.Join(set, "runs")); !os.IsNotExist(err) {
			t.Errorf("a run directory was created for a refused variant (%v)", err)
		}
	})
}

// TestShowGolden pins the scorecard on a fixture results file: one table per
// group (ask, then ingest), columns metric, value, ±SE, cases, runs (037 T3).
func TestShowGolden(t *testing.T) {
	a, out, errb := newApp(nil, nil)
	if code := a.run(context.Background(), []string{"show", filepath.Join("testdata", "show-run")}); code != 0 {
		t.Fatalf("exit = %d: %s", code, errb.String())
	}
	testutil.Golden(t, filepath.Join("testdata", "show.golden"), out.Bytes())
}

func TestShowRefusals(t *testing.T) {
	dir := t.TempDir()
	a, _, errb := newApp(nil, nil)
	if code := a.run(context.Background(), []string{"show", dir}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "results.json") || !strings.Contains(errb.String(), "lweval score") {
		t.Errorf("stderr = %q, want one naming results.json and saying how to make it", errb.String())
	}
}

// TestLwevalSnapshot pins `snapshot`: the file lands at
// <set>/vault-<yyyymmdd>.tar.gz, stdout is the one line to paste into
// cases.toml, and an existing snapshot is never overwritten — cases.toml pins
// its hash, so a silent replacement would break every set that names it
// (037 T3).
func TestLwevalSnapshot(t *testing.T) {
	vault := testutil.CopyFixture(t, "minimal")
	set := filepath.Join(t.TempDir(), "eval")
	a, out, errb := newApp(nil, nil)
	if code := a.run(context.Background(), []string{"snapshot", vault, set}); code != 0 {
		t.Fatalf("exit = %d: %s", code, errb.String())
	}
	file := filepath.Join(set, "vault-20261005.tar.gz")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	sum := sha256.Sum256(b)
	if want := fmt.Sprintf("snapshot_sha256 = %q\n", hex.EncodeToString(sum[:])); out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if !strings.Contains(errb.String(), "vault-20261005.tar.gz") {
		t.Errorf("stderr = %q, want it to name the file written", errb.String())
	}

	// A second snapshot on the same day is refused and leaves the first alone.
	a2, out2, errb2 := newApp(nil, nil)
	if code := a2.run(context.Background(), []string{"snapshot", vault, set}); code != 1 {
		t.Errorf("second snapshot: exit = %d, want 1", code)
	}
	if !strings.Contains(errb2.String(), "already exists") || out2.Len() != 0 {
		t.Errorf("second snapshot: stderr %q stdout %q; want an already-exists refusal", errb2.String(), out2.String())
	}
	if again, _ := os.ReadFile(file); !bytes.Equal(again, b) {
		t.Error("the existing snapshot was modified")
	}

	// A vault that cannot be snapshotted reports why and writes nothing.
	a3, _, errb3 := newApp(nil, nil)
	if code := a3.run(context.Background(), []string{"snapshot", filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "s")}); code != 1 {
		t.Errorf("missing vault: exit = %d, want 1", code)
	}
	if !strings.HasPrefix(errb3.String(), "lweval: ") {
		t.Errorf("missing vault: stderr = %q", errb3.String())
	}
}

// TestLwevalRunScoreCompare drives the whole loop through the CLI: two runs,
// each scored by `run`, re-scored by `score`, shown by `show`, and compared
// (037 T3).
func TestLwevalRunScoreCompare(t *testing.T) {
	set := newSet(t)
	f := &fakeLW{}
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	runOnce := func(extra ...string) (dir string) {
		t.Helper()
		a, out, errb := newApp(f, map[string]string{"LW_EVAL_SET": set})
		a.now = func() time.Time { return clock }
		args := append([]string{"run", "--lw", "lw-fake", "--n", "2", "--parallel", "1"}, extra...)
		if code := a.run(context.Background(), args); code != 0 {
			t.Fatalf("run exit = %d\nstderr: %s", code, errb.String())
		}
		id := "20261005T" + clock.Format("150405") + "Z-v9.9.9-test"
		dir = filepath.Join(set, "runs", id)
		if _, err := os.Stat(filepath.Join(dir, "results.json")); err != nil {
			t.Fatalf("run %s: no results.json: %v", id, err)
		}
		// The scorecard is printed, with the run directory named.
		for _, want := range []string{"run:   " + id, "fact_recall", "ask · 1 case · 2 runs · 0 failed"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("run output lacks %q:\n%s", want, out.String())
			}
		}
		clock = clock.Add(time.Minute)
		return dir
	}
	dirA := runOnce()
	dirB := runOnce("--note", "second")

	// score rewrites results.json: damage it, score again, get it back.
	good, err := os.ReadFile(filepath.Join(dirA, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "results.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, out, errb := newApp(nil, nil)
	if code := a.run(context.Background(), []string{"score", dirA}); code != 0 {
		t.Fatalf("score exit = %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), filepath.Join(dirA, "results.json")) {
		t.Errorf("score output %q does not name the file it wrote", out.String())
	}
	if again, _ := os.ReadFile(filepath.Join(dirA, "results.json")); !bytes.Equal(again, good) {
		t.Error("re-scoring did not reproduce results.json")
	}

	// show reads it back.
	a, out, errb = newApp(nil, nil)
	if code := a.run(context.Background(), []string{"show", dirB}); code != 0 {
		t.Fatalf("show exit = %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "note:  second") {
		t.Errorf("show output lacks the note:\n%s", out.String())
	}

	// compare: the fake answers identically, so Δ is 0 and SE is 0 on both
	// sides — within noise, not REAL.
	a, out, errb = newApp(nil, nil)
	if code := a.run(context.Background(), []string{"compare", dirA, dirB}); code != 0 {
		t.Fatalf("compare exit = %d: %s", code, errb.String())
	}
	for _, want := range []string{"fact_recall", "within noise", "failed: A 0 · B 0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("compare output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "REAL") {
		t.Errorf("identical runs compared as REAL:\n%s", out.String())
	}
	if strings.Contains(out.String(), "warning:") {
		t.Errorf("two runs of the same set, snapshot, lw and n warned:\n%s", out.String())
	}

	// compare names the file that is missing.
	a, _, errb = newApp(nil, nil)
	if code := a.run(context.Background(), []string{"compare", dirA, t.TempDir()}); code != 1 || !strings.Contains(errb.String(), "results.json") {
		t.Errorf("compare with an unscored run: exit %d, stderr %q", code, errb.String())
	}
}

func TestLwevalRunRefusals(t *testing.T) {
	set := newSet(t)

	t.Run("lw is not on PATH", func(t *testing.T) {
		a, _, errb := newApp(nil, map[string]string{"LW_EVAL_SET": set})
		a.lookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }
		if code := a.run(context.Background(), []string{"run"}); code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(errb.String(), "lweval: lw:") {
			t.Errorf("stderr = %q, want a lweval: lw: line", errb.String())
		}
	})

	t.Run("a bad set is reported, not run", func(t *testing.T) {
		a, _, errb := newApp(&fakeLW{}, map[string]string{"LW_EVAL_SET": t.TempDir()})
		if code := a.run(context.Background(), []string{"run"}); code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(errb.String(), "cases.toml") {
			t.Errorf("stderr = %q, want the set loader's message", errb.String())
		}
	})

	t.Run("the flag wins over the environment", func(t *testing.T) {
		f := &fakeLW{}
		a, _, errb := newApp(f, map[string]string{"LW_EVAL_SET": t.TempDir()})
		if code := a.run(context.Background(), []string{"run", "--set", set, "--lw", "lw-fake", "--n", "1", "--parallel", "1"}); code != 0 {
			t.Fatalf("exit = %d: %s", code, errb.String())
		}
		if len(f.calls) == 0 {
			t.Error("lw never ran")
		}
	})

	t.Run("parallel above the provider ceiling", func(t *testing.T) {
		a, _, errb := newApp(&fakeLW{}, map[string]string{"LW_EVAL_SET": set})
		if code := a.run(context.Background(), []string{"run", "--lw", "lw-fake", "--parallel", "3"}); code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(errb.String(), "parallel 3: at most 2") {
			t.Errorf("stderr = %q, want the runner's refusal", errb.String())
		}
	})
}

// TestSetConfigSecretNote pins A-037-7 end to end: a variant that changes a
// secret key records the key's NAME in the run note — never its value, in
// run.json, the scorecard or any message — while lw itself still gets the
// new value in the private config copy (037 T3).
func TestSetConfigSecretNote(t *testing.T) {
	const secret = "sk-NEW-SECRET-value-9"
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "lw"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "lw", "config.toml"), []byte("[llm]\nthinking = \"off\"\napi_key = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("TMPDIR", t.TempDir())
	set := newSet(t)

	f := &fakeLW{}
	a, out, errb := newApp(f, nil)
	code := a.run(context.Background(), []string{
		"run", "--set", set, "--lw", "lw-fake", "--n", "1", "--parallel", "1",
		"--set-config", "llm.api_key=" + secret, "--set-config", "llm.thinking=on",
	})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(f.seen) != 1 || !strings.Contains(string(f.seen[0].bytes), `api_key = "`+secret+`"`) {
		t.Fatalf("lw did not get the new key in its config copy: %+v", f.seen)
	}
	runs, _ := filepath.Glob(filepath.Join(set, "runs", "*", "*.json"))
	if len(runs) == 0 {
		t.Fatal("no run files")
	}
	for _, p := range append(runs, filepath.Join(set, "cases.toml")) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), secret) {
			t.Errorf("%s contains the secret", p)
		}
	}
	for name, text := range map[string]string{"stdout": out.String(), "stderr": errb.String()} {
		if strings.Contains(text, secret) {
			t.Errorf("%s contains the secret:\n%s", name, text)
		}
	}
	if want := "note:  set-config: llm.api_key llm.thinking=on\n"; !strings.Contains(out.String(), want) {
		t.Errorf("scorecard lacks %q:\n%s", want, out.String())
	}

	// A refusal about a secret-named key does not quote the value either.
	a, _, errb = newApp(&fakeLW{}, nil)
	if code := a.run(context.Background(), []string{
		"run", "--set", set, "--lw", "lw-fake", "--set-config", "llm.nope_token=" + secret,
	}); code != 1 || strings.Contains(errb.String(), secret) {
		t.Errorf("exit %d, stderr %q; want a refusal that does not echo the value", code, errb.String())
	}
}

// TestShutdownSignals pins A-037-7: SIGHUP — a closed terminal, a dropped
// ssh session — cancels the run like Ctrl-C, so the variant's config copy is
// deleted instead of the process dying with it on disk. The test delivers the
// real signal to itself (037 T3).
func TestShutdownSignals(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		found := false
		for _, s := range shutdownSignals {
			found = found || s == sig
		}
		if !found {
			t.Errorf("%v is not in shutdownSignals", sig)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP did not cancel the context")
	}
}

// TestRunSweepsStaleConfigCopies pins A-037-7: `run` starts by removing the
// user's own lweval-config-* directories that a killed run left behind, and
// only those that are over an hour old (037 T3).
func TestRunSweepsStaleConfigCopies(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	set := newSet(t)
	f := &fakeLW{}
	a, _, errb := newApp(f, nil)
	a.sweep = eval.SweepVariantDirs
	now := a.now()
	mk := func(name string, age time.Duration) string {
		dir := filepath.Join(tmp, name)
		if err := os.MkdirAll(filepath.Join(dir, "lw"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	stale, fresh := mk("lweval-config-100", 3*time.Hour), mk("lweval-config-200", 5*time.Minute)

	if code := a.run(context.Background(), []string{"run", "--set", set, "--lw", "lw-fake", "--n", "1", "--parallel", "1"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stale config copy survived (%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a fresh config copy was removed: %v", err)
	}
	if !strings.Contains(errb.String(), "removed 1 stale") {
		t.Errorf("stderr = %q, want a line saying one stale config copy was removed", errb.String())
	}
}

// TestRunJobTimeoutFlag pins A-037-9 at the command line: every lw command
// runs under a deadline, 20 minutes unless --job-timeout says otherwise, and
// 0 turns it off (037 T3).
func TestRunJobTimeoutFlag(t *testing.T) {
	set := newSet(t)
	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	calls := 0
	run := func(extra ...string) deadline {
		t.Helper()
		f := &fakeLW{}
		a, _, errb := newApp(f, nil)
		calls++
		at := base.Add(time.Duration(calls) * time.Minute) // a distinct run id per call
		a.now = func() time.Time { return at }
		args := append([]string{"run", "--set", set, "--lw", "lw-fake", "--n", "1", "--parallel", "1"}, extra...)
		if code := a.run(context.Background(), args); code != 0 {
			t.Fatalf("run %v: exit %d: %s", extra, code, errb.String())
		}
		if len(f.limits) != 1 {
			t.Fatalf("run %v: %d query commands, want 1", extra, len(f.limits))
		}
		return f.limits[0]
	}
	if d := run(); !d.ok || d.left < 19*time.Minute || d.left > 20*time.Minute {
		t.Errorf("default: %+v, want a deadline just under 20m", d)
	}
	if d := run("--job-timeout", "5m"); !d.ok || d.left < 4*time.Minute || d.left > 5*time.Minute {
		t.Errorf("--job-timeout 5m: %+v, want a deadline just under 5m", d)
	}
	if d := run("--job-timeout", "0"); d.ok {
		t.Errorf("--job-timeout 0: %+v, want no deadline", d)
	}
}

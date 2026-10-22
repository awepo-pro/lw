package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// prestageFake is a fake exec for testing runner prestage order and failure.
type prestageFake struct {
	mu     sync.Mutex
	argvs  map[string][][]string
	script func(ctx context.Context, key string, n int, argv []string) (Output, error)
}

func newPrestageFake() *prestageFake {
	return &prestageFake{argvs: map[string][][]string{}}
}

func (f *prestageFake) exec(ctx context.Context, c Cmd) (Output, error) {
	if len(c.Args) == 1 && c.Args[0] == "version" {
		return Output{Stdout: []byte("lw v9.9.9-test\n")}, nil
	}
	// For prestage and query, --vault is in the arguments.
	scratch := ""
	for i := 0; i < len(c.Args)-1; i++ {
		if c.Args[i] == "--vault" {
			scratch = c.Args[i+1]
			break
		}
	}
	key := filepath.Base(scratch)

	f.mu.Lock()
	f.argvs[key] = append(f.argvs[key], append([]string(nil), c.Args...))
	n := len(f.argvs[key])
	f.mu.Unlock()

	if f.script != nil {
		return f.script(ctx, key, n, c.Args)
	}
	return Output{Stdout: []byte(fmt.Sprintf("out%d\n", n)), Stderr: []byte(fmt.Sprintf("err%d\n", n))}, nil
}

// TestRunnerPrestageOrder pins 067 D5:
//  1. argv sequence == ingest --prepare --agent eval <abs inputs/…> --vault <scratch>,
//     stage --from <abs …> --vault <scratch>, then the query argv.
//  2. a prestage exit 1 -> the attempt is a setup failure, no query exec, result records
//     the failing argv.
func TestRunnerPrestageOrder(t *testing.T) {
	newSet := func(t *testing.T) *Set {
		set := newRunSet(t)
		for _, name := range []string{"rtt.md", "ops.json"} {
			if err := os.WriteFile(filepath.Join(set.Dir, "inputs", name), []byte("Content "+name+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		scRewriteCases(t, set, `[[ask]]
id = "staged-q"
kind = "covered"
q = "what is the RTT timeout?"
facts = [["timeout"]]
prestage = [
    ["ingest", "--prepare", "--agent", "eval", "inputs/rtt.md"],
    ["stage", "--from", "inputs/ops.json"]
]
`)
		loaded, err := LoadSet(set.Dir)
		if err != nil {
			t.Fatalf("LoadSet: %v", err)
		}
		return loaded
	}

	t.Run("argv sequence matches expected order with resolved inputs", func(t *testing.T) {
		set := newSet(t)
		f := newPrestageFake()
		out := filepath.Join(t.TempDir(), "run")
		runner := &Runner{
			LW: "lw-fake", Set: set, N: 1, Parallel: 1, Out: out, Exec: f.exec,
			Backoff: time.Millisecond, now: stepClock(),
		}
		if err := runner.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}

		scratchKey := "staged-q-1"
		scratch := filepath.Join(out, ".work", scratchKey)
		absRTT := filepath.Join(set.Dir, "inputs", "rtt.md")
		absOps := filepath.Join(set.Dir, "inputs", "ops.json")

		wantArgvs := [][]string{
			{"ingest", "--prepare", "--agent", "eval", absRTT, "--vault", scratch},
			{"stage", "--from", absOps, "--vault", scratch},
			{"query", "--vault", scratch, "what is the RTT timeout?"},
		}

		gotArgvs := f.argvs[scratchKey]
		if !reflect.DeepEqual(gotArgvs, wantArgvs) {
			t.Fatalf("argv sequence mismatch\n got: %v\nwant: %v", gotArgvs, wantArgvs)
		}

		var meta RunMeta
		readJSON(t, filepath.Join(out, "staged-q", "1", "meta.json"), &meta)
		if meta.Attempts != 1 || meta.Failed || meta.ExitCode != 0 || len(meta.SetupFailure) != 0 {
			t.Errorf("meta = %+v, want clean run", meta)
		}
	})

	t.Run("prestage exit 1 is setup failure with no query exec and records failing argv", func(t *testing.T) {
		set := newSet(t)
		f := newPrestageFake()
		absRTT := filepath.Join(set.Dir, "inputs", "rtt.md")

		// First call (ingest --prepare) exits 1 with error
		f.script = func(_ context.Context, _ string, n int, argv []string) (Output, error) {
			if strings.Contains(strings.Join(argv, " "), "ingest --prepare") {
				return Output{ExitCode: 1, Stderr: []byte("failed prepare\n")}, nil
			}
			return Output{Stdout: []byte(fmt.Sprintf("out%d\n", n))}, nil
		}

		out := filepath.Join(t.TempDir(), "run")
		runner := &Runner{
			LW: "lw-fake", Set: set, N: 1, Parallel: 1, Out: out, Exec: f.exec,
			Backoff: time.Millisecond, now: stepClock(),
		}
		if err := runner.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}

		scratchKey := "staged-q-1"
		scratch := filepath.Join(out, ".work", scratchKey)
		expectedFailingArgv := []string{"ingest", "--prepare", "--agent", "eval", absRTT, "--vault", scratch}

		gotArgvs := f.argvs[scratchKey]
		// Each attempt runs ingest --prepare and fails; query must NEVER run.
		for _, argv := range gotArgvs {
			if len(argv) > 0 && argv[0] == "query" {
				t.Fatalf("query exec ran despite prestage exit 1: %v", argv)
			}
		}

		var meta RunMeta
		readJSON(t, filepath.Join(out, "staged-q", "1", "meta.json"), &meta)
		if !meta.Failed || meta.ExitCode != 1 {
			t.Errorf("meta = %+v, want Failed=true and ExitCode=1", meta)
		}
		if !reflect.DeepEqual(meta.SetupFailure, expectedFailingArgv) {
			t.Errorf("meta.SetupFailure = %v, want %v", meta.SetupFailure, expectedFailingArgv)
		}
	})
}

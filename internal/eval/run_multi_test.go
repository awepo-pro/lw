package eval

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// TestRunnerIngestMultiInputArgv pins 049's runner half: a case with `inputs`
// is ONE lw call that gets every file, absolute and in the order written,
// after the scratch vault; a case with a single `input` still gets exactly
// one path, and a query still gets its question — the argv shape every
// earlier run was made with.
func TestRunnerIngestMultiInputArgv(t *testing.T) {
	set := newRunSet(t)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(set.Dir, "inputs", name), []byte("Article "+name+".\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scRewriteCases(t, set, `[[ask]]
id = "kv-cache"
kind = "covered"
q = "How does the KV cache work?"
facts = [["keys"]]

[[ingest]]
id = "three"
inputs = ["inputs/c.txt", "inputs/a.txt", "inputs/b.txt"]
facts = [["x"]]

[[ingest]]
id = "one"
input = "inputs/paper.txt"
facts = [["x"]]

[[ingest]]
id = "listed-once"
inputs = ["inputs/b.txt"]
facts = [["x"]]
`)
	set, err := LoadSet(set.Dir)
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}

	// The fake lw only records its argv: no changeset is left, which a real
	// ingest that staged nothing leaves too, and the runner must cope.
	var (
		mu    sync.Mutex
		argvs = map[string][]string{} // scratch dir name -> args
	)
	exec := func(_ context.Context, c Cmd) (Output, error) {
		if len(c.Args) == 1 && c.Args[0] == "version" {
			return Output{Stdout: []byte("lw v9.9.9-test\n")}, nil
		}
		mu.Lock()
		defer mu.Unlock()
		argvs[filepath.Base(c.Args[2])] = append([]string(nil), c.Args...)
		return Output{}, nil
	}
	out := filepath.Join(t.TempDir(), "runs", "20261005T120000Z-v9.9.9-test")
	r := &Runner{LW: "lw-fake", Set: set, N: 1, Parallel: 1, Out: out, Exec: exec, Backoff: time.Millisecond, now: stepClock()}
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	abs := func(name string) string { return filepath.Join(set.Dir, "inputs", name) }
	scratch := func(key string) string { return filepath.Join(out, ".work", key) }
	want := map[string][]string{
		"kv-cache-1":    {"query", "--vault", scratch("kv-cache-1"), "How does the KV cache work?"},
		"three-1":       {"ingest", "--vault", scratch("three-1"), abs("c.txt"), abs("a.txt"), abs("b.txt")},
		"one-1":         {"ingest", "--vault", scratch("one-1"), abs("paper.txt")},
		"listed-once-1": {"ingest", "--vault", scratch("listed-once-1"), abs("b.txt")},
	}
	if !reflect.DeepEqual(argvs, want) {
		t.Errorf("argv by job\n got %q\nwant %q", argvs, want)
	}
	for key, args := range argvs {
		for _, p := range args[3:] {
			if key != "kv-cache-1" && !filepath.IsAbs(p) {
				t.Errorf("%s: path %q is not absolute", key, p)
			}
		}
	}
}

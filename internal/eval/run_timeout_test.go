package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingExec wraps the fake lw: for the job commands of case key, the
// attempts listed in block hang until their context ends — a provider that
// never answers — and every other command is served by the fake.
type blockingExec struct {
	t     *testing.T
	f     *fakeLW
	key   string
	block map[int]bool // attempt number -> hang

	mu        sync.Mutex
	attempts  int
	deadlines []bool // per job command of key: did its ctx carry a deadline
	limits    []time.Duration
}

func (b *blockingExec) exec(ctx context.Context, c Cmd) (Output, error) {
	if len(c.Args) == 4 && filepath.Base(c.Args[2]) == b.key {
		b.mu.Lock()
		b.attempts++
		n := b.attempts
		dl, ok := ctx.Deadline()
		b.deadlines = append(b.deadlines, ok)
		if ok {
			b.limits = append(b.limits, time.Until(dl))
		}
		b.mu.Unlock()
		if b.block[n] {
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				// A Runner that never cancels the attempt would otherwise hang
				// the test until the go test timeout instead of failing it.
				b.t.Errorf("attempt %d of %s was never cancelled", n, b.key)
			}
			return Output{Stdout: []byte("partial answer"), Stderr: []byte("killed at the deadline")}, context.DeadlineExceeded
		}
	}
	return b.f.Exec(ctx, c)
}

// TestRunnerJobTimeout pins A-037-9: JobTimeout bounds one attempt. An
// attempt that outlives it is cancelled and counts as a failed attempt — so
// it gets the one retry like any other — and a case whose last attempt timed
// out is recorded as failed with TimedOut set, without stopping the rest of
// the run. A provider that hangs must cost one case 2×JobTimeout, not the
// whole run (037 T1/T3).
func TestRunnerJobTimeout(t *testing.T) {
	set := newRunSet(t)
	const timeout = 60 * time.Millisecond
	runs := 0
	setup := func(block map[int]bool) (*Runner, *blockingExec, string) {
		f := newFakeLW(t)
		be := &blockingExec{t: t, f: f, key: "kv-cache-1", block: block}
		runs++
		out := filepath.Join(set.Dir, "runs", fmt.Sprintf("r-%d", runs))
		r := newRunner(set, f, out)
		r.Only = "kv-cache"
		r.Exec = be.exec
		r.JobTimeout = timeout
		return r, be, out
	}
	readMeta := func(t *testing.T, out string) (RunMeta, string) {
		t.Helper()
		var m RunMeta
		raw := readJSON(t, filepath.Join(out, "kv-cache", "1", "meta.json"), &m)
		return m, string(raw)
	}

	t.Run("a timed-out first attempt is retried and can succeed", func(t *testing.T) {
		r, be, out := setup(map[int]bool{1: true})
		start := time.Now()
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("Run took %v", d)
		}
		m, raw := readMeta(t, out)
		if m.Attempts != 2 || m.Failed || m.ExitCode != 0 || m.TimedOut {
			t.Errorf("meta = %+v, want 2 attempts, not failed, not timed out (the last attempt finished)", m)
		}
		if strings.Contains(raw, "timed_out") {
			t.Errorf("meta.json mentions timed_out for a case that finished:\n%s", raw)
		}
		if b, _ := os.ReadFile(filepath.Join(out, "kv-cache", "1", "stdout.txt")); strings.Contains(string(b), "partial") {
			t.Errorf("stdout.txt holds the timed-out attempt's output: %q", b)
		}
		if be.attempts != 2 {
			t.Errorf("%d attempts, want 2", be.attempts)
		}
	})

	t.Run("two timeouts is a failed case that says why", func(t *testing.T) {
		r, _, out := setup(map[int]bool{1: true, 2: true})
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v (a timed-out case is data, not a run error)", err)
		}
		m, raw := readMeta(t, out)
		if m.Attempts != 2 || !m.Failed || m.ExitCode == 0 || !m.TimedOut {
			t.Errorf("meta = %+v, want 2 attempts, failed, non-zero exit, TimedOut", m)
		}
		if !strings.Contains(raw, `"timed_out": true`) {
			t.Errorf("meta.json does not say timed_out:\n%s", raw)
		}
		if b, _ := os.ReadFile(filepath.Join(out, "kv-cache", "1", "stdout.txt")); string(b) != "partial answer" {
			t.Errorf("stdout.txt = %q, want what the last attempt printed before it was cut", b)
		}
		if b, _ := os.ReadFile(filepath.Join(out, "kv-cache", "1", "stderr.txt")); string(b) != "killed at the deadline" {
			t.Errorf("stderr.txt = %q", b)
		}
	})

	t.Run("one hung case does not stop the others", func(t *testing.T) {
		r, _, out := setup(map[int]bool{1: true, 2: true})
		r.Only = ""
		r.Holdout = false
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, c := range []string{"no-topic", "paper-text"} {
			var m RunMeta
			readJSON(t, filepath.Join(out, c, "1", "meta.json"), &m)
			if m.Failed || m.Attempts != 1 {
				t.Errorf("%s: meta %+v, want a clean single attempt", c, m)
			}
		}
	})

	t.Run("every attempt carries the deadline, and zero means none", func(t *testing.T) {
		r, be, _ := setup(nil)
		if err := r.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(be.deadlines) != 1 || !be.deadlines[0] || be.limits[0] <= 0 || be.limits[0] > timeout {
			t.Errorf("deadlines %v limits %v, want one deadline within %v", be.deadlines, be.limits, timeout)
		}

		r, be, _ = setup(nil)
		r.JobTimeout = 0
		r.Out = filepath.Join(set.Dir, "runs", "r-none")
		if err := r.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(be.deadlines) != 1 || be.deadlines[0] {
			t.Errorf("deadlines %v, want none when JobTimeout is 0", be.deadlines)
		}
	})

	t.Run("cancelling the run is not a timeout", func(t *testing.T) {
		r, _, out := setup(map[int]bool{1: true, 2: true})
		r.JobTimeout = time.Hour
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		err := r.Run(ctx)
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("Run err = %v, want the cancellation", err)
		}
		if _, serr := os.Stat(filepath.Join(out, "kv-cache", "1", "meta.json")); !os.IsNotExist(serr) {
			t.Errorf("a cancelled job wrote a meta.json (%v): it did not finish", serr)
		}
	})
}

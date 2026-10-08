package main

// sync_pusher_test.go pins the TUI's asynchronous pusher (042 D5): goroutine
// safe, one push at a time with at most one more queued behind it, a debounce
// so a burst of changes is one push, results to lw.log only, and an exit flush
// that waits for the in-flight push, sends what is left and reports.

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
)

// slowPush is a push whose duration the test controls: it counts calls,
// records the most that ever ran at once, announces each start, and returns
// when released (or when its context is cancelled).
type slowPush struct {
	calls   atomic.Int32
	running atomic.Int32
	maxRun  atomic.Int32
	started chan struct{}
	release chan struct{}
	result  syncPushResult
	sawDone atomic.Bool // the context was cancelled under a run
}

func newSlowPush() *slowPush {
	return &slowPush{started: make(chan struct{}, 64), release: make(chan struct{}, 64), result: syncPushResult{Remote: "r", Pushed: 1}}
}

func (s *slowPush) run(ctx context.Context) syncPushResult {
	s.calls.Add(1)
	n := s.running.Add(1)
	for {
		m := s.maxRun.Load()
		if n <= m || s.maxRun.CompareAndSwap(m, n) {
			break
		}
	}
	s.started <- struct{}{}
	defer s.running.Add(-1)
	select {
	case <-s.release:
		return s.result
	case <-ctx.Done():
		s.sawDone.Store(true)
		return syncPushResult{Err: ctx.Err()}
	}
}

func waitStarted(t *testing.T, s *slowPush) {
	t.Helper()
	select {
	case <-s.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the push never started")
	}
}

// TestTUIPusherSingleFlight: 5 triggers during a slow push are exactly 2
// pushes — the one in flight and one queued behind it — and never two at once;
// the exit flush waits for them and reports.
func TestTUIPusherSingleFlight(t *testing.T) {
	t.Run("5 triggers during a slow push are 2 pushes", func(t *testing.T) {
		s := newSlowPush()
		p := newSyncPusher(s.run, 5*time.Millisecond)

		p.Trigger()
		waitStarted(t, s) // the first push is now in flight and slow
		for i := 0; i < 4; i++ {
			p.Trigger()
		}
		time.Sleep(50 * time.Millisecond) // long past the debounce: a second push must still not start
		if n := s.calls.Load(); n != 1 {
			t.Fatalf("%d pushes started while the first was in flight, want 1", n)
		}

		s.release <- struct{}{} // finish the first; the queued one runs next
		waitStarted(t, s)
		s.release <- struct{}{}

		out := p.Flush(5*time.Second, func() bool { return false })
		if n := s.calls.Load(); n != 2 {
			t.Errorf("pushes = %d, want exactly 2", n)
		}
		if m := s.maxRun.Load(); m != 1 {
			t.Errorf("%d pushes ran at once, want 1", m)
		}
		if out.Err != nil || out.Pushed != 2 || out.Remote != "r" {
			t.Errorf("flush = %+v, want 2 commits to r and no error", out)
		}
	})

	t.Run("a burst before the first push is one push", func(t *testing.T) {
		s := newSlowPush()
		s.release <- struct{}{}
		p := newSyncPusher(s.run, 400*time.Millisecond)
		for i := 0; i < 5; i++ {
			p.Trigger()
			time.Sleep(5 * time.Millisecond)
		}
		waitStarted(t, s)
		out := p.Flush(5*time.Second, func() bool { return false })
		if n := s.calls.Load(); n != 1 {
			t.Errorf("pushes = %d, want 1 (the debounce coalesces a burst)", n)
		}
		if out.Err != nil || out.Pushed != 1 {
			t.Errorf("flush = %+v", out)
		}
	})

	t.Run("a trigger does not push before the debounce", func(t *testing.T) {
		s := newSlowPush()
		s.release <- struct{}{}
		p := newSyncPusher(s.run, 300*time.Millisecond)
		start := time.Now()
		p.Trigger()
		waitStarted(t, s)
		if d := time.Since(start); d < 250*time.Millisecond {
			t.Errorf("the push started after %v, want it held for the 300ms debounce", d)
		}
		p.Flush(5*time.Second, func() bool { return false })
	})

	t.Run("triggers from many goroutines", func(t *testing.T) {
		s := newSlowPush()
		for i := 0; i < 64; i++ {
			s.release <- struct{}{}
		}
		p := newSyncPusher(s.run, time.Millisecond)
		var wg sync.WaitGroup
		for g := 0; g < 16; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 20; i++ {
					p.Trigger()
					time.Sleep(time.Millisecond)
				}
			}()
		}
		wg.Wait()
		p.Flush(10*time.Second, func() bool { return false })
		if m := s.maxRun.Load(); m != 1 {
			t.Errorf("%d pushes ran at once, want 1", m)
		}
		if s.calls.Load() < 1 {
			t.Error("no push ran")
		}
	})
}

// TestTUIPusherFlush: the exit flush waits for the push in flight, sends
// what is left when the tree or HEAD is ahead, bounds itself, and reports.
func TestTUIPusherFlush(t *testing.T) {
	t.Run("nothing happened: no push, no report", func(t *testing.T) {
		s := newSlowPush()
		p := newSyncPusher(s.run, 5*time.Millisecond)
		out := p.Flush(time.Second, func() bool { return false })
		if s.calls.Load() != 0 || out != (syncPushResult{}) {
			t.Errorf("calls %d, flush %+v; want neither", s.calls.Load(), out)
		}
	})

	t.Run("nothing triggered but the tree is ahead: a final push", func(t *testing.T) {
		s := newSlowPush()
		s.release <- struct{}{}
		p := newSyncPusher(s.run, 5*time.Millisecond)
		out := p.Flush(time.Second, func() bool { return true })
		if s.calls.Load() != 1 || out.Pushed != 1 || out.Err != nil {
			t.Errorf("calls %d, flush %+v; want one final push", s.calls.Load(), out)
		}
	})

	t.Run("waits for the push in flight", func(t *testing.T) {
		s := newSlowPush()
		p := newSyncPusher(s.run, time.Millisecond)
		p.Trigger()
		waitStarted(t, s)
		go func() {
			time.Sleep(150 * time.Millisecond)
			s.release <- struct{}{}
		}()
		start := time.Now()
		out := p.Flush(5*time.Second, func() bool { return false })
		if d := time.Since(start); d < 100*time.Millisecond {
			t.Errorf("flush returned after %v, before the push finished", d)
		}
		if out.Err != nil || out.Pushed != 1 || s.calls.Load() != 1 {
			t.Errorf("flush %+v, calls %d; want the in-flight push reported and no second one", out, s.calls.Load())
		}
	})

	t.Run("a trigger still waiting out its debounce is sent now", func(t *testing.T) {
		s := newSlowPush()
		s.release <- struct{}{}
		p := newSyncPusher(s.run, time.Hour)
		p.Trigger()
		start := time.Now()
		out := p.Flush(5*time.Second, func() bool { return true })
		if s.calls.Load() != 1 || out.Pushed != 1 {
			t.Errorf("calls %d, flush %+v; want the pending push sent at exit", s.calls.Load(), out)
		}
		if time.Since(start) > 3*time.Second {
			t.Error("the flush waited out the debounce")
		}
	})

	t.Run("is bounded by its budget and says so", func(t *testing.T) {
		s := newSlowPush() // never released
		p := newSyncPusher(s.run, time.Millisecond)
		p.Trigger()
		waitStarted(t, s)
		start := time.Now()
		out := p.Flush(200*time.Millisecond, func() bool { return true })
		if d := time.Since(start); d > 400*time.Millisecond {
			t.Errorf("flush took %v against a 200ms budget: it must stay inside it", d)
		}
		if out.Err == nil || !strings.Contains(out.Err.Error(), "timed out") {
			t.Errorf("flush error = %v, want a timeout", out.Err)
		}
		if !s.sawDone.Load() {
			t.Error("the stuck push was not cancelled")
		}
	})

	t.Run("a failed push is reported with its error", func(t *testing.T) {
		boom := errors.New("remote on fire")
		p := newSyncPusher(func(ctx context.Context) syncPushResult { return syncPushResult{Err: boom} }, time.Millisecond)
		p.Trigger()
		out := p.Flush(5*time.Second, func() bool { return false })
		if !errors.Is(out.Err, boom) {
			t.Errorf("flush = %+v, want the push's error", out)
		}
	})

	t.Run("a later success clears an earlier failure", func(t *testing.T) {
		var n atomic.Int32
		p := newSyncPusher(func(ctx context.Context) syncPushResult {
			if n.Add(1) == 1 {
				return syncPushResult{Err: errors.New("first try failed")}
			}
			return syncPushResult{Remote: "r", Pushed: 2}
		}, time.Millisecond)
		p.Trigger()
		time.Sleep(50 * time.Millisecond)
		out := p.Flush(5*time.Second, func() bool { return true })
		if out.Err != nil || out.Pushed != 2 {
			t.Errorf("flush = %+v, want the final success", out)
		}
	})

	t.Run("triggers after the flush are ignored", func(t *testing.T) {
		s := newSlowPush()
		p := newSyncPusher(s.run, time.Millisecond)
		p.Flush(time.Second, func() bool { return false })
		p.Trigger()
		time.Sleep(50 * time.Millisecond)
		if s.calls.Load() != 0 {
			t.Error("a push started after the flush")
		}
	})
}

// TestTUIAutoSyncEndToEnd: the real wiring — an engine commit through the hook
// helper triggers the pusher, the push reaches the remote, and the exit line is
// the CLI's success line.
func TestTUIAutoSyncEndToEnd(t *testing.T) {
	a, _, remote := syncPair(t)
	orig := syncPushDebounce
	syncPushDebounce = 10 * time.Millisecond
	t.Cleanup(func() { syncPushDebounce = orig })
	a.act()
	cfg := mustLoadConfig(t)

	as := newTUIAutoSync(a.root, cfg)
	if as == nil {
		t.Fatal("no auto-sync for a synced vault with remotes")
	}
	e, err := openVaultEngine(a.root, as)
	if err != nil {
		t.Fatalf("openVaultEngine: %v", err)
	}
	defer e.Close()
	if _, err := e.OpenChangeset("tui commit", stage.Author{Kind: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Append(stage.Op{
		Kind: stage.OpCreatePage, Path: "wiki/concepts/from-tui.md",
		Content:   []byte("---\ntitle: From TUI\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\ntags: [inference]\nconfidence: medium\n---\n\n# From TUI\n\nSee [[kv-cache]] and [[flash-attention]].\n"),
		Rationale: "test", Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit("tui page"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var line bytes.Buffer
	as.finish(&line)
	if want := "sync: pushed 1 commit(s) to " + remote + "\n"; line.String() != want {
		t.Errorf("exit line = %q, want %q", line.String(), want)
	}
	if subjects := remoteSubjects(t, remote); subjects[0] != "lw 000001: tui page" {
		t.Errorf("the remote's tip is %q, want %q", subjects[0], "lw 000001: tui page")
	}
	if remoteFile(t, remote, "wiki/concepts/from-tui.md") == "" {
		t.Error("the committed page is not on the remote")
	}

	// A second finish has nothing left to say.
	line.Reset()
	as.finish(&line)
	if line.Len() != 0 {
		t.Errorf("a second finish printed %q", line.String())
	}
}

// TestTUIAutoSyncExitPushesLateWork: work that landed after the last push —
// a session record, a manual edit — is committed and pushed at exit.
func TestTUIAutoSyncExitPushesLateWork(t *testing.T) {
	a, _, remote := syncPair(t)
	a.act()
	as := newTUIAutoSync(a.root, mustLoadConfig(t))
	a.write("notes/20261009-120000-late.md", "late\n")

	var line bytes.Buffer
	as.finish(&line)
	if want := "sync: pushed 1 commit(s) to " + remote + "\n"; line.String() != want {
		t.Errorf("exit line = %q, want %q", line.String(), want)
	}
	if remoteFile(t, remote, "notes/20261009-120000-late.md") == "" {
		t.Error("the late note was not pushed")
	}

	// And a failing exit push prints the CLI's failure line.
	a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + deadRemote(t) + "\"]\n")
	a.act()
	as = newTUIAutoSync(a.root, mustLoadConfig(t))
	a.write("notes/20261009-120100-later.md", "later\n")
	line.Reset()
	as.finish(&line)
	if got := line.String(); !strings.HasPrefix(got, "sync: push failed (") || !strings.HasSuffix(got, "); the commit is safe locally — lw sync will retry\n") {
		t.Errorf("exit line = %q, want the push-failed line", got)
	}
}

// TestTUIAutoSyncNeverWritesTheScreen: the pusher reports through lw.log, not
// stdout or stderr, while the TUI is up.
func TestTUIAutoSyncNeverWritesTheScreen(t *testing.T) {
	a, _, _ := syncPair(t)
	a.act()
	orig := syncPushDebounce
	syncPushDebounce = 5 * time.Millisecond
	t.Cleanup(func() { syncPushDebounce = orig })
	initLoggingAt(a.root)

	as := newTUIAutoSync(a.root, mustLoadConfig(t))
	a.write("notes/20261009-120000-x.md", "x\n")
	stdout, stderr, _ := captureRun(t, func() int {
		as.pusher.Trigger()
		time.Sleep(300 * time.Millisecond)
		return 0
	})
	if stdout != "" || stderr != "" {
		t.Errorf("the pusher wrote to the terminal: stdout %q stderr %q", stdout, stderr)
	}
	as.finish(&bytes.Buffer{})
	b, _ := readLog(a.root)
	if !strings.Contains(b, `msg="sync push"`) {
		t.Errorf("lw.log has no `sync push` record:\n%s", b)
	}
}

// readLog returns the vault's lw.log.
func readLog(root string) (string, error) {
	b, err := readFileString(filepath.Join(root, ".llmwiki", "logs", "lw.log"))
	return b, err
}

package main

// sync_s3f_test.go pins the cmd side of 042 A-042-9 beyond the concurrent
// appenders: the shape of the push path (commit, pull-with-rebase, push — with
// the lock gone for the push), the index after a rebasing push, the TUI
// pusher's debounce, the CLI's once-only queued push, and sync.json's
// read-modify-write.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/vault"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// TestPushPathIsCommitThenPullThenPush: the push after a change commits the
// work tree, pulls (which is where a divergence is replayed, under the vault
// lock and the journal quiesce), then pushes with the lock released. Push is
// never tried after a step that failed.
func TestPushPathIsCommitThenPullThenPush(t *testing.T) {
	newAuto := func(t *testing.T) (*autoSync, *syncPC) {
		a, _, _ := syncPair(t)
		settle(t, a)
		a.act()
		a.write("notes/20261009-100000-x.md", "x\n")
		auto := loadAutoSync(a.root)
		if auto == nil {
			t.Fatal("no auto-sync")
		}
		return auto, a
	}
	record := func(t *testing.T) (order *[]string, mu *sync.Mutex) {
		order, mu = new([]string), new(sync.Mutex)
		add := func(s string) { mu.Lock(); *order = append(*order, s); mu.Unlock() }
		oc, op, ou := syncCommitWork, syncPull, syncPush
		syncCommitWork = func(o vaultsync.Options, msg string) (bool, error) { add("commit"); return oc(o, msg) }
		syncPull = func(ctx context.Context, o vaultsync.Options, f int) (vaultsync.State, error) {
			add("pull")
			return op(ctx, o, f)
		}
		syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
			add("push")
			return ou(ctx, o)
		}
		t.Cleanup(func() { syncCommitWork, syncPull, syncPush = oc, op, ou })
		return order, mu
	}

	t.Run("commit, pull, push — and the vault lock is free for the push", func(t *testing.T) {
		auto, a := newAuto(t)
		order, mu := record(t)
		up := syncPush
		var lockErr error
		syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
			release, err := lockVault(a.root)
			if err != nil {
				lockErr = err
			} else {
				release()
			}
			return up(ctx, o)
		}
		res := auto.pushOnce(t.Context())
		if res.Err != nil || res.Pushed != 1 {
			t.Fatalf("pushOnce = %+v", res)
		}
		mu.Lock()
		got := strings.Join(*order, ",")
		mu.Unlock()
		if got != "commit,pull,push" {
			t.Errorf("calls = %s, want commit,pull,push", got)
		}
		if lockErr != nil {
			t.Errorf("the vault lock was held across the push: %v", lockErr)
		}
	})

	t.Run("a tree that is dirty after the commit does not stop the push", func(t *testing.T) {
		auto, _ := newAuto(t)
		order, mu := record(t)
		syncPull = func(ctx context.Context, o vaultsync.Options, f int) (vaultsync.State, error) {
			mu.Lock()
			*order = append(*order, "pull")
			mu.Unlock()
			return vaultsync.State{Remote: "r"}, fmt.Errorf("edit in the way: %w", vaultsync.ErrDirty)
		}
		res := auto.pushOnce(t.Context())
		mu.Lock()
		got := strings.Join(*order, ",")
		mu.Unlock()
		if got != "commit,pull,push" || res.Err != nil {
			t.Errorf("calls = %s, res = %+v; want the push to go on", got, res)
		}
	})

	t.Run("a pull that failed ends the push", func(t *testing.T) {
		auto, a := newAuto(t)
		order, mu := record(t)
		boom := errors.New("pull exploded")
		syncPull = func(ctx context.Context, o vaultsync.Options, f int) (vaultsync.State, error) {
			mu.Lock()
			*order = append(*order, "pull")
			mu.Unlock()
			return vaultsync.State{Remote: "r", Ahead: 1}, boom
		}
		res := auto.pushOnce(t.Context())
		mu.Lock()
		got := strings.Join(*order, ",")
		mu.Unlock()
		if got != "commit,pull" || !errors.Is(res.Err, boom) {
			t.Errorf("calls = %s, res = %+v; want push never tried and the pull's error", got, res)
		}
		if st := readSyncState(a.root); !strings.Contains(st.LastError, "pull exploded") || st.Unpushed != 1 {
			t.Errorf("sync.json = %+v; want the error and 1 unpushed", st)
		}
	})
}

// TestPushPathRebuildsTheIndexAfterARebase: a push that took the remote's
// commits under its own (a rebase) changed the pages the search index covers,
// so the index is rebuilt there as it is after a pull — otherwise it stays
// stale until some later verb happens to notice.
func TestPushPathRebuildsTheIndexAfterARebase(t *testing.T) {
	_, b, remote := syncPair(t)
	settle(t, b)
	b.act()
	if _, err := rebuildIndexIfStale(b.root); err != nil {
		t.Fatal(err)
	}
	if again, err := rebuildIndexIfStale(b.root); err != nil || again {
		t.Fatalf("the index is not current before the push (rebuilt again %v, err %v)", again, err)
	}
	pushFromScratch(t, remote, kvPage, b.read(kvPage)+"\nChanged on another PC.\n", "lw notes")
	b.write("notes/20261009-130000-from-b.md", "from b\n")
	auto := loadAutoSync(b.root)
	if auto == nil {
		t.Fatal("no auto-sync")
	}
	res := auto.pushOnce(t.Context())
	if res.Err != nil || res.Rebased != 1 || res.Pulled != 1 || res.Pushed < 1 {
		t.Fatalf("pushOnce = %+v, want a rebase of 1 over 1 pulled commit and a push", res)
	}
	if !strings.Contains(b.read(kvPage), "Changed on another PC.") {
		t.Fatal("the rebase did not bring the page in")
	}
	v, err := vault.Open(b.root)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Load(filepath.Join(b.root, ".llmwiki", "index.gob"))
	if err != nil || ix.StaleAgainst(v) {
		t.Errorf("the index after a rebasing push: load err %v, stale %v", err, err == nil && ix.StaleAgainst(v))
	}
}

// TestPusherDebounceRestartsOnEveryTrigger: the push starts once the triggers
// have been quiet for the debounce, so a trigger inside it pushes the start
// back — a steady trickle of changes is one push, after the last of them.
func TestPusherDebounceRestartsOnEveryTrigger(t *testing.T) {
	s := newSlowPush()
	s.release <- struct{}{}
	const debounce = 250 * time.Millisecond
	p := newSyncPusher(s.run, debounce)
	for i := 0; i < 4; i++ {
		p.Trigger()
		time.Sleep(150 * time.Millisecond) // inside the debounce each time
	}
	last := time.Now().Add(-150 * time.Millisecond)
	if n := s.calls.Load(); n != 0 {
		t.Fatalf("a push started after %d triggers that each came inside the debounce", n)
	}
	waitStarted(t, s)
	if d := time.Since(last); d < debounce-60*time.Millisecond {
		t.Errorf("the push started %v after the last trigger, want it held for the %v debounce", d, debounce)
	}
	out := p.Flush(5*time.Second, func() bool { return false })
	if n := s.calls.Load(); n != 1 || out.Err != nil {
		t.Errorf("pushes = %d (flush %+v), want the trickle to be 1 push", n, out)
	}
}

// TestRequestPushQueuesOnePush: a CLI verb asks for its push however many
// times it likes and gets one, after dispatch; the next ask after that push has
// run queues a new one.
func TestRequestPushQueuesOnePush(t *testing.T) {
	a, _, _ := syncPair(t)
	settle(t, a)
	a.act()
	auto := loadAutoSync(a.root)
	if auto == nil {
		t.Fatal("no auto-sync")
	}
	discardVerbEnd()
	t.Cleanup(discardVerbEnd)
	var pushes atomic.Int32
	up := syncPush
	syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
		pushes.Add(1)
		return up(ctx, o)
	}
	t.Cleanup(func() { syncPush = up })
	queued := func() int {
		verbEnd.Lock()
		defer verbEnd.Unlock()
		return len(verbEnd.fns)
	}

	for i := 0; i < 4; i++ {
		auto.requestPush()
	}
	if n := queued(); n != 1 {
		t.Fatalf("%d push(es) queued by 4 requests, want 1", n)
	}
	_, _, _ = captureRun(t, func() int { runVerbEnd(); return 0 })
	if n := pushes.Load(); n != 1 {
		t.Errorf("%d push(es) ran, want 1", n)
	}
	auto.requestPush()
	if n := queued(); n != 1 {
		t.Errorf("a request after the push ran queued %d, want 1 more", n)
	}
}

// TestSyncJSONUpdatesDoNotUndoEachOther: the TUI's pusher and the verb that
// started it both record into sync.json; a success and a failure recorded at
// the same moment leave both fields, whichever wrote last.
func TestSyncJSONUpdatesDoNotUndoEachOther(t *testing.T) {
	pinSyncClock(t, time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".llmwiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 150; i++ {
		os.Remove(syncStatePath(root))
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			recordSyncAhead(root, "host:vault", nil, 1)
		}()
		go func() {
			defer wg.Done()
			<-start
			recordSyncAhead(root, "", errors.New("boom"), 3)
		}()
		close(start)
		wg.Wait()
		st := readSyncState(root)
		if st.LastOK == "" || st.Remote != "host:vault" || st.LastError != "boom" {
			t.Fatalf("iteration %d: sync.json = %+v; one record undid the other", i, st)
		}
	}
}

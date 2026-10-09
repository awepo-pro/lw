package main

// sync_s3g_test.go pins 042 S3g (A-042-10) at the verb level: the vault lock is
// taken for the local mutation and not for the network (1), and a late write to
// the tree after the push path's commit is committed and pulled again, not
// reported as a divergence (4).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// vaultLockFree reports whether the vault lock can be taken right now, and
// gives it back.
func vaultLockFree(root string) bool {
	rel, err := stage.AcquireLock(filepath.Join(root, ".llmwiki"))
	if err != nil {
		return false
	}
	rel()
	return true
}

// TestSlowFetchDoesNotHoldTheVaultLock (1): a sync step whose fetch is slow — a
// server far away, the network gone — leaves the vault free for a foreground
// commit the whole time, whichever step it is. The ssh in the middle is a fake
// that is slow once.
func TestSlowFetchDoesNotHoldTheVaultLock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write bool // the step has work of its own to commit first
		drive func(t *testing.T, a *syncPC, auto *autoSync)
	}{
		{"the push path", true, func(t *testing.T, a *syncPC, auto *autoSync) {
			if res := auto.pushOnce(t.Context()); res.Err != nil || res.Pushed < 1 {
				t.Errorf("push = %+v", res)
			}
		}},
		{"the verb-start pull", false, func(t *testing.T, a *syncPC, auto *autoSync) {
			captureRun(t, func() int { auto.pull(); return 0 })
		}},
		{"an explicit sync", true, func(t *testing.T, a *syncPC, auto *autoSync) {
			stdout, stderr, code := captureRun(t, func() int { return run([]string{"sync", "--vault", a.root}) })
			if code != 0 {
				t.Errorf("lw sync: exit %d stdout %q stderr %q", code, stdout, stderr)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			syncHermetic(t)
			installFakeSSH(t)
			a := newSyncPC(t, "a")
			a.useFixture()
			if _, stderr, code := a.lw("sync", "init", "box:remote-vault", "--vault", a.root); code != 0 {
				t.Fatalf("init: exit %d stderr %q", code, stderr)
			}
			settle(t, a)
			a.act()
			auto := loadAutoSync(a.root)
			if auto == nil {
				t.Fatal("no auto-sync")
			}
			if tc.write {
				a.write("notes/20261009-130000-x.md", "x\n")
			}

			mark := filepath.Join(syncTemp(t), "fetching")
			t.Setenv("FAKE_SSH_MARK", mark)
			t.Setenv("FAKE_SSH_DELAY_ONCE", "2")
			done := make(chan struct{})
			go func() { defer close(done); tc.drive(t, a, auto) }()

			deadline := time.Now().Add(15 * time.Second)
			for {
				if _, err := os.Stat(mark); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Error("the fetch never started")
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			// The fetch is on the wire now.
			if !vaultLockFree(a.root) {
				t.Error("the vault lock is held while the fetch is on the wire")
			} else {
				foregroundCommit(t, a.root)
			}
			<-done
		})
	}
}

// TestPullHoldsTheVaultLockOnlyInsideItsQuiesce (1): vaultsync asks for the
// quiesce when it is about to change the tree and gives it back when it is done;
// the vault lock is exactly as long — free before it, free after, held (with
// the journal) inside. Both users of Pull are checked: the verb-start pull and
// the push path.
func TestPullHoldsTheVaultLockOnlyInsideItsQuiesce(t *testing.T) {
	type phase struct{ before, inside, after bool }
	run := func(t *testing.T, root string, drive func(auto *autoSync)) phase {
		t.Helper()
		var ph phase
		var seen bool
		orig := syncPull
		syncPull = func(ctx context.Context, o vaultsync.Options, maxFormat int) (vaultsync.State, error) {
			seen = true
			ph.before = vaultLockFree(root) // the fetch happens here in the real thing
			if o.Quiesce == nil {
				t.Error("the options have no Quiesce")
				return vaultsync.State{Remote: "r"}, nil
			}
			rel, err := o.Quiesce()
			if err != nil {
				t.Errorf("Quiesce: %v", err)
				return vaultsync.State{Remote: "r"}, nil
			}
			ph.inside = !vaultLockFree(root)
			rel()
			ph.after = vaultLockFree(root)
			return vaultsync.State{Remote: "r"}, nil
		}
		t.Cleanup(func() { syncPull = orig })
		auto := loadAutoSync(root)
		if auto == nil {
			t.Fatal("no auto-sync")
		}
		captureRun(t, func() int { drive(auto); return 0 })
		if !seen {
			t.Fatal("Pull was never called")
		}
		return ph
	}
	for name, drive := range map[string]func(auto *autoSync){
		"the verb-start pull": func(auto *autoSync) { auto.pull() },
		"the push path":       func(auto *autoSync) { auto.pushOnce(context.Background()) },
	} {
		t.Run(name, func(t *testing.T) {
			a, _, _ := syncPair(t)
			settle(t, a)
			a.act()
			ph := run(t, a.root, drive)
			if !ph.before || !ph.inside || !ph.after {
				t.Errorf("vault lock free before the quiesce %v (want true), held inside %v (want true), free after %v (want true)", ph.before, ph.inside, ph.after)
			}
		})
	}
}

// TestCommitWorkRunsUnderTheVaultLock (1): with the lock gone from around the
// pull, the commit to git takes it for itself, for the commit alone — a
// foreground commit cannot land in the middle of it — and a vault that is busy
// is not committed.
func TestCommitWorkRunsUnderTheVaultLock(t *testing.T) {
	for name, drive := range map[string]func(t *testing.T, a *syncPC, auto *autoSync){
		"the push path": func(t *testing.T, a *syncPC, auto *autoSync) { auto.pushOnce(t.Context()) },
		"an explicit sync": func(t *testing.T, a *syncPC, auto *autoSync) {
			captureRun(t, func() int { return run([]string{"sync", "--vault", a.root}) })
		},
	} {
		t.Run(name+": held for the commit, free around it", func(t *testing.T) {
			a, _, _ := syncPair(t)
			settle(t, a)
			a.act()
			auto := loadAutoSync(a.root)
			a.write("notes/20261009-130000-x.md", "x\n")
			var calls, heldInside, freeAfter int
			oc := syncCommitWork
			syncCommitWork = func(o vaultsync.Options, msg string) (bool, error) {
				calls++
				if !vaultLockFree(a.root) {
					heldInside++
				}
				return oc(o, msg)
			}
			t.Cleanup(func() { syncCommitWork = oc })
			op := syncPull
			syncPull = func(ctx context.Context, o vaultsync.Options, f int) (vaultsync.State, error) {
				if vaultLockFree(a.root) {
					freeAfter++
				}
				return op(ctx, o, f)
			}
			t.Cleanup(func() { syncPull = op })
			drive(t, a, auto)
			if calls == 0 || heldInside != calls {
				t.Errorf("the vault lock was held for %d of %d commits", heldInside, calls)
			}
			if freeAfter == 0 {
				t.Error("the vault lock was still held when the pull began")
			}
		})
	}

	t.Run("a busy vault is not committed", func(t *testing.T) {
		a, _, _ := syncPair(t)
		settle(t, a)
		a.act()
		a.write("notes/20261009-130000-x.md", "x\n")
		held, err := lockVault(a.root)
		if err != nil {
			t.Fatal(err)
		}
		defer held()
		if _, err := commitWorkLocked(a.root, vaultsync.Options{Dir: a.root}); !errors.Is(err, errSyncBusy) {
			t.Errorf("commitWorkLocked err = %v, want errSyncBusy", err)
		}
	})
}

// TestSyncQuiesceHoldsTheVaultAndTheJournal: the quiesce every sync call is
// made with takes the vault lock (so a commit cannot land in the middle of the
// mutation) and the journal lock (so no line is appended), and gives both back
// together. A vault that is busy, or has a commit half-applied, is refused —
// and the journal is not left held.
func TestSyncQuiesceHoldsTheVaultAndTheJournal(t *testing.T) {
	appendBlocked := func(t *testing.T, root string) (blocked bool, finish func()) {
		t.Helper()
		j, err := stage.OpenJournal(filepath.Join(root, ".llmwiki", "journal.ndjson"))
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			done <- j.Append(stage.Event{TS: time.Unix(1, 0).UTC(), Kind: stage.EvChangesetOpened, Changeset: "cs-q", Actor: stage.Author{Kind: "human"}, Message: "probe"})
		}()
		select {
		case <-done:
			return false, func() {}
		case <-time.After(150 * time.Millisecond):
			return true, func() { <-done }
		}
	}

	t.Run("held then released together", func(t *testing.T) {
		a, _, _ := syncPair(t)
		a.act()
		o := (&autoSync{root: a.root, remotes: []string{"x:y"}}).options()
		rel, err := o.Quiesce()
		if err != nil {
			t.Fatal(err)
		}
		if vaultLockFree(a.root) {
			t.Error("the vault lock is free inside the quiesce")
		}
		blocked, finish := appendBlocked(t, a.root)
		if !blocked {
			t.Error("a journal append went through the quiesce")
		}
		rel()
		finish()
		if !vaultLockFree(a.root) {
			t.Error("the vault lock was not given back with the quiesce")
		}
	})

	t.Run("a busy vault is refused", func(t *testing.T) {
		a, _, _ := syncPair(t)
		a.act()
		o := (&autoSync{root: a.root, remotes: []string{"x:y"}}).options()
		held, err := lockVault(a.root)
		if err != nil {
			t.Fatal(err)
		}
		defer held()
		if rel, err := o.Quiesce(); !errors.Is(err, errSyncBusy) {
			if err == nil {
				rel()
			}
			t.Fatalf("Quiesce err = %v, want errSyncBusy", err)
		}
		if blocked, finish := appendBlocked(t, a.root); blocked {
			finish()
			t.Error("a refused quiesce left the journal held")
		}
	})

	t.Run("a commit half-applied is refused", func(t *testing.T) {
		a, _, _ := syncPair(t)
		a.act()
		o := (&autoSync{root: a.root, remotes: []string{"x:y"}}).options()
		j, err := stage.OpenJournal(filepath.Join(a.root, ".llmwiki", "journal.ndjson"))
		if err != nil {
			t.Fatal(err)
		}
		if err := j.Append(stage.Event{TS: time.Unix(1, 0).UTC(), Kind: stage.EvCommitBegin, Changeset: "cs-halfway", Commit: "000009", Actor: stage.Author{Kind: "human"}}); err != nil {
			t.Fatal(err)
		}
		if rel, err := o.Quiesce(); !errors.Is(err, errCommitInterrupted) {
			if err == nil {
				rel()
			}
			t.Fatalf("Quiesce err = %v, want errCommitInterrupted", err)
		}
		if !vaultLockFree(a.root) {
			t.Error("a refused quiesce left the vault lock held")
		}
		if blocked, finish := appendBlocked(t, a.root); blocked {
			finish()
			t.Error("a refused quiesce left the journal held")
		}
	})
}

// TestPushPathPullsAgainAfterADirtyTree (4): a write that lands in the tracked
// tree between the push path's commit and its pull (a session record) makes the
// pull refuse with ErrDirty. That is a reason to commit it and pull again, not
// to push into what looks like a divergence.
func TestPushPathPullsAgainAfterADirtyTree(t *testing.T) {
	t.Run("calls", func(t *testing.T) {
		a, _, _ := syncPair(t)
		settle(t, a)
		a.act()
		auto := loadAutoSync(a.root)
		var calls []string
		oc, op, ou := syncCommitWork, syncPull, syncPush
		t.Cleanup(func() { syncCommitWork, syncPull, syncPush = oc, op, ou })
		syncCommitWork = func(o vaultsync.Options, msg string) (bool, error) {
			calls = append(calls, "commit")
			return oc(o, msg)
		}
		pulls := 0
		syncPull = func(ctx context.Context, o vaultsync.Options, f int) (vaultsync.State, error) {
			pulls++
			calls = append(calls, "pull")
			if pulls == 1 {
				return vaultsync.State{Remote: "r", Behind: 1}, vaultsync.ErrDirty
			}
			return vaultsync.State{Remote: "r"}, nil
		}
		syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
			calls = append(calls, "push")
			return vaultsync.State{Remote: "r"}, nil
		}
		res := auto.pushOnce(t.Context())
		if got := join(calls); got != "commit,pull,commit,pull,push" || res.Err != nil {
			t.Errorf("calls = %s, res = %+v; want commit,pull,commit,pull,push and no error", got, res)
		}
	})

	t.Run("a tree that stays dirty does not loop", func(t *testing.T) {
		a, _, _ := syncPair(t)
		settle(t, a)
		a.act()
		auto := loadAutoSync(a.root)
		var calls []string
		oc, op, ou := syncCommitWork, syncPull, syncPush
		t.Cleanup(func() { syncCommitWork, syncPull, syncPush = oc, op, ou })
		syncCommitWork = func(o vaultsync.Options, msg string) (bool, error) {
			calls = append(calls, "commit")
			return oc(o, msg)
		}
		syncPull = func(ctx context.Context, o vaultsync.Options, f int) (vaultsync.State, error) {
			calls = append(calls, "pull")
			return vaultsync.State{Remote: "r", Behind: 1}, vaultsync.ErrDirty
		}
		syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
			calls = append(calls, "push")
			return vaultsync.State{Remote: "r"}, nil
		}
		res := auto.pushOnce(t.Context())
		if got := join(calls); got != "commit,pull,commit,pull,push" || res.Err != nil {
			t.Errorf("calls = %s, res = %+v; want one retry and then the push", got, res)
		}
	})

	t.Run("nothing to take is not pulled again", func(t *testing.T) {
		a, _, _ := syncPair(t)
		settle(t, a)
		a.act()
		auto := loadAutoSync(a.root)
		var calls []string
		oc, op, ou := syncCommitWork, syncPull, syncPush
		t.Cleanup(func() { syncCommitWork, syncPull, syncPush = oc, op, ou })
		syncCommitWork = func(o vaultsync.Options, msg string) (bool, error) {
			calls = append(calls, "commit")
			return oc(o, msg)
		}
		syncPull = func(ctx context.Context, o vaultsync.Options, f int) (vaultsync.State, error) {
			calls = append(calls, "pull")
			return vaultsync.State{Remote: "r"}, vaultsync.ErrDirty
		}
		syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
			calls = append(calls, "push")
			return vaultsync.State{Remote: "r"}, nil
		}
		res := auto.pushOnce(t.Context())
		if got := join(calls); got != "commit,pull,commit,push" || res.Err != nil {
			t.Errorf("calls = %s, res = %+v; want the late write committed and no second fetch", got, res)
		}
	})

	t.Run("a late write and a moved remote", func(t *testing.T) {
		_, b, remote := syncPair(t)
		settle(t, b)
		b.act()
		pushFromScratch(t, remote, "notes/20261009-140000-from-a.md", "from a\n", "lw notes")
		b.write("notes/20261009-130000-from-b.md", "from b\n")
		auto := loadAutoSync(b.root)
		op := syncPull
		late := false
		syncPull = func(ctx context.Context, o vaultsync.Options, f int) (vaultsync.State, error) {
			if !late {
				late = true
				b.appendTo(kvPage, "\nrecorded after the commit\n") // a session record landing
			}
			return op(ctx, o, f)
		}
		t.Cleanup(func() { syncPull = op })
		res := auto.pushOnce(t.Context())
		if res.Err != nil || res.Rebased != 2 || res.Pushed != 2 {
			t.Fatalf("pushOnce = %+v; want the note and the late write committed, both replayed on the remote's commit, both pushed", res)
		}
		if remoteFile(t, remote, kvPage) == "" || remoteFile(t, remote, "notes/20261009-130000-from-b.md") == "" {
			t.Error("the late write or the note never reached the remote")
		}
	})
}

// TestSyncExplicitErrNamesABusyVault: when the vault becomes busy between the
// check and the quiesce, vaultsync hands back what the quiesce said wrapped; the
// verb says what it says for a busy vault at the check.
func TestSyncExplicitErrNamesABusyVault(t *testing.T) {
	wrapped := func(err error) error {
		return fmt.Errorf("vaultsync: could not hold off the journal's other writers: %w", err)
	}
	if got := syncExplicitErr(wrapped(errSyncBusy), vaultsync.State{}); got != errBusyTryAgain {
		t.Errorf("busy: %v", got)
	}
	if got := syncExplicitErr(wrapped(errCommitInterrupted), vaultsync.State{}); got != errCommitInterrupted {
		t.Errorf("interrupted: %v", got)
	}
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/vaultsync"
)

// W1 clean tree, remote unchanged: no cycle.
func TestSyncWatch_W1_CleanTree(t *testing.T) {
	var cycles int
	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return time.Unix(1000, 0) },
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.DirtyState{}, nil
		},
		remote: func() (bool, error) {
			return false, nil
		},
		cycle: func() error {
			cycles++
			return nil
		},
		logf: func(format string, a ...any) {},
	}

	w.step()
	if cycles != 0 {
		t.Fatalf("W1: cycle ran %d times on clean tree, want 0", cycles)
	}
}

// W2 dirty, now - Newest < quiet: no cycle. On the first step where now - Newest >= quiet: exactly one cycle.
func TestSyncWatch_W2_DirtyQuiet(t *testing.T) {
	currentTime := time.Unix(1000, 0)
	fileMtime := time.Unix(1000, 0)
	var cycles int

	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return currentTime },
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.DirtyState{
				Paths:  []string{"notes/test.md"},
				Newest: fileMtime,
			}, nil
		},
		remote: func() (bool, error) { return false, nil },
		cycle: func() error {
			cycles++
			return nil
		},
		logf: func(format string, a ...any) {},
	}

	// Step 1: now == Newest (now - Newest = 0 < 3s): no cycle
	w.step()
	if cycles != 0 {
		t.Fatalf("W2: step 1 ran %d cycles, want 0", cycles)
	}

	// Step 2: now - Newest = 2s < quiet: no cycle
	currentTime = time.Unix(1002, 0)
	w.step()
	if cycles != 0 {
		t.Fatalf("W2: step 2 ran %d cycles, want 0", cycles)
	}

	// Step 3: now - Newest = 3s >= quiet: exactly one cycle
	currentTime = time.Unix(1003, 0)
	w.step()
	if cycles != 1 {
		t.Fatalf("W2: step 3 ran %d cycles, want 1", cycles)
	}

	// Step 4: next step without new dirty change: no cycle
	currentTime = time.Unix(1004, 0)
	w.dirty = func() (vaultsync.DirtyState, error) {
		return vaultsync.DirtyState{}, nil
	}
	w.step()
	if cycles != 1 {
		t.Fatalf("W2: step 4 ran %d cycles, want 1", cycles)
	}
}

// W3 files that keep changing (Newest always fresh): a cycle once the tree has been dirty for maxWait.
func TestSyncWatch_W3_MaxWait(t *testing.T) {
	currentTime := time.Unix(1000, 0)
	var cycles int

	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return currentTime },
		dirty: func() (vaultsync.DirtyState, error) {
			// Newest is always fresh (equal to currentTime)
			return vaultsync.DirtyState{
				Paths:  []string{"notes/active.md"},
				Newest: currentTime,
			}, nil
		},
		remote: func() (bool, error) { return false, nil },
		cycle: func() error {
			cycles++
			return nil
		},
		logf: func(format string, a ...any) {},
	}

	// Run from t=0s to t=28s in 2s steps: no cycle because now - Newest == 0 < quiet
	// and now - firstDirty < maxWait (30s).
	for s := int64(0); s < 30; s += 2 {
		currentTime = time.Unix(1000+s, 0)
		w.step()
		if cycles != 0 {
			t.Fatalf("W3: cycle ran prematurely at %ds", s)
		}
	}

	// At t=30s, tree has been dirty for maxWait (30s): cycle must run!
	currentTime = time.Unix(1030, 0)
	w.step()
	if cycles != 1 {
		t.Fatalf("W3: cycle ran %d times at maxWait, want 1", cycles)
	}
}

// W4 the remote is asked only when interval has passed since the last ask; a changed remote starts one cycle even when the tree is clean. After a cycle the next remote ask is one interval later.
func TestSyncWatch_W4_RemoteInterval(t *testing.T) {
	currentTime := time.Unix(1000, 0)
	var remoteAsks, cycles int
	remoteChanged := false

	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return currentTime },
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.DirtyState{}, nil
		},
		remote: func() (bool, error) {
			remoteAsks++
			return remoteChanged, nil
		},
		cycle: func() error {
			cycles++
			return nil
		},
		logf: func(format string, a ...any) {},
	}

	// Step 1 at t=0: initial remote ask occurs. remote unchanged -> no cycle.
	w.step()
	if remoteAsks != 1 {
		t.Fatalf("W4: initial step remoteAsks = %d, want 1", remoteAsks)
	}
	if cycles != 0 {
		t.Fatalf("W4: initial step cycles = %d, want 0", cycles)
	}

	// Step 2 at t=5s: interval (15s) has not passed -> remote not asked.
	currentTime = time.Unix(1005, 0)
	w.step()
	if remoteAsks != 1 {
		t.Fatalf("W4: t=5s remoteAsks = %d, want 1", remoteAsks)
	}

	// Step 3 at t=15s: interval passed -> remote asked. Remote changed -> cycle runs!
	currentTime = time.Unix(1015, 0)
	remoteChanged = true
	w.step()
	if remoteAsks != 2 {
		t.Fatalf("W4: t=15s remoteAsks = %d, want 2", remoteAsks)
	}
	if cycles != 1 {
		t.Fatalf("W4: t=15s cycles = %d, want 1", cycles)
	}

	// Step 4 at t=20s: after cycle, next remote ask is one interval later (1015 + 15 = 1030).
	// At t=20s (1020), remote must NOT be asked.
	currentTime = time.Unix(1020, 0)
	remoteChanged = false
	w.step()
	if remoteAsks != 2 {
		t.Fatalf("W4: t=20s remoteAsks = %d, want 2", remoteAsks)
	}

	// Step 5 at t=30s (1030): remote is asked again.
	currentTime = time.Unix(1030, 0)
	w.step()
	if remoteAsks != 3 {
		t.Fatalf("W4: t=30s remoteAsks = %d, want 3", remoteAsks)
	}
}

// W5 a cycle that returns an error: logf("sync: %v", err) once; an identical error text on later attempts is not logged again; no new cycle before the backoff has passed (first 2*interval, doubling, cap 5 min); the first success after an error logs sync: recovered and clears the backoff. A local change during the backoff does not shorten it.
func TestSyncWatch_W5_ErrorBackoff(t *testing.T) {
	currentTime := time.Unix(1000, 0)
	var logs []string
	var cycles int
	cycleErr := errors.New("connection refused")
	isDirty := true

	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return currentTime },
		dirty: func() (vaultsync.DirtyState, error) {
			if !isDirty {
				return vaultsync.DirtyState{}, nil
			}
			return vaultsync.DirtyState{
				Paths:  []string{"note.md"},
				Newest: currentTime.Add(-4 * time.Second), // quiet satisfied
			}, nil
		},
		remote: func() (bool, error) { return false, nil },
		cycle: func() error {
			cycles++
			return cycleErr
		},
		logf: func(format string, a ...any) {
			logs = append(logs, fmt.Sprintf(format, a...))
		},
	}

	// Trigger first cycle which fails
	w.step()
	if cycles != 1 {
		t.Fatalf("W5: cycles = %d, want 1", cycles)
	}
	if len(logs) != 1 || logs[0] != "sync: connection refused" {
		t.Fatalf("W5: logs = %v, want ['sync: connection refused']", logs)
	}

	// Initial backoff is quiet = 3s (A-083-1).
	// Step at t=1002s (2s < quiet): local change occurs during backoff. Must NOT trigger cycle.
	currentTime = time.Unix(1002, 0)
	w.step()
	if cycles != 1 {
		t.Fatalf("W5: cycle ran during backoff (cycles = %d, want 1)", cycles)
	}

	// Step at t=1003s: backoff passed (1000 + 3s = 1003s). Cycle runs and fails again with same error.
	currentTime = time.Unix(1003, 0)
	w.step()
	if cycles != 2 {
		t.Fatalf("W5: cycle did not run after quiet backoff (cycles = %d, want 2)", cycles)
	}
	// Identical error text must NOT be logged again!
	if len(logs) != 1 {
		t.Fatalf("W5: error logged again: logs = %v", logs)
	}

	// Next backoff is 2*interval = 30s -> until 1003 + 30 = 1033s.
	currentTime = time.Unix(1020, 0)
	w.step()
	if cycles != 2 {
		t.Fatalf("W5: cycle ran before 2*interval backoff expired")
	}

	currentTime = time.Unix(1033, 0)
	w.step()
	if cycles != 3 {
		t.Fatalf("W5: cycle did not run after 2*interval backoff (cycles = %d, want 3)", cycles)
	}
	if len(logs) != 1 {
		t.Fatalf("W5: error logged again: logs = %v", logs)
	}

	// Next backoff doubled: 30s * 2 = 60s -> until 1033 + 60 = 1093s.
	currentTime = time.Unix(1092, 0)
	w.step()
	if cycles != 3 {
		t.Fatalf("W5: cycle ran before doubled backoff expired")
	}

	// Step at t=1093s: backoff passed. Cycle succeeds!
	currentTime = time.Unix(1093, 0)
	cycleErr = nil
	w.step()
	if cycles != 4 {
		t.Fatalf("W5: cycle did not run at t=1093s (cycles = %d, want 4)", cycles)
	}
	// Must log "sync: recovered"
	if len(logs) != 2 || logs[1] != "sync: recovered" {
		t.Fatalf("W5: logs = %v, want ['sync: connection refused', 'sync: recovered']", logs)
	}
}

// W6 deletion guard: Deleted > 20 && Deleted*4 > Tracked holds every cycle (local and remote triggered) and logs once "%d tracked files are deleted - not syncing that unattended; run lw sync to confirm, or restore them". When the condition clears, cycles resume.
func TestSyncWatch_W6_DeletionGuard(t *testing.T) {
	currentTime := time.Unix(1000, 0)
	var logs []string
	var cycles int
	deletedCount := 25
	trackedCount := 80 // 25 > 20 and 25*4 = 100 > 80

	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return currentTime },
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.DirtyState{
				Paths:   []string{"deleted.md"},
				Deleted: deletedCount,
				Tracked: trackedCount,
			}, nil
		},
		remote: func() (bool, error) { return true, nil },
		cycle: func() error {
			cycles++
			return nil
		},
		logf: func(format string, a ...any) {
			logs = append(logs, fmt.Sprintf(format, a...))
		},
	}

	// Step 1: deletion guard triggered
	w.step()
	if cycles != 0 {
		t.Fatalf("W6: cycle ran despite deletion guard (cycles = %d)", cycles)
	}
	wantLog := "25 tracked files are deleted - not syncing that unattended; run lw sync to confirm, or restore them"
	if len(logs) != 1 || logs[0] != wantLog {
		t.Fatalf("W6: logs = %v, want [%q]", logs, wantLog)
	}

	// Step 2: deletion guard still active; must log once only
	currentTime = time.Unix(1005, 0)
	w.step()
	if len(logs) != 1 {
		t.Fatalf("W6: message logged more than once: %v", logs)
	}
	if cycles != 0 {
		t.Fatalf("W6: cycle ran on step 2")
	}

	// Step 3: condition clears (e.g. files restored, deletedCount = 5)
	deletedCount = 5
	currentTime = time.Unix(1010, 0)
	w.step()
	if cycles != 1 {
		t.Fatalf("W6: cycles = %d, want 1 once condition cleared", cycles)
	}
}

// W7 dirty or remote returning an error is logged by the same once-per-text rule and the step ends.
func TestSyncWatch_W7_ProbeError(t *testing.T) {
	currentTime := time.Unix(1000, 0)
	var logs []string
	var cycles int
	dirtyErr := errors.New("dirty probe failed")

	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return currentTime },
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.DirtyState{}, dirtyErr
		},
		remote: func() (bool, error) { return false, nil },
		cycle: func() error {
			cycles++
			return nil
		},
		logf: func(format string, a ...any) {
			logs = append(logs, fmt.Sprintf(format, a...))
		},
	}

	// Step 1: dirty error logged once, step ends (no cycle)
	w.step()
	if cycles != 0 {
		t.Fatalf("W7: cycle ran on dirty error")
	}
	if len(logs) != 1 || logs[0] != "sync: dirty probe failed" {
		t.Fatalf("W7: logs = %v, want ['sync: dirty probe failed']", logs)
	}

	// Step 2: same error not logged again
	currentTime = time.Unix(1002, 0)
	w.step()
	if len(logs) != 1 {
		t.Fatalf("W7: dirty error logged again: %v", logs)
	}

	// Step 3: dirty probe clears, remote probe returns error
	dirtyErr = nil
	remoteErr := errors.New("remote unreachable")
	w.remote = func() (bool, error) {
		return false, remoteErr
	}
	currentTime = time.Unix(1015, 0)
	w.step()
	if len(logs) != 2 || logs[1] != "sync: remote unreachable" {
		t.Fatalf("W7: logs = %v, want remote error logged", logs)
	}

	// Step 4: same remote error not logged again
	currentTime = time.Unix(1030, 0)
	w.step()
	if len(logs) != 2 {
		t.Fatalf("W7: remote error logged again: %v", logs)
	}
}

// W8 a tree that is dirty only because a cycle just failed to push (nothing uncommitted, commits ahead) is not this function's business: cycle decides. step calls cycle when dirty or remote say so, nothing else.
func TestSyncWatch_W8_AheadDoesNotTrigger(t *testing.T) {
	var cycles int
	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return time.Unix(1000, 0) },
		dirty: func() (vaultsync.DirtyState, error) {
			// Tree is clean (nothing uncommitted)
			return vaultsync.DirtyState{}, nil
		},
		remote: func() (bool, error) {
			// Remote has not changed
			return false, nil
		},
		cycle: func() error {
			cycles++
			return nil
		},
		logf: func(format string, a ...any) {},
	}

	w.step()
	if cycles != 0 {
		t.Fatalf("W8: cycle ran when work tree was clean and remote unchanged: %d", cycles)
	}
}

// W9 One watcher per vault: an exclusive, non-blocking flock on <vault>/.llmwiki/tmp/watch.lock, pid written into it. A second one prints lw sync watch: already running for this vault (pid N) on stderr and exits 1.
func TestSyncWatch_W9_LockSingleWatcher(t *testing.T) {
	syncHermetic(t)
	pc := newSyncPC(t, "w9")
	pc.useFixture()
	remote := filepath.Join(syncTemp(t), "w9-remote")
	if _, _, code := pc.lw("sync", "init", remote, "--vault", pc.root); code != 0 {
		t.Fatal("init failed")
	}

	// Simulate first watcher taking the lock on <vault>/.llmwiki/tmp/watch.lock
	lockDir := filepath.Join(pc.root, ".llmwiki", "tmp")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(lockDir, "watch.lock")
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	pid := os.Getpid()
	if _, err := fmt.Fprintf(f, "%d\n", pid); err != nil {
		t.Fatal(err)
	}

	// Run second watcher: should fail with exit code 1 and stderr message
	_, stderr, code := pc.lw("sync", "watch", "--vault", pc.root)
	if code != 1 {
		t.Fatalf("W9: exit code = %d, want 1; stderr:\n%s", code, stderr)
	}
	wantMsg := fmt.Sprintf("lw sync watch: already running for this vault (pid %d)", pid)
	if !strings.Contains(stderr, wantMsg) {
		t.Fatalf("W9: stderr = %q, want it to contain %q", stderr, wantMsg)
	}
}

// W10 Runs one cycle at start, then step every 2 s until SIGINT or SIGTERM; a signal lets a running cycle finish, then exits 0.
func TestSyncWatch_W10_SignalFinishesCycle(t *testing.T) {
	// We test that when a signal is received, a running cycle finishes and loop exits 0.
	var cycleFinished bool
	cycleStarted := make(chan struct{})

	cycle := func() error {
		close(cycleStarted)
		time.Sleep(50 * time.Millisecond)
		cycleFinished = true
		return nil
	}

	sigCh := make(chan os.Signal, 1)

	// Simulate loop runner
	done := make(chan error)
	go func() {
		// Run initial cycle
		err := cycle()
		select {
		case <-sigCh:
			done <- err
			return
		default:
		}
		done <- err
	}()

	<-cycleStarted
	sigCh <- syscall.SIGINT
	err := <-done
	if err != nil {
		t.Fatalf("W10: unexpected error: %v", err)
	}
	if !cycleFinished {
		t.Fatalf("W10: signal did not let cycle finish")
	}
}

// W11 integration, real git, the existing syncPair helper: A writes notes/w11.md; driving A's loop (real functions, a fake clock or short real durations) pushes it; driving B's loop brings the file into B's tree, byte-identical. No sleep longer than 2 s in the test.
func TestSyncWatch_W11_Integration(t *testing.T) {
	a, b, _ := syncPair(t)

	// A writes notes/w11.md
	content := "w11 integration test content\n"
	a.write("notes/w11.md", content)

	// Build A's loop with real Dirty and RemoteHead
	aRoot, aOpts, err := syncSetup(a.root, syncModeWrite)
	if err != nil {
		t.Fatal(err)
	}
	aOpts.Interactive = false

	var aOut bytes.Buffer
	wA := &watchLoop{
		quiet:    1 * time.Second,
		maxWait:  5 * time.Second,
		interval: 5 * time.Second,
		now:      time.Now,
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.Dirty(context.Background(), aOpts)
		},
		remote: func() (bool, error) {
			sha, local, _, err := vaultsync.RemoteHead(context.Background(), aOpts)
			if err != nil {
				return false, err
			}
			return sha != "" && sha != local, nil
		},
		cycle: func() error {
			return runSyncCycle(context.Background(), aRoot, aOpts, &aOut, io.Discard, false, true, nil, nil)
		},
		logf: func(format string, args ...any) {},
	}

	// Trigger A's loop: file was just written, wait 1.1s for quiet to pass
	time.Sleep(1100 * time.Millisecond)
	wA.step()

	// Now drive B's loop
	bRoot, bOpts, err := syncSetup(b.root, syncModeWrite)
	if err != nil {
		t.Fatal(err)
	}
	bOpts.Interactive = false

	var bOut bytes.Buffer
	wB := &watchLoop{
		quiet:    1 * time.Second,
		maxWait:  5 * time.Second,
		interval: 5 * time.Second,
		now:      time.Now,
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.Dirty(context.Background(), bOpts)
		},
		remote: func() (bool, error) {
			sha, local, _, err := vaultsync.RemoteHead(context.Background(), bOpts)
			if err != nil {
				return false, err
			}
			return sha != "" && sha != local, nil
		},
		cycle: func() error {
			return runSyncCycle(context.Background(), bRoot, bOpts, &bOut, io.Discard, false, true, nil, nil)
		},
		logf: func(format string, args ...any) {},
	}

	wB.step()

	// Check B has notes/w11.md byte-identical
	got := b.read("notes/w11.md")
	if got != content {
		t.Fatalf("W11: B read = %q, want %q", got, content)
	}
}

// W12 lw sync status: One more line, last, only while a watcher holds the lock: watch    running (pid N). Without a watcher the output is byte-identical to v2.66.0.
func TestSyncWatch_W12_SyncStatusWatcherLine(t *testing.T) {
	pc := newSyncPC(t, "w12")
	pc.useFixture()
	remote := filepath.Join(syncTemp(t), "w12-remote")
	if _, _, code := pc.lw("sync", "init", remote, "--vault", pc.root); code != 0 {
		t.Fatal("init failed")
	}

	// Without watcher
	stdoutNoWatch, _, code := pc.lw("sync", "status", "--vault", pc.root)
	if code != 0 {
		t.Fatalf("status failed: code %d", code)
	}
	if strings.Contains(stdoutNoWatch, "watch") {
		t.Fatalf("W12: status without watcher contains 'watch':\n%s", stdoutNoWatch)
	}

	// Hold the watch lock
	lockDir := filepath.Join(pc.root, ".llmwiki", "tmp")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(lockDir, "watch.lock")
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	pid := os.Getpid()
	if _, err := fmt.Fprintf(f, "%d\n", pid); err != nil {
		t.Fatal(err)
	}

	// With watcher holding the lock
	stdoutWithWatch, _, code := pc.lw("sync", "status", "--vault", pc.root)
	if code != 0 {
		t.Fatalf("status with watcher failed: code %d", code)
	}

	expectedLine := fmt.Sprintf("watch    running (pid %d)", pid)
	lines := strings.Split(strings.TrimSpace(stdoutWithWatch), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != expectedLine {
		t.Fatalf("W12: last line = %q, want %q; full output:\n%s", lines[len(lines)-1], expectedLine, stdoutWithWatch)
	}

	// Check preceding lines are identical
	withoutLines := strings.Split(strings.TrimSpace(stdoutNoWatch), "\n")
	withPreceding := lines[:len(lines)-1]
	if strings.Join(withoutLines, "\n") != strings.Join(withPreceding, "\n") {
		t.Fatalf("W12: preceding lines mismatch:\nwithout:\n%s\nwith:\n%s", stdoutNoWatch, strings.Join(withPreceding, "\n"))
	}
}

// W13 lw sync install --print [--vault P]: Without --print: lw sync install: only --print is supported, exit 2. With it, on Linux: systemd unit; on darwin: launchd plist.
func TestSyncWatch_W13_InstallPrint(t *testing.T) {
	pc := newSyncPC(t, "w13")
	pc.useFixture()

	// Without --print: exit 2
	_, stderr, code := pc.lw("sync", "install", "--vault", pc.root)
	if code != 2 {
		t.Fatalf("W13: code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "lw sync install: only --print is supported") {
		t.Fatalf("W13: stderr = %q", stderr)
	}

	// With --print on Linux
	oldGOOS := syncInstallGOOS
	defer func() { syncInstallGOOS = oldGOOS }()
	syncInstallGOOS = "linux"

	stdout, _, code := pc.lw("sync", "install", "--print", "--vault", pc.root)
	if code != 0 {
		t.Fatalf("W13 linux: code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "# ~/.config/systemd/user/lw-sync.service") {
		t.Errorf("W13 linux: missing unit path header")
	}
	if !strings.Contains(stdout, "Description=llmwiki sync watch") {
		t.Errorf("W13 linux: missing description")
	}
	if !strings.Contains(stdout, "ExecStart=") || !strings.Contains(stdout, "sync watch --vault") {
		t.Errorf("W13 linux: missing ExecStart with sync watch --vault")
	}
	pathEnv := os.Getenv("PATH")
	wantLinuxEnv := fmt.Sprintf("Environment=\"PATH=%s\"", pathEnv)
	if !strings.Contains(stdout, wantLinuxEnv) {
		t.Errorf("W13 linux: missing %s", wantLinuxEnv)
	}
	if !strings.Contains(stdout, "systemctl --user enable --now lw-sync.service") {
		t.Errorf("W13 linux: missing systemctl command")
	}

	// With --print on Darwin
	syncInstallGOOS = "darwin"
	stdoutDarwin, _, code := pc.lw("sync", "install", "--print", "--vault", pc.root)
	if code != 0 {
		t.Fatalf("W13 darwin: code = %d, want 0", code)
	}
	if !strings.Contains(stdoutDarwin, "~/Library/LaunchAgents/pro.awepo.lw-sync.plist") {
		t.Errorf("W13 darwin: missing plist path")
	}
	if !strings.Contains(stdoutDarwin, "pro.awepo.lw-sync") {
		t.Errorf("W13 darwin: missing label")
	}
	if !strings.Contains(stdoutDarwin, "<key>RunAtLoad</key>") || !strings.Contains(stdoutDarwin, "<key>KeepAlive</key>") {
		t.Errorf("W13 darwin: missing RunAtLoad or KeepAlive")
	}
	wantDarwinEnv := fmt.Sprintf("<key>EnvironmentVariables</key>\n\t<dict>\n\t\t<key>PATH</key>\n\t\t<string>%s</string>\n\t</dict>", pathEnv)
	if !strings.Contains(stdoutDarwin, wantDarwinEnv) {
		t.Errorf("W13 darwin: missing %s", wantDarwinEnv)
	}
	if !strings.Contains(stdoutDarwin, "launchctl load") {
		t.Errorf("W13 darwin: missing launchctl load command")
	}
}

// W14 syncUsage gains the two lines and one paragraph (unattended rules: never take-remote, the deletion guard); lw --help gains a sync watch line; completion offers watch and install after sync.
func TestSyncWatch_W14_UsageHelpCompletion(t *testing.T) {
	pc := newSyncPC(t, "w14")

	// lw sync --help
	stdout, _, code := pc.lw("sync", "--help")
	if code != 0 {
		t.Fatalf("W14: sync --help code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "lw sync watch") {
		t.Errorf("W14: syncUsage missing 'lw sync watch'")
	}
	if !strings.Contains(stdout, "lw sync install") {
		t.Errorf("W14: syncUsage missing 'lw sync install'")
	}
	if !strings.Contains(stdout, "unattended") || !strings.Contains(stdout, "take-remote") {
		t.Errorf("W14: syncUsage missing unattended / take-remote paragraph")
	}

	// lw --help
	stdoutHelp, _, codeHelp := pc.lw("--help")
	if codeHelp != 0 {
		t.Fatalf("W14: --help code = %d, want 0", codeHelp)
	}
	if !strings.Contains(stdoutHelp, "sync watch") {
		t.Errorf("W14: lw --help missing 'sync watch'")
	}

	// Completion offers watch and install after sync
	subcmds, ok := completionSubcommands["sync"]
	if !ok {
		t.Fatalf("W14: completionSubcommands missing 'sync'")
	}
	var hasWatch, hasInstall bool
	for _, sc := range subcmds {
		if sc == "watch" {
			hasWatch = true
		}
		if sc == "install" {
			hasInstall = true
		}
	}
	if !hasWatch || !hasInstall {
		t.Fatalf("W14: completionSubcommands['sync'] = %v, want 'watch' and 'install'", subcmds)
	}
}

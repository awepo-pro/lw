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
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/vaultsync"
)

// FW1: a cycle that fails because the vault is busy (errBusyTryAgain / errSyncBusy)
// is not an error: no log line, no backoff, the next tick tries again.
func TestSyncFixW_FW1_BusyCycleNotError(t *testing.T) {
	currentTime := time.Unix(1000, 0)
	var logs []string
	var cycles int
	cycleErr := errBusyTryAgain

	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return currentTime },
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.DirtyState{
				Paths:  []string{"notes/active.md"},
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

	// Step 1: trigger cycle, fails with errBusyTryAgain
	w.step()
	if cycles != 1 {
		t.Fatalf("FW1: cycles = %d, want 1", cycles)
	}
	if len(logs) != 0 {
		t.Fatalf("FW1: logs = %v, want empty (busy should not log)", logs)
	}
	if w.backoffDuration != 0 {
		t.Fatalf("FW1: backoffDuration = %v, want 0", w.backoffDuration)
	}
	if !w.backoffUntil.IsZero() {
		t.Fatalf("FW1: backoffUntil = %v, want zero", w.backoffUntil)
	}

	// Step 2: next tick (2s later, less than quiet or interval) tries again
	currentTime = time.Unix(1002, 0)
	cycleErr = nil
	w.step()
	if cycles != 2 {
		t.Fatalf("FW1: step 2 did not try again (cycles = %d, want 2)", cycles)
	}
	if len(logs) != 0 {
		t.Fatalf("FW1: step 2 logged unexpected message: %v", logs)
	}
}

// FW2: a failed cycle sets retry; when the backoff has passed, step runs the cycle again
// whatever dirty and remote say; a success clears it. Backoff sequence: quiet (3 s)
// for the first failure, then 2*interval, doubling, cap 5 min.
func TestSyncFixW_FW2_RetryOnFailure(t *testing.T) {
	currentTime := time.Unix(1000, 0)
	var logs []string
	var cycles int
	isDirty := true
	cycleErr := errors.New("network unreachable")

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
				Paths:  []string{"notes/active.md"},
				Newest: currentTime.Add(-4 * time.Second),
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

	// Step 1 at t=1000: dirty tree triggers cycle 1, which fails.
	w.step()
	if cycles != 1 {
		t.Fatalf("FW2: cycles = %d, want 1", cycles)
	}
	if len(logs) != 1 || logs[0] != "sync: network unreachable" {
		t.Fatalf("FW2: logs = %v, want ['sync: network unreachable']", logs)
	}

	// Tree becomes clean, remote unchanged.
	isDirty = false

	// Step 2 at t=1002 (2s < quiet 3s): backoff not passed, no cycle.
	currentTime = time.Unix(1002, 0)
	w.step()
	if cycles != 1 {
		t.Fatalf("FW2: cycle ran before first backoff (cycles = %d, want 1)", cycles)
	}

	// Step 3 at t=1003 (3s == quiet): backoff passed, must retry even though clean!
	currentTime = time.Unix(1003, 0)
	w.step()
	if cycles != 2 {
		t.Fatalf("FW2: retry did not run on clean tree after quiet backoff (cycles = %d, want 2)", cycles)
	}
	// Same error must not be logged again
	if len(logs) != 1 {
		t.Fatalf("FW2: error logged again: %v", logs)
	}

	// Next backoff is 2*interval = 30s (until 1003 + 30 = 1033s).
	currentTime = time.Unix(1020, 0)
	w.step()
	if cycles != 2 {
		t.Fatalf("FW2: cycle ran during second backoff (cycles = %d, want 2)", cycles)
	}

	// Step at t=1033s: backoff passed, cycle runs and succeeds!
	currentTime = time.Unix(1033, 0)
	cycleErr = nil
	w.step()
	if cycles != 3 {
		t.Fatalf("FW2: cycle did not run at t=1033s (cycles = %d, want 3)", cycles)
	}
	if len(logs) != 2 || logs[1] != "sync: recovered" {
		t.Fatalf("FW2: logs = %v, want ['sync: network unreachable', 'sync: recovered']", logs)
	}

	// Step at t=1035s: clean tree, no retry pending -> no cycle.
	currentTime = time.Unix(1035, 0)
	w.step()
	if cycles != 3 {
		t.Fatalf("FW2: cycle ran when clean and not retrying (cycles = %d, want 3)", cycles)
	}
}

// FW3: the cycle run at start goes through the same error handling as every other
// (logged once, sets retry); today its error is dropped.
func TestSyncFixW_FW3_StartCycleErrorHandling(t *testing.T) {
	currentTime := time.Unix(1000, 0)
	var logs []string
	var cycles int
	cycleErr := errors.New("auth failure at startup")

	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return currentTime },
		dirty: func() (vaultsync.DirtyState, error) {
			// Tree is clean
			return vaultsync.DirtyState{}, nil
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

	// Run initial cycle through w's start cycle method
	w.initStartCycle()
	if cycles != 1 {
		t.Fatalf("FW3: start cycle did not run (cycles = %d, want 1)", cycles)
	}
	if len(logs) != 1 || logs[0] != "sync: auth failure at startup" {
		t.Fatalf("FW3: start cycle error not logged: logs = %v", logs)
	}

	// Tree is clean. Step at t=1001 (1s < quiet): no cycle.
	currentTime = time.Unix(1001, 0)
	w.step()
	if cycles != 1 {
		t.Fatalf("FW3: step ran prematurely during start cycle backoff")
	}

	// Step at t=1003 (3s == quiet): backoff passed, retry runs even though clean!
	currentTime = time.Unix(1003, 0)
	cycleErr = nil
	w.step()
	if cycles != 2 {
		t.Fatalf("FW3: step did not retry start cycle error on clean tree (cycles = %d, want 2)", cycles)
	}
	if len(logs) != 2 || logs[1] != "sync: recovered" {
		t.Fatalf("FW3: logs = %v, want recovered message", logs)
	}
}

// FW4: in watch mode the N conflict copy/copies to resolve block is printed only when
// the set of copies differs from the one last printed; rebuilt the search index goes
// through the watcher's timestamped writer.
func TestSyncFixW_FW4_ConflictCopiesWatchDeduplication(t *testing.T) {
	syncHermetic(t)
	pc := newSyncPC(t, "fw4")
	pc.useFixture()
	remote := filepath.Join(syncTemp(t), "fw4-remote")
	if _, _, code := pc.lw("sync", "init", remote, "--vault", pc.root); code != 0 {
		t.Fatal("init failed")
	}

	root, o, err := syncSetup(pc.root, syncModeWrite)
	if err != nil {
		t.Fatal(err)
	}

	// Create a conflict copy
	conflict1 := "notes/test.conflict-host-20261110-120000.md"
	pc.write(conflict1, "conflict content 1\n")

	var ct conflictTracker
	var out1 bytes.Buffer
	tw1 := &timestampWriter{dst: &out1}

	// Cycle 1: first time seeing conflict1 -> must print block
	if err := runSyncCycle(context.Background(), root, o, tw1, io.Discard, false, true, nil, &ct); err != nil {
		t.Fatal(err)
	}
	tw1.Flush()
	s1 := out1.String()
	if !strings.Contains(s1, "1 conflict copy to resolve") || !strings.Contains(s1, conflict1) {
		t.Fatalf("FW4 cycle 1: expected conflict copy printed, got:\n%s", s1)
	}

	// Cycle 2: set of conflict copies is unchanged -> must NOT print block
	var out2 bytes.Buffer
	tw2 := &timestampWriter{dst: &out2}
	if err := runSyncCycle(context.Background(), root, o, tw2, io.Discard, false, true, nil, &ct); err != nil {
		t.Fatal(err)
	}
	tw2.Flush()
	s2 := out2.String()
	if strings.Contains(s2, "conflict copy") {
		t.Fatalf("FW4 cycle 2: conflict copy was printed again despite unchanged set:\n%s", s2)
	}

	// Add a second conflict copy
	conflict2 := "notes/test2.conflict-host-20261110-120001.md"
	pc.write(conflict2, "conflict content 2\n")

	// Cycle 3: set differs -> must print block with 2 copies
	var out3 bytes.Buffer
	tw3 := &timestampWriter{dst: &out3}
	if err := runSyncCycle(context.Background(), root, o, tw3, io.Discard, false, true, nil, &ct); err != nil {
		t.Fatal(err)
	}
	tw3.Flush()
	s3 := out3.String()
	if !strings.Contains(s3, "2 conflict copies to resolve") || !strings.Contains(s3, conflict2) {
		t.Fatalf("FW4 cycle 3: expected 2 conflict copies printed, got:\n%s", s3)
	}
}

// FW4 part 2: rebuilt the search index goes through the watcher's timestamped writer.
func TestSyncFixW_FW4_IndexRebuildTimestampWriter(t *testing.T) {
	a, b, _ := syncPair(t)

	// A edits a wiki page and pushes
	a.appendTo("wiki/concepts/kv-cache.md", "\nEdited on A.\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A could not push")
	}

	// B runs cycle with timestampWriter
	bRoot, bOpts, err := syncSetup(b.root, syncModeWrite)
	if err != nil {
		t.Fatal(err)
	}
	bOpts.Interactive = false

	var bOut bytes.Buffer
	tw := &timestampWriter{dst: &bOut}
	var ct conflictTracker

	if err := runSyncCycle(context.Background(), bRoot, bOpts, tw, io.Discard, false, true, nil, &ct); err != nil {
		t.Fatal(err)
	}
	tw.Flush()

	out := bOut.String()
	if !strings.Contains(out, "rebuilt the search index") {
		t.Fatalf("FW4: expected 'rebuilt the search index' in watcher output, got:\n%s", out)
	}
	// Verify it was timestamped by timestampWriter
	lines := strings.Split(strings.TrimSpace(out), "\n")
	found := false
	for _, line := range lines {
		if strings.Contains(line, "rebuilt the search index") {
			found = true
			parts := strings.SplitN(line, " ", 2)
			if len(parts) < 2 {
				t.Fatalf("FW4: index rebuild line not timestamped: %q", line)
			}
			if _, err := time.Parse(time.RFC3339, parts[0]); err != nil {
				t.Fatalf("FW4: index rebuild line timestamp invalid: %q, err: %v", parts[0], err)
			}
		}
	}
	if !found {
		t.Fatalf("FW4: index rebuild line not found in lines: %v", lines)
	}
}

// FW6: with each of the newly ignored files present (and an Emacs-style lock SYMLINK
// notes/.#x.md), lw sync exits 0, commits none of them, and vaultsync.Dirty does not list them.
func TestSyncFixW_FW6_ManagedGitignore(t *testing.T) {
	pc := newSyncPC(t, "fw6")
	pc.useFixture()
	remote := filepath.Join(syncTemp(t), "fw6-remote")
	if _, _, code := pc.lw("sync", "init", remote, "--vault", pc.root); code != 0 {
		t.Fatal("init failed")
	}

	// Create each of the new ignore patterns:
	// 1. /.llmwiki/.storage-json-tmp-*
	pc.write(".llmwiki/.storage-json-tmp-12345", "temp storage json\n")
	// 2. *.lw-tmp
	pc.write("notes/draft.lw-tmp", "temporary note draft\n")
	// 3. /.trash/
	pc.write(".trash/deleted.md", "trashed file\n")
	// 4. .#* (Emacs lock symlink)
	lockSymlink := filepath.Join(pc.root, "notes", ".#x.md")
	if err := os.Symlink("user@localhost.12345", lockSymlink); err != nil {
		t.Fatal(err)
	}
	// 5. #*# (Emacs autosave)
	pc.write("notes/#autosave.md#", "autosave content\n")
	// 6. *~ (editor backup)
	pc.write("notes/backup.md~", "backup content\n")
	// 7. .*.sw? (vim swap file)
	pc.write("notes/.note.md.swp", "vim swap content\n")

	// vaultsync.Dirty must not list any of them
	_, o, err := syncSetup(pc.root, syncModeWrite)
	if err != nil {
		t.Fatal(err)
	}
	st, err := vaultsync.Dirty(context.Background(), o)
	if err != nil {
		t.Fatalf("Dirty failed: %v", err)
	}
	for _, p := range st.Paths {
		if strings.Contains(p, "storage-json-tmp") ||
			strings.HasSuffix(p, ".lw-tmp") ||
			strings.HasPrefix(p, ".trash/") ||
			strings.Contains(p, ".#x.md") ||
			strings.Contains(p, "#autosave.md#") ||
			strings.HasSuffix(p, "~") ||
			strings.HasSuffix(p, ".swp") {
			t.Fatalf("FW6: Dirty listed ignored path: %s (all paths: %v)", p, st.Paths)
		}
	}

	// lw sync exits 0 and commits none of them
	_, stderr, code := pc.lw("sync", "--vault", pc.root)
	if code != 0 {
		t.Fatalf("lw sync failed: code %d, stderr:\n%s", code, stderr)
	}

	// Verify git does not track any of them
	tracked := pc.git("ls-files")
	for _, p := range strings.Split(tracked, "\n") {
		p = strings.TrimSpace(p)
		if strings.Contains(p, "storage-json-tmp") ||
			strings.HasSuffix(p, ".lw-tmp") ||
			strings.HasPrefix(p, ".trash/") ||
			strings.Contains(p, ".#x.md") ||
			strings.Contains(p, "#autosave.md#") ||
			strings.HasSuffix(p, "~") ||
			strings.HasSuffix(p, ".swp") {
			t.Fatalf("FW6: git tracked ignored file %s", p)
		}
	}
}

// FW7: a stale git lock no longer wedges sync for ever. In internal/vaultsync, when
// a git call fails with the existing *lockError and the lock file it names is inside
// this vault's .git and its mtime is more than 10 minutes old, remove that file and run
// the call once more; younger: the error as today. One log line (slog) when a lock is removed.
// Test FW7: a 0-byte .git/index.lock with an mtime 11 minutes old -> lw sync succeeds;
// with a fresh mtime -> the lock error as today, the file untouched.
func TestSyncFixW_FW7_StaleLockRecovery(t *testing.T) {
	pc := newSyncPC(t, "fw7")
	pc.useFixture()
	remote := filepath.Join(syncTemp(t), "fw7-remote")
	if _, _, code := pc.lw("sync", "init", remote, "--vault", pc.root); code != 0 {
		t.Fatal("init failed")
	}

	lockFile := filepath.Join(pc.root, ".git", "index.lock")

	// Case 1: fresh lock file (mtime = now) -> fails with lock error, file untouched
	if err := os.WriteFile(lockFile, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := pc.lw("sync", "--vault", pc.root)
	if code == 0 {
		t.Fatalf("FW7 fresh lock: expected sync failure, got code 0")
	}
	if !strings.Contains(stderr, "a previous sync was interrupted and left") || !strings.Contains(stderr, "index.lock") {
		t.Fatalf("FW7 fresh lock: expected lock error on stderr, got:\n%s", stderr)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("FW7 fresh lock: lock file was removed: %v", err)
	}

	// Case 2: stale lock file (mtime = 11 minutes old) -> lw sync removes lock and succeeds!
	elevenMinsAgo := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(lockFile, elevenMinsAgo, elevenMinsAgo); err != nil {
		t.Fatal(err)
	}

	_, stderr, code = pc.lw("sync", "--vault", pc.root)
	if code != 0 {
		t.Fatalf("FW7 stale lock: expected sync success, got code %d, stderr:\n%s", code, stderr)
	}
	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Fatalf("FW7 stale lock: lock file was not removed")
	}
}

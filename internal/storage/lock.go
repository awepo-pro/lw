package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// The lock (066 FIX1 X-5, F-2). Every writer of the catalog or the ledger reads
// a file, changes it in memory and replaces it, and every lw process opens its
// own Storage handle - so a mutex inside the handle excluded nobody. Two
// processes (a TUI's background push and the verb in the foreground, or two
// terminals) could each read the old ledger and the second write would erase the
// first's row: a lost "lw put these bytes here" is a file that is never deleted
// when it should be, and a lost "confirmed" is a needless network call; a lost
// catalog entry is a stored file that vanishes from the catalog.
//
// The lock is an exclusive flock on .llmwiki/tmp/storage.lock, a file of its own
// (cmd_tool.go's lockTool is the precedent: a lock on a file that other code
// replaces excludes nobody). flock conflicts between separately opened
// descriptors even inside one process, so concurrent calls in a test are as
// exclusive as concurrent processes, and the kernel frees the lock of a holder
// that dies.
//
// What it is held across matters as much as that it exists. A decision to
// delete or replace bytes is made from the catalog, the ledger and the file, and
// all three are re-read under the lock immediately before the destroy; the
// network (the upload rule P asks for, the download) is NOT under it, so a slow
// transfer blocks no other lw verb. Reconcile and Materialize are therefore
// shaped as plan, then work with no lock, then settle under the lock again.

const lockName = "storage.lock"

// lockWait and lockPoll bound how long a writer waits for another one; vars so
// a test could shorten them.
var (
	lockWait = 30 * time.Second
	lockPoll = 20 * time.Millisecond
)

func (s *Storage) lockPath() string {
	return filepath.Join(s.vaultDir, ".llmwiki", "tmp", lockName)
}

// lock takes the exclusive lock, waiting up to lockWait, and returns the
// function that lets it go. Callers do not nest: flock is not re-entrant, a
// second lock() from the holder would wait for itself.
func (s *Storage) lock() (unlock func(), err error) {
	path := s.lockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("storage: create lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("storage: open lock %s: %w", path, err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			var once sync.Once
			return func() {
				once.Do(func() {
					syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
					f.Close()
				})
			}, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			if time.Now().After(deadline) {
				f.Close()
				// Never advise deleting the file: a new file is a new inode, which the
				// running writer's flock does not cover.
				return nil, fmt.Errorf("storage: another lw is changing storage/ (it holds %s; waited %s): wait for it or stop that process", path, lockWait)
			}
			time.Sleep(lockPoll)
		default:
			f.Close()
			return nil, fmt.Errorf("storage: lock %s: %w", path, err)
		}
	}
}

// rebaseInProgress reports whether the vault is in the middle of a rebase - git's
// own (rebase-merge, rebase-apply) or lw's record of the one it started
// (vaultsync's .git/lw-rebase). While one stands the work tree holds a half-
// replayed history: an entry the rebase is about to bring back is briefly absent
// from the catalog, and Reconcile would read that as "removed on another PC" and
// delete a file the user still has an entry for (F-2).
func (s *Storage) rebaseInProgress() bool {
	for _, d := range []string{"rebase-merge", "rebase-apply", "lw-rebase"} {
		if _, err := os.Lstat(filepath.Join(s.vaultDir, ".git", d)); err == nil {
			return true
		}
	}
	return false
}

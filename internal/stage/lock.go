// lock.go implements the vault lock (backbone §5.2, MASTER §9 D-I). Owned
// by S2-T1.
package stage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrLocked is returned by AcquireLock when another live process already
// holds the vault lock.
var ErrLocked = errors.New("stage: vault is locked by another process")

// lockFileName is the lock file's name inside dir (.llmwiki).
const lockFileName = "lock"

// AcquireLock acquires the vault lock at dir/lock, returning a release
// function that removes it; release is safe to call twice.
//
// Contract (backbone §5.2, MASTER §9 D-AS): the lock file is created
// O_CREATE|O_EXCL|O_WRONLY containing "<pid> <RFC3339 start>\n". If it
// already exists, the recorded pid is tested for liveness with
// syscall.Kill(pid, 0): the pid is alive when Kill returns nil or EPERM
// (EPERM means the process exists and is owned by another user, which is
// still a live holder); ESRCH, or an unparsable or empty lock file, means
// stale. A live holder makes AcquireLock return ErrLocked. A stale lock is
// broken (removed) and retaken with exactly one O_EXCL retry: if that retry
// also finds the file present, another process won the same race, and
// AcquireLock returns ErrLocked rather than looping.
//
// AcquireLock is called only by Commit (backbone §5.4 step 1) — nothing
// else in this package takes the lock.
func AcquireLock(dir string) (release func() error, err error) {
	path := filepath.Join(dir, lockFileName)

	if err := tryAcquire(path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("stage: acquire lock: %w", err)
		}

		alive, err := lockHolderAlive(path)
		if err != nil {
			return nil, err
		}
		if alive {
			return nil, ErrLocked
		}

		// Stale: break it and retake with exactly one O_EXCL retry.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stage: break stale lock: %w", err)
		}
		if err := tryAcquire(path); err != nil {
			if errors.Is(err, os.ErrExist) {
				return nil, ErrLocked
			}
			return nil, fmt.Errorf("stage: acquire lock: %w", err)
		}
	}

	var released bool
	return func() error {
		if released {
			return nil
		}
		released = true
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stage: release lock: %w", err)
		}
		return nil
	}, nil
}

// tryAcquire attempts the O_CREATE|O_EXCL create-and-write of the lock file
// at path. It returns an error satisfying errors.Is(err, os.ErrExist) when
// the file already exists.
func tryAcquire(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	return err
}

// lockHolderAlive reports whether the process recorded in the lock file at
// path is still alive. An unparsable or empty file, or a file that has
// vanished since the caller found it present, is reported as not alive
// (stale).
func lockHolderAlive(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stage: read lock: %w", err)
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return false, nil
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return false, nil
	}

	killErr := syscall.Kill(pid, 0)
	if killErr == nil || errors.Is(killErr, syscall.EPERM) {
		return true, nil
	}
	return false, nil
}

// BreakLock removes dir's lock file unconditionally, regardless of whether
// its holder is alive. It is what lw doctor --unlock (S6-T1) calls.
func BreakLock(dir string) error {
	path := filepath.Join(dir, lockFileName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stage: break lock: %w", err)
	}
	return nil
}

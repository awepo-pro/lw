package stage

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLockContention proves a second AcquireLock in the same process, while
// the first is still held, returns ErrLocked — and that release is
// idempotent and a fresh AcquireLock succeeds afterwards.
func TestLockContention(t *testing.T) {
	dir := t.TempDir()

	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}

	if _, err := AcquireLock(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireLock = %v, want ErrLocked", err)
	}

	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("second release (must be a no-op): %v", err)
	}

	release2, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock after release: %v", err)
	}
	if err := release2(); err != nil {
		t.Fatalf("release2: %v", err)
	}
}

// TestStaleLock writes a lock file recording a pid that cannot exist and
// checks AcquireLock breaks and retakes it.
func TestStaleLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")

	const deadPID = 999999999 // no such process can exist at this pid
	content := strconv.Itoa(deadPID) + " " + time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(lockPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock over stale lock: %v", err)
	}
	defer release()

	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock after break+retake: %v", err)
	}
	if !strings.HasPrefix(string(data), strconv.Itoa(os.Getpid())+" ") {
		t.Fatalf("lock file after break+retake = %q, want it to start with our pid %d", data, os.Getpid())
	}
}

// TestStaleLockUnparsable proves an empty/unparsable lock file is treated
// as stale, not as a live holder.
func TestStaleLockUnparsable(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")
	if err := os.WriteFile(lockPath, []byte(""), 0o644); err != nil {
		t.Fatalf("write empty lock: %v", err)
	}

	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock over empty lock file: %v", err)
	}
	release()
}

// TestLockBreaksRealDeadProcess is probe (a): a real kill -9'd child
// process holding the lock must be broken by the next AcquireLock. A test
// that only synthesizes a lock file with an arbitrary pid does not prove
// the liveness path actually calls into syscall.Kill correctly.
func TestLockBreaksRealDeadProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start sleep(1): %v", err)
	}
	pid := cmd.Process.Pid

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")
	content := strconv.Itoa(pid) + " " + time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(lockPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write lock with real pid: %v", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait() // reap so the pid cannot be recycled onto a lingering zombie

	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock over a real dead pid: %v", err)
	}
	defer release()
}

// TestLockLiveProcessNotBroken proves a lock recording our own (very much
// alive) pid is treated as a live holder, not broken.
func TestLockLiveProcessNotBroken(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")
	content := strconv.Itoa(os.Getpid()) + " " + time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(lockPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write live lock: %v", err)
	}

	if _, err := AcquireLock(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("AcquireLock over our own live pid = %v, want ErrLocked", err)
	}
}

// TestLockHolderOwnedByAnotherUserIsAlive covers the EPERM branch of the
// liveness check, which no other test reaches: TestLockLiveProcessNotBroken
// uses a process we own, so syscall.Kill returns nil there. pid 1 exists and
// belongs to root, so Kill(1, 0) returns EPERM for an unprivileged process —
// and MASTER §9 D-AS calls treating EPERM as "dead" a correctness trap,
// because it would break a lock held by a live process owned by another user.
func TestLockHolderOwnedByAnotherUserIsAlive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: Kill(1, 0) returns nil rather than EPERM")
	}
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")
	content := "1 " + time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(lockPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write root-owned lock: %v", err)
	}

	if _, err := AcquireLock(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("AcquireLock over pid 1 (EPERM) = %v, want ErrLocked", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("a live holder's lock file was removed: %v", err)
	}
}

func TestBreakLock(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer release()

	if err := BreakLock(dir); err != nil {
		t.Fatalf("BreakLock: %v", err)
	}
	// BreakLock on an already-broken lock is unconditional and must not error.
	if err := BreakLock(dir); err != nil {
		t.Fatalf("second BreakLock: %v", err)
	}

	release2, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock after BreakLock: %v", err)
	}
	release2()
}

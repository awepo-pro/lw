// journal_lock.go keeps lw sync from losing a journal line (042 A-042-9).
//
// The journal is appended to by every lw process that stages an op — the TUI
// mid-turn, a second terminal's `lw ingest` — and none of them takes the vault
// lock, which only Commit takes. lw sync, meanwhile, has to take the work tree
// apart for a moment: it sets the journal's uncommitted tail aside, lets git
// replace the file with the other PC's version, and puts the tail back. A line
// appended in that window is overwritten, or makes git see a change it takes
// for a checkout collision. So the two sides share one advisory lock:
//
//   - an appender holds it SHARED, around the open, the write and the fsync of
//     one line — appenders do not wait for each other, and none holds it for
//     longer than a write;
//   - lw sync holds it EXCLUSIVE (QuiesceJournal) around each local mutation of
//     the work tree, and never across the network.
//
// The lock is a flock on .llmwiki/tmp/journal.lock — per PC, inside a directory
// the managed .gitignore already keeps out of every sync, and not on the
// journal itself, because git replaces that file and a lock on a replaced inode
// excludes nobody. The file exists only in a vault that syncs: appenders do not
// create it (a vault that never runs lw sync gains no file and no cost beyond
// one failed open), and a sync creates it before its first mutation
// (EnsureJournalLock; lw sync init and clone make it up front). A vault that was
// put under sync by an lw without the lock, or restored from a backup (tmp/ is
// not in one), is found by its git: OpenEngine makes the lock whenever lw's
// tracking ref is there (OpenEngine, SyncedVault), so even the first append of
// such a vault takes its side.
//
// Nothing about the journal changes: same bytes, same single O_APPEND write,
// same fsync.
package stage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// journalLockName is the lock file's name inside <llmwiki>/tmp.
const journalLockName = "journal.lock"

// journalLockWait is the longest an acquisition waits for the other side. An
// appender holds the lock for a write and a sync, and a sync holds it for a few
// local git commands; waiting longer than this means one of them is wedged, and
// an error is better than a frozen TUI.
var journalLockWait = 10 * time.Second

// journalLockPoll is how often a waiting acquisition tries again.
const journalLockPoll = 2 * time.Millisecond

// SyncTrackingRef is the ref lw sync fetches the remote's branch into: its
// presence in a vault's git is what says the vault syncs. internal/vaultsync
// has the same constant (a test keeps the two equal); stage cannot import it.
const SyncTrackingRef = "refs/remotes/lw/main"

// SyncedVault reports whether the vault at root has been put under lw sync —
// whether its git has lw's tracking ref, as a file or packed by git gc. Any
// doubt (no git, an unreadable file) answers false: the vault is then left as a
// vault that never syncs is.
func SyncedVault(root string) bool {
	gitDir := filepath.Join(root, ".git")
	if info, err := os.Stat(filepath.Join(gitDir, filepath.FromSlash(SyncTrackingRef))); err == nil && info.Mode().IsRegular() {
		return true
	}
	packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(packed), "\n") {
		if strings.HasSuffix(line, " "+SyncTrackingRef) {
			return true
		}
	}
	return false
}

// journalLockPath is where the lock of the journal under llmwikiDir lives.
func journalLockPath(llmwikiDir string) string {
	return filepath.Join(llmwikiDir, "tmp", journalLockName)
}

// EnsureJournalLock creates the journal lock file (and .llmwiki/tmp) if it is
// not there, so appenders from then on take their shared side of it. A vault
// that syncs calls it when it joins sync and before every quiesce.
func EnsureJournalLock(llmwikiDir string) error {
	p := journalLockPath(llmwikiDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("stage: journal lock: %w", err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("stage: journal lock: %w", err)
	}
	return f.Close()
}

// flockWait takes how (syscall.LOCK_SH or LOCK_EX) on f, polling until it gets
// it or journalLockWait passes.
func flockWait(f *os.File, how int) error {
	deadline := time.Now().Add(journalLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			if time.Now().After(deadline) {
				return fmt.Errorf("stage: the journal is busy (another lw process holds its lock)")
			}
			time.Sleep(journalLockPoll)
		default:
			return fmt.Errorf("stage: journal lock: %w", err)
		}
	}
}

// release unlocks and closes f, once.
func releaseFlock(f *os.File) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		})
	}
}

// lockJournalShared is an appender's side: shared, held for the length of one
// append. A vault with no lock file is not syncing and nothing can quiesce it,
// so there is nothing to take.
func lockJournalShared(journalPath string) (release func(), err error) {
	f, err := os.Open(journalLockPath(filepath.Dir(journalPath)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return func() {}, nil
		}
		return nil, fmt.Errorf("stage: journal lock: %w", err)
	}
	if err := flockWait(f, syscall.LOCK_SH); err != nil {
		f.Close()
		return nil, err
	}
	return releaseFlock(f), nil
}

// QuiesceJournal holds off every journal appender in the vault under
// llmwikiDir until the returned release is called: it takes the lock
// exclusively, which waits for appends in flight and blocks new ones. lw sync
// holds it around each local mutation of the work tree — carrying the journal's
// tail across a fast-forward or a rebase, take-remote's reset, a recovery —
// and releases it before any network call. It creates the lock file if need be.
func QuiesceJournal(llmwikiDir string) (release func(), err error) {
	if err := EnsureJournalLock(llmwikiDir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(journalLockPath(llmwikiDir), os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("stage: journal lock: %w", err)
	}
	if err := flockWait(f, syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return releaseFlock(f), nil
}

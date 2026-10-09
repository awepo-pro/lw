package stage

// journal_lock_test.go pins 042 A-042-9 (a): journal appends share a flock with
// lw sync's quiesce, and a vault that never syncs is unchanged.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lockedJournal returns a journal under a fresh .llmwiki directory and the
// directory.
func lockedJournal(t *testing.T) (*Journal, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".llmwiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(filepath.Join(dir, "journal.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	return j, dir
}

func appendNumbered(t *testing.T, j *Journal, n int) error {
	t.Helper()
	return j.Append(Event{TS: time.Unix(1, 0).UTC(), Kind: EvChangesetOpened, Changeset: "cs-1", Actor: Author{Kind: "human"}, Message: fmt.Sprintf("n=%d", n)})
}

// TestJournalAppendCreatesNoLockFile: a vault that never syncs gains no file —
// the lock is made by the sync that needs it.
func TestJournalAppendCreatesNoLockFile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tmpFirst bool // .llmwiki/tmp exists, as in any vault that has had a note
	}{{"no tmp directory", false}, {"a tmp directory", true}} {
		t.Run(tc.name, func(t *testing.T) {
			j, dir := lockedJournal(t)
			if tc.tmpFirst {
				if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 3; i++ {
				if err := appendNumbered(t, j, i); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(journalLockPath(dir)); err == nil {
				t.Error("appending to the journal created the lock file")
			}
			if _, err := os.Stat(filepath.Join(dir, "tmp")); err == nil != tc.tmpFirst {
				t.Errorf("appending changed whether .llmwiki/tmp exists (started with it: %v)", tc.tmpFirst)
			}
			if got := strings.Count(readTestFile(t, filepath.Join(dir, "journal.ndjson")), "\n"); got != 3 {
				t.Errorf("journal has %d lines, want 3", got)
			}
		})
	}
}

func readTestFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestQuiesceBlocksAppendsUntilReleased: while a sync holds the journal, an
// append waits — and lands, once, when it lets go.
func TestQuiesceBlocksAppendsUntilReleased(t *testing.T) {
	j, dir := lockedJournal(t)
	release, err := QuiesceJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	var done atomic.Bool
	errc := make(chan error, 1)
	go func() {
		err := appendNumbered(t, j, 1)
		done.Store(true)
		errc <- err
	}()
	time.Sleep(150 * time.Millisecond)
	if done.Load() {
		t.Fatal("an append went through a quiesced journal")
	}
	if got := readTestFile(t, filepath.Join(dir, "journal.ndjson")); got != "" {
		t.Fatalf("the journal changed while quiesced: %q", got)
	}
	release()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the append never completed after the release")
	}
	if got := strings.Count(readTestFile(t, filepath.Join(dir, "journal.ndjson")), `"n=1"`); got != 1 {
		t.Errorf("the line was written %d times, want 1", got)
	}
}

// TestQuiesceWaitsForAnAppendInFlight: the quiesce is exclusive of an append
// that already holds its shared side.
func TestQuiesceWaitsForAnAppendInFlight(t *testing.T) {
	_, dir := lockedJournal(t)
	if err := EnsureJournalLock(dir); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockJournalShared(filepath.Join(dir, "journal.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	var got atomic.Bool
	go func() {
		rel, err := QuiesceJournal(dir)
		if err == nil {
			got.Store(true)
			rel()
		}
	}()
	time.Sleep(150 * time.Millisecond)
	if got.Load() {
		t.Fatal("the quiesce was granted while an append held the lock")
	}
	unlock()
	deadline := time.Now().Add(5 * time.Second)
	for !got.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !got.Load() {
		t.Fatal("the quiesce never got the lock after the append finished")
	}
}

// TestAppendersDoNotBlockEachOther: many goroutines appending through a vault
// that has the lock keep every line, whole, exactly once.
func TestAppendersDoNotBlockEachOther(t *testing.T) {
	j, dir := lockedJournal(t)
	if err := EnsureJournalLock(dir); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	const workers, each = 8, 25
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := appendNumbered(t, j, w*1000+i); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(readTestFile(t, filepath.Join(dir, "journal.ndjson")), "\n"), "\n")
	if len(lines) != workers*each {
		t.Fatalf("%d lines, want %d", len(lines), workers*each)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		if seen[l] {
			t.Fatalf("duplicate line %q", l)
		}
		seen[l] = true
	}
}

// TestJournalLockWaitIsBounded: neither side waits forever for a wedged other.
func TestJournalLockWaitIsBounded(t *testing.T) {
	orig := journalLockWait
	journalLockWait = 120 * time.Millisecond
	t.Cleanup(func() { journalLockWait = orig })

	j, dir := lockedJournal(t)
	release, err := QuiesceJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := appendNumbered(t, j, 1); err == nil || !strings.Contains(err.Error(), "journal is busy") {
		t.Fatalf("append through a held lock = %v, want a busy error", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the append waited %v", d)
	}
	release()

	unlock, err := lockJournalShared(filepath.Join(dir, "journal.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := QuiesceJournal(dir); err == nil || !strings.Contains(err.Error(), "journal is busy") {
		t.Fatalf("QuiesceJournal against a held append = %v, want a busy error", err)
	}
}

// TestQuiesceReleaseIsIdempotent: releasing twice is harmless and the lock is
// free afterwards.
func TestQuiesceReleaseIsIdempotent(t *testing.T) {
	j, dir := lockedJournal(t)
	rel, err := QuiesceJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	rel()
	rel2, err := QuiesceJournal(dir)
	if err != nil {
		t.Fatalf("the lock was not free after a release: %v", err)
	}
	// A stale release (a deferred one running after an early one) must not let
	// go of the lock the next holder has.
	rel()
	done := make(chan error, 1)
	go func() { done <- appendNumbered(t, j, 1) }()
	select {
	case <-done:
		t.Fatal("a stale release let an append through the next holder's quiesce")
	case <-time.After(100 * time.Millisecond):
	}
	rel2()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

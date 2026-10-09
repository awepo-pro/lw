package stage

// journal_lock_synced_test.go pins 042 A-042-10 (3): a vault that syncs has its
// journal lock from the first append, whichever lw put it under sync — a vault
// joined by an lw that predates the lock, or restored from a backup (the lock
// lives in the ignored tmp/ and so is not in the backup). The engine makes it
// when the vault's git has lw's tracking ref; a vault that never syncs gains
// nothing.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

func TestOpenEngineMakesTheJournalLockOfASyncedVault(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, root string)
		synced bool
	}{
		{"no git at all", func(t *testing.T, root string) {}, false},
		{"git with no remote", func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
		}, false},
		{"git with another remote only", func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, ".git", "refs", "remotes", "origin", "main"), sha+"\n")
		}, false},
		{"git with a ref that only starts like lw's", func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, ".git", "packed-refs"), "# pack-refs with: peeled fully-peeled sorted \n"+sha+" refs/remotes/lw/main-old\n")
		}, false},
		{"lw's tracking ref as a file", func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, ".git", "refs", "remotes", "lw", "main"), sha+"\n")
		}, true},
		{"lw's tracking ref packed by git gc", func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, ".git", "packed-refs"), "# pack-refs with: peeled fully-peeled sorted \n"+sha+" refs/remotes/lw/main\n")
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testutil.CopyFixture(t, "minimal")
			tc.setup(t, root)
			e, err := OpenEngine(root)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			lock := journalLockPath(filepath.Join(root, ".llmwiki"))
			_, statErr := os.Stat(lock)
			if (statErr == nil) != tc.synced {
				t.Fatalf("lock file present = %v, want %v (%v)", statErr == nil, tc.synced, statErr)
			}
			if !tc.synced {
				return
			}
			// And the engine's own appends take their side of it from the first.
			rel, err := QuiesceJournal(filepath.Join(root, ".llmwiki"))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- e.journal.Append(Event{TS: time.Unix(1, 0).UTC(), Kind: EvChangesetOpened, Changeset: "cs-1", Actor: Author{Kind: "human"}, Message: "first"})
			}()
			select {
			case <-done:
				t.Fatal("the engine's first append went through a quiesce")
			case <-time.After(100 * time.Millisecond):
			}
			rel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

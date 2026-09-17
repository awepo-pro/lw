// open_cache_invalidation_test.go is 008 R-804 (MASTER §5): the reviewer's
// F-R1 probe made permanent, plus the F-R2 nil-cache Commit refusal. The
// open cache and the op counter are openMu-guarded since A-802, so a
// concurrent forgetOpen can no longer race Append — but it must never
// rewind the op counter either, and Commit must refuse rather than
// dereference a nil cache.
package stage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// persistedOpenOps reads the open changeset the way a fresh process would:
// directory walk under changesets/open/, then changeset.json. Returns the
// parsed Changeset so assertions run against what is persisted, not what
// the engine happens to hold in memory.
func persistedOpenOps(t *testing.T, e *Engine) *Changeset {
	t.Helper()
	entries, err := os.ReadDir(e.changesetOpenDir())
	if err != nil {
		t.Fatalf("read open dir: %v", err)
	}
	var dir string
	for _, ent := range entries {
		if ent.IsDir() {
			dir = ent.Name()
			break
		}
	}
	if dir == "" {
		t.Fatal("no open changeset on disk")
	}
	b, err := os.ReadFile(filepath.Join(e.changesetOpenDir(), dir, "changeset.json"))
	if err != nil {
		t.Fatalf("read changeset.json: %v", err)
	}
	var cs Changeset
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatalf("unmarshal changeset.json: %v", err)
	}
	return &cs
}

// assertUniqueOps fails the test when ids contains a duplicate, naming the
// duplicated id and its positions.
func assertUniqueOps(t *testing.T, what string, ids []string) {
	t.Helper()
	seen := map[string][]int{}
	for i, id := range ids {
		seen[id] = append(seen[id], i)
	}
	for id, at := range seen {
		if len(at) > 1 {
			t.Errorf("%s: duplicate op id %s at positions %v", what, id, at)
		}
	}
}

func TestOpenCacheInvalidation(t *testing.T) {
	t.Run("concurrent_forget_never_duplicates_op_ids", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("forget-vs-append", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		// The reviewer's TestZZProbeAppendVsForgetOpen shape: one
		// goroutine loops forgetOpen() — exactly what the reload body
		// and Reject call — while the main goroutine drives 40
		// Appends through the engine.
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
				}
				e.forgetOpen()
			}
		}()

		returned := make([]string, 0, 40)
		for i := 0; i < 40; i++ {
			path := fmt.Sprintf("raw/notes/forget-append-%02d.md", i)
			id, err := e.Append(Op{
				Kind:      OpIngestSource,
				Path:      path,
				Extractor: "go/html",
				Content:   stagedRawDoc("https://example.test/"+path, fmt.Sprintf("forget-vs-append body %d.\n", i)),
			})
			if err != nil {
				close(stop)
				<-done
				t.Fatalf("Append %d: %v", i, err)
			}
			returned = append(returned, id)
		}
		close(stop)
		<-done

		assertUniqueOps(t, "Append returns", returned)
		cs := persistedOpenOps(t, e)
		persisted := make([]string, 0, len(cs.Ops))
		for _, op := range cs.Ops {
			persisted = append(persisted, op.ID)
		}
		assertUniqueOps(t, "persisted changeset.json", persisted)
		if len(persisted) != 40 {
			t.Fatalf("persisted %d ops, want 40", len(persisted))
		}
	})

	t.Run("commit_with_emptied_cache_is_no_changeset", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("emptied cache", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		stageKVCachePatch(t, e)

		// Hold the F-R2 window deterministically: a forgetOpen lands
		// between Commit's Refresh (which repopulated the cache) and
		// its cachedOpen read — the shape a two-goroutine library
		// caller can hit for real. Production leaves the hook nil.
		e.invalidateOpen = e.forgetOpen

		id, err := e.Commit("must refuse, not panic")
		if err == nil {
			t.Fatalf("Commit succeeded (%s) on an emptied cache; want ErrNoChangeset", id)
		}
		if !errors.Is(err, ErrNoChangeset) {
			t.Fatalf("Commit error = %v, want errors.Is(..., ErrNoChangeset)", err)
		}

		// The refusal must release the lock (the same fence every
		// other Commit refusal obeys).
		unlock, lerr := AcquireLock(e.llmwikiDir())
		if lerr != nil {
			t.Fatalf("lock still held after refusal: %v", lerr)
		}
		unlock()
	})
}

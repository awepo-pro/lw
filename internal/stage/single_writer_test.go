// single_writer_test.go is 008 R-806 (MASTER §5, contract §10 / A-803):
// the R-805 review's two probes made permanent, plus the serialization and
// writer-failure assertions the single-writer model promises. One write
// mutex serializes every mutating verb; writers work on a private copy and
// publish it only once persisted; a coherence stamp re-reads disk before a
// mutation so a foreign process's change is never written over.
package stage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSingleWriter(t *testing.T) {
	t.Run("concurrent_writers_serialize", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("mixed writers", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		first := stageKVCachePatch(t, e)
		second := stageIngest(t, e, "raw/notes/mixed.md", "mixed body.\n")

		stop := make(chan struct{})
		var wg sync.WaitGroup
		var parses, parseErrs int64

		// changeset.json must parse after every operation: writes land by
		// temp-file-and-rename, and the single-writer serialization means a
		// reader can never catch a half-written file.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				entries, err := os.ReadDir(e.changesetOpenDir())
				if err == nil {
					for _, ent := range entries {
						if !ent.IsDir() {
							continue
						}
						b, err := os.ReadFile(filepath.Join(e.changesetOpenDir(), ent.Name(), "changeset.json"))
						if err != nil {
							continue
						}
						var cs Changeset
						atomic.AddInt64(&parses, 1)
						if err := json.Unmarshal(b, &cs); err != nil {
							atomic.AddInt64(&parseErrs, 1)
						}
					}
				}
			}
		}()

		// Two mutating loops beside the appenders: drops of the two seeded
		// ops, and refreshes of the whole changeset.
		dropIDs := []string{first, second}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, id := range dropIDs {
					if err := e.DropOp(id); err != nil {
						t.Errorf("DropOp %s: %v", id, err)
						return
					}
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := e.Refresh(); err != nil {
					t.Errorf("Refresh: %v", err)
					return
				}
			}
		}()

		// Four appenders at once. Under the single-writer model they
		// serialize; every id drawn is unique and every returned op is
		// persisted by the Append that returned it.
		const appenders, perAppender = 4, 10
		var mu sync.Mutex
		returned := make([]string, 0, appenders*perAppender)
		var appWG sync.WaitGroup
		for g := 0; g < appenders; g++ {
			appWG.Add(1)
			go func(g int) {
				defer appWG.Done()
				local := make([]string, 0, perAppender)
				for i := 0; i < perAppender; i++ {
					path := fmt.Sprintf("raw/notes/mixed-%02d-%02d.md", g, i)
					id, err := e.Append(Op{
						Kind:      OpIngestSource,
						Path:      path,
						Extractor: "go/html",
						Content:   stagedRawDoc("https://example.test/"+path, fmt.Sprintf("mixed body %d-%d.\n", g, i)),
					})
					if err != nil {
						t.Errorf("Append %d/%d: %v", g, i, err)
						return
					}
					local = append(local, id)
				}
				mu.Lock()
				returned = append(returned, local...)
				mu.Unlock()
			}(g)
		}
		appWG.Wait()
		close(stop)
		wg.Wait()

		assertUniqueOps(t, "Append returns", returned)
		cs := persistedOpenOps(t, e)
		persisted := make([]string, 0, len(cs.Ops))
		persistedSet := make(map[string]bool, len(cs.Ops))
		for _, op := range cs.Ops {
			persisted = append(persisted, op.ID)
			persistedSet[op.ID] = true
		}
		assertUniqueOps(t, "persisted changeset.json", persisted)
		for _, id := range returned {
			if !persistedSet[id] {
				t.Errorf("op %s was returned by Append but is missing from changeset.json", id)
			}
		}
		if n := atomic.LoadInt64(&parseErrs); n > 0 {
			t.Errorf("changeset.json failed to parse %d of %d mid-run reads", n, atomic.LoadInt64(&parses))
		}
	})

	// foreign_change_is_not_clobbered is the R-805 review's
	// TestZZProbeR805ForeignDropThenTUIAppend (F-805-1): a foreign engine
	// drops an op and persists; this engine — its cache stale, no
	// ReloadIfChanged in between — then appends. The coherence check must
	// re-read disk first, so the foreign drop survives AND the new op lands.
	t.Run("foreign_change_is_not_clobbered", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("foreign-clobber", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := stageIngest(t, e, "raw/notes/clobber.md", "clobber body.\n")
		if _, err := e.Current(); err != nil { // warm the cache
			t.Fatalf("Current: %v", err)
		}

		e2 := foreignEngine(t, dir)
		if _, err := e2.Current(); err != nil {
			t.Fatalf("foreign Current: %v", err)
		}
		if err := e2.DropOp(id); err != nil { // the CLI reviewer's drop
			t.Fatalf("foreign DropOp: %v", err)
		}

		// The TUI acts inside the reload-tick window: another op is
		// proposed without any reload in between.
		const freshPath = "raw/notes/clobber-2.md"
		newID, err := e.Append(Op{
			Kind:      OpIngestSource,
			Path:      freshPath,
			Extractor: "go/html",
			Content:   stagedRawDoc("https://example.test/clobber-2", "second body.\n"),
		})
		if err != nil {
			t.Fatalf("TUI Append: %v", err)
		}

		cs := persistedOpenOps(t, e)
		var droppedOK, freshOK bool
		for _, op := range cs.Ops {
			if op.ID == id && op.State == StateDropped {
				droppedOK = true
			}
			if op.ID == newID && op.Path == freshPath {
				freshOK = true
			}
		}
		if !droppedOK {
			t.Errorf("foreign drop clobbered by the TUI's write-back: op %s is not %q in changeset.json", id, StateDropped)
		}
		if !freshOK {
			t.Errorf("op %s appended after the foreign change is missing from changeset.json", newID)
		}
	})

	// mutator_vs_current_is_race_free is the R-805 review's
	// TestZZProbeR805MutatorVsCurrentRace (F-805-2): a field-mutator loop
	// against a Current clone loop must be clean under -race. Run through
	// `go test -race`, as the frozen block does.
	t.Run("mutator_vs_current_is_race_free", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("mutator vs current", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := stageIngest(t, e, "raw/notes/mutator.md", "mutator body.\n")

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
				if err := e.DropOp(id); err != nil {
					t.Errorf("DropOp: %v", err)
					return
				}
			}
		}()
		deadline := len(e.cachedOpen().Ops) + 2000
		for i := 0; i < deadline; i++ {
			if _, err := e.Current(); err != nil {
				t.Errorf("Current: %v", err)
				break
			}
		}
		close(stop)
		<-done
	})

	// writer_failure_leaves_cache_equal_to_disk covers the DropHunk/Refresh
	// gap the R-805 review named: a writer that fails before its persist
	// must leave the published cache equal to what is on disk. With the
	// copy-mutate-publish model the mutation happens on a private copy, so
	// the failure publishes nothing. Two injections, both natural seams:
	// a CAS that cannot serve the op's pre-image (DropHunk), and a changeset
	// directory the persist cannot write into (Refresh).
	t.Run("writer_failure_leaves_cache_equal_to_disk", func(t *testing.T) {
		// DropHunk failing at store.Get, after hunk.Dropped was set.
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("failed drop hunk", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		opID := stageKVCachePatch(t, e)
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		op, ok := cs.Op(opID)
		if !ok {
			t.Fatalf("op %s not found", opID)
		}
		// Forge the cached op's pre-image to a sha the CAS does not hold
		// (the staged_test.go forging shape). The DropHunk below must fail
		// at store.Get; the assertion is about the fields that writer was
		// mutating (Dropped/After), not the forged Before itself.
		op.Before = strings.Repeat("0", 64)
		e.cacheOpen(cs)

		if err := e.DropHunk(opID, "h1"); err == nil {
			t.Fatal("DropHunk = nil, want the store.Get error for the forged sha")
		}
		assertCacheEqualsDisk(t, e)

		// Refresh failing at the persist, after the stale flip.
		e2, dir := newTestEngine(t)
		opened, err := e2.OpenChangeset("failed refresh", testAuthor)
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		const stalePath = "raw/notes/refresh-stale.md"
		opID2 := stageIngest(t, e2, stalePath, "refresh body.\n")

		// Make the op stale-worthy: an ingest_source goes stale when its
		// target path appears in the working tree.
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(stalePath)), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, stalePath), []byte("appeared\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		csDir := filepath.Join(e2.changesetOpenDir(), opened.ID)
		if err := os.Chmod(csDir, 0o500); err != nil {
			t.Fatalf("Chmod: %v", err)
		}
		t.Cleanup(func() { os.Chmod(csDir, 0o755) })

		if err := e2.Refresh(); err == nil {
			t.Fatal("Refresh = nil, want the persist error from the read-only directory")
		}
		assertCacheEqualsDisk(t, e2)
		if got := e2.cachedOpen().Ops[0].ID; got != opID2 {
			t.Errorf("cache holds op %s, want %s", got, opID2)
		}
	})
}

// assertCacheEqualsDisk fails the test unless the engine's published cache
// and the persisted changeset.json agree on the changeset id, every op's id
// and state, and every hunk's dropped flag — the quiescent invariant every
// A-803 writer keeps when it fails before its persist.
func assertCacheEqualsDisk(t *testing.T, e *Engine) {
	t.Helper()
	cached, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	disk := persistedOpenOps(t, e)
	if cached.ID != disk.ID {
		t.Fatalf("cache holds changeset %s, disk holds %s", cached.ID, disk.ID)
	}
	if len(cached.Ops) != len(disk.Ops) {
		t.Fatalf("cache holds %d ops, disk holds %d", len(cached.Ops), len(disk.Ops))
	}
	for i := range disk.Ops {
		if cached.Ops[i].ID != disk.Ops[i].ID {
			t.Fatalf("cache op %d = %s, disk op %d = %s", i, cached.Ops[i].ID, i, disk.Ops[i].ID)
		}
		if cached.Ops[i].State != disk.Ops[i].State {
			t.Errorf("op %s: cache state = %q, disk state = %q — a failed writer left the cache ahead of disk",
				disk.Ops[i].ID, cached.Ops[i].State, disk.Ops[i].State)
		}
		for j := range disk.Ops[i].Hunks {
			if cached.Ops[i].Hunks[j].Dropped != disk.Ops[i].Hunks[j].Dropped {
				t.Errorf("op %s hunk %s: cache dropped = %v, disk dropped = %v — a failed writer left the cache ahead of disk",
					disk.Ops[i].ID, disk.Ops[i].Hunks[j].ID, cached.Ops[i].Hunks[j].Dropped, disk.Ops[i].Hunks[j].Dropped)
			}
		}
	}
}

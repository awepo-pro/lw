// current_vs_append_test.go is 008 R-805 (MASTER §5, design D-8H): the
// R-804 review's Probe C made permanent. A concurrent Current() must never
// rewind an in-flight Append's drawn-but-unpersisted op id, never publish a
// cache copy that lacks it, and never hand a caller an aliased view of the
// cache. While a changeset is cached, Current serves a deep copy of the
// cache and touches neither the counter nor the filesystem; only an empty
// cache sends it to disk.
package stage

import (
	"fmt"
	"testing"
)

// driveAppendVsCurrent opens a changeset and then runs the reviewer's
// Probe C shape: one goroutine loops Current() while the main goroutine
// drives n Appends through the engine. It returns the engine and every id
// Append returned, in order.
func driveAppendVsCurrent(t *testing.T, n int) (*Engine, []string) {
	t.Helper()
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("current-vs-append", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	var loopErr error
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			cs, err := e.Current()
			if err != nil {
				loopErr = fmt.Errorf("Current during append: %w", err)
				return
			}
			seen := make(map[string]bool, len(cs.Ops))
			for _, op := range cs.Ops {
				if seen[op.ID] {
					t.Errorf("Current returned duplicate op id %s", op.ID)
				}
				seen[op.ID] = true
			}
		}
	}()

	returned := make([]string, 0, n)
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("raw/notes/current-append-%02d.md", i)
		id, err := e.Append(Op{
			Kind:      OpIngestSource,
			Path:      path,
			Extractor: "go/html",
			Content:   stagedRawDoc("https://example.test/"+path, fmt.Sprintf("current-vs-append body %d.\n", i)),
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
	if loopErr != nil {
		t.Fatalf("%v", loopErr)
	}
	return e, returned
}

func TestCurrentVsAppend(t *testing.T) {
	t.Run("current_during_append_never_duplicates", func(t *testing.T) {
		e, returned := driveAppendVsCurrent(t, 40)
		assertUniqueOps(t, "Append returns", returned)
		cs := persistedOpenOps(t, e)
		persisted := make([]string, 0, len(cs.Ops))
		for _, op := range cs.Ops {
			persisted = append(persisted, op.ID)
		}
		assertUniqueOps(t, "persisted changeset.json", persisted)
	})

	t.Run("current_during_append_never_loses_ops", func(t *testing.T) {
		e, returned := driveAppendVsCurrent(t, 40)
		cs := persistedOpenOps(t, e)
		if len(cs.Ops) != len(returned) {
			t.Fatalf("persisted %d ops, want all %d Append returned", len(cs.Ops), len(returned))
		}
		persisted := make(map[string]bool, len(cs.Ops))
		for _, op := range cs.Ops {
			persisted[op.ID] = true
		}
		for _, id := range returned {
			if !persisted[id] {
				t.Errorf("op %s was returned by Append but is missing from changeset.json", id)
			}
		}
	})

	t.Run("current_returns_an_independent_copy", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("independent copy", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		stageIngest(t, e, "raw/notes/copy-a.md", "first body.\n")
		stageKVCachePatch(t, e)

		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if len(cs.Ops) != 2 {
			t.Fatalf("Current returned %d ops, want 2", len(cs.Ops))
		}
		wantID, wantIntent, wantPath := cs.ID, cs.Intent, cs.Ops[0].Path

		// Forge through everything the returned value exposes: header
		// fields, the Ops slice itself, an op's fields and a hunk's
		// fields. None of it may reach the cache or a later Current.
		cs.ID = "cs-forged"
		cs.Intent = "forged intent"
		cs.Ops[0].Path = "raw/notes/forged.md"
		cs.Ops[0].State = StateDropped
		cs.Ops[1].Hunks[0].Dropped = true
		cs.Ops[1].Hunks[0].Add[0] = "forged line"
		cs.Ops = append(cs.Ops, Op{ID: "op99", Kind: OpRetract, Path: "wiki/forged.md", State: StateProposed})

		again, err := e.Current()
		if err != nil {
			t.Fatalf("Current again: %v", err)
		}
		if again.ID != wantID || again.Intent != wantIntent {
			t.Errorf("forged header leaked: id %q intent %q, want %q / %q", again.ID, again.Intent, wantID, wantIntent)
		}
		if len(again.Ops) != 2 {
			t.Fatalf("forged op leaked: Current returned %d ops, want 2", len(again.Ops))
		}
		if again.Ops[0].Path != wantPath || again.Ops[0].State != StateProposed {
			t.Errorf("forged op fields leaked: path %q state %q", again.Ops[0].Path, again.Ops[0].State)
		}
		if again.Ops[1].Hunks[0].Dropped || again.Ops[1].Hunks[0].Add[0] == "forged line" {
			t.Error("forged hunk fields leaked into a later Current")
		}

		// The cache itself, not only the next copy.
		cached := e.cachedOpen()
		if cached == nil {
			t.Fatal("no cached changeset after Current")
		}
		if cached.ID != wantID || len(cached.Ops) != 2 || cached.Ops[0].Path != wantPath {
			t.Errorf("forged fields reached the cache: id %q ops %d path %q",
				cached.ID, len(cached.Ops), cached.Ops[0].Path)
		}
		if cached.Ops[1].Hunks[0].Dropped {
			t.Error("forged hunk drop reached the cache")
		}
	})

	t.Run("current_rereads_after_invalidation", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("reread after invalidation", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := stageIngest(t, e, "raw/notes/reread.md", "reread body.\n")

		if _, err := e.Current(); err != nil {
			t.Fatalf("Current: %v", err)
		}
		e.forgetOpen()
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current after forgetOpen: %v", err)
		}
		if len(cs.Ops) != 1 || cs.Ops[0].ID != id {
			t.Fatalf("Current after forgetOpen = %d ops, want the persisted %s", len(cs.Ops), id)
		}

		// A foreign engine drops the op; the reload tick notices the
		// foreign journal write, forgets the cache, and the next Current
		// must reflect disk.
		e2 := foreignEngine(t, dir)
		if _, err := e2.Current(); err != nil {
			t.Fatalf("Current (second engine): %v", err)
		}
		if err := e2.DropOp(id); err != nil {
			t.Fatalf("DropOp (second engine): %v", err)
		}
		reloaded, err := e.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged: %v", err)
		}
		if !reloaded {
			t.Fatal("ReloadIfChanged = false after a foreign DropOp, want true")
		}
		cs, err = e.Current()
		if err != nil {
			t.Fatalf("Current after reload: %v", err)
		}
		if len(cs.Ops) != 1 || cs.Ops[0].State != StateDropped {
			t.Fatalf("Current after a foreign drop = state %q, want %q", cs.Ops[0].State, StateDropped)
		}
	})
}

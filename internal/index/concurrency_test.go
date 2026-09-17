// concurrency_test.go is 008 A-802's index half (MASTER §5 R-803): the
// docs map moves as one immutable, copy-on-write map behind an atomic
// pointer, so Search and Len stay race-free against Rebuild and Update,
// and neither mutator replaces the *Index object the agent's tool registry
// holds for the life of the process.
package index

import (
	"sync"
	"testing"
)

func TestIndexConcurrentUpdate(t *testing.T) {
	t.Run("search_during_rebuild_is_race_free", func(t *testing.T) {
		v := openMinimal(t)
		ix := Build(v)

		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if hits := ix.Search("attention memory", Options{}); len(hits) == 0 {
					t.Errorf("Search returned no hits over the minimal fixture")
					return
				}
				if n := ix.Len(); n != 4 {
					t.Errorf("Len() = %d, want 4", n)
					return
				}
			}
		}()

		for i := 0; i < 200; i++ {
			ix.Rebuild(v)
		}

		close(stop)
		wg.Wait()
	})

	t.Run("search_during_update_is_race_free", func(t *testing.T) {
		v := openMinimal(t)
		ix := Build(v)

		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = ix.Search("attention memory", Options{})
				_ = ix.Len()
			}
		}()

		// One path that is always re-built and one that is always dropped —
		// both halves of Update's contract, each write landing on the live
		// map the searcher is iterating in the pre-A-802 code.
		paths := []string{"wiki/concepts/kv-cache.md", "wiki/concepts/not-there.md"}
		for i := 0; i < 200; i++ {
			ix.Update(v, paths)
		}

		close(stop)
		wg.Wait()

		if n := ix.Len(); n != 4 {
			t.Errorf("Len() after the updates = %d, want the fixture's 4", n)
		}
	})

	t.Run("update_keeps_identity", func(t *testing.T) {
		v := openMinimal(t)
		ix := Build(v)
		before := ix

		ix.Update(v, []string{"wiki/concepts/kv-cache.md"})
		if ix != before {
			t.Fatal("Update replaced the *Index object")
		}
		ix.Rebuild(v)
		if ix != before {
			t.Fatal("Rebuild replaced the *Index object")
		}

		// The pointer is the same object AND still answers for the vault.
		if got := ix.Len(); got != 4 {
			t.Errorf("Len() = %d, want 4", got)
		}
		hits := ix.Search("speculative decoding", Options{})
		if len(hits) == 0 || hits[0].Path != "wiki/concepts/speculative-decoding.md" {
			t.Errorf("Search after Update+Rebuild = %+v, want speculative-decoding.md first", hits)
		}
	})
}

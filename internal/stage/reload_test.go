// reload_test.go pins Engine.ReloadIfChanged (008 contract §3): the stamp
// taken at OpenEngine and after every own append makes this Engine's own
// writes invisible, a foreign commit is seen and reloaded (vault, index and
// the cached changeset), and the check is quiet while this Engine holds the
// commit lock.
package stage

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
)

// foreignEngine opens a second Engine over the same root — the stand-in for
// another lw process — and registers its Close.
func foreignEngine(t *testing.T, dir string) *Engine {
	t.Helper()
	e2, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine (second): %v", err)
	}
	t.Cleanup(func() { e2.Close() })
	return e2
}

func TestReloadIfChanged(t *testing.T) {
	t.Run("unchanged_journal_is_noop", func(t *testing.T) {
		e, _ := newTestEngine(t)
		reloaded, err := e.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged: %v", err)
		}
		if reloaded {
			t.Fatal("ReloadIfChanged = true on an unchanged journal, want false")
		}
	})

	t.Run("own_appends_do_not_count", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("watching my own writes", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		stageKVCachePatch(t, e)

		reloaded, err := e.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged: %v", err)
		}
		if reloaded {
			t.Fatal("ReloadIfChanged = true after this engine's own appends, want false")
		}
	})

	t.Run("foreign_commit_reloads_vault", func(t *testing.T) {
		e1, dir := newTestEngine(t)
		e2 := foreignEngine(t, dir)

		if _, err := e2.OpenChangeset("a foreign page", testAuthor); err != nil {
			t.Fatalf("OpenChangeset (second engine): %v", err)
		}
		const path = "wiki/concepts/foreign-page.md"
		content := []byte("---\ntitle: Foreign Page\ncreated: 2026-08-29\nupdated: 2026-08-29\ntype: concept\n" +
			"tags: [inference]\nconfidence: medium\n---\n\n" +
			"# Foreign Page\n\nZygohistactic prefetching is a word only this page carries.\n\n" +
			"See [[kv-cache]] and [[gpt-4]] for background.\n")
		if _, err := e2.Append(Op{
			Kind:       OpCreatePage,
			Path:       path,
			Content:    content,
			Rationale:  "foreign",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}); err != nil {
			t.Fatalf("Append (second engine): %v", err)
		}
		if _, err := e2.Commit("foreign commit"); err != nil {
			t.Fatalf("Commit (second engine): %v", err)
		}

		reloaded, err := e1.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged: %v", err)
		}
		if !reloaded {
			t.Fatal("ReloadIfChanged = false after a foreign commit, want true")
		}
		if _, ok := e1.Vault().Page(path); !ok {
			t.Fatal("the reloaded vault does not contain the foreign page")
		}
		var found bool
		for _, hit := range e1.Index().Search("zygohistactic", index.Options{}) {
			if hit.Path == path {
				found = true
			}
		}
		if !found {
			t.Fatal("the reloaded index does not find the foreign page by a word from its body")
		}
	})

	t.Run("foreign_commit_clears_cached_changeset", func(t *testing.T) {
		e1, dir := newTestEngine(t)
		if _, err := e1.OpenChangeset("cached on engine one", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		// One live op, so the foreign Commit can land this changeset.
		stageKVCachePatch(t, e1)

		e2 := foreignEngine(t, dir)
		if _, err := e2.Current(); err != nil {
			t.Fatalf("Current (second engine): %v", err)
		}
		if _, err := e2.Commit("foreign commit"); err != nil {
			t.Fatalf("Commit (second engine): %v", err)
		}

		reloaded, err := e1.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged: %v", err)
		}
		if !reloaded {
			t.Fatal("ReloadIfChanged = false after a foreign commit, want true")
		}
		if _, err := e1.Current(); !errors.Is(err, ErrNoChangeset) {
			t.Fatalf("Current after the reload = %v, want ErrNoChangeset", err)
		}
		// The cleared cache is what currentOpen serves Append from: with the
		// stale pointer gone, a following Append re-reads disk and finds no
		// open changeset instead of resurrecting the committed one.
		if _, err := e1.Append(Op{Kind: OpAddLink, From: "wiki/concepts/kv-cache.md", To: "wiki/entities/gpt-4.md"}); !errors.Is(err, ErrNoChangeset) {
			t.Fatalf("Append after the reload = %v, want ErrNoChangeset", err)
		}
	})

	t.Run("held_lock_is_a_noop", func(t *testing.T) {
		e1, dir := newTestEngine(t)
		// As Commit leaves the engine between step 1 and step 10: lock held.
		e1.unlock = func() error { return nil }

		e2 := foreignEngine(t, dir)
		if _, err := e2.OpenChangeset("foreign traffic", testAuthor); err != nil {
			t.Fatalf("OpenChangeset (second engine): %v", err)
		}
		if _, err := e2.Append(Op{Kind: OpAddLink, From: "wiki/concepts/kv-cache.md", To: "wiki/entities/gpt-4.md"}); err != nil {
			t.Fatalf("Append (second engine): %v", err)
		}

		reloaded, err := e1.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged: %v", err)
		}
		if reloaded {
			t.Fatal("ReloadIfChanged reloaded while the commit lock was held, want a no-op")
		}
	})

	t.Run("concurrent_stamp_access_is_race_free", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("concurrent stamp traffic", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		const oldLine = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
		if !strings.Contains(page.Body, oldLine) {
			t.Fatalf("fixture body does not contain the hunk's old line:\n%s", page.Body)
		}

		const rounds = 32
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
				if _, err := e.ReloadIfChanged(); err != nil {
					t.Errorf("ReloadIfChanged: %v", err)
					return
				}
			}
		}()
		for i := 0; i < rounds; i++ {
			newLine := fmt.Sprintf("- [[flash-attention]] — a kernel design that cuts round-%d memory-bandwidth cost", i)
			rewritten := *page
			rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)
			if _, err := e.Append(Op{
				Kind:      OpPatchPage,
				Path:      page.Path,
				Section:   "## Related",
				Before:    page.SHA256(),
				Content:   rewritten.Serialize(),
				Rationale: "race traffic",
				Hunks: []Hunk{{
					ID:   "h1",
					Path: page.Path,
					Del:  []string{oldLine},
					Add:  []string{newLine},
				}},
			}); err != nil {
				t.Fatalf("Append %d: %v", i, err)
			}
		}
		close(stop)
		wg.Wait()
	})
}

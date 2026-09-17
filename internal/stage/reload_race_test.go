// reload_race_test.go is 008 A-802's stage half (MASTER §5 R-803), the
// reviewer's canaries made permanent and unguarded: a ReloadIfChanged with
// NO busy guard in front of it — the shape every goroutine family the A-801
// enumeration missed would hit — must be race-free against the reads an
// agent turn, a lint report and a review load issue from other goroutines.
package stage

import (
	"fmt"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
)

// foreignCreateCommit drives e2 (a second engine on the same root) through
// one full create + commit of wiki/concepts/foreign-page-<n>.md, carrying
// the unique word xylophone<n> in its body.
func foreignCreateCommit(t *testing.T, e2 *Engine, n int) {
	t.Helper()
	if _, err := e2.OpenChangeset(fmt.Sprintf("foreign page %d", n), testAuthor); err != nil {
		t.Fatalf("OpenChangeset (foreign %d): %v", n, err)
	}
	path := fmt.Sprintf("wiki/concepts/foreign-page-%02d.md", n)
	content := []byte(fmt.Sprintf("---\ntitle: Foreign Page %d\ncreated: 2026-08-29\nupdated: 2026-08-29\ntype: concept\n"+
		"tags: [inference]\nconfidence: medium\n---\n\n"+
		"# Foreign Page %d\n\nxylophone%02d is a word only this page carries.\n\n"+
		"See [[kv-cache]] and [[gpt-4]] for background.\n", n, n, n))
	if _, err := e2.Append(Op{
		Kind:       OpCreatePage,
		Path:       path,
		Content:    content,
		Rationale:  "foreign",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append (foreign %d): %v", n, err)
	}
	if _, err := e2.Commit("foreign commit"); err != nil {
		t.Fatalf("Commit (foreign %d): %v", n, err)
	}
}

// spinUntilStop runs fn in a goroutine until the returned stop channel
// closes, the harness for every concurrent reader below. Errors are
// reported through t and end the loop; t.Errorf is goroutine-safe, t.Fatalf
// is not. Call wait after closing stop.
func spinUntilStop(t *testing.T, fn func() error) (stop chan struct{}, wait func()) {
	t.Helper()
	stop = make(chan struct{})
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
			if err := fn(); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	return stop, wg.Wait
}

func TestEngineReloadRaceFree(t *testing.T) {
	t.Run("unguarded_reload_while_agent_reads", func(t *testing.T) {
		e, dir := newTestEngine(t)
		e2 := foreignEngine(t, dir)

		stop, wait := spinUntilStop(t, func() error {
			v := e.Vault()
			for _, p := range v.Pages() {
				if _, ok := v.Page(p.Path); !ok {
					return fmt.Errorf("Page(%q) = !ok right after Pages() returned it", p.Path)
				}
			}
			for _, r := range v.RawSources() {
				if _, ok := v.RawSource(r.Path); !ok {
					return fmt.Errorf("RawSource(%q) = !ok right after RawSources() returned it", r.Path)
				}
			}
			g := v.Graph()
			g.Backlinks("wiki/concepts/kv-cache.md")
			g.Outbound("wiki/concepts/kv-cache.md")
			_ = e.Index().Search("attention", index.Options{})
			_, _ = e.Current() // ErrNoChangeset between foreign commits is fine
			return nil
		})

		const rounds = 12
		for i := 0; i < rounds; i++ {
			foreignCreateCommit(t, e2, i)
			if _, err := e.ReloadIfChanged(); err != nil {
				t.Fatalf("ReloadIfChanged (round %d): %v", i, err)
			}
		}

		close(stop)
		wait()

		// The final state shows every foreign page, in the vault and in the
		// index the reader was searching all along.
		for i := 0; i < rounds; i++ {
			path := fmt.Sprintf("wiki/concepts/foreign-page-%02d.md", i)
			if _, ok := e.Vault().Page(path); !ok {
				t.Fatalf("the reloaded vault does not contain %s", path)
			}
			if !findsByWord(e.Index(), fmt.Sprintf("xylophone%02d", i), path) {
				t.Fatalf("the reloaded index does not find %s by its unique word", path)
			}
		}
	})

	t.Run("current_while_reload_is_race_free", func(t *testing.T) {
		e, dir := newTestEngine(t)
		e2 := foreignEngine(t, dir)

		stop, wait := spinUntilStop(t, func() error {
			// Writes the cached open changeset whenever a foreign changeset
			// is open on disk — the exact field the reload clears.
			_, _ = e.Current()
			return nil
		})

		const rounds = 12
		for i := 0; i < rounds; i++ {
			foreignCreateCommit(t, e2, 100+i)
			if _, err := e.ReloadIfChanged(); err != nil {
				t.Fatalf("ReloadIfChanged (round %d): %v", i, err)
			}
		}

		close(stop)
		wait()
	})
}

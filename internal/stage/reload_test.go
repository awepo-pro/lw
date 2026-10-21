// reload_test.go pins Engine.ReloadIfChanged (008 contract §3): the stamp
// taken at OpenEngine and after every own append makes this Engine's own
// writes invisible, a foreign commit is seen and reloaded (vault, index and
// the cached changeset), and the check is quiet while this Engine holds the
// commit lock.
package stage

import (
	"errors"
	"os"
	"path/filepath"
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

		// 020 T-A: the traffic op is add_link, not a repeated patch_page.
		// This subtest probes the journal stamp vs ReloadIfChanged's stat
		// under openMu — it does not care what kind is appended — and a
		// patch_page repeated with the same committed Before is now
		// refused from round 2 on: round 1 stages new content at the
		// path, and an unchained Before on a staged path is exactly the
		// shape chained-ops validation exists to reject. add_link chains
		// nothing and validates the same on every round, so the race
		// probe keeps its 32 successful appends. (Amended outside T-A's
		// file set; see runs/T-A/report.md "Blockers".)
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
			if _, err := e.Append(Op{
				Kind: OpAddLink,
				From: "wiki/concepts/kv-cache.md",
				To:   "wiki/entities/gpt-4.md",
			}); err != nil {
				t.Fatalf("Append %d: %v", i, err)
			}
		}
		close(stop)
		wg.Wait()
	})
}

func TestEngineReloadVault(t *testing.T) {
	t.Run("reload_reflects_disk_and_flips_stale", func(t *testing.T) {
		e, dir := newTestEngine(t)

		// Open a changeset and stage a patch targeting kv-cache.md.
		if _, err := e.OpenChangeset("reload vault test", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		opID := stageKVCachePatch(t, e)

		c, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		op, ok := c.Op(opID)
		if !ok {
			t.Fatalf("op %s not found in changeset", opID)
		}
		if op.State == StateStale {
			t.Fatalf("op.State = %v before reload, want non-stale", op.State)
		}

		// Read the original page from Vault to ensure it exists.
		const targetPath = "wiki/concepts/kv-cache.md"
		page, ok := e.Vault().Page(targetPath)
		if !ok {
			t.Fatalf("target page %s not found in initial vault", targetPath)
		}

		// Directly modify the page on disk outside of stage engine.
		onDisk := filepath.Join(dir, filepath.FromSlash(targetPath))
		const magicKeyword = "hyperparameterized"
		const appendedLine = "\n\nDirectly edited on disk with " + magicKeyword + " token.\n"
		newContent := append([]byte(page.Serialize()), []byte(appendedLine)...)
		if err := os.WriteFile(onDisk, newContent, 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		// ReloadVault must re-read pages, rebuild index, save index.gob, refresh staleness, stamp journal.
		if err := e.ReloadVault(); err != nil {
			t.Fatalf("ReloadVault: %v", err)
		}

		// 1. Verify e.Vault().Page() reflects the new content.
		reloadedPage, ok := e.Vault().Page(targetPath)
		if !ok {
			t.Fatalf("target page %s missing after ReloadVault", targetPath)
		}
		if !strings.Contains(reloadedPage.Body, magicKeyword) {
			t.Fatalf("reloaded page body does not contain %q; got:\n%s", magicKeyword, reloadedPage.Body)
		}

		// 2. Verify e.Index() reflects the new content.
		hits := e.Index().Search(magicKeyword, index.Options{})
		var foundInIndex bool
		for _, hit := range hits {
			if hit.Path == targetPath {
				foundInIndex = true
				break
			}
		}
		if !foundInIndex {
			t.Fatalf("Index().Search(%q) did not find %s; hits = %+v", magicKeyword, targetPath, hits)
		}

		// 3. Verify index.gob was written to disk and is non-empty.
		indexGobPath := filepath.Join(e.llmwikiDir(), "index.gob")
		stat, err := os.Stat(indexGobPath)
		if err != nil {
			t.Fatalf("stat index.gob: %v", err)
		}
		if stat.Size() == 0 {
			t.Fatal("saved index.gob is empty")
		}

		// 4. Verify open changeset's patch_page op targeting the modified page flips to StateStale.
		cAfter, err := e.Current()
		if err != nil {
			t.Fatalf("Current after reload: %v", err)
		}
		opAfter, ok := cAfter.Op(opID)
		if !ok {
			t.Fatalf("op %s not found after reload", opID)
		}
		if opAfter.State != StateStale {
			t.Fatalf("op.State after ReloadVault = %v, want %v", opAfter.State, StateStale)
		}
	})

	t.Run("reload_error_on_corrupted_schema", func(t *testing.T) {
		e, dir := newTestEngine(t)
		schemaPath := filepath.Join(dir, "SCHEMA.md")
		if err := os.WriteFile(schemaPath, []byte("invalid schema ::: [}"), 0o644); err != nil {
			t.Fatalf("WriteFile SCHEMA.md: %v", err)
		}
		if err := e.ReloadVault(); err == nil {
			t.Fatal("ReloadVault = nil on corrupted SCHEMA.md, want error")
		}
	})
}

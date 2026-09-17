// reload_identity_test.go pins 008 A-801: ReloadIfChanged updates the
// index in place, so the *index.Index a caller captured before a reload —
// the agent tool registry's, for the life of the process — is the same
// pointer after it and answers for the reloaded vault.
package stage

import (
	"testing"

	"github.com/awepo-pro/lw/internal/index"
)

// foreignIngestUniqueWord drives a second engine (the stand-in for another
// lw process) through create + commit of a page whose body carries word —
// a word no other page in the fixture contains.
func foreignIngestUniqueWord(t *testing.T, dir, word string) {
	t.Helper()
	e2 := foreignEngine(t, dir)
	if _, err := e2.OpenChangeset("a foreign page with "+word, testAuthor); err != nil {
		t.Fatalf("OpenChangeset (second engine): %v", err)
	}
	const path = "wiki/concepts/foreign-page.md"
	content := []byte("---\ntitle: Foreign Page\ncreated: 2026-08-29\nupdated: 2026-08-29\ntype: concept\n" +
		"tags: [inference]\nconfidence: medium\n---\n\n" +
		"# Foreign Page\n\n" + word + " is a word only this page carries.\n\n" +
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
}

// findsByWord reports whether ix returns path for word.
func findsByWord(ix *index.Index, word, path string) bool {
	for _, hit := range ix.Search(word, index.Options{}) {
		if hit.Path == path {
			return true
		}
	}
	return false
}

func TestReloadKeepsIndexIdentity(t *testing.T) {
	const (
		path = "wiki/concepts/foreign-page.md"
		word = "zygohistactic"
	)

	t.Run("pointer_unchanged_after_reload", func(t *testing.T) {
		e1, dir := newTestEngine(t)
		ix := e1.Index()

		foreignIngestUniqueWord(t, dir, word)

		reloaded, err := e1.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged: %v", err)
		}
		if !reloaded {
			t.Fatal("ReloadIfChanged = false after a foreign commit, want true")
		}
		if e1.Index() != ix {
			t.Fatal("ReloadIfChanged replaced the index object; the agent's captured pointer would go stale")
		}
	})

	t.Run("old_pointer_finds_foreign_page", func(t *testing.T) {
		e1, dir := newTestEngine(t)
		ix := e1.Index()

		foreignIngestUniqueWord(t, dir, word)

		if _, err := e1.ReloadIfChanged(); err != nil {
			t.Fatalf("ReloadIfChanged: %v", err)
		}
		if !findsByWord(ix, word, path) {
			t.Fatal("the pre-reload index pointer does not answer for the reloaded vault's new page")
		}
	})

	t.Run("old_pointer_forgets_removed_page", func(t *testing.T) {
		e1, dir := newTestEngine(t)
		ix := e1.Index()

		foreignIngestUniqueWord(t, dir, word)
		if _, err := e1.ReloadIfChanged(); err != nil {
			t.Fatalf("ReloadIfChanged (first): %v", err)
		}
		if !findsByWord(ix, word, path) {
			t.Fatal("precondition: the reloaded index should find the foreign page")
		}

		// A retract never removes a vault file, but its tombstone rewrites
		// the body — the unique word leaves the page, and the index must
		// follow even through the pointer the caller has been holding.
		e2 := foreignEngine(t, dir)
		if _, err := e2.OpenChangeset("retract the foreign page", testAuthor); err != nil {
			t.Fatalf("OpenChangeset (second engine): %v", err)
		}
		if _, err := e2.Append(Op{Kind: OpRetract, Path: path, Rationale: "gone"}); err != nil {
			t.Fatalf("Append retract (second engine): %v", err)
		}
		if _, err := e2.Commit("foreign retract"); err != nil {
			t.Fatalf("Commit (second engine): %v", err)
		}

		reloaded, err := e1.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged (second): %v", err)
		}
		if !reloaded {
			t.Fatal("ReloadIfChanged = false after a foreign retract, want true")
		}
		if findsByWord(ix, word, path) {
			t.Fatal("the pre-reload index pointer still returns a page whose body no longer contains the word")
		}
	})
}

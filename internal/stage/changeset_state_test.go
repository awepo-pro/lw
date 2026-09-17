// changeset_state_test.go pins Engine.ChangesetState (005 contract §6, as
// amended by R-509): the three state directories are the only oracle for a
// changeset's state word, the lookup matches ErrNoChangeset for an id none
// of them holds, and — the property the ask pane's title depends on — it
// never disturbs the open changeset, no matter what else is calling
// Current at the same time.
package stage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestChangesetState pins the read-only state lookup the ask pane's title
// uses: one stat per state directory, the directory's name as the word.
func TestChangesetState(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		e, _ := newTestEngine(t)
		cs, err := e.OpenChangeset("still under review", testAuthor)
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		got, err := e.ChangesetState(cs.ID)
		if err != nil {
			t.Fatalf("ChangesetState(%s): %v", cs.ID, err)
		}
		if got != "open" {
			t.Fatalf("ChangesetState(%s) = %q, want \"open\"", cs.ID, got)
		}
	})

	t.Run("committed", func(t *testing.T) {
		e, _ := newTestEngine(t)
		cs, err := e.OpenChangeset("accepted by review", testAuthor)
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		// Commit refuses a changeset with no live op (008 ErrNothingToCommit,
		// C-802), so the harness stages one valid op first.
		stageKVCachePatch(t, e)
		if _, err := e.Commit("test commit"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		got, err := e.ChangesetState(cs.ID)
		if err != nil {
			t.Fatalf("ChangesetState(%s): %v", cs.ID, err)
		}
		if got != "committed" {
			t.Fatalf("ChangesetState(%s) = %q, want \"committed\"", cs.ID, got)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		e, _ := newTestEngine(t)
		cs, err := e.OpenChangeset("turned down by review", testAuthor)
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		if err := e.Reject("test reject"); err != nil {
			t.Fatalf("Reject: %v", err)
		}
		got, err := e.ChangesetState(cs.ID)
		if err != nil {
			t.Fatalf("ChangesetState(%s): %v", cs.ID, err)
		}
		if got != "rejected" {
			t.Fatalf("ChangesetState(%s) = %q, want \"rejected\"", cs.ID, got)
		}
	})

	t.Run("unknown_id_is_ErrNoChangeset", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("something is open", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		_, err := e.ChangesetState("cs-doesnotexist")
		if !errors.Is(err, ErrNoChangeset) {
			t.Fatalf("ChangesetState of an unknown id = %v, want ErrNoChangeset", err)
		}
	})

	// does_not_disturb_the_open_changeset is the guarantee that lets the ask
	// pane call this from a title lookup while a turn runs: asking about one
	// id leaves the open changeset exactly as Current and Append left it.
	t.Run("does_not_disturb_the_open_changeset", func(t *testing.T) {
		e, _ := newTestEngine(t)
		cs, err := e.OpenChangeset("mid-review", testAuthor)
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		if _, err := e.ChangesetState("cs-doesnotexist"); !errors.Is(err, ErrNoChangeset) {
			t.Fatalf("precondition: ChangesetState of an unknown id = %v, want ErrNoChangeset", err)
		}

		again, err := e.Current()
		if err != nil {
			t.Fatalf("Current after a lookup: %v", err)
		}
		if again.ID != cs.ID {
			t.Fatalf("Current after a lookup names %s, want the open %s", again.ID, cs.ID)
		}

		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		const oldLine = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
		const newLine = "- [[flash-attention]] — a kernel design that reduces the bandwidth cost"
		if !strings.Contains(page.Body, oldLine) {
			t.Fatalf("fixture body does not contain the hunk's old line:\n%s", page.Body)
		}
		rewritten := *page
		rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)
		if _, err := e.Append(Op{
			Kind:      OpPatchPage,
			Path:      page.Path,
			Section:   "## Related",
			Before:    page.SHA256(),
			Content:   rewritten.Serialize(),
			Rationale: "a following Append still works after a lookup",
			Hunks: []Hunk{{
				ID:   "h1",
				Path: page.Path,
				Del:  []string{oldLine},
				Add:  []string{newLine},
			}},
		}); err != nil {
			t.Fatalf("Append after a lookup: %v", err)
		}

		after, err := e.Current()
		if err != nil {
			t.Fatalf("Current after an Append: %v", err)
		}
		if after.ID != cs.ID || len(after.Live()) != 1 {
			t.Fatalf("open changeset after a lookup and an Append = %s with %d live ops, want %s with 1",
				after.ID, len(after.Live()), cs.ID)
		}
	})

	// concurrent_with_current_is_race_free is the property the contract
	// amendment names: the lookup runs while Current runs — in production,
	// from a tea.Cmd goroutine while the turn goroutine and Update call
	// Current — and must never share a word of engine state with it.
	// Current's open/nextOp writes are openMu-guarded since 008 A-802,
	// but the lookup reads none of them anyway; this subtest exists so
	// `-race` proves that.
	t.Run("concurrent_with_current_is_race_free", func(t *testing.T) {
		e, _ := newTestEngine(t)
		cs, err := e.OpenChangeset("watched from a lookup", testAuthor)
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		const rounds = 64
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if got, err := e.ChangesetState(cs.ID); err != nil || got != "open" {
					t.Errorf("ChangesetState(%s) = %q, %v; want \"open\", nil", cs.ID, got, err)
					return
				}
			}
		}()
		for i := 0; i < rounds; i++ {
			got, err := e.Current()
			if err != nil || got.ID != cs.ID {
				t.Fatalf("Current = %v, %v; want %s, nil", got, err, cs.ID)
			}
		}
		wg.Wait()
	})
}

// TestChangesetStateNeverLeavesTheStateDirs pins the guard the lookup's
// stats depend on: id is joined straight onto each state directory's path,
// so a name carrying a separator — or "." / "..", which stat the container
// directories themselves — must be refused with ErrNoChangeset rather than
// answered from whatever directory the escaped path happens to name.
func TestChangesetStateNeverLeavesTheStateDirs(t *testing.T) {
	e, root := newTestEngine(t)
	if _, err := e.OpenChangeset("the lookup's guard", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	// A real directory outside .llmwiki/changesets/ that a traversing id
	// would otherwise reach and answer from.
	outside := filepath.Join(root, "outside-target")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", outside, err)
	}

	for _, id := range []string{"", ".", "..", "../../../outside-target", "cs-x/../cs-y", "/etc"} {
		got, err := e.ChangesetState(id)
		if !errors.Is(err, ErrNoChangeset) {
			t.Fatalf("ChangesetState(%q) = %q, %v; want an ErrNoChangeset refusal, not an answer from outside the state dirs", id, got, err)
		}
	}
}

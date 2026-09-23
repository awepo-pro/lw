package stage

import (
	"errors"
	"strings"
	"testing"
)

// TestOpenOrJoin pins workflow 019's frozen Engine contract: OpenOrJoin
// opens like OpenChangeset when none is open, and JOINS the open changeset
// when one is — appending " + <intent>" (bounded to 200 runes, overflow
// ending in "…"), journaling changeset_joined, and leaving the author
// unchanged — while the strict verb OpenChangeset keeps refusing with
// ErrOpenChangeset.
func TestOpenOrJoin(t *testing.T) {
	t.Run("none_open", func(t *testing.T) {
		e, _ := newTestEngine(t)

		cs, joined, err := e.OpenOrJoin("first intent", testAuthor)
		if err != nil {
			t.Fatalf("OpenOrJoin on an empty changesets/open/: %v", err)
		}
		if joined {
			t.Error("joined = true, want false — nothing was open")
		}
		if cs.Intent != "first intent" {
			t.Errorf("Intent = %q, want the verbatim %q", cs.Intent, "first intent")
		}
	})

	t.Run("one_open", func(t *testing.T) {
		e, _ := newTestEngine(t)

		first, err := e.OpenChangeset("first", testAuthor)
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		cs, joined, err := e.OpenOrJoin("second", testAuthor)
		if err != nil {
			t.Fatalf("OpenOrJoin with one open: %v", err)
		}
		if !joined {
			t.Error("joined = false, want true — a changeset was open")
		}
		if cs.ID != first.ID {
			t.Errorf("joined changeset id = %q, want the open one %q", cs.ID, first.ID)
		}
		if cs.Intent != "first + second" {
			t.Errorf("Intent = %q, want %q", cs.Intent, "first + second")
		}
		if cs.Author != first.Author {
			t.Errorf("Author = %+v, want the open changeset's %+v unchanged", cs.Author, first.Author)
		}

		joined_events, err := e.journal.Query(Filter{Kinds: []EventKind{EvChangesetJoined}})
		if err != nil {
			t.Fatalf("journal query: %v", err)
		}
		if len(joined_events) != 1 {
			t.Fatalf("changeset_joined events = %d, want exactly 1", len(joined_events))
		}
		if joined_events[0].Changeset != first.ID {
			t.Errorf("event changeset = %q, want %q", joined_events[0].Changeset, first.ID)
		}
	})

	t.Run("bounded", func(t *testing.T) {
		e, _ := newTestEngine(t)

		if _, err := e.OpenChangeset("x", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		cs, joined, err := e.OpenOrJoin(strings.Repeat("y", 300), testAuthor)
		if err != nil {
			t.Fatalf("OpenOrJoin: %v", err)
		}
		if !joined {
			t.Fatal("joined = false, want true")
		}
		got := []rune(cs.Intent)
		if len(got) != 200 {
			t.Fatalf("Intent is %d runes, want exactly 200", len(got))
		}
		if got[len(got)-1] != '…' {
			t.Errorf("Intent ends in %q, want the overflow marker %q", string(got[len(got)-1]), "…")
		}
	})

	t.Run("joined_intent_persisted", func(t *testing.T) {
		e, dir := newTestEngine(t)

		if _, err := e.OpenChangeset("first", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		if _, _, err := e.OpenOrJoin("second", testAuthor); err != nil {
			t.Fatalf("OpenOrJoin: %v", err)
		}

		// A SECOND engine over the same vault must read the joined intent
		// back: the join persisted before it returned.
		e2, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("reopen engine: %v", err)
		}
		defer e2.Close()
		cs, err := e2.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if cs.Intent != "first + second" {
			t.Errorf("reloaded Intent = %q, want %q (the join must persist)", cs.Intent, "first + second")
		}
	})

	t.Run("strict_verb_unchanged", func(t *testing.T) {
		e, _ := newTestEngine(t)

		if _, err := e.OpenChangeset("first", testAuthor); err != nil {
			t.Fatalf("first OpenChangeset: %v", err)
		}
		if _, err := e.OpenChangeset("second", testAuthor); !errors.Is(err, ErrOpenChangeset) {
			t.Errorf("second OpenChangeset = %v, want ErrOpenChangeset (the strict verb is unchanged)", err)
		}
	})
}

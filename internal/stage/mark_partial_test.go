package stage

// mark_partial_test.go holds 061's frozen contract for Engine.MarkPartial: a
// turn that failed AFTER staging keeps its ops, and the changeset says so in
// the one place a reviewer already looks — its Intent. The mark is a plain
// rewrite of that field: no journal event (so no new event kind and no
// vault-format change), the same 200-rune bound a join gets, and an error —
// never a changeset conjured from nothing — when none is open.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// journalLineCount is the number of lines in the vault's journal file; a
// missing file is zero.
func journalLineCount(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".llmwiki", "journal.ndjson"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return strings.Count(string(b), "\n")
}

// TestMarkPartialAppendsIntent: the note lands in brackets after the intent,
// survives a fresh Engine reading the directory, and the journal does not
// grow by a line.
func TestMarkPartialAppendsIntent(t *testing.T) {
	e, dir := newTestEngine(t)
	if _, err := e.OpenChangeset("ingest a.md", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	before := journalLineCount(t, dir)

	if err := e.MarkPartial("partial: x"); err != nil {
		t.Fatalf("MarkPartial: %v", err)
	}

	const want = "ingest a.md [partial: x]"
	cur, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur.Intent != want {
		t.Errorf("Intent = %q, want %q", cur.Intent, want)
	}

	e2, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("reopen engine: %v", err)
	}
	defer e2.Close()
	reloaded, err := e2.Current()
	if err != nil {
		t.Fatalf("Current on a fresh engine: %v", err)
	}
	if reloaded.Intent != want {
		t.Errorf("reloaded Intent = %q, want %q (the mark must persist)", reloaded.Intent, want)
	}

	if after := journalLineCount(t, dir); after != before {
		t.Errorf("journal has %d line(s), want the %d it had — MarkPartial journals nothing", after, before)
	}
}

// TestMarkPartialBounded: the whole intent stays within 200 runes, the cut
// marked by a trailing "…" — the rule a join gets, applied to the same field.
func TestMarkPartialBounded(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset(strings.Repeat("y", 199), testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	if err := e.MarkPartial("partial: x"); err != nil {
		t.Fatalf("MarkPartial: %v", err)
	}

	cur, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	got := []rune(cur.Intent)
	if len(got) != 200 {
		t.Fatalf("Intent is %d runes, want exactly 200", len(got))
	}
	if got[len(got)-1] != '…' {
		t.Errorf("Intent ends in %q, want the overflow marker %q", string(got[len(got)-1]), "…")
	}
}

// TestMarkPartialNoOpen: with nothing open there is nothing to mark.
func TestMarkPartialNoOpen(t *testing.T) {
	e, _ := newTestEngine(t)

	err := e.MarkPartial("partial: x")
	if err == nil {
		t.Fatal("MarkPartial with no open changeset returned nil")
	}
	if got, want := err.Error(), "stage: mark partial: no open changeset"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// TestMarkPartialEmptyIntent: a changeset with no intent gets the bare mark,
// not a leading space.
func TestMarkPartialEmptyIntent(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	if err := e.MarkPartial("partial: x"); err != nil {
		t.Fatalf("MarkPartial: %v", err)
	}
	cur, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur.Intent != "[partial: x]" {
		t.Errorf("Intent = %q, want %q", cur.Intent, "[partial: x]")
	}
}

// TestMarkPartialKeepsOps: the mark rewrites the intent and nothing else —
// every op, its state and its hunks come through untouched, so the review
// that follows sees exactly the work the failed turn staged.
func TestMarkPartialKeepsOps(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("ingest a.md", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	opID := stageKVCachePatch(t, e)
	before, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}

	if err := e.MarkPartial("partial: the agent turn failed (provider 502) after staging 1 op(s)"); err != nil {
		t.Fatalf("MarkPartial: %v", err)
	}

	after, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(after.Ops) != 1 || after.Ops[0].ID != opID || after.Ops[0].State != before.Ops[0].State {
		t.Errorf("ops after = %+v, want the one op %s untouched", after.Ops, opID)
	}
	if after.ID != before.ID || after.Author != before.Author {
		t.Errorf("id/author changed: %s/%+v -> %s/%+v", before.ID, before.Author, after.ID, after.Author)
	}
	if !strings.HasSuffix(after.Intent, "[partial: the agent turn failed (provider 502) after staging 1 op(s)]") {
		t.Errorf("Intent = %q, want it to end with the bracketed note", after.Intent)
	}
}

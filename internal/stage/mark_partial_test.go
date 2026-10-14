package stage

// mark_partial_test.go holds 061's frozen contract for Engine.MarkPartial: a
// turn that failed AFTER staging keeps its ops, and the changeset says so in
// the one place a reviewer already looks — its Intent. The mark is a plain
// rewrite of that field: no journal event (so no new event kind and no
// vault-format change), and an error — never a changeset conjured from
// nothing — when none is open.
//
// FIX1 A-061-1 moved the mark from the END of the intent to the START
// (`[partial: …] ingest a.md`): the 200-rune bound (a join's, boundIntent)
// cuts an intent from its end, and a mark at the end was the first thing a
// multi-source or URL intent lost — the very surfaces that show an intent's
// start (the Review badge, `lw status`, the Changeset panel) now always see it.
// The bound applies to the original intent only.

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

	const want = "[partial: x] ingest a.md" // FIX1: was "ingest a.md [partial: x]"
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

// TestMarkPartialBounded: the 200-rune bound applies to the ORIGINAL intent
// only, so the mark is always whole and first. FIX1 amendment: it was "a
// 199-rune intent → 200 runes ending in …" (the whole intent bounded, the
// mark cut off the end). An original within the bound is kept untouched — a
// 199-rune one has nothing to cut — and a longer one ends in "…" after 199
// of its runes, the mark in front of it either way.
func TestMarkPartialBounded(t *testing.T) {
	const mark = "[partial: x] "
	cases := []struct {
		name     string
		original string
		want     string
	}{
		{"199 runes kept whole", strings.Repeat("y", 199), mark + strings.Repeat("y", 199)},
		{"200 runes kept whole", strings.Repeat("y", 200), mark + strings.Repeat("y", 200)},
		{"201 runes bounded", strings.Repeat("y", 201), mark + strings.Repeat("y", 199) + "…"},
		{"300 runes bounded", strings.Repeat("y", 300), mark + strings.Repeat("y", 199) + "…"},
		{"multibyte original bounded by runes", strings.Repeat("四", 250), mark + strings.Repeat("四", 199) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newTestEngine(t)
			if _, err := e.OpenChangeset(tc.original, testAuthor); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			if err := e.MarkPartial("partial: x"); err != nil {
				t.Fatalf("MarkPartial: %v", err)
			}
			cur, err := e.Current()
			if err != nil {
				t.Fatalf("Current: %v", err)
			}
			if cur.Intent != tc.want {
				t.Errorf("Intent = %.60q… (%d runes), want %.60q… (%d runes)",
					cur.Intent, len([]rune(cur.Intent)), tc.want, len([]rune(tc.want)))
			}
			if !strings.HasPrefix(cur.Intent, mark) {
				t.Errorf("Intent %.40q… does not start with the whole mark", cur.Intent)
			}
		})
	}
}

// TestMarkPartialSurvivesJoin: a later join bounds the whole intent from its
// END (joinIntents), and the mark sits at the start — a verb that joins a
// partial changeset cannot cut it off.
func TestMarkPartialSurvivesJoin(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("ingest a.md", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if err := e.MarkPartial("partial: provider 502 after 2 op(s)"); err != nil {
		t.Fatalf("MarkPartial: %v", err)
	}

	cs, joined, err := e.OpenOrJoin(strings.Repeat("z", 400), testAuthor)
	if err != nil || !joined {
		t.Fatalf("OpenOrJoin = %v, joined %v", err, joined)
	}
	if want := "[partial: provider 502 after 2 op(s)] ingest a.md + "; !strings.HasPrefix(cs.Intent, want) {
		t.Errorf("Intent = %.80q…, want it to still start with %q", cs.Intent, want)
	}
	if n := len([]rune(cs.Intent)); n != 200 {
		t.Errorf("joined Intent is %d runes, want the join's own 200-rune bound", n)
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
	// FIX1: a prefix (it was: ended with the bracketed note).
	if want := "[partial: the agent turn failed (provider 502) after staging 1 op(s)] ingest a.md"; after.Intent != want {
		t.Errorf("Intent = %q, want %q", after.Intent, want)
	}
}

// TestMarkPartialReplacesLeadingMark (FIX2 R6): a second failed turn into the
// same changeset moves the mark, it does not stack another in front. Stacked,
// every failure added forty characters to the front of the intent and the
// reviewer read a history of failures where one fact — "this changeset is
// incomplete" — was meant.
func TestMarkPartialReplacesLeadingMark(t *testing.T) {
	cases := []struct {
		name   string
		intent string
		want   string
	}{
		{"one mark", "[partial: provider 502 after 2 op(s)] ingest a.md", "[partial: stalled after 1 op(s)] ingest a.md"},
		{"mark and a join", "[partial: provider 502 after 2 op(s)] ingest a.md + ingest b.md", "[partial: stalled after 1 op(s)] ingest a.md + ingest b.md"},
		{"mark only", "[partial: provider 502 after 2 op(s)]", "[partial: stalled after 1 op(s)]"},
		{"not a mark in front", "ingest a.md [partial: x]", "[partial: stalled after 1 op(s)] ingest a.md [partial: x]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newTestEngine(t)
			if _, err := e.OpenChangeset(tc.intent, testAuthor); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			if err := e.MarkPartial("partial: stalled after 1 op(s)"); err != nil {
				t.Fatalf("MarkPartial: %v", err)
			}
			cur, err := e.Current()
			if err != nil {
				t.Fatalf("Current: %v", err)
			}
			if cur.Intent != tc.want {
				t.Errorf("Intent = %q, want %q", cur.Intent, tc.want)
			}
		})
	}

	t.Run("twice", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("ingest a.md", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		for _, note := range []string{"partial: provider 502 after 2 op(s)", "partial: provider 429 after 3 op(s)"} {
			if err := e.MarkPartial(note); err != nil {
				t.Fatalf("MarkPartial: %v", err)
			}
		}
		cur, _ := e.Current()
		if want := "[partial: provider 429 after 3 op(s)] ingest a.md"; cur.Intent != want {
			t.Errorf("Intent = %q, want %q", cur.Intent, want)
		}
	})
}

// TestWithoutPartialMark: the one parse of what MarkPartial writes.
func TestWithoutPartialMark(t *testing.T) {
	cases := []struct {
		in         string
		wantRest   string
		wantMarked bool
	}{
		{"[partial: provider 502 after 2 op(s)] ingest a.md", "ingest a.md", true},
		{"[partial: x]", "", true},
		{"[partial: x] ", "", true},
		{"[partial] ingest a.md", "ingest a.md", true},
		{"[partial: x]ingest a.md", "ingest a.md", true},
		{"ingest a.md", "ingest a.md", false},
		{"ingest a.md [partial: x]", "ingest a.md [partial: x]", false},
		{"[partial: never closed", "[partial: never closed", false},
		{"[wip] ingest a.md", "[wip] ingest a.md", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			rest, marked := WithoutPartialMark(tc.in)
			if rest != tc.wantRest || marked != tc.wantMarked {
				t.Errorf("WithoutPartialMark(%q) = (%q, %v), want (%q, %v)", tc.in, rest, marked, tc.wantRest, tc.wantMarked)
			}
		})
	}
}

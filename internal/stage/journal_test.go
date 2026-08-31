package stage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTestJournal opens a fresh Journal backed by a file under t.TempDir().
func newTestJournal(t *testing.T) *Journal {
	t.Helper()
	dir := t.TempDir()
	j, err := OpenJournal(filepath.Join(dir, "journal.ndjson"))
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	return j
}

// equalStrings compares two string slices for equality, treating nil and
// an empty (non-nil) slice as equal — this package writes no test
// dependency (00-conventions.md §4), so this is the three-line comparison
// in place of a library.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestJournalEmptyQuery proves a freshly opened, never-appended journal
// queries cleanly rather than erroring on a zero-byte file.
func TestJournalEmptyQuery(t *testing.T) {
	j := newTestJournal(t)
	got, err := j.Query(Filter{})
	if err != nil {
		t.Fatalf("Query on empty journal: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Query on empty journal = %v, want empty", got)
	}
}

// TestJournalAppendMany is the stage file's headline test: the journal
// must survive 10 000 appends with no corruption, and Query must return
// all of them, still in file order (oldest first).
func TestJournalAppendMany(t *testing.T) {
	j := newTestJournal(t)

	const n = 10000
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		e := Event{
			TS:    base.Add(time.Duration(i) * time.Second),
			Kind:  EvOpProposed,
			Op:    fmt.Sprintf("op%d", i),
			Actor: Author{Kind: "agent"},
		}
		if err := j.Append(e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	got, err := j.Query(Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != n {
		t.Fatalf("Query after %d appends returned %d events, want %d", n, len(got), n)
	}
	for i, e := range got {
		want := fmt.Sprintf("op%d", i)
		if e.Op != want {
			t.Fatalf("event %d has Op %q, want %q (oldest-first file order)", i, e.Op, want)
		}
	}
}

// queryFilterFixture appends six distinguishable events covering every
// Filter field and returns the journal plus the events, in append order.
func queryFilterFixture(t *testing.T) (*Journal, []Event) {
	t.Helper()
	j := newTestJournal(t)

	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	events := []Event{
		{TS: base, Kind: EvChangesetOpened, Changeset: "cs-1", Actor: Author{Kind: "human"}, Message: "e0"},
		{TS: base.Add(1 * time.Minute), Kind: EvOpProposed, Changeset: "cs-1", Op: "op1", Paths: []string{"wiki/a.md"}, Actor: Author{Kind: "agent"}, Message: "e1"},
		{TS: base.Add(2 * time.Minute), Kind: EvOpProposed, Changeset: "cs-2", Op: "op1", Paths: []string{"wiki/b.md"}, Actor: Author{Kind: "agent"}, Message: "e2"},
		{TS: base.Add(3 * time.Minute), Kind: EvHunkDropped, Changeset: "cs-1", Op: "op1", Hunk: "h1", Paths: []string{"wiki/a.md"}, Actor: Author{Kind: "human"}, Message: "e3"},
		{TS: base.Add(4 * time.Minute), Kind: EvCommitBegin, Changeset: "cs-1", Commit: "000001", Paths: []string{"wiki/a.md", "wiki/c.md"}, Actor: Author{Kind: "agent"}, Message: "e4"},
		{TS: base.Add(5 * time.Minute), Kind: EvCommitEnd, Changeset: "cs-1", Commit: "000001", Actor: Author{Kind: "agent"}, Message: "e5"},
	}
	for _, e := range events {
		if err := j.Append(e); err != nil {
			t.Fatalf("append %s: %v", e.Message, err)
		}
	}
	return j, events
}

// TestQueryFilters exercises every Filter field in isolation, and all of
// them set at once (backbone §5.7, MASTER §9 D-AU).
func TestQueryFilters(t *testing.T) {
	j, events := queryFilterFixture(t)
	t1, t2, t3 := events[1].TS, events[2].TS, events[3].TS

	tests := []struct {
		name string
		f    Filter
		want []string // Message values of the matching events, in order
	}{
		{"zero Filter matches everything", Filter{}, []string{"e0", "e1", "e2", "e3", "e4", "e5"}},
		{"Kinds single", Filter{Kinds: []EventKind{EvOpProposed}}, []string{"e1", "e2"}},
		{"Kinds is membership over several", Filter{Kinds: []EventKind{EvCommitBegin, EvCommitEnd}}, []string{"e4", "e5"}},
		{"Changeset", Filter{Changeset: "cs-2"}, []string{"e2"}},
		{"Path matches when Paths contains it", Filter{Path: "wiki/a.md"}, []string{"e1", "e3", "e4"}},
		{"Path with no match", Filter{Path: "wiki/does-not-exist.md"}, nil},
		{"ActorKind human", Filter{ActorKind: "human"}, []string{"e0", "e3"}},
		{"ActorKind agent", Filter{ActorKind: "agent"}, []string{"e1", "e2", "e4", "e5"}},
		{"Since is inclusive of the boundary record", Filter{Since: t3}, []string{"e3", "e4", "e5"}},
		{"Until is inclusive of the boundary record", Filter{Until: t2}, []string{"e0", "e1", "e2"}},
		{"Since and Until both pin exactly one record", Filter{Since: t2, Until: t2}, []string{"e2"}},
		{"every field set at once (AND)", Filter{
			Kinds:     []EventKind{EvOpProposed, EvHunkDropped},
			Changeset: "cs-1",
			Path:      "wiki/a.md",
			Since:     t1,
			Until:     t3,
		}, []string{"e1", "e3"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := j.Query(tt.f)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			gotMsgs := make([]string, len(got))
			for i, e := range got {
				gotMsgs[i] = e.Message
			}
			if !equalStrings(gotMsgs, tt.want) {
				t.Fatalf("Query(%+v) = %v, want %v", tt.f, gotMsgs, tt.want)
			}
		})
	}
}

// TestQueryLimit covers Limit at 0 (unlimited), 1, negative (nothing) and
// larger than the journal, and proves Limit selects the most recent N
// while still returning them oldest-first, and that Last(n) is exactly
// Query(Filter{Limit: n}) (backbone §5.7, MASTER §9 D-AU).
func TestQueryLimit(t *testing.T) {
	j := newTestJournal(t)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		e := Event{TS: base.Add(time.Duration(i) * time.Minute), Kind: EvOpProposed, Op: fmt.Sprintf("op%d", i), Actor: Author{Kind: "agent"}}
		if err := j.Append(e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	tests := []struct {
		name  string
		limit int
		want  []string
	}{
		{"zero is unlimited", 0, []string{"op0", "op1", "op2", "op3", "op4"}},
		{"one returns only the most recent", 1, []string{"op4"}},
		{"negative returns nothing", -1, nil},
		{"larger than the journal returns everything", 100, []string{"op0", "op1", "op2", "op3", "op4"}},
		{"three returns the three most recent, oldest-first", 3, []string{"op2", "op3", "op4"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := j.Query(Filter{Limit: tt.limit})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			gotOps := make([]string, len(got))
			for i, e := range got {
				gotOps[i] = e.Op
			}
			if !equalStrings(gotOps, tt.want) {
				t.Fatalf("Query(Filter{Limit: %d}) = %v, want %v", tt.limit, gotOps, tt.want)
			}
		})
	}

	last, err := j.Last(3)
	if err != nil {
		t.Fatalf("Last(3): %v", err)
	}
	query, err := j.Query(Filter{Limit: 3})
	if err != nil {
		t.Fatalf("Query(Filter{Limit: 3}): %v", err)
	}
	if len(last) != len(query) {
		t.Fatalf("Last(3) returned %d events, Query(Filter{Limit:3}) returned %d", len(last), len(query))
	}
	for i := range last {
		if last[i].Op != query[i].Op {
			t.Fatalf("Last(3)[%d].Op = %q, Query(Filter{Limit:3})[%d].Op = %q, want identical", i, last[i].Op, i, query[i].Op)
		}
	}
}

// TestJournalTruncatedTail simulates a crash mid-Append that leaves a
// partial final record with no terminating "\n" — the classic truncated
// tail — and proves the earlier, complete records still read back cleanly
// and the reader returns no error.
func TestJournalTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.ndjson")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 4; i++ {
		e := Event{TS: base.Add(time.Duration(i) * time.Minute), Kind: EvOpProposed, Op: fmt.Sprintf("op%d", i), Actor: Author{Kind: "agent"}}
		if err := j.Append(e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	const chop = 15
	if len(content) < chop+10 {
		t.Fatalf("journal too short (%d bytes) to truncate meaningfully", len(content))
	}
	// Chop the trailing "\n" and the tail of the fourth record, simulating
	// a crash mid-write of the LAST line.
	if err := os.Truncate(path, int64(len(content)-chop)); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	got, err := j.Query(Filter{})
	if err != nil {
		t.Fatalf("Query after truncated tail: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Query after truncated tail returned %d events, want 3 (op1..op3 survive)", len(got))
	}
	for i, e := range got {
		want := fmt.Sprintf("op%d", i+1)
		if e.Op != want {
			t.Fatalf("event %d = %q, want %q", i, e.Op, want)
		}
	}
}

// TestJournalCorruptedMiddleLine reproduces D-AV's exact failure mode: a
// crash mid-Append leaves a partial record with no "\n", and the next
// Append opens O_APPEND and writes immediately after it, splicing both
// into one unparsable line in the MIDDLE of the file — not the tail —
// with valid history on both sides.
func TestJournalCorruptedMiddleLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.ndjson")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 3; i++ {
		e := Event{TS: base.Add(time.Duration(i) * time.Minute), Kind: EvOpProposed, Op: fmt.Sprintf("op%d", i), Actor: Author{Kind: "agent"}}
		if err := j.Append(e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// Simulate the crash: a partial record, written directly (bypassing
	// Append, which always completes its single Write), with no trailing
	// "\n".
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for simulated crash write: %v", err)
	}
	if _, err := f.WriteString(`{"ts":"2026-01-01T00:03:30Z","kind":"op_pr`); err != nil {
		t.Fatalf("simulated crash write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close simulated crash write: %v", err)
	}

	// The process restarts and appends normally. This call's O_APPEND
	// write lands right after the partial bytes above, splicing them into
	// a single unparsable line — the damage this test targets.
	if err := j.Append(Event{TS: base.Add(4 * time.Minute), Kind: EvOpProposed, Op: "op4", Actor: Author{Kind: "agent"}}); err != nil {
		t.Fatalf("append op4: %v", err)
	}
	if err := j.Append(Event{TS: base.Add(5 * time.Minute), Kind: EvOpProposed, Op: "op5", Actor: Author{Kind: "agent"}}); err != nil {
		t.Fatalf("append op5: %v", err)
	}

	got, err := j.Query(Filter{})
	if err != nil {
		t.Fatalf("Query over a mid-file splice: %v", err)
	}
	want := []string{"op1", "op2", "op3", "op5"} // op4's own record was swallowed into the splice
	gotOps := make([]string, len(got))
	for i, e := range got {
		gotOps[i] = e.Op
	}
	if !equalStrings(gotOps, want) {
		t.Fatalf("Query over a mid-file splice = %v, want %v (op1..op3 and op5 survive on either side)", gotOps, want)
	}
}

// TestJournalConcurrentAppend appends from several goroutines at once and
// proves the result is exactly N parsable lines — no interleaving,
// because Append writes each record in exactly one Write call.
func TestJournalConcurrentAppend(t *testing.T) {
	j := newTestJournal(t)

	const goroutines = 50
	const perGoroutine = 20
	const total = goroutines * perGoroutine

	var wg sync.WaitGroup
	errs := make(chan error, total)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				e := Event{
					TS:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
					Kind:  EvOpProposed,
					Op:    fmt.Sprintf("g%d-op%d", g, i),
					Actor: Author{Kind: "agent"},
				}
				if err := j.Append(e); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Append: %v", err)
	}

	got, err := j.Query(Filter{})
	if err != nil {
		t.Fatalf("Query after concurrent appends: %v", err)
	}
	if len(got) != total {
		t.Fatalf("Query after %d concurrent appends returned %d events, want %d (no interleaving corruption)", total, len(got), total)
	}

	seen := make(map[string]bool, total)
	for _, e := range got {
		if seen[e.Op] {
			t.Fatalf("duplicate event %q returned by Query", e.Op)
		}
		seen[e.Op] = true
	}
}

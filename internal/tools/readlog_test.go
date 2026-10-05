package tools

import (
	"reflect"
	"sync"
	"testing"
)

// TestReadLogCoverage is the 040 frozen unit: ingest raw/a.md with three
// chunks; reads of 1 and 3 leave [2] unread; a read of 2 leaves none; and a
// source that was only ever read — never ingested through this log — is not
// tracked at all.
func TestReadLogCoverage(t *testing.T) {
	l := newReadLog()
	l.noteIngest("raw/a.md", 3)
	l.noteRead("raw/a.md", 1)
	l.noteRead("raw/a.md", 3)
	if got, want := l.unread(nil), []unreadSource{{Path: "raw/a.md", Chunks: []int{2}, N: 3}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unread after reads 1 and 3 = %+v, want %+v", got, want)
	}

	l.noteRead("raw/a.md", 2)
	if got := l.unread(nil); len(got) != 0 {
		t.Fatalf("unread after reading all three = %+v, want none", got)
	}

	// Read, never ingested here: nothing to be unread.
	l.noteRead("raw/other.md", 1)
	if got := l.unread(nil); len(got) != 0 {
		t.Fatalf("a source only read was tracked: %+v", got)
	}
}

// TestReadLogUnreadSortedAndFiltered: sources come out sorted by path with
// ascending chunk lists, and the live filter drops a source it rejects.
func TestReadLogUnreadSortedAndFiltered(t *testing.T) {
	l := newReadLog()
	l.noteIngest("raw/zeta.md", 2)
	l.noteIngest("raw/alpha.md", 3)
	l.noteIngest("raw/gone.md", 2)
	l.noteRead("raw/alpha.md", 2)

	got := l.unread(func(p string) bool { return p != "raw/gone.md" })
	want := []unreadSource{
		{Path: "raw/alpha.md", Chunks: []int{1, 3}, N: 3},
		{Path: "raw/zeta.md", Chunks: []int{1, 2}, N: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unread = %+v, want %+v", got, want)
	}
}

// TestReadLogReingestResetsReads: a path ingested again is a new body — the
// reads of the old one say nothing about it.
func TestReadLogReingestResetsReads(t *testing.T) {
	l := newReadLog()
	l.noteIngest("raw/a.md", 2)
	l.noteRead("raw/a.md", 1)
	l.noteRead("raw/a.md", 2)
	l.noteIngest("raw/a.md", 3)
	if got, want := l.unread(nil), []unreadSource{{Path: "raw/a.md", Chunks: []int{1, 2, 3}, N: 3}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unread after a re-ingest = %+v, want %+v", got, want)
	}
}

// TestReadLogVerdictRefusesOncePerSet pins the verdict state machine on its
// own, without a registry: refuse a new non-empty set once, let the same set
// through, refuse a changed set, and forget the refusal once nothing is
// unread (so a later identical set is a fresh one).
func TestReadLogVerdictRefusesOncePerSet(t *testing.T) {
	l := newReadLog()
	l.noteIngest("raw/a.md", 3)
	step := func(name string, wantRefuse bool, wantUnread int) {
		t.Helper()
		got, refuse := l.closeVerdict(nil)
		if refuse != wantRefuse || len(got) != wantUnread {
			t.Fatalf("%s: refuse=%v unread=%d, want refuse=%v unread=%d", name, refuse, len(got), wantRefuse, wantUnread)
		}
	}
	step("first close, three unread", true, 1)
	step("same set again", false, 1)
	l.noteRead("raw/a.md", 1)
	step("set changed", true, 1)
	step("changed set again", false, 1)
	l.noteRead("raw/a.md", 2)
	l.noteRead("raw/a.md", 3)
	step("all read", false, 0)
	// The refusal is forgotten: a fresh ingest with the same shape is new.
	l.noteIngest("raw/a.md", 3)
	step("re-ingested, three unread again", true, 1)
}

// TestReadLogNilIsInert: a Deps built by hand (stageIngestSourceTool(Deps{}))
// has no log; every method must be a safe no-op rather than a nil panic.
func TestReadLogNilIsInert(t *testing.T) {
	var l *readLog
	l.noteIngest("raw/a.md", 3)
	l.noteRead("raw/a.md", 1)
	if got := l.unread(nil); len(got) != 0 {
		t.Errorf("nil log unread = %+v", got)
	}
	if got, refuse := l.closeVerdict(nil); refuse || len(got) != 0 {
		t.Errorf("nil log verdict = %+v, %v", got, refuse)
	}
}

// TestReadLogConcurrent: tools may run concurrently within a round, so reads,
// ingests and close verdicts race freely. Under -race this is the proof the
// mutex covers every path; the final state is also checked.
func TestReadLogConcurrent(t *testing.T) {
	l := newReadLog()
	const n = 64
	l.noteIngest("raw/a.md", n)
	var wg sync.WaitGroup
	for c := 1; c <= n; c++ {
		wg.Add(2)
		go func() { defer wg.Done(); l.noteRead("raw/a.md", c) }()
		go func() { defer wg.Done(); l.closeVerdict(nil) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); l.noteIngest("raw/b.md", 2) }()
	wg.Wait()

	got := l.unread(nil)
	want := []unreadSource{{Path: "raw/b.md", Chunks: []int{1, 2}, N: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unread after every chunk of a.md was read concurrently = %+v, want %+v", got, want)
	}
}

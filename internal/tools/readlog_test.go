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

// TestReadLogReingestClearsRefusal pins A-040-4: a re-ingested path is a new
// body, so a refusal the log remembers for the old one must not let the new
// one through. The set is rebuilt to the SAME unread chunks the refusal named
// — {2, 3} of 3 — which is exactly where a surviving "already refused this
// set" key would wave the close through.
func TestReadLogReingestClearsRefusal(t *testing.T) {
	l := newReadLog()
	l.noteIngest("raw/a.md", 3)
	l.noteRead("raw/a.md", 1)
	if _, refuse := l.closeVerdict(nil); !refuse {
		t.Fatal("first close over {2, 3} was not refused")
	}

	l.noteIngest("raw/a.md", 3) // the op was dropped and staged again
	l.noteRead("raw/a.md", 1)
	if got, refuse := l.closeVerdict(nil); !refuse || len(got) != 1 {
		t.Fatalf("close over the re-ingested {2, 3} = refuse %v, unread %+v; want a fresh refusal", refuse, got)
	}
	// A refusal is still once per set: the same set again goes through.
	if _, refuse := l.closeVerdict(nil); refuse {
		t.Error("the repeat close over the same set was refused a second time")
	}
}

// TestReadLogCloseVerdictPrunes pins A-040-5: a close prunes what the
// changeset being closed no longer holds. A registry outlives its changesets,
// so without pruning every source ever ingested in the process stays in the
// log, and each close re-filters them all, for as long as the process runs.
func TestReadLogCloseVerdictPrunes(t *testing.T) {
	l := newReadLog()
	l.noteIngest("raw/kept.md", 2)
	l.noteIngest("raw/gone.md", 3)
	l.noteRead("raw/gone.md", 1)

	live := func(p string) bool { return p == "raw/kept.md" }
	got, refuse := l.closeVerdict(live)
	if want := []unreadSource{{Path: "raw/kept.md", Chunks: []int{1, 2}, N: 2}}; !refuse || !reflect.DeepEqual(got, want) {
		t.Fatalf("closeVerdict = %+v, refuse %v; want only raw/kept.md", got, refuse)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ingested) != 1 || len(l.read) != 1 {
		t.Errorf("after the close the log holds %d ingested and %d read entries, want 1 and 1 (raw/gone.md pruned)", len(l.ingested), len(l.read))
	}
	if _, ok := l.ingested["raw/gone.md"]; ok {
		t.Error("raw/gone.md is still in the log after a close that did not find it live")
	}
}

// TestReadLogUnreadDoesNotPrune: unread is a read; only closeVerdict, the
// decision at the end of a changeset, forgets sources. A nil live keeps
// everything — the unit tests and any caller with no changeset to ask.
func TestReadLogUnreadDoesNotPrune(t *testing.T) {
	l := newReadLog()
	l.noteIngest("raw/a.md", 2)
	l.noteIngest("raw/b.md", 2)
	if got := l.unread(func(p string) bool { return p == "raw/a.md" }); len(got) != 1 {
		t.Fatalf("unread = %+v, want only raw/a.md", got)
	}
	if got := l.unread(nil); len(got) != 2 {
		t.Errorf("unread(nil) after a filtered unread = %+v, want both sources: unread must not prune", got)
	}
	if _, refuse := l.closeVerdict(nil); !refuse {
		t.Error("closeVerdict(nil) did not refuse over two unread sources")
	}
	if got := l.unread(nil); len(got) != 2 {
		t.Errorf("closeVerdict(nil) pruned: %+v; a nil live means every source is live", got)
	}
}

package vaultsync

// concurrent_test.go pins 042 A-042-9: a journal line appended by another lw
// process — the TUI staging an op, a second terminal — while sync carries a
// tail, fast-forwards, rebases or resets the work tree must survive, exactly
// once, and must never look like a checkout collision. The appender here is a
// goroutine calling the real stage.Journal.Append in a loop, so it takes
// whatever lock the production appenders take; Options.Quiesce is wired to the
// exclusive side of the same lock, as cmd/lw wires it. The lines that carry
// `//quiesce` are the wiring: a tree without the lock has no such option, and
// the same scenarios run there with the lines deleted — which is how the
// before/after numbers in the S3f report were measured.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
)

// concurrentIterations is how many times each scenario is run, each with a
// different pause between the appender's lines and a different head start, so
// the appends land at different points of the operation.
const concurrentIterations = 14

// appender appends numbered journal events through the real stage API.
type appender struct {
	j     *stage.Journal
	pause time.Duration
	stop  chan struct{}
	done  chan struct{}

	mu    sync.Mutex
	acked []int // numbers whose Append returned nil
	next  int
}

// startAppender starts appending events numbered from first, pausing between
// them.
func startAppender(t *testing.T, vault string, first int, pause time.Duration) *appender {
	t.Helper()
	j, err := stage.OpenJournal(filepath.Join(vault, ".llmwiki", "journal.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	a := &appender{j: j, pause: pause, stop: make(chan struct{}), done: make(chan struct{}), next: first}
	go func() {
		defer close(a.done)
		for {
			select {
			case <-a.stop:
				return
			default:
			}
			a.one()
			time.Sleep(a.pause)
		}
	}()
	return a
}

// one appends the next numbered event.
func (a *appender) one() {
	a.mu.Lock()
	n := a.next
	a.next++
	a.mu.Unlock()
	err := a.j.Append(stage.Event{
		TS: time.Now().UTC(), Kind: stage.EvChangesetOpened, Changeset: "cs-conc",
		Actor: stage.Author{Kind: "human"}, Message: fmt.Sprintf("conc-%06d", n),
	})
	if err == nil {
		a.mu.Lock()
		a.acked = append(a.acked, n)
		a.mu.Unlock()
	}
}

// Stop ends the appender and returns the numbers it appended successfully.
func (a *appender) Stop() []int {
	close(a.stop)
	<-a.done
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]int(nil), a.acked...)
}

var concLine = regexp.MustCompile(`conc-(\d{6})`)

// concOrder returns, for a journal's text, the numbers of the conc lines in
// file order.
func concOrder(text string) []int {
	var out []int
	for _, m := range concLine.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		out = append(out, n)
	}
	return out
}

// wantAll fails the test unless every acknowledged number is in text exactly
// once, and the numbers are in the order they were appended. It also fails on a
// lw-collision marker: a concurrent append must never look like a collision.
func wantAll(t *testing.T, vault, text string, acked []int, what string) {
	t.Helper()
	seen := map[int]int{}
	prev := -1
	for _, n := range concOrder(text) {
		seen[n]++
		if n < prev {
			t.Errorf("%s: line %d is out of order (after %d)", what, n, prev)
		}
		prev = n
	}
	var lost, dup []int
	for _, n := range acked {
		switch seen[n] {
		case 0:
			lost = append(lost, n)
		case 1:
		default:
			dup = append(dup, n)
		}
	}
	if len(lost) > 0 {
		t.Errorf("%s: %d of %d appended journal line(s) were LOST: %v", what, len(lost), len(acked), head(lost))
	}
	if len(dup) > 0 {
		t.Errorf("%s: %d journal line(s) appear twice: %v", what, len(dup), head(dup))
	}
	if _, err := os.Stat(filepath.Join(vault, ".git", "lw-collision")); err == nil {
		t.Errorf("%s: a false checkout collision was recorded:\n%s", what, readFile(t, filepath.Join(vault, ".git", "lw-collision")))
	}
}

func head(ns []int) []int {
	if len(ns) > 6 {
		return ns[:6]
	}
	return ns
}

// withQuiesce wires Options.Quiesce to the exclusive side of the journal lock,
// as cmd/lw does.
func withQuiesce(t *testing.T, o Options, vault string) Options {
	t.Helper()
	dir := filepath.Join(vault, ".llmwiki")
	if err := stage.EnsureJournalLock(dir); err != nil { //quiesce
		t.Fatal(err) //quiesce
	} //quiesce
	o.Quiesce = func() (func(), error) { return stage.QuiesceJournal(dir) } //quiesce
	return o
}

// concurrently runs scenario concurrentIterations times as subtests.
func concurrently(t *testing.T, scenario func(t *testing.T, i int)) {
	t.Helper()
	for i := 0; i < concurrentIterations; i++ {
		t.Run(fmt.Sprintf("i%02d", i), func(t *testing.T) { scenario(t, i) })
	}
}

// pauseFor and headStart vary with the iteration.
func pauseFor(i int) time.Duration  { return time.Duration(i%5+1) * 300 * time.Microsecond }
func headStart(i int) time.Duration { return time.Duration(i*3%30) * time.Millisecond }

// TestConcurrentAppendDuringPullCarry (H2): B has uncommitted journal lines,
// A pushed a commit that appended to the journal; B's fast-forward carries the
// tail across while a second lw process keeps appending.
func TestConcurrentAppendDuringPullCarry(t *testing.T) {
	concurrently(t, func(t *testing.T, i int) {
		hermetic(t)
		p := newPair(t)
		aPushes(t, p, "a-1\na-2\na-3\n", map[string]string{"wiki/from-a.md": "from a\n"})
		o := withQuiesce(t, appendOpts(p.b, p.remote), p.b)

		ap := startAppender(t, p.b, 1, pauseFor(i))
		time.Sleep(headStart(i))
		st, err := Pull(t.Context(), o, 1)
		acked := ap.Stop()
		if err != nil || st.Pulled != 1 {
			t.Fatalf("Pull = %+v, %v", st, err)
		}
		wantAll(t, p.b, readFile(t, filepath.Join(p.b, journalPath)), acked, "after the carry")
		if len(acked) == 0 {
			t.Fatal("the appender never appended")
		}
	})
}

// TestConcurrentAppendDuringRebase (H1, H2): the same through a rebase.
func TestConcurrentAppendDuringRebase(t *testing.T) {
	concurrently(t, func(t *testing.T, i int) {
		hermetic(t)
		p := newPair(t)
		aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
		bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
		o := withQuiesce(t, appendOpts(p.b, p.remote), p.b)

		ap := startAppender(t, p.b, 1, pauseFor(i))
		time.Sleep(headStart(i))
		st, err := Pull(t.Context(), o, 1)
		acked := ap.Stop()
		if err != nil || st.Rebased != 1 {
			t.Fatalf("Pull = %+v, %v", st, err)
		}
		wantAll(t, p.b, readFile(t, filepath.Join(p.b, journalPath)), acked, "after the rebase")
		if readFile(t, filepath.Join(p.b, "notes/from-a.md")) == "" || readFile(t, filepath.Join(p.b, "notes/from-b.md")) == "" {
			t.Error("a note is missing after the rebase")
		}
	})
}

// TestConcurrentAppendDuringRefusedRebase: a conflict aborts the rebase; lines
// appended meanwhile are still there.
func TestConcurrentAppendDuringRefusedRebase(t *testing.T) {
	concurrently(t, func(t *testing.T, i int) {
		hermetic(t)
		p := newPair(t)
		aCommits(t, p, map[string]string{"wiki/alpha.md": "A's version\n"}, "lw sync")
		bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
		o := withQuiesce(t, appendOpts(p.b, p.remote), p.b)

		ap := startAppender(t, p.b, 1, pauseFor(i))
		time.Sleep(headStart(i))
		_, err := Pull(t.Context(), o, 1)
		acked := ap.Stop()
		if !errors.Is(err, ErrDiverged) {
			t.Fatalf("Pull err = %v, want ErrDiverged", err)
		}
		wantAll(t, p.b, readFile(t, filepath.Join(p.b, journalPath)), acked, "after the refused rebase")
	})
}

// TestConcurrentAppendDuringTakeRemote: take-remote saves the work tree onto a
// backup branch and resets to the remote. A line appended meanwhile ends up
// either on the backup branch (it was there when the save was made) or in the
// new work tree (it came after) — never in neither.
func TestConcurrentAppendDuringTakeRemote(t *testing.T) {
	concurrently(t, func(t *testing.T, i int) {
		hermetic(t)
		p := newPair(t)
		aPushes(t, p, "a-1\n", map[string]string{"wiki/alpha.md": "A's version\n"})
		bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
		o := withQuiesce(t, appendOpts(p.b, p.remote), p.b)

		ap := startAppender(t, p.b, 1, pauseFor(i))
		time.Sleep(headStart(i))
		backup, _, err := TakeRemote(t.Context(), o, 1)
		acked := ap.Stop()
		if err != nil {
			t.Fatalf("TakeRemote: %v", err)
		}
		saved := gitRaw(t, p.b, "show", backup+":"+journalPath)
		now := readFile(t, filepath.Join(p.b, journalPath))
		inBackup, inTree := map[int]int{}, map[int]int{}
		for _, n := range concOrder(saved) {
			inBackup[n]++
		}
		for _, n := range concOrder(now) {
			inTree[n]++
		}
		var lost []int
		for _, n := range acked {
			if inBackup[n]+inTree[n] != 1 {
				lost = append(lost, n)
			}
		}
		if len(lost) > 0 {
			t.Errorf("%d of %d appended line(s) are in neither the backup branch nor the tree, or in both: %v", len(lost), len(acked), head(lost))
		}
		if _, err := os.Stat(filepath.Join(p.b, ".git", "lw-collision")); err == nil {
			t.Error("a false checkout collision was recorded")
		}
	})
}

// TestConcurrentAppendDuringRecovery: recovering an interrupted rebase resets
// the vault and puts the set-aside tail back; a line appended meanwhile stays.
func TestConcurrentAppendDuringRecovery(t *testing.T) {
	concurrently(t, func(t *testing.T, i int) {
		hermetic(t)
		p := newPair(t)
		_, tail := interruptedRebase(t, p)
		o := withQuiesce(t, appendOpts(p.b, p.remote), p.b)

		ap := startAppender(t, p.b, 1, pauseFor(i))
		time.Sleep(headStart(i))
		_, err := Recover(t.Context(), o)
		acked := ap.Stop()
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		text := readFile(t, filepath.Join(p.b, journalPath))
		wantAll(t, p.b, text, acked, "after the recovery")
		if strings.Count(text, tail) != 1 {
			t.Errorf("the set-aside tail is in the journal %d times, want once", strings.Count(text, tail))
		}
	})
}

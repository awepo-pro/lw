package main

// sync_concurrent_test.go pins 042 A-042-9 at the verb level: a journal line
// appended by another lw process while a push — which may pull, rebase and
// push — runs on the same vault is neither lost nor taken for a checkout
// collision. The appender calls the real stage.Journal.Append.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
)

const concurrentRuns = 12

// journalAppender appends numbered events until stopped.
type journalAppender struct {
	j     *stage.Journal
	pause time.Duration
	stop  chan struct{}
	done  chan struct{}
	mu    sync.Mutex
	acked []int
	next  int
}

func startJournalAppender(t *testing.T, vault string, pause time.Duration) *journalAppender {
	t.Helper()
	j, err := stage.OpenJournal(filepath.Join(vault, ".llmwiki", "journal.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	a := &journalAppender{j: j, pause: pause, stop: make(chan struct{}), done: make(chan struct{}), next: 1}
	go func() {
		defer close(a.done)
		for {
			select {
			case <-a.stop:
				return
			default:
			}
			a.mu.Lock()
			n := a.next
			a.next++
			a.mu.Unlock()
			err := j.Append(stage.Event{
				TS: time.Now().UTC(), Kind: stage.EvChangesetOpened, Changeset: "cs-conc",
				Actor: stage.Author{Kind: "human"}, Message: fmt.Sprintf("conc-%06d", n),
			})
			if err == nil {
				a.mu.Lock()
				a.acked = append(a.acked, n)
				a.mu.Unlock()
			}
			time.Sleep(a.pause)
		}
	}()
	return a
}

func (a *journalAppender) Stop() []int {
	close(a.stop)
	<-a.done
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]int(nil), a.acked...)
}

var concNumber = regexp.MustCompile(`conc-(\d{6})`)

// requireEveryLine fails the test unless every acknowledged line is in the
// journal exactly once, in order, and no collision marker exists.
func requireEveryLine(t *testing.T, pc *syncPC, acked []int, what string) {
	t.Helper()
	text := pc.read(".llmwiki/journal.ndjson")
	seen := map[int]int{}
	prev := -1
	for _, m := range concNumber.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		seen[n]++
		if n < prev {
			t.Errorf("%s: line %d is out of order", what, n)
		}
		prev = n
	}
	var lost, dup int
	for _, n := range acked {
		switch seen[n] {
		case 0:
			lost++
		case 1:
		default:
			dup++
		}
	}
	if lost > 0 || dup > 0 {
		t.Errorf("%s: of %d appended lines, %d were LOST and %d are duplicated", what, len(acked), lost, dup)
	}
	if _, err := os.Stat(filepath.Join(pc.root, ".git", "lw-collision")); err == nil {
		t.Errorf("%s: a false checkout collision was recorded", what)
	}
}

// TestConcurrentAppendDuringThePushPath (H1): the push after a change — commit
// to git, take the remote's commits and replay ours on them, push — with a
// second lw process appending to the journal the whole time.
func TestConcurrentAppendDuringThePushPath(t *testing.T) {
	for i := 0; i < concurrentRuns; i++ {
		t.Run(fmt.Sprintf("i%02d", i), func(t *testing.T) {
			a, b, remote := syncPair(t)
			settle(t, b)
			b.act()
			pushFromScratch(t, remote, "notes/20261009-140000-from-a.md", "from a\n", "lw notes")
			b.write("notes/20261009-130000-from-b.md", "from b\n") // work the push must commit and replay
			auto := loadAutoSync(b.root)
			if auto == nil {
				t.Fatal("no auto-sync")
			}
			ap := startJournalAppender(t, b.root, time.Duration(i%5+1)*300*time.Microsecond)
			time.Sleep(time.Duration(i*3%30) * time.Millisecond)
			res := auto.pushOnce(t.Context())
			acked := ap.Stop()
			if res.Err != nil || res.Pushed < 1 || res.Rebased != 1 {
				t.Fatalf("pushOnce = %+v", res)
			}
			requireEveryLine(t, b, acked, "after the push path")
			if len(acked) == 0 {
				t.Fatal("the appender never appended")
			}
			_ = a
		})
	}
}

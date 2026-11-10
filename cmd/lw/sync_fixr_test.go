package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/ledger"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// TestSyncFixR_R2_CollectCalledAfterSuccessfulCycleNotFailed tests that collect
// runs after every successful sync cycle, and NOT after a failed cycle.
func TestSyncFixR_R2_CollectCalledAfterSuccessfulCycleNotFailed(t *testing.T) {
	currentTime, _ := time.Parse("2006-01-02 15:04:05", "2026-11-10 12:00:00")
	var collectCalls []string
	var cycleCalls int
	cycleErr := errors.New("cycle failed")
	failCycle := false

	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return currentTime },
		dirty: func() (vaultsync.DirtyState, error) {
			// Tree is dirty to trigger cycle
			return vaultsync.DirtyState{
				Paths:  []string{"notes/a.md"},
				Newest: currentTime.Add(-5 * time.Second),
			}, nil
		},
		remote: func() (bool, error) {
			return false, nil
		},
		cycle: func() error {
			cycleCalls++
			if failCycle {
				return cycleErr
			}
			return nil
		},
		collect: func(today string) error {
			collectCalls = append(collectCalls, today)
			return nil
		},
		logf: func(format string, a ...any) {},
	}

	// 1. At start: initStartCycle runs collect once, then cycle succeeds.
	w.initStartCycle()
	// Start collect + post-cycle collect = 2 calls (or 1 at start + 1 post-cycle)
	startCollectCount := len(collectCalls)
	if startCollectCount == 0 {
		t.Fatalf("start: collect not called")
	}

	// 2. A successful cycle: collect must be called after the successful cycle!
	collectCalls = nil
	failCycle = false
	currentTime = currentTime.Add(5 * time.Second)
	w.step()
	if len(collectCalls) != 1 {
		t.Fatalf("after successful cycle: collectCalls = %v, want 1 call", collectCalls)
	}

	// 3. A failed cycle: collect must NOT be called!
	collectCalls = nil
	failCycle = true
	// clear backoff so step can attempt cycle
	w.backoffUntil = time.Time{}
	w.backoffDuration = 0
	currentTime = currentTime.Add(5 * time.Second)
	w.step()
	if len(collectCalls) != 0 {
		t.Fatalf("after failed cycle: collectCalls = %v, want 0 calls", collectCalls)
	}
}

// TestSyncFixR_R2_CollectErrorKeepsDayOpenAndRetried tests that an error in collect
// does not advance lastCollectDay, is logged once, and is retried.
func TestSyncFixR_R2_CollectErrorKeepsDayOpenAndRetried(t *testing.T) {
	currentTime, _ := time.Parse("2006-01-02 15:04:05", "2026-11-10 12:00:00")
	var collectCalls []string
	var logs []string
	collectErr := errors.New("collect lock error")
	failCollect := true

	w := &watchLoop{
		quiet:    3 * time.Second,
		maxWait:  30 * time.Second,
		interval: 15 * time.Second,
		now:      func() time.Time { return currentTime },
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.DirtyState{}, nil
		},
		remote: func() (bool, error) {
			return false, nil
		},
		cycle: func() error {
			return nil
		},
		collect: func(today string) error {
			collectCalls = append(collectCalls, today)
			if failCollect {
				return collectErr
			}
			return nil
		},
		logf: func(format string, a ...any) {
			logs = append(logs, fmt.Sprintf(format, a...))
		},
	}

	w.initStartCycle()
	if w.lastCollectDay != "" {
		t.Fatalf("lastCollectDay = %q, want empty when collect failed", w.lastCollectDay)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "collect lock error") {
		t.Fatalf("logs = %v, want 1 collect error log", logs)
	}

	// 30s later (within 60s, tree clean, remote unchanged): not retried yet
	currentTime = currentTime.Add(30 * time.Second)
	w.step()
	if len(collectCalls) != 1 {
		t.Fatalf("collectCalls = %v before 60s, want 1", collectCalls)
	}

	// 65s later: retry timer (60s) fires!
	currentTime = currentTime.Add(35 * time.Second)
	failCollect = false // succeeds now!
	w.step()
	if len(collectCalls) != 2 {
		t.Fatalf("collectCalls = %v after 60s retry, want 2", collectCalls)
	}
	if w.lastCollectDay != "2026-11-10" {
		t.Fatalf("lastCollectDay = %q, want 2026-11-10 after success", w.lastCollectDay)
	}
}

// TestSyncFixR_R2_QuietWaitingOutputPrintsOnce tests that repeated identical waiting
// output is printed only once, and bare 0-counts are never printed alone.
func TestSyncFixR_R2_QuietWaitingOutputPrintsOnce(t *testing.T) {
	var tw timestampWriter
	var buf bytes.Buffer
	tw.dst = &buf

	ct := &collectTracker{}

	// Case 1: bare 0 counts -> nothing printed
	out1 := "collected 0 day note(s), 0 waiting\n0 stored, 0 unchanged, 0 conflict(s), 0 error(s)\n"
	printed1 := ct.filterAndPrint(&tw, out1)
	if printed1 {
		t.Errorf("bare 0 counts should not print")
	}
	if buf.Len() != 0 {
		t.Errorf("buf should be empty, got: %s", buf.String())
	}

	// Case 2: waiting output -> prints once
	out2 := "waiting 2026-11-08.md: line 1: two numbers\ncollected 0 day note(s), 1 waiting\n0 stored, 0 unchanged, 0 conflict(s), 0 error(s)\n"
	printed2 := ct.filterAndPrint(&tw, out2)
	if !printed2 {
		t.Errorf("waiting output should print")
	}
	if !strings.Contains(buf.String(), "waiting 2026-11-08.md") {
		t.Errorf("buf should contain waiting line, got: %s", buf.String())
	}

	// Case 3: identical waiting output -> does not print again
	buf.Reset()
	printed3 := ct.filterAndPrint(&tw, out2)
	if printed3 {
		t.Errorf("repeated identical waiting output should not print")
	}
	if buf.Len() != 0 {
		t.Errorf("buf should be empty on duplicate waiting, got: %s", buf.String())
	}

	// Case 4: a day note is collected -> prints!
	buf.Reset()
	out4 := "collected 2026-11-09: lunch 40 -> ledger/2026/11/2026-11-09.md\ncollected 1 day note(s), 0 waiting\n0 stored, 0 unchanged, 0 conflict(s), 0 error(s)\n"
	printed4 := ct.filterAndPrint(&tw, out4)
	if !printed4 {
		t.Errorf("collected note output should print")
	}
	if !strings.Contains(buf.String(), "collected 2026-11-09") {
		t.Errorf("buf should contain collected line, got: %s", buf.String())
	}
}

// TestSyncFixR_R2_IntegrationCollectAfterSync tests:
// watcher A with --collect, vault B writes yesterday's day note and syncs AFTER A started;
// within the test's deadline A's bookkeeping.md has the [[date]] line and B (after its own sync)
// has the archived note and no root note.
func TestSyncFixR_R2_IntegrationCollectAfterSync(t *testing.T) {
	syncHermetic(t)
	pcA := newSyncPC(t, "fixr-r2-a")
	pcA.useFixture()
	remote := filepath.Join(syncTemp(t), "fixr-r2-remote")
	if _, _, code := pcA.lw("sync", "init", remote, "--vault", pcA.root); code != 0 {
		t.Fatal("init failed")
	}

	pcB := newSyncPC(t, "fixr-r2-b")
	if _, _, code := pcB.lw("sync", "clone", remote, pcB.root); code != 0 {
		t.Fatal("clone failed")
	}

	aRoot, aOpts, err := syncSetup(pcA.root, syncModeWrite)
	if err != nil {
		t.Fatal(err)
	}
	aOpts.Interactive = false

	var aOut bytes.Buffer
	tw := &timestampWriter{dst: &aOut}
	var ct conflictTracker

	cycleA := func() error {
		defer tw.Flush()
		return runSyncCycle(context.Background(), aRoot, aOpts, tw, io.Discard, false, true, nil, &ct)
	}

	// The fake clock starts at the real one: this loop reads the REAL mtimes of
	// the files collect moves (vaultsync.Dirty), and a fixed fake time made the
	// test pass only while the wall clock was earlier than it (it failed from
	// 2026-11-10 12:00 UTC on).
	fakeTime := time.Now()
	yest := fakeTime.AddDate(0, 0, -1).Format("2006-01-02")
	var colTracker collectTracker
	wA := &watchLoop{
		quiet:    1 * time.Second,
		maxWait:  5 * time.Second,
		interval: 5 * time.Second,
		now:      func() time.Time { return fakeTime },
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.Dirty(context.Background(), aOpts)
		},
		remote: func() (bool, error) {
			sha, local, _, err := vaultsync.RemoteHead(context.Background(), aOpts)
			if err != nil {
				return false, err
			}
			return sha != "" && sha != local, nil
		},
		cycle: cycleA,
		collect: func(curToday string) error {
			defer tw.Flush()
			var buf bytes.Buffer
			if err := collectDayNotes(&buf, aRoot, curToday, false, false); err != nil {
				return err
			}
			if err := importReceipts(&buf, aRoot, false); err != nil {
				return err
			}
			colTracker.filterAndPrint(tw, buf.String())
			return nil
		},
		logf: func(format string, a ...any) {},
	}

	// 1. Watcher A starts FIRST. No day notes exist yet.
	wA.initStartCycle()

	// 2. AFTER A started, B writes yesterday's day note and syncs.
	noteBody := "55 (lunch)\n"
	pcB.write(yest+".md", noteBody)
	if _, stderrB, codeB := pcB.lw("sync", "--vault", pcB.root); codeB != 0 {
		t.Fatalf("B sync failed: %d, %s", codeB, stderrB)
	}

	// 3. Drive watcher A:
	// Advance fake time past interval (5s) so remoteDue is true.
	fakeTime = fakeTime.Add(5 * time.Second)
	// Remote check detects B's commit -> triggers cycle -> pulls B's note.
	// Since cycle succeeds, collect runs immediately after cycle!
	// Collect moves 2026-11-09.md to archive and appends line to bookkeeping.md.
	// That dirties A's tree!
	wA.step() // pulls B's note; cycle succeeds; post-cycle collect archives it and updates bookkeeping.md!

	// Check if A's bookkeeping has the line already
	bkA := pcA.read(ledger.LooseFile)
	if !strings.Contains(bkA, fmt.Sprintf("[[%s]]", yest)) {
		t.Fatalf("A bookkeeping.md does not have [[%s]] line: %s", yest, bkA)
	}

	// Advance fake time past quiet (1s) so local dirty debounce triggers cycle.
	fakeTime = fakeTime.Add(2 * time.Second)
	// Step A again to commit and push the collection changes
	wA.step()

	// 4. B syncs from remote
	if _, stderrB, codeB := pcB.lw("sync", "--vault", pcB.root); codeB != 0 {
		t.Fatalf("B final sync failed: %d, %s", codeB, stderrB)
	}

	// Verify on B:
	// - bookkeeping.md has the [[date]] line
	bkB := pcB.read(ledger.LooseFile)
	if !strings.Contains(bkB, fmt.Sprintf("[[%s]]", yest)) {
		t.Errorf("B bookkeeping.md lacks [[%s]]: %s", yest, bkB)
	}
	// - day note is gone from B's root
	if pcB.read(yest+".md") != "" {
		t.Errorf("%s.md still at B's root", yest)
	}
	// - archived note is on B
	archRel := fmt.Sprintf("ledger/%s/%s/%s.md", yest[:4], yest[5:7], yest)
	if pcB.read(archRel) != noteBody {
		t.Errorf("B archived note %s = %q, want %q", archRel, pcB.read(archRel), noteBody)
	}
}

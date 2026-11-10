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

// TestSyncFixI_I5_PushRetryOnDiverged tests I5:
// a cycle whose push is rejected because the remote moved meanwhile
// (Push returns ErrDiverged right after a successful pull) pulls and pushes
// ONCE more inside the same cycle before it reports anything; only a second
// failure is an error. Plain `lw sync` gets the same retry.
// Test: a hook between pull and push lets the other PC push; `lw sync` exits 0
// with the rebased and pushed lines and no `diverged` text.
func TestSyncFixI_I5_PushRetryOnDiverged(t *testing.T) {
	syncHermetic(t)
	pcA := newSyncPC(t, "i5-a")
	pcA.useFixture()
	remote := filepath.Join(syncTemp(t), "i5-remote")
	if _, _, code := pcA.lw("sync", "init", remote, "--vault", pcA.root); code != 0 {
		t.Fatal("init failed")
	}

	pcB := newSyncPC(t, "i5-b")
	if _, _, code := pcB.lw("sync", "clone", remote, pcB.root); code != 0 {
		t.Fatal("clone failed")
	}

	// Both write different notes
	pcA.write("notes/a.md", "hello from a\n")
	pcB.write("notes/b.md", "hello from b\n")

	// Set hook on pcA: between pull and push, pcB pushes to remote
	syncPrePushHook = func() {
		stdoutB, stderrB, codeB := pcB.lw("sync", "--vault", pcB.root)
		if codeB != 0 {
			t.Fatalf("pcB sync failed: code %d, stderr: %s, stdout: %s", codeB, stderrB, stdoutB)
		}
	}
	defer func() { syncPrePushHook = nil }()

	// Now run sync on pcA
	stdoutA, stderrA, codeA := pcA.lw("sync", "--vault", pcA.root)
	if codeA != 0 {
		t.Fatalf("pcA sync failed: code %d, stderr: %s, stdout: %s", codeA, stderrA, stdoutA)
	}
	if strings.Contains(stdoutA, "diverged") || strings.Contains(stderrA, "diverged") {
		t.Fatalf("output contains diverged text: stdout=%q stderr=%q", stdoutA, stderrA)
	}
	if !strings.Contains(stdoutA, "rebased") {
		t.Errorf("stdout missing rebased line: %q", stdoutA)
	}
	if !strings.Contains(stdoutA, "pushed") {
		t.Errorf("stdout missing pushed line: %q", stdoutA)
	}

	// Both notes should now exist on pcA
	if pcA.read("notes/a.md") != "hello from a\n" {
		t.Errorf("pcA missing notes/a.md")
	}
	if pcA.read("notes/b.md") != "hello from b\n" {
		t.Errorf("pcA missing notes/b.md")
	}
}

// TestSyncFixI_I5_SecondRejectionIsError tests that the retry happens at most once per cycle;
// a second rejection is an error just as before.
func TestSyncFixI_I5_SecondRejectionIsError(t *testing.T) {
	syncHermetic(t)
	pcA := newSyncPC(t, "i5-a2")
	pcA.useFixture()
	remote := filepath.Join(syncTemp(t), "i5-remote2")
	if _, _, code := pcA.lw("sync", "init", remote, "--vault", pcA.root); code != 0 {
		t.Fatal("init failed")
	}

	pcB := newSyncPC(t, "i5-b2")
	if _, _, code := pcB.lw("sync", "clone", remote, pcB.root); code != 0 {
		t.Fatal("clone failed")
	}

	pcA.write("notes/a.md", "hello from a\n")

	// First push diverged because pcB pushes note b1
	syncPrePushHook = func() {
		pcB.write("notes/b1.md", "b1\n")
		_, _, codeB := pcB.lw("sync", "--vault", pcB.root)
		if codeB != 0 {
			t.Fatalf("pcB sync b1 failed: %d", codeB)
		}
	}
	// Second push ALSO diverged because pcB pushes note b2
	syncPreRetryPushHook = func() {
		pcB.write("notes/b2.md", "b2\n")
		_, _, codeB := pcB.lw("sync", "--vault", pcB.root)
		if codeB != 0 {
			t.Fatalf("pcB sync b2 failed: %d", codeB)
		}
	}
	defer func() {
		syncPrePushHook = nil
		syncPreRetryPushHook = nil
	}()

	stdoutA, stderrA, codeA := pcA.lw("sync", "--vault", pcA.root)
	if codeA != 1 {
		t.Fatalf("code = %d, want 1 (second failure must fail)\nstdout: %s\nstderr: %s", codeA, stdoutA, stderrA)
	}
	if !strings.Contains(stderrA, "diverged") {
		t.Errorf("stderr does not contain diverged text: %q", stderrA)
	}
}

// TestSyncFixI_I6_ClockCrossingMidnightTriggersCollect tests that fake clock crossing
// midnight triggers exactly one collect (and without the flag none).
func TestSyncFixI_I6_ClockCrossingMidnightTriggersCollect(t *testing.T) {
	// Parse 2026-11-10 23:59:50 in UTC
	currentTime, err := time.Parse("2006-01-02 15:04:05", "2026-11-10 23:59:50")
	if err != nil {
		t.Fatal(err)
	}

	var collectCalls []string
	var cycleCalls int

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
			cycleCalls++
			return nil
		},
		collect: func(today string) error {
			collectCalls = append(collectCalls, today)
			return nil
		},
		logf: func(format string, a ...any) {},
	}

	// 1. At start: initStartCycle runs collect once for 2026-11-10
	w.initStartCycle()
	if len(collectCalls) != 1 || collectCalls[0] != "2026-11-10" {
		t.Fatalf("start: collectCalls = %v, want [2026-11-10]", collectCalls)
	}
	if cycleCalls != 1 {
		t.Fatalf("start: cycleCalls = %d, want 1", cycleCalls)
	}

	// 2. Same day, 5s later (23:59:55): step runs, tree clean, no collect
	currentTime = currentTime.Add(5 * time.Second)
	w.step()
	if len(collectCalls) != 1 {
		t.Fatalf("same day: collectCalls = %v, want 1", collectCalls)
	}

	// 3. Clock crosses midnight to 2026-11-11 00:00:05: triggers exactly one collect!
	currentTime = currentTime.Add(10 * time.Second)
	w.step()
	if len(collectCalls) != 2 || collectCalls[1] != "2026-11-11" {
		t.Fatalf("midnight: collectCalls = %v, want [2026-11-10 2026-11-11]", collectCalls)
	}
	if cycleCalls != 2 {
		t.Fatalf("midnight: cycleCalls = %d, want 2 (cycle must run after collect)", cycleCalls)
	}

	// 4. Same new day, 5s later (00:00:10): no new collect
	currentTime = currentTime.Add(5 * time.Second)
	w.step()
	if len(collectCalls) != 2 {
		t.Fatalf("after midnight: collectCalls = %v, want 2", collectCalls)
	}
}

// TestSyncFixI_I6_WithoutFlagNoCollect tests that without collect func, the watcher never runs collect.
func TestSyncFixI_I6_WithoutFlagNoCollect(t *testing.T) {
	currentTime, _ := time.Parse("2006-01-02 15:04:05", "2026-11-10 23:59:50")
	var cycleCalls int

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
			cycleCalls++
			return nil
		},
		collect: nil, // without --collect
		logf:    func(format string, a ...any) {},
	}

	w.initStartCycle()
	if cycleCalls != 1 {
		t.Fatalf("start cycleCalls = %d, want 1", cycleCalls)
	}

	// Clock crosses midnight
	currentTime = currentTime.Add(20 * time.Second)
	w.step()
	// Tree is clean, remote unchanged: no cycle, no collect
	if cycleCalls != 1 {
		t.Fatalf("without collect: cycleCalls = %d, want 1", cycleCalls)
	}
}

// TestSyncFixI_I6_ErrorLoggedOnce tests that a collect error is logged once and does not stop syncing.
func TestSyncFixI_I6_ErrorLoggedOnce(t *testing.T) {
	currentTime, _ := time.Parse("2006-01-02 15:04:05", "2026-11-10 12:00:00")
	var logs []string
	var cycleCalls int
	collectErr := errors.New("collect permission denied")

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
			cycleCalls++
			return nil
		},
		collect: func(today string) error {
			return collectErr
		},
		logf: func(format string, a ...any) {
			logs = append(logs, fmt.Sprintf(format, a...))
		},
	}

	w.initStartCycle()
	// Cycle still ran!
	if cycleCalls != 1 {
		t.Fatalf("cycleCalls = %d, want 1 (error must not stop syncing)", cycleCalls)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "collect permission denied") {
		t.Fatalf("logs = %v, want 1 log containing error", logs)
	}

	// Advance clock past midnight; error occurs again
	currentTime = currentTime.Add(24 * time.Hour)
	w.step()
	// Same error text must NOT be logged again
	if len(logs) != 1 {
		t.Fatalf("error was logged multiple times: %v", logs)
	}
	if cycleCalls != 2 {
		t.Fatalf("cycleCalls = %d, want 2", cycleCalls)
	}
}

// TestSyncFixI_I6_IntegrationCollectSyncsAcrossPCs tests the end-to-end integration:
// a watcher with --collect on A turns yesterday's day note into a central line
// that reaches B with the archived note.
func TestSyncFixI_I6_IntegrationCollectSyncsAcrossPCs(t *testing.T) {
	syncHermetic(t)
	pcA := newSyncPC(t, "i6-a")
	pcA.useFixture()
	remote := filepath.Join(syncTemp(t), "i6-remote")
	if _, _, code := pcA.lw("sync", "init", remote, "--vault", pcA.root); code != 0 {
		t.Fatal("init failed")
	}

	pcB := newSyncPC(t, "i6-b")
	if _, _, code := pcB.lw("sync", "clone", remote, pcB.root); code != 0 {
		t.Fatal("clone failed")
	}

	// PC A writes a day note for 2026-11-09
	noteBody := "44 (kfc)\n"
	pcA.write("2026-11-09.md", noteBody)

	// Set up A's watcher loop with --collect
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

	today := "2026-11-10" // today is the 10th; yesterday is the 9th
	wA := &watchLoop{
		quiet:    1 * time.Second,
		maxWait:  5 * time.Second,
		interval: 5 * time.Second,
		now:      func() time.Time { t, _ := time.Parse("2006-01-02", today); return t },
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
			if err := collectDayNotes(tw, aRoot, curToday, false, false); err != nil {
				return err
			}
			return importReceipts(tw, aRoot, false)
		},
		logf: func(format string, a ...any) {},
	}

	// Run start cycle on A: runs collect for yesterday's note, commits into central line, pushes to remote
	wA.initStartCycle()

	// Verify on A:
	// 1. 2026-11-09.md gone from root
	if pcA.read("2026-11-09.md") != "" {
		t.Errorf("2026-11-09.md still at A's root")
	}
	// 2. Archived at ledger/2026/11/2026-11-09.md
	if pcA.read("ledger/2026/11/2026-11-09.md") != noteBody {
		t.Errorf("A archived note = %q, want %q", pcA.read("ledger/2026/11/2026-11-09.md"), noteBody)
	}
	// 3. Central line in bookkeeping.md
	bkA := pcA.read(ledger.LooseFile)
	if !strings.Contains(bkA, "2026-11-09, 44 (kfc) [[2026-11-09]]") {
		t.Errorf("A bookkeeping.md = %q, want central line", bkA)
	}

	// Now B syncs from remote
	stdoutB, stderrB, codeB := pcB.lw("sync", "--vault", pcB.root)
	if codeB != 0 {
		t.Fatalf("B sync failed: %d, stderr: %s, stdout: %s", codeB, stderrB, stdoutB)
	}

	// Verify on B:
	// 1. Central line reaches B in bookkeeping.md
	bkB := pcB.read(ledger.LooseFile)
	if !strings.Contains(bkB, "2026-11-09, 44 (kfc) [[2026-11-09]]") {
		t.Errorf("B bookkeeping.md = %q, want central line", bkB)
	}
	// 2. Archived note reaches B byte-identical
	if pcB.read("ledger/2026/11/2026-11-09.md") != noteBody {
		t.Errorf("B archived note = %q, want %q", pcB.read("ledger/2026/11/2026-11-09.md"), noteBody)
	}
}

// TestSyncFixI_I6_SyncInstallCollectFlag tests that `lw sync install --print --collect`
// adds --collect to ExecStart (and launchd plist).
func TestSyncFixI_I6_SyncInstallCollectFlag(t *testing.T) {
	pc := newSyncPC(t, "i6-install")
	pc.useFixture()

	// Linux
	oldGOOS := syncInstallGOOS
	defer func() { syncInstallGOOS = oldGOOS }()
	syncInstallGOOS = "linux"

	stdout, _, code := pc.lw("sync", "install", "--print", "--collect", "--vault", pc.root)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "sync watch --vault") || !strings.Contains(stdout, "--collect") {
		t.Errorf("stdout missing --collect in ExecStart: %s", stdout)
	}

	// Darwin
	syncInstallGOOS = "darwin"
	stdoutDarwin, _, code := pc.lw("sync", "install", "--print", "--collect", "--vault", pc.root)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdoutDarwin, "<string>--collect</string>") {
		t.Errorf("stdoutDarwin missing <string>--collect</string>: %s", stdoutDarwin)
	}
}

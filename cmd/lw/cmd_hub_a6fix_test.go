package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/hub"
	"github.com/awepo-pro/lw/internal/hubserver"
)

// TestA6Fix2WriteRateLimit: in cmd_hub.go the write path uses a SHORTER rate
// limit (2 s) than reads (10 s).
func TestA6Fix2WriteRateLimit(t *testing.T) {
	a, b, remote := syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	a.act()

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	_, engine, _, _, _, _, err := setupHubWiring(a.root, 0, devicesPath)
	if err != nil {
		t.Fatalf("setupHubWiring: %v", err)
	}

	// 1. PC b pushes a new file to remote
	putFile(t, b.root, "notes/b-file.md", "# From B\n")
	b.lw("sync")

	// 2. Set timer to 3 seconds ago (elapsed = 3s).
	// Reads require 10s elapsed -> must be rate limited (skip pull).
	setHubRefreshTimer(time.Now().Add(-3 * time.Second))

	ctx := context.Background()
	_, err = engine.Manifest(ctx)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	bFilePath := filepath.Join(a.root, "notes/b-file.md")
	if _, err := os.Stat(bFilePath); err == nil {
		t.Fatalf("read path with elapsed=3s should be rate-limited (limit 10s), but b-file.md was pulled")
	}

	// 3. PC b pushes another new file to remote
	putFile(t, b.root, "notes/b-file2.md", "# From B 2\n")
	b.lw("sync")

	// Set timer to 3 seconds ago again (elapsed = 3s).
	// Writes require only 2s elapsed -> must pull!
	setHubRefreshTimer(time.Now().Add(-3 * time.Second))

	// PUT from phone with write context
	dev := hub.Device{Name: "phone"}
	_, err = engine.Put(ctx, dev, "notes/phone-test.md", hub.BaseNew, []byte("# Phone\n"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	bFile2Path := filepath.Join(a.root, "notes/b-file2.md")
	if _, err := os.Stat(bFile2Path); err != nil {
		t.Fatalf("write path with elapsed=3s should pull (limit 2s), but b-file2.md was not pulled: %v", err)
	}
}

// TestA6Fix3: R1: close the window where a write is lost from main during a heal.
// During healHubDivergence (right before TakeRemote), a phone PUT must answer
// 503 hub_diverged (not 200). After the heal completes, the marker is gone,
// re-sending the PUT answers 200 applied, and remote main contains the note.
func TestA6Fix3(t *testing.T) {
	a, b, remote, hServer, pusher, devID := setupA6Test(t)

	initContent := "# Initial a\n"
	putFile(t, a.root, "notes/a.md", initContent)
	syncCommitAndPush(t, a, remote, "initial a")
	b.lw("sync", "--vault", b.root)

	initBytes, err := os.ReadFile(filepath.Join(a.root, "notes/a.md"))
	if err != nil {
		t.Fatal(err)
	}
	oldSHA := sha256Hex(initBytes)

	pcContent := "# PC edit on a\n"
	putFile(t, b.root, "notes/a.md", pcContent)
	syncCommitAndPush(t, b, remote, "pc edit on a")

	disableHubRefreshForTest(true)
	defer disableHubRefreshForTest(false)

	phoneContent := "# Phone edit on a\n"
	putReq := httptest.NewRequest("PUT", "/v1/file?path=notes/a.md&base="+oldSHA, bytes.NewReader([]byte(phoneContent)))
	putReq.RemoteAddr = "127.0.0.1:12345"
	putReq.Header.Set("Tailscale-User-Login", "phone-user")
	putReq.Header.Set("X-LW-Device", devID)
	putReq.Header.Set("X-LW-Device-Name", "phone")
	putRec := httptest.NewRecorder()
	hServer.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT notes/a.md code = %d, want 200; body=%s", putRec.Code, putRec.Body.String())
	}

	var hookPutCode int
	var hookPutBody string
	hubHealBeforeTake = func() {
		lateReq := httptest.NewRequest("PUT", "/v1/file?path=notes/late.md&base=new", bytes.NewReader([]byte("# Late note\n")))
		lateReq.RemoteAddr = "127.0.0.1:12345"
		lateReq.Header.Set("Tailscale-User-Login", "phone-user")
		lateReq.Header.Set("X-LW-Device", devID)
		lateReq.Header.Set("X-LW-Device-Name", "phone")
		lateRec := httptest.NewRecorder()
		hServer.ServeHTTP(lateRec, lateReq)
		hookPutCode = lateRec.Code
		hookPutBody = lateRec.Body.String()
	}
	defer func() { hubHealBeforeTake = nil }()

	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	// Check if late.md exists on remote main
	remoteTree := gitIn(t, remote, "ls-tree", "-r", "--name-only", "refs/heads/main")
	hasLateOnRemote := strings.Contains(remoteTree, "notes/late.md")

	if hookPutCode != http.StatusServiceUnavailable {
		if hookPutCode == http.StatusOK && !hasLateOnRemote {
			t.Fatalf("the hook PUT answered 200 and notes/late.md is missing from remote main after the heal (got %d body=%s)", hookPutCode, hookPutBody)
		}
		t.Fatalf("hook PUT answered code %d, want 503 hub_diverged; body=%s", hookPutCode, hookPutBody)
	}

	if isHubDiverged(a.root) {
		t.Fatalf("hub marker .git/lw-hub-diverged still exists after heal")
	}

	// Re-sending the same PUT now answers 200 applied
	retryReq := httptest.NewRequest("PUT", "/v1/file?path=notes/late.md&base=new", bytes.NewReader([]byte("# Late note\n")))
	retryReq.RemoteAddr = "127.0.0.1:12345"
	retryReq.Header.Set("Tailscale-User-Login", "phone-user")
	retryReq.Header.Set("X-LW-Device", devID)
	retryReq.Header.Set("X-LW-Device-Name", "phone")
	retryRec := httptest.NewRecorder()
	hServer.ServeHTTP(retryRec, retryReq)
	if retryRec.Code != http.StatusOK {
		t.Fatalf("retry PUT code = %d, want 200; body=%s", retryRec.Code, retryRec.Body.String())
	}

	_, o, err := syncSetup(a.root, syncModeWrite)
	if err != nil {
		t.Fatalf("syncSetup: %v", err)
	}
	syncOpts := o
	syncOpts.Interactive = false
	syncOpts.Stderr = io.Discard
	syncOpts.Quiesce = quiesceVault(a.root)
	syncOpts.AppendOnly = syncAppendOnly
	if _, err := syncPush(context.Background(), syncOpts); err != nil {
		t.Fatalf("syncPush: %v", err)
	}

	remoteLate := gitIn(t, remote, "cat-file", "blob", "refs/heads/main:notes/late.md")
	if strings.TrimSpace(remoteLate) != "# Late note" {
		t.Fatalf("remote main notes/late.md content = %q, want %q", remoteLate, "# Late note")
	}
}

// TestA6Fix4: R2: two heals must not run at once.
// When two heals are triggered concurrently, hubHealMu serializes them and the
// second re-checks remote status, returning healed=true without re-running TakeRemote.
// Assert: exactly ONE lw-diverged-* branch exists, exactly ONE notes/a.conflict-*.md
// on remote main, no marker, remote main has PC bytes at notes/a.md.
func TestA6Fix4(t *testing.T) {
	a, b, remote := syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	b.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n", remote))
	a.act()

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	root, engine, hServer, pusher, _, _, err := setupHubWiring(a.root, 0, devicesPath)
	if err != nil {
		t.Fatalf("setupHubWiring: %v", err)
	}
	_ = pusher

	devID := "device-12345678"
	devStore, _ := hubserver.OpenDevices(devicesPath)
	_, _ = devStore.RegisterPending(devID, "phone", "phone-user", "127.0.0.1", time.Now())
	_, _ = devStore.Approve("phone")

	// 1. Initial note synced
	initContent := "# Initial a\n"
	putFile(t, a.root, "notes/a.md", initContent)
	syncCommitAndPush(t, a, remote, "initial a")
	b.lw("sync", "--vault", b.root)

	initBytes, err := os.ReadFile(filepath.Join(a.root, "notes/a.md"))
	if err != nil {
		t.Fatal(err)
	}
	oldSHA := sha256Hex(initBytes)

	// 2. PC edits notes/a.md and pushes
	pcContent := "# PC edit on a\n"
	putFile(t, b.root, "notes/a.md", pcContent)
	syncCommitAndPush(t, b, remote, "pc edit on a")

	// 3. Phone PUT with stale base creates local commit on hub
	disableHubRefreshForTest(true)
	defer disableHubRefreshForTest(false)

	phoneContent := "# Phone edit on a\n"
	putReq := httptest.NewRequest("PUT", "/v1/file?path=notes/a.md&base="+oldSHA, bytes.NewReader([]byte(phoneContent)))
	putReq.RemoteAddr = "127.0.0.1:12345"
	putReq.Header.Set("Tailscale-User-Login", "phone-user")
	putReq.Header.Set("X-LW-Device", devID)
	putReq.Header.Set("X-LW-Device-Name", "phone")
	putRec := httptest.NewRecorder()
	hServer.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT notes/a.md code = %d, want 200; body=%s", putRec.Code, putRec.Body.String())
	}

	_, o, err := syncSetup(a.root, syncModeWrite)
	if err != nil {
		t.Fatalf("syncSetup: %v", err)
	}
	syncOpts := o
	syncOpts.Interactive = false
	syncOpts.Stderr = io.Discard
	syncOpts.Quiesce = quiesceVault(a.root)
	syncOpts.AppendOnly = syncAppendOnly

	// Hook holds the first heal after recording bytes while the second starts
	healStarted := make(chan struct{})
	hubHealAfterRecord = func() {
		hubHealAfterRecord = nil
		close(healStarted)
		time.Sleep(100 * time.Millisecond)
	}
	defer func() { hubHealAfterRecord = nil }()

	ctx := context.Background()
	var wg sync.WaitGroup
	var err1, err2 error
	var h1, h2 bool

	wg.Add(1)
	go func() {
		defer wg.Done()
		h1, err1 = healHubDivergence(ctx, root, syncOpts, engine)
	}()

	<-healStarted

	wg.Add(1)
	go func() {
		defer wg.Done()
		h2, err2 = healHubDivergence(ctx, root, syncOpts, engine)
	}()

	wg.Wait()

	if err1 != nil || err2 != nil {
		t.Fatalf("healHubDivergence returned error: err1=%v, err2=%v", err1, err2)
	}
	if !h1 || !h2 {
		t.Fatalf("healHubDivergence result: h1=%v (err1=%v), h2=%v (err2=%v); want both true", h1, err1, h2, err2)
	}

	// Assertions:
	// exactly ONE lw-diverged-* branch exists
	branchOut := gitIn(t, a.root, "branch", "--list", "lw-diverged-*")
	branchList := strings.Fields(branchOut)
	if len(branchList) != 1 {
		t.Fatalf("expected exactly ONE lw-diverged-* branch, got %d: %v", len(branchList), branchList)
	}

	// exactly ONE notes/a.conflict-*.md on remote main
	treeOut := gitIn(t, remote, "ls-tree", "-r", "--name-only", "refs/heads/main", "notes")
	var conflictFiles []string
	for _, f := range strings.Split(treeOut, "\n") {
		f = strings.TrimSpace(f)
		if strings.HasPrefix(f, "notes/a.conflict-") && strings.HasSuffix(f, ".md") {
			conflictFiles = append(conflictFiles, f)
		}
	}
	if len(conflictFiles) != 1 {
		t.Fatalf("expected exactly ONE conflict copy on remote main, got %d: %v", len(conflictFiles), conflictFiles)
	}

	// no marker
	if isHubDiverged(a.root) {
		t.Fatalf("hub marker .git/lw-hub-diverged still exists")
	}

	// remote main has PC bytes at notes/a.md
	remoteMainA := gitIn(t, remote, "cat-file", "blob", "refs/heads/main:notes/a.md")
	if strings.TrimSpace(remoteMainA) != strings.TrimSpace(pcContent) {
		t.Fatalf("remote main notes/a.md = %q, want PC content %q", remoteMainA, pcContent)
	}
}

// TestA6Fix5: R3: re-entrant heal deadlock.
// healHubDivergence holds hubHealMu while step c) re-applies each recorded file
// through engine.Put. If Refresh pulls a new divergence from PC during the heal,
// it must not call healHubDivergence again and deadlock on hubHealMu.
func TestA6Fix5(t *testing.T) {
	a, b, remote := syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	b.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n", remote))
	a.act()

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	root, engine, hServer, pusher, _, _, err := setupHubWiring(a.root, 0, devicesPath)
	if err != nil {
		t.Fatalf("setupHubWiring: %v", err)
	}
	_ = pusher

	devID := "device-12345678"
	devStore, _ := hubserver.OpenDevices(devicesPath)
	_, _ = devStore.RegisterPending(devID, "phone", "phone-user", "127.0.0.1", time.Now())
	_, _ = devStore.Approve("phone")

	initContent := "# Initial a\n"
	putFile(t, a.root, "notes/a.md", initContent)
	syncCommitAndPush(t, a, remote, "initial a")
	b.lw("sync", "--vault", b.root)

	initBytes, err := os.ReadFile(filepath.Join(a.root, "notes/a.md"))
	if err != nil {
		t.Fatal(err)
	}
	oldSHA := sha256Hex(initBytes)

	pcContent := "# PC edit on a\n"
	putFile(t, b.root, "notes/a.md", pcContent)
	syncCommitAndPush(t, b, remote, "pc edit on a")

	disableHubRefreshForTest(true)
	defer disableHubRefreshForTest(false)

	phoneContent := "# Phone edit on a\n"
	putReq := httptest.NewRequest("PUT", "/v1/file?path=notes/a.md&base="+oldSHA, bytes.NewReader([]byte(phoneContent)))
	putReq.RemoteAddr = "127.0.0.1:12345"
	putReq.Header.Set("Tailscale-User-Login", "phone-user")
	putReq.Header.Set("X-LW-Device", devID)
	putReq.Header.Set("X-LW-Device-Name", "phone")
	putRec := httptest.NewRecorder()
	hServer.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT notes/a.md code = %d, want 200; body=%s", putRec.Code, putRec.Body.String())
	}

	_, o, err := syncSetup(a.root, syncModeWrite)
	if err != nil {
		t.Fatalf("syncSetup: %v", err)
	}
	syncOpts := o
	syncOpts.Interactive = false
	syncOpts.Stderr = io.Discard
	syncOpts.Quiesce = quiesceVault(a.root)
	syncOpts.AppendOnly = syncAppendOnly

	hubHealBeforeReapply = func() {
		disableHubRefreshForTest(false)
		gitIn(t, b.root, "reset", "--hard", "HEAD~1")
		putFile(t, b.root, "notes/a.md", "# PC edit 2 on a\n")
		gitIn(t, b.root, "add", "-A")
		gitIn(t, b.root, "commit", "--quiet", "-m", "pc edit 2 on a")
		gitIn(t, b.root, "push", "--force", "--quiet", remote, "HEAD:refs/heads/main")
		setHubRefreshTimer(time.Time{})
	}
	defer func() { hubHealBeforeReapply = nil }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = healHubDivergence(ctx, root, syncOpts, engine)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("heal entry point did not return within 60 s deadline (deadlock on hubHealMu)")
	}

	// Assert: no marker is left OR the marker names a real divergence (not "healing")
	markerPath := filepath.Join(a.root, ".git", "lw-hub-diverged")
	if markerData, err := os.ReadFile(markerPath); err == nil {
		reason := strings.TrimSpace(string(markerData))
		if reason == "healing" {
			t.Fatalf("marker names %q, want real divergence or no marker", reason)
		}
	}

	// Assert: the hub is not wedged: a following phone PUT of a new path
	// returns 200 or 503 hub_diverged within 30 s.
	folDone := make(chan struct{})
	var folCode int
	var folBody string
	go func() {
		folReq := httptest.NewRequest("PUT", "/v1/file?path=notes/followup.md&base=new", bytes.NewReader([]byte("# Followup\n")))
		folReq.RemoteAddr = "127.0.0.1:12345"
		folReq.Header.Set("Tailscale-User-Login", "phone-user")
		folReq.Header.Set("X-LW-Device", devID)
		folReq.Header.Set("X-LW-Device-Name", "phone")
		folRec := httptest.NewRecorder()
		hServer.ServeHTTP(folRec, folReq)
		folCode = folRec.Code
		folBody = folRec.Body.String()
		close(folDone)
	}()

	select {
	case <-folDone:
	case <-time.After(30 * time.Second):
		t.Fatalf("following phone PUT wedged (did not return within 30s)")
	}

	if folCode != http.StatusOK && folCode != http.StatusServiceUnavailable {
		t.Fatalf("following phone PUT code = %d, want 200 or 503; body=%s", folCode, folBody)
	}
	if folCode == http.StatusServiceUnavailable && !strings.Contains(folBody, "hub_diverged") {
		t.Fatalf("following phone PUT returned 503 without hub_diverged; body=%s", folBody)
	}
}

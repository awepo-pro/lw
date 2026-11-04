package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/hubserver"
)

func setupA6Test(t *testing.T) (a, b *syncPC, remote string, hServer http.Handler, pusher *syncPusher, devID string) {
	t.Helper()
	a, b, remote = syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	b.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n", remote))
	a.act()

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	var err error
	hServer, pusher, err = newTestHubServer(a.root, a.configPath(), devicesPath)
	if err != nil {
		t.Fatalf("newTestHubServer: %v", err)
	}

	devID = "device-12345678"
	devStore, _ := hubserver.OpenDevices(devicesPath)
	_, _ = devStore.RegisterPending(devID, "phone", "phone-user", "127.0.0.1", time.Now())
	_, _ = devStore.Approve("phone")
	return a, b, remote, hServer, pusher, devID
}

func syncCommitAndPush(t *testing.T, pc *syncPC, remote, msg string) {
	t.Helper()
	gitIn(t, pc.root, "add", "-A")
	gitIn(t, pc.root, "commit", "--quiet", "-m", msg)
	gitIn(t, pc.root, "push", "--quiet", remote, "HEAD:refs/heads/main")
}

// TestC11: PC clone pushes an edit of notes/a.md; the hub (Refresh disabled to force
// the stale path) applies a phone PUT of notes/a.md with the old base; pusher runs
// -> remote main has the PC's bytes at notes/a.md AND a file notes/a.conflict-<slug>-<ts>.md
// with the phone's bytes; no marker; hub repo has a lw-diverged-* branch holding the
// original hub commit; a following phone PUT succeeds (200).
func TestC11(t *testing.T) {
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
	var putResp map[string]any
	if err := json.Unmarshal(putRec.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if putResp["result"] != "applied" {
		t.Fatalf("PUT result = %v, want applied", putResp["result"])
	}

	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	remoteMainA := gitIn(t, remote, "cat-file", "blob", "refs/heads/main:notes/a.md")
	if strings.TrimSpace(remoteMainA) != strings.TrimSpace(pcContent) {
		t.Fatalf("remote main notes/a.md = %q, want PC content %q", remoteMainA, pcContent)
	}

	treeOut := gitIn(t, remote, "ls-tree", "-r", "--name-only", "refs/heads/main", "notes")
	var conflictFile string
	for _, f := range strings.Split(treeOut, "\n") {
		f = strings.TrimSpace(f)
		if strings.HasPrefix(f, "notes/a.conflict-phone-") && strings.HasSuffix(f, ".md") {
			conflictFile = f
			break
		}
	}
	if conflictFile == "" {
		t.Fatalf("remote main missing notes/a.conflict-phone-*.md; ls-tree output:\n%s", treeOut)
	}
	remoteConflict := gitIn(t, remote, "cat-file", "blob", "refs/heads/main:"+conflictFile)
	if strings.TrimSpace(remoteConflict) != strings.TrimSpace(phoneContent) {
		t.Fatalf("remote conflict file %s content = %q, want phone content %q", conflictFile, remoteConflict, phoneContent)
	}

	if isHubDiverged(a.root) {
		t.Fatalf("hub marked as diverged, want no marker")
	}

	branchOut := gitIn(t, a.root, "branch", "--list", "lw-diverged-*")
	branchList := strings.Fields(branchOut)
	if len(branchList) == 0 {
		t.Fatalf("hub repo missing lw-diverged-* branch; git branch --list:\n%s", branchOut)
	}
	backupBranch := branchList[0]
	backupContent := gitIn(t, a.root, "cat-file", "blob", backupBranch+":notes/a.md")
	if strings.TrimSpace(backupContent) != strings.TrimSpace(phoneContent) {
		t.Fatalf("backup branch %s notes/a.md = %q, want original hub commit %q", backupBranch, backupContent, phoneContent)
	}

	folReq := httptest.NewRequest("PUT", "/v1/file?path=notes/followup.md&base=new", bytes.NewReader([]byte("# Followup\n")))
	folReq.RemoteAddr = "127.0.0.1:12345"
	folReq.Header.Set("Tailscale-User-Login", "phone-user")
	folReq.Header.Set("X-LW-Device", devID)
	folReq.Header.Set("X-LW-Device-Name", "phone")
	folRec := httptest.NewRecorder()
	hServer.ServeHTTP(folRec, folReq)
	if folRec.Code != http.StatusOK {
		t.Fatalf("following PUT code = %d, want 200; body=%s", folRec.Code, folRec.Body.String())
	}
}

// TestC12: ledger day file: same race on ledger/2026/11/2026-11-04.md -> remote has
// every line of both sides, no conflict copy.
func TestC12(t *testing.T) {
	a, b, remote, hServer, pusher, devID := setupA6Test(t)

	dayPath := "ledger/2026/11/2026-11-04.md"
	initContent := "- (id:: 1) initial item\n"
	putFile(t, a.root, dayPath, initContent)
	syncCommitAndPush(t, a, remote, "initial ledger")
	b.lw("sync", "--vault", b.root)

	initBytes, err := os.ReadFile(filepath.Join(a.root, dayPath))
	if err != nil {
		t.Fatal(err)
	}
	oldSHA := sha256Hex(initBytes)

	pcContent := "- (id:: 1) initial item\n- (id:: pc-entry) from pc\n"
	putFile(t, b.root, dayPath, pcContent)
	syncCommitAndPush(t, b, remote, "pc ledger append")

	disableHubRefreshForTest(true)
	defer disableHubRefreshForTest(false)

	phoneContent := "- (id:: 1) initial item\n- (id:: phone-entry) from phone\n"
	putReq := httptest.NewRequest("PUT", "/v1/file?path="+dayPath+"&base="+oldSHA, bytes.NewReader([]byte(phoneContent)))
	putReq.RemoteAddr = "127.0.0.1:12345"
	putReq.Header.Set("Tailscale-User-Login", "phone-user")
	putReq.Header.Set("X-LW-Device", devID)
	putReq.Header.Set("X-LW-Device-Name", "phone")
	putRec := httptest.NewRecorder()
	hServer.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT %s code = %d, want 200; body=%s", dayPath, putRec.Code, putRec.Body.String())
	}

	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	remoteContent := gitIn(t, remote, "cat-file", "blob", "refs/heads/main:"+dayPath)
	if !strings.Contains(remoteContent, "- (id:: 1) initial item") {
		t.Fatalf("remote content missing initial item:\n%s", remoteContent)
	}
	if !strings.Contains(remoteContent, "- (id:: pc-entry) from pc") {
		t.Fatalf("remote content missing pc-entry:\n%s", remoteContent)
	}
	if !strings.Contains(remoteContent, "- (id:: phone-entry) from phone") {
		t.Fatalf("remote content missing phone-entry:\n%s", remoteContent)
	}

	treeOut := gitIn(t, remote, "ls-tree", "-r", "--name-only", "refs/heads/main")
	if strings.Contains(treeOut, "conflict") {
		t.Fatalf("expected no conflict copy on remote, found:\n%s", treeOut)
	}

	if isHubDiverged(a.root) {
		t.Fatalf("hub marked as diverged, want no marker")
	}
}

// TestC13: a local non-"hub:" commit in the divergence -> marker stays, writes 503, nothing healed.
func TestC13(t *testing.T) {
	a, b, remote, hServer, pusher, devID := setupA6Test(t)

	initContent := "# Initial note\n"
	putFile(t, a.root, "notes/a.md", initContent)
	syncCommitAndPush(t, a, remote, "initial note")
	b.lw("sync", "--vault", b.root)

	putFile(t, b.root, "notes/a.md", "# PC edit\n")
	syncCommitAndPush(t, b, remote, "pc edit")

	putFile(t, a.root, "notes/a.md", "# Local manual edit\n")
	gitIn(t, a.root, "add", "-A")
	gitIn(t, a.root, "commit", "-m", "manual edit on hub host")

	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	if !isHubDiverged(a.root) {
		t.Fatalf("expected marker .git/lw-hub-diverged to stay for non-hub commit")
	}

	putReq := httptest.NewRequest("PUT", "/v1/file?path=notes/after-fail.md&base=new", bytes.NewReader([]byte("# New\n")))
	putReq.RemoteAddr = "127.0.0.1:12345"
	putReq.Header.Set("Tailscale-User-Login", "phone-user")
	putReq.Header.Set("X-LW-Device", devID)
	putReq.Header.Set("X-LW-Device-Name", "phone")
	putRec := httptest.NewRecorder()
	hServer.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("phone write while diverged code = %d, want 503; body=%s", putRec.Code, putRec.Body.String())
	}
}

// TestC14: L1: with Refresh enabled, the same race (PC push, then phone PUT with
// the old base) yields conflict_copy directly (no divergence at all).
func TestC14(t *testing.T) {
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

	disableHubRefreshForTest(false)
	resetHubRefreshTimer()

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
	var putResp map[string]any
	if err := json.Unmarshal(putRec.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if putResp["result"] != "conflict_copy" {
		t.Fatalf("PUT result = %v, want conflict_copy directly", putResp["result"])
	}
	conflictPath, ok := putResp["conflict_path"].(string)
	if !ok || !strings.HasPrefix(conflictPath, "notes/a.conflict-phone-") {
		t.Fatalf("conflict_path = %v, want notes/a.conflict-phone-*.md", putResp["conflict_path"])
	}

	if isHubDiverged(a.root) {
		t.Fatalf("expected no diverged marker")
	}

	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	remoteMainA := gitIn(t, remote, "cat-file", "blob", "refs/heads/main:notes/a.md")
	if strings.TrimSpace(remoteMainA) != strings.TrimSpace(pcContent) {
		t.Fatalf("remote main notes/a.md = %q, want PC content %q", remoteMainA, pcContent)
	}
}

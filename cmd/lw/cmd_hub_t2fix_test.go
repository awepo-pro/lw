package main

import (
	"bytes"
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

// TestT2Fix1StorageUpload: hub's background push must carry lw storage exactly like
// lw sync does: upload the payload objects the catalog names BEFORE the git push.
// Test: a receipt PUT via handler -> after pusher flush, object is present under
// scratch remote's .storage/objects/.
func TestT2Fix1StorageUpload(t *testing.T) {
	a, _, remote := syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	a.act()

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	hServer, pusher, err := newTestHubServer(a.root, a.configPath(), devicesPath)
	if err != nil {
		t.Fatalf("newTestHubServer: %v", err)
	}

	devID := "device-12345678"
	devStore, _ := hubserver.OpenDevices(devicesPath)
	_, _ = devStore.RegisterPending(devID, "phone", "phone-user", "127.0.0.1", time.Now())
	_, _ = devStore.Approve("phone")

	receiptPath := "ledger/receipts/2026/11/receipt1.jpg"
	receiptBody := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00receipt-image-content-for-test-079\n")
	putReq := httptest.NewRequest("PUT", "/v1/file?path="+receiptPath+"&base=new", bytes.NewReader(receiptBody))
	putReq.RemoteAddr = "127.0.0.1:12345"
	putReq.Header.Set("Tailscale-User-Login", "phone-user")
	putReq.Header.Set("X-LW-Device", devID)
	putReq.Header.Set("X-LW-Device-Name", "phone")
	putRec := httptest.NewRecorder()
	hServer.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT receipt code = %d, want 200; body=%s", putRec.Code, putRec.Body.String())
	}

	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	objFile := s066ObjectPath(remote+".storage", string(receiptBody))
	if _, err := os.Stat(objFile); err != nil {
		t.Fatalf("receipt object not present under remote .storage/objects/: %v", err)
	}
}

// TestT2Fix2aLockWait: Options.Lock and Commit backend WAIT for vault lock (retry
// lockVault with backoff up to 15s) instead of failing on "busy".
func TestT2Fix2aLockWait(t *testing.T) {
	a, _, _ := syncPair(t)
	a.setConfig("[sync]\nremotes = [\"fake\"]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n")
	a.act()

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	hServer, _, err := newTestHubServer(a.root, a.configPath(), devicesPath)
	if err != nil {
		t.Fatalf("newTestHubServer: %v", err)
	}

	devID := "device-12345678"
	devStore, _ := hubserver.OpenDevices(devicesPath)
	_, _ = devStore.RegisterPending(devID, "phone", "phone-user", "127.0.0.1", time.Now())
	_, _ = devStore.Approve("phone")

	// Pre-acquire lock for 100ms
	rel, err := lockVault(a.root)
	if err != nil {
		t.Fatalf("lockVault: %v", err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		rel()
	}()

	fileBody := []byte("# Lock wait note\n")
	putReq := httptest.NewRequest("PUT", "/v1/file?path=notes/wait-lock.md&base=new", bytes.NewReader(fileBody))
	putReq.RemoteAddr = "127.0.0.1:12345"
	putReq.Header.Set("Tailscale-User-Login", "phone-user")
	putReq.Header.Set("X-LW-Device", devID)
	putReq.Header.Set("X-LW-Device-Name", "phone")
	putRec := httptest.NewRecorder()
	hServer.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT while lock held code = %d, want 200; body=%s", putRec.Code, putRec.Body.String())
	}
}

// TestT2Fix2bPushSweep: runPush commits uncommitted work tree with message "hub: sweep"
// before pulling so an earlier failed Commit is never orphaned.
func TestT2Fix2bPushSweep(t *testing.T) {
	a, _, remote := syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	a.act()

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	_, pusher, err := newTestHubServer(a.root, a.configPath(), devicesPath)
	if err != nil {
		t.Fatalf("newTestHubServer: %v", err)
	}

	notesDir := filepath.Join(a.root, "notes")
	if err := os.MkdirAll(notesDir, 0755); err != nil {
		t.Fatal(err)
	}
	uncommittedFile := filepath.Join(notesDir, "uncommitted.md")
	uncommittedBody := []byte("uncommitted note content\n")
	if err := os.WriteFile(uncommittedFile, uncommittedBody, 0644); err != nil {
		t.Fatal(err)
	}

	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	subjects := remoteSubjects(t, remote)
	if len(subjects) == 0 || subjects[0] != "hub: sweep" {
		var top string
		if len(subjects) > 0 {
			top = subjects[0]
		}
		t.Fatalf("remote latest commit = %q, want \"hub: sweep\"", top)
	}
	content := remoteFile(t, remote, "notes/uncommitted.md")
	if content != strings.TrimSpace(string(uncommittedBody)) {
		t.Fatalf("remote file content = %q, want %q", content, strings.TrimSpace(string(uncommittedBody)))
	}
}

// TestT2Fix3DivergedLock: while .git/lw-hub-diverged exists, Options.Lock returns
// &hub.Error{CodeDiverged} so no phone write or delete reaches disk.
func TestT2Fix3DivergedLock(t *testing.T) {
	a, _, remote := syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	a.act()

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	hServer, _, err := newTestHubServer(a.root, a.configPath(), devicesPath)
	if err != nil {
		t.Fatalf("newTestHubServer: %v", err)
	}

	devID := "device-12345678"
	devStore, _ := hubserver.OpenDevices(devicesPath)
	_, _ = devStore.RegisterPending(devID, "phone", "phone-user", "127.0.0.1", time.Now())
	_, _ = devStore.Approve("phone")

	markHubDiverged(a.root, "divergence test")

	notePath := "notes/diverged-note.md"
	putReq := httptest.NewRequest("PUT", "/v1/file?path="+notePath+"&base=new", strings.NewReader("diverged body\n"))
	putReq.RemoteAddr = "127.0.0.1:12345"
	putReq.Header.Set("Tailscale-User-Login", "phone-user")
	putReq.Header.Set("X-LW-Device", devID)
	putReq.Header.Set("X-LW-Device-Name", "phone")
	putRec := httptest.NewRecorder()
	hServer.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT while diverged code = %d, want 503", putRec.Code)
	}

	diskPath := filepath.Join(a.root, notePath)
	if _, err := os.Stat(diskPath); err == nil {
		t.Fatalf("file %s was written to disk while hub diverged", notePath)
	}
}

// TestT2Fix4DivergedRefreshResolve: Refresh still runs (rate-limited) pull while
// marker exists, so a clean pull after user resolves divergence clears marker
// and hub serves writes again.
func TestT2Fix4DivergedRefreshResolve(t *testing.T) {
	a, b, remote := syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	a.act()

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	hServer, _, err := newTestHubServer(a.root, a.configPath(), devicesPath)
	if err != nil {
		t.Fatalf("newTestHubServer: %v", err)
	}

	devID := "device-12345678"
	devStore, _ := hubserver.OpenDevices(devicesPath)
	_, _ = devStore.RegisterPending(devID, "phone", "phone-user", "127.0.0.1", time.Now())
	_, _ = devStore.Approve("phone")

	// Create divergence
	a.write("notes/conflict.md", "hub content\n")
	a.lw("commit", "-m", "hub commit", "--vault", a.root)

	b.write("notes/conflict.md", "pc content\n")
	b.lw("commit", "-m", "pc commit", "--vault", b.root)
	b.lw("sync", "--vault", b.root)

	// Set diverged marker
	markHubDiverged(a.root, "diverged from remote")

	// Verify PUT returns 503
	putReq1 := httptest.NewRequest("PUT", "/v1/file?path=notes/p1.md&base=new", strings.NewReader("p1\n"))
	putReq1.RemoteAddr = "127.0.0.1:12345"
	putReq1.Header.Set("Tailscale-User-Login", "phone-user")
	putReq1.Header.Set("X-LW-Device", devID)
	putReq1.Header.Set("X-LW-Device-Name", "phone")
	putRec1 := httptest.NewRecorder()
	hServer.ServeHTTP(putRec1, putReq1)
	if putRec1.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT diverged code = %d, want 503", putRec1.Code)
	}

	// Resolve divergence on hub host: user runs `lw sync --take-remote`
	out, errs, code := a.lw("sync", "--take-remote", "--vault", a.root)
	if code != 0 {
		t.Fatalf("a.lw sync --take-remote exit=%d\nout: %s\nerrs: %s", code, out, errs)
	}

	// Set marker back to test that Refresh clears it
	markHubDiverged(a.root, "test marker to clear")

	// Reset timer so Refresh pulls
	resetHubRefreshTimer()

	// Trigger Refresh via GET /v1/changes
	changesReq := httptest.NewRequest("GET", "/v1/changes?since=0000000000000000000000000000000000000000", nil)
	changesReq.RemoteAddr = "127.0.0.1:12345"
	changesReq.Header.Set("Tailscale-User-Login", "phone-user")
	changesReq.Header.Set("X-LW-Device", devID)
	changesRec := httptest.NewRecorder()
	hServer.ServeHTTP(changesRec, changesReq)

	if isHubDiverged(a.root) {
		t.Fatalf("marker .git/lw-hub-diverged not cleared by Refresh after resolution")
	}

	// PUT succeeds 200 OK
	putReq2 := httptest.NewRequest("PUT", "/v1/file?path=notes/p2.md&base=new", strings.NewReader("p2\n"))
	putReq2.RemoteAddr = "127.0.0.1:12345"
	putReq2.Header.Set("Tailscale-User-Login", "phone-user")
	putReq2.Header.Set("X-LW-Device", devID)
	putReq2.Header.Set("X-LW-Device-Name", "phone")
	putRec2 := httptest.NewRecorder()
	hServer.ServeHTTP(putRec2, putReq2)
	if putRec2.Code != http.StatusOK {
		t.Fatalf("PUT after resolve code = %d, want 200; body=%s", putRec2.Code, putRec2.Body.String())
	}
}

// TestT2Fix5ServeLogging: lw hub serve must attach the vault log (<vault>/.llmwiki/logs/lw.log)
// and route hubserver Config.Log and the pusher's results there.
func TestT2Fix5ServeLogging(t *testing.T) {
	home := syncHermetic(t)
	cfgDir := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	a, _, remote := syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	a.act()

	installDiscard()

	logFile := filepath.Join(a.root, ".llmwiki", "logs", "lw.log")
	_ = os.Remove(logFile)

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	hServer, pusher, err := newTestHubServer(a.root, a.configPath(), devicesPath)
	if err != nil {
		t.Fatalf("newTestHubServer: %v", err)
	}

	// Make devicesPath unwritable directory so a request makes the handler log an error
	_ = os.Remove(devicesPath)
	if err := os.Mkdir(devicesPath, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(devicesPath)

	req := httptest.NewRequest("GET", "/v1/hello", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Tailscale-User-Login", "phone-user")
	req.Header.Set("X-LW-Device", "device-12345678")
	rec := httptest.NewRecorder()
	hServer.ServeHTTP(rec, req)

	// Flush pusher
	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read %s: %v", logFile, err)
	}
	if !strings.Contains(string(data), "hubserver:") {
		t.Fatalf("expected handler log in %s, got:\n%s", logFile, string(data))
	}
	if !strings.Contains(string(data), "hub: push") {
		t.Fatalf("expected pusher results in %s, got:\n%s", logFile, string(data))
	}
}

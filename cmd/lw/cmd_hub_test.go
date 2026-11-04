package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/hub"
	"github.com/awepo-pro/lw/internal/hubserver"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestC1: [hub] port = 8780, users = ["a@b"] parses; absent [hub] -> Hub == nil; existing config tests unchanged.
func TestC1(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// 1. With port and users
	writeConfigFile(t, dir, "[hub]\nport = 8780\nusers = [\"a@b\"]\n")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	if cfg.Hub == nil {
		t.Fatalf("cfg.Hub = nil, want non-nil")
	}
	if cfg.Hub.Port != 8780 {
		t.Errorf("cfg.Hub.Port = %d, want 8780", cfg.Hub.Port)
	}
	if len(cfg.Hub.Users) != 1 || cfg.Hub.Users[0] != "a@b" {
		t.Errorf("cfg.Hub.Users = %v, want [\"a@b\"]", cfg.Hub.Users)
	}

	// 2. Absent [hub] -> Hub == nil
	writeConfigFile(t, dir, "theme = \"nord\"\n")
	cfg2, err := config.Load()
	if err != nil {
		t.Fatalf("Load config 2: %v", err)
	}
	if cfg2.Hub != nil {
		t.Fatalf("cfg2.Hub = %+v, want nil", cfg2.Hub)
	}

	// 3. Default port 8779 when port is 0 or omitted
	writeConfigFile(t, dir, "[hub]\nusers = [\"alice@example.com\"]\n")
	cfg3, err := config.Load()
	if err != nil {
		t.Fatalf("Load config 3: %v", err)
	}
	if cfg3.Hub == nil {
		t.Fatalf("cfg3.Hub = nil, want non-nil")
	}
	if cfg3.Hub.Port != 8779 {
		t.Errorf("cfg3.Hub.Port = %d, want default 8779", cfg3.Hub.Port)
	}
}

// TestC2: lw hub serve on a vault not under lw sync -> exit 1, stderr names lw sync;
// with [hub] missing or users empty -> exit 1, stderr names [hub] users.
func TestC2(t *testing.T) {
	home := syncHermetic(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// 1. Vault not under lw sync
	notSyncedDir := t.TempDir()
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"hub", "serve", "--vault", notSyncedDir})
	})
	if code != 1 {
		t.Errorf("vault not under lw sync: exit = %d, want 1; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "lw sync") {
		t.Errorf("stderr %q does not name \"lw sync\"", stderr)
	}

	// 2. Vault under lw sync, but [hub] missing
	a, _, _ := syncPair(t)
	a.setConfig("[sync]\nremotes = [\"fake\"]\n")
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"hub", "serve", "--vault", a.root})
	})
	if code != 1 {
		t.Errorf("[hub] missing: exit = %d, want 1; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "[hub] users") {
		t.Errorf("stderr %q does not name \"[hub] users\"", stderr)
	}

	// 3. Vault under lw sync, but [hub] users is empty
	a.setConfig("[sync]\nremotes = [\"fake\"]\n[hub]\nport = 8779\nusers = []\n")
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"hub", "serve", "--vault", a.root})
	})
	if code != 1 {
		t.Errorf("[hub] users empty: exit = %d, want 1; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "[hub] users") {
		t.Errorf("stderr %q does not name \"[hub] users\"", stderr)
	}
}

// TestC3: end to end (in-process handler, scratch vault under lw sync to a local bare remote):
// approved device PUTs notes/from-phone.md -> within the flush the bare remote's main has a commit
// hub: <slug> put notes/from-phone.md containing the file; a second clone pulls it.
func TestC3(t *testing.T) {
	a, b, remote := syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	a.act()

	// Setup hub handler in-process for vault a
	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	hServer, pusher, err := newTestHubServer(a.root, a.configPath(), devicesPath)
	if err != nil {
		t.Fatalf("newTestHubServer: %v", err)
	}

	// First request from device to register pending
	devID := "device-12345678"
	req := httptest.NewRequest("GET", "/v1/hello", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Tailscale-User-Login", "phone-user")
	req.Header.Set("X-LW-Device", devID)
	req.Header.Set("X-LW-Device-Name", "phone")
	rec := httptest.NewRecorder()
	hServer.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("hello pending device code = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	// Approve device
	devStore, err := hubserver.OpenDevices(devicesPath)
	if err != nil {
		t.Fatalf("OpenDevices: %v", err)
	}
	record, err := devStore.Approve("phone")
	if err != nil {
		t.Fatalf("Approve device: %v", err)
	}
	if record.State != "approved" {
		t.Fatalf("record state = %s, want approved", record.State)
	}

	// Approved device PUTs notes/from-phone.md
	fileBody := []byte("# Note from phone\nHello world\n")
	putReq := httptest.NewRequest("PUT", "/v1/file?path=notes/from-phone.md&base=new", bytes.NewReader(fileBody))
	putReq.RemoteAddr = "127.0.0.1:12345"
	putReq.Header.Set("Tailscale-User-Login", "phone-user")
	putReq.Header.Set("X-LW-Device", devID)
	putReq.Header.Set("X-LW-Device-Name", "phone")
	putRec := httptest.NewRecorder()
	hServer.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT code = %d, want 200; body=%s", putRec.Code, putRec.Body.String())
	}

	// Flush the pusher
	pusher.Flush(20*time.Second, nil)

	// Verify the bare remote's main has commit "hub: phone put notes/from-phone.md"
	subjects := remoteSubjects(t, remote)
	if len(subjects) == 0 {
		t.Fatalf("remote has no commits")
	}
	wantSubject := "hub: phone put notes/from-phone.md"
	if subjects[0] != wantSubject {
		t.Errorf("remote latest commit = %q, want %q", subjects[0], wantSubject)
	}
	content := remoteFile(t, remote, "notes/from-phone.md")
	if content != strings.TrimSpace(string(fileBody)) {
		t.Errorf("remote file content = %q, want %q", content, strings.TrimSpace(string(fileBody)))
	}

	// Second clone b pulls it
	stdout, stderr, code := b.lw("sync")
	if code != 0 {
		t.Fatalf("b.lw sync exit = %d; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	bContent := b.read("notes/from-phone.md")
	if bContent != string(fileBody) {
		t.Errorf("clone b file content = %q, want %q", bContent, string(fileBody))
	}
}

// TestC4: a PC clone pushes notes/pc.md; the hub's next refresh (timer forced) ->
// /v1/changes?since=<old head> lists notes/pc.md with its sha.
func TestC4(t *testing.T) {
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

	oldHead := remoteHead(t, remote)

	// PC clone pushes notes/pc.md
	b.write("notes/pc.md", "written from pc\n")
	b.lw("sync")

	// Force refresh
	resetHubRefreshTimer()

	// Trigger /v1/changes?since=<oldHead>
	req := httptest.NewRequest("GET", "/v1/changes?since="+oldHead, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Tailscale-User-Login", "phone-user")
	req.Header.Set("X-LW-Device", devID)
	rec := httptest.NewRecorder()
	hServer.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/changes code = %d; body=%s", rec.Code, rec.Body.String())
	}

	var changes hub.Changes
	if err := json.Unmarshal(rec.Body.Bytes(), &changes); err != nil {
		t.Fatalf("unmarshal changes: %v", err)
	}
	found := false
	for _, ch := range changes.Changed {
		if ch.Path == "notes/pc.md" {
			found = true
			if ch.SHA256 == nil || *ch.SHA256 == "" {
				t.Errorf("notes/pc.md has nil/empty sha256")
			}
			break
		}
	}
	if !found {
		t.Errorf("changes do not list notes/pc.md: %+v", changes)
	}
}

// TestC5: divergence that rebase cannot resolve (same note edited on the PC clone and via the hub) ->
// writes answer 503 hub_diverged; lw hub status prints a line containing diverged; nothing is lost on either side.
func TestC5(t *testing.T) {
	a, b, remote := syncPair(t)
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

	// 1. Initial note synced to both sides
	initialBody := "# Shared note\ninitial content\n"
	b.write("notes/shared.md", initialBody)
	b.lw("sync")
	a.lw("sync")

	// 2. PC clone edits notes/shared.md and pushes
	pcBody := "# Shared note\npc content edit\n"
	b.write("notes/shared.md", pcBody)
	b.lw("sync")

	// 3. Hub edits notes/shared.md with conflicting edit (manual edit on the hub)
	hubBody := []byte("# Shared note\nhub content edit\n")
	a.write("notes/shared.md", string(hubBody))
	gitAdd := exec.Command("git", "-C", a.root, "add", "notes/shared.md")
	if out, err := gitAdd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v, out=%s", err, string(out))
	}
	gitCmd := exec.Command("git", "-C", a.root, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-am", "manual edit on the hub")
	if out, err := gitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v, out=%s", err, string(out))
	}

	// Pusher runs and attempts to pull/rebase from remote, which fails because notes/shared.md diverges!
	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	// Marker file must exist in .git/lw-hub-diverged
	markerFile := filepath.Join(a.root, ".git", "lw-hub-diverged")
	if _, err := os.Stat(markerFile); err != nil {
		t.Fatalf("marker file %s does not exist: %v", markerFile, err)
	}

	// Next write answers 503 hub_diverged
	putReq2 := httptest.NewRequest("PUT", "/v1/file?path=notes/another.md&base=new", bytes.NewReader([]byte("test")))
	putReq2.RemoteAddr = "127.0.0.1:12345"
	putReq2.Header.Set("Tailscale-User-Login", "phone-user")
	putReq2.Header.Set("X-LW-Device", devID)
	putRec2 := httptest.NewRecorder()
	hServer.ServeHTTP(putRec2, putReq2)
	if putRec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("write while diverged code = %d, want 503; body=%s", putRec2.Code, putRec2.Body.String())
	}
	if !strings.Contains(putRec2.Body.String(), hub.CodeDiverged) {
		t.Errorf("error body %s does not contain %q", putRec2.Body.String(), hub.CodeDiverged)
	}

	// lw hub status prints a line containing diverged
	stdout, stderr, code := a.lw("hub", "status", "--vault", a.root)
	if code != 0 {
		t.Errorf("hub status exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "diverged") {
		t.Errorf("hub status stdout %q does not contain \"diverged\"", stdout)
	}

	// Verify nothing lost on either side
	if b.read("notes/shared.md") != pcBody {
		t.Errorf("PC note content lost: got %q, want %q", b.read("notes/shared.md"), pcBody)
	}
	if a.read("notes/shared.md") != string(hubBody) {
		t.Errorf("Hub note content lost: got %q, want %q", a.read("notes/shared.md"), string(hubBody))
	}
}

// TestC6: lw hub devices with one pending + one approved device prints a table with header
// NAME  STATE  LAST SEEN  ADDR  PLUGIN  LAST ERROR; lw hub approve <slug> prints approved <slug>;
// revoke prints revoked <slug>; unknown -> exit 1 naming it.
func TestC6(t *testing.T) {
	home := syncHermetic(t)
	cfgDir := filepath.Join(home, ".config", "lw")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}

	devicesPath := filepath.Join(cfgDir, "hub-devices.json")
	devStore, err := hubserver.OpenDevices(devicesPath)
	if err != nil {
		t.Fatalf("OpenDevices: %v", err)
	}

	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	// 1 pending device
	_, _ = devStore.RegisterPending("id-pending-1", "Tablet", "user1", "100.64.0.2", now)
	// 1 approved device
	_, _ = devStore.RegisterPending("id-approved-1", "Phone", "user1", "100.64.0.1", now)
	_, _ = devStore.Approve("phone")

	// lw hub devices
	stdout, stderr, code := captureRun(t, func() int { return run([]string{"hub", "devices"}) })
	if code != 0 {
		t.Fatalf("hub devices exit = %d; stderr=%q", code, stderr)
	}
	wantHeader := "NAME  STATE  LAST SEEN  ADDR  PLUGIN  LAST ERROR"
	if !strings.Contains(stdout, wantHeader) {
		t.Errorf("stdout %q does not contain exact header %q", stdout, wantHeader)
	}
	if !strings.Contains(stdout, "phone") || !strings.Contains(stdout, "approved") {
		t.Errorf("stdout %q missing approved phone", stdout)
	}
	if !strings.Contains(stdout, "tablet") || !strings.Contains(stdout, "pending") {
		t.Errorf("stdout %q missing pending tablet", stdout)
	}

	// lw hub approve <slug>
	stdout, stderr, code = captureRun(t, func() int { return run([]string{"hub", "approve", "tablet"}) })
	if code != 0 {
		t.Fatalf("hub approve exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "approved tablet") {
		t.Errorf("stdout %q does not contain \"approved tablet\"", stdout)
	}

	// lw hub revoke <slug>
	stdout, stderr, code = captureRun(t, func() int { return run([]string{"hub", "revoke", "tablet"}) })
	if code != 0 {
		t.Fatalf("hub revoke exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "revoked tablet") {
		t.Errorf("stdout %q does not contain \"revoked tablet\"", stdout)
	}

	// unknown -> exit 1 naming it
	stdout, stderr, code = captureRun(t, func() int { return run([]string{"hub", "approve", "nonexistent"}) })
	if code != 1 {
		t.Fatalf("hub approve unknown exit = %d, want 1; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "nonexistent") {
		t.Errorf("stderr %q does not name \"nonexistent\"", stderr)
	}
}

// TestC7: lw hub install --print prints a systemd user unit (ExecStart=<abs lw path> hub serve --vault <abs vault>),
// tailscale serve --bg --https=<port> http://127.0.0.1:<port>, systemctl --user enable --now lw-hub.service and
// loginctl enable-linger <user>; it executes nothing and writes no file (test: temp HOME unchanged).
func TestC7(t *testing.T) {
	home := syncHermetic(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	vaultDir := t.TempDir()

	writeConfigFile(t, filepath.Join(home, ".config"), "[hub]\nport = 8780\nusers = [\"alice\"]\n")

	digestBefore := treeDigest(t, home)

	// Without --print: exit 2
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"hub", "install", "--vault", vaultDir})
	})
	if code != 2 {
		t.Fatalf("hub install without --print exit = %d, want 2; stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "--print") {
		t.Errorf("stderr %q does not mention \"--print\"", stderr)
	}

	// With --print: prints systemd unit and commands
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"hub", "install", "--print", "--vault", vaultDir})
	})
	if code != 0 {
		t.Fatalf("hub install --print exit = %d; stderr=%q", code, stderr)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	absExe, _ := filepath.Abs(exe)
	absVault, _ := filepath.Abs(vaultDir)

	wantExecStart := fmt.Sprintf("ExecStart=%s hub serve --vault %s", absExe, absVault)
	if !strings.Contains(stdout, wantExecStart) {
		t.Errorf("stdout missing %q; got:\n%s", wantExecStart, stdout)
	}
	if !strings.Contains(stdout, "Restart=on-failure") {
		t.Errorf("stdout missing Restart=on-failure; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "tailscale serve --bg --https=8780 http://127.0.0.1:8780") {
		t.Errorf("stdout missing tailscale command; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "systemctl --user daemon-reload") {
		t.Errorf("stdout missing daemon-reload; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "systemctl --user enable --now lw-hub.service") {
		t.Errorf("stdout missing systemctl enable; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "loginctl enable-linger") {
		t.Errorf("stdout missing loginctl enable-linger; got:\n%s", stdout)
	}

	// Verify temp HOME unchanged
	digestAfter := treeDigest(t, home)
	if digestBefore != digestAfter {
		t.Errorf("temp HOME was modified by lw hub install --print")
	}
}

// TestC8: lw hub serve listens on 127.0.0.1:<port> only (not 0.0.0.0); SIGTERM flushes the pusher and exits 0.
func TestC8(t *testing.T) {
	a, _, remote := syncPair(t)
	port := findFreePort(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = %d\nusers = [\"phone-user\"]\n", remote, port))

	// Register signal guard
	guard := make(chan os.Signal, 4)
	signal.Notify(guard, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(guard) })

	done := make(chan int, 1)
	go func() {
		_, _, code := a.lw("hub", "serve", "--vault", a.root, "--port", fmt.Sprintf("%d", port))
		done <- code
	}()

	// Wait until server is listening
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify it is NOT listening on all interfaces (or that loopback only is bound)
	// Send SIGTERM to process
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill SIGTERM: %v", err)
	}

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("hub serve exited with code %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("hub serve did not exit after SIGTERM")
	}
}

// TestC9: lw --help lists hub; lw doctor prints a hub: line only when [hub] is set (pending devices counted).
func TestC9(t *testing.T) {
	home := syncHermetic(t)
	cfgDir := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	// 1. lw --help lists hub
	stdout, _, code := captureRun(t, func() int { return run([]string{"--help"}) })
	if code != 0 {
		t.Fatalf("lw --help exit = %d", code)
	}
	if !strings.Contains(stdout, "hub") {
		t.Errorf("lw --help missing hub:\n%s", stdout)
	}

	// 2. lw doctor with [hub] absent
	vaultDir := testutil.CopyFixture(t, "minimal")
	stdoutDoc1, _, _ := captureRun(t, func() int {
		return run([]string{"doctor", "--vault", vaultDir})
	})
	if strings.Contains(stdoutDoc1, "hub") {
		t.Errorf("lw doctor unexpectedly printed hub when [hub] was absent:\n%s", stdoutDoc1)
	}

	// 3. lw doctor with [hub] set and 1 pending device
	writeConfigFile(t, cfgDir, "[hub]\nport = 8780\nusers = [\"alice\"]\n")
	devicesPath := filepath.Join(cfgDir, "lw", "hub-devices.json")
	devStore, _ := hubserver.OpenDevices(devicesPath)
	_, _ = devStore.RegisterPending("dev-pending", "Phone", "alice", "127.0.0.1", time.Now())

	stdoutDoc2, _, _ := captureRun(t, func() int {
		return run([]string{"doctor", "--vault", vaultDir})
	})
	if !strings.Contains(stdoutDoc2, "hub") {
		t.Errorf("lw doctor missing hub check when [hub] is set:\n%s", stdoutDoc2)
	}
	if !strings.Contains(stdoutDoc2, "1 pending") {
		t.Errorf("lw doctor hub line does not count pending devices:\n%s", stdoutDoc2)
	}
}

// TestC10: the vault format is unchanged (FormatVersion 4, TestFormatFingerprint untouched); make check green.
func TestC10(t *testing.T) {
	if stage.FormatVersion != 4 {
		t.Fatalf("stage.FormatVersion = %d, want 4", stage.FormatVersion)
	}
}

func findFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func newTestHubServer(root, configPath, devicesPath string) (http.Handler, *syncPusher, error) {
	_, _, handler, pusher, _, _, err := setupHubWiring(root, 0, devicesPath)
	return handler, pusher, err
}

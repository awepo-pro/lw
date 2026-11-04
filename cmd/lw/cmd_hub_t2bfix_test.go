package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/hubserver"
)

// TestReceiptCommitPaths verifies whether receipt import creates a commit
// touching a tracked path outside CheckPath (e.g. .llmwiki/storage.json).
func TestReceiptCommitPaths(t *testing.T) {
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

	cmdDiff := exec.Command("git", "-C", a.root, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	outDiff, err := cmdDiff.CombinedOutput()
	if err != nil {
		t.Fatalf("diff-tree: %v", err)
	}
	t.Logf("DIFF TREE HEAD:\n%s", string(outDiff))
}

// TestT2bFix1a: C11 race + an orphaned in-scope write (write notes/orphan.md into the hub tree, uncommitted)
// -> after the heal remote main has notes/orphan.md (red: missing).
func TestT2bFix1a(t *testing.T) {
	a, b, remote := syncPair(t)
	a.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n[hub]\nport = 8780\nusers = [\"phone-user\"]\n", remote))
	b.setConfig(fmt.Sprintf("[sync]\nremotes = [%q]\n", remote))
	a.act()

	devicesPath := filepath.Join(a.cfg, "hub-devices.json")
	_, engine, hServer, _, _, _, err := setupHubWiring(a.root, 0, devicesPath)
	if err != nil {
		t.Fatalf("setupHubWiring: %v", err)
	}

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

	orphanContent := "# Orphan uncommitted note\n"
	putFile(t, a.root, "notes/orphan.md", orphanContent)

	_, o, err := syncSetup(a.root, syncModeWrite)
	if err != nil {
		t.Fatal(err)
	}
	syncOpts := o
	syncOpts.Interactive = false
	syncOpts.Stderr = io.Discard
	syncOpts.Quiesce = quiesceVault(a.root)
	syncOpts.AppendOnly = syncAppendOnly

	ctx := context.Background()
	healed, healErr := healHubDivergence(ctx, a.root, syncOpts, engine)
	if healErr != nil {
		t.Fatalf("healHubDivergence: %v", healErr)
	}
	if !healed {
		t.Fatalf("healHubDivergence want true, got false")
	}

	if _, err := syncPush(ctx, syncOpts); err != nil {
		t.Fatalf("syncPush: %v", err)
	}

	remoteOrphan := gitIn(t, remote, "cat-file", "blob", "refs/heads/main:notes/orphan.md")
	if strings.TrimSpace(remoteOrphan) != strings.TrimSpace(orphanContent) {
		t.Fatalf("remote main notes/orphan.md = %q, want %q", remoteOrphan, orphanContent)
	}
}

// TestT2bFix1b: C11 race + an uncommitted wiki/hand.md in the hub tree
// -> no heal: marker present, a PUT answers 503, wiki/hand.md still on disk unchanged,
// no lw-diverged-* branch (red: healed and wiki/hand.md gone from the tree).
func TestT2bFix1b(t *testing.T) {
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

	handContent := "# Hand manual edit\n"
	putFile(t, a.root, "wiki/hand.md", handContent)

	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	// Marker present
	if !isHubDiverged(a.root) {
		t.Fatalf("hub should be diverged, but marker is absent")
	}

	// Next write answers 503
	putReq2 := httptest.NewRequest("PUT", "/v1/file?path=notes/next.md&base=new", bytes.NewReader([]byte("next")))
	putReq2.RemoteAddr = "127.0.0.1:12345"
	putReq2.Header.Set("Tailscale-User-Login", "phone-user")
	putReq2.Header.Set("X-LW-Device", devID)
	putReq2.Header.Set("X-LW-Device-Name", "phone")
	putRec2 := httptest.NewRecorder()
	hServer.ServeHTTP(putRec2, putReq2)
	if putRec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT while diverged code = %d, want 503; body=%s", putRec2.Code, putRec2.Body.String())
	}

	// wiki/hand.md still on disk unchanged
	diskContent, err := os.ReadFile(filepath.Join(a.root, "wiki/hand.md"))
	if err != nil {
		t.Fatalf("wiki/hand.md missing from disk: %v", err)
	}
	if string(diskContent) != handContent {
		t.Fatalf("wiki/hand.md content = %q, want %q", string(diskContent), handContent)
	}

	// No lw-diverged-* branch
	branchOut := gitIn(t, a.root, "branch", "--list", "lw-diverged-*")
	if strings.TrimSpace(branchOut) != "" {
		t.Fatalf("unexpected lw-diverged-* branch present:\n%s", branchOut)
	}
}

// TestT2bFix1c: C11 race where a local "hub: sweep" commit touches wiki/hand.md
// -> no heal, same assertions (red: healed).
func TestT2bFix1c(t *testing.T) {
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

	handContent := "# Hand manual edit\n"
	putFile(t, a.root, "wiki/hand.md", handContent)
	// Commit with subject "hub: sweep"
	gitIn(t, a.root, "add", "-A")
	gitIn(t, a.root, "commit", "-m", "hub: sweep")

	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	// Marker present
	if !isHubDiverged(a.root) {
		t.Fatalf("hub should be diverged, but marker is absent")
	}

	// Next write answers 503
	putReq2 := httptest.NewRequest("PUT", "/v1/file?path=notes/next.md&base=new", bytes.NewReader([]byte("next")))
	putReq2.RemoteAddr = "127.0.0.1:12345"
	putReq2.Header.Set("Tailscale-User-Login", "phone-user")
	putReq2.Header.Set("X-LW-Device", devID)
	putReq2.Header.Set("X-LW-Device-Name", "phone")
	putRec2 := httptest.NewRecorder()
	hServer.ServeHTTP(putRec2, putReq2)
	if putRec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT while diverged code = %d, want 503; body=%s", putRec2.Code, putRec2.Body.String())
	}

	// wiki/hand.md still on disk unchanged
	diskContent, err := os.ReadFile(filepath.Join(a.root, "wiki/hand.md"))
	if err != nil {
		t.Fatalf("wiki/hand.md missing from disk: %v", err)
	}
	if string(diskContent) != handContent {
		t.Fatalf("wiki/hand.md content = %q, want %q", string(diskContent), handContent)
	}

	// No lw-diverged-* branch
	branchOut := gitIn(t, a.root, "branch", "--list", "lw-diverged-*")
	if strings.TrimSpace(branchOut) != "" {
		t.Fatalf("unexpected lw-diverged-* branch present:\n%s", branchOut)
	}
}

// TestT2bFix1d: C11 race where the hub's local-only commits include a receipt PUT
// (ledger/receipts/...jpg -> storage.json changed) AND the PC side added a different storage entry
// and pushed; after the heal the remote's storage.json lists BOTH the PC's entry and the receipt,
// no marker, writes 200. Red before = heal refused (or the receipt missing from the catalog).
func TestT2bFix1d(t *testing.T) {
	a, b, remote, hServer, pusher, devID := setupA6Test(t)

	// Initial commit to establish main
	initContent := "# Initial\n"
	putFile(t, a.root, "notes/init.md", initContent)
	syncCommitAndPush(t, a, remote, "initial")
	b.lw("sync", "--vault", b.root)

	// PC side adds a storage entry and pushes
	tempDir := t.TempDir()
	pcDoc := filepath.Join(tempDir, "pc.pdf")
	if err := os.WriteFile(pcDoc, []byte("%PDF-1.4 pc file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errs, code := b.lw("storage", "add", pcDoc, "--name", "docs/pc.pdf", "--vault", b.root)
	if code != 0 {
		t.Fatalf("b storage add exit %d; out:\n%s\nerrs:\n%s", code, out, errs)
	}
	out, errs, code = b.lw("sync", "--vault", b.root)
	if code != 0 {
		t.Fatalf("b sync exit %d; out:\n%s\nerrs:\n%s", code, out, errs)
	}

	disableHubRefreshForTest(true)
	defer disableHubRefreshForTest(false)

	// Hub side PUTs a receipt
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

	// After heal: no marker
	if isHubDiverged(a.root) {
		t.Fatalf("hub should not be diverged after heal")
	}

	// Remote storage.json lists BOTH the PC's entry and the receipt
	remoteCatalog := gitIn(t, remote, "cat-file", "blob", "refs/heads/main:.llmwiki/storage.json")
	if !strings.Contains(remoteCatalog, "docs/pc.pdf") {
		t.Fatalf("remote storage.json missing PC entry docs/pc.pdf:\n%s", remoteCatalog)
	}
	if !strings.Contains(remoteCatalog, "receipts/2026/11/receipt1.jpg") {
		t.Fatalf("remote storage.json missing receipt receipts/2026/11/receipt1.jpg:\n%s", remoteCatalog)
	}

	// Next write answers 200
	putReq2 := httptest.NewRequest("PUT", "/v1/file?path=notes/next.md&base=new", bytes.NewReader([]byte("# Next\n")))
	putReq2.RemoteAddr = "127.0.0.1:12345"
	putReq2.Header.Set("Tailscale-User-Login", "phone-user")
	putReq2.Header.Set("X-LW-Device", devID)
	putReq2.Header.Set("X-LW-Device-Name", "phone")
	putRec2 := httptest.NewRecorder()
	hServer.ServeHTTP(putRec2, putReq2)
	if putRec2.Code != http.StatusOK {
		t.Fatalf("PUT next.md code = %d, want 200; body=%s", putRec2.Code, putRec2.Body.String())
	}
}

// TestT2bFix2: conflictCopyPattern also matches the same-second sequence suffix
// (<stem>.conflict-<slug>-<YYYYMMDD-HHMMSS>-<n><ext>).
// Test: a -2 copy is listed by lw hub status and lw sync (red).
func TestT2bFix2(t *testing.T) {
	a, _, _ := syncPair(t)
	a.act()

	seqConflict := "notes/doc.conflict-pixel-20261103-102030-2.md"
	putFile(t, a.root, seqConflict, "# Conflicted seq 2\n")

	conflicts := findConflictCopies(a.root)
	found := false
	for _, c := range conflicts {
		if c == seqConflict {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("findConflictCopies(%q) = %v; missing %s", a.root, conflicts, seqConflict)
	}

	stdout, _, code := a.lw("hub", "status")
	if code != 0 {
		t.Fatalf("lw hub status exit %d, want 0; stdout:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "conflicts: 1") {
		t.Fatalf("lw hub status stdout missing 'conflicts: 1':\n%s", stdout)
	}

	stdoutSync, _, _ := a.lw("sync", "--vault", a.root)
	if !strings.Contains(stdoutSync, seqConflict) {
		t.Fatalf("lw sync stdout missing sequenced conflict copy %s:\n%s", seqConflict, stdoutSync)
	}
}

// TestT2bFix3: findConflictCopies descends only into notes/ and ledger/ (plus files directly at the root);
// everything else at the top level is skipped with SkipDir.
// Test: a conflict-named file under raw/ deep in a directory the walk would otherwise read is not visited.
func TestT2bFix3(t *testing.T) {
	a, _, _ := syncPair(t)
	a.act()

	rawConflict := "raw/nested/deep/doc.conflict-pixel-20261103-102030.md"
	putFile(t, a.root, rawConflict, "# Fake conflict in raw\n")

	var visited []string
	testConflictCopiesWalkHook = func(path string) {
		rel, err := filepath.Rel(a.root, path)
		if err == nil {
			visited = append(visited, filepath.ToSlash(rel))
		}
	}
	t.Cleanup(func() { testConflictCopiesWalkHook = nil })

	conflicts := findConflictCopies(a.root)
	if len(conflicts) != 0 {
		t.Fatalf("findConflictCopies found out-of-scope files: %v", conflicts)
	}

	for _, v := range visited {
		if strings.HasPrefix(v, "raw/nested") {
			t.Fatalf("findConflictCopies descended into %s; should have skipped top-level raw/", v)
		}
	}
}

// TestT2bFix4: syncRun prints the conflict copies even when a storage issue makes it return an error afterwards
// (print first, then issues.finish). Test with an existing storage-failure helper (red: lines missing).
func TestT2bFix4(t *testing.T) {
	r := newS066Rig(t, false)
	a := r.a

	conflictFile := "notes/note.conflict-phone-20261104-120000.md"
	putFile(t, a.root, conflictFile, "# Conflict to be reported\n")

	s066Unwritable(t, r.storeDir)
	s066AutoSync(t, a, false)
	r.store(t, a, "h", "bytes that cannot be uploaded\n")

	stdout, stderr, code := s066Sync(a)
	if code != 1 {
		t.Fatalf("lw sync exit %d, want 1; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, conflictFile) {
		t.Fatalf("lw sync stdout missing conflict copy %s; stdout:\n%s\nstderr:\n%s", conflictFile, stdout, stderr)
	}
}

// TestT2bFix5: the slug used for a re-applied path is the slug of the NEWEST local commit that touched it.
// Test: two hub commits by phone-a then phone-b on notes/list.md, race -> the conflict copy name contains phone-b (red: phone-a).
func TestT2bFix5(t *testing.T) {
	a, b, remote, hServer, pusher, devIDA := setupA6Test(t)

	// Register phone-b device as well
	devStore, err := hubserver.OpenDevices(filepath.Join(a.cfg, "hub-devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	devIDB := "device-phone-b"
	_, _ = devStore.RegisterPending(devIDB, "phone-b", "phone-user", "127.0.0.1", time.Now())
	_, _ = devStore.Approve("phone-b")

	initContent := "# Initial list\n"
	putFile(t, a.root, "notes/list.md", initContent)
	syncCommitAndPush(t, a, remote, "initial list")
	b.lw("sync", "--vault", b.root)

	initBytes, err := os.ReadFile(filepath.Join(a.root, "notes/list.md"))
	if err != nil {
		t.Fatal(err)
	}
	oldSHA := sha256Hex(initBytes)

	// PC edits notes/list.md
	pcContent := "# PC edit on list\n"
	putFile(t, b.root, "notes/list.md", pcContent)
	syncCommitAndPush(t, b, remote, "pc edit on list")

	disableHubRefreshForTest(true)
	defer disableHubRefreshForTest(false)

	// phone-a edits notes/list.md
	contentA := "# Phone-A edit on list\n"
	putReqA := httptest.NewRequest("PUT", "/v1/file?path=notes/list.md&base="+oldSHA, bytes.NewReader([]byte(contentA)))
	putReqA.RemoteAddr = "127.0.0.1:12345"
	putReqA.Header.Set("Tailscale-User-Login", "phone-user")
	putReqA.Header.Set("X-LW-Device", devIDA)
	putReqA.Header.Set("X-LW-Device-Name", "phone-a")
	putRecA := httptest.NewRecorder()
	hServer.ServeHTTP(putRecA, putReqA)
	if putRecA.Code != http.StatusOK {
		t.Fatalf("PUT A code = %d, want 200; body=%s", putRecA.Code, putRecA.Body.String())
	}
	var respA map[string]any
	_ = json.Unmarshal(putRecA.Body.Bytes(), &respA)
	shaA := respA["sha256"].(string)

	// phone-b edits notes/list.md on top of shaA
	contentB := "# Phone-B edit on list\n"
	putReqB := httptest.NewRequest("PUT", "/v1/file?path=notes/list.md&base="+shaA, bytes.NewReader([]byte(contentB)))
	putReqB.RemoteAddr = "127.0.0.1:12345"
	putReqB.Header.Set("Tailscale-User-Login", "phone-user")
	putReqB.Header.Set("X-LW-Device", devIDB)
	putReqB.Header.Set("X-LW-Device-Name", "phone-b")
	putRecB := httptest.NewRecorder()
	hServer.ServeHTTP(putRecB, putReqB)
	if putRecB.Code != http.StatusOK {
		t.Fatalf("PUT B code = %d, want 200; body=%s", putRecB.Code, putRecB.Body.String())
	}

	pusher.Trigger()
	pusher.Flush(20*time.Second, nil)

	// Look at the conflict copy on remote main
	treeOut := gitIn(t, remote, "ls-tree", "-r", "--name-only", "refs/heads/main", "notes")
	var conflictFile string
	for _, f := range strings.Split(treeOut, "\n") {
		f = strings.TrimSpace(f)
		if strings.HasPrefix(f, "notes/list.conflict-") && strings.HasSuffix(f, ".md") {
			conflictFile = f
			break
		}
	}
	if conflictFile == "" {
		t.Fatalf("remote main missing notes/list.conflict-*.md; ls-tree output:\n%s", treeOut)
	}

	if !strings.Contains(conflictFile, "phone-b") {
		t.Fatalf("conflict copy filename %s does not contain phone-b (attributed to older commit author?)", conflictFile)
	}
}

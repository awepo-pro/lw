package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestH1Fix1 Startup pull:
// The C11 race left diverged with the marker present (Refresh disabled while creating it,
// then re-enabled); calling the startup hook heals it with NO HTTP request: marker gone,
// remote main has the conflict copy.
func TestH1Fix1(t *testing.T) {
	a, b, remote, hServer, _, devID := setupA6Test(t)

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

	// Divergence created. Mark diverged and re-enable Refresh.
	markHubDiverged(a.root, "diverged from remote")
	disableHubRefreshForTest(false)

	if !isHubDiverged(a.root) {
		t.Fatalf("hub should be marked diverged")
	}

	// Calling the startup hook heals it with NO HTTP request
	if hubStartupHook == nil {
		t.Fatalf("hubStartupHook is nil; startup pull hook not wired")
	}

	if err := hubStartupHook(context.Background()); err != nil {
		t.Fatalf("hubStartupHook returned error: %v", err)
	}

	if isHubDiverged(a.root) {
		t.Fatalf("hub marked as diverged, want marker cleared by startup hook")
	}

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
}

// TestH1Fix2: `lw hub status` lists conflict copies:
// after the existing lines, `conflicts: <n>` and, for each, one line `  <path>` (sorted).
// Test: a vault with two conflict copies and one decoy (notes/conflict-notes.md) ->
// `conflicts: 2` and both paths, not the decoy; with none -> `conflicts: 0`.
func TestH1Fix2(t *testing.T) {
	a, _, _ := syncPair(t)

	// 1. With none -> conflicts: 0
	stdout, stderr, code := a.lw("hub", "status", "--vault", a.root)
	if code != 0 {
		t.Fatalf("hub status clean vault exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "conflicts: 0") {
		t.Fatalf("hub status clean vault missing \"conflicts: 0\"; stdout:\n%s", stdout)
	}

	// 2. Add two conflict copies and one decoy
	decoy := "notes/conflict-notes.md"
	conflict1 := "notes/foo.conflict-phone-20261104-123456.md"
	conflict2 := "notes/bar.conflict-tablet-20261104-123457.md"
	putFile(t, a.root, decoy, "# Decoy\n")
	putFile(t, a.root, conflict1, "# Conflict 1\n")
	putFile(t, a.root, conflict2, "# Conflict 2\n")

	// Verify finder directly
	found := findConflictCopies(a.root)
	wantFound := []string{conflict2, conflict1} // bar before foo
	if strings.Join(found, ",") != strings.Join(wantFound, ",") {
		t.Fatalf("findConflictCopies(%s) = %v, want %v", a.root, found, wantFound)
	}

	stdout, stderr, code = a.lw("hub", "status", "--vault", a.root)
	if code != 0 {
		t.Fatalf("hub status exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "conflicts: 2") {
		t.Fatalf("hub status missing \"conflicts: 2\"; stdout:\n%s", stdout)
	}
	if strings.Contains(stdout, decoy) {
		t.Fatalf("hub status includes decoy %q; stdout:\n%s", decoy, stdout)
	}
	wantLines := "conflicts: 2\n  notes/bar.conflict-tablet-20261104-123457.md\n  notes/foo.conflict-phone-20261104-123456.md"
	if !strings.Contains(stdout, wantLines) {
		t.Fatalf("hub status missing expected sorted conflict lines:\n%s\ngot stdout:\n%s", wantLines, stdout)
	}
}

// TestH1Fix3: `lw sync` (the plain sync verb, after a successful sync) prints,
// only when conflict copies exist, to STDOUT:
// `<n> conflict cop(y|ies) to resolve: merge each into its note, then delete it:`
// followed by `  <path>` lines.
// Nothing printed when there are none (so every existing sync test output is unchanged).
// Test: sync a clone whose remote carries a conflict copy -> the lines appear; a clean vault -> output unchanged.
func TestH1Fix3(t *testing.T) {
	a, b, remote := syncPair(t)

	// 1. Clean vault -> output unchanged (no conflict lines)
	stdout, stderr, code := b.lw("sync", "--vault", b.root)
	if code != 0 {
		t.Fatalf("clean sync exit = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "conflict") {
		t.Fatalf("clean sync stdout unexpectedly contains \"conflict\":\n%s", stdout)
	}

	// 2. Remote carries 1 conflict copy
	conflict1 := "notes/page.conflict-phone-20261104-123456.md"
	putFile(t, a.root, conflict1, "# Conflict Copy\n")
	gitIn(t, a.root, "add", "-A")
	gitIn(t, a.root, "commit", "-m", "add conflict copy 1")
	gitIn(t, a.root, "push", remote, "HEAD:refs/heads/main")

	stdout, stderr, code = b.lw("sync", "--vault", b.root)
	if code != 0 {
		t.Fatalf("sync exit = %d, stderr = %q", code, stderr)
	}
	wantSingle := "1 conflict copy to resolve: merge each into its note, then delete it:\n  notes/page.conflict-phone-20261104-123456.md"
	if !strings.Contains(stdout, wantSingle) {
		t.Fatalf("sync stdout missing single conflict copy warning:\n%s\ngot stdout:\n%s", wantSingle, stdout)
	}

	// 3. Remote carries a second conflict copy (plural: copies)
	conflict2 := "notes/alpha.conflict-phone-20261104-123457.md"
	putFile(t, a.root, conflict2, "# Conflict Copy 2\n")
	gitIn(t, a.root, "add", "-A")
	gitIn(t, a.root, "commit", "-m", "add conflict copy 2")
	gitIn(t, a.root, "push", remote, "HEAD:refs/heads/main")

	stdout, stderr, code = b.lw("sync", "--vault", b.root)
	if code != 0 {
		t.Fatalf("sync exit = %d, stderr = %q", code, stderr)
	}
	wantPlural := "2 conflict copies to resolve: merge each into its note, then delete it:\n  notes/alpha.conflict-phone-20261104-123457.md\n  notes/page.conflict-phone-20261104-123456.md"
	if !strings.Contains(stdout, wantPlural) {
		t.Fatalf("sync stdout missing plural conflict copies warning:\n%s\ngot stdout:\n%s", wantPlural, stdout)
	}
}

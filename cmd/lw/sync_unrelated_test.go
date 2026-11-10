package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSyncRefusesUnrelatedRemote (084 B1a, E1):
// Two independent syncPairs; point A1's config at pair 2's remote (and remove
// A1's .llmwiki/sync.json so only this guard is tested); lw sync exits 1 with
// "<remote> holds a different vault (no commit in common with this one); nothing was changed - check [sync] remotes in the config";
// A1's HEAD and tree digest unchanged; refs/remotes/lw/main in A1 still points
// at pair 1's commit; pair 2's remote unchanged.
func TestSyncRefusesUnrelatedRemote(t *testing.T) {
	a1, _, remote1 := syncPair(t)
	// Ensure pair 2 has a distinct commit timestamp even on fast machines
	time.Sleep(1100 * time.Millisecond)
	_, _, remote2 := syncPair(t)

	// Remove A1's sync.json so B1b doesn't trigger.
	if err := os.Remove(filepath.Join(a1.root, ".llmwiki", "sync.json")); err != nil {
		t.Fatal(err)
	}
	a1.setConfig("[vault]\npath = \"" + a1.root + "\"\n[sync]\nremotes = [\"" + remote2 + "\"]\n")

	head1 := a1.git("rev-parse", "HEAD")
	track1 := a1.git("rev-parse", "refs/remotes/lw/main")
	digest1 := treeDigest(t, a1.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json")
	headRemote2 := remoteHead(t, remote2)

	stdout, stderr, code := a1.lw("sync")
	want := remote2 + " holds a different vault (no commit in common with this one); nothing was changed - check [sync] remotes in the config"
	if code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("A1 sync: exit %d\nstdout %q\nstderr %q\nwant in stderr %q", code, stdout, stderr, want)
	}

	if gotHead := a1.git("rev-parse", "HEAD"); gotHead != head1 {
		t.Errorf("A1 HEAD changed: %s -> %s", head1, gotHead)
	}
	if gotDigest := treeDigest(t, a1.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json"); gotDigest != digest1 {
		t.Errorf("A1 tree digest changed")
	}
	if gotTrack := a1.git("rev-parse", "refs/remotes/lw/main"); gotTrack != track1 {
		t.Errorf("A1 tracking ref changed: %s -> %s (remote1 was %s)", track1, gotTrack, remote1)
	}
	if gotHead2 := remoteHead(t, remote2); gotHead2 != headRemote2 {
		t.Errorf("remote2 changed: %s -> %s", headRemote2, gotHead2)
	}
}

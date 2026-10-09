package main

// sync_rebase_test.go pins, at the verb level, 042 A-042-8: a divergence that
// conflicts with nothing is rebased and pushed — the notes written offline stay
// in the work tree, where they were — while a divergence that does conflict, and
// above all two lw commits, is refused as before.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// offlineNotes takes n notes on pc while its remote is unreachable: each is
// saved and committed to git locally, its push failing safely. It restores the
// config afterwards.
func offlineNotes(t *testing.T, pc *syncPC, n int) {
	t.Helper()
	good := pc.config()
	pc.setConfig("[vault]\npath = \"" + pc.root + "\"\n[sync]\nremotes = [\"" + deadRemote(t) + "\"]\n")
	for i := 0; i < n; i++ {
		noteAt(t, i)
		if _, stderr, code := pc.lw("note", "-m", "offline "+string(rune('a'+i)), "--vault", pc.root); code != 0 {
			t.Fatalf("offline note %d: exit %d stderr %q", i, code, stderr)
		}
	}
	pc.setConfig(good)
	if got := pc.git("rev-list", "--count", "refs/remotes/lw/main..HEAD"); got != itoaInt(n) {
		t.Fatalf("setup: %s commit(s) ahead, want %d", got, n)
	}
}

func itoaInt(n int) string { return string(rune('0' + n)) }

// TestSyncRebasesACleanDivergence is the case that made the rule too strict: PC-A
// wrote 2 notes while offline, PC-B pushed 1. lw sync prints what it did, and
// A's notes are still in A's work tree — and on the server — afterwards.
func TestSyncRebasesACleanDivergence(t *testing.T) {
	a, b, remote := syncPair(t)
	settle(t, a)
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B could not catch up")
	}
	offlineNotes(t, a, 2)
	b.write("notes/20261009-140000-from-b.md", "from b\n")
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B could not push")
	}
	notes := []string{"notes/20261009-120000-offline-a.md", "notes/20261009-120100-offline-b.md", "notes/20261009-140000-from-b.md"}

	stdout, stderr, code := a.lw("sync")
	want := "pulled 1 commit(s) from " + remote + "\n" +
		"rebased 2 local commit(s) onto " + remote + "\n" +
		"pushed 2 commit(s) to " + remote + "\n"
	if code != 0 || stdout != want {
		t.Fatalf("A sync: exit %d\nstdout %q\nwant   %q\nstderr %q", code, stdout, want, stderr)
	}
	for _, n := range notes {
		if a.read(n) == "" {
			t.Errorf("A lost %s", n)
		}
		if remoteFile(t, remote, n) == "" {
			t.Errorf("the remote lacks %s", n)
		}
	}
	if got := a.git("rev-list", "--merges", "--count", "HEAD"); got != "0" {
		t.Errorf("the history has %s merge commit(s)", got)
	}
	if stdout, _, code := b.lw("sync"); code != 0 || stdout != "pulled 2 commit(s) from "+remote+"\n" {
		t.Errorf("B sync: exit %d stdout %q", code, stdout)
	}
	for _, n := range notes {
		if b.read(n) == "" {
			t.Errorf("B lacks %s", n)
		}
	}
}

// TestAutoStartRebasesAndPushes: the same through a verb's start: the pull
// rebases, says so, and the push the verb asks for at its end sends the
// replayed commits.
func TestAutoStartRebasesAndPushes(t *testing.T) {
	a, b, remote := syncPair(t)
	settle(t, a)
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B could not catch up")
	}
	offlineNotes(t, a, 2)
	b.write("notes/20261009-140000-from-b.md", "from b\n")
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B could not push")
	}
	stdout, stderr, code := a.lw("lint", "--fix", "--vault", a.root)
	want := "sync: pulled 1 commit(s) from " + remote + "\n" +
		"sync: rebased 2 local commit(s) onto " + remote + "\n" +
		"sync: pushed 2 commit(s) to " + remote + "\n"
	if code != 0 || stdout != "clean\n" || stderr != want {
		t.Fatalf("exit %d stdout %q\nstderr %q\nwant   %q", code, stdout, stderr, want)
	}
	if a.read("notes/20261009-140000-from-b.md") == "" || remoteFile(t, remote, "notes/20261009-120000-offline-a.md") == "" {
		t.Error("the notes did not all end up everywhere")
	}
}

// TestPushPathRebases: the remote moves between a verb's pull and its push —
// the push the commit hook makes finds a divergence, rebases, and sends.
func TestPushPathRebases(t *testing.T) {
	a, _, remote := syncPair(t)
	openCreatePageChangeset(t, a.root, "wiki/concepts/first-commit.md", "First Commit")
	a.act()
	origPull := syncPull
	t.Cleanup(func() { syncPull = origPull })
	moved := false
	syncPull = func(ctx context.Context, o vaultsync.Options, maxFormat int) (vaultsync.State, error) {
		st, err := origPull(ctx, o, maxFormat)
		if !moved {
			moved = true // another PC pushes right after A's start-of-verb pull
			pushFromScratch(t, remote, "notes/20261009-140000-from-b.md", "from b\n", "lw notes")
		}
		return st, err
	}
	out, code := captureCombined(t, func() int { return run([]string{"commit", "--vault", a.root, "-m", "first page"}) })
	want := "committed 000001\n" +
		"sync: pulled 1 commit(s) from " + remote + "\n" +
		"sync: rebased 1 local commit(s) onto " + remote + "\n" +
		"sync: pushed 1 commit(s) to " + remote + "\n"
	if code != 0 || out != want {
		t.Fatalf("exit %d\noutput %q\nwant   %q", code, out, want)
	}
	if remoteFile(t, remote, "wiki/concepts/first-commit.md") == "" || a.read("notes/20261009-140000-from-b.md") == "" {
		t.Error("the page or the other PC's note is missing")
	}
}

// TestSyncStillRefusesAConflict: the same page edited on both sides is the
// refusal it always was, and A's tree is exactly as it was.
func TestSyncStillRefusesAConflict(t *testing.T) {
	a, b, remote := syncPair(t)
	a.appendTo(kvPage, "\nA's edit.\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A could not push")
	}
	b.appendTo(kvPage, "\nB's edit.\n")
	b.git("add", "-A")
	b.git("commit", "--quiet", "-m", "lw sync")
	head := b.git("rev-parse", "HEAD")
	before := treeDigest(t, b.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json")
	_, stderr, code := b.lw("sync")
	if code != 1 || !strings.Contains(stderr, "diverged from "+remote+": this PC has 1 commit(s) the remote lacks, the remote has 1 this PC lacks") {
		t.Fatalf("exit %d stderr %q; want the divergence refused", code, stderr)
	}
	if b.git("rev-parse", "HEAD") != head || treeDigest(t, b.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json") != before {
		t.Error("the refused rebase changed B")
	}
	if got := b.git("status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("B's tree is not clean:\n%s", got)
	}
}

// TestTwoLWCommitsAreNeverRebased: each PC ran a real lw commit — the same next
// commit id, so the same snapshot file with different bytes, and both appended
// to the journal. Whatever else is clean about them, they are refused.
func TestTwoLWCommitsAreNeverRebased(t *testing.T) {
	a, b, remote := journalPair(t)

	// B commits while offline: its push fails, the commit stays local.
	good := b.config()
	b.setConfig("[vault]\npath = \"" + b.root + "\"\n[sync]\nremotes = [\"" + deadRemote(t) + "\"]\n")
	openCreatePageChangeset(t, b.root, "wiki/concepts/b-page.md", "B Page")
	if stdout, stderr, code := b.lw("commit", "--vault", b.root, "-m", "b page"); code != 0 || stdout != "committed 000002\n" {
		t.Fatalf("B commit: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	b.setConfig(good)

	openCreatePageChangeset(t, a.root, "wiki/concepts/a-page.md", "A Page")
	if stdout, stderr, code := a.lw("commit", "--vault", a.root, "-m", "a page"); code != 0 || stdout != "committed 000002\n" {
		t.Fatalf("A commit: exit %d stdout %q stderr %q", code, stdout, stderr)
	}

	head := b.git("rev-parse", "HEAD")
	before := treeDigest(t, b.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json")
	_, stderr, code := b.lw("sync")
	if code != 1 || !strings.Contains(stderr, "diverged from "+remote) {
		t.Fatalf("B sync: exit %d stderr %q; want the lw commits refused", code, stderr)
	}
	if b.git("rev-parse", "HEAD") != head || treeDigest(t, b.root, ".git", ".llmwiki/logs", ".llmwiki/sync.json") != before {
		t.Error("the refusal changed B")
	}
	// ... and at a verb's start: a warning, the verb still runs.
	// (revert of a commit that does not exist: the verb fails after its start.)
	_, stderr, code = b.lw("revert", "--vault", b.root, "000099")
	if want := "sync: diverged from " + remote + " — run lw sync; working on the local vault\n"; code != 1 || !strings.HasPrefix(stderr, want) {
		t.Errorf("revert: exit %d stderr %q; want it to begin with %q", code, stderr, want)
	}
}

// stuckRebase leaves PC pc as a crash during lw's rebase would, by hand: the
// pre-rebase ref and lw's record, the journal's tail set aside in the record,
// and git stopped on a conflict. It returns the HEAD from before the rebase.
func stuckRebase(t *testing.T, a, b *syncPC, remote string) string {
	t.Helper()
	a.appendTo(kvPage, "\nA's edit.\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A could not push")
	}
	b.appendTo(kvPage, "\nB's edit.\n")
	b.git("add", "-A")
	b.git("commit", "--quiet", "-m", "lw sync")
	pre := b.git("rev-parse", "HEAD")
	if _, _, code := b.lw("sync", "status"); code != 0 { // fetch
		t.Fatal("B could not fetch")
	}
	b.git("update-ref", "refs/lw/pre-rebase", pre)
	putFile(t, b.root, ".git/lw-rebase/state.json", `{"pre":"`+pre+`"}`+"\n")
	if _, err := gitInErr(b.root, "rebase", "--quiet", "refs/remotes/lw/main"); err == nil {
		t.Fatal("setup: the rebase did not conflict")
	}
	if _, err := os.Stat(filepath.Join(b.root, ".git", "rebase-merge")); err != nil {
		t.Fatalf("setup: no rebase in progress: %v", err)
	}
	return pre
}

// TestInterruptedRebaseIsReported: the verb that finds a rebase a dead process
// left says that it undid it, and then does its own work.
func TestInterruptedRebaseIsReported(t *testing.T) {
	t.Run("at a verb's start", func(t *testing.T) {
		a, b, remote := syncPair(t)
		pre := stuckRebase(t, a, b, remote)
		_, stderr, code := b.lw("lint", "--fix", "--vault", b.root)
		note := "sync: an interrupted rebase was undone — the vault is back at " + pre[:10] + " (refs/lw/pre-rebase) with its uncommitted journal lines put back\n"
		if code != 0 || !strings.HasPrefix(stderr, note) {
			t.Fatalf("exit %d stderr %q; want it to begin with %q", code, stderr, note)
		}
		if _, err := os.Stat(filepath.Join(b.root, ".git", "rebase-merge")); err == nil {
			t.Error("the rebase is still in progress")
		}
	})
	t.Run("in lw sync", func(t *testing.T) {
		a, b, remote := syncPair(t)
		pre := stuckRebase(t, a, b, remote)
		_, stderr, _ := b.lw("sync")
		if !strings.Contains(stderr, "sync: an interrupted rebase was undone — the vault is back at "+pre[:10]) {
			t.Errorf("stderr = %q; want the note", stderr)
		}
		if b.git("rev-parse", "HEAD") != pre && !strings.Contains(stderr, "diverged") {
			t.Error("HEAD is neither back nor explained")
		}
	})
}

// TestAForeignRebaseIsLeftAlone: a rebase the user started is not lw's.
func TestAForeignRebaseIsLeftAlone(t *testing.T) {
	a, b, remote := syncPair(t)
	a.appendTo(kvPage, "\nA's edit.\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A could not push")
	}
	b.appendTo(kvPage, "\nB's edit.\n")
	b.git("add", "-A")
	b.git("commit", "--quiet", "-m", "my own commit")
	if _, _, code := b.lw("sync", "status"); code != 0 {
		t.Fatal("fetch failed")
	}
	if _, err := gitInErr(b.root, "rebase", "--quiet", "refs/remotes/lw/main"); err == nil {
		t.Fatal("setup: the rebase did not conflict")
	}
	_, stderr, code := b.lw("lint", "--fix", "--vault", b.root)
	if code != 0 || !strings.HasPrefix(stderr, "sync: a git rebase is in progress in the vault — lw did not start it;") || !strings.HasSuffix(stderr, "; working on the local vault\n") {
		t.Errorf("lint --fix: exit %d stderr %q", code, stderr)
	}
	_, stderr, code = b.lw("sync")
	if code != 1 || !strings.Contains(stderr, "a git rebase is in progress in the vault") {
		t.Errorf("sync: exit %d stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(b.root, ".git", "rebase-merge")); err != nil {
		t.Error("lw aborted a rebase it did not start")
	}
	_ = remote
}

// TestTUIExitReportsTheRebase: the pusher that rebases says so on the exit line,
// like the CLI's push does.
func TestTUIExitReportsTheRebase(t *testing.T) {
	a, _, remote := syncPair(t)
	settle(t, a)
	a.act()
	as := newTUIAutoSync(a.root, mustLoadConfig(t))
	pushFromScratch(t, remote, "notes/20261009-140000-from-b.md", "from b\n", "lw notes")
	a.write("notes/20261009-130000-late.md", "late\n")
	var line strings.Builder
	as.finish(&line)
	want := "sync: pulled 1 commit(s) from " + remote + "\n" +
		"sync: rebased 1 local commit(s) onto " + remote + "\n" +
		"sync: pushed 1 commit(s) to " + remote + "\n"
	if line.String() != want {
		t.Errorf("exit output = %q, want %q", line.String(), want)
	}
	if a.read("notes/20261009-140000-from-b.md") == "" {
		t.Error("A did not take the other PC's note")
	}
}

// TestSyncOptionsQuiesceTheJournal: the options every sync call is made with
// carry the quiesce — the exclusive side of the journal lock — and it really
// does hold journal appends off while held (A-042-9 b).
func TestSyncOptionsQuiesceTheJournal(t *testing.T) {
	a, _, _ := syncPair(t)
	a.act()
	for name, o := range map[string]vaultsync.Options{"auto-sync": (&autoSync{root: a.root, remotes: []string{"x:y"}}).options()} {
		if o.Quiesce == nil {
			t.Fatalf("%s options have no Quiesce", name)
		}
	}
	_, explicit, err := syncSetup(a.root, syncModeRead)
	if err != nil || explicit.Quiesce == nil {
		t.Fatalf("explicit sync options: Quiesce %v, %v", explicit.Quiesce != nil, err)
	}

	release, err := explicit.Quiesce()
	if err != nil {
		t.Fatal(err)
	}
	j, err := stage.OpenJournal(filepath.Join(a.root, ".llmwiki", "journal.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- j.Append(stage.Event{TS: time.Now().UTC(), Kind: stage.EvChangesetOpened, Changeset: "cs-q", Actor: stage.Author{Kind: "human"}, Message: "held-off"})
	}()
	select {
	case <-done:
		t.Fatal("a journal append went through the quiesce")
	case <-time.After(150 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(a.read(".llmwiki/journal.ndjson"), "held-off"); got != 1 {
		t.Errorf("the held-off line is in the journal %d times, want 1", got)
	}
}

// TestSyncInitAndCloneMakeTheJournalLock: a vault that joins sync has its lock
// file from then on, so appenders take their side before the first quiesce.
func TestSyncInitAndCloneMakeTheJournalLock(t *testing.T) {
	a, b, _ := syncPair(t)
	for name, pc := range map[string]*syncPC{"init": a, "clone": b} {
		if _, err := os.Stat(filepath.Join(pc.root, ".llmwiki", "tmp", "journal.lock")); err != nil {
			t.Errorf("after sync %s: %v", name, err)
		}
		if got := pc.git("ls-files", ".llmwiki/tmp"); got != "" {
			t.Errorf("after sync %s the lock is tracked: %q", name, got)
		}
	}
}

package main

// sync_s3b_test.go pins 042 A-042-7 on the cmd side: a PC in the middle of a
// changeset can still take another PC's commits (the journal's uncommitted
// lines are carried across the pull), a verb's start only pulls and never
// commits, the last remote that answered is tried first, and the vault lock
// is held for the local work and never for the network.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// journalPair is syncPair after one committed page, so both PCs track a
// journal that has lines in it and are level with the remote.
func journalPair(t *testing.T) (a, b *syncPC, remote string) {
	t.Helper()
	a, b, remote = syncPair(t)
	openCreatePageChangeset(t, a.root, "wiki/concepts/seed.md", "Seed")
	if _, stderr, code := a.lw("commit", "--vault", a.root, "-m", "seed page"); code != 0 {
		t.Fatalf("A commit: exit %d stderr %q", code, stderr)
	}
	if stdout, stderr, code := b.lw("sync"); code != 0 {
		t.Fatalf("B sync: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if b.read(".llmwiki/journal.ndjson") != a.read(".llmwiki/journal.ndjson") {
		t.Fatal("setup: the PCs' journals differ")
	}
	return a, b, remote
}

// stageIngestOnA leaves a changeset open on pc without committing it: an ingest
// with a fake agent that stages one raw source.
func stageIngestOnA(t *testing.T, pc *syncPC, rawPath string) {
	t.Helper()
	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		return &fakeStageAgent{e: e, sessions: sessions, ops: []stage.Op{{
			Kind: stage.OpIngestSource, Path: rawPath, Content: []byte("ingested body\n"), Extractor: "test-fake",
		}}}, nil
	})
	src := filepath.Join(t.TempDir(), "source.md")
	if err := os.WriteFile(src, []byte("# A Source\n\nBody text.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := pc.lw("ingest", "--vault", pc.root, src); code != 0 {
		t.Fatalf("ingest: exit %d stderr %q", code, stderr)
	}
}

// TestOpenChangeSurvivesAPull is the scenario A-042-7 exists for: PC-A has a
// changeset open and uncommitted; PC-B commits and pushes; A runs another
// writing verb. A's start-of-verb pull fast-forwards past B's commit with no
// divergence warning, A's changeset is still open and reviewable, and A's
// commit is then pushed — the remote's journal holds B's lines, then A's.
func TestOpenChangeSurvivesAPull(t *testing.T) {
	a, b, remote := journalPair(t)

	stageIngestOnA(t, a, "raw/articles/from-a.md")
	status, _, _ := a.lw("status", "--vault", a.root)
	var csID string
	for _, f := range strings.Fields(status) {
		if strings.HasPrefix(f, "cs-") {
			csID = strings.TrimSuffix(f, ":")
		}
	}
	if csID == "" {
		t.Fatalf("A has no open changeset:\n%s", status)
	}
	if got := a.git("status", "--porcelain"); !strings.Contains(got, ".llmwiki/journal.ndjson") {
		t.Fatalf("setup: A's journal is not dirty:\n%s", got)
	}
	aHead := a.git("rev-parse", "HEAD")

	// B commits and pushes while A's changeset is open.
	openCreatePageChangeset(t, b.root, "wiki/concepts/b-page.md", "B Page")
	if stdout, stderr, code := b.lw("commit", "--vault", b.root, "-m", "b page"); code != 0 || stdout != "committed 000002\n" {
		t.Fatalf("B commit: exit %d stdout %q stderr %q", code, stdout, stderr)
	}

	// A's next writing verb takes B's commit: no divergence, nothing committed.
	// (revert of a commit that does not exist: the verb fails after its start.)
	stdout, stderr, code := a.lw("revert", "--vault", a.root, "000099")
	if want := "sync: pulled 1 commit(s) from " + remote + "\n"; code != 1 || stdout != "" || !strings.HasPrefix(stderr, want) {
		t.Fatalf("A revert: exit %d stdout %q stderr %q; want the pull line first, then the verb's own error", code, stdout, stderr)
	}
	if strings.Contains(stderr, "diverged") {
		t.Errorf("a divergence was reported: %q", stderr)
	}
	if a.read("wiki/concepts/b-page.md") == "" {
		t.Error("A did not receive B's page")
	}
	if a.git("rev-parse", "HEAD") == aHead {
		// moved by the fast-forward: that is the point
		t.Error("A's HEAD did not move to B's commit")
	}
	if a.git("rev-parse", "HEAD") != b.git("rev-parse", "HEAD") {
		t.Error("A is not at B's commit")
	}

	// A's changeset is still open and still reviewable.
	status, _, _ = a.lw("status", "--vault", a.root)
	if !strings.Contains(status, "open changeset "+csID) {
		t.Fatalf("A's changeset is gone after the pull:\n%s", status)
	}
	if out, errs, code := a.lw("diff", "--vault", a.root, "--stat"); code != 0 || !strings.Contains(out, "raw/articles/from-a.md") {
		t.Fatalf("A diff: exit %d stdout %q stderr %q", code, out, errs)
	}

	// A commits: pushed, and no divergence — B's commit is already in A's history.
	// (--force: a raw source nothing cites yet is a lint regression, which is
	// not what this test is about.)
	stdout, stderr, code = a.lw("commit", "--vault", a.root, "-m", "a raw", "--force")
	if code != 0 || stdout != "committed 000003\n" || !strings.HasSuffix(stderr, "sync: pushed 1 commit(s) to "+remote+"\n") {
		t.Fatalf("A commit: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if subjects := remoteSubjects(t, remote); subjects[0] != "lw 000003: a raw" {
		t.Errorf("remote tip = %q, want A's commit", subjects[0])
	}

	// The remote's journal: B's lines, then A's — every line whole.
	journal := remoteFile(t, remote, ".llmwiki/journal.ndjson")
	for i, line := range strings.Split(journal, "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("journal line %d is not JSON (%v): %q", i+1, err, line)
		}
	}
	bAt, aAt := strings.Index(journal, `"message":"b page"`), strings.Index(journal, csID)
	if bAt < 0 || aAt < 0 || bAt > aAt {
		t.Errorf("the journal does not hold B's lines before A's (b at %d, A's changeset at %d)", bAt, aAt)
	}
	if !strings.HasPrefix(journal, a.git("show", "HEAD~2:.llmwiki/journal.ndjson")) {
		t.Error("the journal is not an extension of the seed commit's")
	}
}

// TestStartOfVerbOnlyPulls: a verb's start never commits the work tree. A tree
// with an edit git has not seen is left exactly so — silent when the remote has
// nothing, one named warning when it has something the edit stops A taking.
func TestStartOfVerbOnlyPulls(t *testing.T) {
	a, b, remote := syncPair(t)
	a.appendTo(kvPage, "\nan edit in Obsidian\n")
	head := a.git("rev-parse", "HEAD")

	stdout, stderr, code := a.lw("lint", "--fix", "--vault", a.root)
	if code != 0 || stdout != "clean\n" || stderr != "" {
		t.Fatalf("remote has nothing: exit %d stdout %q stderr %q; want silence", code, stdout, stderr)
	}
	if a.git("rev-parse", "HEAD") != head {
		t.Fatal("a verb's start committed the work tree")
	}

	b.write("notes/20261009-130000-b.md", "from b\n")
	b.appendTo(kvPage, "\nB's edit of the same page\n") // so that the divergence below conflicts (A-042-8)
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B could not push")
	}
	stdout, stderr, code = a.lw("lint", "--fix", "--vault", a.root)
	want := "sync: the vault has uncommitted changes — run lw sync; working on the local vault\n"
	if code != 0 || stdout != "clean\n" || stderr != want {
		t.Fatalf("remote has news: exit %d stdout %q stderr %q; want %q", code, stdout, stderr, want)
	}
	if a.git("rev-parse", "HEAD") != head || !strings.Contains(a.read(kvPage), "an edit in Obsidian") {
		t.Error("the refused pull changed A")
	}

	// lw sync is the way through: commit, pull (diverged here — both committed).
	_, stderr, code = a.lw("sync")
	if code != 1 || !strings.Contains(stderr, "diverged from "+remote) {
		t.Fatalf("lw sync: exit %d stderr %q; want the divergence named", code, stderr)
	}
}

// TestExplicitSyncCarriesAndPushes: lw sync pulls first; a PC whose only
// uncommitted change is journal lines takes the remote's commit, then commits
// and pushes its own lines — pulled and pushed in one run.
func TestExplicitSyncCarriesAndPushes(t *testing.T) {
	a, b, remote := journalPair(t)
	stageIngestOnA(t, a, "raw/articles/from-a.md")
	b.write("notes/20261009-130000-b.md", "from b\n")
	if _, _, code := b.lw("sync"); code != 0 {
		t.Fatal("B could not push")
	}
	before := remoteCount(t, remote)

	stdout, stderr, code := a.lw("sync")
	want := "pulled 1 commit(s) from " + remote + "\npushed 1 commit(s) to " + remote + "\n"
	if code != 0 || stdout != want {
		t.Fatalf("A sync: exit %d\nstdout %q\nwant   %q\nstderr %q", code, stdout, want, stderr)
	}
	if remoteCount(t, remote) != before+1 {
		t.Errorf("the remote gained %d commit(s), want 1", remoteCount(t, remote)-before)
	}
	if subjects := remoteSubjects(t, remote); subjects[0] != "lw sync" {
		t.Errorf("tip = %q", subjects[0])
	}
	if got := a.git("status", "--porcelain"); got != "" {
		t.Errorf("A is not clean after the sync:\n%s", got)
	}
	if stdout, _, code := a.lw("sync"); code != 0 || stdout != "up to date with "+remote+"\n" {
		t.Errorf("second sync: exit %d stdout %q", code, stdout)
	}
}

// TestOrderedRemotes: the remote that answered last goes first when it is still
// configured; the rest keep their configured order.
func TestOrderedRemotes(t *testing.T) {
	root := t.TempDir()
	set := func(remote string) {
		putFile(t, root, ".llmwiki/sync.json", `{"last_ok":"2026-10-09T12:00:00Z","remote":"`+remote+`","last_error":""}`+"\n")
	}
	conf := []string{"home:v", "home-remote:v", "third:v"}
	cases := []struct {
		name, last string
		want       []string
	}{
		{"the last one moves to the front", "home-remote:v", []string{"home-remote:v", "home:v", "third:v"}},
		{"the last of three keeps the others in order", "third:v", []string{"third:v", "home:v", "home-remote:v"}},
		{"already first", "home:v", []string{"home:v", "home-remote:v", "third:v"}},
		{"no longer configured", "gone:v", conf},
	}
	for _, tc := range cases {
		set(tc.last)
		got := orderedRemotes(root, conf)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	os.Remove(filepath.Join(root, ".llmwiki", "sync.json"))
	if got := orderedRemotes(root, conf); strings.Join(got, ",") != strings.Join(conf, ",") {
		t.Errorf("never synced: got %v, want the configured order", got)
	}
	// The configured slice is not edited.
	set("third:v")
	_ = orderedRemotes(root, conf)
	if conf[0] != "home:v" {
		t.Errorf("orderedRemotes edited its argument: %v", conf)
	}
}

// TestStickyRemoteReachesVaultsync: every sync step hands vaultsync the remotes
// with the last answering one first — the explicit verb, the pull at a verb's
// start and the push after a change — so a dead first remote is paid for once,
// not on every step.
func TestStickyRemoteReachesVaultsync(t *testing.T) {
	a, _, remote := syncPair(t)
	dead := filepath.Join(syncTemp(t), "gone")
	a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + dead + "\", \"" + remote + "\"]\n")
	// syncPair's init stamped sync.json with the remote that answered.
	if got := a.read(".llmwiki/sync.json"); !strings.Contains(got, `"remote": "`+remote+`"`) {
		t.Fatalf("setup: sync.json = %q", got)
	}

	var pulls, pushes [][]string
	origPull, origPush := syncPull, syncPush
	t.Cleanup(func() { syncPull, syncPush = origPull, origPush })
	syncPull = func(ctx context.Context, o vaultsync.Options, maxFormat int) (vaultsync.State, error) {
		pulls = append(pulls, append([]string(nil), o.Remotes...))
		return origPull(ctx, o, maxFormat)
	}
	syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
		pushes = append(pushes, append([]string(nil), o.Remotes...))
		return origPush(ctx, o)
	}
	wantFirst := []string{remote, dead}

	if _, _, code := a.lw("lint", "--fix", "--vault", a.root); code != 0 {
		t.Fatal("lint --fix failed")
	}
	a.write("notes/20261009-130000-x.md", "x\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("sync failed")
	}
	noteAt(t, 0)
	if _, _, code := a.lw("note", "-m", "y", "--vault", a.root); code != 0 {
		t.Fatal("note failed")
	}
	if len(pulls) < 3 || len(pushes) < 2 {
		t.Fatalf("pulls %v pushes %v: the steps did not all reach vaultsync", pulls, pushes)
	}
	for _, got := range append(append([][]string{}, pulls...), pushes...) {
		if strings.Join(got, ",") != strings.Join(wantFirst, ",") {
			t.Errorf("vaultsync got remotes %v, want %v", got, wantFirst)
		}
	}

	// Never synced on this PC: the configured order, dead remote first.
	os.Remove(filepath.Join(a.root, ".llmwiki", "sync.json"))
	pulls = nil
	if _, _, code := a.lw("lint", "--fix", "--vault", a.root); code != 0 {
		t.Fatal("lint --fix failed")
	}
	if len(pulls) != 1 || strings.Join(pulls[0], ",") != dead+","+remote {
		t.Errorf("without sync.json vaultsync got %v, want the configured order", pulls)
	}
}

// TestStickyRemoteEndToEnd: with a dead remote configured first, the step after
// the first success does not try it first — the dead remote's error never
// shows, where it would have otherwise been the first thing tried.
func TestStickyRemoteEndToEnd(t *testing.T) {
	a, _, remote := syncPair(t)
	dead := filepath.Join(syncTemp(t), "gone")
	a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + dead + "\", \"" + remote + "\"]\n")
	if stdout, _, code := a.lw("sync"); code != 0 || stdout != "up to date with "+remote+"\n" {
		t.Fatalf("exit %d stdout %q", code, stdout)
	}
}

// blockedPush stubs syncPush to announce itself and wait to be released.
func blockedPush(t *testing.T) (started <-chan struct{}, release func()) {
	t.Helper()
	st := make(chan struct{})
	rel := make(chan struct{})
	var once atomic.Bool
	orig := syncPush
	syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
		if once.CompareAndSwap(false, true) {
			close(st)
		}
		<-rel
		return vaultsync.State{Remote: o.Remotes[0], Pushed: 1}, nil
	}
	var released atomic.Bool
	release = func() {
		if released.CompareAndSwap(false, true) {
			close(rel)
		}
	}
	t.Cleanup(func() { release(); syncPush = orig })
	return st, release
}

// foregroundCommit commits a new changeset through a fresh engine — what the
// TUI's review pane does while a background push is in flight. It fails the
// test if the vault lock is held.
func foregroundCommit(t *testing.T, root string) {
	t.Helper()
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	if _, err := e.OpenChangeset("foreground", stage.Author{Kind: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Append(stage.Op{
		Kind: stage.OpCreatePage, Path: "wiki/concepts/foreground.md",
		Content:   []byte("---\ntitle: Foreground\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\ntags: [inference]\nconfidence: medium\n---\n\n# Foreground\n\nSee [[kv-cache]] and [[flash-attention]].\n"),
		Rationale: "test", Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit("foreground commit"); err != nil {
		t.Fatalf("a foreground commit failed while a push was in flight: %v", err)
	}
}

// TestPushDoesNotHoldTheVaultLock (A-042-7 d): the lock covers local work only
// — each commit to git, and the pull's mutation inside its quiesce (A-042-10) —
// and is gone before the network is touched, so a foreground commit succeeds
// while a push is blocked on the wire. (The fetch before the pull is covered by
// TestSlowFetchDoesNotHoldTheVaultLock.)
func TestPushDoesNotHoldTheVaultLock(t *testing.T) {
	t.Run("a background push", func(t *testing.T) {
		a, _, _ := syncPair(t)
		a.act()
		started, release := blockedPush(t)
		auto := loadAutoSync(a.root)
		a.write("notes/20261009-130000-x.md", "x\n")
		done := make(chan syncPushResult, 1)
		go func() { done <- auto.pushOnce(t.Context()) }()
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("the push never started")
		}
		foregroundCommit(t, a.root)
		if rel, err := stage.AcquireLock(filepath.Join(a.root, ".llmwiki")); err != nil {
			t.Errorf("the vault lock is held during a push: %v", err)
		} else {
			rel()
		}
		release()
		if res := <-done; res.Err != nil || res.Pushed != 1 {
			t.Errorf("push = %+v", res)
		}
	})

	t.Run("an explicit sync", func(t *testing.T) {
		a, _, _ := syncPair(t)
		started, release := blockedPush(t)
		a.write("notes/20261009-130000-x.md", "x\n")
		stdout, stderr, code := captureRun(t, func() int {
			done := make(chan int, 1)
			go func() { done <- run([]string{"sync", "--vault", a.root}) }()
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Error("the push never started")
				release()
				return <-done
			}
			foregroundCommit(t, a.root)
			release()
			return <-done
		})
		if code != 0 || !strings.HasPrefix(stdout, "pushed 1 commit(s) to ") {
			t.Errorf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
	})
}

// TestExplicitSyncPushesWhatIsWaiting: a commit made earlier and never pushed
// (the push failed, offline) is sent by the next lw sync even though this run
// commits nothing new.
func TestExplicitSyncPushesWhatIsWaiting(t *testing.T) {
	a, _, remote := syncPair(t)
	a.write("notes/20261009-130000-x.md", "x\n")
	a.git("add", "-A")
	a.git("commit", "--quiet", "-m", "lw notes")
	stdout, stderr, code := a.lw("sync")
	if want := "pushed 1 commit(s) to " + remote + "\n"; code != 0 || stdout != want {
		t.Fatalf("exit %d stdout %q stderr %q; want %q", code, stdout, stderr, want)
	}
	if remoteFile(t, remote, "notes/20261009-130000-x.md") == "" {
		t.Error("the waiting commit was not pushed")
	}
}

// TestObjectsStagedOnBothPCs: a content-addressed object both PCs staged is the
// same file on both, so the pull takes the remote's and goes on; the same path
// with other bytes is named, and the verb carries on with the local vault.
func TestObjectsStagedOnBothPCs(t *testing.T) {
	const obj = ".llmwiki/objects/cd/cd1234"
	t.Run("identical", func(t *testing.T) {
		a, b, remote := syncPair(t)
		a.write(obj, "object bytes\n")
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("A could not push")
		}
		b.write(obj, "object bytes\n")
		stdout, stderr, code := b.lw("lint", "--fix", "--vault", b.root)
		if want := "sync: pulled 1 commit(s) from " + remote + "\n"; code != 0 || stdout != "clean\n" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q; want a clean pull", code, stdout, stderr)
		}
		if b.read(obj) != "object bytes\n" {
			t.Errorf("the object = %q", b.read(obj))
		}
	})
	t.Run("different", func(t *testing.T) {
		a, b, remote := syncPair(t)
		a.write(obj, "a's bytes\n")
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("A could not push")
		}
		b.write(obj, "b's bytes\n")
		head := b.git("rev-parse", "HEAD")
		stdout, stderr, code := b.lw("lint", "--fix", "--vault", b.root)
		want := "sync: pull failed: untracked files would be overwritten by the pull: " + obj +
			" — move them aside and run lw sync again; working on the local vault\n"
		if code != 0 || stdout != "clean\n" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q; want %q", code, stdout, stderr, want)
		}
		if b.read(obj) != "b's bytes\n" || b.git("rev-parse", "HEAD") != head {
			t.Error("the refused pull changed B")
		}
		_ = remote
	})
}

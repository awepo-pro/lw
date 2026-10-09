package main

// sync_s3d_test.go pins 042 S3d, the fixes the S3 review asked for: a commit
// that never reached the server is retried (H1), a vault that is not under lw
// sync says so (M1), the network is budgeted and the verb's own line comes
// first (M2), and the smaller edges of the verbs around sync (L1-L5).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// commitLocally commits the vault's work tree to git without pushing: what is
// left behind when a push fails, or the process is killed between the two.
func commitLocally(t *testing.T, pc *syncPC, rel, body string) {
	t.Helper()
	pc.write(rel, body)
	pc.git("add", "-A")
	pc.git("commit", "--quiet", "-m", "lw notes")
}

// settle runs the first engine open a fixture vault has not had and syncs what
// it creates, so a test that counts commits counts only its own.
func settle(t *testing.T, pc *syncPC) {
	t.Helper()
	if _, stderr, code := pc.lw("status", "--vault", pc.root); code != 0 {
		t.Fatalf("status: %q", stderr)
	}
	if _, stderr, code := pc.lw("sync"); code != 0 {
		t.Fatalf("settling sync: %q", stderr)
	}
}

// --- H1 ----------------------------------------------------------------------

// quitSpy stands in for the tea.Program.
type quitSpy struct{ quit chan struct{} }

func newQuitSpy() *quitSpy { return &quitSpy{quit: make(chan struct{}, 8)} }
func (q *quitSpy) Quit()   { q.quit <- struct{}{} }

// TestTUIQuitsOnHangup: a closed terminal (SIGHUP) or a termination signal
// asks the program to quit, so the deferred exit flush runs and the last
// commit is pushed — instead of the process dying with it unpushed. A signal
// that arrives before the program exists, or after it has returned, must not
// kill the process either: the flush still has to run.
func TestTUIQuitsOnHangup(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			sq := quitOnSignals()
			defer sq.stop()
			spy := newQuitSpy()
			sq.attach(spy)
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-spy.quit:
			case <-time.After(5 * time.Second):
				t.Fatalf("%v did not make the program quit", sig)
			}
		})
	}

	t.Run("before the program exists", func(t *testing.T) {
		sq := quitOnSignals()
		defer sq.stop()
		if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond) // still alive, or this test never returns
		spy := newQuitSpy()
		sq.attach(spy) // the program is built after the hangup: it quits at once
		select {
		case <-spy.quit:
		case <-time.After(5 * time.Second):
			t.Fatal("a hangup before the program existed was forgotten")
		}
	})

	t.Run("after the program has returned", func(t *testing.T) {
		sq := quitOnSignals()
		defer sq.stop()
		spy := newQuitSpy()
		sq.attach(spy)
		sq.detach()
		if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond) // alive: the flush after Run can finish
		select {
		case <-spy.quit:
			t.Error("a detached program was asked to quit")
		default:
		}
	})
}

// TestUnpushedCommitIsRetried: a commit that is in git and not on the server
// (its push failed, or the process was killed before it ran) is pushed by the
// next writing verb — even a verb that makes no commit of its own.
func TestUnpushedCommitIsRetried(t *testing.T) {
	t.Run("a verb that commits nothing", func(t *testing.T) {
		a, _, remote := syncPair(t)
		settle(t, a)
		commitLocally(t, a, "notes/20261009-130000-x.md", "x\n")
		before := remoteCount(t, remote)
		stdout, stderr, code := a.lw("lint", "--fix", "--vault", a.root)
		if want := "sync: pushed 1 commit(s) to " + remote + "\n"; code != 0 || stdout != "clean\n" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q; want the push of the waiting commit", code, stdout, stderr)
		}
		if remoteCount(t, remote) != before+1 || remoteFile(t, remote, "notes/20261009-130000-x.md") == "" {
			t.Error("the waiting commit did not reach the remote")
		}
		// Settled: the next verb has nothing to retry.
		if _, stderr, _ := a.lw("lint", "--fix", "--vault", a.root); stderr != "" {
			t.Errorf("a second verb printed %q", stderr)
		}
	})

	t.Run("beside an edit the pull could not carry", func(t *testing.T) {
		a, _, remote := syncPair(t)
		settle(t, a)
		commitLocally(t, a, "notes/20261009-130000-x.md", "x\n")
		a.appendTo(kvPage, "\nedited\n")
		stdout, stderr, code := a.lw("lint", "--fix", "--vault", a.root)
		if want := "sync: pushed 2 commit(s) to " + remote + "\n"; code != 0 || stdout != "clean\n" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q; want both commits pushed", code, stdout, stderr)
		}
	})

	t.Run("the TUI triggers its pusher", func(t *testing.T) {
		a, _, remote := syncPair(t)
		settle(t, a)
		a.act()
		orig := syncPushDebounce
		syncPushDebounce = 10 * time.Millisecond
		t.Cleanup(func() { syncPushDebounce = orig })
		commitLocally(t, a, "notes/20261009-130000-x.md", "x\n")
		as := newTUIAutoSync(a.root, mustLoadConfig(t))
		captureRun(t, func() int { as.pull(); return 0 })
		// The pull alone sets the pusher going — the exit flush is not what
		// sends it: wait for the remote to have the commit before the session
		// ends.
		deadline := time.Now().Add(10 * time.Second)
		for remoteFile(t, remote, "notes/20261009-130000-x.md") == "" && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if remoteFile(t, remote, "notes/20261009-130000-x.md") == "" {
			t.Fatal("the TUI's pull did not set the pusher going")
		}
		var line strings.Builder
		as.finish(&line)
		if want := "sync: pushed 1 commit(s) to " + remote + "\n"; line.String() != want {
			t.Errorf("exit line = %q, want %q", line.String(), want)
		}
		if remoteFile(t, remote, "notes/20261009-130000-x.md") == "" {
			t.Error("the waiting commit did not reach the remote")
		}
	})
}

// TestKilledTUIIsRetriedByTheNextVerb is the kill-equivalent: the TUI commits,
// the hook triggers the pusher, and the process dies before the exit flush
// (nothing here calls finish). The next `lw note` pushes the old commit too.
func TestKilledTUIIsRetriedByTheNextVerb(t *testing.T) {
	a, _, remote := syncPair(t)
	a.act()
	orig := syncPushDebounce
	syncPushDebounce = time.Hour // the pusher never gets to run: the process "dies"
	t.Cleanup(func() { syncPushDebounce = orig })
	as := newTUIAutoSync(a.root, mustLoadConfig(t))
	e, err := openVaultEngine(a.root, as)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.OpenChangeset("tui commit", stage.Author{Kind: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Append(stage.Op{
		Kind: stage.OpCreatePage, Path: "wiki/concepts/from-tui.md",
		Content:   []byte("---\ntitle: From TUI\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\ntags: [inference]\nconfidence: medium\n---\n\n# From TUI\n\nSee [[kv-cache]] and [[flash-attention]].\n"),
		Rationale: "test", Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit("tui page"); err != nil {
		t.Fatal(err)
	}
	e.Close()
	if remoteFile(t, remote, "wiki/concepts/from-tui.md") != "" {
		t.Fatal("setup: the commit was pushed")
	}

	noteAt(t, 0)
	stdout, stderr, code := a.lw("note", "-m", "after the crash", "--vault", a.root)
	if code != 0 || !strings.HasPrefix(stdout, "noted ") || !strings.Contains(stderr, "sync: pushed ") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if remoteFile(t, remote, "wiki/concepts/from-tui.md") == "" || remoteFile(t, remote, "notes/20261009-120000-after-the-crash.md") == "" {
		t.Error("the next verb did not push the old commit with its own note")
	}
}

// TestSyncJSONKeepsTheErrorWhileCommitsWait: a pull that succeeds while HEAD is
// still ahead does not clear the last push failure — the commit is not there
// yet — and records how many commits wait; the push that sends them clears
// both. lw sync status shows the count.
func TestSyncJSONKeepsTheErrorWhileCommitsWait(t *testing.T) {
	pinSyncClock(t, time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	a, _, remote := syncPair(t)
	a.act()
	commitLocally(t, a, "notes/20261009-130000-x.md", "x\n")
	putFile(t, a.root, ".llmwiki/sync.json", `{"last_ok":"2026-10-09T11:00:00Z","remote":"`+remote+`","last_error":"push failed: boom"}`+"\n")

	auto := loadAutoSync(a.root)
	captureRun(t, func() int { auto.pull(); return 0 })
	discardVerbEnd() // the pull asked for a push at the end of the verb; there is no verb here
	st := readSyncState(a.root)
	if st.LastError != "push failed: boom" || st.Unpushed != 1 {
		t.Fatalf("after a pull with a commit waiting: %+v; want the error kept and unpushed 1", st)
	}
	if got := a.read(".llmwiki/sync.json"); !strings.Contains(got, `"unpushed": 1`) {
		t.Errorf("sync.json = %q, want unpushed recorded", got)
	}

	stdout, _, code := a.lw("sync", "status")
	if code != 0 || !strings.Contains(stdout, "\nlast     2026-10-09T12:00:00Z\nunpushed 1\n") {
		t.Errorf("sync status: exit %d\n%s\nwant an `unpushed 1` line after `last`", code, stdout)
	}

	if res := auto.pushOnce(t.Context()); res.Err != nil || res.Pushed != 1 {
		t.Fatalf("push = %+v", res)
	}
	st = readSyncState(a.root)
	if st.LastError != "" || st.Unpushed != 0 {
		t.Errorf("after the push: %+v; want both cleared", st)
	}
	if got := a.read(".llmwiki/sync.json"); strings.Contains(got, "unpushed") {
		t.Errorf("sync.json still names unpushed: %q", got)
	}
	if stdout, _, _ := a.lw("sync", "status"); strings.Contains(stdout, "unpushed") {
		t.Errorf("sync status still shows unpushed:\n%s", stdout)
	}
}

// --- M1 ----------------------------------------------------------------------

const notSyncing = "sync: this vault is not under lw sync — run lw sync init or lw sync clone; not syncing\n"

// TestWritingVerbSaysItIsNotSyncing: remotes are configured but this vault is
// not under lw sync — or git cannot say — so auto-sync is off; a writing verb
// says so, once, and logs why. Read-only verbs stay silent.
func TestWritingVerbSaysItIsNotSyncing(t *testing.T) {
	setup := func(t *testing.T) *syncPC {
		syncHermetic(t)
		pc := newSyncPC(t, "c")
		pc.useFixture()
		pc.setConfig("[sync]\nremotes = [\"" + deadRemote(t) + "\"]\n")
		return pc
	}

	t.Run("a vault nobody put under lw sync", func(t *testing.T) {
		pc := setup(t)
		for _, args := range [][]string{
			{"note", "-m", "scratch", "--vault", pc.root},
			{"lint", "--fix", "--vault", pc.root},
		} {
			noteAt(t, 0)
			_, stderr, code := pc.lw(args...)
			if code != 0 || stderr != notSyncing {
				t.Errorf("%v: exit %d stderr %q; want exactly %q", args, code, stderr, notSyncing)
			}
		}
	})

	t.Run("read-only verbs are silent", func(t *testing.T) {
		pc := setup(t)
		for _, args := range [][]string{{"status"}, {"log"}, {"lint"}, {"note", "list"}, {"session", "list"}, {"diff"}} {
			_, stderr, _ := pc.lw(append(append([]string{}, args...), "--vault", pc.root)...)
			if strings.Contains(stderr, "sync:") {
				t.Errorf("lw %v said %q", args, stderr)
			}
		}
	})

	t.Run("the user's own repository", func(t *testing.T) {
		pc := setup(t)
		pc.git("init", "--quiet", "-b", "main")
		pc.git("add", "-A")
		pc.git("commit", "--quiet", "-m", "my own history")
		noteAt(t, 0)
		_, stderr, code := pc.lw("note", "-m", "scratch", "--vault", pc.root)
		if code != 0 || stderr != notSyncing {
			t.Errorf("exit %d stderr %q; want %q", code, stderr, notSyncing)
		}
	})

	t.Run("git cannot say", func(t *testing.T) {
		pc := setup(t)
		// A .git that is not a repository: git rev-parse fails with something
		// other than "no such ref".
		if err := os.MkdirAll(filepath.Join(pc.root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(pc.root, ".llmwiki", "logs"), 0o755); err != nil {
			t.Fatal(err)
		}
		putFile(t, pc.root, ".llmwiki/logs/lw.log", "")
		noteAt(t, 0)
		_, stderr, code := pc.lw("note", "-m", "scratch", "--vault", pc.root)
		if code != 0 || stderr != notSyncing {
			t.Errorf("exit %d stderr %q; want %q", code, stderr, notSyncing)
		}
		log := pc.read(".llmwiki/logs/lw.log")
		if !strings.Contains(log, "WARN") || !strings.Contains(log, "not under lw sync") || !strings.Contains(log, "git rev-parse") {
			t.Errorf("lw.log does not carry the warning and git's error:\n%s", log)
		}
	})

	t.Run("auto = false is a choice, not a problem", func(t *testing.T) {
		pc := setup(t)
		pc.setConfig("[sync]\nremotes = [\"x:y\"]\nauto = false\n")
		noteAt(t, 0)
		if _, stderr, _ := pc.lw("note", "-m", "scratch", "--vault", pc.root); stderr != "" {
			t.Errorf("stderr = %q, want silence", stderr)
		}
	})

	t.Run("without remotes nothing is said", func(t *testing.T) {
		syncHermetic(t)
		pc := newSyncPC(t, "c")
		pc.useFixture()
		noteAt(t, 0)
		if _, stderr, _ := pc.lw("note", "-m", "scratch", "--vault", pc.root); stderr != "" {
			t.Errorf("stderr = %q, want silence", stderr)
		}
	})
}

// --- M2 ----------------------------------------------------------------------

// TestRemoteBudget: each remote gets a share of the step's deadline, never
// less than the floor, so a stalled first remote cannot use up the time the
// others need.
func TestRemoteBudget(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{0, 15 * time.Second},
		{1, 15 * time.Second},
		{2, 7500 * time.Millisecond},
		{3, 5 * time.Second},
		{4, 5 * time.Second}, // the floor
		{9, 5 * time.Second},
	}
	for _, tc := range cases {
		if got := remoteBudget(15*time.Second, tc.n); got != tc.want {
			t.Errorf("remoteBudget(15s, %d) = %v, want %v", tc.n, got, tc.want)
		}
	}
	a := &autoSync{root: t.TempDir(), remotes: []string{"a:b", "c:d"}}
	if got := a.options().Timeout; got != 7500*time.Millisecond {
		t.Errorf("options().Timeout with two remotes = %v, want 7.5s", got)
	}
}

// TestStalledRemoteDoesNotStarveTheNext: the first remote hangs; the second
// still gets its turn within the step's deadline.
func TestStalledRemoteDoesNotStarveTheNext(t *testing.T) {
	syncHermetic(t)
	installFakeSSH(t)
	t.Setenv("FAKE_SSH_SLEEP", "30")
	t.Setenv("FAKE_SSH_SLEEP_HOST", "hung")
	origTotal, origMin := syncAutoTimeout, syncMinRemoteBudget
	syncAutoTimeout, syncMinRemoteBudget = 1200*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { syncAutoTimeout, syncMinRemoteBudget = origTotal, origMin })

	a := newSyncPC(t, "a")
	a.useFixture()
	good := filepath.Join(syncTemp(t), "lw-vault")
	if _, stderr, code := a.lw("sync", "init", good, "--vault", a.root); code != 0 {
		t.Fatalf("init: %q", stderr)
	}
	os.Remove(filepath.Join(a.root, ".llmwiki", "sync.json")) // never synced: the configured order stands
	a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"hung:vault\", \"" + good + "\"]\n")

	start := time.Now()
	stdout, stderr, code := a.lw("lint", "--fix", "--vault", a.root)
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("the verb took %v", took)
	}
	if code != 0 || stdout != "clean\n" || stderr != "" {
		t.Fatalf("exit %d stdout %q stderr %q; want the second remote to answer and nothing said", code, stdout, stderr)
	}
	if got := readSyncState(a.root).Remote; got != good {
		t.Errorf("sync.json remote = %q, want the one that answered, %q", got, good)
	}
}

// TestVerbResultComesBeforeThePush: the line a verb prints for itself is first,
// the push line after it — the user sees the result, then waits for the
// network, not the other way round.
func TestVerbResultComesBeforeThePush(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		a, _, remote := syncPair(t)
		openCreatePageChangeset(t, a.root, "wiki/concepts/first-commit.md", "First Commit")
		a.act()
		out, code := captureCombined(t, func() int { return run([]string{"commit", "--vault", a.root, "-m", "first page"}) })
		if want := "committed 000001\nsync: pushed 1 commit(s) to " + remote + "\n"; code != 0 || out != want {
			t.Errorf("exit %d output %q, want %q", code, out, want)
		}
	})
	t.Run("note", func(t *testing.T) {
		a, _, remote := syncPair(t)
		noteAt(t, 0)
		a.act()
		out, code := captureCombined(t, func() int { return run([]string{"note", "-m", "n", "--vault", a.root}) })
		if want := "noted notes/20261009-120000-n.md\nsync: pushed 1 commit(s) to " + remote + "\n"; code != 0 || out != want {
			t.Errorf("exit %d output %q, want %q", code, out, want)
		}
	})
	t.Run("a failed ingest: its error, then the push of the rejection", func(t *testing.T) {
		a, _, remote := syncPair(t)
		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return &failingStageAgent{e: e, sessions: sessions}, nil
		})
		src := filepath.Join(t.TempDir(), "note.md")
		if err := os.WriteFile(src, []byte("# A Note\n\nSome body text.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		a.act()
		out, code := captureCombined(t, func() int { return run([]string{"ingest", "--vault", a.root, src}) })
		i, j := strings.Index(out, "lw: ingest: "), strings.Index(out, "sync: pushed 1 commit(s) to "+remote)
		if code != 1 || i < 0 || j < 0 || i > j {
			t.Errorf("exit %d output %q; want the verb's error before the push line", code, out)
		}
	})
}

// TestSlowStepsAnnounceThemselves: a pull or push still running after a second
// (here, 300 ms) prints one line saying so, terminal or not; a step that
// finishes in time prints nothing.
func TestSlowStepsAnnounceThemselves(t *testing.T) {
	slowAfter := func(d time.Duration) {
		orig := syncSlowAfter
		syncSlowAfter = d
		t.Cleanup(func() { syncSlowAfter = orig })
	}

	t.Run("a slow pull", func(t *testing.T) {
		a, _, _ := syncPair(t)
		slowAfter(300 * time.Millisecond)
		origPull := syncPull
		syncPull = func(ctx context.Context, o vaultsync.Options, maxFormat int) (vaultsync.State, error) {
			time.Sleep(900 * time.Millisecond)
			return origPull(ctx, o, maxFormat)
		}
		t.Cleanup(func() { syncPull = origPull })
		_, stderr, code := a.lw("lint", "--fix", "--vault", a.root)
		if code != 0 || stderr != "sync: pulling…\n" {
			t.Errorf("exit %d stderr %q; want one `sync: pulling…` line", code, stderr)
		}
	})

	t.Run("a slow push", func(t *testing.T) {
		a, _, remote := syncPair(t)
		slowAfter(300 * time.Millisecond)
		origPush := syncPush
		syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
			time.Sleep(900 * time.Millisecond)
			return origPush(ctx, o)
		}
		t.Cleanup(func() { syncPush = origPush })
		noteAt(t, 0)
		out, code := captureCombined(t, func() int { a.act(); return run([]string{"note", "-m", "n", "--vault", a.root}) })
		want := "noted notes/20261009-120000-n.md\nsync: pushing…\nsync: pushed 1 commit(s) to " + remote + "\n"
		if code != 0 || out != want {
			t.Errorf("exit %d output %q, want %q", code, out, want)
		}
	})

	t.Run("the TUI's exit flush", func(t *testing.T) {
		a, _, remote := syncPair(t)
		a.act()
		slowAfter(300 * time.Millisecond)
		origPush := syncPush
		syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
			time.Sleep(900 * time.Millisecond)
			return origPush(ctx, o)
		}
		t.Cleanup(func() { syncPush = origPush })
		as := newTUIAutoSync(a.root, mustLoadConfig(t))
		a.write("notes/20261009-130000-late.md", "late\n")
		var line strings.Builder
		as.finish(&line)
		if want := "sync: pushing…\nsync: pushed 1 commit(s) to " + remote + "\n"; line.String() != want {
			t.Errorf("exit output = %q, want %q", line.String(), want)
		}
	})

	t.Run("fast steps say nothing", func(t *testing.T) {
		a, _, remote := syncPair(t)
		noteAt(t, 0)
		_, stderr, _ := a.lw("note", "-m", "n", "--vault", a.root)
		if stderr != "sync: pushed 1 commit(s) to "+remote+"\n" {
			t.Errorf("stderr = %q", stderr)
		}
	})
}

// --- M4 ----------------------------------------------------------------------

// TestSyncInitRefusesAnExistingRepository: a vault that is already a git
// repository with commits and no lw ref is the user's own history; lw sync init
// will not commit onto their branch and push it as main.
func TestSyncInitRefusesAnExistingRepository(t *testing.T) {
	syncHermetic(t)
	pc := newSyncPC(t, "a")
	pc.useFixture()
	pc.git("init", "--quiet", "-b", "dev")
	pc.git("add", "-A")
	pc.git("commit", "--quiet", "-m", "my own history")
	pc.write("notes/wip.md", "work in progress\n")
	remote := filepath.Join(syncTemp(t), "lw-vault")
	head := pc.git("rev-parse", "HEAD")

	stdout, stderr, code := pc.lw("sync", "init", remote, "--vault", pc.root)
	want := "lw: sync: " + pc.root + " is already a git repository with its own history — lw sync init will not adopt it; move its .git aside or start from a copy\n"
	if code != 1 || stdout != "" || !stderrEndsWith(stderr, want) {
		t.Fatalf("exit %d stdout %q stderr %q; want %q", code, stdout, stderr, want)
	}
	if pc.git("rev-parse", "HEAD") != head || pc.git("rev-parse", "--abbrev-ref", "HEAD") != "dev" {
		t.Error("the refused init changed the user's repository")
	}
	if _, err := os.Stat(filepath.Join(pc.root, ".gitignore")); err == nil {
		t.Error("the refused init wrote a .gitignore")
	}
	if _, err := os.Stat(remote); err == nil {
		t.Error("the refused init created the remote")
	}
	if pc.config() != "" {
		t.Error("the refused init wrote config")
	}
}

// --- L1-L5 -------------------------------------------------------------------

// TestInitChecksTheFormatFirst: lw init — with --force too — into a vault
// written by a newer lw refuses before it writes a byte.
func TestInitChecksTheFormatFirst(t *testing.T) {
	syncHermetic(t)
	const text = "vault format 2 is newer than this lw supports (1): upgrade lw"
	// Both vaults are copied before the first chdir: the fixture is found from
	// the working directory.
	roots := []string{formatTooNewVault(t, "2"), formatTooNewVault(t, "2")}
	for i, args := range [][]string{{"init", "--schema", "x", "--force"}, {"init", "--schema", "x"}} {
		root := roots[i]
		before := treeDigest(t, root)
		chdir(t, root)
		stdout, stderr, code := captureRun(t, func() int { return run(args) })
		if want := "lw: init: " + text + "\n"; code != 1 || stdout != "" || stderr != want {
			t.Errorf("%v: exit %d stdout %q stderr %q; want %q", args, code, stdout, stderr, want)
		}
		if treeDigest(t, root) != before {
			t.Errorf("%v: the refusal changed files", args)
		}
	}
}

// TestQueryRejectIsPushed (L2): a query turn that opens a changeset has it
// rejected, and the rejection goes through the hook helper like any other.
func TestQueryRejectIsPushed(t *testing.T) {
	a, _, remote := syncPair(t)
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		return &fakeStagingQueryAgent{e: e, sessions: sessions}, nil
	})
	before := remoteCount(t, remote)
	_, stderr, code := a.lw("query", "--vault", a.root, "what is the kv cache?")
	if code != 1 || !strings.Contains(stderr, "sync: pushed ") {
		t.Fatalf("exit %d stderr %q; want the rejection pushed", code, stderr)
	}
	if remoteCount(t, remote) <= before || !strings.Contains(remoteFile(t, remote, ".llmwiki/journal.ndjson"), "changeset_rejected") {
		t.Error("the rejection did not reach the remote")
	}
}

// TestQueryStaysQuietAboutSync: a query is read-only; with remotes configured
// and a vault that is not under lw sync it says nothing about syncing.
func TestQueryStaysQuietAboutSync(t *testing.T) {
	syncHermetic(t)
	pc := newSyncPC(t, "c")
	pc.useFixture()
	pc.setConfig("[sync]\nremotes = [\"" + deadRemote(t) + "\"]\n")
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		return &fakeTextAgent{sessions: sessions, reply: "an answer"}, nil
	})
	if _, stderr, code := pc.lw("query", "--vault", pc.root, "what?"); code != 0 || strings.Contains(stderr, "sync:") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
}

// TestDoctorDiscardUnreadablePushes (L2): the unreadable changeset doctor moves
// aside is journalled without the engine's Reject, so the hook never fires; the
// push is asked for explicitly.
func TestDoctorDiscardUnreadablePushes(t *testing.T) {
	a, _, remote := syncPair(t)
	id := seedUnreadableChangeset(t, a.root)
	before := remoteCount(t, remote)
	stdout, stderr, _ := a.lw("doctor", "--vault", a.root, "--discard-changeset")
	if !strings.Contains(stdout, "discarded unreadable open changeset "+id) {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "sync: pushed ") || remoteCount(t, remote) <= before {
		t.Errorf("stderr %q; want the discard pushed", stderr)
	}
	if !strings.Contains(remoteFile(t, remote, ".llmwiki/journal.ndjson"), id) {
		t.Error("the remote's journal does not record the discard")
	}
}

// TestRelativeVaultPathIsRefused (L3): a relative [vault] path would resolve
// against whatever directory lw runs in.
func TestRelativeVaultPathIsRefused(t *testing.T) {
	syncHermetic(t)
	pc := newSyncPC(t, "a")
	pc.setConfig("[vault]\npath = \"some/vault\"\n")
	chdir(t, noVaultDir(t))
	stdout, stderr, code := pc.lw("status")
	if want := "lw: status: [vault] path some/vault: must be absolute or start with ~/\n"; code != 1 || stdout != "" || stderr != want {
		t.Errorf("exit %d stdout %q stderr %q; want %q", code, stdout, stderr, want)
	}
	pc.setConfig("[vault]\npath = \"~/no-such-vault\"\n") // ~/ is fine: it then fails on the missing SCHEMA.md
	if _, stderr, _ := pc.lw("status"); strings.Contains(stderr, "must be absolute") {
		t.Errorf("a ~/ path was called relative: %q", stderr)
	}
}

// TestDoctorSurvivesAMalformedConfig (L4): a config.toml that does not parse is
// a failed config check, not a nil dereference in the provider probe.
func TestDoctorSurvivesAMalformedConfig(t *testing.T) {
	syncHermetic(t)
	pc := newSyncPC(t, "a")
	pc.setConfig("not = [valid toml")
	pc.act()
	root := testutil.CopyFixture(t, "minimal")
	var probed atomic.Bool
	orig := probeProvider
	probeProvider = func(ctx context.Context, cfg *config.Config) llm.ProbeResult {
		probed.Store(true)
		return llm.ProbeResult{}
	}
	t.Cleanup(func() { probeProvider = orig })

	rep := runDoctor(t.Context(), root, doctorOptions{probe: true})
	var cfgCheck, provider *doctorCheck
	for i := range rep.Checks {
		switch rep.Checks[i].Name {
		case "config":
			cfgCheck = &rep.Checks[i]
		case "provider":
			provider = &rep.Checks[i]
		}
	}
	if cfgCheck == nil || cfgCheck.OK || !strings.Contains(cfgCheck.Detail, "parse") {
		t.Fatalf("config check = %+v; want a failure naming the parse error", cfgCheck)
	}
	if provider == nil || !provider.Skipped || probed.Load() {
		t.Errorf("provider check = %+v, probed %v; want it skipped without a probe", provider, probed.Load())
	}
	stdout, _, code := pc.lw("doctor", "--vault", root)
	if code != 1 || !strings.Contains(stdout, "config") {
		t.Errorf("lw doctor: exit %d\n%s", code, stdout)
	}
}

// TestConfigShowsVaultAndSync (L5): lw config lists [vault] path and [sync]
// remotes/auto when they are set, and only then.
func TestConfigShowsVaultAndSync(t *testing.T) {
	syncHermetic(t)
	pc := newSyncPC(t, "a")
	out, _, _ := pc.lw("config")
	for _, k := range []string{"vault.path", "sync.remotes", "sync.auto"} {
		if strings.Contains(out, k) {
			t.Errorf("lw config lists %s although it is not set:\n%s", k, out)
		}
	}
	pc.setConfig("[vault]\npath = \"/srv/ml-notes\"\n[sync]\nremotes = [\"home:lw-vault\", \"home-remote:lw-vault\"]\nauto = false\n")
	out, _, code := pc.lw("config")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{
		"vault.path ",
		"/srv/ml-notes",
		"sync.remotes",
		"home:lw-vault, home-remote:lw-vault",
		"sync.auto",
		"= false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lw config lacks %q:\n%s", want, out)
		}
	}
}

// --- coverage ----------------------------------------------------------------

// TestCwdAncestorBeatsVaultPath: working inside a vault means that vault, even
// when the config names another as the default.
func TestCwdAncestorBeatsVaultPath(t *testing.T) {
	syncHermetic(t)
	pc := newSyncPC(t, "a")
	inside := discoveryVault(t, "inside")
	other := discoveryVault(t, "other")
	putFile(t, inside, "wiki/only-here.md", "---\ntitle: Only Here\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\ntags: [testing]\n---\n\n# Only Here\n\nBody.\n")
	pc.setConfig("[vault]\npath = \"" + other + "\"\n")
	chdir(t, filepath.Join(inside, "wiki"))
	stdout, _, code := pc.lw("status")
	if code != 0 || !strings.HasPrefix(stdout, "1 pages") {
		t.Errorf("exit %d stdout %q; want the vault the working directory is in (1 page), not the configured one (0)", code, stdout)
	}
}

// TestGarbageJournalLineDoesNotMaskAnInterruptedCommit: a line of the journal
// that does not parse (a torn write, an editor's stray byte) is skipped; an
// interrupted commit around it is still seen.
func TestGarbageJournalLineDoesNotMaskAnInterruptedCommit(t *testing.T) {
	root := t.TempDir()
	begin := `{"ts":"2026-10-09T12:00:00Z","kind":"commit_begin","changeset":"cs-x","commit":"000009","actor":{"kind":"human"}}`
	end := `{"ts":"2026-10-09T12:00:01Z","kind":"commit_end","changeset":"cs-x","commit":"000009","actor":{"kind":"human"}}`
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"begin, garbage":      {begin + "\n{not json\n", true},
		"garbage, begin":      {"\x00\x01garbage\n" + begin + "\n", true},
		"begin, garbage, end": {begin + "\n{torn\n" + end + "\n", false},
		"garbage only":        {"{not json\n", false},
		"nothing":             {"", false},
	} {
		putFile(t, root, ".llmwiki/journal.ndjson", tc.body)
		got, err := commitInterrupted(root)
		if err != nil || got != tc.want {
			t.Errorf("%s: commitInterrupted = %v, %v; want %v", name, got, err, tc.want)
		}
	}
}

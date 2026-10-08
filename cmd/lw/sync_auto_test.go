package main

// sync_auto_test.go pins 042's auto-sync (D5): the pull a writing verb makes
// before it opens the engine, the push after a change, what each prints, and
// the rule that makes it safe to leave on — any failure warns once and the
// command carries on with the local vault.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// noteAt pins the note clock to a minute of 2026-10-09 and returns the file
// name stem a note taken then with the given text gets.
func noteAt(t *testing.T, minute int) time.Time {
	t.Helper()
	at := time.Date(2026, 10, 9, 12, minute, 0, 0, noteZone)
	setNoteClock(t, at)
	return at
}

// deadRemote returns a local path nothing lives at: a remote that never
// answers, instantly.
func deadRemote(t *testing.T) string { return filepath.Join(syncTemp(t), "gone") }

// remoteCount is the number of commits on the remote's main.
func remoteCount(t *testing.T, remote string) int {
	t.Helper()
	return len(remoteSubjects(t, remote))
}

// TestAutoPullAtStart: with remotes configured, `lw note -m` on a PC that is
// behind prints the sync: pulled line, then the note, then the sync: pushed
// line, and the remote ends up with both notes.
func TestAutoPullAtStart(t *testing.T) {
	a, b, remote := syncPair(t)

	noteAt(t, 0)
	stdout, stderr, code := a.lw("note", "-m", "from a")
	if code != 0 || stdout != "noted notes/20261009-120000-from-a.md\n" || stderr != "sync: pushed 1 commit(s) to "+remote+"\n" {
		t.Fatalf("A: exit %d stdout %q stderr %q; want the note and one pushed line", code, stdout, stderr)
	}
	if subjects := remoteSubjects(t, remote); subjects[0] != "lw notes" {
		t.Errorf("A's push has subject %q, want %q", subjects[0], "lw notes")
	}

	noteAt(t, 1)
	b.act()
	out, code := captureCombined(t, func() int { return run([]string{"note", "-m", "from b", "--vault", b.root}) })
	want := "sync: pulled 1 commit(s) from " + remote + "\n" +
		"noted notes/20261009-120100-from-b.md\n" +
		"sync: pushed 1 commit(s) to " + remote + "\n"
	if code != 0 || out != want {
		t.Fatalf("B: exit %d output\n%q\nwant\n%q", code, out, want)
	}
	if b.read("notes/20261009-120000-from-a.md") == "" {
		t.Error("B did not pull A's note")
	}
	for _, rel := range []string{"notes/20261009-120000-from-a.md", "notes/20261009-120100-from-b.md"} {
		if remoteFile(t, remote, rel) == "" {
			t.Errorf("the remote lacks %s", rel)
		}
	}

	// A catches up on the next verb it runs — and only the writing verbs sync.
	noteAt(t, 2)
	if _, stderr, _ := a.lw("note", "-m", "again", "--vault", a.root); stderr != "sync: pulled 1 commit(s) from "+remote+"\nsync: pushed 1 commit(s) to "+remote+"\n" {
		t.Errorf("A's second note: stderr %q, want a pull then a push", stderr)
	}
}

// TestAutoPullRebuildsTheIndex: a fast-forward that changed wiki/ leaves the
// index current by the time the pull returns — before any engine opens, which
// would otherwise do it as a side effect and hide a pull that did not.
func TestAutoPullRebuildsTheIndex(t *testing.T) {
	a, b, remote := syncPair(t)
	a.appendTo(kvPage, "\nA changed this page.\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A could not push")
	}
	b.act()
	auto := loadAutoSync(b.root)
	if auto == nil {
		t.Fatal("no auto-sync for B")
	}
	_, stderr, _ := captureRun(t, func() int { auto.pull(); return 0 })
	if want := "sync: pulled 1 commit(s) from " + remote + "\n"; stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
	if !strings.Contains(b.read(kvPage), "A changed this page.") {
		t.Error("B did not pull the page")
	}
	v, err := vault.Open(b.root)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Load(filepath.Join(b.root, ".llmwiki", "index.gob"))
	if err != nil || ix.StaleAgainst(v) {
		t.Errorf("the index after a pull that changed wiki/: load err %v, stale %v", err, err == nil && ix.StaleAgainst(v))
	}
}

// TestAutoSyncStepsCarryTheDeadline: the context every auto-sync step hands
// vaultsync has a deadline syncAutoTimeout from now — vaultsync's own Timeout
// is per git call, and a verb's whole sync must be bounded as well.
func TestAutoSyncStepsCarryTheDeadline(t *testing.T) {
	a, _, _ := syncPair(t)
	var pullDL, pushDL time.Time
	origPull, origPush := syncPull, syncPush
	t.Cleanup(func() { syncPull, syncPush = origPull, origPush })
	syncPull = func(ctx context.Context, o vaultsync.Options, maxFormat int) (vaultsync.State, error) {
		pullDL, _ = ctx.Deadline()
		return origPull(ctx, o, maxFormat)
	}
	syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
		pushDL, _ = ctx.Deadline()
		return origPush(ctx, o)
	}
	noteAt(t, 0)
	start := time.Now()
	if _, stderr, code := a.lw("note", "-m", "deadline", "--vault", a.root); code != 0 {
		t.Fatalf("note: exit %d stderr %q", code, stderr)
	}
	for name, dl := range map[string]time.Time{"pull": pullDL, "push": pushDL} {
		if dl.IsZero() {
			t.Errorf("the %s context has no deadline", name)
			continue
		}
		if d := dl.Sub(start); d < 14*time.Second || d > 16*time.Second {
			t.Errorf("the %s deadline is %v from the start, want about 15s", name, d)
		}
	}
}

// TestAutoPushAfterCommit: CLI `lw commit -m` pushes before it exits, and the
// remote's commit message is `lw <id>: <msg>`.
func TestAutoPushAfterCommit(t *testing.T) {
	a, b, remote := syncPair(t)
	before := remoteCount(t, remote)

	openCreatePageChangeset(t, a.root, "wiki/concepts/first-commit.md", "First Commit")
	stdout, stderr, code := a.lw("commit", "--vault", a.root, "-m", "first page")
	if code != 0 || stdout != "committed 000001\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	m := regexp.MustCompile(`^sync: pushed (\d+) commit\(s\) to ` + regexp.QuoteMeta(remote) + `\n$`).FindStringSubmatch(stderr)
	if m == nil {
		t.Fatalf("stderr = %q, want one `sync: pushed N commit(s) to %s` line", stderr, remote)
	}
	if got := remoteCount(t, remote) - before; m[1] != strconv.Itoa(got) {
		t.Errorf("the line says %s commit(s), the remote gained %d", m[1], got)
	}
	if subjects := remoteSubjects(t, remote); subjects[0] != "lw 000001: first page" {
		t.Errorf("the remote's tip is %q, want %q", subjects[0], "lw 000001: first page")
	}
	if remoteFile(t, remote, "wiki/concepts/first-commit.md") == "" {
		t.Error("the remote lacks the committed page")
	}

	// B gets it, with its journal, on its next sync.
	if stdout, _, code := b.lw("sync"); code != 0 || stdout != "pulled "+strconv.Itoa(remoteCount(t, remote)-before)+" commit(s) from "+remote+"\nrebuilt the search index\n" {
		t.Errorf("B: exit %d stdout %q", code, stdout)
	}
	if !strings.Contains(b.read(".llmwiki/journal.ndjson"), `"commit":"000001"`) {
		t.Error("B did not receive the journal's commit_end")
	}
}

// TestAutoSyncFailureWarnsAndContinues: a dead remote costs one
// `sync: … working on the local vault` line at the start, and the command
// still succeeds. A verb that pushes afterwards says so once more, and what it
// did stays committed locally.
func TestAutoSyncFailureWarnsAndContinues(t *testing.T) {
	t.Run("a verb that only pulls", func(t *testing.T) {
		a, _, _ := syncPair(t)
		dead := deadRemote(t)
		a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + dead + "\"]\n")
		stdout, stderr, code := a.lw("lint", "--fix", "--vault", a.root)
		re := regexp.MustCompile(`^sync: pull failed: ` + regexp.QuoteMeta(dead) + `: [^\n]*; working on the local vault\n$`)
		if code != 0 || stdout != "clean\n" || !re.MatchString(stderr) {
			t.Fatalf("exit %d stdout %q stderr %q; want success and one warning line", code, stdout, stderr)
		}
	})

	t.Run("a verb that pushes too", func(t *testing.T) {
		a, _, remote := syncPair(t)
		dead := deadRemote(t)
		a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + dead + "\"]\n")
		noteAt(t, 0)
		stdout, stderr, code := a.lw("note", "-m", "offline", "--vault", a.root)
		lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
		if code != 0 || stdout != "noted notes/20261009-120000-offline.md\n" || len(lines) != 2 {
			t.Fatalf("exit %d stdout %q stderr %q; want the note and two sync lines", code, stdout, stderr)
		}
		if !regexp.MustCompile(`^sync: pull failed: ` + regexp.QuoteMeta(dead) + `: .*; working on the local vault$`).MatchString(lines[0]) {
			t.Errorf("first line = %q", lines[0])
		}
		if !regexp.MustCompile(`^sync: push failed \(` + regexp.QuoteMeta(dead) + `: .*\); the commit is safe locally — lw sync will retry$`).MatchString(lines[1]) {
			t.Errorf("second line = %q", lines[1])
		}
		if got := a.git("log", "-1", "--format=%s"); got != "lw notes" {
			t.Errorf("local tip = %q, want the note committed locally as %q", got, "lw notes")
		}
		if remoteCount(t, remote) != 1 {
			t.Error("the dead-remote run pushed somewhere")
		}
	})

	t.Run("recovering later sends it", func(t *testing.T) {
		a, _, remote := syncPair(t)
		good := a.config()
		a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + deadRemote(t) + "\"]\n")
		noteAt(t, 0)
		if _, _, code := a.lw("note", "-m", "offline", "--vault", a.root); code != 0 {
			t.Fatal("note failed")
		}
		a.setConfig(good)
		if stdout, _, code := a.lw("sync"); code != 0 || stdout != "pushed 1 commit(s) to "+remote+"\n" {
			t.Errorf("after the remote came back: exit %d stdout %q", code, stdout)
		}
	})
}

// TestAutoDivergedWarns: a diverged vault warns and the command still runs
// against the local vault.
func TestAutoDivergedWarns(t *testing.T) {
	a, b, remote := syncPair(t)
	a.write("notes/20261009-110000-a.md", "from a\n")
	if _, _, code := a.lw("sync"); code != 0 {
		t.Fatal("A could not push")
	}
	// B's earlier push failed (it was offline): its work is committed, safe
	// locally, and the remote has moved on. (A merely uncommitted change would
	// not diverge: a verb's start only pulls, A-042-7 b.)
	b.write("notes/20261009-130000-b.md", "from b, committed locally\n")
	b.git("add", "-A")
	b.git("commit", "--quiet", "-m", "lw notes")

	stdout, stderr, code := b.lw("lint", "--fix", "--vault", b.root)
	if want := "sync: diverged from " + remote + " — run lw sync; working on the local vault\n"; code != 0 || stdout != "clean\n" || stderr != want {
		t.Fatalf("lint --fix: exit %d stdout %q stderr %q; want %q", code, stdout, stderr, want)
	}
	// The warning did not cost B its work, and nothing was merged.
	if b.read("notes/20261009-130000-b.md") == "" || b.read("notes/20261009-110000-a.md") != "" {
		t.Error("a diverged auto-sync changed B's tree")
	}

	noteAt(t, 5)
	stdout, stderr, code = b.lw("note", "-m", "still works", "--vault", b.root)
	want := "sync: diverged from " + remote + " — run lw sync; working on the local vault\n" +
		"sync: push failed (diverged from " + remote + " — run lw sync); the commit is safe locally — lw sync will retry\n"
	if code != 0 || stdout != "noted notes/20261009-120500-still-works.md\n" || stderr != want {
		t.Fatalf("note: exit %d stdout %q stderr %q; want the note and %q", code, stdout, stderr, want)
	}
}

// TestAutoSyncStaysOutOfTheWay: auto-sync does nothing — prints nothing,
// writes nothing, starts no git — unless the vault is under lw sync, there are
// remotes, and auto is not false.
func TestAutoSyncStaysOutOfTheWay(t *testing.T) {
	t.Run("auto = false", func(t *testing.T) {
		a, _, remote := syncPair(t)
		a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + remote + "\"]\nauto = false\n")
		noteAt(t, 0)
		stdout, stderr, code := a.lw("note", "-m", "quiet", "--vault", a.root)
		if code != 0 || stdout != "noted notes/20261009-120000-quiet.md\n" || stderr != "" {
			t.Fatalf("exit %d stdout %q stderr %q; want only the note", code, stdout, stderr)
		}
		if remoteCount(t, remote) != 1 || a.git("status", "--porcelain") == "" {
			t.Error("auto = false still committed or pushed")
		}
	})

	t.Run("remotes configured, vault never put under lw sync", func(t *testing.T) {
		syncHermetic(t)
		pc := newSyncPC(t, "c")
		pc.useFixture()
		pc.setConfig("[sync]\nremotes = [\"" + deadRemote(t) + "\"]\n")
		noteAt(t, 0)
		stdout, stderr, code := pc.lw("note", "-m", "scratch", "--vault", pc.root)
		if code != 0 || stdout != "noted notes/20261009-120000-scratch.md\n" || stderr != "" {
			t.Fatalf("exit %d stdout %q stderr %q; want only the note", code, stdout, stderr)
		}
		if _, err := os.Stat(filepath.Join(pc.root, ".git")); err == nil {
			t.Error("auto-sync made a repository out of a vault nobody put under lw sync")
		}
	})

	t.Run("the user's own git repository is not ours to commit", func(t *testing.T) {
		syncHermetic(t)
		pc := newSyncPC(t, "c")
		pc.useFixture()
		pc.git("init", "--quiet", "-b", "main")
		pc.git("add", "-A")
		pc.git("commit", "--quiet", "-m", "my own history")
		pc.setConfig("[sync]\nremotes = [\"" + deadRemote(t) + "\"]\n")
		noteAt(t, 0)
		stdout, stderr, code := pc.lw("note", "-m", "scratch", "--vault", pc.root)
		if code != 0 || stdout != "noted notes/20261009-120000-scratch.md\n" || stderr != "" {
			t.Fatalf("exit %d stdout %q stderr %q; want only the note", code, stdout, stderr)
		}
		if got := pc.git("log", "--format=%s"); got != "my own history" {
			t.Errorf("history = %q, want the user's one commit untouched", got)
		}
		if _, err := os.Stat(filepath.Join(pc.root, ".gitignore")); err == nil {
			t.Error("auto-sync wrote a .gitignore into the user's own repository")
		}
	})

	t.Run("read-only verbs never touch the network", func(t *testing.T) {
		a, _, _ := syncPair(t)
		a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + deadRemote(t) + "\"]\n")
		for _, args := range [][]string{
			{"status"}, {"log"}, {"diff"}, {"lint"}, {"note", "list"}, {"session", "list"}, {"trace"}, {"doctor"},
		} {
			full := append(append([]string{}, args...), "--vault", a.root)
			_, stderr, _ := a.lw(full...)
			if strings.Contains(stderr, "sync:") {
				t.Errorf("lw %v tried to sync: %q", args, stderr)
			}
		}
	})
}

// TestAutoSyncCoversTheWritingVerbs: ingest, revert and commit pull at start
// too; ingest --dry-run does not.
func TestAutoSyncCoversTheWritingVerbs(t *testing.T) {
	newBehind := func(t *testing.T) (*syncPC, string) {
		a, b, remote := syncPair(t)
		a.write("notes/20261009-110000-a.md", "from a\n")
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("A could not push")
		}
		return b, remote
	}
	pulled := func(remote string) string { return "sync: pulled 1 commit(s) from " + remote + "\n" }

	t.Run("ingest", func(t *testing.T) {
		b, remote := newBehind(t)
		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return &fakeStageAgent{e: e, sessions: sessions, ops: []stage.Op{{
				Kind: stage.OpIngestSource, Path: "raw/articles/synced.md", Content: []byte("body\n"), Extractor: "test-fake",
			}}}, nil
		})
		src := filepath.Join(t.TempDir(), "note.md")
		if err := os.WriteFile(src, []byte("# A Note\n\nSome body text.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, stderr, code := b.lw("ingest", "--vault", b.root, src)
		if code != 0 || stderr != pulled(remote) {
			t.Fatalf("exit %d stderr %q; want the pulled line", code, stderr)
		}
		if b.read("notes/20261009-110000-a.md") == "" {
			t.Error("ingest ran on a vault that had not pulled")
		}
	})

	t.Run("ingest --dry-run reads the vault and changes nothing", func(t *testing.T) {
		b, _ := newBehind(t)
		src := filepath.Join(t.TempDir(), "note.md")
		if err := os.WriteFile(src, []byte("# A Note\n\nSome body text.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// The first verb to open the engine on a fresh clone creates its
		// journal; open it once so the dry run is measured against a settled vault.
		if _, _, code := b.lw("status", "--vault", b.root); code != 0 {
			t.Fatal("status failed")
		}
		before := treeDigest(t, b.root, ".git", ".llmwiki/logs")
		_, stderr, code := b.lw("ingest", "--dry-run", "--vault", b.root, src)
		if code != 0 || strings.Contains(stderr, "sync:") {
			t.Fatalf("exit %d stderr %q; a dry run must not sync", code, stderr)
		}
		if treeDigest(t, b.root, ".git", ".llmwiki/logs") != before {
			t.Error("a dry run changed the vault")
		}
	})

	t.Run("revert", func(t *testing.T) {
		b, remote := newBehind(t)
		_, stderr, _ := b.lw("revert", "--vault", b.root, "000099")
		if !strings.HasPrefix(stderr, pulled(remote)) {
			t.Errorf("revert did not pull first: stderr %q", stderr)
		}
	})

	t.Run("commit", func(t *testing.T) {
		// Nothing is staged, so the commit itself fails — after the pull.
		b, remote := newBehind(t)
		_, stderr, code := b.lw("commit", "--vault", b.root, "-m", "b's page")
		if code != 1 || !strings.HasPrefix(stderr, pulled(remote)) || !strings.Contains(stderr, "no open changeset") {
			t.Fatalf("exit %d stderr %q; want the pulled line, then the verb's own error", code, stderr)
		}
	})

	t.Run("a changeset open on this PC does not stop the pull", func(t *testing.T) {
		// Opening a changeset appends to the journal, which is synced. Before
		// A-042-7 the next verb committed those lines to git first and the
		// remote having moved meanwhile was a divergence; now a verb's start
		// only pulls, and the uncommitted journal lines are carried across it.
		b, remote := newBehind(t)
		openCreatePageChangeset(t, b.root, "wiki/concepts/from-b.md", "From B")
		_, stderr, code := b.lw("commit", "--vault", b.root, "-m", "b's page")
		want := pulled(remote) + "sync: pushed 1 commit(s) to " + remote + "\n"
		if code != 0 || stderr != want {
			t.Fatalf("exit %d stderr %q; want the pull, the commit and its push — no divergence", code, stderr)
		}
	})

	t.Run("a failed ingest rejects its changeset and the rejection is pushed", func(t *testing.T) {
		a, _, remote := syncPair(t)
		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return &failingStageAgent{e: e, sessions: sessions}, nil
		})
		src := filepath.Join(t.TempDir(), "note.md")
		if err := os.WriteFile(src, []byte("# A Note\n\nSome body text.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := remoteCount(t, remote)
		_, stderr, code := a.lw("ingest", "--vault", a.root, src)
		if code != 1 || !strings.Contains(stderr, "sync: pushed ") {
			t.Fatalf("exit %d stderr %q; want the failure and a pushed line (a rejection is a terminal event)", code, stderr)
		}
		if remoteCount(t, remote) <= before {
			t.Error("the rejection was not pushed")
		}
		if !strings.Contains(remoteFile(t, remote, ".llmwiki/journal.ndjson"), "changeset_rejected") {
			t.Error("the remote's journal lacks the changeset_rejected event")
		}
	})
}

// failingStageAgent opens the turn and fails it, so cmdIngest rejects the
// changeset it opened.
type failingStageAgent struct {
	e        *stage.Engine
	sessions agent.SessionStore
}

func (f *failingStageAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	defer close(out)
	err := errors.New("provider unreachable")
	select {
	case out <- agent.ErrorEv{Err: err}:
	case <-ctx.Done():
	}
	return err
}

func (f *failingStageAgent) Sessions() agent.SessionStore { return f.sessions }

// TestDoctorDiscardPushes: a changeset discarded by lw doctor is a rejection,
// and a rejection is pushed like a commit (A-042-4 M4).
func TestDoctorDiscardPushes(t *testing.T) {
	a, _, remote := syncPair(t)
	openCreatePageChangeset(t, a.root, "wiki/concepts/doomed.md", "Doomed")
	before := remoteCount(t, remote)
	// doctor's own exit code reports its checks (no provider key here); the
	// push is on stderr.
	_, stderr, _ := a.lw("doctor", "--vault", a.root, "--discard-changeset")
	if !strings.Contains(stderr, "sync: pushed ") {
		t.Fatalf("stderr %q; want a pushed line", stderr)
	}
	if remoteCount(t, remote) <= before {
		t.Error("the rejection was not pushed")
	}
}

// TestSyncAbortsOnTheFirstFailure: a failed pull ends the whole sync — the
// push is never tried — and a failed commit-work ends the push after a change.
func TestSyncAbortsOnTheFirstFailure(t *testing.T) {
	stub := func(t *testing.T) (pushes *atomic.Int32) {
		pushes = new(atomic.Int32)
		origPush, origPull := syncPush, syncPull
		syncPush = func(ctx context.Context, o vaultsync.Options) (vaultsync.State, error) {
			pushes.Add(1)
			return origPush(ctx, o)
		}
		t.Cleanup(func() { syncPush, syncPull = origPush, origPull })
		return pushes
	}

	t.Run("explicit sync", func(t *testing.T) {
		a, _, _ := syncPair(t)
		pushes := stub(t)
		syncPull = func(ctx context.Context, o vaultsync.Options, maxFormat int) (vaultsync.State, error) {
			return vaultsync.State{Remote: "r"}, errors.New("pull exploded")
		}
		a.appendTo(kvPage, "\nlocal edit\n")
		_, stderr, code := a.lw("sync")
		if code != 1 || !stderrEndsWith(stderr, "lw: sync: pull exploded\n") {
			t.Fatalf("exit %d stderr %q; want the pull error", code, stderr)
		}
		if n := pushes.Load(); n != 0 {
			t.Errorf("Push was called %d time(s) after a failed Pull", n)
		}
	})

	t.Run("a diverged pull", func(t *testing.T) {
		a, b, _ := syncPair(t)
		a.write("notes/20261009-110000-a.md", "a\n")
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("A could not push")
		}
		b.appendTo("index.md", "\nedited on B\n") // a tracked edit: only a committed one can diverge
		pushes := stub(t)
		if _, _, code := b.lw("sync"); code != 1 {
			t.Fatalf("exit %d, want 1", code)
		}
		if n := pushes.Load(); n != 0 {
			t.Errorf("Push was called %d time(s) after a diverged Pull", n)
		}
	})

	t.Run("push after a change", func(t *testing.T) {
		a, _, remote := syncPair(t)
		pushes := stub(t)
		orig := syncCommitWork
		syncCommitWork = func(o vaultsync.Options, message string) (bool, error) {
			return false, errors.New("cannot sync wiki/x.md: symlink — lw sync copies files only")
		}
		t.Cleanup(func() { syncCommitWork = orig })
		noteAt(t, 0)
		_, stderr, code := a.lw("note", "-m", "nope", "--vault", a.root)
		if code != 0 {
			t.Fatalf("exit %d, the note itself must succeed", code)
		}
		if !strings.Contains(stderr, "sync: push failed (cannot sync wiki/x.md: symlink") {
			t.Errorf("stderr %q; want the push-failed line naming the cause", stderr)
		}
		if n := pushes.Load(); n != 0 {
			t.Errorf("Push was called %d time(s) after a failed CommitWork", n)
		}
		_ = remote
	})
}

// TestSyncBookkeeping: .llmwiki/sync.json is stamped by every sync step and
// never travels.
func TestSyncBookkeeping(t *testing.T) {
	stamp := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	later := stamp.Add(time.Hour)

	t.Run("success then failure", func(t *testing.T) {
		pinSyncClock(t, stamp)
		a, _, remote := syncPair(t)
		want := "{\n  \"last_ok\": \"2026-10-09T12:00:00Z\",\n  \"remote\": \"" + remote + "\",\n  \"last_error\": \"\"\n}\n"
		if got := a.read(".llmwiki/sync.json"); got != want {
			t.Fatalf("sync.json after init =\n%q\nwant\n%q", got, want)
		}

		pinSyncClock(t, later)
		good := a.config()
		dead := deadRemote(t)
		a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + dead + "\"]\n")
		if _, _, code := a.lw("sync"); code != 1 {
			t.Fatal("a sync against a dead remote succeeded")
		}
		got := a.read(".llmwiki/sync.json")
		if !strings.Contains(got, `"last_ok": "2026-10-09T12:00:00Z"`) || !strings.Contains(got, `"remote": "`+remote+`"`) {
			t.Errorf("a failure disturbed last_ok / remote: %q", got)
		}
		if !strings.Contains(got, `"last_error": "`+dead) {
			t.Errorf("a failure was not recorded: %q", got)
		}

		a.setConfig(good)
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("recovery sync failed")
		}
		want = "{\n  \"last_ok\": \"2026-10-09T13:00:00Z\",\n  \"remote\": \"" + remote + "\",\n  \"last_error\": \"\"\n}\n"
		if got := a.read(".llmwiki/sync.json"); got != want {
			t.Errorf("sync.json after recovery =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("every kind of step stamps it", func(t *testing.T) {
		pinSyncClock(t, stamp)
		a, b, _ := syncPair(t)
		pinSyncClock(t, later)
		noteAt(t, 0)
		if _, _, code := a.lw("note", "-m", "x", "--vault", a.root); code != 0 {
			t.Fatal("note failed")
		}
		if got := a.read(".llmwiki/sync.json"); !strings.Contains(got, `"last_ok": "2026-10-09T13:00:00Z"`) {
			t.Errorf("an auto push did not stamp sync.json: %q", got)
		}
		pinSyncClock(t, later.Add(time.Hour))
		if _, _, code := b.lw("lint", "--fix", "--vault", b.root); code != 0 {
			t.Fatal("lint --fix failed")
		}
		if got := b.read(".llmwiki/sync.json"); !strings.Contains(got, `"last_ok": "2026-10-09T14:00:00Z"`) {
			t.Errorf("an auto pull did not stamp sync.json: %q", got)
		}
	})

	t.Run("it is per PC", func(t *testing.T) {
		a, _, remote := syncPair(t)
		if remoteFile(t, remote, ".llmwiki/sync.json") != "" {
			t.Error("sync.json is on the remote")
		}
		if got := a.git("ls-files", ".llmwiki/sync.json"); got != "" {
			t.Errorf("sync.json is tracked: %q", got)
		}
	})
}

// TestSyncSkipsWhileACommitIsInterrupted: open/ is per PC, so a half-applied
// commit must never be committed to git and pushed (A-042-4 M2).
func TestSyncSkipsWhileACommitIsInterrupted(t *testing.T) {
	interrupt := func(t *testing.T, root string, unmoved bool) {
		t.Helper()
		j, err := stage.OpenJournal(filepath.Join(root, ".llmwiki", "journal.ndjson"))
		if err != nil {
			t.Fatal(err)
		}
		ts := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
		if err := j.Append(stage.Event{TS: ts, Kind: stage.EvCommitBegin, Changeset: "cs-halfway", Commit: "000009", Actor: stage.Author{Kind: "human"}}); err != nil {
			t.Fatal(err)
		}
		if unmoved {
			if err := j.Append(stage.Event{TS: ts, Kind: stage.EvCommitEnd, Changeset: "cs-halfway", Commit: "000009", Actor: stage.Author{Kind: "human"}, Message: "done"}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, ".llmwiki", "changesets", "open", "cs-halfway"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	const reason = "a commit was interrupted — run lw doctor first"

	for _, unmoved := range []bool{false, true} {
		name := "begin without end"
		if unmoved {
			name = "committed but never moved out of open/"
		}
		t.Run(name, func(t *testing.T) {
			a, _, remote := syncPair(t)
			interrupt(t, a.root, unmoved)
			head := a.git("rev-parse", "HEAD")

			// Auto-sync at the start of a verb.
			stdout, stderr, code := a.lw("lint", "--fix", "--vault", a.root)
			if code != 0 || stdout != "clean\n" || stderr != "sync: "+reason+"\n" {
				t.Fatalf("lint --fix: exit %d stdout %q stderr %q; want the verb to run and one warning", code, stdout, stderr)
			}
			if a.git("rev-parse", "HEAD") != head {
				t.Error("auto-sync committed the vault while a commit was interrupted")
			}

			// Auto-sync after a change.
			noteAt(t, 0)
			_, stderr, code = a.lw("note", "-m", "x", "--vault", a.root)
			if code != 0 || stderr != "sync: "+reason+"\nsync: "+reason+"\n" {
				t.Errorf("note: exit %d stderr %q; want the warning at both ends", code, stderr)
			}
			if a.git("rev-parse", "HEAD") != head {
				t.Error("a note committed the vault while a commit was interrupted")
			}

			// Explicit sync.
			_, stderr, code = a.lw("sync")
			if code != 1 || !stderrEndsWith(stderr, "lw: sync: "+reason+"\n") {
				t.Errorf("sync: exit %d stderr %q; want the refusal", code, stderr)
			}
			if remoteCount(t, remote) != 1 || a.git("rev-parse", "HEAD") != head {
				t.Error("an explicit sync committed or pushed while a commit was interrupted")
			}
		})
	}
}

// TestSyncSkipsWhileTheVaultIsBusy: a held vault lock means a commit is in
// progress — explicit sync says so, auto-sync skips with its warning.
func TestSyncSkipsWhileTheVaultIsBusy(t *testing.T) {
	a, _, _ := syncPair(t)
	lock := filepath.Join(a.root, ".llmwiki", "lock")
	if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())+" 2026-10-09T12:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.appendTo(kvPage, "\nedit\n")
	head := a.git("rev-parse", "HEAD")

	_, stderr, code := a.lw("sync")
	if want := "lw: sync: vault is busy (a commit is in progress) — try again\n"; code != 1 || !stderrEndsWith(stderr, want) {
		t.Errorf("sync: exit %d stderr %q; want %q", code, stderr, want)
	}
	stdout, stderr, code := a.lw("lint", "--fix", "--vault", a.root)
	if want := "sync: vault is busy; working on the local vault\n"; code != 0 || stdout != "clean\n" || stderr != want {
		t.Errorf("lint --fix: exit %d stdout %q stderr %q; want %q", code, stdout, stderr, want)
	}
	if a.git("rev-parse", "HEAD") != head {
		t.Error("a busy vault was committed")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Error("sync removed a lock it did not take")
	}
}

// TestSyncReleasesTheLock: every sync step gives the vault lock back.
func TestSyncReleasesTheLock(t *testing.T) {
	a, _, _ := syncPair(t)
	lock := filepath.Join(a.root, ".llmwiki", "lock")
	noteAt(t, 0)
	for _, args := range [][]string{{"sync"}, {"sync", "status"}, {"note", "-m", "x", "--vault", a.root}, {"lint", "--fix", "--vault", a.root}} {
		if _, _, code := a.lw(args...); code != 0 {
			t.Fatalf("%v failed", args)
		}
		if _, err := os.Stat(lock); err == nil {
			t.Errorf("%v left the vault lock behind", args)
		}
	}
	// A failing step releases it too.
	dead := deadRemote(t)
	a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + dead + "\"]\n")
	a.lw("sync")
	if _, err := os.Stat(lock); err == nil {
		t.Error("a failed sync left the vault lock behind")
	}
}

// TestSyncCommitMessage: lw <id>[, <id>…]: <first message> for the lw commits
// since the last git commit; lw notes when only notes/ changed; else lw sync.
func TestSyncCommitMessage(t *testing.T) {
	commitEnd := func(t *testing.T, root, id, msg string) {
		t.Helper()
		j, err := stage.OpenJournal(filepath.Join(root, ".llmwiki", "journal.ndjson"))
		if err != nil {
			t.Fatal(err)
		}
		if err := j.Append(stage.Event{
			TS: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), Kind: stage.EvCommitEnd, Changeset: "cs-" + id, Commit: id,
			Actor: stage.Author{Kind: "human"}, Message: msg,
		}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("one lw commit", func(t *testing.T) {
		a, _, _ := syncPair(t)
		commitEnd(t, a.root, "000007", "add the kv page")
		a.appendTo(kvPage, "\nx\n")
		if got := syncCommitMessage(a.root); got != "lw 000007: add the kv page" {
			t.Errorf("message = %q", got)
		}
	})

	t.Run("several: ids listed, the first message kept", func(t *testing.T) {
		a, _, _ := syncPair(t)
		commitEnd(t, a.root, "000007", "first message")
		commitEnd(t, a.root, "000008", "second message")
		commitEnd(t, a.root, "000009", "third message")
		a.appendTo(kvPage, "\nx\n")
		if got := syncCommitMessage(a.root); got != "lw 000007, 000008, 000009: first message" {
			t.Errorf("message = %q", got)
		}
	})

	t.Run("only the first line of a long message", func(t *testing.T) {
		a, _, _ := syncPair(t)
		commitEnd(t, a.root, "000007", "headline\n\nbody paragraph\nmore")
		a.appendTo(kvPage, "\nx\n")
		if got := syncCommitMessage(a.root); got != "lw 000007: headline" {
			t.Errorf("message = %q", got)
		}
	})

	t.Run("commits already in git are not listed again", func(t *testing.T) {
		a, _, _ := syncPair(t)
		commitEnd(t, a.root, "000007", "old news")
		a.git("add", "-A")
		a.git("commit", "--quiet", "-m", "lw 000007: old news")
		commitEnd(t, a.root, "000008", "new")
		a.appendTo(kvPage, "\nx\n")
		if got := syncCommitMessage(a.root); got != "lw 000008: new" {
			t.Errorf("message = %q, want only the commit git has not seen", got)
		}
	})

	t.Run("notes only", func(t *testing.T) {
		a, _, _ := syncPair(t)
		a.write("notes/20261009-120000-idea.md", "an idea\n")
		a.write("notes/20261009-120100-other.md", "another\n")
		if got := syncCommitMessage(a.root); got != "lw notes" {
			t.Errorf("message = %q", got)
		}
	})

	t.Run("notes and something else", func(t *testing.T) {
		a, _, _ := syncPair(t)
		a.write("notes/20261009-120000-idea.md", "an idea\n")
		a.appendTo(kvPage, "\nx\n")
		if got := syncCommitMessage(a.root); got != "lw sync" {
			t.Errorf("message = %q", got)
		}
	})

	t.Run("anything else", func(t *testing.T) {
		a, _, _ := syncPair(t)
		a.appendTo(kvPage, "\nx\n")
		if got := syncCommitMessage(a.root); got != "lw sync" {
			t.Errorf("message = %q", got)
		}
		if got := syncCommitMessage(filepath.Join(t.TempDir(), "nope")); got != "lw sync" {
			t.Errorf("message for an unreadable vault = %q, want the fallback", got)
		}
	})

	t.Run("a commit id wins over notes", func(t *testing.T) {
		a, _, _ := syncPair(t)
		commitEnd(t, a.root, "000007", "m")
		a.write("notes/20261009-120000-idea.md", "an idea\n")
		if got := syncCommitMessage(a.root); got != "lw 000007: m" {
			t.Errorf("message = %q", got)
		}
	})
}

// TestAutoSyncHasADeadline: auto-sync gets a context deadline — vaultsync's
// timeout is per call, and a hung server must not hold a verb for minutes.
func TestAutoSyncHasADeadline(t *testing.T) {
	syncHermetic(t)
	installFakeSSH(t)
	t.Setenv("FAKE_SSH_SLEEP", "30")
	orig := syncAutoTimeout
	syncAutoTimeout = 300 * time.Millisecond
	t.Cleanup(func() { syncAutoTimeout = orig })

	a := newSyncPC(t, "a")
	a.useFixture()
	// Put the vault under sync with a local remote, then point auto-sync at
	// an ssh host that hangs.
	remote := filepath.Join(syncTemp(t), "lw-vault")
	if _, stderr, code := a.lw("sync", "init", remote, "--vault", a.root); code != 0 {
		t.Fatalf("init: %q", stderr)
	}
	a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"hung:vault\"]\n")

	start := time.Now()
	stdout, stderr, code := a.lw("lint", "--fix", "--vault", a.root)
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("lint --fix took %v against a hung server; the deadline did not apply", took)
	}
	if code != 0 || stdout != "clean\n" || !strings.HasPrefix(stderr, "sync: pull failed: ") || !strings.HasSuffix(stderr, "; working on the local vault\n") {
		t.Errorf("exit %d stdout %q stderr %q; want the verb to succeed with one warning", code, stdout, stderr)
	}
}

// TestSyncInteractivity: explicit sync, init and clone are interactive (ssh
// may prompt; a first push can take minutes), auto-sync is not (BatchMode, a
// bounded connect).
func TestSyncInteractivity(t *testing.T) {
	syncHermetic(t)
	logPath := installFakeSSH(t)
	// "host:path" through the fake ssh lands in the same temp area: the fake
	// runs the command in $HOME, so a path relative to HOME is the remote.
	home := os.Getenv("HOME")

	a := newSyncPC(t, "a")
	a.useFixture()
	remote := "box:remote-vault"
	if _, stderr, code := a.lw("sync", "init", remote, "--vault", a.root); code != 0 {
		t.Fatalf("init over the fake ssh: exit %d stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, "remote-vault", "HEAD")); err != nil {
		t.Fatalf("the fake ssh did not create the remote repository: %v", err)
	}
	read := func() string { b, _ := os.ReadFile(logPath); return string(b) }
	if log := read(); strings.Contains(log, "BatchMode") {
		t.Errorf("sync init was run non-interactively (BatchMode on the ssh line):\n%s", log)
	}

	os.WriteFile(logPath, nil, 0o644)
	if _, stderr, code := a.lw("sync"); code != 0 {
		t.Fatalf("sync: exit %d stderr %q", code, stderr)
	}
	if log := read(); log == "" || strings.Contains(log, "BatchMode") {
		t.Errorf("explicit sync must be interactive and must reach ssh:\n%s", log)
	}

	c := newSyncPC(t, "c")
	os.WriteFile(logPath, nil, 0o644)
	if _, stderr, code := c.lw("sync", "clone", remote, c.root); code != 0 {
		t.Fatalf("clone: exit %d stderr %q", code, stderr)
	}
	if log := read(); log == "" || strings.Contains(log, "BatchMode") {
		t.Errorf("sync clone must be interactive:\n%s", log)
	}

	os.WriteFile(logPath, nil, 0o644)
	if _, stderr, code := a.lw("lint", "--fix", "--vault", a.root); code != 0 || stderr != "" {
		t.Fatalf("auto pull: exit %d stderr %q", code, stderr)
	}
	if log := read(); !strings.Contains(log, "BatchMode=yes") || !strings.Contains(log, "ConnectTimeout=5") {
		t.Errorf("auto-sync must be non-interactive:\n%s", log)
	}
}

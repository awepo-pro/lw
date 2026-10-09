package vaultsync

// s3f_test.go pins the rest of 042 A-042-9 inside vaultsync (the concurrent
// appenders are in concurrent_test.go): the Quiesce is held across local
// mutations and never across the network, the user's git configuration cannot
// change what a rebase does, recovery never resets over a change that is not
// its own and finishes whatever became of the caller's context, the guards a
// rebase keeps, and ssh's liveness limits with the master exit.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
)

// --- (b) the quiesce: held for local mutations, never for the network ------------

// quiesceSpy counts how often the quiesce was taken and whether it is held now.
type quiesceSpy struct {
	mu    sync.Mutex
	held  bool
	takes int
	nest  bool // taken while already held
}

func (q *quiesceSpy) fn() func() (func(), error) {
	return func() (func(), error) {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.held {
			q.nest = true
		}
		q.held, q.takes = true, q.takes+1
		return func() { q.mu.Lock(); q.held = false; q.mu.Unlock() }, nil
	}
}

func (q *quiesceSpy) isHeld() bool { q.mu.Lock(); defer q.mu.Unlock(); return q.held }

// watchHeld samples the spy whenever the fake ssh has been started for a git
// transport call (it logs before it runs the command) and reports, when
// stopped, whether the quiesce was held at any of those moments.
func watchHeld(t *testing.T, q *quiesceSpy) (stop func() (sawHeldDuringNetwork bool)) {
	t.Helper()
	logPath := os.Getenv("FAKE_SSH_LOG")
	done := make(chan struct{})
	var wg sync.WaitGroup
	var bad bool
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		seen := 0
		for {
			select {
			case <-done:
				return
			default:
			}
			b, _ := os.ReadFile(logPath)
			n := strings.Count(string(b), "git-upload-pack") + strings.Count(string(b), "git-receive-pack")
			if n > seen {
				seen = n
				// The fake ssh logs before it runs the command, and the command
				// takes a moment (git upload-pack on a local repo): sample twice.
				for i := 0; i < 3; i++ {
					if q.isHeld() {
						mu.Lock()
						bad = true
						mu.Unlock()
					}
					time.Sleep(time.Millisecond)
				}
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	return func() bool {
		close(done)
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		return bad
	}
}

// TestQuiesceIsHeldForLocalMutationsOnly: Pull takes it around the carry and the
// fast-forward or rebase, once and never nested; Push, which touches no file,
// never takes it; and it is released before any network call, so a slow server
// cannot hold every journal append off.
func TestQuiesceIsHeldForLocalMutationsOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, p pair)
		takes  int
		pushed bool
	}{
		{"fast-forward", func(t *testing.T, p pair) { aPushes(t, p, "a-1\n", map[string]string{"notes/a.md": "a\n"}) }, 1, false},
		{"rebase", func(t *testing.T, p pair) {
			aCommits(t, p, map[string]string{"notes/a.md": "a\n"}, "lw notes")
			bCommitsLocally(t, p, map[string]string{"notes/b.md": "b\n"}, "lw notes")
		}, 1, false},
		{"nothing to take", func(t *testing.T, p pair) {}, 0, false},
	} {
		t.Run("Pull "+tc.name, func(t *testing.T) {
			hermetic(t)
			installFakeSSH(t)
			p := newPair(t)
			tc.setup(t, p)
			var q quiesceSpy
			o := opts(p.b, "fake:"+p.bare)
			o.Quiesce = q.fn()
			stop := watchHeld(t, &q)
			_, err := Pull(t.Context(), o, 1)
			if bad := stop(); bad {
				t.Error("the quiesce was held while the network call ran")
			}
			if err != nil {
				t.Fatalf("Pull: %v", err)
			}
			if q.takes != tc.takes || q.nest || q.isHeld() {
				t.Errorf("quiesce taken %d time(s) (want %d), nested %v, still held %v", q.takes, tc.takes, q.nest, q.isHeld())
			}
		})
	}

	t.Run("TakeRemote holds it once after the fetch", func(t *testing.T) {
		hermetic(t)
		installFakeSSH(t)
		p := newPair(t)
		aPushes(t, p, "a-1\n", map[string]string{"wiki/alpha.md": "A's version\n"})
		bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
		var q quiesceSpy
		o := opts(p.b, "fake:"+p.bare)
		o.Quiesce = q.fn()
		stop := watchHeld(t, &q)
		_, _, err := TakeRemote(t.Context(), o, 1)
		if bad := stop(); bad {
			t.Error("the quiesce was held while the network call ran")
		}
		if err != nil {
			t.Fatalf("TakeRemote: %v", err)
		}
		if q.takes != 1 || q.nest || q.isHeld() {
			t.Errorf("quiesce taken %d time(s) (want 1), nested %v, still held %v", q.takes, q.nest, q.isHeld())
		}
	})

	t.Run("Push never takes it", func(t *testing.T) {
		hermetic(t)
		installFakeSSH(t)
		p := newPair(t)
		bCommitsLocally(t, p, map[string]string{"notes/b.md": "b\n"}, "lw notes")
		var q quiesceSpy
		o := opts(p.b, "fake:"+p.bare)
		o.Quiesce = q.fn()
		if st, err := Push(t.Context(), o); err != nil || st.Pushed != 1 {
			t.Fatalf("Push = %+v, %v", st, err)
		}
		if q.takes != 0 {
			t.Errorf("Push took the quiesce %d time(s)", q.takes)
		}
	})

	t.Run("a quiesce that fails stops the Pull before it changes anything", func(t *testing.T) {
		hermetic(t)
		p := newPair(t)
		aPushes(t, p, "a-1\n", map[string]string{"notes/a.md": "a\n"})
		head := git(t, p.b, "rev-parse", "HEAD")
		o := opts(p.b, p.remote)
		boom := errors.New("the journal is busy")
		o.Quiesce = func() (func(), error) { return nil, boom }
		if _, err := Pull(t.Context(), o, 1); !errors.Is(err, boom) {
			t.Fatalf("Pull err = %v, want the quiesce's error", err)
		}
		if git(t, p.b, "rev-parse", "HEAD") != head {
			t.Error("HEAD moved although the journal could not be held")
		}
	})
}

// --- (f) the user's git configuration cannot steer a rebase -----------------------

// TestUserRebaseConfigHasNoEffect: `rebase.autoStash` would stash the journal's
// uncommitted lines around the replay and `rerere.enabled` would resolve a
// conflict from a recorded resolution; lw sets both off on its own command line
// and neither global setting changes what Pull does.
func TestUserRebaseConfigHasNoEffect(t *testing.T) {
	hermetic(t)
	writeGlobalGitConfig(t, "[rebase]\n\tautoStash = true\n\tautoSquash = true\n\tupdateRefs = true\n[rerere]\n\tenabled = true\n\tautoUpdate = true\n[pull]\n\trebase = true\n")

	t.Run("a journal tail still comes through a clean replay", func(t *testing.T) {
		p := newPair(t)
		aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
		bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
		appendTo(t, p.b, journalPath, "tail-1\ntail-2\n")
		rrBefore := rrCache(t, p.b)
		st, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1)
		if err != nil || st.Rebased != 1 {
			t.Fatalf("Pull = %+v, %v", st, err)
		}
		if got := readFile(t, filepath.Join(p.b, journalPath)); !strings.HasSuffix(got, "tail-1\ntail-2\n") || strings.Count(got, "tail-1") != 1 {
			t.Errorf("journal = %q, want the tail once at the end", got)
		}
		if out := git(t, p.b, "stash", "list"); out != "" {
			t.Errorf("an autostash was made: %q", out)
		}
		if after := rrCache(t, p.b); after != rrBefore {
			t.Errorf("rerere recorded something: %q -> %q", rrBefore, after)
		}
	})

	t.Run("a conflict is refused not resolved from a recorded resolution", func(t *testing.T) {
		p := newPair(t)
		aCommits(t, p, map[string]string{"wiki/alpha.md": "A's version\n"}, "lw sync")
		bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
		head := git(t, p.b, "rev-parse", "HEAD")
		rrBefore := rrCache(t, p.b)
		if _, err := Pull(t.Context(), opts(p.b, p.remote), 1); !errors.Is(err, ErrDiverged) {
			t.Fatalf("Pull err = %v, want ErrDiverged", err)
		}
		if git(t, p.b, "rev-parse", "HEAD") != head || rebaseInProgress(p.b) {
			t.Error("the refused rebase left the vault changed")
		}
		if after := rrCache(t, p.b); after != rrBefore {
			t.Errorf("rerere recorded the conflict: %q -> %q", rrBefore, after)
		}
	})
}

// rrCache lists what rerere has recorded in the vault ("" when nothing).
func rrCache(t *testing.T, dir string) string {
	t.Helper()
	var names []string
	root := filepath.Join(dir, ".git", "rr-cache")
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && p != root {
			rel, _ := filepath.Rel(root, p)
			names = append(names, rel)
		}
		return nil
	})
	return strings.Join(names, ",")
}

// TestRebaseRunsWithLwsOwnFlags: whatever the user's configuration says, the
// replay is run with autostash, ref updates and rerere off and the sequence
// editor out of play — the command line itself is what carries that, so it is
// read from a git that logs what it is asked.
func TestRebaseRunsWithLwsOwnFlags(t *testing.T) {
	hermetic(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	bin, logPath := t.TempDir(), filepath.Join(t.TempDir(), "git.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := newPair(t) // set up with the real git
	aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
	bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if st, err := Pull(t.Context(), opts(p.b, p.remote), 1); err != nil || st.Rebased != 1 {
		t.Fatalf("Pull = %+v, %v", st, err)
	}
	var line string
	for _, l := range strings.Split(readFile(t, logPath), "\n") {
		if strings.Contains(l, " rebase ") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no rebase in the git log:\n%s", readFile(t, logPath))
	}
	for _, want := range []string{"rebase.autoStash=false", "rebase.updateRefs=false", "rerere.enabled=false", "rebase --quiet --merge " + trackRef} {
		if !strings.Contains(line, want) {
			t.Errorf("the rebase command line lacks %q: %s", want, line)
		}
	}
}

// --- (d) recovery: nothing reset away, cleanup finishes ---------------------------

// TestRecoveryFinishesWhateverBecameOfTheContext: undoing a rebase is cleanup,
// and a caller whose deadline ran out (or who was cancelled) while it ran must
// not leave the vault half-restored. The quiesce is taken after the context
// was set free of the caller's, so cancelling inside it is the same as the
// caller giving up in the middle.
func TestRecoveryFinishesWhateverBecameOfTheContext(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	pre, tail := interruptedRebase(t, p)
	treeBefore := workTree(t, p.b)
	_ = treeBefore

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	o := appendOpts(p.b, p.remote)
	o.Quiesce = func() (func(), error) {
		cancel() // the caller gives up as the cleanup begins
		return func() {}, nil
	}
	note, err := Recover(ctx, o)
	if err != nil || note == "" {
		t.Fatalf("Recover = %q, %v; want it to finish despite the cancelled context", note, err)
	}
	if rebaseInProgress(p.b) || git(t, p.b, "rev-parse", "HEAD") != pre {
		t.Error("the vault is not back at its pre-rebase HEAD")
	}
	if got := readFile(t, filepath.Join(p.b, journalPath)); !strings.HasSuffix(got, tail) {
		t.Errorf("journal = %q, want the set-aside tail back", got)
	}
	if _, err := os.Stat(filepath.Join(p.b, ".git", "lw-rebase")); err == nil {
		t.Error("lw's record was left behind")
	}
}

// TestRefusedRebaseFinishesWhateverBecameOfTheContext: the same for the cleanup
// of a rebase that conflicted — a context cancelled after the replay began
// (here: by the seam that stands for an abort that did not finish) does not
// stop the vault from being put back.
func TestRefusedRebaseFinishesWhateverBecameOfTheContext(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"wiki/alpha.md": "A's version\n"}, "lw sync")
	bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
	head := git(t, p.b, "rev-parse", "HEAD")
	treeBefore := workTree(t, p.b)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	orig := afterRebaseAbort
	afterRebaseAbort = func(_ context.Context, r *runner) {
		cancel() // the deadline ran out in the middle of the cleanup ...
		git(t, r.o.Dir, "reset", "--hard", "--quiet", "HEAD~1")
	}
	t.Cleanup(func() { afterRebaseAbort = orig })

	if _, err := Pull(ctx, opts(p.b, p.remote), 1); !errors.Is(err, ErrDiverged) {
		t.Fatalf("Pull err = %v, want ErrDiverged with the vault put back", err)
	}
	if git(t, p.b, "rev-parse", "HEAD") != head || rebaseInProgress(p.b) {
		t.Error("the vault was left half-restored by the cancelled cleanup")
	}
	sameTree(t, workTree(t, p.b), treeBefore, "after the cleanup")
}

// TestRecoveryKeepsLinesAppendedWhileTheRebaseStood: a process that died inside
// the rebase let go of the lock, so a journal line can be appended before the
// next lw recovers; `git rebase --abort` resets the tree, and the line must
// ride out of it in the record.
func TestRecoveryKeepsLinesAppendedWhileTheRebaseStood(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	pre, tail := interruptedRebase(t, p)
	appendTo(t, p.b, journalPath, "after-the-crash-1\nafter-the-crash-2\n")

	if _, err := Recover(t.Context(), appendOpts(p.b, p.remote)); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if git(t, p.b, "rev-parse", "HEAD") != pre {
		t.Error("HEAD is not back")
	}
	got := readFile(t, filepath.Join(p.b, journalPath))
	if !strings.HasSuffix(got, tail+"after-the-crash-1\nafter-the-crash-2\n") || strings.Count(got, "after-the-crash-1") != 1 || strings.Count(got, tail) != 1 {
		t.Errorf("journal = %q, want the set-aside tail then the lines appended since, once each", got)
	}
}

// TestRecoveryRefusesAnEditItWouldResetAway: recovery resets the vault to its
// pre-rebase HEAD, and a tracked file changed since — by a verb that does not
// sync — is not lw's to discard: it is an error that names the way out, and the
// record is kept so the next run tries again.
func TestRecoveryRefusesAnEditItWouldResetAway(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	// A crash that left the rebase aborted but HEAD moved: only the reset is left.
	pre, _ := interruptedRebase(t, p)
	git(t, p.b, "rebase", "--abort")
	git(t, p.b, "reset", "--hard", "--quiet", "HEAD~1")
	put(t, p.b, "wiki/alpha.md", "written by `lw edit`, not by sync\n")

	_, err := Recover(t.Context(), appendOpts(p.b, p.remote))
	if err == nil || !strings.Contains(err.Error(), "will not reset them away") {
		t.Fatalf("Recover err = %v, want a refusal to reset the edit away", err)
	}
	if got := readFile(t, filepath.Join(p.b, "wiki/alpha.md")); got != "written by `lw edit`, not by sync\n" {
		t.Errorf("the edit was reset away: %q", got)
	}
	if _, serr := os.Stat(filepath.Join(p.b, ".git", "lw-rebase", "state.json")); serr != nil {
		t.Error("lw's record was removed although the vault is not back")
	}
	// Once the edit is dealt with, the next run finishes the job.
	git(t, p.b, "checkout", "--quiet", "--", "wiki/alpha.md")
	if _, err := Recover(t.Context(), appendOpts(p.b, p.remote)); err != nil {
		t.Fatalf("Recover after the edit was dealt with: %v", err)
	}
	if git(t, p.b, "rev-parse", "HEAD") != pre {
		t.Error("HEAD is not back after the second run")
	}
}

// --- M2: guards a rebase keeps -----------------------------------------------------

// TestRefusedRebaseKeepsLinesAppendedWhileItRan: with no Quiesce to hold other
// writers off (a library caller, an lw that predates the lock), a line appended
// while the replay stood must survive the abort's reset on the way out.
func TestRefusedRebaseKeepsLinesAppendedWhileItRan(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"wiki/alpha.md": "A's version\n"}, "lw sync")
	bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
	appendTo(t, p.b, journalPath, "before-1\n") // set aside for the replay, put back after
	head := git(t, p.b, "rev-parse", "HEAD")

	orig := afterRebase
	afterRebase = func(_ context.Context, r *runner) {
		if !rebaseInProgress(r.o.Dir) {
			t.Error("the seam ran with no rebase standing")
		}
		appendTo(t, r.o.Dir, journalPath, "during-1\nduring-2\n")
	}
	t.Cleanup(func() { afterRebase = orig })

	if _, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1); !errors.Is(err, ErrDiverged) {
		t.Fatalf("Pull err = %v, want ErrDiverged", err)
	}
	if git(t, p.b, "rev-parse", "HEAD") != head || rebaseInProgress(p.b) {
		t.Error("the vault was not put back")
	}
	got := readFile(t, filepath.Join(p.b, journalPath))
	if !strings.HasSuffix(got, "before-1\nduring-1\nduring-2\n") || strings.Count(got, "during-1") != 1 || strings.Count(got, "before-1") != 1 {
		t.Errorf("journal = %q, want the carried line then the lines appended meanwhile, once each", got)
	}
}

// TestRebaseThatLeavesHeadBehindIsUndone: a replay that does not end on top of
// the remote's tip (another process fetched a newer one into lw's tracking ref
// while this one rebased) is not a result to keep: the vault goes back, and
// the answer is a plain error, not a success.
func TestRebaseThatLeavesHeadBehindIsUndone(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
	bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
	head := git(t, p.b, "rev-parse", "HEAD")
	treeBefore := workTree(t, p.b)

	orig := afterRebase
	afterRebase = func(_ context.Context, r *runner) {
		// A second a-side commit arrives and a concurrent Status fetches it.
		aCommits(t, p, map[string]string{"notes/later-from-a.md": "later\n"}, "lw notes")
		git(t, r.o.Dir, "fetch", "--quiet", p.bare, "+refs/heads/main:"+trackRef)
	}
	t.Cleanup(func() { afterRebase = orig })

	st, err := Pull(t.Context(), opts(p.b, p.remote), 1)
	if err == nil || errors.Is(err, ErrDiverged) || !strings.Contains(err.Error(), "behind the remote") {
		t.Fatalf("Pull = %+v, %v; want an error saying the rebase left HEAD behind", st, err)
	}
	if st.Rebased != 0 {
		t.Errorf("State.Rebased = %d for a rebase that was undone", st.Rebased)
	}
	if git(t, p.b, "rev-parse", "HEAD") != head {
		t.Error("HEAD was not put back")
	}
	sameTree(t, workTree(t, p.b), treeBefore, "after the undone rebase")
	if _, err := os.Stat(filepath.Join(p.b, ".git", "lw-rebase")); err == nil {
		t.Error("lw's record was left behind after the vault was put back")
	}
}

// TestPutBackNeverOverwrites: what a rebase set aside returns only to an empty
// place — a file the replay brought is the newer truth — and a carried tail is
// appended once however many times recovery repeats the step.
func TestPutBackNeverOverwrites(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	r, err := newRunner(opts(p.b, p.remote))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "record")
	for rel, body := range map[string]string{"moved/notes/set-aside.md": "mine\n", "moved/notes/replaced.md": "mine too\n"} {
		put(t, dir, rel, body)
	}
	put(t, p.b, "notes/replaced.md", "the replay brought this\n")
	s := rebaseState{
		Moved: []string{"notes/set-aside.md", "notes/replaced.md"},
		Carry: []carryRecord{{Path: journalPath, Tail: []byte("carried\n")}},
	}
	for i := 0; i < 2; i++ { // twice: the second is recovery repeating a step
		if err := r.putBack(dir, s); err != nil {
			t.Fatalf("putBack #%d: %v", i+1, err)
		}
	}
	if got := readFile(t, filepath.Join(p.b, "notes/set-aside.md")); got != "mine\n" {
		t.Errorf("the set-aside file is %q, want it back", got)
	}
	if got := readFile(t, filepath.Join(p.b, "notes/replaced.md")); got != "the replay brought this\n" {
		t.Errorf("a file the replay brought was overwritten: %q", got)
	}
	if got := readFile(t, filepath.Join(p.b, journalPath)); strings.Count(got, "carried\n") != 1 {
		t.Errorf("journal = %q, want the carried tail once", got)
	}
}

// --- (e) ssh liveness limits and the master exit ----------------------------------

// TestMuxHasLivenessLimits: a master whose connection died is noticed in about
// 30 s, not never; an interactive call gains NO connect limit (a ProxyCommand
// that logs in first can take longer than any we would pick); and a user's own
// ControlPath leaves all of it alone.
func TestMuxHasLivenessLimits(t *testing.T) {
	t.Run("batch", func(t *testing.T) {
		logPath, _ := muxEnv(t)
		p := newPair(t)
		if _, err := Status(t.Context(), opts(p.a, "fake:"+p.bare)); err != nil {
			t.Fatal(err)
		}
		line := transportLine(t, readFile(t, logPath))
		order(t, line, "-o", "ControlPersist=600", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2")
		if !strings.Contains(line, "[ConnectTimeout=5]") || strings.Count(line, "ConnectTimeout") != 1 {
			t.Errorf("a batch call must keep its 5 s connect and no other: %s", line)
		}
	})
	t.Run("interactive", func(t *testing.T) {
		logPath, _ := muxEnv(t)
		p := newPair(t)
		o := opts(p.a, "fake:"+p.bare)
		o.Interactive = true
		if _, err := Status(t.Context(), o); err != nil {
			t.Fatal(err)
		}
		line := transportLine(t, readFile(t, logPath))
		order(t, line, "-o", "ControlPersist=600", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2")
		if strings.Contains(line, "BatchMode") || strings.Contains(line, "ConnectTimeout") {
			t.Errorf("an interactive call took connect limits: %s", line)
		}
	})
	t.Run("the user's own ControlPath is left alone", func(t *testing.T) {
		for _, interactive := range []bool{false, true} {
			logPath, _ := muxEnv(t)
			t.Setenv("FAKE_SSH_CONTROLPATH", "/home/u/.ssh/cm-%r@%h:%p")
			p := newPair(t)
			o := opts(p.a, "fake:"+p.bare)
			o.Interactive = interactive
			if _, err := Status(t.Context(), o); err != nil {
				t.Fatal(err)
			}
			line := transportLine(t, readFile(t, logPath))
			if strings.Contains(line, "ServerAlive") || (interactive && strings.Contains(line, "ConnectTimeout")) {
				t.Errorf("interactive=%v: lw added liveness options to the user's multiplexing: %s", interactive, line)
			}
		}
	})
}

// TestSlowBannerSurvivesAnInteractiveCall: an ssh whose banner arrives after
// 12 s (a ProxyCommand delivering an Access login) works interactively and is
// still given up on in batch mode, which promised a short connect.
func TestSlowBannerSurvivesAnInteractiveCall(t *testing.T) {
	t.Run("interactive", func(t *testing.T) {
		muxEnv(t)
		p := newPair(t)
		t.Setenv("FAKE_SSH_BANNER_DELAY", "12")
		o := opts(p.a, "fake:"+p.bare)
		o.Interactive = true
		if _, err := Status(t.Context(), o); err != nil {
			t.Fatalf("an interactive Status died on a slow banner: %v", err)
		}
	})
	t.Run("batch", func(t *testing.T) {
		muxEnv(t)
		p := newPair(t)
		t.Setenv("FAKE_SSH_BANNER_DELAY", "12")
		_, err := Status(t.Context(), opts(p.a, "fake:"+p.bare))
		if err == nil || !strings.Contains(err.Error(), "banner exchange") {
			t.Fatalf("batch Status err = %v, want the connect limit to give up on the banner", err)
		}
	})
}

// exitCalls returns the logged `ssh -O exit` calls.
func exitCalls(log string) []string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "[-O] [exit]") {
			out = append(out, l)
		}
	}
	return out
}

// TestTimedOutMuxedCallExitsTheMaster: a call through the lw-made control master
// that times out leaves a master that may be stuck or dead; lw asks it to exit
// so the next call starts a fresh one instead of hanging on it too. A failure
// that is not a timeout, and a connection that is the user's own, are not its
// business.
func TestTimedOutMuxedCallExitsTheMaster(t *testing.T) {
	// call runs Status against spec with every ssh command sleeping 30 s and
	// the call's limit at 700 ms, and returns the ssh log, the control socket
	// directory and the error.
	call := func(t *testing.T, spec string, setup func(t *testing.T)) (log, sockets string, err error) {
		logPath, cache := muxEnv(t)
		if setup != nil {
			setup(t)
		}
		p := newPair(t)
		t.Setenv("FAKE_SSH_SLEEP", "30")
		o := opts(p.a, strings.ReplaceAll(spec, "@BARE@", p.bare))
		o.Timeout = 700 * time.Millisecond
		start := time.Now()
		_, err = Status(t.Context(), o)
		if d := time.Since(start); d > 8*time.Second {
			t.Errorf("the timed-out call took %v", d)
		}
		return readFile(t, logPath), filepath.Join(cache, "lw", "ssh"), err
	}
	wantTimeout := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("err = %v, want a timeout", err)
		}
	}

	// (An interactive call has no limit of lw's own — a person may be typing a
	// passphrase — so it is bounded by ConnectTimeout and ServerAlive instead.)
	t.Run("the master is told to exit", func(t *testing.T) {
		log, sockets, err := call(t, "fake:@BARE@", nil)
		wantTimeout(t, err)
		calls := exitCalls(log)
		if len(calls) != 1 {
			t.Fatalf("%d ssh -O exit call(s), want 1:\n%s", len(calls), log)
		}
		order(t, calls[0], "-O", "exit", "-o", "ControlPath="+sockets+"/%C", "fake")
		if strings.Contains(calls[0], "[-p]") {
			t.Errorf("a port was passed that the destination does not have: %s", calls[0])
		}
	})

	t.Run("a destination with a port keeps it", func(t *testing.T) {
		log, _, err := call(t, "ssh://fake:2222@BARE@", nil)
		wantTimeout(t, err)
		calls := exitCalls(log)
		if len(calls) != 1 {
			t.Fatalf("%d ssh -O exit call(s), want 1:\n%s", len(calls), log)
		}
		order(t, calls[0], "-O", "exit", "-p", "2222", "fake")
	})

	t.Run("the user's own ssh command is the one asked", func(t *testing.T) {
		log, _, err := call(t, "fake:@BARE@", func(t *testing.T) { t.Setenv("GIT_SSH_COMMAND", "ssh -4") })
		wantTimeout(t, err)
		calls := exitCalls(log)
		if len(calls) != 1 {
			t.Fatalf("%d ssh -O exit call(s), want 1:\n%s", len(calls), log)
		}
		order(t, calls[0], "-4", "-O", "exit")
	})

	t.Run("a connection that is the user's own is not touched", func(t *testing.T) {
		log, _, err := call(t, "fake:@BARE@", func(t *testing.T) { t.Setenv("FAKE_SSH_CONTROLPATH", "/home/u/.ssh/cm-%r@%h:%p") })
		wantTimeout(t, err)
		if calls := exitCalls(log); len(calls) != 0 {
			t.Errorf("lw ran ssh -O exit on a master it did not make:\n%s", strings.Join(calls, "\n"))
		}
	})

	t.Run("a failure that is not a timeout leaves the master alone", func(t *testing.T) {
		logPath, _ := muxEnv(t)
		p := newPair(t)
		if _, err := Status(t.Context(), opts(p.a, "dead:"+p.bare)); err == nil {
			t.Fatal("Status on a dead host succeeded")
		}
		if calls := exitCalls(readFile(t, logPath)); len(calls) != 0 {
			t.Errorf("ssh -O exit after a plain failure:\n%s", strings.Join(calls, "\n"))
		}
	})
}

// TestTrackingRefsAgree: stage finds a synced vault by the ref vaultsync
// fetches into (A-042-10); the two spellings must not drift apart.
func TestTrackingRefsAgree(t *testing.T) {
	if stage.SyncTrackingRef != trackRef {
		t.Errorf("stage.SyncTrackingRef = %q, vaultsync's trackRef = %q", stage.SyncTrackingRef, trackRef)
	}
}

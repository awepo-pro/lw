package vaultsync

// rebase_test.go pins 042 A-042-8: a divergence is rebased cleanly when it can
// be — the local commits are replayed on the remote's tip and pushed — and is
// refused exactly as before when it cannot, with the tree untouched. lw's own
// commits must keep being refused: two PCs that each ran one both wrote the
// same snapshot file and appended to the same journal.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aCommits makes and pushes one commit on PC A.
func aCommits(t *testing.T, p pair, files map[string]string, msg string) {
	t.Helper()
	for rel, body := range files {
		put(t, p.a, rel, body)
	}
	if _, err := CommitWork(opts(p.a, p.remote), msg); err != nil {
		t.Fatalf("A CommitWork: %v", err)
	}
	if st, err := Push(t.Context(), opts(p.a, p.remote)); err != nil || st.Pushed != 1 {
		t.Fatalf("A Push = %+v, %v", st, err)
	}
}

// bCommitsLocally makes a commit on PC B that is not pushed.
func bCommitsLocally(t *testing.T, p pair, files map[string]string, msg string) {
	t.Helper()
	for rel, body := range files {
		put(t, p.b, rel, body)
	}
	if c, err := CommitWork(opts(p.b, p.remote), msg); err != nil || !c {
		t.Fatalf("B CommitWork = %v, %v", c, err)
	}
}

// rebaseInProgress reports whether the vault has a rebase's state directory.
func rebaseInProgress(dir string) bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(dir, ".git", d)); err == nil {
			return true
		}
	}
	return false
}

// TestPullRebasesANotesOnlyDivergence: PC-A pushed a note while PC-B, offline,
// committed another; B's Pull replays its commit on A's — no merge commit — and
// the Push that follows sends it. Both PCs end with every note.
func TestPullRebasesANotesOnlyDivergence(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
	bCommitsLocally(t, p, map[string]string{"notes/from-b-1.md": "b1\n"}, "lw notes")
	bCommitsLocally(t, p, map[string]string{"notes/from-b-2.md": "b2\n"}, "lw notes")
	before := git(t, p.b, "rev-parse", "HEAD")

	st, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if st.Pulled != 1 || st.Rebased != 2 || st.Ahead != 2 || st.Behind != 0 || st.Remote != p.remote {
		t.Fatalf("State = %+v, want Pulled 1, Rebased 2, Ahead 2, Behind 0", st)
	}
	if got := git(t, p.b, "rev-parse", "refs/lw/pre-rebase"); got != before {
		t.Errorf("refs/lw/pre-rebase = %s, want the HEAD before the rebase, %s", got, before)
	}
	if n := git(t, p.b, "rev-list", "--merges", "--count", "HEAD"); n != "0" {
		t.Errorf("the rebase made %s merge commit(s)", n)
	}
	for _, f := range []string{"notes/from-a.md", "notes/from-b-1.md", "notes/from-b-2.md"} {
		if readFile(t, filepath.Join(p.b, f)) == "" {
			t.Errorf("B lacks %s after the rebase", f)
		}
	}
	if rebaseInProgress(p.b) {
		t.Error("a rebase is still in progress")
	}
	if _, err := os.Stat(filepath.Join(p.b, ".git", "lw-rebase")); err == nil {
		t.Error("the rebase's state directory was left behind")
	}

	push, err := Push(t.Context(), appendOpts(p.b, p.remote))
	if err != nil || push.Pushed != 2 {
		t.Fatalf("Push = %+v, %v; want the 2 replayed commits sent", push, err)
	}
	if st, err := Pull(t.Context(), appendOpts(p.a, p.remote), 1); err != nil || st.Pulled != 2 {
		t.Fatalf("A Pull = %+v, %v", st, err)
	}
	for _, f := range []string{"notes/from-a.md", "notes/from-b-1.md", "notes/from-b-2.md"} {
		if readFile(t, filepath.Join(p.a, f)) == "" {
			t.Errorf("A lacks %s", f)
		}
	}
}

// TestPullRebasesEditsToDifferentPages: each side hand-edited a different wiki
// page — nothing conflicts.
func TestPullRebasesEditsToDifferentPages(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"wiki/alpha.md": "A's edit\n"}, "lw sync")
	bCommitsLocally(t, p, map[string]string{"index.md": "B's edit\n"}, "lw sync")
	st, err := Pull(t.Context(), opts(p.b, p.remote), 1)
	if err != nil || st.Rebased != 1 {
		t.Fatalf("Pull = %+v, %v; want a clean rebase", st, err)
	}
	if readFile(t, filepath.Join(p.b, "wiki/alpha.md")) != "A's edit\n" || readFile(t, filepath.Join(p.b, "index.md")) != "B's edit\n" {
		t.Error("the rebased tree lacks one side's edit")
	}
}

// TestPullRefusesWhenTheSamePageWasEditedOnBothSides: a conflict aborts the
// rebase, leaves HEAD and every byte of the tree as they were, and returns
// ErrDiverged with the counts — as before.
func TestPullRefusesWhenTheSamePageWasEditedOnBothSides(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"wiki/alpha.md": "A's version\n"}, "lw sync")
	bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
	head := git(t, p.b, "rev-parse", "HEAD")
	treeBefore := workTree(t, p.b)

	st, err := Pull(t.Context(), opts(p.b, p.remote), 1)
	if !errors.Is(err, ErrDiverged) || st.Ahead != 1 || st.Behind != 1 || st.Rebased != 0 || st.Pulled != 0 {
		t.Fatalf("Pull = %+v, %v; want ErrDiverged with Ahead 1 Behind 1", st, err)
	}
	if git(t, p.b, "rev-parse", "HEAD") != head {
		t.Error("HEAD moved")
	}
	sameTree(t, workTree(t, p.b), treeBefore, "after the refused rebase")
	if rebaseInProgress(p.b) {
		t.Error("the aborted rebase left its state directory")
	}
	if got := git(t, p.b, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("the tree is not clean after the abort:\n%s", got)
	}
	if got := git(t, p.b, "rev-parse", "refs/lw/pre-rebase"); got != head {
		t.Errorf("refs/lw/pre-rebase = %s, want %s", got, head)
	}
	if _, err := os.Stat(filepath.Join(p.b, ".git", "lw-rebase")); err == nil {
		t.Error("the rebase's state directory was left behind")
	}
}

// TestLWCommitsOnBothSidesStayRefused: two PCs that each ran an lw commit wrote
// the same snapshot file (the next id) with different bytes and appended to the
// same journal. That must never be rebased: the ids and the audit trail would
// be two histories pretending to be one.
func TestLWCommitsOnBothSidesStayRefused(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	lwCommit := func(dir, who string) {
		put(t, dir, ".llmwiki/snapshots/000002.tree", "tree written by "+who+"\n")
		appendTo(t, dir, journalPath, `{"kind":"commit_end","commit":"000002","message":"`+who+`"}`+"\n")
		put(t, dir, "wiki/"+who+"-page.md", who+"\n")
		if c, err := CommitWork(opts(dir, p.remote), "lw 000002: "+who); err != nil || !c {
			t.Fatalf("%s CommitWork = %v, %v", who, c, err)
		}
	}
	lwCommit(p.a, "a")
	if _, err := Push(t.Context(), opts(p.a, p.remote)); err != nil {
		t.Fatal(err)
	}
	lwCommit(p.b, "b")
	head := git(t, p.b, "rev-parse", "HEAD")
	treeBefore := workTree(t, p.b)

	for _, op := range []string{"Pull", "Push"} {
		var st State
		var err error
		if op == "Pull" {
			st, err = Pull(t.Context(), appendOpts(p.b, p.remote), 1)
		} else {
			st, err = Push(t.Context(), appendOpts(p.b, p.remote))
		}
		if !errors.Is(err, ErrDiverged) || st.Ahead != 1 || st.Behind != 1 {
			t.Fatalf("%s = %+v, %v; want ErrDiverged 1/1", op, st, err)
		}
		if git(t, p.b, "rev-parse", "HEAD") != head {
			t.Errorf("%s moved HEAD", op)
		}
		sameTree(t, workTree(t, p.b), treeBefore, "after the refused "+op)
	}
}

// TestPushRebasesAfterTheRemoteMoved: Push's own divergence path — the commit
// hook's push, run after another PC pushed — rebases and sends in one call.
func TestPushRebasesAfterTheRemoteMoved(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
	bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
	st, err := Push(t.Context(), opts(p.b, p.remote))
	if err != nil || st.Pushed != 1 || st.Rebased != 1 || st.Pulled != 1 || st.Ahead != 0 || st.Behind != 0 {
		t.Fatalf("Push = %+v, %v; want Pulled 1, Rebased 1, Pushed 1", st, err)
	}
	if got := git(t, p.bare, "--git-dir="+p.bare, "rev-parse", "main"); got != git(t, p.b, "rev-parse", "HEAD") {
		t.Errorf("the remote tip %s is not B's HEAD", got)
	}
	if readFile(t, filepath.Join(p.b, "notes/from-a.md")) == "" {
		t.Error("B lacks A's note")
	}
}

// TestPushRefusesAConflictingDivergence: the same, with a conflict — nothing is
// pushed, nothing changes.
func TestPushRefusesAConflictingDivergence(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"wiki/alpha.md": "A's version\n"}, "lw sync")
	bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
	head := git(t, p.b, "rev-parse", "HEAD")
	remoteTip := git(t, p.bare, "--git-dir="+p.bare, "rev-parse", "main")
	st, err := Push(t.Context(), opts(p.b, p.remote))
	if !errors.Is(err, ErrDiverged) || st.Pushed != 0 || st.Ahead != 1 || st.Behind != 1 {
		t.Fatalf("Push = %+v, %v; want ErrDiverged", st, err)
	}
	if git(t, p.b, "rev-parse", "HEAD") != head || git(t, p.bare, "--git-dir="+p.bare, "rev-parse", "main") != remoteTip {
		t.Error("a refused Push moved something")
	}
}

// TestNoRebaseUnlessDiverged: a fast-forward is a fast-forward, and a push of
// local commits alone is a push — git rebase is not run for either.
func TestNoRebaseUnlessDiverged(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
	st, err := Pull(t.Context(), opts(p.b, p.remote), 1)
	if err != nil || st.Pulled != 1 || st.Rebased != 0 {
		t.Fatalf("Pull = %+v, %v; want a plain fast-forward", st, err)
	}
	bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
	st, err = Push(t.Context(), opts(p.b, p.remote))
	if err != nil || st.Pushed != 1 || st.Rebased != 0 || st.Pulled != 0 {
		t.Fatalf("Push = %+v, %v; want a plain push", st, err)
	}
	if out, err := gitErr(p.b, "rev-parse", "--verify", "--quiet", "refs/lw/pre-rebase"); err == nil {
		t.Errorf("a rebase ran (refs/lw/pre-rebase = %s) though the vault never diverged", out)
	}
}

// TestRebaseCarriesTheJournalTail: the uncommitted journal lines of an open
// changeset ride across the rebase like they ride across a fast-forward.
func TestRebaseCarriesTheJournalTail(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	base := readFile(t, filepath.Join(p.a, journalPath))
	aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
	appendTo(t, p.a, journalPath, "a-line\n")
	if _, err := CommitWork(opts(p.a, p.remote), "lw sync"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(t.Context(), opts(p.a, p.remote)); err != nil {
		t.Fatal(err)
	}
	bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
	appendTo(t, p.b, journalPath, "b-1\nb-2\n")

	st, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1)
	if err != nil || st.Rebased != 1 {
		t.Fatalf("Pull = %+v, %v", st, err)
	}
	if got, want := readFile(t, filepath.Join(p.b, journalPath)), base+"a-line\n"+"b-1\nb-2\n"; got != want {
		t.Errorf("journal = %q, want A's lines then B's tail %q", got, want)
	}
	if got := git(t, p.b, "status", "--porcelain"); got != "M "+journalPath {
		t.Errorf("status = %q, want only the journal modified", got)
	}
}

// TestRebaseConflictRestoresTheJournalTail: the same, ending in a refusal.
func TestRebaseConflictRestoresTheJournalTail(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"wiki/alpha.md": "A's version\n"}, "lw sync")
	bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
	appendTo(t, p.b, journalPath, "b-1\nb-2\n")
	want := readFile(t, filepath.Join(p.b, journalPath))
	if _, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1); !errors.Is(err, ErrDiverged) {
		t.Fatalf("Pull err = %v, want ErrDiverged", err)
	}
	if got := readFile(t, filepath.Join(p.b, journalPath)); got != want {
		t.Errorf("journal = %q, want the tail back as it was: %q", got, want)
	}
}

// TestRebaseNeedsACleanTree: anything uncommitted that cannot be carried keeps
// Pull at ErrDirty — it does not start a rebase it cannot finish.
func TestRebaseNeedsACleanTree(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
	bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
	put(t, p.b, "wiki/alpha.md", "edited, not committed\n")
	head := git(t, p.b, "rev-parse", "HEAD")
	if _, err := Pull(t.Context(), opts(p.b, p.remote), 1); !errors.Is(err, ErrDirty) {
		t.Fatalf("Pull err = %v, want ErrDirty", err)
	}
	// Push has no ErrDirty to give: it refuses as it always did.
	if _, err := Push(t.Context(), opts(p.b, p.remote)); !errors.Is(err, ErrDiverged) {
		t.Fatalf("Push err = %v, want ErrDiverged", err)
	}
	if git(t, p.b, "rev-parse", "HEAD") != head || readFile(t, filepath.Join(p.b, "wiki/alpha.md")) != "edited, not committed\n" {
		t.Error("a refusal changed the vault")
	}
}

// TestRebaseUntrackedFiles: an untracked file the replay would overwrite is
// removed when it is the remote's own bytes and refused when it is not.
func TestRebaseUntrackedFiles(t *testing.T) {
	const cas = ".llmwiki/objects/cd/cd1234"
	setup := func(t *testing.T, bytes string) pair {
		hermetic(t)
		p := newPair(t)
		aCommits(t, p, map[string]string{cas: "object bytes\n"}, "lw sync")
		bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
		put(t, p.b, cas, bytes)
		return p
	}
	t.Run("identical", func(t *testing.T) {
		p := setup(t, "object bytes\n")
		st, err := Pull(t.Context(), opts(p.b, p.remote), 1)
		if err != nil || st.Rebased != 1 {
			t.Fatalf("Pull = %+v, %v", st, err)
		}
		if readFile(t, filepath.Join(p.b, cas)) != "object bytes\n" {
			t.Error("the object is not the remote's after the rebase")
		}
	})
	t.Run("different", func(t *testing.T) {
		p := setup(t, "b's bytes\n")
		head := git(t, p.b, "rev-parse", "HEAD")
		_, err := Pull(t.Context(), opts(p.b, p.remote), 1)
		want := "untracked files would be overwritten by the pull: " + cas + " — move them aside and run lw sync again"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
		if git(t, p.b, "rev-parse", "HEAD") != head || readFile(t, filepath.Join(p.b, cas)) != "b's bytes\n" {
			t.Error("a refused rebase changed the vault")
		}
		if out, err := gitErr(p.b, "rev-parse", "--verify", "--quiet", "refs/lw/pre-rebase"); err == nil {
			t.Errorf("a refused pull saved a pre-rebase ref (%s): nothing was to change", out)
		}
	})
}

// TestRebaseRunsNoHooks: neither the user's global core.hooksPath nor a hook in
// the vault's own .git runs during the rebase — a failing pre-rebase hook there
// would otherwise turn every clean rebase into a refusal.
func TestRebaseRunsNoHooks(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
	bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")

	ran := filepath.Join(t.TempDir(), "hook-ran")
	hooks := t.TempDir()
	script := "#!/bin/sh\necho \"$0\" >> " + ran + "\nexit 1\n"
	for _, name := range []string{"pre-rebase", "post-rewrite", "post-checkout", "post-commit", "pre-commit", "commit-msg", "prepare-commit-msg"} {
		put(t, hooks, name, script)
		if err := os.Chmod(filepath.Join(hooks, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeGlobalGitConfig(t, "[core]\n\thooksPath = "+hooks+"\n")
	put(t, p.b, ".git/hooks/pre-rebase", script)
	if err := os.Chmod(filepath.Join(p.b, ".git", "hooks", "pre-rebase"), 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := Pull(t.Context(), opts(p.b, p.remote), 1)
	if err != nil || st.Rebased != 1 {
		t.Fatalf("Pull = %+v, %v; want the rebase to run past the failing hooks", st, err)
	}
	if b, err := os.ReadFile(ran); err == nil {
		t.Errorf("hooks ran during the rebase:\n%s", b)
	}
}

// --- an interrupted rebase ---------------------------------------------------

// interruptedRebase leaves PC B as a crash during lw's rebase would: the
// pre-rebase ref and lw's state record written, the journal tail set aside, and
// git stopped in the middle of a conflicting rebase. It returns B's HEAD before
// the rebase and the tail that was set aside.
func interruptedRebase(t *testing.T, p pair) (pre, tail string) {
	t.Helper()
	aCommits(t, p, map[string]string{"wiki/alpha.md": "A's version\n"}, "lw sync")
	bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
	tail = "b-1\nb-2\n"
	head := readFile(t, filepath.Join(p.b, journalPath))
	appendTo(t, p.b, journalPath, tail)
	pre = git(t, p.b, "rev-parse", "HEAD")
	git(t, p.b, "update-ref", "refs/lw/pre-rebase", pre)
	r, err := newRunner(opts(p.b, p.remote))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.saveRebaseState(t.Context(), rebaseState{Pre: pre, Carry: []carryRecord{{Path: journalPath, Tail: []byte(tail)}}}); err != nil {
		t.Fatal(err)
	}
	put(t, p.b, journalPath, head)                                      // what lw does before it rebases: the HEAD bytes back
	if _, err := Status(t.Context(), opts(p.b, p.remote)); err != nil { // fetch: the rebase needs the remote's tip
		t.Fatal(err)
	}
	if _, err := gitErr(p.b, "rebase", "--quiet", "refs/remotes/lw/main"); err == nil {
		t.Fatal("setup: the rebase did not conflict")
	}
	if !rebaseInProgress(p.b) {
		t.Fatal("setup: no rebase in progress")
	}
	return pre, tail
}

// TestInterruptedRebaseIsUndone: a crash leaves .git/rebase-merge behind. The
// next CommitWork, Pull, Push or TakeRemote aborts it, puts the vault back at
// refs/lw/pre-rebase and gives the journal its lines back — once.
func TestInterruptedRebaseIsUndone(t *testing.T) {
	entries := map[string]func(t *testing.T, p pair){
		"CommitWork": func(t *testing.T, p pair) {
			if _, err := CommitWork(opts(p.b, p.remote), "lw sync"); err != nil {
				t.Fatalf("CommitWork: %v", err)
			}
		},
		"Pull": func(t *testing.T, p pair) {
			// The recovery, then the divergence it left — which conflicts again.
			if _, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1); !errors.Is(err, ErrDiverged) {
				t.Fatalf("Pull err = %v, want ErrDiverged", err)
			}
		},
		"Push": func(t *testing.T, p pair) {
			if _, err := Push(t.Context(), opts(p.b, p.remote)); !errors.Is(err, ErrDiverged) {
				t.Fatalf("Push err = %v, want ErrDiverged", err)
			}
		},
		"TakeRemote": func(t *testing.T, p pair) {
			if _, _, err := TakeRemote(t.Context(), opts(p.b, p.remote), 1); err != nil {
				t.Fatalf("TakeRemote: %v", err)
			}
		},
	}
	for name, entry := range entries {
		t.Run(name, func(t *testing.T) {
			hermetic(t)
			p := newPair(t)
			pre, tail := interruptedRebase(t, p)
			entry(t, p)
			if rebaseInProgress(p.b) {
				t.Error("the interrupted rebase is still in progress")
			}
			if _, err := os.Stat(filepath.Join(p.b, ".git", "lw-rebase")); err == nil {
				t.Error("lw's rebase record is still there")
			}
			if name == "TakeRemote" {
				return // the remote's tree now; the backup branch holds pre
			}
			if got := git(t, p.b, "rev-parse", "HEAD"); got != pre && name != "CommitWork" {
				t.Errorf("HEAD = %s, want the pre-rebase %s", got, pre)
			}
			if !strings.HasSuffix(readFile(t, filepath.Join(p.b, journalPath)), tail) || strings.Count(readFile(t, filepath.Join(p.b, journalPath)), tail) != 1 {
				t.Errorf("journal = %q, want the set-aside lines back exactly once", readFile(t, filepath.Join(p.b, journalPath)))
			}
		})
	}
}

// TestRecoverReportsWhatItUndid: Recover is the call cmd makes at the start of a
// sync step to tell the user; it says nothing when there is nothing to undo.
func TestRecoverReportsWhatItUndid(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	if note, err := Recover(t.Context(), opts(p.b, p.remote)); err != nil || note != "" {
		t.Fatalf("Recover on a quiet vault = %q, %v", note, err)
	}
	pre, _ := interruptedRebase(t, p)
	note, err := Recover(t.Context(), opts(p.b, p.remote))
	if err != nil || !strings.Contains(note, "interrupted rebase") || !strings.Contains(note, "refs/lw/pre-rebase") {
		t.Fatalf("Recover = %q, %v; want a note naming the interrupted rebase and the backup ref", note, err)
	}
	if git(t, p.b, "rev-parse", "HEAD") != pre || rebaseInProgress(p.b) {
		t.Error("the vault is not back at the pre-rebase state")
	}
	if note, _ := Recover(t.Context(), opts(p.b, p.remote)); note != "" {
		t.Errorf("a second Recover said %q", note)
	}
}

// TestRecoverAfterACompletedRebase: a crash after git finished but before lw
// removed its record leaves a consistent vault; the record goes, nothing is
// reset, and a tail that is already back is not appended twice.
func TestRecoverAfterACompletedRebase(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"notes/from-a.md": "a\n"}, "lw notes")
	bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
	appendTo(t, p.b, journalPath, "b-1\n")
	pre := git(t, p.b, "rev-parse", "HEAD")
	r, err := newRunner(opts(p.b, p.remote))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.saveRebaseState(t.Context(), rebaseState{Pre: pre, Done: true, Carry: []carryRecord{{Path: journalPath, Tail: []byte("b-1\n")}}}); err != nil {
		t.Fatal(err)
	}
	// git's rebase ran to the end and lw appended the tail back; then it died.
	if _, err := Status(t.Context(), opts(p.b, p.remote)); err != nil { // fetch, so the rebase moves HEAD
		t.Fatal(err)
	}
	git(t, p.b, "stash") // the tail is lw's to carry, not git's
	if _, err := gitErr(p.b, "rebase", "--quiet", "refs/remotes/lw/main"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	appendTo(t, p.b, journalPath, "b-1\n")
	done := git(t, p.b, "rev-parse", "HEAD")
	if done == pre {
		t.Fatal("setup: the rebase did not move HEAD")
	}
	want := readFile(t, filepath.Join(p.b, journalPath))

	if _, err := Recover(t.Context(), opts(p.b, p.remote)); err != nil {
		t.Fatal(err)
	}
	if git(t, p.b, "rev-parse", "HEAD") != done {
		t.Error("Recover reset a completed rebase")
	}
	if got := readFile(t, filepath.Join(p.b, journalPath)); got != want {
		t.Errorf("journal = %q, want it untouched: %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(p.b, ".git", "lw-rebase")); err == nil {
		t.Error("lw's rebase record is still there")
	}
}

// TestAForeignRebaseIsLeftAlone: a rebase the user started in the vault is not
// lw's to abort. Every entry point refuses, naming it, and changes nothing.
func TestAForeignRebaseIsLeftAlone(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{"wiki/alpha.md": "A's version\n"}, "lw sync")
	bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
	if _, err := Status(t.Context(), opts(p.b, p.remote)); err != nil {
		t.Fatal(err)
	}
	if _, err := gitErr(p.b, "rebase", "--quiet", "refs/remotes/lw/main"); err == nil {
		t.Fatal("setup: the rebase did not conflict")
	}
	if !rebaseInProgress(p.b) {
		t.Fatal("setup: no rebase in progress")
	}
	for name, call := range map[string]func() error{
		"CommitWork": func() error { _, err := CommitWork(opts(p.b, p.remote), "x"); return err },
		"Pull":       func() error { _, err := Pull(t.Context(), opts(p.b, p.remote), 1); return err },
		"Push":       func() error { _, err := Push(t.Context(), opts(p.b, p.remote)); return err },
		"TakeRemote": func() error { _, _, err := TakeRemote(t.Context(), opts(p.b, p.remote), 1); return err },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "a git rebase is in progress") {
			t.Errorf("%s err = %v, want a refusal naming the rebase in progress", name, err)
		}
		if !rebaseInProgress(p.b) {
			t.Errorf("%s aborted a rebase lw did not start", name)
		}
	}
}

// TestAbortIsVerifiedNotTrusted: after `git rebase --abort` lw checks that HEAD
// and the tree are what they were, and puts them back itself if they are not.
// The seam stands in for an abort that did not finish the job.
func TestAbortIsVerifiedNotTrusted(t *testing.T) {
	conflict := func(t *testing.T) pair {
		hermetic(t)
		p := newPair(t)
		aCommits(t, p, map[string]string{"wiki/alpha.md": "A's version\n"}, "lw sync")
		bCommitsLocally(t, p, map[string]string{"wiki/alpha.md": "B's version\n"}, "lw sync")
		return p
	}
	hook := func(t *testing.T, fn func(r *runner)) {
		orig := afterRebaseAbort
		afterRebaseAbort = func(_ context.Context, r *runner) { fn(r) }
		t.Cleanup(func() { afterRebaseAbort = orig })
	}

	t.Run("a tree the abort left changed is put back", func(t *testing.T) {
		p := conflict(t)
		head := git(t, p.b, "rev-parse", "HEAD")
		treeBefore := workTree(t, p.b)
		hook(t, func(r *runner) { put(t, r.o.Dir, "wiki/alpha.md", "garbage the abort left\n") })
		if _, err := Pull(t.Context(), opts(p.b, p.remote), 1); !errors.Is(err, ErrDiverged) {
			t.Fatalf("Pull err = %v, want ErrDiverged", err)
		}
		if git(t, p.b, "rev-parse", "HEAD") != head {
			t.Error("HEAD moved")
		}
		sameTree(t, workTree(t, p.b), treeBefore, "after the repaired abort")
	})

	t.Run("a HEAD the abort left moved is put back", func(t *testing.T) {
		p := conflict(t)
		head := git(t, p.b, "rev-parse", "HEAD")
		treeBefore := workTree(t, p.b)
		hook(t, func(r *runner) { git(t, r.o.Dir, "reset", "--hard", "--quiet", "HEAD~1") })
		if _, err := Pull(t.Context(), opts(p.b, p.remote), 1); !errors.Is(err, ErrDiverged) {
			t.Fatalf("Pull err = %v, want ErrDiverged", err)
		}
		if git(t, p.b, "rev-parse", "HEAD") != head {
			t.Error("HEAD was not put back")
		}
		sameTree(t, workTree(t, p.b), treeBefore, "after the repaired abort")
	})

	t.Run("what cannot be restored is an error, kept for Recover", func(t *testing.T) {
		p := conflict(t)
		head := git(t, p.b, "rev-parse", "HEAD")
		treeBefore := workTree(t, p.b)
		lock := filepath.Join(p.b, ".git", "index.lock")
		hook(t, func(r *runner) {
			put(t, r.o.Dir, "wiki/alpha.md", "garbage the abort left\n")
			os.WriteFile(lock, nil, 0o644) // reset --hard will not get past it
		})
		_, err := Pull(t.Context(), opts(p.b, p.remote), 1)
		if err == nil || errors.Is(err, ErrDiverged) || !strings.Contains(err.Error(), "could not be restored") {
			t.Fatalf("err = %v; want an error saying the vault could not be restored", err)
		}
		if _, serr := os.Stat(filepath.Join(p.b, ".git", "lw-rebase", "state.json")); serr != nil {
			t.Error("lw's record was removed although the vault is not back")
		}
		os.Remove(lock)
		afterRebaseAbort = func(context.Context, *runner) {}
		if _, rerr := Recover(t.Context(), opts(p.b, p.remote)); rerr != nil {
			t.Fatalf("Recover: %v", rerr)
		}
		if git(t, p.b, "rev-parse", "HEAD") != head {
			t.Error("HEAD is not back after Recover")
		}
		sameTree(t, workTree(t, p.b), treeBefore, "after Recover")
	})
}

// TestPushWontRebaseOntoANewerFormat: Push takes no maxFormat, but replaying
// commits on a tip written by a newer lw would put this lw's shape under theirs.
// Options.MaxFormat is what it checks the remote against.
func TestPushWontRebaseOntoANewerFormat(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	aCommits(t, p, map[string]string{".llmwiki/format": "{\"version\": 2}\n"}, "lw sync")
	bCommitsLocally(t, p, map[string]string{"notes/from-b.md": "b\n"}, "lw notes")
	head := git(t, p.b, "rev-parse", "HEAD")

	o := opts(p.b, p.remote)
	o.MaxFormat = 1
	_, err := Push(t.Context(), o)
	var fe *FormatError
	if !errors.As(err, &fe) || fe.Have != 2 || fe.Max != 1 {
		t.Fatalf("Push err = %v, want a *FormatError{Have 2, Max 1}", err)
	}
	if git(t, p.b, "rev-parse", "HEAD") != head {
		t.Error("HEAD moved")
	}
	// An lw that does support format 2 rebases.
	o.MaxFormat = 2
	if st, err := Push(t.Context(), o); err != nil || st.Rebased != 1 || st.Pushed != 1 {
		t.Fatalf("Push with MaxFormat 2 = %+v, %v", st, err)
	}
}

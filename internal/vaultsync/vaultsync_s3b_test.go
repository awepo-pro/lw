package vaultsync

// vaultsync_s3b_test.go pins 042 A-042-7 (a) and (e): Pull carries an
// append-only file's uncommitted tail across a fast-forward, tells an untracked
// duplicate of an incoming file from a different one, wraps ErrDirty for any
// other uncommitted change, and git's progress is asked for only when a person
// can see it.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const journalPath = ".llmwiki/journal.ndjson"

// appendOpts is opts with the journal declared append-only, as cmd/lw does.
func appendOpts(dir string, remotes ...string) Options {
	o := opts(dir, remotes...)
	o.AppendOnly = []string{journalPath}
	return o
}

// appendTo adds text to the end of a vault file.
func appendTo(t *testing.T, root, rel, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(root, filepath.FromSlash(rel)), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// aPushes makes a commit on PC A — the journal gains lines, plus any extra
// files — and pushes it.
func aPushes(t *testing.T, p pair, lines string, extra map[string]string) {
	t.Helper()
	appendTo(t, p.a, journalPath, lines)
	for rel, body := range extra {
		put(t, p.a, rel, body)
	}
	if _, err := CommitWork(opts(p.a, p.remote), "lw 000002: a"); err != nil {
		t.Fatalf("A CommitWork: %v", err)
	}
	if st, err := Push(t.Context(), opts(p.a, p.remote)); err != nil || st.Pushed != 1 {
		t.Fatalf("A Push = %+v, %v", st, err)
	}
}

// TestPullCarriesAppendOnlyOver: PC-B has 2 uncommitted journal lines, PC-A
// pushed one commit appending 3 lines. B's Pull fast-forwards, and the journal
// is A's version plus B's 2 lines, byte for byte — with no local commit.
func TestPullCarriesAppendOnlyOver(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	base := readFile(t, filepath.Join(p.a, journalPath))

	appendTo(t, p.b, journalPath, "b-1\nb-2\n")
	aPushes(t, p, "a-1\na-2\na-3\n", map[string]string{"wiki/from-a.md": "from a\n"})

	st, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if st.Pulled != 1 || st.Behind != 0 || st.Ahead != 0 || st.Remote != p.remote {
		t.Fatalf("State = %+v, want Pulled 1, Behind 0, Ahead 0", st)
	}
	if got, want := readFile(t, filepath.Join(p.b, journalPath)), base+"a-1\na-2\na-3\n"+"b-1\nb-2\n"; got != want {
		t.Fatalf("journal =\n%q\nwant A's version then B's lines\n%q", got, want)
	}
	if got := readFile(t, filepath.Join(p.b, "wiki/from-a.md")); got != "from a\n" {
		t.Errorf("the pulled file = %q", got)
	}
	if b, a := git(t, p.b, "rev-parse", "HEAD"), git(t, p.a, "rev-parse", "HEAD"); b != a {
		t.Errorf("B's HEAD %s is not A's %s: the pull made a commit", b, a)
	}
	if got := git(t, p.b, "status", "--porcelain"); got != "M "+journalPath && got != " M "+journalPath {
		t.Errorf("status after the pull = %q, want only the journal modified", got)
	}

	// The carried lines then travel like any others.
	if c, err := CommitWork(opts(p.b, p.remote), "lw sync"); err != nil || !c {
		t.Fatalf("B CommitWork = %v, %v", c, err)
	}
	if st, err := Push(t.Context(), opts(p.b, p.remote)); err != nil || st.Pushed != 1 {
		t.Fatalf("B Push = %+v, %v", st, err)
	}
	if got := gitRaw(t, p.bare, "--git-dir="+p.bare, "show", "main:"+journalPath); got != base+"a-1\na-2\na-3\n"+"b-1\nb-2\n" {
		t.Errorf("the remote's journal = %q", got)
	}
}

// TestPullAppendOnlyOtherUncommittedChangesAreErrDirty: anything but a pure
// extension of an append-only file still stops the pull, with an error that
// wraps ErrDirty and keeps its old text — and nothing is changed.
func TestPullAppendOnlyOtherUncommittedChangesAreErrDirty(t *testing.T) {
	cases := []struct {
		name  string
		dirty func(t *testing.T, b string)
		opt   bool // declare the journal append-only
	}{
		{"a line edited in place", func(t *testing.T, b string) {
			put(t, b, journalPath, strings.Replace(readFile(t, filepath.Join(b, journalPath)), "synced", "EDITED", 1))
		}, true},
		{"the journal truncated", func(t *testing.T, b string) { put(t, b, journalPath, "syn") }, true},
		{"the journal emptied", func(t *testing.T, b string) { put(t, b, journalPath, "") }, true},
		{"the journal deleted", func(t *testing.T, b string) { os.Remove(filepath.Join(b, journalPath)) }, true},
		{"a pure append plus another tracked file edited", func(t *testing.T, b string) {
			appendTo(t, b, journalPath, "b-1\n")
			put(t, b, "wiki/alpha.md", "edited, not committed\n")
		}, true},
		{"a staged append", func(t *testing.T, b string) {
			appendTo(t, b, journalPath, "b-1\n")
			git(t, b, "add", journalPath)
		}, true},
		{"a pure append to a file nobody declared append-only", func(t *testing.T, b string) {
			appendTo(t, b, journalPath, "b-1\n")
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hermetic(t)
			p := newPair(t)
			tc.dirty(t, p.b)
			aPushes(t, p, "a-1\n", nil)
			headBefore := git(t, p.b, "rev-parse", "HEAD")
			treeBefore := workTree(t, p.b)

			o := opts(p.b, p.remote)
			if tc.opt {
				o = appendOpts(p.b, p.remote)
			}
			st, err := Pull(t.Context(), o, 1)
			if !errors.Is(err, ErrDirty) {
				t.Fatalf("err = %v, want one wrapping ErrDirty", err)
			}
			if want := "the vault has uncommitted changes — run git -C " + p.b + " status"; err.Error() != want {
				t.Errorf("text = %q, want the old text %q", err.Error(), want)
			}
			if st.Remote != p.remote || st.Behind != 1 || st.Pulled != 0 {
				t.Errorf("State = %+v, want the fetched counts (Behind 1) and Pulled 0", st)
			}
			if git(t, p.b, "rev-parse", "HEAD") != headBefore {
				t.Error("HEAD moved")
			}
			sameTree(t, workTree(t, p.b), treeBefore, "after the refused Pull")
		})
	}
}

// TestPullErrDirtyIsExported: the sentinel exists for callers to test with
// errors.Is, and its own text does not repeat the directory.
func TestPullErrDirtyIsExported(t *testing.T) {
	if ErrDirty == nil || !strings.Contains(ErrDirty.Error(), "uncommitted changes") {
		t.Fatalf("ErrDirty = %v", ErrDirty)
	}
}

// TestPullAppendOnlyNothingToPull: a dirty journal and a remote with no news
// is no error and no change.
func TestPullAppendOnlyNothingToPull(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	appendTo(t, p.b, journalPath, "b-1\n")
	want := readFile(t, filepath.Join(p.b, journalPath))
	st, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1)
	if err != nil || st.Pulled != 0 || st.Behind != 0 {
		t.Fatalf("Pull = %+v, %v", st, err)
	}
	if got := readFile(t, filepath.Join(p.b, journalPath)); got != want {
		t.Errorf("journal = %q, want %q", got, want)
	}
}

// TestPullAppendOnlyDivergedChangesNothing: a divergence is refused with the
// journal's uncommitted lines exactly where they were.
func TestPullAppendOnlyDivergedChangesNothing(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	put(t, p.b, "wiki/alpha.md", "b's edit\n")
	if _, err := CommitWork(opts(p.b, p.remote), "lw sync"); err != nil {
		t.Fatal(err)
	}
	appendTo(t, p.b, journalPath, "b-1\n")
	// A edits the same page (A-042-8: otherwise the divergence is rebased).
	aPushes(t, p, "a-1\n", map[string]string{"wiki/alpha.md": "a's edit\n"})
	treeBefore := workTree(t, p.b)
	st, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1)
	if !errors.Is(err, ErrDiverged) || st.Ahead != 1 || st.Behind != 1 {
		t.Fatalf("Pull = %+v, %v; want ErrDiverged 1/1", st, err)
	}
	sameTree(t, workTree(t, p.b), treeBefore, "after the diverged Pull")
}

// TestPullUntrackedIdenticalIsRemoved: an untracked file the incoming commits
// create, with the same bytes — a content-addressed object staged on both PCs —
// is removed first, so the pull succeeds and the file is the remote's.
func TestPullUntrackedIdenticalIsRemoved(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	const cas = ".llmwiki/objects/cd/cd1234"
	put(t, p.b, cas, "object bytes\n")
	put(t, p.b, ".llmwiki/objects/ef/ef5678", "only on b\n") // untracked, and not coming: stays
	aPushes(t, p, "a-1\n", map[string]string{cas: "object bytes\n"})

	st, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1)
	if err != nil || st.Pulled != 1 {
		t.Fatalf("Pull = %+v, %v", st, err)
	}
	if got := readFile(t, filepath.Join(p.b, cas)); got != "object bytes\n" {
		t.Errorf("%s = %q", cas, got)
	}
	if got := readFile(t, filepath.Join(p.b, ".llmwiki/objects/ef/ef5678")); got != "only on b\n" {
		t.Errorf("an untracked file the pull does not touch changed: %q", got)
	}
	if got := git(t, p.b, "status", "--porcelain", "--untracked-files=all"); strings.Contains(got, "cd1234") {
		t.Errorf("the object is still untracked after the pull: %q", got)
	}
	if b, a := git(t, p.b, "rev-parse", "HEAD"), git(t, p.a, "rev-parse", "HEAD"); b != a {
		t.Error("B did not fast-forward to A")
	}
}

// TestPullUntrackedDifferentIsRefused: an untracked file at a path the incoming
// commits create, with other bytes, stops the pull with the exact error, and
// nothing is changed — not that file, not an identical one beside it, not the
// journal's uncommitted lines.
func TestPullUntrackedDifferentIsRefused(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	put(t, p.b, ".llmwiki/objects/cd/cd1234", "b's bytes\n")       // differs
	put(t, p.b, ".llmwiki/snapshots/000002.tree", "b's tree\n")    // differs
	put(t, p.b, ".llmwiki/objects/ef/ef5678", "identical bytes\n") // identical: must survive a refusal
	appendTo(t, p.b, journalPath, "b-1\n")
	aPushes(t, p, "a-1\n", map[string]string{
		".llmwiki/objects/cd/cd1234":     "a's bytes\n",
		".llmwiki/snapshots/000002.tree": "a's tree\n",
		".llmwiki/objects/ef/ef5678":     "identical bytes\n",
	})
	headBefore := git(t, p.b, "rev-parse", "HEAD")
	treeBefore := workTree(t, p.b)

	st, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1)
	want := "untracked files would be overwritten by the pull: .llmwiki/objects/cd/cd1234, .llmwiki/snapshots/000002.tree" +
		" — move them aside and run lw sync again"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %q", err, want)
	}
	if st.Pulled != 0 {
		t.Errorf("Pulled = %d on a refused pull", st.Pulled)
	}
	if git(t, p.b, "rev-parse", "HEAD") != headBefore {
		t.Error("HEAD moved")
	}
	sameTree(t, workTree(t, p.b), treeBefore, "after the refused Pull")
}

// TestPullAppendOnlyUntrackedFileIsCarried: the append-only file is not in
// B's HEAD at all (a vault whose journal began after the first sync) and B has
// lines in it; A pushed the file. B's lines are the tail of A's.
func TestPullAppendOnlyUntrackedFileIsCarried(t *testing.T) {
	hermetic(t)
	ctx := t.Context()
	vault := makeVault(t)
	os.Remove(filepath.Join(vault, journalPath))
	remote := filepath.Join(t.TempDir(), "remote.git")
	if err := os.Mkdir(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(ctx, opts(vault, remote)); err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(t.TempDir(), "pc-b")
	if err := Clone(ctx, opts(b, remote), 1); err != nil {
		t.Fatal(err)
	}
	put(t, vault, journalPath, "a-1\n")
	if _, err := CommitWork(opts(vault, remote), "lw sync"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, opts(vault, remote)); err != nil {
		t.Fatal(err)
	}
	put(t, b, journalPath, "b-1\nb-2\n")

	st, err := Pull(ctx, appendOpts(b, remote), 1)
	if err != nil || st.Pulled != 1 {
		t.Fatalf("Pull = %+v, %v", st, err)
	}
	if got := readFile(t, filepath.Join(b, journalPath)); got != "a-1\nb-1\nb-2\n" {
		t.Errorf("journal = %q, want A's line then B's", got)
	}
}

// TestPullAppendOnlyMergeFailureLeavesTheFileAsItWas: when the merge itself
// fails, the carried lines go back where they were.
func TestPullAppendOnlyMergeFailureLeavesTheFileAsItWas(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	appendTo(t, p.b, journalPath, "b-1\nb-2\n")
	aPushes(t, p, "a-1\n", nil)
	want := readFile(t, filepath.Join(p.b, journalPath))
	headBefore := git(t, p.b, "rev-parse", "HEAD")
	// A stale index.lock makes the merge — and only the merge — fail.
	if err := os.WriteFile(filepath.Join(p.b, ".git", "index.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Pull(t.Context(), appendOpts(p.b, p.remote), 1)
	if err == nil {
		t.Fatal("Pull succeeded through a stale index.lock")
	}
	if got := readFile(t, filepath.Join(p.b, journalPath)); got != want {
		t.Errorf("journal after the failed merge = %q, want %q", got, want)
	}
	if git(t, p.b, "rev-parse", "HEAD") != headBefore {
		t.Error("HEAD moved")
	}
}

// TestPullRejectsAnAppendOnlyPathOutsideTheVault: a path that is not a
// vault-relative file is a caller bug, refused before anything runs.
func TestPullRejectsAnAppendOnlyPathOutsideTheVault(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", ".git/config", "."} {
		o := opts(p.b, p.remote)
		o.AppendOnly = []string{bad}
		if _, err := Pull(t.Context(), o, 1); err == nil || !strings.Contains(err.Error(), "AppendOnly") {
			t.Errorf("AppendOnly %q: err = %v, want a refusal naming AppendOnly", bad, err)
		}
	}
}

// TestIsTerminal: only a character device counts as somewhere a person is
// watching — a terminal, or /dev/null, which discards what it is shown.
func TestIsTerminal(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	reg, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	for name, tc := range map[string]struct {
		w    io.Writer
		want bool
	}{
		"a pipe":             {pw, false},
		"a regular file":     {reg, false},
		"a character device": {null, true},
		"a strings.Builder":  {&strings.Builder{}, false},
		"nil":                {nil, false},
	} {
		if got := isTerminal(tc.w); got != tc.want {
			t.Errorf("isTerminal(%s) = %v, want %v", name, got, tc.want)
		}
	}
}

// TestProgressOnlyOnATerminal: git is told --progress only when an
// interactive call's Stderr is somewhere a person is watching. A pipe — lw sync
// 2>log, a test, a wrapper — gets git's other stderr lines but not the carriage
// -return progress.
func TestProgressOnlyOnATerminal(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	var sb strings.Builder
	for name, tc := range map[string]struct {
		o    Options
		want bool
	}{
		"interactive on a terminal":     {Options{Interactive: true, Stderr: null}, true},
		"interactive on a pipe":         {Options{Interactive: true, Stderr: pw}, false},
		"interactive on a Builder":      {Options{Interactive: true, Stderr: &sb}, false},
		"interactive with no Stderr":    {Options{Interactive: true}, false},
		"non-interactive on a terminal": {Options{Stderr: null}, false},
	} {
		if got := (&runner{o: tc.o}).progress(); got != tc.want {
			t.Errorf("progress() for %s = %v, want %v", name, got, tc.want)
		}
	}

	// And end to end: a push through a pipe prints git's summary, no progress.
	hermetic(t)
	p := newPair(t)
	put(t, p.a, "wiki/alpha.md", strings.Repeat("a line of text\n", 400))
	if _, err := CommitWork(opts(p.a, p.remote), "lw sync"); err != nil {
		t.Fatal(err)
	}
	o := opts(p.a, p.remote)
	o.Interactive = true
	o.Stderr = pw
	if _, err := Push(t.Context(), o); err != nil {
		t.Fatalf("Push: %v", err)
	}
	pw.Close()
	raw, err := io.ReadAll(pr)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if !strings.Contains(out, "->") {
		t.Errorf("git's own stderr no longer reaches Stderr: %q", out)
	}
	for _, noise := range []string{"Counting objects", "Compressing objects", "Writing objects", "\r"} {
		if strings.Contains(out, noise) {
			t.Errorf("progress %q reached a pipe: %q", noise, out)
		}
	}
}

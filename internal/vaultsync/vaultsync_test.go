package vaultsync

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The tests in this file are the frozen block of 042 S1 (workflow 042,
// "Expected results (frozen)"). Their names are the contract; none may be
// deleted, weakened or renamed.

// TestIgnoreBytes pins D2: the managed .gitignore is exactly these bytes.
func TestIgnoreBytes(t *testing.T) {
	want := "# managed by lw sync: per-PC state, never synced\n" +
		"/.llmwiki/index.gob\n" +
		"/.llmwiki/cache/\n" +
		"/.llmwiki/tmp/\n" +
		"/.llmwiki/lock\n" +
		"/.llmwiki/logs/\n" +
		"/.llmwiki/traces/\n" +
		"/.llmwiki/changesets/open/\n" +
		"/.llmwiki/sync.json\n"
	if Ignore != want {
		t.Fatalf("Ignore = %q\nwant     %q", Ignore, want)
	}
	if Branch != "main" {
		t.Fatalf("Branch = %q, want main", Branch)
	}
}

// TestInitLocalBareAndPush: a vault plus an empty-dir remote gives a bare
// repo, one pushed commit, every synced file on the remote's main, and none
// of the per-PC files.
func TestInitLocalBareAndPush(t *testing.T) {
	hermetic(t)
	vault := makeVault(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	if err := os.Mkdir(remote, 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := Init(t.Context(), opts(vault, remote))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if st.Pushed != 1 || st.Remote != remote || st.Ahead != 0 || st.Behind != 0 {
		t.Fatalf("State = %+v, want Pushed 1, Remote %q, Ahead 0, Behind 0", st, remote)
	}
	if st.RemoteFormat != 1 {
		t.Fatalf("RemoteFormat = %d, want 1 (no .llmwiki/format means 1)", st.RemoteFormat)
	}

	if got := git(t, remote, "rev-parse", "--is-bare-repository"); got != "true" {
		t.Fatalf("remote is not a bare repo: %q", got)
	}
	if got := git(t, remote, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("remote HEAD = %q, want refs/heads/main", got)
	}
	want := append([]string{".gitignore"}, syncedFiles...)
	sort.Strings(want)
	if got := treeNames(t, remote, "main"); !reflect.DeepEqual(got, want) {
		t.Fatalf("remote main tree:\n got %v\nwant %v", got, want)
	}
	if got := readFile(t, filepath.Join(vault, ".gitignore")); got != Ignore {
		t.Fatalf("vault .gitignore = %q, want Ignore", got)
	}
	if n := git(t, remote, "rev-list", "--count", "main"); n != "1" {
		t.Fatalf("remote has %s commits, want 1", n)
	}
	if got := git(t, remote, "log", "-1", "--format=%s", "main"); got != "lw sync init" {
		t.Fatalf("first commit message = %q, want %q", got, "lw sync init")
	}
	// The local tracking ref must exist after Init: it is what marks the
	// vault as "under lw sync".
	if a, b := git(t, vault, "rev-parse", "HEAD"), git(t, vault, "rev-parse", "refs/remotes/lw/main"); a != b {
		t.Fatalf("refs/remotes/lw/main = %s, want HEAD %s", b, a)
	}
}

// TestInitOverSSHFake: a host:path remote through the fake ssh gives the same
// result, and the script the fake saw created the repo.
func TestInitOverSSHFake(t *testing.T) {
	home := hermetic(t)
	logPath := installFakeSSH(t)

	for _, tc := range []struct {
		name string
		path string // the remote path as the server's shell resolves it
		spec func(string) string
	}{
		{"absolute", filepath.Join(t.TempDir(), "remote.git"), func(p string) string { return "fake:" + p }},
		// home:lw-vault in the real deployment: relative to the login
		// directory, and the directory does not exist yet.
		{"relative to the login dir", filepath.Join(home, "lw-vault"), func(string) string { return "fake:lw-vault" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			vault := makeVault(t)
			spec := tc.spec(tc.path)
			st, err := Init(t.Context(), opts(vault, spec))
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if st.Pushed != 1 || st.Remote != spec {
				t.Fatalf("State = %+v, want Pushed 1, Remote %q", st, spec)
			}
			want := append([]string{".gitignore"}, syncedFiles...)
			sort.Strings(want)
			if got := treeNames(t, tc.path, "main"); !reflect.DeepEqual(got, want) {
				t.Fatalf("remote main tree:\n got %v\nwant %v", got, want)
			}
			log := readFile(t, logPath)
			if !strings.Contains(log, "git init --bare -b main") {
				t.Fatalf("the ssh script never ran git init --bare -b main; ssh saw:\n%s", log)
			}
			if !strings.Contains(log, "git-receive-pack") {
				t.Fatalf("the push did not go through ssh; ssh saw:\n%s", log)
			}
			// The init call itself is non-interactive too.
			first := strings.SplitN(log, "\n", 2)[0]
			if !strings.Contains(first, "[BatchMode=yes]") || !strings.Contains(first, "[ConnectTimeout=5]") {
				t.Fatalf("the init ssh call lacks the batch options: %s", first)
			}
		})
	}
}

// TestInitRefusals: every way Init says no, with its exact text.
func TestInitRefusals(t *testing.T) {
	hermetic(t)
	installFakeSSH(t)

	t.Run("remote already holds a vault", func(t *testing.T) {
		first := makeVault(t)
		bare := filepath.Join(t.TempDir(), "remote.git")
		if _, err := Init(t.Context(), opts(first, bare)); err != nil {
			t.Fatal(err)
		}
		tip := git(t, bare, "rev-parse", "main")
		second := makeVault(t)
		_, err := Init(t.Context(), opts(second, bare))
		want := "remote " + bare + " already holds a vault — use lw sync clone"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
		if _, serr := os.Stat(filepath.Join(second, ".git")); serr == nil {
			t.Fatal("a refused Init still left a .git in the vault")
		}
		if got := git(t, bare, "rev-parse", "main"); got != tip {
			t.Fatalf("remote main moved to %s from %s", got, tip)
		}
	})

	t.Run("remote over ssh already holds a vault", func(t *testing.T) {
		first := makeVault(t)
		bare := filepath.Join(t.TempDir(), "remote.git")
		if _, err := Init(t.Context(), opts(first, bare)); err != nil {
			t.Fatal(err)
		}
		spec := "fake:" + bare
		_, err := Init(t.Context(), opts(makeVault(t), spec))
		want := "remote " + spec + " already holds a vault — use lw sync clone"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("non-empty directory that is not a repo", func(t *testing.T) {
		dir := t.TempDir()
		put(t, dir, "junk.txt", "x\n")
		vault := makeVault(t)
		_, err := Init(t.Context(), opts(vault, dir))
		want := "remote " + dir + " is not empty and not a bare git repo"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
		if _, serr := os.Stat(filepath.Join(vault, ".git")); serr == nil {
			t.Fatal("a refused Init still left a .git in the vault")
		}
		if got := readFile(t, filepath.Join(dir, "junk.txt")); got != "x\n" {
			t.Fatalf("the refused remote was modified: %q", got)
		}
	})

	t.Run("non-bare work tree is not a bare repo", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-b", "main", ".")
		_, err := Init(t.Context(), opts(makeVault(t), dir))
		want := "remote " + dir + " is not empty and not a bare git repo"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("path is a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		put(t, filepath.Dir(file), "file", "x\n")
		_, err := Init(t.Context(), opts(makeVault(t), file))
		want := "remote " + file + " is not empty and not a bare git repo"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	for _, tc := range []struct{ name, spec, path string }{
		{"space in an ssh path", "fake:/srv/a b", "/srv/a b"},
		{"shell syntax in an ssh path", "fake:/srv/$(id)", "/srv/$(id)"},
		{"semicolon in an ssh path", "fake:/srv/a;b", "/srv/a;b"},
		{"space in a local path", "/srv/has space/r.git", "/srv/has space/r.git"},
		{"empty ssh path", "fake:", ""},
	} {
		t.Run("bad path char: "+tc.name, func(t *testing.T) {
			vault := makeVault(t)
			_, err := Init(t.Context(), opts(vault, tc.spec))
			want := "remote path \"" + tc.path + "\": only letters, digits and . _ / ~ - are allowed"
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if _, serr := os.Stat(filepath.Join(vault, ".git")); serr == nil {
				t.Fatal("a refused Init still left a .git in the vault")
			}
		})
	}

	t.Run("local vault already under lw sync", func(t *testing.T) {
		vault := makeVault(t)
		if _, err := Init(t.Context(), opts(vault, filepath.Join(t.TempDir(), "r1.git"))); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(t.TempDir(), "r2.git")
		_, err := Init(t.Context(), opts(vault, other))
		if err == nil || err.Error() != "already under lw sync" {
			t.Fatalf("err = %v, want %q", err, "already under lw sync")
		}
		if _, serr := os.Stat(other); serr == nil {
			t.Fatal("a refused Init created the second remote")
		}
	})
}

// TestCloneThenPullPush: the everyday cycle across two PCs.
func TestCloneThenPullPush(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()

	// B is a faithful copy of A's synced state, and has none of A's
	// per-PC files.
	for _, rel := range syncedFiles {
		if got, want := readFile(t, filepath.Join(p.b, rel)), "synced "+rel+"\n"; got != want {
			t.Errorf("clone %s = %q, want %q", rel, got, want)
		}
	}
	for _, rel := range ignoredFiles {
		if _, err := os.Stat(filepath.Join(p.b, rel)); err == nil {
			t.Errorf("clone carries the per-PC file %s", rel)
		}
	}
	if got := git(t, p.b, "remote"); got != "" {
		t.Errorf("the clone kept a git remote %q; lw tracks refs/remotes/lw/main itself", got)
	}
	if a, b := git(t, p.b, "rev-parse", "HEAD"), git(t, p.b, "rev-parse", "refs/remotes/lw/main"); a != b {
		t.Errorf("clone: refs/remotes/lw/main = %s, want HEAD %s", b, a)
	}

	// A commits and pushes.
	put(t, p.a, "wiki/alpha.md", "alpha v2\n")
	committed, err := CommitWork(opts(p.a, p.remote), "lw 000002: edit alpha")
	if err != nil || !committed {
		t.Fatalf("A CommitWork = %v, %v; want true, nil", committed, err)
	}
	if again, err := CommitWork(opts(p.a, p.remote), "lw nothing"); err != nil || again {
		t.Fatalf("a second CommitWork with nothing new = %v, %v; want false, nil", again, err)
	}
	st, err := Push(ctx, opts(p.a, p.remote))
	if err != nil {
		t.Fatalf("A Push: %v", err)
	}
	if st.Pushed != 1 || st.Ahead != 0 || st.Behind != 0 || st.Remote != p.remote {
		t.Fatalf("A Push State = %+v, want Pushed 1, Ahead 0, Behind 0", st)
	}
	if got, want := git(t, p.bare, "rev-parse", "main"), git(t, p.a, "rev-parse", "HEAD"); got != want {
		t.Fatalf("remote main = %s, want A's HEAD %s", got, want)
	}
	if got := git(t, p.a, "log", "-1", "--format=%s"); got != "lw 000002: edit alpha" {
		t.Fatalf("A commit message = %q", got)
	}
	// A successful push records where the remote now is.
	if got, want := git(t, p.a, "rev-parse", "refs/remotes/lw/main"), git(t, p.a, "rev-parse", "HEAD"); got != want {
		t.Fatalf("A refs/remotes/lw/main = %s after the push, want HEAD %s", got, want)
	}

	// B is one behind, then pulls.
	st, err = Status(ctx, opts(p.b, p.remote))
	if err != nil || st.Behind != 1 || st.Ahead != 0 || st.Pulled != 0 {
		t.Fatalf("B Status = %+v, %v; want Behind 1, Ahead 0", st, err)
	}
	st, err = Pull(ctx, opts(p.b, p.remote), 1)
	if err != nil {
		t.Fatalf("B Pull: %v", err)
	}
	if st.Pulled != 1 || st.Behind != 0 || st.Ahead != 0 {
		t.Fatalf("B Pull State = %+v, want Pulled 1, Behind 0, Ahead 0", st)
	}
	if got := readFile(t, filepath.Join(p.b, "wiki/alpha.md")); got != "alpha v2\n" {
		t.Fatalf("B wiki/alpha.md = %q after the pull", got)
	}
	// A pull with nothing new is a no-op.
	st, err = Pull(ctx, opts(p.b, p.remote), 1)
	if err != nil || st.Pulled != 0 {
		t.Fatalf("B second Pull = %+v, %v; want Pulled 0", st, err)
	}

	// B commits and pushes; A pulls.
	put(t, p.b, "wiki/beta.md", "beta\n")
	if committed, err := CommitWork(opts(p.b, p.remote), "lw 000003: add beta"); err != nil || !committed {
		t.Fatalf("B CommitWork = %v, %v", committed, err)
	}
	st, err = Push(ctx, opts(p.b, p.remote))
	if err != nil || st.Pushed != 1 {
		t.Fatalf("B Push = %+v, %v; want Pushed 1", st, err)
	}
	st, err = Pull(ctx, opts(p.a, p.remote), 1)
	if err != nil || st.Pulled != 1 {
		t.Fatalf("A Pull = %+v, %v; want Pulled 1", st, err)
	}
	if got := readFile(t, filepath.Join(p.a, "wiki/beta.md")); got != "beta\n" {
		t.Fatalf("A wiki/beta.md = %q after the pull", got)
	}
	if a, b := git(t, p.a, "rev-parse", "HEAD"), git(t, p.b, "rev-parse", "HEAD"); a != b {
		t.Fatalf("A HEAD %s != B HEAD %s", a, b)
	}
	// A's per-PC files survived the pull.
	for _, rel := range ignoredFiles {
		if got, want := readFile(t, filepath.Join(p.a, rel)), "ignored "+rel+"\n"; got != want {
			t.Errorf("A %s = %q after the pull, want it untouched", rel, got)
		}
	}
}

// diverge makes both PCs of p commit a different change, A first pushing its
// own. It returns A's and B's commit ids.
func diverge(t *testing.T, p pair) (aTip, bTip string) {
	t.Helper()
	ctx := t.Context()
	put(t, p.a, "wiki/alpha.md", "alpha from A\n")
	if _, err := CommitWork(opts(p.a, p.remote), "lw 000002: A"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatal(err)
	}
	put(t, p.b, "wiki/beta.md", "beta from B\n")
	put(t, p.b, "raw/r1.md", "raw edited on B\n")
	if _, err := CommitWork(opts(p.b, p.remote), "lw 000002: B"); err != nil {
		t.Fatal(err)
	}
	return git(t, p.a, "rev-parse", "HEAD"), git(t, p.b, "rev-parse", "HEAD")
}

// TestDivergedRefusedNothingChanged: both PCs committed ⇒ Pull and Push
// refuse, with the counts, and change nothing.
func TestDivergedRefusedNothingChanged(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()
	aTip, bTip := diverge(t, p)

	beforeTree := workTree(t, p.b)
	st, err := Pull(ctx, opts(p.b, p.remote), 1)
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("Pull err = %v, want ErrDiverged", err)
	}
	if st.Ahead != 1 || st.Behind != 1 || !st.Diverged() || st.Pulled != 0 {
		t.Fatalf("Pull State = %+v, want Ahead 1, Behind 1, Pulled 0", st)
	}
	if got := git(t, p.b, "rev-parse", "HEAD"); got != bTip {
		t.Fatalf("B HEAD moved to %s from %s", got, bTip)
	}
	sameTree(t, workTree(t, p.b), beforeTree, "B work tree after the refused Pull")
	if got := git(t, p.b, "status", "--porcelain"); got != "" {
		t.Fatalf("B work tree is dirty after the refused Pull:\n%s", got)
	}

	st, err = Push(ctx, opts(p.b, p.remote))
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("Push err = %v, want ErrDiverged", err)
	}
	if st.Ahead != 1 || st.Behind != 1 || st.Pushed != 0 {
		t.Fatalf("Push State = %+v, want Ahead 1, Behind 1, Pushed 0", st)
	}
	if got := git(t, p.bare, "rev-parse", "main"); got != aTip {
		t.Fatalf("remote main = %s, want A's tip %s untouched", got, aTip)
	}
	if got := git(t, p.b, "rev-parse", "HEAD"); got != bTip {
		t.Fatalf("B HEAD moved to %s from %s", got, bTip)
	}
	sameTree(t, workTree(t, p.b), beforeTree, "B work tree after the refused Push")
}

// TestPushRaceBecomesDiverged: the remote moves between Push's fetch and its
// push. The fake ssh runs a second PC's push just before git-receive-pack
// starts, so git itself rejects the push; the caller sees ErrDiverged with
// the new counts, never a raw git error.
func TestPushRaceBecomesDiverged(t *testing.T) {
	hermetic(t)
	logPath := installFakeSSH(t)
	ctx := t.Context()

	a := makeVault(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	remote := "fake:" + bare
	if _, err := Init(ctx, opts(a, remote)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	b := filepath.Join(t.TempDir(), "pc-b")
	if err := Clone(ctx, opts(b, remote)); err != nil {
		t.Fatalf("Clone b: %v", err)
	}
	c := filepath.Join(t.TempDir(), "pc-c")
	if err := Clone(ctx, opts(c, bare)); err != nil {
		t.Fatalf("Clone c: %v", err)
	}
	put(t, c, "wiki/from-c.md", "c\n")
	if _, err := CommitWork(opts(c, bare), "lw 000002: C"); err != nil {
		t.Fatal(err)
	}
	cTip := git(t, c, "rev-parse", "HEAD")

	put(t, b, "wiki/from-b.md", "b\n")
	if _, err := CommitWork(opts(b, remote), "lw 000002: B"); err != nil {
		t.Fatal(err)
	}
	bTip := git(t, b, "rev-parse", "HEAD")

	hook := filepath.Join(t.TempDir(), "race.sh")
	script := "#!/bin/sh\nexec git -C '" + c + "' push -q '" + bare + "' HEAD:refs/heads/main\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SSH_BEFORE_RECEIVE", hook)

	st, err := Push(ctx, opts(b, remote))
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("Push err = %v, want ErrDiverged (log:\n%s)", err, readFile(t, logPath))
	}
	if st.Ahead != 1 || st.Behind != 1 || st.Pushed != 0 {
		t.Fatalf("Push State = %+v, want Ahead 1, Behind 1, Pushed 0", st)
	}
	if got := git(t, bare, "rev-parse", "main"); got != cTip {
		t.Fatalf("remote main = %s, want C's tip %s (B must not have clobbered it)", got, cTip)
	}
	if got := git(t, b, "rev-parse", "HEAD"); got != bTip {
		t.Fatalf("B HEAD moved to %s from %s", got, bTip)
	}
	if !strings.Contains(readFile(t, logPath), "git-receive-pack") {
		t.Fatalf("the race never reached git-receive-pack:\n%s", readFile(t, logPath))
	}
}

// TestFormatTooNewRefused: a remote tip whose .llmwiki/format is newer than
// this lw supports is refused before anything is merged.
func TestFormatTooNewRefused(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()

	put(t, p.a, ".llmwiki/format", "{\"version\": 2}\n")
	if _, err := CommitWork(opts(p.a, p.remote), "lw 000002: format 2"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatalf("A Push: %v", err)
	}
	bHead := git(t, p.b, "rev-parse", "HEAD")
	beforeTree := workTree(t, p.b)

	st, err := Pull(ctx, opts(p.b, p.remote), 1)
	var fe *FormatError
	if !errors.As(err, &fe) {
		t.Fatalf("Pull err = %v, want *FormatError", err)
	}
	want := "the remote vault is format 2; this lw supports 1 — upgrade lw on this PC, then lw sync"
	if err.Error() != want {
		t.Fatalf("err = %q\nwant  %q", err.Error(), want)
	}
	if fe.Have != 2 || fe.Max != 1 || fe.Remote != p.remote {
		t.Fatalf("FormatError = %+v, want Have 2, Max 1, Remote %q", *fe, p.remote)
	}
	if st.RemoteFormat != 2 || st.Pulled != 0 {
		t.Fatalf("State = %+v, want RemoteFormat 2, Pulled 0", st)
	}
	if got := git(t, p.b, "rev-parse", "HEAD"); got != bHead {
		t.Fatalf("B HEAD moved to %s from %s: something was merged", got, bHead)
	}
	sameTree(t, workTree(t, p.b), beforeTree, "B work tree after the refused Pull")

	// Status reports the format without failing.
	st, err = Status(ctx, opts(p.b, p.remote))
	if err != nil || st.RemoteFormat != 2 || st.Behind != 1 {
		t.Fatalf("Status = %+v, %v; want RemoteFormat 2, Behind 1", st, err)
	}
	// A lw that does support 2 pulls it.
	st, err = Pull(ctx, opts(p.b, p.remote), 2)
	if err != nil || st.Pulled != 1 {
		t.Fatalf("Pull with maxFormat 2 = %+v, %v; want Pulled 1", st, err)
	}
	if got := readFile(t, filepath.Join(p.b, ".llmwiki/format")); got != "{\"version\": 2}\n" {
		t.Fatalf("B .llmwiki/format = %q", got)
	}
}

// TestRemoteFallbackOrder: the first remote that answers wins; all dead is a
// *RemoteError that names every remote.
func TestRemoteFallbackOrder(t *testing.T) {
	hermetic(t)
	installFakeSSH(t)
	p := newPair(t)
	ctx := t.Context()
	deadSSH := "dead:/srv/nowhere"
	deadLocal := filepath.Join(t.TempDir(), "no-such-remote.git")

	t.Run("dead ssh then good", func(t *testing.T) {
		st, err := Status(ctx, opts(p.a, deadSSH, p.remote))
		if err != nil || st.Remote != p.remote {
			t.Fatalf("Status = %+v, %v; want Remote %q", st, err, p.remote)
		}
	})
	t.Run("dead local then dead ssh then good", func(t *testing.T) {
		st, err := Status(ctx, opts(p.a, deadLocal, deadSSH, p.remote))
		if err != nil || st.Remote != p.remote {
			t.Fatalf("Status = %+v, %v; want Remote %q", st, err, p.remote)
		}
	})
	t.Run("two good remotes: the first wins", func(t *testing.T) {
		second := bareRemote(t)
		git(t, p.a, "push", second, "HEAD:refs/heads/main")
		st, err := Status(ctx, opts(p.a, second, p.remote))
		if err != nil || st.Remote != second {
			t.Fatalf("Status = %+v, %v; want Remote %q", st, err, second)
		}
	})
	t.Run("pull and push also fall back", func(t *testing.T) {
		put(t, p.b, "wiki/fallback.md", "x\n")
		if _, err := CommitWork(opts(p.b, p.remote), "lw fallback"); err != nil {
			t.Fatal(err)
		}
		st, err := Push(ctx, opts(p.b, deadSSH, p.remote))
		if err != nil || st.Remote != p.remote || st.Pushed != 1 {
			t.Fatalf("Push = %+v, %v; want Remote %q, Pushed 1", st, err, p.remote)
		}
		st, err = Pull(ctx, opts(p.a, deadSSH, p.remote), 1)
		if err != nil || st.Remote != p.remote || st.Pulled != 1 {
			t.Fatalf("Pull = %+v, %v; want Remote %q, Pulled 1", st, err, p.remote)
		}
	})
	t.Run("all dead", func(t *testing.T) {
		st, err := Status(ctx, opts(p.a, deadSSH, deadLocal))
		var re *RemoteError
		if !errors.As(err, &re) {
			t.Fatalf("err = %v, want *RemoteError", err)
		}
		if !reflect.DeepEqual(re.Tried, []string{deadSSH, deadLocal}) || len(re.Errs) != 2 {
			t.Fatalf("RemoteError = %+v, want both remotes tried in order, two errors", re)
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, deadSSH+": ") || !strings.Contains(msg, "; "+deadLocal+": ") {
			t.Fatalf("message does not join \"remote: err\" with \"; \": %s", msg)
		}
		if !strings.Contains(msg, "Could not resolve hostname dead") {
			t.Fatalf("message lost the ssh error: %s", msg)
		}
		if st.Remote != "" {
			t.Fatalf("State.Remote = %q, want empty when nothing answered", st.Remote)
		}
	})
}

// TestTakeRemote: the escape hatch keeps the remote and saves this PC's
// commits on a backup branch.
func TestTakeRemote(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()
	aTip, bTip := diverge(t, p)

	name, st, err := TakeRemote(ctx, opts(p.b, p.remote))
	if err != nil {
		t.Fatalf("TakeRemote: %v", err)
	}
	if !regexp.MustCompile(`^lw-diverged-\d{8}-\d{6}$`).MatchString(name) {
		t.Fatalf("backup branch %q does not match lw-diverged-YYYYMMDD-HHMMSS", name)
	}
	if got := git(t, p.b, "rev-parse", "refs/heads/"+name); got != bTip {
		t.Fatalf("backup branch is at %s, want the old HEAD %s", got, bTip)
	}
	if got := git(t, p.b, "rev-parse", "HEAD"); got != aTip {
		t.Fatalf("HEAD = %s, want the remote tip %s", got, aTip)
	}
	if st.Remote != p.remote || st.Ahead != 0 || st.Behind != 0 {
		t.Fatalf("State = %+v, want Remote %q, Ahead 0, Behind 0", st, p.remote)
	}
	if got := git(t, p.b, "status", "--porcelain"); got != "" {
		t.Fatalf("B work tree is dirty after TakeRemote:\n%s", got)
	}
	// The work tree is the remote's, file for file.
	names := git(t, p.a, "ls-files")
	if got := git(t, p.b, "ls-files"); got != names {
		t.Fatalf("B tracks:\n%s\nwant A's:\n%s", got, names)
	}
	for _, rel := range strings.Split(names, "\n") {
		if a, b := readFile(t, filepath.Join(p.a, rel)), readFile(t, filepath.Join(p.b, rel)); a != b {
			t.Errorf("%s: B = %q, want A's %q", rel, b, a)
		}
	}
	if _, err := os.Stat(filepath.Join(p.b, "wiki/beta.md")); err == nil {
		t.Error("B's local-only wiki/beta.md survived the reset")
	}
	// Nothing is lost: B's work is on the backup branch.
	if got := git(t, p.b, "show", name+":wiki/beta.md"); got != "beta from B" {
		t.Errorf("backup branch wiki/beta.md = %q", got)
	}
	// The remote was not touched.
	if got := git(t, p.bare, "rev-parse", "main"); got != aTip {
		t.Errorf("remote main = %s, want %s", got, aTip)
	}
}

// TestNonInteractiveEnv: with Interactive false ssh runs in batch mode with a
// short connect timeout and git never prompts; with Interactive true neither
// is imposed.
func TestNonInteractiveEnv(t *testing.T) {
	hermetic(t)
	logPath := installFakeSSH(t)
	p := newPair(t)
	remote := "fake:" + p.bare

	o := opts(p.a, remote)
	if _, err := Status(t.Context(), o); err != nil {
		t.Fatalf("Status: %v", err)
	}
	log := readFile(t, logPath)
	for _, want := range []string{"[BatchMode=yes]", "[ConnectTimeout=5]", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(log, want) {
			t.Errorf("non-interactive: ssh log lacks %s:\n%s", want, log)
		}
	}

	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	o.Interactive = true
	var stderr strings.Builder
	o.Stderr = &stderr
	if _, err := Status(t.Context(), o); err != nil {
		t.Fatalf("interactive Status: %v", err)
	}
	log = readFile(t, logPath)
	if log == "" {
		t.Fatal("the interactive call never reached ssh")
	}
	for _, unwanted := range []string{"BatchMode", "ConnectTimeout"} {
		if strings.Contains(log, unwanted) {
			t.Errorf("interactive: ssh log has %s:\n%s", unwanted, log)
		}
	}
	if !strings.Contains(log, "GIT_TERMINAL_PROMPT=unset") {
		t.Errorf("interactive: git was told not to prompt:\n%s", log)
	}
}

// TestGitIdentityAndNoSign: a global git config that signs every commit and
// names nobody must not stop CommitWork, and the commit is authored by lw.
func TestGitIdentityAndNoSign(t *testing.T) {
	hermetic(t)
	writeGlobalGitConfig(t, "[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = /nonexistent/gpg\n[user]\n\tuseConfigOnly = true\n")
	t.Setenv("GIT_AUTHOR_NAME", "Someone Else")
	t.Setenv("GIT_COMMITTER_EMAIL", "else@example.com")

	vault := makeVault(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	if _, err := Init(t.Context(), opts(vault, remote)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	put(t, vault, "wiki/alpha.md", "changed\n")
	committed, err := CommitWork(opts(vault, remote), "lw 000002: edit")
	if err != nil || !committed {
		t.Fatalf("CommitWork = %v, %v; want true, nil", committed, err)
	}
	email := "lw@" + commitHost()
	want := "lw <" + email + ">|lw <" + email + ">|N"
	for _, rev := range []string{"HEAD", "HEAD~1"} {
		got := git(t, vault, "log", "-1", "--format=%an <%ae>|%cn <%ce>|%G?", rev)
		if got != want {
			t.Errorf("%s: %q, want %q", rev, got, want)
		}
	}
}

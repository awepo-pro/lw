package vaultsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tests here go beyond the frozen block: the parsing, the refusals and
// the failure modes a real server and a real git config would hit.

func TestParseRemote(t *testing.T) {
	home := hermetic(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		spec string
		want remote
	}{
		{"home:lw-vault", remote{kind: kindSSH, dest: "home", path: "lw-vault", arg: "home:lw-vault"}},
		{"home:~/lw-vault", remote{kind: kindSSH, dest: "home", path: "~/lw-vault", arg: "home:~/lw-vault"}},
		{"me@host:/srv/v.git", remote{kind: kindSSH, dest: "me@host", path: "/srv/v.git", arg: "me@host:/srv/v.git"}},
		{"[::1]:v", remote{kind: kindSSH, dest: "::1", path: "v", arg: "[::1]:v"}},
		{"ssh://host/srv/v.git", remote{kind: kindSSH, dest: "host", path: "/srv/v.git", arg: "ssh://host/srv/v.git"}},
		{"ssh://me@host:2222/~/v", remote{kind: kindSSH, dest: "me@host", port: "2222", path: "~/v", arg: "ssh://me@host:2222/~/v"}},
		{"/abs/path", remote{kind: kindLocal, path: "/abs/path", arg: "/abs/path"}},
		{"rel/path", remote{kind: kindLocal, path: "rel/path", arg: filepath.Join(cwd, "rel/path")}},
		{"./x:y", remote{kind: kindLocal, path: "./x:y", arg: filepath.Join(cwd, "x:y")}},
		{"/srv/a:b", remote{kind: kindLocal, path: "/srv/a:b", arg: "/srv/a:b"}},
		{"~/v.git", remote{kind: kindLocal, path: "~/v.git", arg: filepath.Join(home, "v.git")}},
		{"file:///srv/v.git", remote{kind: kindLocal, path: "/srv/v.git", arg: "file:///srv/v.git"}},
		{"https://example.com/v.git", remote{kind: kindOther, arg: "https://example.com/v.git"}},
	} {
		got, err := parseRemote(tc.spec)
		if err != nil {
			t.Errorf("parseRemote(%q): %v", tc.spec, err)
			continue
		}
		tc.want.raw = tc.spec
		if got != tc.want {
			t.Errorf("parseRemote(%q)\n got %+v\nwant %+v", tc.spec, got, tc.want)
		}
	}
	for _, spec := range []string{"", "-oProxyCommand=x:y", "-x", "ssh://-oProxyCommand=x/y", "ssh:///nohost"} {
		if _, err := parseRemote(spec); err == nil {
			t.Errorf("parseRemote(%q) accepted a hostile or empty spec", spec)
		}
	}
}

func TestSanitizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"vostro":          "vostro",
		"My Mac.local":    "My-Mac.local",
		"a<b>c@d":         "a-b-c-d",
		"":                "localhost",
		"  ":              "localhost",
		"host-1.lan_x":    "host-1.lan_x",
		"naïve":           "na-ve",
		"tab\there\nline": "tab-here-line",
	} {
		if got := sanitizeHost(in); got != want {
			t.Errorf("sanitizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemoteErrorText(t *testing.T) {
	e := &RemoteError{Tried: []string{"a:x", "/b"}, Errs: []error{errors.New("boom"), errors.New("bang")}}
	if got, want := e.Error(), "a:x: boom; /b: bang"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := (&RemoteError{Tried: []string{"a:x"}}).Error(); got != "a:x: failed" {
		t.Errorf("a missing cause = %q", got)
	}
	if got := (&RemoteError{}).Error(); got != "no remote answered" {
		t.Errorf("no remotes = %q", got)
	}
	cause := errors.New("cause")
	if !errors.Is(&RemoteError{Tried: []string{"a"}, Errs: []error{cause}}, cause) {
		t.Error("RemoteError does not unwrap to its causes")
	}
	if got := (State{Ahead: 1, Behind: 1}).Diverged(); !got {
		t.Error("1/1 is diverged")
	}
	for _, s := range []State{{Ahead: 1}, {Behind: 1}, {}} {
		if s.Diverged() {
			t.Errorf("%+v reported as diverged", s)
		}
	}
}

func TestNoGit(t *testing.T) {
	hermetic(t)
	vault := makeVault(t)
	t.Setenv("PATH", t.TempDir())
	ctx := t.Context()
	o := opts(vault, filepath.Join(t.TempDir(), "r.git"))

	_, ierr := Init(ctx, o)
	_, perr := Pull(ctx, o, 1)
	_, uerr := Push(ctx, o)
	_, serr := Status(ctx, o)
	_, _, terr := TakeRemote(ctx, o, 1)
	_, cerr := CommitWork(o, "x")
	for name, err := range map[string]error{"Init": ierr, "Clone": Clone(ctx, o, 1), "CommitWork": cerr,
		"Pull": perr, "Push": uerr, "Status": serr, "TakeRemote": terr} {
		if !errors.Is(err, ErrNoGit) {
			t.Errorf("%s err = %v, want ErrNoGit", name, err)
		}
	}
	if ErrNoGit.Error() != "git not found on PATH" {
		t.Errorf("ErrNoGit text = %q", ErrNoGit.Error())
	}
}

func TestErrNotRepo(t *testing.T) {
	hermetic(t)
	ctx := t.Context()
	want := "the vault is not under lw sync yet — run lw sync init <remote> or lw sync clone"
	if ErrNotRepo.Error() != want {
		t.Fatalf("ErrNotRepo text = %q", ErrNotRepo.Error())
	}
	remote := bareRemote(t)

	plain := makeVault(t)
	// A vault that merely sits inside another repository is not under lw
	// sync, and must never stage the other repository's files.
	outer := t.TempDir()
	git(t, outer, "init", "-b", "main", ".")
	nested := filepath.Join(outer, "vault")
	put(t, nested, "SCHEMA.md", "s\n")
	// A git init with no commit yet.
	unborn := t.TempDir()
	git(t, unborn, "init", "-b", "main", ".")
	put(t, unborn, "SCHEMA.md", "s\n")

	for name, dir := range map[string]string{"plain dir": plain, "nested in another repo": nested, "no commit yet": unborn} {
		o := opts(dir, remote)
		_, perr := Pull(ctx, o, 1)
		_, uerr := Push(ctx, o)
		_, serr := Status(ctx, o)
		_, _, terr := TakeRemote(ctx, o, 1)
		for op, err := range map[string]error{"Pull": perr, "Push": uerr, "Status": serr, "TakeRemote": terr} {
			if !errors.Is(err, ErrNotRepo) {
				t.Errorf("%s: %s err = %v, want ErrNotRepo", name, op, err)
			}
		}
	}
	if _, err := CommitWork(opts(plain, remote), "x"); !errors.Is(err, ErrNotRepo) {
		t.Errorf("CommitWork on a plain dir = %v, want ErrNotRepo", err)
	}
	if _, err := CommitWork(opts(nested, remote), "x"); !errors.Is(err, ErrNotRepo) {
		t.Errorf("CommitWork nested in another repo = %v, want ErrNotRepo", err)
	}
	if got := git(t, outer, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("the outer repository was touched:\n%s", got)
	}

	// Init in a nested vault makes its own repository, leaving the outer one alone.
	if _, err := Init(ctx, opts(nested, remote)); err != nil {
		t.Fatalf("Init nested: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, ".git")); err != nil {
		t.Errorf("Init did not create the vault's own repository: %v", err)
	}
	if got := git(t, outer, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("Init staged files in the outer repository:\n%s", got)
	}
	// A repo with no commit yet is accepted by Init and becomes a vault.
	if _, err := Init(ctx, opts(unborn, bareRemote(t))); err != nil {
		t.Errorf("Init on a git init'd vault with no commit: %v", err)
	}
}

func TestNoRemotesAndNoDir(t *testing.T) {
	hermetic(t)
	ctx := t.Context()
	p := newPair(t)
	for name, err := range map[string]error{
		"Status": func() error { _, err := Status(ctx, opts(p.a)); return err }(),
		"Pull":   func() error { _, err := Pull(ctx, opts(p.a), 1); return err }(),
		"Push":   func() error { _, err := Push(ctx, opts(p.a)); return err }(),
		"Init":   func() error { _, err := Init(ctx, opts(makeVault(t))); return err }(),
		"Clone":  Clone(ctx, opts(filepath.Join(t.TempDir(), "c")), 1),
	} {
		var re *RemoteError
		if err == nil || errors.As(err, &re) {
			t.Errorf("%s with no remotes: err = %v, want a plain error", name, err)
		}
	}
	if _, err := Status(ctx, Options{Remotes: []string{p.remote}}); err == nil {
		t.Error("an empty Options.Dir was accepted")
	}
	if _, err := Init(ctx, opts(filepath.Join(t.TempDir(), "missing"), bareRemote(t))); err == nil {
		t.Error("Init on a missing vault dir succeeded")
	}
}

func TestTimeoutKillsHungSSH(t *testing.T) {
	hermetic(t)
	installFakeSSH(t)
	p := newPair(t)
	t.Setenv("FAKE_SSH_SLEEP", "60")

	o := opts(p.a, "fake:"+p.bare)
	o.Timeout = time.Second
	start := time.Now()
	_, err := Status(t.Context(), o)
	elapsed := time.Since(start)
	var re *RemoteError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("err = %v, want a *RemoteError saying timed out after 1s", err)
	}
	// The whole process group is killed, not just git: a sleep that kept
	// the pipes open would hold Wait for the 2 s WaitDelay on top, 3 s in all.
	if elapsed > 2400*time.Millisecond {
		t.Fatalf("took %s; the hung ssh child outlived the kill", elapsed)
	}
}

// An ssh ControlPersist master (or a ProxyCommand) can outlive git while
// holding git's stderr pipe. Wait then gives up after the wait delay with
// exec.ErrWaitDelay even though git exited 0; that must still be a success.
func TestLingeringGrandchildIsNotAFailure(t *testing.T) {
	hermetic(t)
	installFakeSSH(t)
	p := newPair(t)
	t.Setenv("FAKE_SSH_BG", "6")

	start := time.Now()
	st, err := Status(t.Context(), opts(p.a, "fake:"+p.bare))
	elapsed := time.Since(start)
	if err != nil || st.Remote != "fake:"+p.bare {
		t.Fatalf("Status = %+v, %v; want success", st, err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took %s: the call waited for the grandchild instead of giving up on its pipes", elapsed)
	}
}

func TestTidy(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"fatal: couldn't find remote ref refs/heads/main\n", "couldn't find remote ref refs/heads/main"},
		{"ssh: connect to host home port 22: Connection timed out\r\nfatal: Could not read from remote repository.\n\nPlease make sure you have the correct access rights\nand the repository exists.\n",
			"ssh: connect to host home port 22: Connection timed out"},
		{"Enumerating objects: 5, done.\nCounting objects:  20% (1/5)\rCounting objects: 100% (5/5), done.\nWriting objects: 100% (3/3), 220 bytes | 220.00 KiB/s, done.\nremote: Total 3 (delta 1), reused 0 (delta 0)\n ! [remote rejected] HEAD -> main (pre-receive hook declined)\nerror: failed to push some refs\n",
			"! [remote rejected] HEAD -> main (pre-receive hook declined) error: failed to push some refs"},
	} {
		if got := tidy(tc.in); got != tc.want {
			t.Errorf("tidy(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestCancelledContextIsNotARemoteFailure(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, err := range map[string]error{
		"Status": func() error { _, err := Status(ctx, opts(p.a, p.remote)); return err }(),
		"Pull":   func() error { _, err := Pull(ctx, opts(p.a, p.remote), 1); return err }(),
		"Push":   func() error { _, err := Push(ctx, opts(p.a, p.remote)); return err }(),
		"Clone":  Clone(ctx, opts(filepath.Join(t.TempDir(), "c"), p.remote), 1),
	} {
		var re *RemoteError
		if !errors.Is(err, context.Canceled) || errors.As(err, &re) {
			t.Errorf("%s err = %v, want context.Canceled", name, err)
		}
	}
}

func TestEmptyRemoteAnsweredEmpty(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()
	empty := bareRemote(t)

	st, err := Status(ctx, opts(p.a, empty))
	if err != nil {
		t.Fatalf("Status against an empty remote: %v", err)
	}
	if st.Remote != empty || st.Ahead != 1 || st.Behind != 0 || st.RemoteFormat != 0 {
		t.Fatalf("State = %+v, want Remote %q, Ahead 1 (all of HEAD), Behind 0, RemoteFormat 0", st, empty)
	}
	if _, err := gitErr(p.a, "rev-parse", "--verify", "--quiet", "refs/remotes/lw/main"); err == nil {
		t.Error("the stale tracking ref survived an empty remote answer")
	}
	st, err = Pull(ctx, opts(p.a, empty), 1)
	if err != nil || st.Pulled != 0 || st.Ahead != 1 {
		t.Fatalf("Pull from an empty remote = %+v, %v; want a no-op", st, err)
	}
	if _, _, err := TakeRemote(ctx, opts(p.a, empty), 1); err == nil || !strings.Contains(err.Error(), "no commit to take") {
		t.Fatalf("TakeRemote on an empty remote = %v, want a refusal", err)
	}
	if got := git(t, p.a, "branch", "--list", "lw-diverged-*"); got != "" {
		t.Fatalf("a refused TakeRemote left a backup branch: %s", got)
	}
	st, err = Push(ctx, opts(p.a, empty))
	if err != nil || st.Pushed != 1 {
		t.Fatalf("Push to an empty remote = %+v, %v; want Pushed 1", st, err)
	}
	if got, want := git(t, empty, "rev-parse", "main"), git(t, p.a, "rev-parse", "HEAD"); got != want {
		t.Fatalf("remote main = %s, want %s", got, want)
	}
	if got, want := git(t, p.a, "rev-parse", "refs/remotes/lw/main"), git(t, p.a, "rev-parse", "HEAD"); got != want {
		t.Fatalf("tracking ref = %s after a push, want HEAD %s", got, want)
	}
}

func TestNothingToDo(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()

	st, err := Push(ctx, opts(p.a, p.remote))
	if err != nil || st.Pushed != 0 || st.Ahead != 0 || st.Behind != 0 || st.Remote != p.remote || st.RemoteFormat != 1 {
		t.Fatalf("idle Push = %+v, %v", st, err)
	}
	// Ahead only: Pull changes nothing.
	put(t, p.a, "wiki/alpha.md", "local\n")
	if _, err := CommitWork(opts(p.a, p.remote), "lw 1: x"); err != nil {
		t.Fatal(err)
	}
	st, err = Pull(ctx, opts(p.a, p.remote), 1)
	if err != nil || st.Pulled != 0 || st.Ahead != 1 || st.Behind != 0 {
		t.Fatalf("Pull while ahead = %+v, %v; want no-op with Ahead 1", st, err)
	}
	// Behind only: Push changes nothing and says so.
	if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatal(err)
	}
	st, err = Push(ctx, opts(p.b, p.remote))
	if err != nil || st.Pushed != 0 || st.Behind != 1 {
		t.Fatalf("Push while behind = %+v, %v; want Pushed 0, Behind 1", st, err)
	}
}

func TestPullRefusesUncommittedChanges(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	put(t, p.b, "wiki/alpha.md", "edited, not committed\n")
	_, err := Pull(t.Context(), opts(p.b, p.remote), 1)
	if err == nil || !strings.Contains(err.Error(), "git -C "+p.b+" status") {
		t.Fatalf("err = %v, want one that names git status", err)
	}
	if got := readFile(t, filepath.Join(p.b, "wiki/alpha.md")); got != "edited, not committed\n" {
		t.Fatalf("the edit was lost: %q", got)
	}
}

func TestCommitWorkIgnoreFile(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	o := opts(p.a, p.remote)

	// Per-PC files change all the time and never make a commit.
	put(t, p.a, ".llmwiki/index.gob", "rebuilt\n")
	put(t, p.a, ".llmwiki/changesets/open/cs2/changeset.json", "{}\n")
	put(t, p.a, ".llmwiki/sync.json", "{}\n")
	if committed, err := CommitWork(o, "lw nothing"); err != nil || committed {
		t.Fatalf("CommitWork over ignored files = %v, %v; want false, nil", committed, err)
	}
	// A hand edit that reverts to the managed bytes leaves nothing to commit.
	put(t, p.a, ".gitignore", "*.tmp\n")
	if committed, err := CommitWork(o, "lw nothing"); err != nil || committed {
		t.Fatalf("CommitWork after a reverted .gitignore = %v, %v; want false, nil", committed, err)
	}
	if got := readFile(t, filepath.Join(p.a, ".gitignore")); got != Ignore {
		t.Fatalf(".gitignore = %q, want Ignore", got)
	}
	// A committed .gitignore that differs (another lw version wrote it) is
	// rewritten to Ignore and the rewrite is committed. An empty message
	// falls back to "lw sync".
	put(t, p.a, ".gitignore", "/.llmwiki/index.gob\n")
	git(t, p.a, "commit", "-qam", "older lw")
	committed, err := CommitWork(o, "")
	if err != nil || !committed {
		t.Fatalf("CommitWork over an older .gitignore = %v, %v; want true, nil", committed, err)
	}
	if got := readFile(t, filepath.Join(p.a, ".gitignore")); got != Ignore {
		t.Fatalf(".gitignore = %q, want Ignore", got)
	}
	if got := git(t, p.a, "log", "-1", "--format=%s"); got != "lw sync" {
		t.Fatalf("an empty message was committed as %q, want the default \"lw sync\"", got)
	}
	// A new synced file, a deleted one and an untracked directory all land.
	put(t, p.a, "notes/new.md", "n\n")
	put(t, p.a, "wiki/deep/er/page.md", "p\n")
	if err := os.Remove(filepath.Join(p.a, "log.md")); err != nil {
		t.Fatal(err)
	}
	if committed, err := CommitWork(o, "lw notes"); err != nil || !committed {
		t.Fatalf("CommitWork = %v, %v", committed, err)
	}
	names := git(t, p.a, "ls-files")
	for _, want := range []string{"notes/new.md", "wiki/deep/er/page.md"} {
		if !strings.Contains(names, want) {
			t.Errorf("%s not tracked:\n%s", want, names)
		}
	}
	if strings.Contains(names, "\nlog.md") || strings.HasPrefix(names, "log.md") {
		t.Errorf("log.md still tracked after its deletion:\n%s", names)
	}
	if got := git(t, p.a, "status", "--porcelain"); got != "" {
		t.Errorf("dirty after CommitWork:\n%s", got)
	}
}

// A global core.autocrlf=input (common on a Mac) would normalise CRLF in an
// immutable raw source on `git add`; the other PC would then hold bytes whose
// sha no longer matches the raw's frontmatter.
func TestRawBytesSurviveAutocrlf(t *testing.T) {
	hermetic(t)
	writeGlobalGitConfig(t, "[core]\n\tautocrlf = input\n\teol = lf\n")
	ctx := t.Context()
	vault := makeVault(t)
	put(t, vault, "raw/crlf.md", "line one\r\nline two\r\n")
	put(t, vault, "raw/bin.dat", "\x00\x01\r\n\x02")
	remote := filepath.Join(t.TempDir(), "r.git")
	if _, err := Init(ctx, opts(vault, remote)); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "pc-b")
	if err := Clone(ctx, opts(clone, remote), 1); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"raw/crlf.md": "line one\r\nline two\r\n", "raw/bin.dat": "\x00\x01\r\n\x02"} {
		if got := readFile(t, filepath.Join(clone, rel)); got != want {
			t.Errorf("%s on the second PC = %q, want %q", rel, got, want)
		}
		if blob, err := gitErr(remote, "cat-file", "blob", "main:"+rel); err != nil || blob != strings.TrimSpace(want) {
			t.Errorf("%s on the remote = %q, %v; want the original bytes", rel, blob, err)
		}
	}
}

// A user's global hooks (a lint pre-commit, a husky post-commit) are not run
// for a vault commit.
func TestGlobalHooksDoNotRun(t *testing.T) {
	home := hermetic(t)
	hooks := filepath.Join(home, "hooks")
	marker := filepath.Join(home, "hook-ran")
	put(t, hooks, "pre-commit", "#!/bin/sh\necho pre >> '"+marker+"'\nexit 1\n")
	put(t, hooks, "post-commit", "#!/bin/sh\necho post >> '"+marker+"'\n")
	put(t, hooks, "pre-push", "#!/bin/sh\necho push >> '"+marker+"'\nexit 1\n")
	for _, h := range []string{"pre-commit", "post-commit", "pre-push"} {
		if err := os.Chmod(filepath.Join(hooks, h), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeGlobalGitConfig(t, "[core]\n\thooksPath = "+hooks+"\n")

	vault := makeVault(t)
	if _, err := Init(t.Context(), opts(vault, filepath.Join(t.TempDir(), "r.git"))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	put(t, vault, "wiki/alpha.md", "x\n")
	if committed, err := CommitWork(opts(vault), "lw x"); err != nil || !committed {
		t.Fatalf("CommitWork = %v, %v", committed, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("a global hook ran: %s", readFile(t, marker))
	}
}

// A `make check` run from a git hook inherits GIT_DIR and GIT_INDEX_FILE; a
// shell may export GIT_AUTHOR_NAME. None of it may redirect or rename lw.
func TestAmbientGitEnvIsIgnored(t *testing.T) {
	hermetic(t)
	elsewhere := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(elsewhere, "nonexistent.git"))
	t.Setenv("GIT_WORK_TREE", elsewhere)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(elsewhere, "index"))
	t.Setenv("GIT_AUTHOR_NAME", "Someone Else")

	vault := makeVault(t)
	_, ierr := Init(t.Context(), opts(vault, filepath.Join(t.TempDir(), "r.git")))
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_AUTHOR_NAME"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if ierr != nil {
		t.Fatalf("Init with ambient GIT_* set: %v", ierr)
	}
	if _, err := os.Stat(filepath.Join(vault, ".git")); err != nil {
		t.Fatalf("the vault has no .git of its own: %v", err)
	}
	if got := git(t, vault, "log", "-1", "--format=%an"); got != "lw" {
		t.Fatalf("author = %q, want lw", got)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE redirected git into %s", elsewhere)
	}
}

func TestTakeRemoteBackupNameAndUncommittedWork(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()
	diverge(t, p)
	at := time.Date(2026, time.October, 9, 15, 30, 45, 0, time.Local)

	// B also has an edit it never committed: the backup must hold it.
	put(t, p.b, "notes/unsaved.md", "unsaved\n")
	name, _, err := takeRemoteAt(ctx, opts(p.b, p.remote), 1, at)
	if err != nil {
		t.Fatal(err)
	}
	if name != "lw-diverged-20261009-153045" {
		t.Fatalf("name = %q, want lw-diverged-20261009-153045 (local time)", name)
	}
	if got := git(t, p.b, "show", name+":notes/unsaved.md"); got != "unsaved" {
		t.Fatalf("the uncommitted file is not on the backup branch: %q", got)
	}
	if _, err := os.Stat(filepath.Join(p.b, "notes/unsaved.md")); err == nil {
		t.Fatal("the uncommitted file survived a reset to the remote")
	}

	// A second take within the same second must not clobber the first.
	first := git(t, p.b, "rev-parse", name)
	put(t, p.b, "wiki/again.md", "again\n")
	if _, err := CommitWork(opts(p.b, p.remote), "lw again"); err != nil {
		t.Fatal(err)
	}
	name2, st, err := takeRemoteAt(ctx, opts(p.b, p.remote), 1, at)
	if err != nil {
		t.Fatal(err)
	}
	if name2 != "lw-diverged-20261009-153045-2" {
		t.Fatalf("second name = %q, want the -2 suffix", name2)
	}
	if got := git(t, p.b, "rev-parse", name); got != first {
		t.Fatalf("the first backup moved: %s -> %s", first, got)
	}
	if st.Ahead != 0 || st.Behind != 0 || st.Pulled != 0 {
		t.Fatalf("State = %+v, want all zero (only local commits were discarded)", st)
	}
	// And the real clock names it the same way.
	put(t, p.b, "wiki/third.md", "3\n")
	name3, _, err := TakeRemote(ctx, opts(p.b, p.remote), 1)
	if err != nil || !strings.HasPrefix(name3, "lw-diverged-") {
		t.Fatalf("TakeRemote = %q, %v", name3, err)
	}
}

func TestCloneRules(t *testing.T) {
	hermetic(t)
	installFakeSSH(t)
	p := newPair(t)
	ctx := t.Context()

	t.Run("refuses a non-empty directory", func(t *testing.T) {
		dir := t.TempDir()
		put(t, dir, "mine.txt", "keep\n")
		err := Clone(ctx, opts(dir, p.remote), 1)
		want := dir + " is not empty — lw sync clone needs a new or empty directory"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
		if got := readFile(t, filepath.Join(dir, "mine.txt")); got != "keep\n" {
			t.Fatalf("the directory was modified: %q", got)
		}
	})
	t.Run("refuses a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "f")
		put(t, filepath.Dir(file), "f", "x\n")
		if err := Clone(ctx, opts(file, p.remote), 1); err == nil {
			t.Fatal("cloned onto a file")
		}
	})
	t.Run("accepts an existing empty directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := Clone(ctx, opts(dir, p.remote), 1); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(dir, "SCHEMA.md")); got != "synced SCHEMA.md\n" {
			t.Fatalf("SCHEMA.md = %q", got)
		}
		if _, err := gitErr(dir, "config", "--get", "branch.main.remote"); err == nil {
			t.Error("the clone kept a branch.main.remote; git pull would bypass the divergence check")
		}
	})
	t.Run("falls back and leaves nothing behind", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "deep", "pc")
		if err := Clone(ctx, opts(dir, "dead:/srv/none", p.remote), 1); err != nil {
			t.Fatal(err)
		}
		if got := git(t, dir, "rev-parse", "HEAD"); got != git(t, p.a, "rev-parse", "HEAD") {
			t.Fatalf("HEAD = %s", got)
		}
	})
	t.Run("an empty remote cannot be cloned, and the directory is cleaned", func(t *testing.T) {
		fresh := filepath.Join(t.TempDir(), "pc")
		err := Clone(ctx, opts(fresh, bareRemote(t)), 1)
		var re *RemoteError
		if !errors.As(err, &re) {
			t.Fatalf("err = %v, want *RemoteError", err)
		}
		if _, serr := os.Stat(fresh); serr == nil {
			t.Error("a failed Clone left the directory it created")
		}
		existing := t.TempDir()
		if err := Clone(ctx, opts(existing, bareRemote(t)), 1); err == nil {
			t.Fatal("cloned an empty remote")
		}
		if entries, _ := os.ReadDir(existing); len(entries) != 0 {
			t.Errorf("a failed Clone left %d entries in the empty dir it was given", len(entries))
		}
	})
	t.Run("all dead", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "pc")
		err := Clone(ctx, opts(dir, "dead:/a", "dead:/b"), 1)
		var re *RemoteError
		if !errors.As(err, &re) || len(re.Tried) != 2 {
			t.Fatalf("err = %v, want a *RemoteError naming both", err)
		}
	})
}

func TestRemoteFormatReading(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()

	for _, tc := range []struct {
		body    string
		want    int
		wantErr string
	}{
		{"{\"version\": 3, \"features\": [\"x\"]}\n", 3, ""},
		{"{\"version\": 1}", 1, ""},
		{"{\"version\": 0}\n", 0, ".llmwiki/format"},
		{"{\"features\": []}\n", 0, ".llmwiki/format"},
		{"nonsense\n", 0, ".llmwiki/format"},
		{"{\"version\": 2.5}\n", 0, ".llmwiki/format"},
		{"{\"version\": 2}\n", 2, ""},
	} {
		// A raw git push: the library itself refuses to work on top of an
		// unreadable format, which is the point.
		put(t, p.a, ".llmwiki/format", tc.body)
		git(t, p.a, "add", "-A")
		git(t, p.a, "commit", "-qm", "format")
		git(t, p.a, "push", "-q", p.bare, "HEAD:refs/heads/main")
		st, err := Status(ctx, opts(p.b, p.remote))
		switch {
		case tc.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%q: Status err = %v, want one naming %s", tc.body, err, tc.wantErr)
			}
			// Nothing is merged on top of a format this lw cannot read.
			if _, err := Pull(ctx, opts(p.b, p.remote), 99); err == nil {
				t.Errorf("%q: Pull went ahead", tc.body)
			}
			if _, err := Push(ctx, opts(p.a, p.remote)); err == nil {
				t.Errorf("%q: Push went ahead", tc.body)
			}
		case err != nil || st.RemoteFormat != tc.want:
			t.Errorf("%q: Status = %+v, %v; want RemoteFormat %d", tc.body, st, err, tc.want)
		}
	}
}

func TestInitAcceptsEmptyBareRepoAndRepointsHEAD(t *testing.T) {
	hermetic(t)
	bare := filepath.Join(t.TempDir(), "r.git")
	git(t, filepath.Dir(bare), "init", "--bare", "-b", "master", bare)
	if _, err := Init(t.Context(), opts(makeVault(t), bare)); err != nil {
		t.Fatalf("Init into an empty bare repo: %v", err)
	}
	if got := git(t, bare, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("remote HEAD = %q; a plain git clone would warn", got)
	}
	if got := git(t, bare, "branch", "--list"); !strings.Contains(got, "main") {
		t.Fatalf("remote branches: %q", got)
	}
}

// A failed push leaves the vault with a repository and a commit but no
// tracking ref, and the next Init picks up where it stopped.
func TestInitRetriesAfterAFailedPush(t *testing.T) {
	hermetic(t)
	bare := filepath.Join(t.TempDir(), "r.git")
	git(t, filepath.Dir(bare), "init", "--bare", "-b", "main", bare)
	hook := filepath.Join(bare, "hooks", "pre-receive")
	put(t, bare, "hooks/pre-receive", "#!/bin/sh\necho 'rejected by policy' >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	vault := makeVault(t)

	_, err := Init(t.Context(), opts(vault, bare))
	var re *RemoteError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "rejected by policy") {
		t.Fatalf("err = %v, want a *RemoteError carrying the server's reason", err)
	}
	if _, err := gitErr(vault, "rev-parse", "--verify", "--quiet", "refs/remotes/lw/main"); err == nil {
		t.Fatal("a failed push set the tracking ref")
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	st, err := Init(t.Context(), opts(vault, bare))
	if err != nil || st.Pushed != 1 {
		t.Fatalf("retry = %+v, %v; want Pushed 1", st, err)
	}
	if got, want := git(t, bare, "rev-parse", "main"), git(t, vault, "rev-parse", "HEAD"); got != want {
		t.Fatalf("remote main = %s, want %s", got, want)
	}
}

func TestRemoteForms(t *testing.T) {
	hermetic(t)
	logPath := installFakeSSH(t)
	ctx := t.Context()

	t.Run("file URL", func(t *testing.T) {
		bare := filepath.Join(t.TempDir(), "r.git")
		vault := makeVault(t)
		spec := "file://" + bare
		if st, err := Init(ctx, opts(vault, spec)); err != nil || st.Pushed != 1 || st.Remote != spec {
			t.Fatalf("Init = %+v, %v", st, err)
		}
		if st, err := Status(ctx, opts(vault, spec)); err != nil || st.Remote != spec {
			t.Fatalf("Status = %+v, %v", st, err)
		}
		clone := filepath.Join(t.TempDir(), "c")
		if err := Clone(ctx, opts(clone, spec), 1); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ssh URL with a port", func(t *testing.T) {
		if err := os.WriteFile(logPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		bare := filepath.Join(t.TempDir(), "r.git")
		spec := "ssh://me@fake:2222" + bare
		vault := makeVault(t)
		if st, err := Init(ctx, opts(vault, spec)); err != nil || st.Pushed != 1 {
			t.Fatalf("Init = %+v, %v", st, err)
		}
		log := readFile(t, logPath)
		first := strings.SplitN(log, "\n", 2)[0]
		for _, want := range []string{"[-p] [2222]", "[me@fake]", "[--]"} {
			if !strings.Contains(first, want) {
				t.Errorf("the init ssh call lacks %s: %s", want, first)
			}
		}
		if got := treeNames(t, bare, "main"); len(got) == 0 {
			t.Error("nothing reached the remote")
		}
		clone := filepath.Join(t.TempDir(), "c")
		if err := Clone(ctx, opts(clone, spec), 1); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tilde path over ssh", func(t *testing.T) {
		// home:~/lw-vault, the real deployment: the server's shell expands
		// the ~ in Init's command, and git-receive-pack expands it itself.
		vault := makeVault(t)
		home := os.Getenv("HOME")
		spec := "fake:~/lw-vault-tilde"
		if st, err := Init(ctx, opts(vault, spec)); err != nil || st.Pushed != 1 {
			t.Fatalf("Init = %+v, %v", st, err)
		}
		if got := treeNames(t, filepath.Join(home, "lw-vault-tilde"), "main"); len(got) == 0 {
			t.Fatal("nothing reached $HOME/lw-vault-tilde")
		}
		if _, err := os.Stat("~"); err == nil {
			t.Fatal("a literal ~ directory was created")
		}
		if st, err := Status(ctx, opts(vault, spec)); err != nil || st.Behind != 0 {
			t.Fatalf("Status = %+v, %v", st, err)
		}
	})
	t.Run("an https remote cannot be initialised", func(t *testing.T) {
		vault := makeVault(t)
		_, err := Init(ctx, opts(vault, "https://example.com/v.git"))
		want := "remote https://example.com/v.git: lw sync init needs an ssh or local path remote"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})
	t.Run("interactive init has no batch options and streams stderr", func(t *testing.T) {
		if err := os.WriteFile(logPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		o := opts(makeVault(t), "fake:"+filepath.Join(t.TempDir(), "r.git"))
		o.Interactive = true
		var stderr strings.Builder
		o.Stderr = &stderr
		if _, err := Init(ctx, o); err != nil {
			t.Fatal(err)
		}
		// A prompt may need the user, so no BatchMode and no short connect limit;
		// the mux's own bound (ConnectTimeout=10) is a different thing (A-042-9 e).
		if log := readFile(t, logPath); strings.Contains(log, "BatchMode") || strings.Contains(log, "ConnectTimeout=5") {
			t.Errorf("interactive init imposed batch options:\n%s", log)
		}
		if !strings.Contains(stderr.String(), "->") {
			t.Errorf("git's push progress never reached Options.Stderr: %q", stderr.String())
		}
	})
}

package vaultsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests here come from the fresh review of S1 (042 S1c): what a user's
// own git and ssh configuration, a hostile path, an interrupted sync or an odd
// vault can do to a sync, each pinned so the fix cannot be lost.

// wantInfoAttributes is spelled out here, not shared with the code under test.
// uniq is a sleep length no other process (an earlier crashed run included)
// shares, so waitGone can tell this test's child from a stranger.
func uniq(k int) string { return strconv.Itoa(200000 + os.Getpid()%100000 + k) }

const wantInfoAttributes = "* -text -eol -filter -ident -working-tree-encoding\n"

// H1: a user's git configuration must neither drop a vault file nor rewrite
// its bytes. This user has ~/.config/git/ignore; a global attributesFile or a
// vault .gitattributes with `* text=auto` rewrites CRLF in an immutable raw.
func TestUserGitConfigCannotChangeVaultBytes(t *testing.T) {
	const crlf = "line one\r\nline two\r\n"
	const pdf = "%PDF-1.4\r\n\x00\x01\xff\r\nbinary \r\n%%EOF\r\n"
	const logBody = "log line\r\n"
	const lf = "plain\nlf\n"

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, home, vault string)
	}{
		{"global core.excludesFile", func(t *testing.T, home, vault string) {
			put(t, home, "global-ignore", "*.pdf\n*.log\nraw/\n")
			writeGlobalGitConfig(t, "[core]\n\texcludesFile = "+filepath.Join(home, "global-ignore")+"\n")
		}},
		{"XDG git/ignore", func(t *testing.T, home, vault string) {
			put(t, home, ".config/git/ignore", "*.pdf\n*.log\n")
		}},
		{"global core.attributesFile", func(t *testing.T, home, vault string) {
			put(t, home, "global-attrs", "* text=auto eol=lf\n")
			writeGlobalGitConfig(t, "[core]\n\tattributesFile = "+filepath.Join(home, "global-attrs")+"\n")
		}},
		{"in-vault .gitattributes", func(t *testing.T, home, vault string) {
			put(t, vault, ".gitattributes", "* text=auto eol=lf\n*.pdf ident\n")
		}},
		{"in-vault .gitattributes eol=crlf", func(t *testing.T, home, vault string) {
			put(t, vault, ".gitattributes", "* text eol=crlf\n")
		}},
		{"init.templateDir with info/exclude", func(t *testing.T, home, vault string) {
			put(t, home, "tpl/info/exclude", "*.pdf\n*.log\n")
			writeGlobalGitConfig(t, "[init]\n\ttemplateDir = "+filepath.Join(home, "tpl")+"\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := hermetic(t)
			ctx := t.Context()
			vault := makeVault(t)
			files := map[string]string{"raw/crlf.md": crlf, "raw/book.pdf": pdf, "raw/data.log": logBody, "raw/lf.md": lf}
			for rel, body := range files {
				put(t, vault, rel, body)
			}
			tc.setup(t, home, vault)
			remote := filepath.Join(t.TempDir(), "remote.git")
			if _, err := Init(ctx, opts(vault, remote)); err != nil {
				t.Fatalf("Init: %v", err)
			}
			check := func(label, workDir, bare string, want map[string]string) {
				t.Helper()
				for rel, body := range want {
					if got := readFile(t, filepath.Join(workDir, rel)); got != body {
						t.Errorf("%s: work tree %s = %q, want %q", label, rel, got, body)
					}
					if got := gitRaw(t, bare, "cat-file", "blob", "main:"+rel); got != body {
						t.Errorf("%s: remote %s = %q, want %q", label, rel, got, body)
					}
				}
			}
			check("after Init", vault, remote, files)

			// The second PC checks the same bytes out.
			clone := filepath.Join(t.TempDir(), "pc-b")
			if err := Clone(ctx, opts(clone, remote), 1); err != nil {
				t.Fatalf("Clone: %v", err)
			}
			check("after Clone", clone, remote, files)

			// A later edit and a brand-new file travel the same way, in both directions.
			files["raw/crlf.md"] = "changed\r\nagain\r\n"
			files["raw/new.pdf"] = pdf + "2"
			for rel, body := range files {
				put(t, vault, rel, body)
			}
			if committed, err := CommitWork(opts(vault, remote), "lw edit"); err != nil || !committed {
				t.Fatalf("CommitWork = %v, %v", committed, err)
			}
			if _, err := Push(ctx, opts(vault, remote)); err != nil {
				t.Fatalf("Push: %v", err)
			}
			if st, err := Pull(ctx, opts(clone, remote), 1); err != nil || st.Pulled != 1 {
				t.Fatalf("Pull = %+v, %v", st, err)
			}
			check("after Push and Pull", clone, remote, files)

			put(t, clone, "raw/from-b.pdf", pdf+"3")
			put(t, clone, "raw/from-b.log", logBody+"3")
			if committed, err := CommitWork(opts(clone, remote), "lw b"); err != nil || !committed {
				t.Fatalf("B CommitWork = %v, %v", committed, err)
			}
			if _, err := Push(ctx, opts(clone, remote)); err != nil {
				t.Fatalf("B Push: %v", err)
			}
			for rel, body := range map[string]string{"raw/from-b.pdf": pdf + "3", "raw/from-b.log": logBody + "3"} {
				if got := gitRaw(t, remote, "cat-file", "blob", "main:"+rel); got != body {
					t.Errorf("remote %s = %q, want %q", rel, got, body)
				}
			}
		})
	}
}

// The attribute override lives in .git/info/attributes: it outranks every
// other attribute source, and is kept in step on every CommitWork and Clone.
func TestInfoAttributesOverride(t *testing.T) {
	hermetic(t)
	ctx := t.Context()
	vault := makeVault(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	if _, err := Init(ctx, opts(vault, remote)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(vault, ".git", "info", "attributes")
	if got := readFile(t, path); got != wantInfoAttributes {
		t.Fatalf("after Init .git/info/attributes = %q, want %q", got, wantInfoAttributes)
	}
	if err := os.WriteFile(path, []byte("* text=auto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	put(t, vault, "wiki/alpha.md", "x\n")
	if _, err := CommitWork(opts(vault, remote), "lw x"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != wantInfoAttributes {
		t.Fatalf("after CommitWork .git/info/attributes = %q, want it restored", got)
	}
	clone := filepath.Join(t.TempDir(), "pc-b")
	if err := Clone(ctx, opts(clone, remote), 1); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(clone, ".git", "info", "attributes")); got != wantInfoAttributes {
		t.Fatalf("after Clone .git/info/attributes = %q, want %q", got, wantInfoAttributes)
	}
}

// M1: a vault whose .git is empty or invalid, inside another repository, must
// fail closed. Without a ceiling git walks up, finds the parent repository and
// commits the parent's files.
func TestNestedInvalidGitDirFailsClosed(t *testing.T) {
	hermetic(t)
	outer := t.TempDir()
	git(t, outer, "init", "-b", "main", ".")
	put(t, outer, "outer.txt", "the parent's own file\n")
	git(t, outer, "add", "-A")
	git(t, outer, "commit", "-m", "outer")
	before := git(t, outer, "rev-parse", "HEAD")

	vault := filepath.Join(outer, "vault")
	put(t, vault, "SCHEMA.md", "s\n")
	put(t, vault, "wiki/a.md", "a\n")
	if err := os.Mkdir(filepath.Join(vault, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	if _, err := Init(t.Context(), opts(vault, remote)); err == nil {
		t.Fatal("Init succeeded on a vault whose .git is not a repository")
	}
	if committed, err := CommitWork(opts(vault, remote), "x"); err == nil || committed {
		t.Fatalf("CommitWork = %v, %v; want a refusal", committed, err)
	}
	if got := git(t, outer, "rev-parse", "HEAD"); got != before {
		t.Fatalf("the parent repository got a commit: %s -> %s", before, got)
	}
	if got := git(t, outer, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("the parent repository has staged files:\n%s", got)
	}
}

// M2: an interrupted sync leaves a .lock; the next sync names it instead of
// printing git's wall of text.
func TestInterruptedSyncNamesTheLock(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()
	// B must have something to fetch, or git never touches the ref.
	put(t, p.a, "wiki/alpha.md", "newer\n")
	if _, err := CommitWork(opts(p.a, p.remote), "lw 2"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatal(err)
	}

	lock := filepath.Join(p.b, ".git", "refs", "remotes", "lw", "main.lock")
	put(t, p.b, ".git/refs/remotes/lw/main.lock", "")
	named := func(err error, path string) bool {
		if err == nil {
			return false
		}
		for _, p := range []string{path, realOf(path)} {
			if err.Error() == "a previous sync was interrupted and left "+p+"; remove it and run lw sync again" {
				return true
			}
		}
		return false
	}
	for name, op := range map[string]func() error{
		"Status": func() error { _, err := Status(ctx, opts(p.b, p.remote)); return err },
		"Pull":   func() error { _, err := Pull(ctx, opts(p.b, p.remote), 1); return err },
		"Push":   func() error { _, err := Push(ctx, opts(p.b, p.remote)); return err },
	} {
		err := op()
		var re *RemoteError
		if !named(err, lock) || errors.As(err, &re) {
			t.Errorf("%s err = %v, want the interrupted-sync text naming %s, not a RemoteError", name, err, lock)
		}
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if st, err := Pull(ctx, opts(p.b, p.remote), 1); err != nil || st.Pulled != 1 {
		t.Fatalf("Pull after removing the lock = %+v, %v", st, err)
	}

	// The same for the index.
	index := filepath.Join(p.b, ".git", "index.lock")
	put(t, p.b, ".git/index.lock", "")
	put(t, p.b, "notes/x.md", "x\n")
	if _, err := CommitWork(opts(p.b, p.remote), "lw x"); !named(err, index) {
		t.Fatalf("CommitWork err = %v, want the interrupted-sync text naming %s", err, index)
	}
	os.Remove(index)
	if committed, err := CommitWork(opts(p.b, p.remote), "lw x"); err != nil || !committed {
		t.Fatalf("CommitWork after removing the lock = %v, %v", committed, err)
	}
}

func realOf(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// M2: a timeout asks the process group to stop (SIGTERM, so git can remove
// its own locks) and only kills it after a grace period.
func TestTimeoutTermThenKill(t *testing.T) {
	hermetic(t)
	installFakeSSH(t)
	p := newPair(t)

	t.Run("SIGTERM is delivered first", func(t *testing.T) {
		mark := filepath.Join(t.TempDir(), "term")
		t.Setenv("FAKE_SSH_SLEEP", uniq(1))
		t.Setenv("FAKE_SSH_TERM_MARK", mark)
		o := opts(p.a, "fake:"+p.bare)
		o.Timeout = time.Second
		_, err := Status(t.Context(), o)
		var re *RemoteError
		if !errors.As(err, &re) || !strings.Contains(err.Error(), "timed out after 1s") {
			t.Fatalf("err = %v, want a timeout", err)
		}
		if got := readFile(t, mark); got != "term\n" {
			t.Fatalf("the ssh child never saw SIGTERM before it was killed (marker %q)", got)
		}
		waitGone(t, "sleep "+uniq(1))
	})
	t.Run("SIGKILL follows a grace period for a child that ignores SIGTERM", func(t *testing.T) {
		t.Setenv("FAKE_SSH_SLEEP", uniq(2))
		t.Setenv("FAKE_SSH_IGNORE_TERM", "1")
		o := opts(p.a, "fake:"+p.bare)
		o.Timeout = time.Second
		start := time.Now()
		_, err := Status(t.Context(), o)
		elapsed := time.Since(start)
		var re *RemoteError
		if !errors.As(err, &re) {
			t.Fatalf("err = %v, want a timeout", err)
		}
		if elapsed < 2300*time.Millisecond || elapsed > 5*time.Second {
			t.Fatalf("took %s; want the 1 s timeout plus the 1.5 s grace before the kill", elapsed)
		}
		waitGone(t, "sleep "+uniq(2))
	})
}

// M2: cancelling an interactive call must not orphan git's ssh child.
func TestInteractiveCancelKillsChildren(t *testing.T) {
	hermetic(t)
	installFakeSSH(t)
	p := newPair(t)
	t.Setenv("FAKE_SSH_SLEEP", uniq(3))

	o := opts(p.a, "fake:"+p.bare)
	o.Interactive = true
	var stderr strings.Builder
	o.Stderr = &stderr
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	time.AfterFunc(700*time.Millisecond, cancel)
	start := time.Now()
	_, err := Status(ctx, o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("the cancelled call took %s", time.Since(start))
	}
	waitGone(t, "sleep "+uniq(3))
}

// M3: the remote path alphabet is enforced character by character, so
// loosening the regex for any one byte is caught.
func TestRemotePathPerCharacter(t *testing.T) {
	allowed := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("._/~-", c) >= 0
	}
	for c := 0; c < 0x80; c++ {
		path := "a" + string(rune(c)) + "b"
		err := remote{path: path}.validatePath()
		if allowed(byte(c)) {
			if err != nil {
				t.Errorf("byte %#02x was refused: %v", c, err)
			}
			continue
		}
		want := fmt.Sprintf("remote path %q: only letters, digits and . _ / ~ - are allowed", path)
		if err == nil || err.Error() != want {
			t.Errorf("byte %#02x: err = %v, want %q", c, err, want)
		}
	}
	for _, path := range []string{"", "é", "a b", "日本"} {
		if err := (remote{path: path}).validatePath(); err == nil {
			t.Errorf("path %q was accepted", path)
		}
	}
	// A leading "-" would be an option to whatever receives the path.
	err := remote{path: "-x"}.validatePath()
	if want := `remote path "-x": must not start with "-"`; err == nil || err.Error() != want {
		t.Errorf("leading dash: err = %v, want %q", err, want)
	}
}

// M3: through the fake ssh, which really runs the command in sh -c, a path
// built to inject a command runs nothing — it never reaches ssh at all.
func TestRemotePathInjectionRunsNothing(t *testing.T) {
	home := hermetic(t)
	logPath := installFakeSSH(t)
	for _, path := range []string{
		`a'; touch $HOME/pwn; '`,
		`a"; touch $HOME/pwn; "`,
		"a$(touch $HOME/pwn)",
		"a`touch $HOME/pwn`",
		"a;touch $HOME/pwn",
		"a&&touch $HOME/pwn",
		"a|touch $HOME/pwn",
		"a\ntouch $HOME/pwn",
	} {
		_, err := Init(t.Context(), opts(makeVault(t), "fake:"+path))
		if err == nil || !strings.HasPrefix(err.Error(), "remote path ") {
			t.Errorf("path %q: err = %v, want a path refusal", path, err)
		}
		if _, serr := os.Stat(filepath.Join(home, "pwn")); serr == nil {
			t.Fatalf("path %q ran a command on the server", path)
		}
	}
	if got := readFile(t, logPath); got != "" {
		t.Fatalf("a refused path still reached ssh:\n%s", got)
	}
	// And a leading dash on the way through Init.
	_, err := Init(t.Context(), opts(makeVault(t), "fake:-x"))
	if want := `remote path "-x": must not start with "-"`; err == nil || err.Error() != want {
		t.Fatalf("host:-x err = %v, want %q", err, want)
	}
}

// M4: a global push.gpgSign=true would fail every push on a server that
// cannot verify a signature.
func TestGlobalPushGpgSignIsOff(t *testing.T) {
	hermetic(t)
	writeGlobalGitConfig(t, "[push]\n\tgpgSign = true\n")
	remote := filepath.Join(t.TempDir(), "remote.git")
	vault := makeVault(t)
	if st, err := Init(t.Context(), opts(vault, remote)); err != nil || st.Pushed != 1 {
		t.Fatalf("Init = %+v, %v", st, err)
	}
	put(t, vault, "wiki/alpha.md", "x\n")
	if _, err := CommitWork(opts(vault, remote), "lw x"); err != nil {
		t.Fatal(err)
	}
	if st, err := Push(t.Context(), opts(vault, remote)); err != nil || st.Pushed != 1 {
		t.Fatalf("Push = %+v, %v", st, err)
	}
}

// M4: a user's own ssh command (a key, a jump host) is kept, with the batch
// options appended, for git's transport and for Init's own ssh call.
func TestUserSSHCommandIsKept(t *testing.T) {
	logOrder := func(t *testing.T, line string, in ...string) {
		t.Helper()
		from := 0
		for _, w := range in {
			i := strings.Index(line[from:], "["+w+"]")
			if i < 0 {
				t.Fatalf("ssh argv lacks [%s] (in order %v): %s", w, in, line)
			}
			from += i + len(w) + 2
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T)
		key   string
	}{
		{"GIT_SSH_COMMAND", func(t *testing.T) { t.Setenv("GIT_SSH_COMMAND", "ssh -i /fake/key-env") }, "/fake/key-env"},
		{"core.sshCommand", func(t *testing.T) {
			writeGlobalGitConfig(t, "[core]\n\tsshCommand = ssh -i /fake/key-cfg\n")
		}, "/fake/key-cfg"},
		{"GIT_SSH_COMMAND wins over core.sshCommand", func(t *testing.T) {
			t.Setenv("GIT_SSH_COMMAND", "ssh -i /fake/key-env")
			writeGlobalGitConfig(t, "[core]\n\tsshCommand = ssh -i /fake/key-cfg\n")
		}, "/fake/key-env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hermetic(t)
			logPath := installFakeSSH(t)
			p := newPair(t)
			tc.setup(t)
			ctx := t.Context()

			// git's own transport, non-interactive.
			if err := os.WriteFile(logPath, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Status(ctx, opts(p.a, "fake:"+p.bare)); err != nil {
				t.Fatalf("Status: %v", err)
			}
			first := strings.SplitN(readFile(t, logPath), "\n", 2)[0]
			logOrder(t, first, "-i", tc.key, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5")

			// git's own transport, interactive: the user's command, untouched.
			if err := os.WriteFile(logPath, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			o := opts(p.a, "fake:"+p.bare)
			o.Interactive = true
			if _, err := Status(ctx, o); err != nil {
				t.Fatalf("interactive Status: %v", err)
			}
			first = strings.SplitN(readFile(t, logPath), "\n", 2)[0]
			logOrder(t, first, "-i", tc.key)
			if strings.Contains(first, "BatchMode") {
				t.Errorf("interactive: batch options imposed: %s", first)
			}

			// Init's own ssh call uses the same command.
			for _, interactive := range []bool{false, true} {
				if err := os.WriteFile(logPath, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				initOpts := opts(makeVault(t), "fake:"+filepath.Join(t.TempDir(), "r.git"))
				initOpts.Interactive = interactive
				if _, err := Init(ctx, initOpts); err != nil {
					t.Fatalf("Init (interactive=%v): %v", interactive, err)
				}
				first = strings.SplitN(readFile(t, logPath), "\n", 2)[0]
				logOrder(t, first, "-i", tc.key)
				if got := strings.Contains(first, "[BatchMode=yes]"); got == interactive {
					t.Errorf("Init (interactive=%v): BatchMode present = %v: %s", interactive, got, first)
				}
			}
		})
	}
}

// M5: when the checkout does not match the commit (a case collision on a
// case-insensitive filesystem), the step that made it says so. A case-sensitive
// test machine cannot collide — core.ignorecase=true changes nothing there, as
// this test's global config shows — so installUmaskGit makes the checkout drop
// an executable bit, the same "work tree differs from the commit" state.
func TestCheckoutCollisionIsAnError(t *testing.T) {
	hermetic(t)
	writeGlobalGitConfig(t, "[core]\n\tignorecase = true\n")
	installUmaskGit(t)
	ctx := t.Context()
	want := func(paths string) string {
		return "checkout collision: " + paths + " differ from the commit (case-insensitive filesystem?) — nothing will be committed until this is fixed"
	}
	// A vault of root-level files only: the umask the wrapper applies would
	// stop git creating directories during the checkout.
	newExec := func(t *testing.T) pair {
		t.Helper()
		a := filepath.Join(t.TempDir(), "vault")
		put(t, a, "SCHEMA.md", "s\n")
		put(t, a, "run.sh", "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(a, "run.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		p := pair{a: a, bare: filepath.Join(t.TempDir(), "remote.git")}
		p.remote = p.bare
		if _, err := Init(ctx, opts(a, p.remote)); err != nil {
			t.Fatal(err)
		}
		p.b = filepath.Join(t.TempDir(), "pc-b")
		if err := Clone(ctx, opts(p.b, p.remote), 1); err != nil {
			t.Fatalf("Clone: %v", err)
		}
		return p
	}
	t.Run("clone", func(t *testing.T) {
		p := newExec(t)
		t.Setenv("FAKE_GIT_UMASK", "0111")
		dir := filepath.Join(t.TempDir(), "deep", "pc")
		err := Clone(ctx, opts(dir, p.remote, "dead:/never-tried"), 1)
		var re *RemoteError
		if err == nil || err.Error() != want("run.sh") || errors.As(err, &re) {
			t.Fatalf("err = %v, want %q (and not a RemoteError: another remote has the same content)", err, want("run.sh"))
		}
		if _, serr := os.Stat(filepath.Dir(dir)); serr == nil {
			t.Fatal("a failed Clone left its directories behind")
		}
	})
	t.Run("pull", func(t *testing.T) {
		p := newExec(t)
		put(t, p.a, "tool.sh", "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(p.a, "tool.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := CommitWork(opts(p.a, p.remote), "lw tool"); err != nil {
			t.Fatal(err)
		}
		if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
			t.Fatal(err)
		}
		t.Setenv("FAKE_GIT_UMASK", "0111")
		st, err := Pull(ctx, opts(p.b, p.remote), 1)
		if err == nil || err.Error() != want("tool.sh") {
			t.Fatalf("err = %v, want %q", err, want("tool.sh"))
		}
		if st.Pulled != 1 {
			t.Fatalf("State = %+v: HEAD moved, so Pulled must say 1", st)
		}
	})
	t.Run("take-remote", func(t *testing.T) {
		p := newExec(t)
		put(t, p.a, "tool.sh", "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(p.a, "tool.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := CommitWork(opts(p.a, p.remote), "lw tool"); err != nil {
			t.Fatal(err)
		}
		if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
			t.Fatal(err)
		}
		put(t, p.b, "local.md", "local\n")
		if _, err := CommitWork(opts(p.b, p.remote), "lw local"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("FAKE_GIT_UMASK", "0111")
		name, _, err := TakeRemote(ctx, opts(p.b, p.remote), 1)
		if err == nil || err.Error() != want("tool.sh") {
			t.Fatalf("err = %v, want %q", err, want("tool.sh"))
		}
		if name == "" || git(t, p.b, "show", name+":local.md") != "local" {
			t.Fatalf("the backup branch %q was not reported/kept", name)
		}
	})
	t.Run("a clean checkout is not an error", func(t *testing.T) {
		p := newExec(t) // FAKE_GIT_UMASK is empty again here
		if st, err := Pull(ctx, opts(p.b, p.remote), 1); err != nil || st.Pulled != 0 {
			t.Fatalf("Pull = %+v, %v", st, err)
		}
	})
}

func TestCollisionMessage(t *testing.T) {
	z := func(entries ...string) string { return strings.Join(entries, "\x00") + "\x00" }
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{z(" M run.sh"), []string{"run.sh"}},
		{z(" M Wiki/A.md", " M wiki/a.md"), []string{"Wiki/A.md", "wiki/a.md"}},
		{z("R  new name.md", "old name.md", " M b.md"), []string{"new name.md", "b.md"}},
		{z("MM x"), []string{"x"}},
	} {
		if got := statusPaths(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("statusPaths(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if got, want := collisionError([]string{"a", "b"}).Error(),
		"checkout collision: a, b differ from the commit (case-insensitive filesystem?) — nothing will be committed until this is fixed"; got != want {
		t.Errorf("two paths: %q, want %q", got, want)
	}
	seven := []string{"1", "2", "3", "4", "5", "6", "7"}
	if got, want := collisionError(seven).Error(),
		"checkout collision: 1, 2, 3, 4, 5 and 2 more differ from the commit (case-insensitive filesystem?) — nothing will be committed until this is fixed"; got != want {
		t.Errorf("seven paths: %q, want %q", got, want)
	}
}

// L: a root file named HEAD makes `git rev-list HEAD` and `git reset HEAD`
// ambiguous without a `--`.
func TestRootFileNamedHEAD(t *testing.T) {
	hermetic(t)
	ctx := t.Context()
	a := makeVault(t)
	put(t, a, "HEAD", "not a ref\n")
	put(t, a, "main", "nor this\n")
	bare := filepath.Join(t.TempDir(), "remote.git")
	if st, err := Init(ctx, opts(a, bare)); err != nil || st.Pushed != 1 {
		t.Fatalf("Init = %+v, %v", st, err)
	}
	b := filepath.Join(t.TempDir(), "pc-b")
	if err := Clone(ctx, opts(b, bare), 1); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	put(t, a, "wiki/alpha.md", "v2\n")
	if _, err := CommitWork(opts(a, bare), "lw 2"); err != nil {
		t.Fatal(err)
	}
	if st, err := Push(ctx, opts(a, bare)); err != nil || st.Pushed != 1 {
		t.Fatalf("Push = %+v, %v", st, err)
	}
	if st, err := Status(ctx, opts(b, bare)); err != nil || st.Behind != 1 {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	if st, err := Pull(ctx, opts(b, bare), 1); err != nil || st.Pulled != 1 {
		t.Fatalf("Pull = %+v, %v", st, err)
	}
	put(t, a, "wiki/alpha.md", "v3 from A\n")
	if _, err := CommitWork(opts(a, bare), "lw 3"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, opts(a, bare)); err != nil {
		t.Fatal(err)
	}
	put(t, b, "wiki/beta.md", "from B\n")
	put(t, b, "wiki/alpha.md", "v3 from B\n") // A-042-8: a conflict, or it would be rebased
	if _, err := CommitWork(opts(b, bare), "lw b"); err != nil {
		t.Fatal(err)
	}
	if _, err := Pull(ctx, opts(b, bare), 1); !errors.Is(err, ErrDiverged) {
		t.Fatalf("Pull err = %v, want ErrDiverged", err)
	}
	if name, _, err := TakeRemote(ctx, opts(b, bare), 1); err != nil || name == "" {
		t.Fatalf("TakeRemote = %q, %v", name, err)
	}
}

// L: a symlink or a nested repository is stored by git without its content, so
// the other PC would get a dangling link or an empty directory. Refuse.
func TestSymlinkAndNestedRepoRefused(t *testing.T) {
	hermetic(t)
	ctx := t.Context()

	t.Run("symlink", func(t *testing.T) {
		p := newPair(t)
		before := git(t, p.a, "rev-parse", "HEAD")
		if err := os.Symlink("../wiki/alpha.md", filepath.Join(p.a, "notes", "link")); err != nil {
			t.Fatal(err)
		}
		committed, err := CommitWork(opts(p.a, p.remote), "lw x")
		want := "cannot sync notes/link: symlink — lw sync copies files only"
		if err == nil || err.Error() != want || committed {
			t.Fatalf("CommitWork = %v, %v; want false, %q", committed, err, want)
		}
		if got := git(t, p.a, "rev-parse", "HEAD"); got != before {
			t.Fatal("a refused CommitWork still committed")
		}
		if got := git(t, p.a, "diff", "--cached", "--name-only"); got != "" {
			t.Fatalf("a refused CommitWork left files staged:\n%s", got)
		}
		os.Remove(filepath.Join(p.a, "notes", "link"))
		if _, err := CommitWork(opts(p.a, p.remote), "lw y"); err != nil {
			t.Fatalf("after removing the link: %v", err)
		}
	})
	t.Run("nested repository", func(t *testing.T) {
		p := newPair(t)
		nested := filepath.Join(p.a, "raw", "proj")
		put(t, nested, "readme.md", "inner\n")
		git(t, nested, "init", "-b", "main", ".")
		git(t, nested, "add", "-A")
		git(t, nested, "commit", "-m", "inner")
		committed, err := CommitWork(opts(p.a, p.remote), "lw x")
		want := "cannot sync raw/proj: nested git repo — lw sync copies files only"
		if err == nil || err.Error() != want || committed {
			t.Fatalf("CommitWork = %v, %v; want false, %q", committed, err, want)
		}
		if got := git(t, p.a, "diff", "--cached", "--name-only"); got != "" {
			t.Fatalf("a refused CommitWork left files staged:\n%s", got)
		}
	})
	t.Run("Init refuses before pushing and retries once fixed", func(t *testing.T) {
		vault := makeVault(t)
		if err := os.Symlink("wiki/alpha.md", filepath.Join(vault, "shortcut")); err != nil {
			t.Fatal(err)
		}
		bare := filepath.Join(t.TempDir(), "remote.git")
		_, err := Init(ctx, opts(vault, bare))
		if want := "cannot sync shortcut: symlink — lw sync copies files only"; err == nil || err.Error() != want {
			t.Fatalf("Init err = %v, want %q", err, want)
		}
		if got := git(t, bare, "rev-list", "-n", "1", "--all"); got != "" {
			t.Fatalf("the remote received a commit: %s", got)
		}
		os.Remove(filepath.Join(vault, "shortcut"))
		if st, err := Init(ctx, opts(vault, bare)); err != nil || st.Pushed != 1 {
			t.Fatalf("Init retry = %+v, %v", st, err)
		}
	})
	t.Run("ignored paths may hold both", func(t *testing.T) {
		p := newPair(t)
		if err := os.MkdirAll(filepath.Join(p.a, ".llmwiki", "cache"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc/hostname", filepath.Join(p.a, ".llmwiki", "cache", "link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc", filepath.Join(p.a, ".DS_Store")); err != nil {
			t.Fatal(err)
		}
		if committed, err := CommitWork(opts(p.a, p.remote), "lw x"); err != nil || committed {
			t.Fatalf("CommitWork over ignored symlinks = %v, %v; want false, nil", committed, err)
		}
	})
}

// L: when ssh answers with something that is not the script's verdict, the
// remote's stderr is the only clue to why.
func TestUnexpectedReplyKeepsStderr(t *testing.T) {
	hermetic(t)
	installFakeSSH(t)
	t.Setenv("FAKE_SSH_BADREPLY", "1")
	_, err := Init(t.Context(), opts(makeVault(t), "fake:/srv/r.git"))
	var re *RemoteError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), `"hello"`) || !strings.Contains(err.Error(), "some warning") {
		t.Fatalf("err = %v, want the reply and the remote's stderr", err)
	}
}

// L: an empty remote that answers first must not win over one that holds the
// vault — otherwise the next push re-sends the whole vault to the wrong place.
func TestEmptyRemoteDoesNotWinOverFullRemote(t *testing.T) {
	hermetic(t)
	installFakeSSH(t)
	p := newPair(t)
	ctx := t.Context()
	empty, empty2 := bareRemote(t), bareRemote(t)

	t.Run("empty then full", func(t *testing.T) {
		st, err := Status(ctx, opts(p.a, empty, p.remote))
		if err != nil || st.Remote != p.remote || st.Ahead != 0 || st.Behind != 0 || st.RemoteFormat != 1 {
			t.Fatalf("Status = %+v, %v; want the full remote", st, err)
		}
	})
	t.Run("dead, empty, then full", func(t *testing.T) {
		st, err := Status(ctx, opts(p.a, "dead:/srv/none", empty, p.remote))
		if err != nil || st.Remote != p.remote {
			t.Fatalf("Status = %+v, %v; want the full remote", st, err)
		}
	})
	t.Run("full then empty: the first stays", func(t *testing.T) {
		st, err := Status(ctx, opts(p.a, p.remote, empty))
		if err != nil || st.Remote != p.remote {
			t.Fatalf("Status = %+v, %v", st, err)
		}
	})
	t.Run("all empty: the first answered wins", func(t *testing.T) {
		st, err := Status(ctx, opts(p.a, empty, empty2))
		if err != nil || st.Remote != empty || st.RemoteFormat != 0 || st.Ahead < 1 {
			t.Fatalf("Status = %+v, %v; want the first empty remote with all of HEAD ahead", st, err)
		}
	})
	t.Run("empty then dead: the empty one answered", func(t *testing.T) {
		st, err := Status(ctx, opts(p.a, empty, "dead:/srv/none"))
		if err != nil || st.Remote != empty {
			t.Fatalf("Status = %+v, %v", st, err)
		}
	})
	t.Run("push goes to the full remote", func(t *testing.T) {
		put(t, p.b, "wiki/beta.md", "b\n")
		if _, err := CommitWork(opts(p.b, p.remote), "lw b"); err != nil {
			t.Fatal(err)
		}
		st, err := Push(ctx, opts(p.b, empty, p.remote))
		if err != nil || st.Remote != p.remote || st.Pushed != 1 {
			t.Fatalf("Push = %+v, %v; want Pushed 1 to the full remote", st, err)
		}
		if got := git(t, empty, "rev-list", "-n", "1", "--all"); got != "" {
			t.Fatal("the empty remote received a push meant for the full one")
		}
	})
}

// A global core.ignorecase=true is what a Mac has; the everyday cycle must
// not care.
func TestCycleUnderIgnoreCase(t *testing.T) {
	hermetic(t)
	writeGlobalGitConfig(t, "[core]\n\tignorecase = true\n")
	p := newPair(t)
	ctx := t.Context()
	put(t, p.a, "Topics/Page.md", "x\n")
	if _, err := CommitWork(opts(p.a, p.remote), "lw x"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatal(err)
	}
	if st, err := Pull(ctx, opts(p.b, p.remote), 1); err != nil || st.Pulled != 1 {
		t.Fatalf("Pull = %+v, %v", st, err)
	}
	if got := readFile(t, filepath.Join(p.b, "Topics/Page.md")); got != "x\n" {
		t.Fatalf("Topics/Page.md = %q", got)
	}
}

// The git flags themselves neutralise the user's configuration, independently
// of .git/info/attributes (which they back up): a call that runs before that
// file exists, or outside CommitWork, must not see the user's attributes or
// ignore rules either.
func TestGitFlagsNeutraliseUserConfig(t *testing.T) {
	home := hermetic(t)
	put(t, home, "global-attrs", "* text=auto eol=crlf\n")
	put(t, home, "global-ignore", "*.pdf\n")
	writeGlobalGitConfig(t, "[core]\n\tattributesFile = "+filepath.Join(home, "global-attrs")+
		"\n\texcludesFile = "+filepath.Join(home, "global-ignore")+"\n")
	vault := makeVault(t)
	ctx := t.Context()
	if _, err := Init(ctx, opts(vault, filepath.Join(t.TempDir(), "r.git"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(vault, ".git", "info", "attributes")); err != nil {
		t.Fatal(err)
	}
	r, err := newRunner(opts(vault))
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.gitCall(ctx, call{args: []string{"check-attr", "text", "eol", "--", "raw/x.md"}})
	if want := "raw/x.md: text: unspecified\nraw/x.md: eol: unspecified\n"; err != nil || out != want {
		t.Fatalf("check-attr = %q, %v; want the user's attributes ignored: %q", out, err, want)
	}
	// Exit 1 and no output: not ignored.
	out, err = r.gitCall(ctx, call{args: []string{"check-ignore", "--no-index", "-v", "--", "raw/book.pdf"}})
	var ce *cmdError
	if !errors.As(err, &ce) || ce.exitCode() != 1 || out != "" {
		t.Fatalf("check-ignore = %q, %v; want exit 1 (the user's global ignore not applied)", out, err)
	}
}

// A Pull on its own — no CommitWork before it — must not check files out
// under the vault's own .gitattributes either.
func TestPullDoesNotDependOnCommitWorkForAttributes(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	ctx := t.Context()
	put(t, p.a, ".gitattributes", "* text eol=crlf\n")
	put(t, p.a, "raw/crlf.md", "a\r\nb\r\n")
	put(t, p.a, "raw/lf.md", "a\nb\n")
	if _, err := CommitWork(opts(p.a, p.remote), "lw x"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(p.b, ".git", "info", "attributes")); err != nil {
		t.Fatal(err)
	}
	if st, err := Pull(ctx, opts(p.b, p.remote), 1); err != nil || st.Pulled != 1 {
		t.Fatalf("Pull = %+v, %v", st, err)
	}
	for rel, want := range map[string]string{"raw/crlf.md": "a\r\nb\r\n", "raw/lf.md": "a\nb\n"} {
		if got := readFile(t, filepath.Join(p.b, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

// ---- S1d: the collision marker (.git/lw-collision) ----

func collisionRefusal(paths string) string {
	return "a checkout collision is unresolved (" + paths + ") — rename the clashing files on the PC that created them, sync there, then run lw sync --take-remote here"
}

func collisionText(paths string) string {
	return "checkout collision: " + paths + " differ from the commit (case-insensitive filesystem?) — nothing will be committed until this is fixed"
}

// execPair is a synced pair whose vault is root-level files only, one of them
// executable: the shape installUmaskGit needs to make a checkout come out
// different from its commit.
func execPair(t *testing.T) pair {
	t.Helper()
	ctx := t.Context()
	a := filepath.Join(t.TempDir(), "vault")
	put(t, a, "SCHEMA.md", "s\n")
	addExec(t, a, "run.sh")
	p := pair{a: a, bare: filepath.Join(t.TempDir(), "remote.git")}
	p.remote = p.bare
	if _, err := Init(ctx, opts(a, p.remote)); err != nil {
		t.Fatal(err)
	}
	p.b = filepath.Join(t.TempDir(), "pc-b")
	if err := Clone(ctx, opts(p.b, p.remote), 1); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	return p
}

func addExec(t *testing.T, dir, name string) {
	t.Helper()
	put(t, dir, name, "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
}

// collide makes B's Pull produce a collision on tool.sh (the umask wrapper
// strips its exec bit) and returns with the marker in place and the umask
// still active.
func collide(t *testing.T, p pair) {
	t.Helper()
	ctx := t.Context()
	addExec(t, p.a, "tool.sh")
	if _, err := CommitWork(opts(p.a, p.remote), "lw tool"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GIT_UMASK", "0111")
	if _, err := Pull(ctx, opts(p.b, p.remote), 1); err == nil || err.Error() != collisionText("tool.sh") {
		t.Fatalf("Pull err = %v, want the collision", err)
	}
}

type repoState struct {
	head, index, remote, refs string
	tree                      map[string]string
}

func snapshot(t *testing.T, p pair) repoState {
	t.Helper()
	return repoState{
		head:   git(t, p.b, "rev-parse", "HEAD"),
		index:  git(t, p.b, "ls-files", "-s"),
		remote: git(t, p.bare, "rev-parse", "main"),
		refs:   git(t, p.b, "for-each-ref"),
		tree:   workTree(t, p.b),
	}
}

func (s repoState) mustEqual(t *testing.T, got repoState, what string) {
	t.Helper()
	if s.head != got.head || s.index != got.index || s.remote != got.remote || s.refs != got.refs {
		t.Errorf("%s changed the repository:\nhead %s -> %s\nindex:\n%s\n->\n%s\nremote %s -> %s\nrefs:\n%s\n->\n%s",
			what, s.head, got.head, s.index, got.index, s.remote, got.remote, s.refs, got.refs)
	}
	sameTree(t, got.tree, s.tree, what)
}

// A collision persists as .git/lw-collision. While it exists CommitWork, Pull
// and Push refuse, and change nothing: the next CommitWork would otherwise
// read the collided tree as the user's edit and commit a deletion, and the
// push would carry it to every PC.
func TestCollisionMarkerBlocksSyncUntilTakeRemote(t *testing.T) {
	hermetic(t)
	installUmaskGit(t)
	ctx := t.Context()
	p := execPair(t)
	collide(t, p)

	marker := filepath.Join(p.b, ".git", "lw-collision")
	if got := readFile(t, marker); got != "tool.sh\n" {
		t.Fatalf(".git/lw-collision = %q, want %q", got, "tool.sh\n")
	}
	// Something other than lw sync committed (the engine does), and the tree
	// has unsaved work and a changed .gitignore.
	git(t, p.b, "commit", "--allow-empty", "-m", "engine commit")
	put(t, p.b, "notes.md", "unsaved\n")
	put(t, p.b, ".gitignore", "junk\n")
	before := snapshot(t, p)

	want := collisionRefusal("tool.sh")
	committed, err := CommitWork(opts(p.b, p.remote), "lw x")
	if err == nil || err.Error() != want || committed {
		t.Fatalf("CommitWork = %v, %v; want false, %q", committed, err, want)
	}
	if _, err := Pull(ctx, opts(p.b, p.remote), 1); err == nil || err.Error() != want {
		t.Fatalf("Pull err = %v, want %q", err, want)
	}
	if st, err := Push(ctx, opts(p.b, p.remote)); err == nil || err.Error() != want || st.Pushed != 0 {
		t.Fatalf("Push = %+v, %v; want a refusal %q", st, err, want)
	}
	before.mustEqual(t, snapshot(t, p), "a refused CommitWork, Pull and Push")
	if got := readFile(t, marker); got != "tool.sh\n" {
		t.Fatalf("the marker changed: %q", got)
	}
	// Reading stays possible.
	if st, err := Status(ctx, opts(p.b, p.remote)); err != nil || st.Ahead != 1 {
		t.Fatalf("Status = %+v, %v", st, err)
	}

	// The clashing file is fixed on the PC that made it, and synced there.
	if err := os.Chmod(filepath.Join(p.a, "tool.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	if committed, err := CommitWork(opts(p.a, p.remote), "lw fix tool"); err != nil || !committed {
		t.Fatalf("A CommitWork = %v, %v", committed, err)
	}
	if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatal(err)
	}

	// take-remote is the way out. It does not commit the collided tree.
	name, st, err := TakeRemote(ctx, opts(p.b, p.remote), 1)
	if err != nil {
		t.Fatalf("TakeRemote: %v", err)
	}
	if got := git(t, p.b, "rev-parse", "refs/heads/"+name); got != before.head {
		t.Fatalf("backup branch %s is at %s, want the HEAD before (%s): the collided tree was committed onto it", name, got, before.head)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the marker survived a clean take-remote")
	}
	if got, want := git(t, p.b, "rev-parse", "HEAD"), git(t, p.bare, "rev-parse", "main"); got != want {
		t.Fatalf("HEAD = %s, want the remote tip %s", got, want)
	}
	if got := git(t, p.b, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Fatalf("work tree is dirty after take-remote:\n%s", got)
	}
	if st.Ahead != 0 || st.Behind != 0 {
		t.Fatalf("State = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(p.b, "notes.md")); err != nil {
		t.Fatalf("untracked work was removed: %v", err)
	}

	// Normal service resumes.
	t.Setenv("FAKE_GIT_UMASK", "")
	put(t, p.a, "wiki.md", "from A\n")
	if _, err := CommitWork(opts(p.a, p.remote), "lw a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatal(err)
	}
	if st, err := Pull(ctx, opts(p.b, p.remote), 1); err != nil || st.Pulled != 1 {
		t.Fatalf("Pull = %+v, %v", st, err)
	}
	put(t, p.b, "from-b.md", "from B\n")
	if committed, err := CommitWork(opts(p.b, p.remote), "lw b"); err != nil || !committed {
		t.Fatalf("B CommitWork = %v, %v", committed, err)
	}
	if st, err := Push(ctx, opts(p.b, p.remote)); err != nil || st.Pushed != 1 {
		t.Fatalf("B Push = %+v, %v", st, err)
	}
}

// take-remote that still collides must neither clear the marker nor commit
// the collided tree; the guard stays until a take-remote comes out clean.
func TestTakeRemoteKeepsMarkerWhileStillColliding(t *testing.T) {
	hermetic(t)
	installUmaskGit(t)
	ctx := t.Context()
	p := execPair(t)
	collide(t, p)
	marker := filepath.Join(p.b, ".git", "lw-collision")
	head := git(t, p.b, "rev-parse", "HEAD")

	// The remote still holds the executable tool.sh, so the umask still
	// breaks the checkout take-remote makes.
	name, _, err := TakeRemote(ctx, opts(p.b, p.remote), 1)
	if err == nil || err.Error() != collisionText("tool.sh") {
		t.Fatalf("TakeRemote err = %v, want the collision", err)
	}
	if name == "" || git(t, p.b, "rev-parse", "refs/heads/"+name) != head {
		t.Fatalf("backup %q is not at the HEAD before (%s)", name, head)
	}
	if got := readFile(t, marker); got != "tool.sh\n" {
		t.Fatalf("the marker after a still-colliding take-remote = %q, want it kept", got)
	}
	if _, err := CommitWork(opts(p.b, p.remote), "lw x"); err == nil || err.Error() != collisionRefusal("tool.sh") {
		t.Fatalf("CommitWork err = %v, want the refusal", err)
	}

	// Once the checkout comes out right, take-remote clears it.
	t.Setenv("FAKE_GIT_UMASK", "")
	if _, _, err := TakeRemote(ctx, opts(p.b, p.remote), 1); err != nil {
		t.Fatalf("second TakeRemote: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the marker survived a clean take-remote")
	}
	if got := git(t, p.b, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Fatalf("work tree is dirty:\n%s", got)
	}
	if _, err := CommitWork(opts(p.b, p.remote), "lw x"); err != nil {
		t.Fatalf("CommitWork after the marker went: %v", err)
	}
}

// The refusal lists every path in the marker, joined by ", ".
func TestCollisionRefusalListsEveryPath(t *testing.T) {
	hermetic(t)
	p := newPair(t)
	put(t, p.a, ".git/lw-collision", "Wiki/A.md\nwiki/a.md\n")
	want := collisionRefusal("Wiki/A.md, wiki/a.md")
	if _, err := CommitWork(opts(p.a, p.remote), "x"); err == nil || err.Error() != want {
		t.Fatalf("CommitWork err = %v, want %q", err, want)
	}
	if _, err := Pull(t.Context(), opts(p.a, p.remote), 1); err == nil || err.Error() != want {
		t.Fatalf("Pull err = %v, want %q", err, want)
	}
	if _, err := Push(t.Context(), opts(p.a, p.remote)); err == nil || err.Error() != want {
		t.Fatalf("Push err = %v, want %q", err, want)
	}
}

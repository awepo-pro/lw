package vaultsync

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// hermetic points everything git and ssh read from the environment at temp
// files, so no test touches the real HOME, ~/.ssh, ~/.gitconfig or any
// ambient GIT_* override (a `make check` run from a git hook carries
// GIT_DIR and GIT_INDEX_FILE). It returns the fake HOME.
func hermetic(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// ssh control sockets (mux.go) go under the cache directory, whose path
	// must be short: t.TempDir() carries the test's name and is not.
	t.Setenv("XDG_CACHE_HOME", shortTemp(t))
	cfg := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// t.Setenv records the old value so the cleanup restores it; the
	// Unsetenv that follows makes the variable absent for this test.
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_PREFIX",
		"GIT_NAMESPACE", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_TERMINAL_PROMPT",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME",
		"GIT_COMMITTER_EMAIL", "GIT_CEILING_DIRECTORIES",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return home
}

// writeGlobalGitConfig replaces the hermetic global git config.
func writeGlobalGitConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(os.Getenv("GIT_CONFIG_GLOBAL"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeSSHScript stands in for ssh. It logs its argv (one [bracketed] word per
// argument) and the two environment variables the non-interactive contract
// names, drops ssh's options and the host, then runs the remaining command
// locally with sh -c — which is exactly what an ssh server's login shell
// would do with it. A host named dead* fails like an unreachable machine;
// FAKE_SSH_SLEEP makes it hang (FAKE_SSH_IGNORE_TERM: ignoring SIGTERM;
// FAKE_SSH_TERM_MARK: writing that file when SIGTERM arrives);
// FAKE_SSH_BADREPLY answers every command with a stdout line and a stderr
// line instead of running it; FAKE_SSH_BG leaves a background process
// holding its pipes after it exits (an ssh ControlPersist master);
// `ssh -G <host>` (the multiplexing probe, mux.go) prints "controlpath none" —
// or FAKE_SSH_CONTROLPATH, or fails under FAKE_SSH_G_FAIL — and is logged to
// $FAKE_SSH_LOG.G, never to $FAKE_SSH_LOG, so the tests that read the transport
// calls there do not see it. FAKE_SSH_BEFORE_RECEIVE names a script run
// once just before a git-receive-pack starts (a push race in the real
// protocol, no seam in the code under test).
const fakeSSHScript = `#!/bin/sh
PATH="$(git --exec-path):$PATH"
LOG="$FAKE_SSH_LOG"
for a in "$@"; do if [ "$a" = "-G" ]; then LOG="$FAKE_SSH_LOG.G"; fi; done
{
  printf 'argv:'
  for a in "$@"; do printf ' [%s]' "$a"; done
  printf '\n'
  printf 'env: GIT_TERMINAL_PROMPT=%s\n' "${GIT_TERMINAL_PROMPT-unset}"
} >> "$LOG"
G=
O=
while [ $# -gt 0 ]; do
  case "$1" in
    --) shift; break ;;
    -G) G=1; shift ;;
    -O) O=1; shift 2 ;;
    -o|-p|-i|-l|-F|-J|-L|-R|-D|-b|-c|-E|-e|-m|-Q|-S|-W|-w) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
host="$1"
shift
# ssh -O exit: a control-master command, answered at once, whatever else is faked.
if [ -n "$O" ]; then exit 0; fi
if [ -n "$G" ]; then
  # ssh -G prints the configuration it would use, locally. FAKE_SSH_G_FAIL makes it fail.
  if [ -n "$FAKE_SSH_G_FAIL" ]; then echo "ssh: unknown option -- G" >&2; exit 255; fi
  case "$FAKE_SSH_CONTROLPATH" in
    omit) printf 'hostname %s\n' "$host" ;;   # real OpenSSH omits controlpath when none is set
    garbage) printf 'not an ssh -G answer\n' ;;
    *) printf 'hostname %s\ncontrolpath %s\n' "$host" "${FAKE_SSH_CONTROLPATH:-none}" ;;
  esac
  exit 0
fi
case "$host" in
  dead*) echo "ssh: Could not resolve hostname $host: Name or service not known" >&2; exit 255 ;;
esac
if [ -n "$FAKE_SSH_BADREPLY" ]; then echo hello; echo "some warning" >&2; exit 0; fi
if [ -n "$FAKE_SSH_SLEEP" ]; then
  if [ -n "$FAKE_SSH_TERM_MARK" ]; then
    trap 'echo term > "$FAKE_SSH_TERM_MARK"; exit 0' TERM
    sleep "$FAKE_SSH_SLEEP" &
    wait
    exit 0
  fi
  if [ -n "$FAKE_SSH_IGNORE_TERM" ]; then trap '' TERM; sleep "$FAKE_SSH_SLEEP"; exit 0; fi
  exec sleep "$FAKE_SSH_SLEEP"
fi
if [ -n "$FAKE_SSH_BG" ]; then sleep "$FAKE_SSH_BG" & fi
case "$*" in
  *git-receive-pack*)
    if [ -n "$FAKE_SSH_BEFORE_RECEIVE" ] && [ ! -e "$FAKE_SSH_BEFORE_RECEIVE.done" ]; then
      : > "$FAKE_SSH_BEFORE_RECEIVE.done"
      sh "$FAKE_SSH_BEFORE_RECEIVE" || exit 99
    fi ;;
esac
cd "$HOME" || exit 1
exec sh -c "$*"
`

// installFakeSSH puts the fake ssh first on PATH and returns the path of the
// file it logs to.
func installFakeSSH(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fakeSSHScript), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	t.Setenv("FAKE_SSH_LOG", logPath)
	t.Setenv("FAKE_SSH_CONTROLPATH", "")
	t.Setenv("FAKE_SSH_G_FAIL", "")
	t.Setenv("FAKE_SSH_SLEEP", "")
	t.Setenv("FAKE_SSH_BG", "")
	t.Setenv("FAKE_SSH_BADREPLY", "")
	t.Setenv("FAKE_SSH_IGNORE_TERM", "")
	t.Setenv("FAKE_SSH_TERM_MARK", "")
	t.Setenv("FAKE_SSH_BEFORE_RECEIVE", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// readFile returns a file's text, or "" when it does not exist.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

// git runs git inside dir with the test identity and fails the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitErr(dir, args...)
	if err != nil {
		t.Fatalf("git %s (in %s): %v", strings.Join(args, " "), dir, err)
	}
	return out
}

// gitErr is git without the fatal: it returns trimmed stdout and an error
// that carries stderr.
func gitErr(dir string, args ...string) (string, error) {
	full := append([]string{"-c", "user.name=test", "-c", "user.email=test@test",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), &execErr{err: err, stderr: errb.String()}
	}
	return strings.TrimSpace(out.String()), nil
}

type execErr struct {
	err    error
	stderr string
}

func (e *execErr) Error() string { return e.err.Error() + ": " + strings.TrimSpace(e.stderr) }

// put writes root/rel (creating directories) and fails the test on error.
func put(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// syncedFiles are the vault paths a sync carries; ignoredFiles are the
// per-PC paths the managed .gitignore keeps out.
var syncedFiles = []string{
	"SCHEMA.md",
	"index.md",
	"log.md",
	"notes/n1.md",
	"raw/r1.md",
	"wiki/alpha.md",
	".llmwiki/journal.ndjson",
	".llmwiki/objects/ab/abcdef",
	".llmwiki/snapshots/000001.tree",
	".llmwiki/changesets/committed/000001/changeset.json",
	".llmwiki/changesets/rejected/000002/changeset.json",
}

var ignoredFiles = []string{
	".llmwiki/index.gob",
	".llmwiki/cache/x.json",
	".llmwiki/tmp/t",
	".llmwiki/lock",
	".llmwiki/logs/lw.log",
	".llmwiki/traces/t1/turn.json",
	".llmwiki/changesets/open/cs1/changeset.json",
	".llmwiki/sync.json",
}

// makeVault builds a vault-shaped directory: every synced path and every
// ignored path from the lists above, with distinct content.
func makeVault(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vault")
	for _, rel := range syncedFiles {
		put(t, dir, rel, "synced "+rel+"\n")
	}
	for _, rel := range ignoredFiles {
		put(t, dir, rel, "ignored "+rel+"\n")
	}
	return dir
}

// opts is the common Options for a test: non-interactive, a generous
// timeout, the given remotes.
func opts(dir string, remotes ...string) Options {
	return Options{Dir: dir, Remotes: remotes, Timeout: 30 * time.Second}
}

// bareRemote creates an empty bare repo (branch main) under a fresh temp dir
// and returns its path.
func bareRemote(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "remote.git")
	git(t, filepath.Dir(p), "init", "--bare", "-b", "main", p)
	return p
}

// treeNames lists the file paths of ref's tree in the repo at dir, sorted.
func treeNames(t *testing.T, dir, ref string) []string {
	t.Helper()
	out := git(t, dir, "ls-tree", "-r", "--name-only", ref)
	if out == "" {
		return nil
	}
	names := strings.Split(out, "\n")
	sort.Strings(names)
	return names
}

// workTree maps every file under dir (excluding .git) to its bytes.
func workTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(dir, path)
		m[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// sameTree fails the test when two workTree maps differ.
func sameTree(t *testing.T, got, want map[string]string, what string) {
	t.Helper()
	for k, v := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("%s: %s missing", what, k)
		} else if g != v {
			t.Errorf("%s: %s = %q, want %q", what, k, g, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: unexpected %s", what, k)
		}
	}
}

// pair is two PCs sharing one remote: a is the vault that ran Init, b is its
// clone.
type pair struct {
	a, b   string // vault dirs
	remote string // the remote spec both use
	bare   string // the bare repo behind remote
}

// newPair inits a vault against a local bare remote and clones it. The bare
// repo is created by Init itself (the remote dir is empty).
func newPair(t *testing.T) pair {
	t.Helper()
	ctx := t.Context()
	p := pair{a: makeVault(t)}
	p.bare = filepath.Join(t.TempDir(), "remote.git")
	if err := os.Mkdir(p.bare, 0o755); err != nil {
		t.Fatal(err)
	}
	p.remote = p.bare
	if _, err := Init(ctx, opts(p.a, p.remote)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	p.b = filepath.Join(t.TempDir(), "pc-b")
	if err := Clone(ctx, opts(p.b, p.remote), 1); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	return p
}

// gitRaw is git without TrimSpace on stdout, for comparing blob bytes.
func gitRaw(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=test", "-c", "user.email=test@test",
		"-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), dir, err, errb.String())
	}
	return out.String()
}

// installUmaskGit puts a git wrapper first on PATH that runs `reset` and
// `merge` — the two commands that write a checkout — under the umask in
// FAKE_GIT_UMASK. A umask of 0111 strips the owner's exec bit from every file
// the checkout creates, so an executable file in the commit comes out
// different from the commit: the same observable as a case collision on a
// case-insensitive filesystem, which a case-sensitive test machine cannot
// produce (core.ignorecase=true does not). Directories are only created by
// other commands, so the 0111 never breaks them.
func installUmaskGit(t *testing.T) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in reset|merge) [ -n \"$FAKE_GIT_UMASK\" ] && umask \"$FAKE_GIT_UMASK\" ;; esac; done\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GIT_UMASK", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// waitGone polls until no process command line contains pattern, or fails the
// test after a few seconds.
func waitGone(t *testing.T, pattern string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		if err := exec.Command("pgrep", "-f", pattern).Run(); err != nil {
			return // pgrep exits 1 when nothing matches
		}
		if time.Now().After(deadline) {
			exec.Command("pkill", "-f", pattern).Run()
			t.Fatalf("a process matching %q outlived the call", pattern)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// shortTemp is a fresh directory with a short path (/tmp/lwv-123456): a unix
// socket path must fit in 104 bytes, and t.TempDir() puts the test's name in
// its.
func shortTemp(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "lwv-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

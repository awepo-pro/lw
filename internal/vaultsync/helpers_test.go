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
// FAKE_SSH_SLEEP makes it hang; FAKE_SSH_BG leaves a background process
// holding its pipes after it exits (an ssh ControlPersist master);
// FAKE_SSH_BEFORE_RECEIVE names a script run
// once just before a git-receive-pack starts (a push race in the real
// protocol, no seam in the code under test).
const fakeSSHScript = `#!/bin/sh
PATH="$(git --exec-path):$PATH"
{
  printf 'argv:'
  for a in "$@"; do printf ' [%s]' "$a"; done
  printf '\n'
  printf 'env: GIT_TERMINAL_PROMPT=%s\n' "${GIT_TERMINAL_PROMPT-unset}"
} >> "$FAKE_SSH_LOG"
while [ $# -gt 0 ]; do
  case "$1" in
    --) shift; break ;;
    -o|-p|-i|-l|-F|-J|-L|-R|-D|-b|-c|-E|-e|-m|-O|-Q|-S|-W|-w) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
host="$1"
shift
case "$host" in
  dead*) echo "ssh: Could not resolve hostname $host: Name or service not known" >&2; exit 255 ;;
esac
if [ -n "$FAKE_SSH_SLEEP" ]; then exec sleep "$FAKE_SSH_SLEEP"; fi
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
	t.Setenv("FAKE_SSH_SLEEP", "")
	t.Setenv("FAKE_SSH_BG", "")
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

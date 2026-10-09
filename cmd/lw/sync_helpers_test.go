package main

// sync_helpers_test.go is the harness every 042 sync test shares. Nothing
// here touches the network, ~/.ssh, ~/.gitconfig or the real HOME/XDG: git is
// pointed at an empty global config, remotes are bare repositories in temp
// directories (or a fake ssh that runs the command locally), and each "PC" is
// a private XDG config directory plus a private vault directory. The process
// has one environment, so a PC "acts" by switching XDG_CONFIG_HOME before it
// runs a verb — which is exactly the isolation two real machines have.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/testutil"
)

// syncHermetic points everything git, ssh and lw read from the environment
// at temp files and returns the fake HOME. It is the first line of every sync
// test.
func syncHermetic(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	// ssh control sockets go under the cache directory, whose path must be
	// short (a unix socket path fits 104 bytes): not under the test's long name.
	t.Setenv("XDG_CACHE_HOME", syncTemp(t))
	gitCfg := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(gitCfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitCfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_PREFIX",
		"GIT_NAMESPACE", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_TERMINAL_PROMPT",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME",
		"GIT_COMMITTER_EMAIL", "GIT_CEILING_DIRECTORIES", "LW_VAULT",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return home
}

// syncTemp is a fresh temp directory whose name is safe in a remote path: the
// remote path regexp allows letters, digits and . _ / ~ - only, and t.TempDir
// puts the test's own name — commas and all — in the path.
func syncTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lwsync-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// syncPC is one PC of a sync test: its own config directory and its own vault
// path (which may not exist yet — a PC that has not cloned).
type syncPC struct {
	t    *testing.T
	name string
	cfg  string // XDG_CONFIG_HOME while this PC acts
	root string // where this PC's vault lives
}

// newSyncPC makes a PC with no config and no vault.
func newSyncPC(t *testing.T, name string) *syncPC {
	t.Helper()
	dir := syncTemp(t)
	return &syncPC{t: t, name: name, cfg: filepath.Join(dir, "xdg-config"), root: filepath.Join(dir, name+"-vault")}
}

// act makes this PC the one lw reads its config from.
func (pc *syncPC) act() {
	pc.t.Helper()
	pc.t.Setenv("XDG_CONFIG_HOME", pc.cfg)
}

// lw runs `lw args...` as this PC, in process, and returns what it printed.
func (pc *syncPC) lw(args ...string) (stdout, stderr string, code int) {
	pc.t.Helper()
	pc.act()
	return captureRun(pc.t, func() int { return run(args) })
}

// configPath is this PC's config.toml.
func (pc *syncPC) configPath() string { return filepath.Join(pc.cfg, "lw", "config.toml") }

// setConfig replaces this PC's config.toml with body ("" removes the file).
func (pc *syncPC) setConfig(body string) {
	pc.t.Helper()
	if body == "" {
		os.Remove(pc.configPath())
		return
	}
	if err := os.MkdirAll(filepath.Dir(pc.configPath()), 0o700); err != nil {
		pc.t.Fatal(err)
	}
	if err := os.WriteFile(pc.configPath(), []byte(body), 0o600); err != nil {
		pc.t.Fatal(err)
	}
}

// config returns the config.toml bytes ("" when there is none).
func (pc *syncPC) config() string {
	b, err := os.ReadFile(pc.configPath())
	if err != nil {
		return ""
	}
	return string(b)
}

// useFixture copies the minimal fixture vault to this PC's root.
func (pc *syncPC) useFixture() {
	pc.t.Helper()
	src := testutil.CopyFixture(pc.t, "minimal")
	if err := os.Rename(src, pc.root); err != nil {
		pc.t.Fatalf("move fixture: %v", err)
	}
}

// write creates root-relative file rel with body.
func (pc *syncPC) write(rel, body string) {
	pc.t.Helper()
	putFile(pc.t, pc.root, rel, body)
}

// read returns root-relative file rel, or "" when it does not exist.
func (pc *syncPC) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(pc.root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	return string(b)
}

// git runs git in this PC's vault.
func (pc *syncPC) git(args ...string) string {
	pc.t.Helper()
	return gitIn(pc.t, pc.root, args...)
}

// putFile writes root/rel (creating directories).
func putFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitIn runs git in dir with a test identity and fails the test on error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitInErr(dir, args...)
	if err != nil {
		t.Fatalf("git %s (in %s): %v", strings.Join(args, " "), dir, err)
	}
	return out
}

// gitInErr is gitIn without the fatal: trimmed stdout and an error carrying
// stderr.
func gitInErr(dir string, args ...string) (string, error) {
	full := append([]string{"-c", "user.name=test", "-c", "user.email=test@test",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), &gitInError{err: err, stderr: errb.String()}
	}
	return strings.TrimSpace(out.String()), nil
}

type gitInError struct {
	err    error
	stderr string
}

func (e *gitInError) Error() string { return e.err.Error() + ": " + strings.TrimSpace(e.stderr) }

// remoteSubjects lists the commit subjects on the remote's main, newest first.
func remoteSubjects(t *testing.T, remote string) []string {
	t.Helper()
	out := gitIn(t, remote, "--git-dir="+remote, "log", "--format=%s", "main")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// remoteFile returns a file of the remote's main tip, or "" when absent.
func remoteFile(t *testing.T, remote, rel string) string {
	t.Helper()
	out, err := gitInErr(remote, "--git-dir="+remote, "show", "main:"+rel)
	if err != nil {
		return ""
	}
	return out
}

// remoteHead returns the remote main tip's commit id.
func remoteHead(t *testing.T, remote string) string {
	t.Helper()
	return gitIn(t, remote, "--git-dir="+remote, "rev-parse", "main")
}

// pushFromScratch makes a commit on top of the remote's main from a scratch
// clone — what a PC running some other lw would have pushed.
func pushFromScratch(t *testing.T, remote, rel, body, msg string) {
	t.Helper()
	scratch := filepath.Join(t.TempDir(), "scratch")
	gitIn(t, filepath.Dir(scratch), "clone", "--quiet", "--branch", "main", remote, scratch)
	putFile(t, scratch, rel, body)
	gitIn(t, scratch, "add", "-A")
	gitIn(t, scratch, "commit", "--quiet", "-m", msg)
	gitIn(t, scratch, "push", "--quiet", "origin", "HEAD:refs/heads/main")
}

// syncPair returns PC A (the fixture vault, synced to a fresh remote with
// sync init) and PC B (a clone of it), both with the config sync init and
// sync clone wrote. The remote is a local bare repository path.
func syncPair(t *testing.T) (a, b *syncPC, remote string) {
	t.Helper()
	syncHermetic(t)
	remote = filepath.Join(syncTemp(t), "lw-vault")
	a, b = newSyncPC(t, "a"), newSyncPC(t, "b")
	a.useFixture()
	if out, errs, code := a.lw("sync", "init", remote, "--vault", a.root); code != 0 {
		t.Fatalf("sync init: exit %d\nstdout %q\nstderr %q", code, out, errs)
	}
	if out, errs, code := b.lw("sync", "clone", remote, b.root); code != 0 {
		t.Fatalf("sync clone: exit %d\nstdout %q\nstderr %q", code, out, errs)
	}
	return a, b, remote
}

// treeDigest hashes every file under root (path, mode and bytes), skipping
// the volatile per-PC directories named in skip. Two equal digests mean no
// file was created, removed or touched.
func treeDigest(t *testing.T, root string, skip ...string) string {
	t.Helper()
	h := sha256.New()
	var rels []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for _, s := range skip {
			if rel == s || strings.HasPrefix(rel, s+"/") {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if !d.IsDir() {
			rels = append(rels, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", root, err)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(h, rel+"\x00")
		h.Write(b)
		io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// pinSyncClock fixes the clock sync.json is stamped with.
func pinSyncClock(t *testing.T, at time.Time) {
	t.Helper()
	orig := syncNow
	syncNow = func() time.Time { return at }
	t.Cleanup(func() { syncNow = orig })
}

// captureCombined runs fn with stdout and stderr redirected into ONE pipe, so
// the order lines were printed in survives — the ordering test needs it.
func captureCombined(t *testing.T, fn func() int) (out string, code int) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	t.Cleanup(func() { os.Stdout, os.Stderr = origOut, origErr })
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	code = fn()
	w.Close()
	out = <-done
	os.Stdout, os.Stderr = origOut, origErr
	return out, code
}

// fakeSSHScript stands in for ssh: it logs its argv, drops ssh's options and
// the host, and runs the remaining command locally — which is what an ssh
// server's login shell does with it. FAKE_SSH_SLEEP makes it hang.
const fakeSSHScript = `#!/bin/sh
PATH="$(git --exec-path):$PATH"
LOG="$FAKE_SSH_LOG"
for a in "$@"; do if [ "$a" = "-G" ]; then LOG="$FAKE_SSH_LOG.G"; fi; done
{
  printf 'argv:'
  for a in "$@"; do printf ' [%s]' "$a"; done
  printf '\n'
} >> "$LOG"
G=
while [ $# -gt 0 ]; do
  case "$1" in
    --) shift; break ;;
    -G) G=1; shift ;;
    -o|-p|-i|-l|-F|-J|-L|-R|-D|-b|-c|-E|-e|-m|-O|-Q|-S|-W|-w) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
host="$1"
shift
if [ -n "$G" ]; then
  printf 'hostname %s\ncontrolpath %s\n' "$host" "${FAKE_SSH_CONTROLPATH:-none}"
  exit 0
fi
case "$host" in
  dead*) echo "ssh: Could not resolve hostname $host: Name or service not known" >&2; exit 255 ;;
esac
if [ -n "$FAKE_SSH_SLEEP" ] && { [ -z "$FAKE_SSH_SLEEP_HOST" ] || [ "$host" = "$FAKE_SSH_SLEEP_HOST" ]; }; then exec sleep "$FAKE_SSH_SLEEP"; fi
cd "$HOME" || exit 1
exec sh -c "$*"
`

// installFakeSSH puts the fake ssh first on PATH and returns its log path.
func installFakeSSH(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fakeSSHScript), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	t.Setenv("FAKE_SSH_LOG", logPath)
	t.Setenv("FAKE_SSH_SLEEP", "")
	t.Setenv("FAKE_SSH_SLEEP_HOST", "")
	t.Setenv("FAKE_SSH_CONTROLPATH", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// mustLoadConfig loads the config of the PC that is acting.
func mustLoadConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// readFileString returns a file's text.
func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

package vaultsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// defaultTimeout bounds one network git call when Options.Timeout is 0.
	defaultTimeout = 30 * time.Second

	// batchOpts is appended to the user's ssh command (plain "ssh" when they
	// have none) for a non-interactive call: never ask a question (BatchMode)
	// and give up on an unreachable host in 5 s, so a laptop on a train does
	// not hang `lw tui` at startup (042 D3).
	batchOpts = " -o BatchMode=yes -o ConnectTimeout=5"

	// killGrace is how long a timed-out or cancelled child has to exit on
	// SIGTERM — git removes its own .lock files on it — before SIGKILL.
	killGrace = 1500 * time.Millisecond

	// waitDelay bounds how long a finished or killed git may keep its pipes
	// open through a grandchild (git spawns ssh, ssh spawns a ProxyCommand).
	waitDelay = 2 * time.Second
)

// gitFlags are passed to every git call, ahead of the subcommand, after the
// lw identity gitCall adds (042 D3). commit.gpgsign=false and
// core.quotepath=off are the spec: commits are never signed (a global
// commit.gpgsign=true with no key would otherwise fail every CommitWork) and
// file names are printed raw. The rest keep the vault's bytes and the call's
// timing predictable on a machine whose global git config was tuned for code,
// not notes:
//   - core.autocrlf=false: a global autocrlf=input would normalise CRLF in
//     an immutable raw source on add, and the other PC would check out
//     bytes whose sha no longer matches the raw's frontmatter.
//   - core.hooksPath=/dev/null: a vault commit never runs the user's global
//     hooks (a pre-commit linter, a husky install).
//   - core.excludesFile=/dev/null (and the XDG ~/.config/git/ignore it
//     replaces) and core.attributesFile=/dev/null: the managed .gitignore is
//     the only thing that may keep a vault file out of a sync, and the
//     bytes of a raw source must reach the other PC untouched. A user's
//     global "*.pdf" or "* text=auto" would otherwise drop or rewrite them
//     silently. CommitWork also writes .git/info/attributes (see
//     ensureAttributes) to outrank a .gitattributes inside the vault.
//   - push.gpgSign=false: a global push.gpgSign=true fails every push to a
//     server that cannot verify a signature.
//   - gc.autoDetach=false, maintenance.autoDetach=false: an automatic gc
//     runs in the foreground when it runs at all, so no background process
//     outlives the call and holds its pipes or the temp dir.
var gitFlags = []string{
	"-c", "commit.gpgsign=false",
	"-c", "core.quotepath=off",
	"-c", "core.autocrlf=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.excludesFile=/dev/null",
	"-c", "core.attributesFile=/dev/null",
	"-c", "push.gpgSign=false",
	"-c", "gc.autoDetach=false",
	"-c", "maintenance.autoDetach=false",
}

// scrubbed are the environment variables that would redirect git away from
// Options.Dir or override the lw identity. A `make check` run from a git
// hook inherits GIT_DIR and GIT_INDEX_FILE; a developer's shell may export
// GIT_AUTHOR_NAME. Dates are left alone so a caller can pin commit times.
var scrubbed = map[string]bool{
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_INDEX_FILE":                   true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_NAMESPACE":                    true,
	"GIT_PREFIX":                       true,
	"GIT_AUTHOR_NAME":                  true,
	"GIT_AUTHOR_EMAIL":                 true,
	"GIT_COMMITTER_NAME":               true,
	"GIT_COMMITTER_EMAIL":              true,
}

// runner executes git (and ssh) for one Options value.
type runner struct {
	o   Options
	git string // the resolved git binary

	ssh string // the user's ssh command line, once looked up
}

// newRunner validates o and finds git. Dir is made absolute so every later
// path agrees on one spelling.
func newRunner(o Options) (*runner, error) {
	if o.Dir == "" {
		return nil, errors.New("vaultsync: Options.Dir is empty")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrNoGit
		}
		return nil, fmt.Errorf("vaultsync: find git: %w", err)
	}
	abs, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("vaultsync: %w", err)
	}
	o.Dir = abs
	return &runner{o: o, git: gitPath}, nil
}

// commitHost is the machine name in the commit identity lw@<host>.
func commitHost() string {
	h, err := os.Hostname()
	if err != nil {
		return "localhost"
	}
	return sanitizeHost(h)
}

// sanitizeHost replaces every byte git would reject in an email (and every
// space) with "-"; "localhost" when nothing is left.
func sanitizeHost(h string) string {
	h = hostBad.ReplaceAllString(strings.TrimSpace(h), "-")
	if h == "" {
		return "localhost"
	}
	return h
}

var hostBad = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// timeout is the bound on one network call when !Interactive.
func (r *runner) timeout() time.Duration {
	if r.o.Timeout > 0 {
		return r.o.Timeout
	}
	return defaultTimeout
}

// env builds the child environment: the caller's, minus the variables that
// redirect git, with git's repository search stopped at the vault's parent,
// plus the non-interactive settings when !Interactive, plus extra (KEY=VALUE,
// last wins). sshCmd, when not empty, becomes GIT_SSH_COMMAND.
//
// GIT_CEILING_DIRECTORIES: a vault whose .git is empty or invalid would
// otherwise make git walk up, find the repository the vault happens to sit
// in, and commit that repository's files (M1 of the S1 review). With the
// ceiling git looks at the vault itself and fails closed.
func (r *runner) env(extra []string, sshCmd string) []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !scrubbed[name] {
			out = append(out, kv)
		}
	}
	out = setEnv(out, "GIT_CEILING_DIRECTORIES="+filepath.Dir(r.o.Dir))
	if !r.o.Interactive {
		out = setEnv(out, "GIT_TERMINAL_PROMPT=0")
	}
	if sshCmd != "" {
		out = setEnv(out, "GIT_SSH_COMMAND="+sshCmd)
	}
	for _, kv := range extra {
		out = setEnv(out, kv)
	}
	return out
}

// sshCommand is the ssh command line git would use: GIT_SSH_COMMAND, else
// core.sshCommand, else plain ssh. A non-interactive call appends batchOpts
// to it instead of replacing it, so a user's key, jump host or config file
// survives (M4 of the S1 review); Init's own ssh call runs the same command.
func (r *runner) sshCommand(ctx context.Context) string {
	if r.ssh != "" {
		return r.ssh
	}
	cmd := strings.TrimSpace(os.Getenv("GIT_SSH_COMMAND"))
	if cmd == "" {
		_, statErr := os.Stat(r.o.Dir)
		out, err := r.proc(ctx, r.git, call{args: []string{"config", "--get", "core.sshCommand"}, noDir: statErr != nil})
		if err == nil {
			cmd = strings.TrimSpace(out)
		}
	}
	if cmd == "" {
		cmd = "ssh"
	}
	r.ssh = cmd
	return cmd
}

func setEnv(env []string, kv string) []string {
	prefix := kv[:strings.Index(kv, "=")+1]
	out := env[:0:0]
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, kv)
}

// call describes one child process.
type call struct {
	args  []string
	net   bool     // a network call: bounded by Timeout when !Interactive; stderr streams to Options.Stderr when Interactive
	env   []string // extra KEY=VALUE
	noDir bool     // do not run inside the vault (clone creates it)
}

// cmdError is a failed child process. Error() is the one-line reason a user
// can act on, not git's whole stderr.
type cmdError struct {
	stderr  string
	err     error
	timeout time.Duration // non-zero: killed after this long
}

func (e *cmdError) Error() string {
	if e.timeout > 0 {
		return fmt.Sprintf("timed out after %s", e.timeout)
	}
	if msg := tidy(e.stderr); msg != "" {
		return msg
	}
	return e.err.Error()
}

func (e *cmdError) Unwrap() error { return e.err }

// exitCode is the child's exit status, or -1 when it did not exit normally.
func (e *cmdError) exitCode() int {
	var ee *exec.ExitError
	if errors.As(e.err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// boilerplate are the lines git adds after every transport failure; they
// say nothing the line above did not.
var boilerplate = []string{
	"Could not read from remote repository",
	"Please make sure you have the correct access rights",
	"and the repository exists",
}

// progressLine matches the transfer progress git prints with --progress
// ("Writing objects:  45% (9/20)", "remote: Total 3 (delta 1)", ", done.").
var progressLine = regexp.MustCompile(`\d+% \(\d+/\d+\)|, done\.$|^(remote: )?(Enumerating|Counting|Compressing|Writing|Receiving|Resolving|Total) `)

// tidy folds a stderr blob into one line: blank, progress and boilerplate
// lines gone, "fatal: " dropped, the rest joined by spaces. Progress ends in
// \r, not \n, so both split a line.
func tidy(stderr string) string {
	var keep []string
lines:
	for _, line := range strings.FieldsFunc(stderr, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if line == "" || progressLine.MatchString(line) {
			continue
		}
		for _, b := range boilerplate {
			if strings.Contains(line, b) {
				continue lines
			}
		}
		keep = append(keep, strings.TrimPrefix(line, "fatal: "))
	}
	return strings.Join(keep, " ")
}

// proc runs bin with c, returning stdout.
func (r *runner) proc(ctx context.Context, bin string, c call) (string, error) {
	out, _, err := r.procErr(ctx, bin, c)
	return out, err
}

// procErr is proc that also returns stderr, which a successful child may use
// to say why it did not do what was asked.
//
// A cancelled or timed-out child is sent SIGTERM and, killGrace later,
// SIGKILL: git removes its own .lock files on SIGTERM, whereas SIGKILL leaves
// refs/remotes/lw/main.lock behind and every later sync fails on it (M2). A
// non-interactive network call runs in its own process group so the signals
// reach ssh and its ProxyCommand too; an interactive one cannot (a background
// group may not read the tty ssh prompts on, SIGTTIN), so git and every
// descendant found at that moment are signalled one by one.
func (r *runner) procErr(ctx context.Context, bin string, c call) (string, string, error) {
	timed := c.net && !r.o.Interactive
	pctx := ctx
	if timed {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, r.timeout())
		defer cancel()
	}
	sshCmd := ""
	if timed {
		sshCmd = r.sshCommand(ctx) + batchOpts
	}
	cmd := exec.CommandContext(pctx, bin, c.args...)
	if !c.noDir {
		cmd.Dir = r.o.Dir
	}
	cmd.Env = r.env(c.env, sshCmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if c.net && r.o.Interactive && r.o.Stderr != nil {
		cmd.Stderr = io.MultiWriter(&stderr, r.o.Stderr)
	} else {
		cmd.Stderr = &stderr
	}
	cmd.WaitDelay = waitDelay
	if timed {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	var (
		mu   sync.Mutex // Cancel runs on another goroutine
		done bool       // the child has been reaped: its pid may be recycled
	)
	cmd.Cancel = func() error {
		leader := cmd.Process.Pid
		var pids []int // everything to signal, leader first
		if timed {
			pids = []int{-leader} // the whole group
		} else {
			pids = append([]int{leader}, descendants(leader)...)
		}
		signalled := false
		for _, pid := range pids {
			if err := syscall.Kill(pid, syscall.SIGTERM); err == nil {
				signalled = true
			}
		}
		if !signalled {
			return os.ErrProcessDone
		}
		time.AfterFunc(killGrace, func() {
			mu.Lock()
			defer mu.Unlock()
			for i, pid := range pids {
				if i == 0 && done && !timed {
					continue // the leader is gone; its pid is not ours any more
				}
				syscall.Kill(pid, syscall.SIGKILL) // ESRCH when already gone
			}
		})
		return nil
	}
	err := cmd.Run()
	mu.Lock()
	done = true
	mu.Unlock()
	// A grandchild that outlives its parent while holding the pipes (an ssh
	// ControlPersist master, a ProxyCommand) makes Wait give up after
	// waitDelay with ErrWaitDelay even though git exited 0. Its work is done
	// and everything it printed was read, so that is success, not failure.
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		err = nil
	}
	if err == nil {
		return stdout.String(), stderr.String(), nil
	}
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	if timed && errors.Is(pctx.Err(), context.DeadlineExceeded) {
		return "", "", &cmdError{err: err, timeout: r.timeout()}
	}
	return stdout.String(), stderr.String(), &cmdError{stderr: stderr.String(), err: err}
}

// gitCall runs one git call with the lw identity and flags and returns stdout.
func (r *runner) gitCall(ctx context.Context, c call) (string, error) {
	args := append([]string{
		"-c", "user.name=lw",
		"-c", "user.email=lw@" + commitHost(),
	}, gitFlags...)
	c.args = append(args, c.args...)
	out, err := r.proc(ctx, r.git, c)
	var ce *cmdError
	if errors.As(err, &ce) {
		if p := r.lockPath(ce.stderr); p != "" {
			return out, &lockError{path: p}
		}
	}
	return out, err
}

// lockError is a stale .lock file in the vault's .git: a previous sync was
// killed between creating it and removing it, and git will not proceed until
// it is gone.
type lockError struct{ path string }

func (e *lockError) Error() string {
	return "a previous sync was interrupted and left " + e.path + "; remove it and run lw sync again"
}

var (
	lockRe     = regexp.MustCompile(`Unable to create '([^']+\.lock)'`)
	fetchRefRe = regexp.MustCompile(`fetching ref (\S+) failed: reference already exists`)
)

// lockPath finds the stale lock a git failure complains about, or "". Two
// shapes: "Unable to create '<path>.lock': File exists" (index, update-ref)
// and, for a fetch that cannot take refs/remotes/lw/main.lock, "fetching ref
// <ref> failed: reference already exists". Only a lock inside this vault's own
// repository counts — a lock on the remote's side is the remote's business.
func (r *runner) lockPath(stderr string) string {
	var path string
	if m := lockRe.FindStringSubmatch(stderr); m != nil {
		path = m[1]
	} else if m := fetchRefRe.FindStringSubmatch(stderr); m != nil {
		guess := filepath.Join(r.o.Dir, ".git", filepath.FromSlash(m[1])+".lock")
		if _, err := os.Stat(guess); err == nil {
			path = guess
		}
	}
	if path == "" {
		return ""
	}
	for _, root := range []string{r.o.Dir, realPath(r.o.Dir)} {
		if strings.HasPrefix(path, root+string(filepath.Separator)) {
			return path
		}
	}
	return ""
}

// realPath resolves symlinks, falling back to p itself.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// out runs a local git call and returns its trimmed stdout. A failure is
// wrapped with the subcommand's name.
func (r *runner) out(ctx context.Context, args ...string) (string, error) {
	s, err := r.gitCall(ctx, call{args: args})
	if err != nil {
		return "", wrap(args[0], err)
	}
	return strings.TrimSpace(s), nil
}

// wrap names the git subcommand in a failure, leaving context errors bare.
func wrap(verb string, err error) error {
	var le *lockError
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &le) {
		return err
	}
	return fmt.Errorf("git %s: %w", verb, err)
}

// isRepo reports whether Dir is the root of its own git work tree. A vault
// that merely sits inside some other repository is not: `git add -A` there
// would stage the other repository's files.
func (r *runner) isRepo() bool {
	_, err := os.Stat(filepath.Join(r.o.Dir, ".git"))
	return err == nil
}

// hasHead reports whether HEAD names a commit.
func (r *runner) hasHead(ctx context.Context) bool {
	_, err := r.gitCall(ctx, call{args: []string{"rev-parse", "--verify", "--quiet", "HEAD"}})
	return err == nil
}

// hasRef reports whether ref exists.
func (r *runner) hasRef(ctx context.Context, ref string) bool {
	_, err := r.gitCall(ctx, call{args: []string{"rev-parse", "--verify", "--quiet", ref}})
	return err == nil
}

// requireRepo is the precondition of every operation except Init and Clone:
// the vault is a git work tree with at least one commit.
func (r *runner) requireRepo(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err // a cancelled call is not "not a repo"
	}
	if !r.isRepo() || !r.hasHead(ctx) {
		return ErrNotRepo
	}
	return nil
}

// descendants lists the pids below pid, children first, using pgrep -P (on
// Linux and macOS alike). Without pgrep it finds none.
func descendants(pid int) []int {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	var all []int
	for _, f := range strings.Fields(string(out)) {
		child, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		all = append(all, child)
		all = append(all, descendants(child)...)
	}
	return all
}

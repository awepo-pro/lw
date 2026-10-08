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
	"strings"
	"syscall"
	"time"
)

const (
	// defaultTimeout bounds one network git call when Options.Timeout is 0.
	defaultTimeout = 30 * time.Second

	// batchSSH is GIT_SSH_COMMAND for a non-interactive call: never ask a
	// question (BatchMode) and give up on an unreachable host in 5 s, so a
	// laptop on a train does not hang `lw tui` at startup (042 D3).
	batchSSH = "ssh -o BatchMode=yes -o ConnectTimeout=5"

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
//   - gc.autoDetach=false, maintenance.autoDetach=false: an automatic gc
//     runs in the foreground when it runs at all, so no background process
//     outlives the call and holds its pipes or the temp dir.
var gitFlags = []string{
	"-c", "commit.gpgsign=false",
	"-c", "core.quotepath=off",
	"-c", "core.autocrlf=false",
	"-c", "core.hooksPath=/dev/null",
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
// redirect git, plus the non-interactive pair when !Interactive, plus extra
// (KEY=VALUE, last wins).
func (r *runner) env(extra []string) []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !scrubbed[name] {
			out = append(out, kv)
		}
	}
	if !r.o.Interactive {
		out = setEnv(out, "GIT_TERMINAL_PROMPT=0")
		out = setEnv(out, "GIT_SSH_COMMAND="+batchSSH)
	}
	for _, kv := range extra {
		out = setEnv(out, kv)
	}
	return out
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

// proc runs bin with c, returning stdout. A network call that outlives the
// timeout is killed with its whole process group (git's ssh child would
// otherwise survive it) and reported as a timeout; a cancelled ctx wins over
// both.
func (r *runner) proc(ctx context.Context, bin string, c call) (string, error) {
	timed := c.net && !r.o.Interactive
	pctx := ctx
	if timed {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, r.timeout())
		defer cancel()
	}
	cmd := exec.CommandContext(pctx, bin, c.args...)
	if !c.noDir {
		cmd.Dir = r.o.Dir
	}
	cmd.Env = r.env(c.env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if c.net && r.o.Interactive && r.o.Stderr != nil {
		cmd.Stderr = io.MultiWriter(&stderr, r.o.Stderr)
	} else {
		cmd.Stderr = &stderr
	}
	cmd.WaitDelay = waitDelay
	if timed {
		// A new group so the kill below reaches ssh too. Not for an
		// interactive call: a background group cannot read the tty that
		// ssh prompts on (SIGTTIN).
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			return nil
		}
	}
	err := cmd.Run()
	// A grandchild that outlives its parent while holding the pipes (an ssh
	// ControlPersist master, a ProxyCommand) makes Wait give up after
	// waitDelay with ErrWaitDelay even though git exited 0. Its work is done
	// and everything it printed was read, so that is success, not failure.
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		err = nil
	}
	if err == nil {
		return stdout.String(), nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if timed && errors.Is(pctx.Err(), context.DeadlineExceeded) {
		return "", &cmdError{err: err, timeout: r.timeout()}
	}
	return stdout.String(), &cmdError{stderr: stderr.String(), err: err}
}

// gitCall runs one git call with the lw identity and flags and returns stdout.
func (r *runner) gitCall(ctx context.Context, c call) (string, error) {
	args := append([]string{
		"-c", "user.name=lw",
		"-c", "user.email=lw@" + commitHost(),
	}, gitFlags...)
	c.args = append(args, c.args...)
	return r.proc(ctx, r.git, c)
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
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
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

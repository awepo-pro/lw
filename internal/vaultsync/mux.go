package vaultsync

// mux.go makes every ssh connection lw sync opens share one authenticated
// session (042 S3c). A sync step is up to three ssh connects — the fetch, the
// push, Init's own probe — and through a Cloudflare Access tunnel each connect
// costs seconds (5.3 s measured; a multiplexed one 0.85 s), which made a plain
// `lw note` take 17 s. OpenSSH can reuse one connection for all of them:
// ControlMaster=auto starts a master in the background the first time,
// ControlPath says where its socket lives, ControlPersist keeps it alive for
// ten minutes after the last use, so the next lw verb — not just the next
// step — connects in a fraction of the time.
//
// It is added to the user's own ssh command line (the S1c prefix rule), for
// interactive and non-interactive calls alike, and only when the user has not
// done it themselves: `ssh -G <host>` prints the configuration ssh would use
// for that host, locally, with no network, and a controlpath other than none
// there means their ~/.ssh/config already multiplexes (or deliberately does
// not), which is theirs to keep.

import (
	"context"
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
	// muxPersist is ControlPersist, in seconds: how long the background master
	// outlives its last client.
	muxPersist = 600

	// controlSocketLen is the longest a socket name under the control directory
	// gets: "/", the 40 hex characters %C expands to (a SHA-1 of local host,
	// remote host, port and user), and the 17 ssh appends while it binds the
	// socket before renaming it into place (".XXXXXXXXXXXXXXXX").
	controlSocketLen = 1 + 40 + 17

	// maxControlPath is the longest bound socket path accepted. A unix socket
	// path must fit sun_path — 104 bytes on macOS, 108 on Linux — and ssh
	// refuses to start a master whose path is longer ("ControlPath too long");
	// 100 keeps a margin.
	maxControlPath = 100

	// muxAliveInterval and muxAliveCount are ServerAliveInterval and
	// ServerAliveCountMax; muxInteractiveConnect is the ConnectTimeout an
	// interactive call gets (a non-interactive one has batchOpts').
	muxAliveInterval      = 15
	muxAliveCount         = 2
	muxInteractiveConnect = 10

	// sshExitTimeout bounds `ssh -O exit`.
	sshExitTimeout = 3 * time.Second

	// sshGTimeout bounds `ssh -G`. It touches no network, so only a broken
	// ssh makes it slow.
	sshGTimeout = 5 * time.Second
)

// The directories the control sockets may live in, and who owns them, are
// variables for the same reason newAgent is one in cmd/lw: a test has to make a
// path too long, or a cache directory that is not there, without a machine
// that has one.
var (
	userCacheDir = os.UserCacheDir
	tempDir      = os.TempDir
	getuid       = os.Getuid
)

// controlDir returns the directory for control sockets, created and owned by
// this user with mode 0700, or false when there is none that fits. The user's
// cache directory comes first (lw/ssh under it); when the socket path there
// would be too long for sun_path the temp directory is tried (lw-ssh-<uid>);
// when that is too long as well there is no multiplexing, which only costs
// speed.
func controlDir() (string, bool) {
	var candidates []string
	if base, err := userCacheDir(); err == nil && base != "" {
		candidates = append(candidates, filepath.Join(base, "lw", "ssh"))
	}
	candidates = append(candidates, filepath.Join(tempDir(), "lw-ssh-"+strconv.Itoa(getuid())))
	for _, dir := range candidates {
		if len(dir)+controlSocketLen > maxControlPath {
			continue
		}
		if privateDir(dir) {
			return dir, true
		}
	}
	return "", false
}

// privateDir makes dir (and its parents) if need be and reports whether it is
// a real directory — not a symlink — that belongs to this user and is closed
// to everyone else, tightening the mode to 0700 when it is looser. The check is
// what makes a predictable name in a shared /tmp safe: another user who got
// there first, or who planted a link, would otherwise be handed our ssh
// sessions.
func privateDir(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	info, err := os.Lstat(dir) // Lstat: a symlink to a directory is not a directory here
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != getuid() {
		return false
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return false
		}
	}
	return true
}

// sshConfigAnswers caches, for the life of the process, what `ssh -G` said
// about a destination: whether the user's own configuration sets a ControlPath.
// The answer cannot change between the steps of one verb, and a TUI that lives
// for hours should not run ssh -G before every push.
var sshConfigAnswers = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// resetSSHConfigAnswers forgets what ssh -G said; tests change the fake ssh.
func resetSSHConfigAnswers() {
	sshConfigAnswers.Lock()
	defer sshConfigAnswers.Unlock()
	sshConfigAnswers.m = map[string]bool{}
}

// controlPathLine matches the line of `ssh -G` output that names the control
// path. ssh prints option names in lower case.
var controlPathLine = regexp.MustCompile(`(?m)^controlpath[ \t]+(\S.*)$`)

// sshGAnswer recognises a real `ssh -G` answer: it always names the resolved
// hostname. Output without it (a wrapper that prints nothing useful) is not
// trusted to mean "no ControlPath".
var sshGAnswer = regexp.MustCompile(`(?m)^hostname[ \t]+\S`)

// userMultiplexes reports whether the user's effective ssh configuration for
// dest already sets a ControlPath (anything but none). `ssh -G` is run with the
// user's own ssh command, so a -F config file or -o options in their
// GIT_SSH_COMMAND count. When ssh cannot say — it is missing, it predates -G
// (before OpenSSH 6.8), or its answer is not a real -G answer (no hostname line) — the answer is
// "yes": adding nothing is the safe way to fail.
func (r *runner) userMultiplexes(ctx context.Context, dest, port string) bool {
	base := r.sshCommand(ctx)
	key := base + "\x00" + dest + "\x00" + port
	if base == "ssh" {
		if p, err := exec.LookPath("ssh"); err == nil {
			key = p + "\x00" + dest + "\x00" + port // the same name may mean another ssh
		}
	}
	sshConfigAnswers.Lock()
	if v, ok := sshConfigAnswers.m[key]; ok {
		sshConfigAnswers.Unlock()
		return v
	}
	sshConfigAnswers.Unlock()

	args := []string{"-G"}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, dest)
	gctx, cancel := context.WithTimeout(ctx, sshGTimeout)
	defer cancel()
	var out string
	var err error
	if base != "ssh" {
		out, _, err = r.procErr(gctx, "sh", call{args: append([]string{"-c", base + ` "$@"`, "sh"}, args...), noDir: true})
	} else {
		out, _, err = r.procErr(gctx, "ssh", call{args: args, noDir: true})
	}
	multiplexes := true
	if err == nil {
		if m := controlPathLine.FindStringSubmatch(out); m != nil {
			multiplexes = strings.TrimSpace(m[1]) != "none"
		} else if sshGAnswer.MatchString(out) {
			// Real OpenSSH (checked against 10.x, 2026-10-09) omits the
			// controlpath line when none is set, so a well-formed answer
			// without it means "not multiplexing" — reading it as "yes" left
			// every Cloudflare sync at one 5 s connect per git call.
			multiplexes = false
		}
	} else if ctx.Err() != nil {
		return true // cancelled: say nothing, and cache nothing
	}
	sshConfigAnswers.Lock()
	sshConfigAnswers.m[key] = multiplexes
	sshConfigAnswers.Unlock()
	return multiplexes
}

// muxArgs returns the ssh options that multiplex connections to dest, or nil
// when they should not be added: the user's configuration has a ControlPath,
// GIT_SSH names a program lw does not drive, or there is no control directory
// that fits.
func (r *runner) muxArgs(ctx context.Context, dest, port string) []string {
	if dest == "" || (os.Getenv("GIT_SSH") != "" && strings.TrimSpace(os.Getenv("GIT_SSH_COMMAND")) == "") {
		return nil
	}
	if r.userMultiplexes(ctx, dest, port) {
		return nil
	}
	dir, ok := controlDir()
	if !ok {
		return nil
	}
	args := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(dir, "%C"),
		"-o", "ControlPersist=" + strconv.Itoa(muxPersist),
		// A master whose connection died (a laptop that slept, a tunnel that
		// dropped) must not hold every later call hostage: ssh notices in about
		// 2 x 15 s instead of never (A-042-9 e).
		"-o", "ServerAliveInterval=" + strconv.Itoa(muxAliveInterval),
		"-o", "ServerAliveCountMax=" + strconv.Itoa(muxAliveCount),
	}
	if r.o.Interactive {
		// The batch options bound the connect for a non-interactive call; an
		// interactive one had none, and a stopped master would hang it.
		args = append(args, "-o", "ConnectTimeout="+strconv.Itoa(muxInteractiveConnect))
	}
	return args
}

// exitMaster asks the control master for dest to exit (`ssh -O exit`). A call
// through it that timed out means the master is stuck or dead; leaving it
// would make the next call hang on it too, whereas a fresh call starts a new
// one. It is best effort and bounded, and it only touches the socket lw made
// for this destination.
func (r *runner) exitMaster(dest, port string) {
	dir, ok := controlDir()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sshExitTimeout)
	defer cancel()
	args := []string{"-O", "exit", "-o", "ControlPath=" + filepath.Join(dir, "%C")}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, dest)
	if base := r.sshCommand(ctx); base != "ssh" {
		r.procErr(ctx, "sh", call{args: append([]string{"-c", base + ` "$@"`, "sh"}, args...), noDir: true})
		return
	}
	r.procErr(ctx, "ssh", call{args: args, noDir: true})
}

// muxOpts is muxArgs as text to append to a command line that sh will parse
// (GIT_SSH_COMMAND): a leading space, each word shell-quoted if it needs it.
func (r *runner) muxOpts(ctx context.Context, dest, port string) string {
	var b strings.Builder
	for _, a := range r.muxArgs(ctx, dest, port) {
		b.WriteByte(' ')
		b.WriteString(shellWord(a))
	}
	return b.String()
}

var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWord quotes s for sh unless it is made only of characters sh leaves
// alone.
func shellWord(s string) string {
	if safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

package vaultsync

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// kind says how a remote is reached.
type kind int

const (
	kindLocal kind = iota // a directory path or file:// URL on this machine
	kindSSH               // scp-like host:path or an ssh:// URL
	kindOther             // any other URL git understands (https://, git://)
)

// remote is a parsed remote spec.
type remote struct {
	raw  string // exactly what the caller wrote; names the remote in State and errors
	kind kind
	dest string // kindSSH: the ssh destination, [user@]host
	port string // kindSSH from an ssh:// URL
	path string // the path part as written (kindLocal: before ~ expansion); what Init validates
	arg  string // what is handed to git: raw, or the absolute path for a bare local path
}

// parseRemote classifies spec the way git does: a URL has "://"; otherwise a
// ':' before any '/' makes it scp-like host:path; anything else is a local
// path (042 D3 — a mounted NAS or a USB drive is a real remote). A spec that
// starts with "-" is refused outright: it would reach git or ssh as an
// option (the classic ssh://-oProxyCommand=... attack).
func parseRemote(spec string) (remote, error) {
	if spec == "" {
		return remote{}, errors.New("empty remote")
	}
	if strings.HasPrefix(spec, "-") {
		return remote{}, errors.New("must not start with \"-\"")
	}
	rem := remote{raw: spec, arg: spec}
	switch {
	case strings.HasPrefix(spec, "file://"):
		u, err := url.Parse(spec)
		if err != nil {
			return remote{}, err
		}
		rem.kind, rem.path = kindLocal, u.Path
	case strings.HasPrefix(spec, "ssh://"):
		u, err := url.Parse(spec)
		if err != nil {
			return remote{}, err
		}
		host := u.Hostname()
		if host == "" || strings.HasPrefix(host, "-") {
			return remote{}, errors.New("bad host")
		}
		rem.kind, rem.dest, rem.port = kindSSH, host, u.Port()
		if u.User != nil && u.User.Username() != "" {
			rem.dest = u.User.Username() + "@" + host
		}
		// ssh://host/~/dir means the login directory's dir; git sends the
		// path without the leading slash, and so does Init's script.
		rem.path = u.Path
		if strings.HasPrefix(rem.path, "/~") {
			rem.path = rem.path[1:]
		}
	case strings.Contains(spec, "://"):
		rem.kind = kindOther
	default:
		if dest, path, ok := splitSCP(spec); ok {
			rem.kind, rem.dest, rem.path = kindSSH, dest, path
			break
		}
		rem.kind, rem.path = kindLocal, spec
		abs, err := absLocal(spec)
		if err != nil {
			return remote{}, err
		}
		rem.arg = abs
	}
	return rem, nil
}

// sshDest is the ssh destination of an ssh remote, "" for any other kind.
func (rem remote) sshDest() string {
	if rem.kind == kindSSH {
		return rem.dest
	}
	return ""
}

// splitSCP splits "host:path" (git's scp-like syntax) at the colon, which
// must come before any slash. [::1]:path keeps IPv6 hosts workable.
func splitSCP(spec string) (dest, path string, ok bool) {
	if strings.HasPrefix(spec, "[") {
		if i := strings.Index(spec, "]:"); i > 1 {
			return spec[1:i], spec[i+2:], true
		}
		return "", "", false
	}
	colon := strings.Index(spec, ":")
	if colon <= 0 {
		return "", "", false
	}
	if slash := strings.Index(spec, "/"); slash >= 0 && slash < colon {
		return "", "", false
	}
	return spec[:colon], spec[colon+1:], true
}

// absLocal makes a local remote path absolute: ~ and ~/ expand to the home
// directory (a config file's remotes = ["~/vault.git"] is not seen by a
// shell) and a relative path is taken from the process's directory, not from
// the vault git runs in.
func absLocal(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}

// pathRe is the only alphabet a remote path may use for Init: the path is
// pasted into a command line run by the server's login shell, so anything
// with shell meaning is refused instead of quoted (042 D3).
var pathRe = regexp.MustCompile(`^[A-Za-z0-9._/~-]+$`)

// validatePath is the rule above, with its byte-exact message, plus one more:
// a path that starts with "-" would be an option to the remote's sh or git
// (host:-x).
func (rem remote) validatePath() error {
	if !pathRe.MatchString(rem.path) {
		return fmt.Errorf("remote path %q: only letters, digits and . _ / ~ - are allowed", rem.path)
	}
	if strings.HasPrefix(rem.path, "-") {
		return fmt.Errorf("remote path %q: must not start with \"-\"", rem.path)
	}
	return nil
}

// remoteScript is what Init runs where the remote repository lives (over ssh
// for a host:path, in a local sh for a path). $1 is the path. It prints one
// word and exits 0 when it reached a verdict:
//
//	created      the path was missing or an empty dir: a bare repo now exists
//	empty        an existing bare repo with no commit: accepted
//	has-commits  an existing bare repo that already holds history
//	refused      anything else
//
// It contains no single quote so it can be wrapped in one for the remote
// shell, and no "!" so no login shell's history expansion can touch it. HEAD
// of an accepted empty repo is pointed at main so a plain `git clone` works.
const remoteScript = `p=$1
if [ -e "$p" ]; then
  if [ -d "$p" ] && [ -z "$(ls -A -- "$p" 2>/dev/null)" ]; then
    git init --bare -b main -- "$p" >/dev/null || exit 1
    echo created
    exit 0
  fi
  if [ "$(git --git-dir="$p" rev-parse --is-bare-repository 2>/dev/null)" = true ]; then
    if [ -n "$(git --git-dir="$p" rev-list -n 1 --all 2>/dev/null)" ]; then
      echo has-commits
      exit 0
    fi
    git --git-dir="$p" symbolic-ref HEAD refs/heads/main || exit 1
    echo empty
    exit 0
  fi
  echo refused
  exit 0
fi
git init --bare -b main -- "$p" >/dev/null || exit 1
echo created
`

// ensureRemote makes sure rem is a bare repo Init may push to: it creates
// one where the path is missing or empty, accepts a bare repo without
// commits, and refuses the rest with the spec's texts. A failure to reach
// the remote at all is a *RemoteError, like every other network step.
func (r *runner) ensureRemote(ctx context.Context, rem remote) error {
	var (
		out, stderr string
		err         error
	)
	switch rem.kind {
	case kindLocal:
		abs, aerr := absLocal(rem.path)
		if aerr != nil {
			return aerr
		}
		out, stderr, err = r.procErr(ctx, "sh", call{args: []string{"-c", remoteScript, "sh", abs}})
	case kindSSH:
		var args []string
		if !r.o.Interactive {
			args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5")
		}
		args = append(args, r.muxArgs(ctx, rem.dest, rem.port)...)
		if rem.port != "" {
			args = append(args, "-p", rem.port)
		}
		// One argument: ssh joins the rest with spaces anyway, and the
		// remote login shell parses it. The path is regex-safe, and unquoted
		// so the shell expands a leading ~.
		args = append(args, "--", rem.dest, "sh -c '"+remoteScript+"' sh "+rem.path)
		out, stderr, err = r.runSSH(ctx, args)
	default:
		return fmt.Errorf("remote %s: lw sync init needs an ssh or local path remote", rem.raw)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &RemoteError{Tried: []string{rem.raw}, Errs: []error{err}}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	switch verdict := strings.TrimSpace(lines[len(lines)-1]); verdict {
	case "created", "empty":
		return nil
	case "has-commits":
		return fmt.Errorf("remote %s already holds a vault — use lw sync clone", rem.raw)
	case "refused":
		return fmt.Errorf("remote %s is not empty and not a bare git repo", rem.raw)
	default:
		// Whatever the remote said besides the verdict (a login banner, a
		// missing git) is the only clue to why there is none.
		msg := fmt.Sprintf("unexpected reply from the remote: %q", verdict)
		if detail := tidy(stderr); detail != "" {
			msg += " (stderr: " + detail + ")"
		}
		return &RemoteError{Tried: []string{rem.raw}, Errs: []error{errors.New(msg)}}
	}
}

// runSSH runs the user's ssh command (see sshCommand) with args. Plain "ssh" is
// executed directly; anything else is a command line for sh, which keeps the
// quoting the user wrote in GIT_SSH_COMMAND or core.sshCommand.
func (r *runner) runSSH(ctx context.Context, args []string) (stdout, stderr string, err error) {
	if base := r.sshCommand(ctx); base != "ssh" {
		return r.procErr(ctx, "sh", call{args: append([]string{"-c", base + ` "$@"`, "sh"}, args...), net: true})
	}
	return r.procErr(ctx, "ssh", call{args: args, net: true})
}

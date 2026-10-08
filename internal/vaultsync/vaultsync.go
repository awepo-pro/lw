// Package vaultsync moves a vault between PCs through a storage-only git
// remote (042, remote vault). Each PC keeps a full local vault; `lw sync`
// commits the vault's state to git and exchanges it with a bare repo on a
// server that has nothing but sshd and git — no lw, no API key. git gives
// the three properties the job needs for free: an atomic remote update, a
// refusal when the remote moved, and transport over the user's own ssh.
//
// lw shells out to the user's git and ssh. It links no git library and
// stores no credentials; ssh does the authentication. Divergence (both
// sides committed since the last sync) is detected and refused, never
// resolved: every operation here is fast-forward only, and TakeRemote is
// the escape hatch that keeps a backup branch of whatever it discards.
//
// The package is a leaf: it imports nothing from lw, so cmd/lw can wire it
// to the engine, the config and the vault lock without a cycle.
package vaultsync

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Ignore is the managed .gitignore: the per-PC state that never syncs. The
// index is derived and rebuilt after a pull, the cache, tmp, lock, logs and
// traces belong to the machine that produced them, an open changeset is
// work in progress on the PC that staged it, and sync.json is this very
// package's bookkeeping (042 D2). CommitWork rewrites the file whenever its
// bytes differ, so every PC agrees on what is excluded.
const Ignore = "# managed by lw sync: per-PC state, never synced\n" +
	"/.llmwiki/index.gob\n" +
	"/.llmwiki/cache/\n" +
	"/.llmwiki/tmp/\n" +
	"/.llmwiki/lock\n" +
	"/.llmwiki/logs/\n" +
	"/.llmwiki/traces/\n" +
	"/.llmwiki/changesets/open/\n" +
	"/.llmwiki/sync.json\n"

// Branch is the one branch lw sync uses, locally and on the remote.
const Branch = "main"

// Options says which vault to sync and how to reach the remote.
type Options struct {
	Dir         string        // the vault root = the git work tree
	Remotes     []string      // tried in order: scp-like "host:path", ssh:// URL, or a local path (no ':' before the first '/', or file://)
	Interactive bool          // false ⇒ ssh -o BatchMode=yes -o ConnectTimeout=5, and Timeout bounds each network git call
	Timeout     time.Duration // per network git call when !Interactive; 0 ⇒ 30 s
	Stderr      io.Writer     // git/ssh progress and prompts when Interactive; nil ⇒ discarded
}

// State is what one call learned about the vault and the remote, and what it
// did about it.
type State struct {
	Remote       string // the remote that answered ("" before any network step)
	Ahead        int    // commits on HEAD the remote tip lacks
	Behind       int    // commits on the remote tip HEAD lacks
	Pulled       int    // commits fast-forwarded by this call
	Pushed       int    // commits pushed by this call
	RemoteFormat int    // "version" in the remote tip's .llmwiki/format; 1 when absent; 0 when the remote has no commit
}

// Diverged reports that both sides have commits the other lacks.
func (s State) Diverged() bool { return s.Ahead > 0 && s.Behind > 0 }

// ErrNoGit is returned when git is not on PATH. Sync cannot work without it,
// and the message is for the person who has to install it.
var ErrNoGit = errors.New("git not found on PATH")

// ErrNotRepo is returned when the vault has not been put under lw sync.
var ErrNotRepo = errors.New("the vault is not under lw sync yet — run lw sync init <remote> or lw sync clone")

// ErrDiverged is returned by Pull and Push when both sides have commits the
// other lacks. The State returned beside it carries the counts; nothing was
// changed.
var ErrDiverged = errors.New("diverged")

// FormatError says the remote vault's on-disk format is newer than this lw
// can write safely. An older lw that wrote it anyway would silently drop the
// fields it does not know (encoding/json ignores unknown fields), so the
// pull is refused before anything is merged (042 D4/P2).
type FormatError struct {
	Remote string // the remote that answered
	Have   int    // the format the remote tip declares
	Max    int    // the newest format this lw supports
}

func (e *FormatError) Error() string {
	return fmt.Sprintf("the remote vault is format %d; this lw supports %d — upgrade lw on this PC, then lw sync", e.Have, e.Max)
}

// RemoteError means no remote answered. Tried lists every remote attempted
// in order and Errs[i] is why Tried[i] failed.
type RemoteError struct {
	Tried []string
	Errs  []error
}

func (e *RemoteError) Error() string {
	if len(e.Tried) == 0 {
		return "no remote answered"
	}
	parts := make([]string, len(e.Tried))
	for i, r := range e.Tried {
		var err error = errors.New("failed")
		if i < len(e.Errs) && e.Errs[i] != nil {
			err = e.Errs[i]
		}
		parts[i] = r + ": " + err.Error()
	}
	return strings.Join(parts, "; ")
}

// Unwrap exposes the per-remote errors to errors.Is and errors.As, so a
// caller can tell a timeout from a refusal.
func (e *RemoteError) Unwrap() []error { return e.Errs }

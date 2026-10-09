package vaultsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Init puts the vault under lw sync and pushes it to its first remote, which
// must be empty (the first PC of a shared vault, 042). Only Remotes[0] is
// used: a second remote is an alias for the same server, not a second
// destination.
//
// Order matters because Init must leave nothing behind when it refuses: the
// remote path is validated and the local vault checked first, then the
// remote is created or accepted, and only then does the vault get a .git. A
// failed push after that is retryable — the next Init finds the work tree
// without a tracking ref and carries on.
//
// The remote side is one shell script (remoteScript), run over ssh for a
// host:path and locally for a path, so the two cannot drift apart. Pushed is
// the number of commits sent.
func Init(ctx context.Context, o Options) (State, error) {
	r, err := newRunner(o)
	if err != nil {
		return State{}, err
	}
	if len(r.o.Remotes) == 0 {
		return State{}, errNoRemotes
	}
	if fi, err := os.Stat(r.o.Dir); err != nil || !fi.IsDir() {
		return State{}, fmt.Errorf("vault %s: not a directory", r.o.Dir)
	}
	spec := r.o.Remotes[0]
	rem, err := parseRemote(spec)
	if err != nil {
		return State{}, fmt.Errorf("remote %q: %w", spec, err)
	}
	if rem.kind == kindOther {
		return State{}, fmt.Errorf("remote %s: lw sync init needs an ssh or local path remote", spec)
	}
	if err := rem.validatePath(); err != nil {
		return State{}, err
	}
	if r.isRepo() && r.hasRef(ctx, trackRef) {
		return State{}, errors.New("already under lw sync")
	}
	// A repository with commits that this Init did not make is the user's own
	// history: committing the work tree onto their branch and pushing all of it
	// as main is not something lw sync init does unasked (S3d M4). It is told
	// apart from the repository a failed Init left behind — whose retry is the
	// reason Init orders its work as it does — by a marker in .git.
	if r.isRepo() && r.hasHead(ctx) && !r.hasRef(ctx, trackRef) && !r.initMarked(ctx) {
		return State{}, fmt.Errorf("%s is already a git repository with its own history — lw sync init will not adopt it; move its .git aside or start from a copy", r.o.Dir)
	}
	if err := r.ensureRemote(ctx, rem); err != nil {
		return State{}, err
	}

	if !r.isRepo() {
		if _, err := r.out(ctx, "init", "--quiet", "--template=", "-b", Branch); err != nil {
			return State{}, err
		}
		if err := r.markInit(ctx); err != nil {
			return State{}, err
		}
	}
	if _, err := r.commitWork(ctx, "lw sync init"); err != nil {
		return State{}, err
	}
	n, err := r.countHead(ctx)
	if err != nil {
		return State{}, err
	}
	if err := r.pushTo(ctx, spec); err != nil {
		if ctx.Err() != nil {
			return State{}, ctx.Err()
		}
		return State{}, &RemoteError{Tried: []string{spec}, Errs: []error{err}}
	}
	if _, err := r.out(ctx, "update-ref", trackRef, "HEAD"); err != nil {
		return State{}, err
	}
	r.clearInitMark(ctx) // under lw sync now: the ref says so
	st := State{Remote: spec, Pushed: n}
	if st.RemoteFormat, err = r.remoteFormat(ctx); err != nil {
		return st, err
	}
	return st, nil
}

// initMarker is the file in .git that says this repository was made by Init
// and has not finished: a retry of Init may carry on with it. It is never
// tracked or synced (it is inside .git), and it is gone once the tracking ref
// exists.
const initMarker = "lw-init"

// initMarkerPath is where the marker lives for this vault.
func (r *runner) initMarkerPath(ctx context.Context) (string, error) {
	rel, err := r.out(ctx, "rev-parse", "--git-path", initMarker)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(rel) {
		return rel, nil
	}
	return filepath.Join(r.o.Dir, rel), nil
}

// initMarked reports whether Init made this repository.
func (r *runner) initMarked(ctx context.Context) bool {
	path, err := r.initMarkerPath(ctx)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// markInit records that Init made the repository it just created.
func (r *runner) markInit(ctx context.Context) error {
	path, err := r.initMarkerPath(ctx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte("lw sync init\n"), 0o644); err != nil {
		return fmt.Errorf("vaultsync: write %s: %w", initMarker, err)
	}
	return nil
}

// clearInitMark removes the marker once Init has finished. Best effort: a
// marker left behind in a repository that is under lw sync does nothing.
func (r *runner) clearInitMark(ctx context.Context) {
	if path, err := r.initMarkerPath(ctx); err == nil {
		os.Remove(path)
	}
}

// Clone copies a synced vault to Dir, which must not exist or must be empty
// (a PC joining the vault, 042). Remotes are tried in order; the first that
// answers wins. A failed attempt is cleaned up so the next starts from the
// same state as the first.
//
// A remote whose .llmwiki/format is newer than maxFormat is refused with a
// *FormatError before any file is checked out, and the refusal leaves Dir as
// it was — absent (with any leading directories git made), or still empty
// (A-042-5). Without it a format-2 remote could be checked out under an older
// lw, which would then rewrite changesets and drop the fields it does not
// know. The format lives in the remote's tree, so the clone is made without a
// checkout, the tip's format is read from it, and only then are the files
// written; one download serves both. A refusal is an answer, not an outage:
// the remaining remotes are not tried.
//
// git clone is run with --origin lw so the clone itself creates
// refs/remotes/lw/main, then the "lw" remote is removed and the ref kept:
// the vault ends up with exactly the state a Pull/Push cycle expects and no
// configured git remote, in one network round trip instead of clone plus a
// fetch.
func Clone(ctx context.Context, o Options, maxFormat int) error {
	r, err := newRunner(o)
	if err != nil {
		return err
	}
	if len(r.o.Remotes) == 0 {
		return errNoRemotes
	}
	existed, err := emptyOrMissing(r.o.Dir)
	if err != nil {
		return err
	}
	created := "" // what this call may remove again: everything git makes for a Dir that did not exist
	if !existed {
		created = topMissing(r.o.Dir)
	}
	re := &RemoteError{}
	for _, spec := range r.o.Remotes {
		re.Tried = append(re.Tried, spec)
		err := r.cloneOne(ctx, spec, maxFormat)
		if err == nil {
			return nil
		}
		resetDir(r.o.Dir, created)
		var fe *FormatError
		var co *checkoutCollision
		if errors.As(err, &fe) || errors.As(err, &co) {
			return err // the same content on every remote; trying another changes nothing
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		re.Errs = append(re.Errs, err)
	}
	return re
}

// cloneOne clones one remote into Dir, gates on its format, checks the files
// out and leaves refs/remotes/lw/main set.
func (r *runner) cloneOne(ctx context.Context, spec string, maxFormat int) error {
	rem, err := parseRemote(spec)
	if err != nil {
		return err
	}
	// --template=: no template directory, so a user's init.templateDir cannot
	// plant an info/exclude or hooks in the vault's repository.
	args := []string{"clone", "--no-checkout", "--template=", "--branch", Branch, "--origin", "lw", "--no-tags"}
	if r.progress() {
		args = append(args, "--progress")
	}
	args = append(args, "--", rem.arg, r.o.Dir)
	if _, err := r.gitCall(ctx, call{args: args, net: true, noDir: true, sshDest: rem.sshDest(), sshPort: rem.port}); err != nil {
		return err
	}
	format, err := r.remoteFormat(ctx)
	if err != nil {
		return err
	}
	if format > maxFormat {
		return &FormatError{Remote: spec, Have: format, Max: maxFormat}
	}
	tip, err := r.out(ctx, "rev-parse", trackRef)
	if err != nil {
		return err
	}
	if _, err := r.out(ctx, "remote", "remove", "lw"); err != nil {
		return err
	}
	if _, err := r.out(ctx, "update-ref", trackRef, tip); err != nil {
		return err
	}
	// The attribute override must be in place before the files are written.
	if err := r.ensureAttributes(ctx); err != nil {
		return err
	}
	// The clone was made without a checkout; HEAD is main and the index is
	// empty, so this writes every file of the tip.
	if _, err := r.out(ctx, "reset", "--hard", "--quiet", "HEAD", "--"); err != nil {
		return err
	}
	return r.verifyCheckout(ctx)
}

// emptyOrMissing checks Clone's precondition and reports whether Dir already
// existed (as an empty directory).
func emptyOrMissing(dir string) (existed bool, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("vault %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return true, fmt.Errorf("%s is not empty — lw sync clone needs a new or empty directory", dir)
	}
	return true, nil
}

// topMissing is the highest ancestor of dir (or dir itself) that does not
// exist yet: git clone creates all of them, so a failed clone removes all of
// them.
func topMissing(dir string) string {
	top := dir
	for p := filepath.Dir(dir); ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil || filepath.Dir(p) == p {
			return top
		}
		top = p
	}
}

// resetDir undoes a failed clone attempt: what the attempt created (created,
// non-empty) is removed, and a Dir that existed (empty) is emptied again. git
// cleans up after an ordinary failure, but not after the SIGKILL of a
// timeout, and not after a refusal that came once the clone had finished.
func resetDir(dir, created string) {
	if created != "" {
		os.RemoveAll(created)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

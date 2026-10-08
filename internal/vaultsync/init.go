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
	if err := r.ensureRemote(ctx, rem); err != nil {
		return State{}, err
	}

	if !r.isRepo() {
		if _, err := r.out(ctx, "init", "--quiet", "-b", Branch); err != nil {
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
	st := State{Remote: spec, Pushed: n}
	if st.RemoteFormat, err = r.remoteFormat(ctx); err != nil {
		return st, err
	}
	return st, nil
}

// Clone copies a synced vault to Dir, which must not exist or must be empty
// (a PC joining the vault, 042). Remotes are tried in order; the first that
// clones wins and a failed attempt is cleaned up so the next starts from the
// same empty Dir.
//
// git clone is run with --origin lw so the clone itself creates
// refs/remotes/lw/main, then the "lw" remote is removed and the ref kept:
// the vault ends up with exactly the state a Pull/Push cycle expects and no
// configured git remote, in one network round trip instead of clone plus a
// fetch. It does not know which format the remote holds — the caller's
// engine open or a following Pull refuses one it cannot read.
func Clone(ctx context.Context, o Options) error {
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
	re := &RemoteError{}
	for _, spec := range r.o.Remotes {
		re.Tried = append(re.Tried, spec)
		err := r.cloneOne(ctx, spec)
		if err == nil {
			return nil
		}
		resetDir(r.o.Dir, existed)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		re.Errs = append(re.Errs, err)
	}
	return re
}

// cloneOne clones one remote into Dir and leaves refs/remotes/lw/main set.
func (r *runner) cloneOne(ctx context.Context, spec string) error {
	rem, err := parseRemote(spec)
	if err != nil {
		return err
	}
	args := []string{"clone", "--branch", Branch, "--origin", "lw", "--no-tags"}
	if r.progress() {
		args = append(args, "--progress")
	}
	args = append(args, "--", rem.arg, r.o.Dir)
	if _, err := r.gitCall(ctx, call{args: args, net: true, noDir: true}); err != nil {
		return err
	}
	tip, err := r.out(ctx, "rev-parse", trackRef)
	if err != nil {
		return err
	}
	if _, err := r.out(ctx, "remote", "remove", "lw"); err != nil {
		return err
	}
	_, err = r.out(ctx, "update-ref", trackRef, tip)
	return err
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

// resetDir undoes a failed clone attempt: a Dir the attempt created is
// removed, a Dir that existed (empty) is emptied again. git cleans up after
// an ordinary failure, but not after the SIGKILL of a timeout.
func resetDir(dir string, existed bool) {
	if !existed {
		os.RemoveAll(dir)
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

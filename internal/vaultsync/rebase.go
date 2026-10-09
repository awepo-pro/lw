package vaultsync

// rebase.go resolves a divergence without a human when it can (042 A-042-8).
// Two PCs that each added a note while apart have diverged in git's eyes, yet
// nothing of theirs conflicts; refusing — and sending the user to
// --take-remote, which moves their work off the tree onto a backup branch — was
// the right call while the only alternative was a merge lw could not vouch for,
// and the wrong one for the commonest case. So before it refuses, Pull and Push
// replay the local commits on the remote's tip with `git rebase`:
//
//   - A clean replay leaves a history that is the remote's plus the local
//     commits, in order, with no merge commit; the State says how many were
//     replayed, and Push (or the Push that follows a Pull) sends them.
//   - ANY conflict — the same page edited on both sides, two lw commits (which
//     both wrote the same snapshot id and appended to the same journal) — aborts
//     the rebase, verifies that HEAD and the tree are exactly what they were, and
//     returns ErrDiverged as before. lw commits stay refused on purpose: their
//     ids and their audit trail are two histories that must not be passed off
//     as one.
//
// The rebase is the only thing lw does that rewrites history, so it leaves
// itself a way back whatever happens to the process: refs/lw/pre-rebase holds
// HEAD from before it (and stays there after a success, as a safety net), and
// .git/lw-rebase holds a record of what was set aside — the journal's
// uncommitted lines, the untracked files the replay would have overwritten — so
// the next CommitWork, Pull, Push or TakeRemote can finish or undo a rebase that
// was cut short (recoverRebase), and lw sync reports it (Recover).
//
// git runs with the same flags as every other call (no hooks, no signing, the
// lw identity), plus no autostash, no ref updates, and an editor that is `true`:
// the rebase can never stop to ask anything.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// preRebaseRef holds HEAD from before the last rebase lw attempted.
	preRebaseRef = "refs/lw/pre-rebase"

	// rebaseStateDir, in .git, holds lw's record of a rebase in progress.
	rebaseStateDir = "lw-rebase"
)

// carryRecord is a carried append-only file as lw's record keeps it.
type carryRecord struct {
	Path  string `json:"path"`
	Whole bool   `json:"whole,omitempty"` // the file was untracked: Tail is all of it
	Tail  []byte `json:"tail"`
}

// rebaseState is lw's record of a rebase it has begun: the HEAD to go back to,
// the uncommitted journal bytes it took out of the way, and the untracked files
// it moved aside to .git/lw-rebase/moved/<path>.
type rebaseState struct {
	Pre   string        `json:"pre"`
	Carry []carryRecord `json:"carry,omitempty"`
	Moved []string      `json:"moved,omitempty"`

	// Done is set once git's rebase has finished and HEAD has been checked. A
	// record that is Done describes a rebase to keep (only the bookkeeping is
	// left); one that is not describes a vault that must go back to Pre.
	Done bool `json:"done,omitempty"`
}

// afterRebaseAbort runs between `git rebase --abort` and the check that the
// abort restored everything. It is a seam for the test that proves the check
// exists: a git that aborts badly is not something a test can summon.
var afterRebaseAbort = func(ctx context.Context, r *runner) {}

// ErrRebaseInProgress is what a sync step returns, wrapped, when git has a
// rebase in progress in the vault that lw did not start. It is the user's
// rebase and lw leaves it alone.
var ErrRebaseInProgress = errors.New("a git rebase is in progress in the vault")

// errForeignRebase is ErrRebaseInProgress with the way out.
func (r *runner) errForeignRebase() error {
	return fmt.Errorf("%w — lw did not start it; finish or abort it with git -C %s rebase, then run lw sync again", ErrRebaseInProgress, r.o.Dir)
}

// gitPath is where git keeps name for this vault, absolute.
func (r *runner) gitPath(ctx context.Context, name string) (string, error) {
	rel, err := r.out(ctx, "rev-parse", "--git-path", name)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(rel) {
		return rel, nil
	}
	return filepath.Join(r.o.Dir, rel), nil
}

// gitRebaseRunning reports whether git has a rebase in progress in the vault.
func (r *runner) gitRebaseRunning(ctx context.Context) bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if p, err := r.gitPath(ctx, d); err == nil {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	}
	return false
}

// saveRebaseState writes lw's record, replacing any earlier one.
func (r *runner) saveRebaseState(ctx context.Context, s rebaseState) error {
	dir, err := r.gitPath(ctx, rebaseStateDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("vaultsync: %w", err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("vaultsync: %w", err)
	}
	tmp := filepath.Join(dir, "state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("vaultsync: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "state.json")); err != nil {
		return fmt.Errorf("vaultsync: %w", err)
	}
	return nil
}

// loadRebaseState reads lw's record; ok is false when there is none.
func (r *runner) loadRebaseState(ctx context.Context) (s rebaseState, dir string, ok bool, err error) {
	dir, err = r.gitPath(ctx, rebaseStateDir)
	if err != nil {
		return s, "", false, err
	}
	b, rerr := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(rerr, fs.ErrNotExist) {
		return s, dir, false, nil
	}
	if rerr != nil {
		return s, dir, false, fmt.Errorf("vaultsync: %w", rerr)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, dir, false, fmt.Errorf("vaultsync: the rebase record %s is damaged: %w", filepath.Join(dir, "state.json"), err)
	}
	return s, dir, true, nil
}

// setAside moves each untracked file that the replay would overwrite into the
// record's moved/ directory. A rename: nothing is copied, nothing can be half
// written, and the file is whole again if put back.
func (r *runner) setAside(dir string, paths []string) error {
	for _, p := range paths {
		dst := filepath.Join(dir, "moved", filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("vaultsync: %w", err)
		}
		if err := os.Rename(filepath.Join(r.o.Dir, filepath.FromSlash(p)), dst); err != nil {
			return fmt.Errorf("vaultsync: %w", err)
		}
	}
	return nil
}

// putBack finishes the bookkeeping a rebase leaves, whichever way it ended:
// every set-aside file whose place is empty goes back (when the replay brought
// the same bytes there already, the copy is simply dropped), and every carried
// tail is appended to its file unless the file already ends with it — recovery
// may be repeating a step the crashed process had done. It returns the first
// error but tries everything.
func (r *runner) putBack(dir string, s rebaseState) error {
	var errs []error
	for _, p := range s.Moved {
		src := filepath.Join(dir, "moved", filepath.FromSlash(p))
		dst := filepath.Join(r.o.Dir, filepath.FromSlash(p))
		if _, err := os.Lstat(src); err != nil {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			errs = append(errs, err)
		}
	}
	for _, c := range s.Carry {
		if len(c.Tail) == 0 {
			continue
		}
		abs := filepath.Join(r.o.Dir, filepath.FromSlash(c.Path))
		if cur, err := os.ReadFile(abs); err == nil && bytes.HasSuffix(cur, c.Tail) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		f, err := os.OpenFile(abs, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_, werr := f.Write(c.Tail)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			errs = append(errs, werr)
		}
	}
	return errors.Join(errs...)
}

// treeIsAt reports whether HEAD is sha and no tracked file differs from it —
// the "byte-identical" check after an abort.
func (r *runner) treeIsAt(ctx context.Context, sha string) bool {
	head, err := r.out(ctx, "rev-parse", "HEAD")
	if err != nil || head != sha {
		return false
	}
	dirty, err := r.out(ctx, "status", "--porcelain", "--untracked-files=no")
	return err == nil && dirty == ""
}

// recoverRebase finishes or undoes a rebase a dead process left (A-042-8). A
// record that is not Done means the vault must go back to the record's HEAD:
// git's rebase, if still running, is aborted, and a tree that is not exactly at
// that HEAD is reset to it. A record that is Done means the rebase succeeded and
// only the bookkeeping is left. A rebase running WITHOUT lw's record is the
// user's, and is refused, not touched. It returns a sentence for the user when
// it did anything.
func (r *runner) recoverRebase(ctx context.Context) (string, error) {
	s, dir, have, err := r.loadRebaseState(ctx)
	if err != nil {
		return "", err
	}
	running := r.gitRebaseRunning(ctx)
	switch {
	case !have && !running:
		return "", nil
	case !have && running:
		return "", r.errForeignRebase()
	}

	note := "a rebase that was cut short had already finished; lw cleared its record"
	if !s.Done {
		if running {
			if _, err := r.gitCall(ctx, call{args: []string{"rebase", "--abort"}}); err != nil {
				return "", wrap("rebase --abort", err)
			}
		}
		if !r.treeIsAt(ctx, s.Pre) {
			if _, err := r.out(ctx, "reset", "--hard", "--quiet", s.Pre, "--"); err != nil {
				return "", err
			}
		}
		note = fmt.Sprintf("an interrupted rebase was undone — the vault is back at %s (%s) with its uncommitted journal lines put back", shortSHA(s.Pre), preRebaseRef)
	}
	if err := r.putBack(dir, s); err != nil {
		return "", fmt.Errorf("vaultsync: could not put back what the rebase set aside (it is kept in %s): %w", dir, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("vaultsync: %w", err)
	}
	return note, nil
}

// shortSHA is the first 10 characters of a commit id.
func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

// Recover finishes or undoes a rebase that a dead process left in the vault
// and reports it in one sentence ("" when there was nothing). Pull, Push,
// CommitWork and TakeRemote each do this themselves before anything else, so a
// crash never leaves the vault in a state they would build on; Recover is the
// call a caller makes first when it wants to TELL the user. A rebase in
// progress that lw did not start is an error, left exactly as it is.
func Recover(ctx context.Context, o Options) (string, error) {
	r, err := newRunner(o)
	if err != nil {
		return "", err
	}
	if !r.isRepo() {
		return "", nil
	}
	return r.recoverRebase(ctx)
}

// rebaseDiverged replays the local commits on the remote's tip, for a vault
// whose State says it has diverged. rebased reports whether it did; false with
// no error means the replay conflicted and everything was put back, so the
// caller returns ErrDiverged. carry holds the uncommitted journal tails that
// dirtyCarry found: they are set aside for the replay and put back after,
// either way. On success the State has Pulled (the remote commits now under
// HEAD), Rebased and Ahead (the replayed commits), and Behind 0; a collision
// found afterwards comes back as the error beside a State that is still the
// rebased one.
func (r *runner) rebaseDiverged(ctx context.Context, st State, carry []carried, appendOnly map[string]bool) (State, bool, error) {
	if err := r.ensureAttributes(ctx); err != nil {
		return st, false, err
	}
	// Everything that can refuse does so before anything changes.
	carry, identical, err := r.classifyIncoming(ctx, carry, appendOnly)
	if err != nil {
		return st, false, err
	}
	pre, err := r.out(ctx, "rev-parse", "HEAD")
	if err != nil {
		return st, false, err
	}

	state := rebaseState{Pre: pre}
	for _, c := range carry {
		state.Carry = append(state.Carry, carryRecord{Path: c.path, Whole: c.whole, Tail: c.tail})
	}
	for _, f := range identical {
		state.Moved = append(state.Moved, f.path)
	}
	if err := r.saveRebaseState(ctx, state); err != nil {
		return st, false, err
	}
	dir, err := r.gitPath(ctx, rebaseStateDir)
	if err != nil {
		return st, false, err
	}
	if _, err := r.out(ctx, "update-ref", preRebaseRef, pre); err != nil {
		os.RemoveAll(dir)
		return st, false, err
	}

	// From here the vault changes; any failure goes through giveUp.
	giveUp := func(cause error) (State, bool, error) {
		if r.gitRebaseRunning(ctx) {
			if _, aerr := r.gitCall(ctx, call{args: []string{"rebase", "--abort"}}); aerr != nil {
				return st, false, errors.Join(cause, wrap("rebase --abort", aerr))
			}
		}
		afterRebaseAbort(ctx, r)
		if !r.treeIsAt(ctx, pre) {
			// The abort did not restore everything. The tree was clean of
			// tracked changes when the rebase began (the tails are set aside),
			// so putting it back to the saved HEAD loses nothing.
			if _, rerr := r.out(ctx, "reset", "--hard", "--quiet", pre, "--"); rerr != nil || !r.treeIsAt(ctx, pre) {
				return st, false, errors.Join(cause, fmt.Errorf("vaultsync: the vault could not be restored after a failed rebase (%s holds its HEAD; lw sync will try again): %v", preRebaseRef, rerr))
			}
		}
		if perr := r.putBack(dir, state); perr != nil {
			return st, false, errors.Join(cause, fmt.Errorf("vaultsync: could not put back what the rebase set aside (it is kept in %s): %w", dir, perr))
		}
		os.RemoveAll(dir)
		return st, false, cause
	}

	if err := r.setAside(dir, state.Moved); err != nil {
		return giveUp(err)
	}
	for _, c := range carry {
		abs := filepath.Join(r.o.Dir, filepath.FromSlash(c.path))
		var werr error
		if c.whole {
			werr = os.Remove(abs)
		} else {
			werr = os.WriteFile(abs, c.head, 0o644)
		}
		if werr != nil {
			return giveUp(fmt.Errorf("vaultsync: %w", werr))
		}
	}

	_, rerr := r.gitCall(ctx, call{
		args: []string{"-c", "rebase.autoStash=false", "-c", "rebase.updateRefs=false", "-c", "rerere.enabled=false",
			"rebase", "--quiet", "--merge", trackRef},
		env: []string{"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true"},
	})
	if rerr != nil {
		conflicted := r.gitRebaseRunning(ctx)
		if _, _, err := giveUp(nil); err != nil {
			return st, false, err
		}
		if conflicted {
			return st, false, nil
		}
		return st, false, wrap("rebase", rerr)
	}

	ahead, behind, err := r.counts(ctx)
	if err != nil || behind != 0 {
		return giveUp(fmt.Errorf("vaultsync: the rebase left HEAD %d behind the remote (err %v)", behind, err))
	}
	state.Done = true
	if err := r.saveRebaseState(ctx, state); err != nil {
		return giveUp(err)
	}
	out := st
	out.Pulled, out.Rebased, out.Ahead, out.Behind = st.Behind, ahead, ahead, 0
	// Checked before the tails go back, as after a fast-forward: they are
	// uncommitted changes, and verifyCheckout reads any difference as a collision.
	checkoutErr := r.verifyCheckout(ctx)
	if perr := r.putBack(dir, state); perr != nil {
		return out, true, errors.Join(checkoutErr, fmt.Errorf("vaultsync: the rebase is done but what it set aside could not be put back (kept in %s): %w", dir, perr))
	}
	os.RemoveAll(dir)
	return out, true, checkoutErr
}

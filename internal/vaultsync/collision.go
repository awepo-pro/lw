package vaultsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// collisionMarker is the file in .git that records an unresolved checkout
// collision, one path per line (A-042-5 follow-up, S1d). It lives in .git so
// it is never tracked, never synced and survives a CommitWork.
const collisionMarker = "lw-collision"

// checkoutCollision means the files a checkout wrote differ from the commit
// it checked out — the symptom of two paths that differ only in case landing
// on one file on a case-insensitive filesystem (a Mac). The next CommitWork
// would read the difference as an edit and commit a deletion, so the step that
// produced it reports it instead of carrying on.
type checkoutCollision struct{ paths []string }

func (e *checkoutCollision) Error() string {
	shown := e.paths
	more := ""
	if len(shown) > 5 {
		more = fmt.Sprintf(" and %d more", len(shown)-5)
		shown = shown[:5]
	}
	return "checkout collision: " + strings.Join(shown, ", ") + more +
		" differ from the commit (case-insensitive filesystem?) — nothing will be committed until this is fixed"
}

// collisionError builds the error for the given differing paths.
func collisionError(paths []string) error { return &checkoutCollision{paths: paths} }

// verifyCheckout requires the work tree to match HEAD after a checkout that
// lw itself made (Clone, a fast-forward Pull, TakeRemote). A mismatch is
// recorded in the collision marker before the error is returned, so the
// refusal outlives this call. (A Clone removes the whole directory on this
// error, marker included: there is no vault left to guard.)
func (r *runner) verifyCheckout(ctx context.Context) error {
	// gitCall, not out: the -z output must not be trimmed.
	out, err := r.gitCall(ctx, call{args: []string{"status", "--porcelain=v1", "-z", "--untracked-files=no"}})
	if err != nil {
		return wrap("status", err)
	}
	paths := statusPaths(out)
	if len(paths) == 0 {
		return nil
	}
	if err := r.writeCollision(ctx, paths); err != nil {
		return errors.Join(collisionError(paths), err)
	}
	return collisionError(paths)
}

// statusPaths lists the paths in `git status --porcelain=v1 -z` output. An
// entry is "XY path"; a rename or copy is followed by one more entry, the
// path it came from, which is skipped.
func statusPaths(z string) []string {
	var paths []string
	entries := strings.Split(z, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
		if strings.ContainsAny(e[:2], "RC") {
			i++
		}
	}
	return paths
}

// markerPath is where the collision marker lives for this vault.
func (r *runner) markerPath(ctx context.Context) (string, error) {
	rel, err := r.out(ctx, "rev-parse", "--git-path", collisionMarker)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(rel) {
		return rel, nil
	}
	return filepath.Join(r.o.Dir, rel), nil
}

// collisionPaths reads the marker: the paths it lists, and whether it exists.
func (r *runner) collisionPaths(ctx context.Context) (paths []string, exists bool, err error) {
	path, err := r.markerPath(ctx)
	if err != nil {
		return nil, false, err
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("vaultsync: read %s: %w", collisionMarker, err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, true, nil
}

// writeCollision (re)writes the marker with paths.
func (r *runner) writeCollision(ctx context.Context, paths []string) error {
	path, err := r.markerPath(ctx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(strings.Join(paths, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("vaultsync: write %s: %w", collisionMarker, err)
	}
	return nil
}

// clearCollision removes the marker.
func (r *runner) clearCollision(ctx context.Context) error {
	path, err := r.markerPath(ctx)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("vaultsync: remove %s: %w", collisionMarker, err)
	}
	return nil
}

// unresolvedCollision is the refusal while the marker exists.
type unresolvedCollision struct{ paths []string }

func (e *unresolvedCollision) Error() string {
	return "a checkout collision is unresolved (" + strings.Join(e.paths, ", ") +
		") — rename the clashing files on the PC that created them, sync there, then run lw sync --take-remote here"
}

// refuseIfCollided is the guard at the top of CommitWork, Pull and Push: while
// the marker exists they change nothing. The next CommitWork would otherwise
// read the collided tree as the user's edit, commit the "deleted" file, and
// Push would carry the loss to every PC. Status and TakeRemote still run.
func (r *runner) refuseIfCollided(ctx context.Context) error {
	paths, exists, err := r.collisionPaths(ctx)
	if err != nil {
		return err
	}
	if exists {
		return &unresolvedCollision{paths: paths}
	}
	return nil
}

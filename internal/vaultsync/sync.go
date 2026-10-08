package vaultsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// trackRef is where every fetch lands. A vault is "under lw sync" exactly
// when this ref exists; it is never a configured git remote, so a stray `git
// pull` in the vault cannot bypass the divergence check.
const trackRef = "refs/remotes/lw/" + Branch

// remoteHead is the ref a push updates on the remote.
const remoteHead = "refs/heads/" + Branch

var errNoRemotes = errors.New("vaultsync: no remote given")

// CommitWork writes the managed .gitignore if its bytes differ, stages the
// whole vault and commits it with message when anything is staged. It
// reports whether it committed. It is local only — no network — so it is
// safe on the way into every sync step, including a failing one: the work is
// committed even when the push that follows is not (042 D3).
func CommitWork(o Options, message string) (bool, error) {
	r, err := newRunner(o)
	if err != nil {
		return false, err
	}
	ctx := context.Background()
	if !r.isRepo() {
		return false, ErrNotRepo
	}
	return r.commitWork(ctx, message)
}

func (r *runner) commitWork(ctx context.Context, message string) (bool, error) {
	if err := r.writeIgnore(); err != nil {
		return false, err
	}
	if err := r.ensureAttributes(ctx); err != nil {
		return false, err
	}
	if _, err := r.out(ctx, "add", "-A"); err != nil {
		return false, err
	}
	if err := r.refuseLinks(ctx); err != nil {
		return false, err
	}
	// status, not diff --cached: it also answers for a repo with no commit.
	staged, err := r.out(ctx, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	if staged == "" {
		return false, nil
	}
	if strings.TrimSpace(message) == "" {
		message = "lw sync"
	}
	if _, err := r.out(ctx, "commit", "--quiet", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// infoAttributes is written to .git/info/attributes, the attribute source that
// outranks every other: no text conversion, no eol rewrite, no clean/smudge
// filter, no $Id$ expansion, no re-encoding. A vault's bytes — a CRLF raw, a
// PDF — must reach the other PC exactly as they are, whatever the user's
// global attributes or a .gitattributes inside the vault say (H1 of the S1
// review: `* text=auto` rewrote CRLF).
const infoAttributes = "* -text -eol -filter -ident -working-tree-encoding\n"

// ensureAttributes keeps .git/info/attributes at infoAttributes.
func (r *runner) ensureAttributes(ctx context.Context) error {
	rel, err := r.out(ctx, "rev-parse", "--git-path", "info/attributes")
	if err != nil {
		return err
	}
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.o.Dir, path)
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == infoAttributes {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("vaultsync: %w", err)
	}
	if err := os.WriteFile(path, []byte(infoAttributes), 0o644); err != nil {
		return fmt.Errorf("vaultsync: write .git/info/attributes: %w", err)
	}
	return nil
}

// refuseLinks fails when the index holds a symlink or a nested git repository
// (a gitlink): git stores the first as its target text and the second as a
// bare commit id, so the other PC would get a dangling link or an empty
// directory, never the content. lw sync copies files only. The index is read
// after `git add -A`, so ignored paths (a symlink in .llmwiki/cache) never
// count; on a refusal the staging is undone so a failed CommitWork leaves the
// index as it found it.
func (r *runner) refuseLinks(ctx context.Context) error {
	out, err := r.gitCall(ctx, call{args: []string{"ls-files", "-s", "-z"}})
	if err != nil {
		return wrap("ls-files", err)
	}
	for _, entry := range strings.Split(out, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		var kind string
		switch {
		case strings.HasPrefix(meta, "120000 "):
			kind = "symlink"
		case strings.HasPrefix(meta, "160000 "):
			kind = "nested git repo"
		default:
			continue
		}
		if _, rerr := r.out(ctx, "reset", "--quiet"); rerr != nil {
			return rerr
		}
		return fmt.Errorf("cannot sync %s: %s — lw sync copies files only", path, kind)
	}
	return nil
}

// writeIgnore keeps the managed .gitignore at exactly Ignore.
func (r *runner) writeIgnore() error {
	path := filepath.Join(r.o.Dir, ".gitignore")
	cur, err := os.ReadFile(path)
	if err == nil && string(cur) == Ignore {
		return nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("vaultsync: read .gitignore: %w", err)
	}
	if err := os.WriteFile(path, []byte(Ignore), 0o644); err != nil {
		return fmt.Errorf("vaultsync: write .gitignore: %w", err)
	}
	return nil
}

// fetch tries each remote in order and returns the State of the first that
// answers with commits. A remote with no main yet (an empty bare repo)
// answers "empty": Behind 0, RemoteFormat 0, and any stale tracking ref is
// dropped so the counts agree with the server. An empty answer does not win
// while a later remote may hold the vault — otherwise the next push would
// send the whole vault to the wrong place — but when every answering remote
// is empty the first of them is used. When none answers the error is a
// *RemoteError naming them all.
func (r *runner) fetch(ctx context.Context) (State, error) {
	if len(r.o.Remotes) == 0 {
		return State{}, errNoRemotes
	}
	re := &RemoteError{}
	firstEmpty := ""
	for _, spec := range r.o.Remotes {
		re.Tried = append(re.Tried, spec)
		empty, err := r.fetchOne(ctx, spec)
		if err != nil {
			var le *lockError
			if ctx.Err() != nil || errors.As(err, &le) {
				if ctx.Err() != nil {
					return State{}, ctx.Err()
				}
				return State{}, err // a local problem, not this remote's
			}
			re.Errs = append(re.Errs, err)
			continue
		}
		if empty {
			if firstEmpty == "" {
				firstEmpty = spec
			}
			re.Errs = append(re.Errs, errors.New("has no commit"))
			continue
		}
		return r.state(ctx, spec, false)
	}
	if firstEmpty != "" {
		return r.state(ctx, firstEmpty, true)
	}
	return State{}, re
}

// fetchOne fetches main from one remote into trackRef. empty is true when
// the remote answered but has no main.
func (r *runner) fetchOne(ctx context.Context, spec string) (empty bool, err error) {
	rem, err := parseRemote(spec)
	if err != nil {
		return false, err
	}
	args := []string{"fetch", "--no-tags"}
	if r.progress() {
		args = append(args, "--progress")
	}
	args = append(args, "--", rem.arg, "+"+remoteHead+":"+trackRef)
	// LC_ALL=C: the "no such ref" answer below is told apart from a dead
	// remote by git's English message; a translated one would read as a
	// failure and an empty remote would never be accepted. One round trip
	// instead of a separate ls-remote matters when each costs a Cloudflare
	// handshake and the first remote may be the dead one.
	_, err = r.gitCall(ctx, call{args: args, net: true, env: []string{"LC_ALL=C"}})
	if err == nil {
		return false, nil
	}
	var ce *cmdError
	if errors.As(err, &ce) && strings.Contains(ce.stderr, "couldn't find remote ref") {
		if r.hasRef(ctx, trackRef) {
			if _, derr := r.out(ctx, "update-ref", "-d", trackRef); derr != nil {
				return false, derr
			}
		}
		return true, nil
	}
	return false, err
}

// progress reports whether git should print transfer progress: only a person
// watching (Interactive with somewhere to write).
func (r *runner) progress() bool { return r.o.Interactive && r.o.Stderr != nil }

// state builds the State for a remote that answered.
func (r *runner) state(ctx context.Context, spec string, empty bool) (State, error) {
	st := State{Remote: spec}
	if empty {
		n, err := r.countHead(ctx)
		if err != nil {
			return st, err
		}
		st.Ahead = n
		return st, nil
	}
	var err error
	if st.Ahead, st.Behind, err = r.counts(ctx); err != nil {
		return st, err
	}
	if st.RemoteFormat, err = r.remoteFormat(ctx); err != nil {
		return st, err
	}
	return st, nil
}

// countHead is the number of commits reachable from HEAD.
func (r *runner) countHead(ctx context.Context) (int, error) {
	out, err := r.out(ctx, "rev-list", "--count", "HEAD", "--")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// counts is Ahead and Behind of HEAD against trackRef.
func (r *runner) counts(ctx context.Context) (ahead, behind int, err error) {
	out, err := r.out(ctx, "rev-list", "--left-right", "--count", "HEAD..."+trackRef, "--")
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("vaultsync: unexpected rev-list output %q", out)
	}
	if ahead, err = strconv.Atoi(f[0]); err != nil {
		return 0, 0, err
	}
	if behind, err = strconv.Atoi(f[1]); err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

// remoteFormat reads "version" from .llmwiki/format in the remote tip: 1 when
// the file is absent (a vault from before the format existed). A file that
// does not parse, or names a version below 1, is an error — the gate fails
// closed rather than guess.
func (r *runner) remoteFormat(ctx context.Context) (int, error) {
	listed, err := r.out(ctx, "ls-tree", trackRef, "--", ".llmwiki/format")
	if err != nil {
		return 0, err
	}
	if listed == "" {
		return 1, nil
	}
	body, err := r.out(ctx, "cat-file", "blob", trackRef+":.llmwiki/format")
	if err != nil {
		return 0, err
	}
	var f struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		return 0, fmt.Errorf("remote .llmwiki/format: %w", err)
	}
	if f.Version == nil || *f.Version < 1 {
		return 0, errors.New("remote .llmwiki/format: \"version\" must be an integer of at least 1")
	}
	return *f.Version, nil
}

// Status fetches and reports ahead/behind, the remote's format and which
// remote answered (042: `lw sync status` and the auto-sync warnings). It never
// touches the work tree or HEAD; the only thing it writes is the tracking ref.
func Status(ctx context.Context, o Options) (State, error) {
	r, err := newRunner(o)
	if err != nil {
		return State{}, err
	}
	if err := r.requireRepo(ctx); err != nil {
		return State{}, err
	}
	return r.fetch(ctx)
}

// Pull fetches, then fast-forwards HEAD to the remote tip when this PC has
// nothing the remote lacks. A remote format newer than maxFormat is refused
// before anything is merged, and a divergence is reported with its counts and
// changes nothing. The work tree must have no uncommitted tracked changes:
// callers CommitWork first (042 D3).
func Pull(ctx context.Context, o Options, maxFormat int) (State, error) {
	r, err := newRunner(o)
	if err != nil {
		return State{}, err
	}
	if err := r.requireRepo(ctx); err != nil {
		return State{}, err
	}
	if dirty, err := r.out(ctx, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return State{}, err
	} else if dirty != "" {
		return State{}, fmt.Errorf("the vault has uncommitted changes — run git -C %s status", r.o.Dir)
	}
	st, err := r.fetch(ctx)
	if err != nil {
		return st, err
	}
	if st.RemoteFormat > maxFormat {
		return st, &FormatError{Remote: st.Remote, Have: st.RemoteFormat, Max: maxFormat}
	}
	if st.Diverged() {
		return st, ErrDiverged
	}
	if st.Behind > 0 && st.Ahead == 0 {
		// Normally CommitWork has just written it; a Pull on its own must
		// not depend on that.
		if err := r.ensureAttributes(ctx); err != nil {
			return st, err
		}
		if _, err := r.out(ctx, "merge", "--ff-only", "--quiet", trackRef); err != nil {
			return st, err
		}
		st.Pulled, st.Behind = st.Behind, 0
		if err := r.verifyCheckout(ctx); err != nil {
			return st, err
		}
	}
	return st, nil
}

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
// lw itself made (Clone, a fast-forward Pull, TakeRemote).
func (r *runner) verifyCheckout(ctx context.Context) error {
	// gitCall, not out: the -z output must not be trimmed.
	out, err := r.gitCall(ctx, call{args: []string{"status", "--porcelain=v1", "-z", "--untracked-files=no"}})
	if err != nil {
		return wrap("status", err)
	}
	if paths := statusPaths(out); len(paths) > 0 {
		return collisionError(paths)
	}
	return nil
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

// Push fetches, then pushes HEAD to the remote's main when the remote has
// nothing this PC lacks. A divergence is refused with its counts. A push
// that git rejects because the remote moved after our fetch (a race with
// another PC) is turned into the same ErrDiverged with fresh counts, never
// surfaced as a raw git error (042 D3).
func Push(ctx context.Context, o Options) (State, error) {
	r, err := newRunner(o)
	if err != nil {
		return State{}, err
	}
	if err := r.requireRepo(ctx); err != nil {
		return State{}, err
	}
	st, err := r.fetch(ctx)
	if err != nil {
		return st, err
	}
	if st.Diverged() {
		return st, ErrDiverged
	}
	if st.Ahead == 0 {
		return st, nil // nothing to push; Behind > 0 is Pull's business
	}

	// The remote that just answered goes first; the others follow in order
	// in case it fails between the fetch and the push.
	order := []string{st.Remote}
	for _, spec := range r.o.Remotes {
		if spec != st.Remote {
			order = append(order, spec)
		}
	}
	re := &RemoteError{}
	for _, spec := range order {
		err := r.pushTo(ctx, spec)
		if err == nil {
			if _, err := r.out(ctx, "update-ref", trackRef, "HEAD"); err != nil {
				return st, err
			}
			st.Remote, st.Pushed, st.Ahead = spec, st.Ahead, 0
			return st, nil
		}
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		// Was it a race? Ask the remote again rather than parse git's
		// rejection text: if it moved ahead of what we fetched, we have
		// diverged, whatever git called it.
		if empty, ferr := r.fetchOne(ctx, spec); ferr == nil && !empty {
			ahead, behind, cerr := r.counts(ctx)
			if cerr == nil && behind > 0 {
				format, _ := r.remoteFormat(ctx)
				return State{Remote: spec, Ahead: ahead, Behind: behind, RemoteFormat: format}, ErrDiverged
			}
		}
		re.Tried = append(re.Tried, spec)
		re.Errs = append(re.Errs, err)
	}
	return st, re
}

// pushTo pushes HEAD to main on one remote.
func (r *runner) pushTo(ctx context.Context, spec string) error {
	rem, err := parseRemote(spec)
	if err != nil {
		return err
	}
	args := []string{"push"}
	if r.progress() {
		args = append(args, "--progress")
	}
	args = append(args, "--", rem.arg, "HEAD:"+remoteHead)
	_, err = r.gitCall(ctx, call{args: args, net: true})
	return err
}

// TakeRemote is the escape hatch for a divergence: it keeps the remote and
// sets this PC's commits aside on a branch named lw-diverged-<YYYYMMDD-HHMMSS>
// (local time), then makes HEAD and the work tree the remote's tip. Nothing
// is lost: uncommitted work is committed onto the backup first, and the
// branch name is returned. It refuses a remote with no commit — there is
// nothing to take — and a remote whose format is newer than maxFormat, with a
// *FormatError, before it commits, branches or resets anything (A-042-5):
// checking out a format this lw cannot write would leave the PC's own work on
// a backup branch and a vault it then refuses to open.
func TakeRemote(ctx context.Context, o Options, maxFormat int) (backup string, st State, err error) {
	return takeRemoteAt(ctx, o, maxFormat, time.Now())
}

// takeRemoteAt is TakeRemote with the clock injected for the branch name.
func takeRemoteAt(ctx context.Context, o Options, maxFormat int, now time.Time) (string, State, error) {
	r, err := newRunner(o)
	if err != nil {
		return "", State{}, err
	}
	if err := r.requireRepo(ctx); err != nil {
		return "", State{}, err
	}
	st, err := r.fetch(ctx)
	if err != nil {
		return "", st, err
	}
	if st.RemoteFormat == 0 {
		return "", st, fmt.Errorf("remote %s has no commit to take", st.Remote)
	}
	if st.RemoteFormat > maxFormat {
		return "", st, &FormatError{Remote: st.Remote, Have: st.RemoteFormat, Max: maxFormat}
	}
	if _, err := r.commitWork(ctx, "lw sync: save before take-remote"); err != nil {
		return "", st, err
	}
	name := "lw-diverged-" + now.Format("20060102-150405")
	for n := 2; r.hasRef(ctx, "refs/heads/"+name); n++ {
		name = "lw-diverged-" + now.Format("20060102-150405") + "-" + strconv.Itoa(n)
	}
	if _, err := r.out(ctx, "branch", name, "HEAD"); err != nil {
		return "", st, err
	}
	if _, err := r.out(ctx, "reset", "--hard", "--quiet", trackRef, "--"); err != nil {
		return "", st, err
	}
	st.Pulled, st.Ahead, st.Behind = st.Behind, 0, 0
	if err := r.verifyCheckout(ctx); err != nil {
		return name, st, err
	}
	return name, st, nil
}

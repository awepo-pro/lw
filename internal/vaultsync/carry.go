package vaultsync

// carry.go is what lets Pull fast-forward a work tree that is not clean
// (042 A-042-7 a). Two kinds of local state are in the way of a fast-forward
// that git would refuse, and both are routine for the PC that is in the middle
// of a changeset:
//
//   - an append-only file with uncommitted lines — the journal, to which an
//     open changeset has appended events. Its uncommitted part is a tail
//     added to the HEAD version, so Pull sets the tail aside, restores the
//     HEAD bytes, fast-forwards and appends the tail to the new version. The
//     result is the other PC's lines followed by this PC's, which is what the
//     journal's order has always meant: a line is where it was appended.
//   - an untracked file at a path the incoming commits create. Staging an
//     object the other PC also staged makes the same content-addressed file
//     on both; identical bytes are removed first (the merge brings them
//     back), different bytes are the user's to move aside.
//
// Anything else — a tracked file edited, an append-only file changed in any
// way but extended — is ErrDirty: Pull cannot tell what it would lose.
//
// Everything is checked before anything is changed, and a merge that fails puts
// every file back as it was. The one window left is the instant between
// restoring the HEAD bytes and appending the tail, in which the file holds
// only the HEAD version; the vault lock (cmd/lw) keeps lw's own commits out,
// but a journal line appended by another lw process in exactly that instant
// would be overwritten.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// carried is an append-only file whose uncommitted bytes a fast-forward must
// not lose.
type carried struct {
	path  string // vault-relative, slash-separated
	head  []byte // the HEAD version (nil when HEAD has none)
	tail  []byte // the bytes after it in the work tree
	whole bool   // the file is untracked: tail is all of it, and there is no HEAD version to restore
}

// original is the file's bytes before Pull touched it.
func (c carried) original() []byte { return append(append([]byte(nil), c.head...), c.tail...) }

// cleanAppendOnly validates Options.AppendOnly and returns it as a set. A path
// that is not a plain vault-relative file path is a caller's bug and is
// refused before Pull does anything.
func cleanAppendOnly(paths []string) (map[string]bool, error) {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		c := path.Clean(filepath.ToSlash(p))
		if p == "" || path.IsAbs(c) || c == "." || c == ".." || strings.HasPrefix(c, "../") || c == ".git" || strings.HasPrefix(c, ".git/") {
			return nil, fmt.Errorf("vaultsync: AppendOnly path %q must be a file inside the vault", p)
		}
		set[c] = true
	}
	return set, nil
}

// dirtyCarry classifies the work tree's uncommitted changes to tracked files.
// None: nil, nil. Only pure extensions of append-only files: the tails to
// carry. Anything else: a *dirtyError.
func (r *runner) dirtyCarry(ctx context.Context, appendOnly map[string]bool) ([]carried, error) {
	// gitCall, not out: the -z output must not be trimmed.
	out, err := r.gitCall(ctx, call{args: []string{"status", "--porcelain=v1", "-z", "--untracked-files=no"}})
	if err != nil {
		return nil, wrap("status", err)
	}
	var carry []carried
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		xy, p := e[:2], e[3:]
		if strings.ContainsAny(xy, "RC") {
			i++ // the entry after a rename or copy is its source
		}
		// Only an unstaged modification of a declared file can be a tail: a
		// staged change leaves the index holding bytes this does not restore.
		if !appendOnly[p] || xy != " M" {
			return nil, &dirtyError{dir: r.o.Dir}
		}
		head, err := r.gitCall(ctx, call{args: []string{"cat-file", "blob", "HEAD:" + p}})
		if err != nil {
			return nil, &dirtyError{dir: r.o.Dir}
		}
		work, err := os.ReadFile(filepath.Join(r.o.Dir, filepath.FromSlash(p)))
		if err != nil || !bytes.HasPrefix(work, []byte(head)) {
			return nil, &dirtyError{dir: r.o.Dir}
		}
		carry = append(carry, carried{path: p, head: []byte(head), tail: work[len(head):]})
	}
	return carry, nil
}

// incomingAdds lists the paths the commits between HEAD and the remote-tracking
// ref create.
func (r *runner) incomingAdds(ctx context.Context) ([]string, error) {
	out, err := r.gitCall(ctx, call{args: []string{"diff", "--no-renames", "--name-only", "-z", "--diff-filter=A", "HEAD", trackRef, "--"}})
	if err != nil {
		return nil, wrap("diff", err)
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// removed is an untracked file Pull deleted because the merge brings the same
// bytes back.
type removed struct {
	path string
	data []byte
	mode fs.FileMode
}

// classifyIncoming sorts the untracked files at paths the incoming commits
// create (HEAD to the remote-tracking ref: the same set whether the commits
// will be fast-forwarded or replayed under): an append-only file is carried
// whole, a file byte-identical to the incoming one is to be removed, and a
// different one is the user's, so the pull is refused, naming them. Nothing is
// changed.
func (r *runner) classifyIncoming(ctx context.Context, carry []carried, appendOnly map[string]bool) ([]carried, []removed, error) {
	adds, err := r.incomingAdds(ctx)
	if err != nil {
		return nil, nil, err
	}
	var identical []removed
	var conflicts []string
	for _, p := range adds {
		abs := filepath.Join(r.o.Dir, filepath.FromSlash(p))
		info, err := os.Lstat(abs)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("vaultsync: %w", err)
		}
		if !info.Mode().IsRegular() {
			conflicts = append(conflicts, p)
			continue
		}
		local, err := os.ReadFile(abs)
		if err != nil {
			return nil, nil, fmt.Errorf("vaultsync: %w", err)
		}
		if appendOnly[p] {
			// The file is not in HEAD, so everything in it is uncommitted lines.
			carry = append(carry, carried{path: p, tail: local, whole: true})
			continue
		}
		incoming, err := r.gitCall(ctx, call{args: []string{"cat-file", "blob", trackRef + ":" + p}})
		if err != nil {
			return nil, nil, wrap("cat-file", err)
		}
		if incoming == string(local) {
			identical = append(identical, removed{path: p, data: local, mode: info.Mode().Perm()})
		} else {
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) > 0 {
		return nil, nil, fmt.Errorf("untracked files would be overwritten by the pull: %s — move them aside and run lw sync again", strings.Join(conflicts, ", "))
	}
	return carry, identical, nil
}

// prepareFastForward makes the work tree one git can fast-forward, or says why
// it cannot, changing nothing in that case. It extends carry with append-only
// files that are untracked here but created by the incoming commits, removes
// the untracked files that are byte-identical to the incoming ones, and
// restores the HEAD bytes of every carried file. The returned undo puts it all
// back; the returned carry is what reappend must append afterwards.
func (r *runner) prepareFastForward(ctx context.Context, carry []carried, appendOnly map[string]bool) ([]carried, func(), error) {
	carry, identical, err := r.classifyIncoming(ctx, carry, appendOnly)
	if err != nil {
		return nil, nil, err
	}

	undo := func() {
		for _, c := range carry {
			writeFile(r.o.Dir, c.path, c.original(), 0o644)
		}
		for _, f := range identical {
			writeFile(r.o.Dir, f.path, f.data, f.mode)
		}
	}
	for _, f := range identical {
		if err := os.Remove(filepath.Join(r.o.Dir, filepath.FromSlash(f.path))); err != nil {
			undo()
			return nil, nil, fmt.Errorf("vaultsync: %w", err)
		}
	}
	for _, c := range carry {
		abs := filepath.Join(r.o.Dir, filepath.FromSlash(c.path))
		var err error
		if c.whole {
			err = os.Remove(abs)
		} else {
			err = os.WriteFile(abs, c.head, 0o644)
		}
		if err != nil {
			undo()
			return nil, nil, fmt.Errorf("vaultsync: %w", err)
		}
	}
	return carry, undo, nil
}

// reappend writes each carried tail onto the end of its file, which the
// fast-forward has just replaced with the incoming version.
func (r *runner) reappend(carry []carried) error {
	var errs []error
	for _, c := range carry {
		if len(c.tail) == 0 {
			continue
		}
		abs := filepath.Join(r.o.Dir, filepath.FromSlash(c.path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		f, err := os.OpenFile(abs, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_, werr := f.Write(c.tail)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			errs = append(errs, werr)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("vaultsync: the pull is done but the uncommitted lines of an append-only file could not be put back: %w", err)
	}
	return nil
}

// writeFile restores a file's bytes, best effort: it is only used to undo.
func writeFile(dir, rel string, data []byte, mode fs.FileMode) {
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if os.MkdirAll(filepath.Dir(abs), 0o755) == nil {
		_ = os.WriteFile(abs, data, mode)
	}
}

// isTerminal reports whether w is somewhere a person is watching: an *os.File
// that is a character device — a terminal (or /dev/null, which discards what it
// is shown). golang.org/x/term would say it exactly, but vaultsync links
// nothing outside the standard library, and a pipe, a file or a buffer is never
// a character device, which is the distinction that matters here.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

package vaultsync

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DirtyState is what the work tree holds that the next CommitWork would commit.
type DirtyState struct {
	Paths   []string  // vault-relative, slash-separated, sorted; empty = clean
	Newest  time.Time // the latest mtime among Paths that still exist; zero when none exists
	Deleted int       // how many of Paths are tracked files that no longer exist
	Tracked int       // how many files HEAD tracks (0 before the first commit)
}

// Dirty reads the work tree: git status --porcelain=v1 -z --untracked-files=all, so ignored paths never count.
// It changes nothing (no index write: use GIT_OPTIONAL_LOCKS=0).
func Dirty(ctx context.Context, o Options) (DirtyState, error) {
	r, err := newRunner(o)
	if err != nil {
		return DirtyState{}, err
	}
	if !r.isRepo() {
		return DirtyState{}, ErrNotRepo
	}

	var st DirtyState
	if r.hasHead(ctx) {
		out, err := r.gitCall(ctx, call{args: []string{"ls-tree", "-r", "--name-only", "-z", "HEAD"}})
		if err != nil {
			return DirtyState{}, wrap("ls-tree", err)
		}
		if out != "" {
			entries := strings.Split(out, "\x00")
			count := 0
			for _, e := range entries {
				if e != "" {
					count++
				}
			}
			st.Tracked = count
		}
	}

	out, err := r.gitCall(ctx, call{
		args: []string{"status", "--porcelain=v1", "-z", "--untracked-files=all"},
		env:  []string{"GIT_OPTIONAL_LOCKS=0"},
	})
	if err != nil {
		return DirtyState{}, wrap("status", err)
	}
	if out == "" {
		return st, nil
	}

	entries := strings.Split(out, "\x00")
	seen := make(map[string]bool)
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		statusCode := e[:2]
		path := e[3:]

		var origPath string
		if strings.ContainsAny(statusCode, "RC") {
			if i+1 < len(entries) {
				i++
				origPath = entries[i]
			}
		}

		pathsToCheck := []string{path}
		if origPath != "" {
			pathsToCheck = append(pathsToCheck, origPath)
		}

		for _, p := range pathsToCheck {
			p = filepath.ToSlash(p)
			if seen[p] {
				continue
			}
			seen[p] = true
			st.Paths = append(st.Paths, p)

			fullPath := filepath.Join(r.o.Dir, filepath.FromSlash(p))
			info, serr := os.Stat(fullPath)
			if serr != nil && os.IsNotExist(serr) {
				if strings.ContainsAny(statusCode, "D") || (origPath != "" && p == origPath) {
					st.Deleted++
				}
			} else if serr == nil {
				mt := info.ModTime()
				if st.Newest.IsZero() || mt.After(st.Newest) {
					st.Newest = mt
				}
			}
		}
	}
	sort.Strings(st.Paths)
	return st, nil
}

// RemoteHead asks the remotes, in order, for refs/heads/main WITHOUT fetching objects (git ls-remote), and
// returns the first answer. sha is "" for a remote that has no main yet. local is the sha of the tracking ref
// this PC holds for it ("" when none). When no remote answers the error is a *RemoteError, as fetch returns.
func RemoteHead(ctx context.Context, o Options) (sha, local, remote string, err error) {
	r, err := newRunner(o)
	if err != nil {
		return "", "", "", err
	}
	if len(r.o.Remotes) == 0 {
		return "", "", "", errNoRemotes
	}

	re := &RemoteError{}
	for _, spec := range r.o.Remotes {
		re.Tried = append(re.Tried, spec)
		rem, perr := parseRemote(spec)
		if perr != nil {
			re.Errs = append(re.Errs, perr)
			continue
		}

		args := []string{"ls-remote", rem.arg, remoteHead}
		out, lerr := r.gitCall(ctx, call{
			args:    args,
			net:     true,
			sshDest: rem.sshDest(),
			sshPort: rem.port,
		})
		if lerr != nil {
			if ctx.Err() != nil {
				return "", "", "", ctx.Err()
			}
			re.Errs = append(re.Errs, lerr)
			continue
		}

		remoteSha := ""
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == remoteHead {
				remoteSha = fields[0]
				break
			}
		}

		localSha := ""
		if r.isRepo() && r.hasRef(ctx, trackRef) {
			if lsha, rerr := r.out(ctx, "rev-parse", trackRef); rerr == nil {
				localSha = strings.TrimSpace(lsha)
			}
		}

		return remoteSha, localSha, spec, nil
	}

	return "", "", "", re
}

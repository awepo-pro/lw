package vaultsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	// hostConflictRe matches any character outside [a-z0-9-].
	hostConflictRe = regexp.MustCompile(`[^a-z0-9-]`)

	// testConflictCopyPreHook is called before resolving an unmerged path.
	testConflictCopyPreHook func(p string) error

	// testConflictCopyPostHook is called after resolving an unmerged path.
	testConflictCopyPostHook func(p string) error

	// testNowFunc overrides time.Now in tests.
	testNowFunc func() time.Time
)

// SetTestConflictCopyPostHook sets testConflictCopyPostHook for tests.
func SetTestConflictCopyPostHook(fn func(p string) error) {
	testConflictCopyPostHook = fn
}

// unmergedPaths returns the sorted list of unmerged paths (diff-filter=U)
// from the index, slash-separated.
func (r *runner) unmergedPaths(ctx context.Context) ([]string, error) {
	out, err := r.gitCall(ctx, call{args: []string{"diff", "--name-only", "--diff-filter=U", "-z"}})
	if err != nil {
		return nil, wrap("diff --name-only", err)
	}
	parts := strings.Split(out, "\x00")
	var paths []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			paths = append(paths, filepath.ToSlash(p))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// stageBlob reads blob contents at stage (2 or 3) for path p.
// It returns (bytes, true, nil) if present, (nil, false, nil) if not in that stage,
// or an error if git failed unexpectedly.
func (r *runner) stageBlob(ctx context.Context, stage int, p string) ([]byte, bool, error) {
	var buf bytes.Buffer
	_, err := r.gitCall(ctx, call{
		args:   []string{"cat-file", "blob", fmt.Sprintf(":%d:%s", stage, p)},
		stdout: &buf,
	})
	if err == nil {
		return buf.Bytes(), true, nil
	}
	var ce *cmdError
	if errors.As(err, &ce) && (strings.Contains(ce.stderr, "not at stage") || strings.Contains(ce.stderr, "does not exist in the index") || strings.Contains(ce.stderr, "not in the index")) {
		return nil, false, nil
	}
	return nil, false, wrap("cat-file blob", err)
}

// conflictHost returns commitHost() lower-cased with any character outside
// [a-z0-9-] replaced by "-".
func conflictHost() string {
	h := strings.ToLower(commitHost())
	clean := hostConflictRe.ReplaceAllString(h, "-")
	if clean == "" {
		return "localhost"
	}
	return clean
}

// conflictCopyCandidate finds an available conflict copy path for relPath.
// Format: <stem>.conflict-<host>-<YYYYMMDD>-<HHMMSS><ext>
// (-2, -3... before <ext> when taken).
func (r *runner) conflictCopyCandidate(relPath string, alreadyCreated []string) (string, error) {
	now := time.Now()
	if testNowFunc != nil {
		now = testNowFunc()
	}
	timeStr := now.Format("20060102-150405")
	host := conflictHost()

	dir := path.Dir(relPath)
	base := path.Base(relPath)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	prefix := ""
	if dir != "." && dir != "" {
		prefix = dir + "/"
	}

	targetDir := filepath.Join(r.o.Dir, filepath.FromSlash(dir))
	entries, _ := os.ReadDir(targetDir)

	isTaken := func(name, full, rel string) bool {
		for _, taken := range alreadyCreated {
			if strings.EqualFold(taken, rel) {
				return true
			}
		}
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), name) {
				return true
			}
		}
		if _, err := os.Lstat(full); !os.IsNotExist(err) {
			return true
		}
		return false
	}

	buildCandidate := func(seq int) (string, string, string) {
		var candidateName string
		if seq == 1 {
			candidateName = fmt.Sprintf("%s.conflict-%s-%s%s", stem, host, timeStr, ext)
		} else {
			candidateName = fmt.Sprintf("%s.conflict-%s-%s-%d%s", stem, host, timeStr, seq, ext)
		}
		relCandidate := prefix + candidateName
		full := filepath.Join(r.o.Dir, filepath.FromSlash(relCandidate))
		return relCandidate, candidateName, full
	}

	for seq := 1; ; seq++ {
		relCandidate, candidateName, full := buildCandidate(seq)
		if !isTaken(candidateName, full, relCandidate) {
			return relCandidate, nil
		}
	}
}

// resolveConflictPaths resolves each unmerged note path in unmerged.
// For each path:
//   - both exist: write this PC's bytes (stage 3) to conflict copy, git add copy;
//     write remote bytes (stage 2) to p, git add p.
//   - stage 3 only: this PC edited, remote deleted. Write stage 3 bytes to p, git add p.
//   - stage 2 only: remote edited, this PC deleted. Write stage 2 bytes to p, git add p.
//
// Returns the list of conflict copies created.
func (r *runner) resolveConflictPaths(ctx context.Context, unmerged []string, allCopies []string, createdFiles *[]string) ([]string, error) {
	var copies []string
	for _, p := range unmerged {
		if testConflictCopyPreHook != nil {
			if err := testConflictCopyPreHook(p); err != nil {
				return nil, err
			}
		}
		b2, has2, err := r.stageBlob(ctx, 2, p)
		if err != nil {
			return nil, err
		}
		b3, has3, err := r.stageBlob(ctx, 3, p)
		if err != nil {
			return nil, err
		}

		absP := filepath.Join(r.o.Dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(absP), 0o755); err != nil {
			return nil, fmt.Errorf("vaultsync: %w", err)
		}

		switch {
		case has2 && has3:
			// Both edited:
			// Safety property: Write conflict copy to disk and git add it
			// BEFORE overwriting note path p with remote's bytes.
			candidate, err := r.conflictCopyCandidate(p, append(allCopies, copies...))
			if err != nil {
				return nil, err
			}
			absCopy := filepath.Join(r.o.Dir, filepath.FromSlash(candidate))
			if err := os.MkdirAll(filepath.Dir(absCopy), 0o755); err != nil {
				return nil, fmt.Errorf("vaultsync: %w", err)
			}
			if err := os.WriteFile(absCopy, b3, 0o644); err != nil {
				return nil, fmt.Errorf("vaultsync: %w", err)
			}
			*createdFiles = append(*createdFiles, absCopy)
			if _, err := r.out(ctx, "add", candidate); err != nil {
				return nil, err
			}

			// Now overwrite note path p with remote's bytes
			if err := os.WriteFile(absP, b2, 0o644); err != nil {
				return nil, fmt.Errorf("vaultsync: %w", err)
			}
			if _, err := r.out(ctx, "add", p); err != nil {
				return nil, err
			}
			copies = append(copies, candidate)

		case has3 && !has2:
			// Remote deleted, this PC edited: edit wins (keep this PC's bytes)
			if err := os.WriteFile(absP, b3, 0o644); err != nil {
				return nil, fmt.Errorf("vaultsync: %w", err)
			}
			if _, err := r.out(ctx, "add", p); err != nil {
				return nil, err
			}

		case has2 && !has3:
			// This PC deleted, remote edited: edit wins (keep remote's bytes)
			if err := os.WriteFile(absP, b2, 0o644); err != nil {
				return nil, fmt.Errorf("vaultsync: %w", err)
			}
			if _, err := r.out(ctx, "add", p); err != nil {
				return nil, err
			}

		default:
			_ = os.Remove(absP)
			_, _ = r.out(ctx, "rm", "-f", "--ignore-unmatch", p)
		}

		if testConflictCopyPostHook != nil {
			if err := testConflictCopyPostHook(p); err != nil {
				return nil, err
			}
		}
	}
	return copies, nil
}

// continueOrSkip continues the rebase or skips the commit if it became empty.
func (r *runner) continueOrSkip(ctx context.Context) error {
	// Check if there are staged changes relative to HEAD
	_, diffErr := r.gitCall(ctx, call{args: []string{"diff", "--cached", "--quiet"}})
	if diffErr == nil {
		// Nothing staged: commit became empty. Skip it.
		_, serr := r.gitCall(ctx, call{
			args: []string{"-c", "rebase.autoStash=false", "-c", "rebase.updateRefs=false", "-c", "rerere.enabled=false",
				"-c", "core.editor=true", "rebase", "--skip"},
			env: []string{"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true"},
		})
		if serr != nil && !r.gitRebaseRunning(ctx) {
			return wrap("rebase --skip", serr)
		}
		return nil
	}

	// Changes are staged: continue rebase
	_, cerr := r.gitCall(ctx, call{
		args: []string{"-c", "rebase.autoStash=false", "-c", "rebase.updateRefs=false", "-c", "rerere.enabled=false",
			"-c", "core.editor=true", "rebase", "--continue"},
		env: []string{"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true"},
	})
	if cerr != nil {
		if !r.gitRebaseRunning(ctx) {
			return wrap("rebase --continue", cerr)
		}
		// If rebase is still running, check if it failed because the commit was empty
		var ce *cmdError
		if errors.As(cerr, &ce) && (strings.Contains(ce.stderr, "empty") || strings.Contains(ce.stderr, "No changes")) {
			_, serr := r.gitCall(ctx, call{
				args: []string{"-c", "rebase.autoStash=false", "-c", "rebase.updateRefs=false", "-c", "rerere.enabled=false",
					"-c", "core.editor=true", "rebase", "--skip"},
				env: []string{"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true"},
			})
			if serr != nil && !r.gitRebaseRunning(ctx) {
				return wrap("rebase --skip", serr)
			}
		}
	}
	return nil
}

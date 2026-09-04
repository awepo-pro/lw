// snapshot.go implements the backbone §5.8 snapshot format — a
// path→sha256 manifest of the whole vault at a commit, written as
// snapshots/<id>.tree — plus the thin Engine.Snapshot wrapper the backbone
// assigns here rather than to any method-owning file (MASTER §9 D-AT).
package stage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Snapshot maps a vault-relative path to the hex sha256 of that file's
// canonical bytes at some commit (backbone §5.8).
type Snapshot map[string]string

// WriteSnapshot writes s to dir/<commitID>.tree.
//
// Contract (backbone §5.8, MASTER §9 D-AW): one "<sha>  <path>\n" line per
// entry — a 64-character lowercase hex sha, exactly two spaces, then the
// vault-relative path — sorted by path. The write goes through a temp file
// in dir, fsync'd then renamed into place, so a crashed write never leaves
// a half-written snapshot under commitID.
//
// It rejects any entry ReadSnapshot could not read back — a sha that is not
// 64 lowercase hex, an empty path, or a path containing a newline — so the
// round trip is total: whatever WriteSnapshot accepts, ReadSnapshot returns
// unchanged (MASTER §10 OR-4). Without that, a bad sha writes cleanly at
// Commit time and only fails at Revert, pointing the error at the reader
// instead of the writer that produced it.
func WriteSnapshot(dir, commitID string, s Snapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("stage: write snapshot %s: %w", commitID, err)
	}

	paths := make([]string, 0, len(s))
	for p := range s {
		if p == "" || strings.Contains(p, "\n") {
			return fmt.Errorf("stage: write snapshot %s: unreadable path %q", commitID, p)
		}
		if !isLowerHex64(s[p]) {
			return fmt.Errorf("stage: write snapshot %s: path %q: sha %q is not 64 lowercase hex characters", commitID, p, s[p])
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var b strings.Builder
	for _, p := range paths {
		b.WriteString(s[p])
		b.WriteString("  ")
		b.WriteString(p)
		b.WriteByte('\n')
	}

	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return fmt.Errorf("stage: write snapshot %s: %w", commitID, err)
	}
	tmpPath := tmp.Name()

	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.WriteString(b.String()); err != nil {
		return fmt.Errorf("stage: write snapshot %s: %w", commitID, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("stage: write snapshot %s: %w", commitID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("stage: write snapshot %s: %w", commitID, err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, commitID+".tree")); err != nil {
		return fmt.Errorf("stage: write snapshot %s: %w", commitID, err)
	}
	ok = true
	return nil
}

// ReadSnapshot reads dir/<commitID>.tree and returns the Snapshot it
// encodes.
//
// Contract (backbone §5.8, MASTER §9 D-AW): each line is parsed by fixed
// offsets — line[:64] is the sha, line[64:66] must be exactly "  ", and
// line[66:] is the path, taken whole — never strings.Fields and never a
// split on the first run of spaces, because a vault path may legally
// contain a space (and no fixture happens to exercise that today, so no
// Fields-based reader would be caught by an existing test). A line whose
// first 64 bytes are not lowercase hex, or whose separator is not exactly
// two spaces, is a corrupt snapshot: unlike the journal's skip-and-continue,
// a .tree entry is one file in the tree Revert rebuilds, and a silently
// dropped entry there is a lost file.
func ReadSnapshot(dir, commitID string) (Snapshot, error) {
	path := filepath.Join(dir, commitID+".tree")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("stage: read snapshot %s: %w", commitID, err)
	}

	s := make(Snapshot)
	lines := strings.Split(string(b), "\n")
	// Every line this package writes is "\n"-terminated, so splitting on
	// "\n" leaves exactly one trailing "" that is not a line at all — drop
	// that one only. A genuine blank line anywhere else still fails the
	// fixed-offset parse below and is reported as corruption, per D-AW.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		sha, p, err := parseSnapshotLine(line)
		if err != nil {
			return nil, fmt.Errorf("stage: read snapshot %s: line %d: %w", commitID, i+1, err)
		}
		s[p] = sha
	}
	return s, nil
}

// parseSnapshotLine parses one ".tree" line by fixed byte offsets (backbone
// §5.8, MASTER §9 D-AW). See ReadSnapshot's doc comment for why.
func parseSnapshotLine(line string) (sha, path string, err error) {
	if len(line) < 67 {
		return "", "", fmt.Errorf("malformed snapshot line %q", line)
	}
	sha = line[:64]
	if !isLowerHex64(sha) {
		return "", "", fmt.Errorf("malformed snapshot line %q: sha is not 64 lowercase hex characters", line)
	}
	if line[64:66] != "  " {
		return "", "", fmt.Errorf("malformed snapshot line %q: separator is not two spaces", line)
	}
	path = line[66:]
	return sha, path, nil
}

// isLowerHex64 reports whether s is exactly 64 lowercase hex characters.
func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Snapshot returns the recorded path→sha manifest for commitID.
//
// Contract (backbone §5.4, MASTER §9 D-AT): the thin wrapper
// ReadSnapshot(filepath.Join(e.llmwikiDir(), "snapshots"), commitID) —
// assigned here because cmd/lw (S2-T7) is a different package and cannot
// add a method to *Engine at all.
func (e *Engine) Snapshot(commitID string) (Snapshot, error) {
	return ReadSnapshot(filepath.Join(e.llmwikiDir(), "snapshots"), commitID)
}

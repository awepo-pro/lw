// id.go implements the two unexported id generators later waves call
// across the package (backbone §5's "unexported seams" Contract, MASTER §9
// D-AS). Owned by S2-T1.
package stage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"
)

// changesetIDPattern is what every newChangesetID output must match.
var changesetIDPattern = regexp.MustCompile(`^cs-[0-9a-f]{7}$`)

// newChangesetID returns a new changeset id: "cs-" followed by the first 7
// hex characters of sha256(now formatted as RFC3339Nano, followed by 8
// bytes read from r) (MASTER §9 D-H). now and r are injected — never
// time.Now() or a package rand source (00-conventions.md §3) — so callers'
// tests can fix the clock and the entropy source and get a reproducible id.
func newChangesetID(now time.Time, r io.Reader) (string, error) {
	entropy := make([]byte, 8)
	if _, err := io.ReadFull(r, entropy); err != nil {
		return "", fmt.Errorf("stage: new changeset id: %w", err)
	}

	h := sha256.New()
	h.Write([]byte(now.Format(time.RFC3339Nano)))
	h.Write(entropy)
	sum := hex.EncodeToString(h.Sum(nil))

	return "cs-" + sum[:7], nil
}

// treeFilePattern matches a snapshot file name, e.g. "000042.tree".
var treeFilePattern = regexp.MustCompile(`^(\d{6})\.tree$`)

// nextCommitID scans snapshotsDir for NNNNNN.tree files and returns the
// zero-padded six-digit successor of the highest one found. An absent or
// empty snapshotsDir yields "000001" — the id gate G2's `revert 000001`
// depends on.
func nextCommitID(snapshotsDir string) (string, error) {
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "000001", nil
		}
		return "", fmt.Errorf("stage: next commit id: %w", err)
	}

	highest := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := treeFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return fmt.Sprintf("%06d", highest+1), nil
}

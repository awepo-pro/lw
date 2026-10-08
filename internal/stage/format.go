// format.go implements the vault format gate (042 D4). A vault carries a
// version for its on-disk shape — changeset.json, the journal, the snapshot
// trees, session records, raw frontmatter — in an optional file,
// .llmwiki/format. lw sync copies a vault between PCs, so a PC running an
// older lw can end up holding a vault written by a newer one; opening it
// would misread the shape and then write back a mixture of the two. OpenEngine
// therefore refuses a vault newer than FormatVersion before it writes a byte,
// and TestFormatFingerprint (format_fingerprint_test.go) makes sure the
// version moves whenever the shape does.
package stage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// FormatVersion is the on-disk shape this lw reads and writes. It is 1: the
// shape every vault had before 042 existed. A change to anything the
// fingerprint test records means bumping this, adding a migration, and
// regenerating testdata/format-fingerprint.txt — in that order.
const FormatVersion = 1

// formatFileName is the file inside .llmwiki/ that records a vault's format.
// While FormatVersion is 1 lw writes none: a missing file means format 1, so
// every pre-042 vault, and lw init's output, stays byte-identical.
const formatFileName = "format"

// FormatError reports a vault written by an lw newer than this one: its
// format file says Have, this lw supports at most Max. cmd/lw prints the
// message verbatim, so it names the one thing the user can do.
type FormatError struct {
	Have, Max int
}

// Error implements error.
func (e *FormatError) Error() string {
	return fmt.Sprintf("vault format %d is newer than this lw supports (%d): upgrade lw", e.Have, e.Max)
}

// ReadFormat returns the format version recorded in llmwikiDir/format, the
// JSON object {"version": N}. A missing file — or a missing .llmwiki/ — is
// format 1, the shape of every vault that predates the file. A file that
// cannot be read, is not that object, or names a version below 1 is an
// error: guessing a damaged file's version could open a vault of unknown
// shape. Keys other than "version" are ignored so a later lw may add some.
func ReadFormat(llmwikiDir string) (int, error) {
	b, err := os.ReadFile(filepath.Join(llmwikiDir, formatFileName))
	if err != nil {
		// ENOTDIR: .llmwiki is itself a regular file. That is not a format
		// problem; OpenEngine reports it where it first needs the directory.
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return 1, nil
		}
		return 0, fmt.Errorf("read .llmwiki/format: %w", err)
	}
	var f struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return 0, fmt.Errorf("read .llmwiki/format: %w", err)
	}
	if f.Version < 1 {
		return 0, fmt.Errorf("read .llmwiki/format: version %d is not valid (it must be at least 1)", f.Version)
	}
	return f.Version, nil
}

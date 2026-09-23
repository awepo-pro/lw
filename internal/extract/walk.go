package extract

// walk.go implements 004 T1's Walk: the folder leg of `lw ingest`. The
// walker is deliberately stat-only — it never reads file contents. Whether
// a file is text is a property of the backend (004 §4.7): Walk only asks
// ex.CanHandle, so the eligible set is exactly the extractor registry and
// no extension list lives here. Content sniffing happens in
// fileExtractor.Extract via ErrNotText (F.E3), so a chain and a lone
// extractor always agree on what Walk selects.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Skipped is one file Walk did not select, and why.
type Skipped struct{ Path, Reason string }

// Walk lists every file under dir that ex can handle, in filepath.WalkDir
// (lexical) order, and every file or directory it passed over, with a
// reason. dir itself is never reported; a dot-file or dot-directory below
// it is hidden (a dot-directory is reported once and not descended), a
// symlink is never followed, and anything that is not a regular file,
// is empty, or ex cannot handle is skipped with the corresponding reason.
func Walk(dir string, ex Extractor) (files []string, skipped []Skipped, err error) {
	// WalkDir itself would happily treat a plain file as a one-entry root
	// (its root Lstat succeeds); 004 F.E1 wants that refused up front.
	info, err := os.Stat(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("walk: %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("walk: %s: not a directory", dir)
	}

	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		// The root is exempt from the dot rule, everything below it is not.
		if strings.HasPrefix(d.Name(), ".") {
			skipped = append(skipped, Skipped{path, "hidden"})
			if d.IsDir() {
				// Reported once, not descended — .git and friends are
				// skipped wholesale, not file by file.
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			skipped = append(skipped, Skipped{path, "symlink"})
			return nil
		}
		if d.IsDir() {
			// A non-hidden directory is descended into, never reported.
			return nil
		}
		if !d.Type().IsRegular() {
			skipped = append(skipped, Skipped{path, "not a regular file"})
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			skipped = append(skipped, Skipped{path, "empty"})
			return nil
		}
		if !ex.CanHandle(path) {
			skipped = append(skipped, Skipped{path, "unsupported type"})
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk: %s: %w", dir, err)
	}
	return files, skipped, nil
}

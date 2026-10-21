package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EntryState is where one catalog entry stands on this PC (066 D1).
//
// The catalog is the truth; storage/<name> is only this PC's copy of it. Two
// very different things used to share the one word "missing or corrupt" (B6): a
// payload that has not arrived yet and a payload that was damaged. The first
// is waiting, the second needs a person, and mixing them sent users to "restore
// from a backup" for a file another PC had not uploaded yet.
type EntryState int

const (
	// StateVerified: the file's sha256 and size equal the catalog's.
	StateVerified EntryState = iota
	// StatePending: lw can bring the right bytes — the file is absent, or it
	// holds an older version lw itself put there (EntryStatus.Stale).
	StatePending
	// StateCorrupt: bytes are present that match neither the catalog nor
	// anything lw put there. Never overwritten automatically.
	StateCorrupt
)

// String is the word `lw storage list -v` and `lw doctor` print.
func (s EntryState) String() string {
	switch s {
	case StateVerified:
		return "verified"
	case StatePending:
		return "pending"
	case StateCorrupt:
		return "corrupt"
	}
	return fmt.Sprintf("EntryState(%d)", int(s))
}

// EntryStatus is one catalog entry and the state of its bytes on this PC.
type EntryStatus struct {
	Entry Entry
	State EntryState
	Stale bool // pending because the on-disk bytes are an older version lw put there
}

// Report is Status's answer: every catalog entry, and the strays.
type Report struct {
	Entries []EntryStatus // catalog order: sorted by name
	Orphans []string      // slash paths relative to storage/, sorted
}

// ReconcileResult says what Reconcile did on this PC after the catalog moved.
type ReconcileResult struct {
	Removed []string // names deleted because the catalog no longer has them
	Kept    []string // names no longer in the catalog whose bytes changed locally: left on disk
	Adopted []string // catalog names found verified on disk without a ledger row
}

// diskFile is what is at storage/<name> right now.
type diskFile struct {
	present bool   // anything at all
	regular bool   // a regular file (a link to one counts), readable
	size    int64  // regular only
	sha     string // regular only: sha256 of its bytes
}

// pathOf is the file path of a clean name under storage/.
func (s *Storage) pathOf(clean string) string {
	return filepath.Join(s.storageDir(), filepath.FromSlash(clean))
}

// Path returns the absolute path of storage/<name> (name already clean). It is
// the one place the layout is spelled out, so callers that serve or hash a
// stored file (lw storage get, the doctor remedy) do not rebuild it.
func (s *Storage) Path(name string) string {
	dir := s.vaultDir
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Join(dir, "storage", filepath.FromSlash(name))
}

// inspect reads storage/<clean>. A file that cannot be statted or read is
// present but not regular: lw cannot vouch for it, so it is never verified and
// never overwritten.
func (s *Storage) inspect(clean string) diskFile {
	p := s.pathOf(clean)
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return diskFile{}
	}
	if err != nil || !fi.Mode().IsRegular() {
		return diskFile{present: true}
	}
	sha, size, err := hashFile(p)
	if err != nil {
		return diskFile{present: true}
	}
	return diskFile{present: true, regular: true, size: size, sha: sha}
}

// hashFile returns the sha256 and the length of the bytes in p.
func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// classify is the rule behind the states (066 D1): disk == catalog is verified;
// absent, or disk == what lw put here before the catalog moved on (the ledger),
// is pending; any other bytes are corrupt.
func classify(e Entry, d diskFile, row string, hasRow bool) (EntryState, bool) {
	switch {
	case !d.present:
		return StatePending, false
	case d.regular && d.sha == e.SHA256 && d.size == e.Size:
		return StateVerified, false
	case d.regular && hasRow && d.sha == row:
		return StatePending, true
	default:
		return StateCorrupt, false
	}
}

// cleanCatalogName reports whether name is already in the form CleanName
// returns: a name that is not could point anywhere, and is never touched on disk.
func (s *Storage) cleanCatalogName(name string) bool {
	clean, err := s.CleanName(name)
	return err == nil && clean == name
}

// sortedEntries is the catalog's entries by name, copied: another PC's commit
// need not have written them sorted.
func sortedEntries(m *Manifest) []Entry {
	out := make([]Entry, len(m.Entries))
	copy(out, m.Entries)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Status classifies every catalog entry against the disk and the ledger and
// lists the files in storage/ the catalog does not know (orphans). It only
// reads. It replaces Validate's single "missing or corrupt" verdict for doctor
// and `lw storage list -v` (066 R6, R7).
func (s *Storage) Status() (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m, err := s.loadManifest()
	if err != nil {
		return Report{}, err
	}
	l, err := s.loadLedger()
	if err != nil {
		return Report{}, err
	}
	var rep Report
	inCatalog := make(map[string]bool, len(m.Entries))
	for _, e := range sortedEntries(m) {
		inCatalog[e.Name] = true
		if !s.cleanCatalogName(e.Name) {
			rep.Entries = append(rep.Entries, EntryStatus{Entry: e, State: StateCorrupt})
			continue
		}
		row, has := l.Materialized[e.Name]
		state, stale := classify(e, s.inspect(e.Name), row, has)
		rep.Entries = append(rep.Entries, EntryStatus{Entry: e, State: state, Stale: stale})
	}
	rep.Orphans, err = s.orphans(inCatalog)
	if err != nil {
		return Report{}, err
	}
	return rep, nil
}

// orphans lists the regular files under storage/ whose name is not in the
// catalog. Dot-files, dot-directories and *.tmp names are not stored files
// (they are lw's own temp files and editor debris), as 062 already treated
// them; a symlink is not a stored file either. A storage/ that is itself a link
// to another disk is followed.
func (s *Storage) orphans(inCatalog map[string]bool) ([]string, error) {
	root := s.storageDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir // an unreadable subdirectory hides its files, it does not fail the report
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		if slash := filepath.ToSlash(rel); !inCatalog[slash] {
			out = append(out, slash)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("storage: scan storage/: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// Reconcile brings storage/ in line with the catalog after the catalog moved
// (a pull). It touches the disk only, never the network (066 D1, plan M4):
//
//   - a ledger name the catalog no longer lists is deleted iff the file still
//     holds the bytes lw put there (its sha equals the ledger row), then its
//     emptied parent directories go. This is what makes `lw storage rm` on one
//     PC reach the others (B1). A file whose bytes changed is kept and reported
//     — the user's edit is not lw's to destroy — and becomes an orphan;
//   - a catalog entry whose file already holds exactly the catalog's bytes but
//     has no (or an older) ledger row is adopted: files that v2.49.0 put there
//     have no row, and without one a later rm elsewhere could never delete them.
//
// The ledger row of every name handled is dropped, so a second call decides
// nothing. Ledger names are read from a file and are not trusted: one that
// CleanName would not return unchanged is dropped without touching the disk.
func (s *Storage) Reconcile() (ReconcileResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var res ReconcileResult
	m, err := s.loadManifest()
	if err != nil {
		return res, err
	}
	l, err := s.loadLedger()
	if err != nil {
		return res, err
	}
	// Every write of the catalog leaves the file in place, even with no entries,
	// so a missing file with rows in the ledger is not "everything was removed":
	// it was deleted or lost. Acting on it would delete every stored file.
	if _, err := os.Stat(s.manifestPath()); errors.Is(err, os.ErrNotExist) && len(l.Materialized) > 0 {
		return res, fmt.Errorf("storage: %s is missing but %d file(s) were stored here; restore it from git before syncing, nothing was deleted", s.manifestPath(), len(l.Materialized))
	}
	inCatalog := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		inCatalog[e.Name] = true
	}
	changed := false
	var firstErr error

	names := make([]string, 0, len(l.Materialized))
	for name := range l.Materialized {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if inCatalog[name] {
			continue
		}
		sha := l.Materialized[name]
		if s.cleanCatalogName(name) {
			d := s.inspect(name)
			switch {
			case !d.present:
				// already gone: nothing to delete, nothing to report
			case d.regular && d.sha == sha:
				if err := os.Remove(s.pathOf(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					if firstErr == nil {
						firstErr = fmt.Errorf("storage: remove %s: %w", name, err)
					}
					continue // keep the row: the next pass retries
				}
				s.pruneEmptyDirs(s.pathOf(name))
				res.Removed = append(res.Removed, name)
			default:
				res.Kept = append(res.Kept, name)
			}
		}
		delete(l.Materialized, name)
		changed = true
	}

	for _, e := range sortedEntries(m) {
		if row, has := l.Materialized[e.Name]; has && row == e.SHA256 {
			continue
		}
		if !s.cleanCatalogName(e.Name) {
			continue
		}
		// A cheap stat first: hashing a large file that cannot match is waste.
		if fi, err := os.Stat(s.pathOf(e.Name)); err != nil || !fi.Mode().IsRegular() || fi.Size() != e.Size {
			continue
		}
		if d := s.inspect(e.Name); d.regular && d.sha == e.SHA256 && d.size == e.Size {
			l.Materialized[e.Name] = e.SHA256
			res.Adopted = append(res.Adopted, e.Name)
			changed = true
		}
	}

	// The files go first, the ledger after: a crash in between leaves rows for
	// files that are already gone (dropped silently next time), never files
	// that lost their row and stay on disk forever.
	if changed {
		if err := s.saveLedger(l); err != nil {
			return res, err
		}
	}
	return res, firstErr
}

// Materialize writes r to storage/<name> for the catalog entry named name,
// verifying sha256 and size against the catalog before the rename, and records
// it in the ledger. It refuses an entry whose current state is StateCorrupt.
//
// The bytes land in a temp file beside the target and are renamed only when
// both checks pass, so a cut or tampered stream leaves nothing: no file, no
// ledger row, no temp file, no directory it had to create (R9). At most
// catalog-size+1 bytes are read, so a hostile remote cannot fill the disk.
func (s *Storage) Materialize(name string, r io.Reader) error {
	clean, err := s.CleanName(name)
	if err != nil {
		return fmt.Errorf("storage: invalid name %q: %w", name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	m, err := s.loadManifest()
	if err != nil {
		return err
	}
	var entry Entry
	found := false
	for _, e := range m.Entries {
		if e.Name == clean {
			entry, found = e, true
			break
		}
	}
	if !found {
		return fmt.Errorf("storage: entry %q is not in the catalog", clean)
	}
	l, err := s.loadLedger()
	if err != nil {
		return err
	}

	row, has := l.Materialized[clean]
	d := s.inspect(clean)
	if state, _ := classify(entry, d, row, has); state == StateCorrupt {
		why := fmt.Sprintf("its bytes are sha256 %s, the catalog says %s", d.sha, entry.SHA256)
		if !d.regular {
			why = "it is not a readable regular file"
		}
		return fmt.Errorf("storage: %s is corrupt on disk (%s); not overwriting it — restore the file or lw storage rm it", clean, why)
	}

	target := s.pathOf(clean)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("storage: create directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".storage-tmp-*")
	if err != nil {
		s.pruneEmptyDirs(target)
		return fmt.Errorf("storage: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			s.pruneEmptyDirs(target)
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, entry.Size+1))
	if err != nil {
		return fmt.Errorf("storage: %s: read stream: %w", clean, err)
	}
	if n != entry.Size {
		return fmt.Errorf("storage: %s: received %d bytes, the catalog says %d (sha256 %s)", clean, n, entry.Size, entry.SHA256)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != entry.SHA256 {
		return fmt.Errorf("storage: %s: sha256 mismatch (catalog %s, got %s)", clean, entry.SHA256, got)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("storage: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("storage: rename file: %w", err)
	}
	done = true
	if err := s.record(l, clean, entry.SHA256); err != nil {
		return fmt.Errorf("storage: %s was written but this PC's ledger could not be updated: %w", clean, err)
	}
	return nil
}

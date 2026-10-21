package storage

import (
	"context"
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
	"sync/atomic"
	"time"
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
	// anything lw put there; or the catalog's own record is unusable (a name that
	// escapes storage/, a sha256 that is not 64 lower-case hex digits). Never
	// overwritten automatically.
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

	// Damaged: pending, and a fetch of the catalog's object failed verification, so
	// the remote's copy is damaged rather than late (066 X-4).
	Damaged bool
	// Confirmed: the remote's object store is known to hold the catalog's object.
	Confirmed bool
	// DiskSHA is the sha256 of the file when this call read it; empty for an absent
	// or unreadable file, and for one trusted by StatusQuick without being read.
	DiskSHA string
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

	// Preserved are the Removed names whose copy on the remote was made by this
	// call: the file was the only copy, and was uploaded before it was deleted.
	Preserved []string
	// NoCopy are the names left on disk because no copy of their bytes could be
	// shown on the remote (rule P). Each is reported once per (name, sha256); a
	// transient failure keeps the ledger row, so the next call tries again.
	NoCopy []NoCopy
	// Skipped: a rebase is in progress, so nothing was decided (X-5).
	Skipped bool
	// Swept counts the stale temp files of killed fetches that were removed (X-10).
	Swept int
}

// NoCopy is one file Reconcile did not delete because the remote has no copy.
type NoCopy struct {
	Name   string
	Reason string
}

// Blob is a stored payload that is about to be destroyed or published: its name
// under storage/, the sha256 and size of its bytes, and the absolute path of the
// file that holds them.
type Blob struct {
	Name   string
	SHA256 string
	Size   int64
	Path   string
}

// EnsureResult is the verdict on one Blob.
type EnsureResult struct {
	Held      bool  // the remote's object store holds the object now
	Uploaded  bool  // ... because this call put it there (Held and not Uploaded: it was there)
	Err       error // not Held: why not
	Transient bool  // the remote could not be reached or written; the same call can succeed later
}

// Ensure proves that the remote holds a copy of each blob, uploading what it
// lacks, and answers per sha256. It is the one network call rule P needs, and it
// is passed in (cmd/lw owns the transport) so this package stays offline and a
// test can make the world fail. A blob missing from the answer counts as not held.
// A nil Ensure proves nothing.
type Ensure func(ctx context.Context, blobs []Blob) map[string]EnsureResult

// NoCopyError is what Materialize returns when it will not replace a stale file
// because the version it would destroy has no copy on the remote (rule P, F-1).
// The file is untouched and the stream was not read.
type NoCopyError struct {
	Name      string
	Reason    string
	Transient bool
}

func (e *NoCopyError) Error() string {
	return fmt.Sprintf("storage: kept %s: the replaced version has no copy on the remote yet (%s)", e.Name, e.Reason)
}

// VerifyError is what Materialize returns when the bytes it was given are not the
// catalog's: the wrong length or the wrong sha256. For a caller that fetches them
// from the remote that means the remote's object is damaged (066 X-4).
type VerifyError struct {
	Name string
	msg  string
}

func (e *VerifyError) Error() string { return e.msg }

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

// ValidSHA256 reports whether sha is a sha256 as the catalog writes it: 64
// lower-case hex digits. Anything else cannot name an object, and reaching a
// path or a remote shell with it is how one bad record used to fail a whole
// batch (X-8, F-6).
func ValidSHA256(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	for i := 0; i < len(sha); i++ {
		c := sha[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
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

// hashed counts the files hashFile has read in this process: a read-only probe
// for tests (HashCount) that pin the cost of a sync (X-6).
var hashed atomic.Int64

// HashCount reports how many stored files this process has read to hash them
// (Status, Verify, Reconcile, Materialize). Nothing in lw reads it; tests use the
// difference across a call to prove that a sync of unchanged files reads none.
func HashCount() int64 { return hashed.Load() }

// hashFile returns the sha256 and the length of the bytes in p.
func hashFile(p string) (string, int64, error) {
	hashed.Add(1)
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

// usable reports whether the catalog's record of e can be acted on at all: a
// name that stays inside storage/ and a sha256 that names an object. A record
// that fails is corrupt, is skipped on its own, and is never allowed to stop the
// rest (X-8).
func (s *Storage) usable(e Entry) bool {
	return s.cleanCatalogName(e.Name) && ValidSHA256(e.SHA256) && e.Size >= 0
}

// sortedEntries is the catalog's entries by name, copied: another PC's commit
// need not have written them sorted.
func sortedEntries(m *Manifest) []Entry {
	out := make([]Entry, len(m.Entries))
	copy(out, m.Entries)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// classifyEntry is the state of one catalog entry. With trust, a file that is
// where the ledger put it - the row holds the catalog's sha256 - and still has the
// catalog's size is taken for verified without being read (X-6): the ledger says lw
// wrote exactly these bytes, and a file of that size that nothing touched is them.
// The price is that an in-place edit that keeps the size goes unseen until a full
// check (Status, Verify: doctor, list -v, get) reads the file. Reading is what
// cost a 1 GiB payload 1.9 s on every lw sync.
func (s *Storage) classifyEntry(e Entry, l *ledger, trust bool) EntryStatus {
	es := EntryStatus{Entry: e}
	if !s.usable(e) {
		es.State = StateCorrupt
		return es
	}
	es.Confirmed = l.Confirmed[e.SHA256]
	row, has := l.Materialized[e.Name]
	if trust && has && row == e.SHA256 {
		if fi, err := os.Stat(s.pathOf(e.Name)); err == nil && fi.Mode().IsRegular() && fi.Size() == e.Size {
			es.State = StateVerified
			return es
		}
	}
	d := s.inspect(e.Name)
	es.DiskSHA = d.sha
	es.State, es.Stale = classify(e, d, row, has)
	es.Damaged = es.State == StatePending && l.Damaged[e.SHA256]
	return es
}

// Status classifies every catalog entry against the disk and the ledger and
// lists the files in storage/ the catalog does not know (orphans). It only
// reads, and it reads every stored file: it is the full check (doctor, list -v).
// It replaces Validate's single "missing or corrupt" verdict (066 R6, R7).
func (s *Storage) Status() (Report, error) { return s.status(false) }

// StatusQuick is Status for the sync paths: files the ledger vouches for are
// taken as verified without being read (classifyEntry), and the orphan walk is
// skipped. Orphans is nil.
func (s *Storage) StatusQuick() (Report, error) { return s.status(true) }

func (s *Storage) status(quick bool) (Report, error) {
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
		rep.Entries = append(rep.Entries, s.classifyEntry(e, l, quick))
	}
	if !quick {
		rep.Orphans, err = s.orphans(inCatalog)
		if err != nil {
			return Report{}, err
		}
	}
	return rep, nil
}

// Verify is the full check of one entry: it reads the file and says verified,
// pending (absent, or stale) or corrupt, with the sha256 it found. It is what
// `lw storage get` and `show` ask before they serve or describe a file, and the
// one place that spells out "is this file exactly the catalog's".
func (s *Storage) Verify(name string) (EntryStatus, error) {
	clean, err := s.CleanName(name)
	if err != nil {
		return EntryStatus{}, fmt.Errorf("storage: invalid name %q: %w", name, err)
	}
	m, err := s.loadManifest()
	if err != nil {
		return EntryStatus{}, err
	}
	l, err := s.loadLedger()
	if err != nil {
		return EntryStatus{}, err
	}
	for _, e := range m.Entries {
		if e.Name == clean {
			return s.classifyEntry(e, l, false), nil
		}
	}
	return EntryStatus{}, fmt.Errorf("storage: entry %q not found", clean)
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

// Outbox is what this PC could put on the remote.
type Outbox struct {
	// Blobs are the payloads the catalog lists that this PC holds exactly and the
	// remote is not yet known to hold: the question to ask the remote and the
	// upload to make. One per sha256 (two names with the same bytes are one object).
	Blobs []Blob
	// Unready are catalog entries whose file is here but is not the catalog's, so
	// nothing can be uploaded for them (X-9): the user is told once per run.
	Unready []Unready
}

// Unready is a catalog entry that cannot be uploaded from this PC, and why.
type Unready struct {
	Name string
	Why  string
}

// Outbox lists what an upload should consider (X-6). An entry whose object is
// confirmed on the remote is not listed - the answer is already known, so a sync
// of unchanged files asks the remote nothing - and an absent or stale file is not
// this PC's to upload. A file the ledger vouches for is not read; the upload's own
// hashing of what it sends catches a same-size edit.
func (s *Storage) Outbox() (Outbox, error) {
	m, err := s.loadManifest()
	if err != nil {
		return Outbox{}, err
	}
	l, err := s.loadLedger()
	if err != nil {
		return Outbox{}, err
	}
	var ob Outbox
	seen := map[string]bool{}
	for _, e := range sortedEntries(m) {
		if !s.usable(e) || l.Confirmed[e.SHA256] {
			continue
		}
		es := s.classifyEntry(e, l, true)
		switch es.State {
		case StateVerified:
			if !seen[e.SHA256] {
				seen[e.SHA256] = true
				ob.Blobs = append(ob.Blobs, Blob{Name: e.Name, SHA256: e.SHA256, Size: e.Size, Path: s.Path(e.Name)})
			}
		case StateCorrupt:
			got := "it is not a readable regular file"
			if es.DiskSHA != "" {
				got = "its sha256 is " + es.DiskSHA
			}
			ob.Unready = append(ob.Unready, Unready{Name: e.Name, Why: fmt.Sprintf("storage/%s is not the file the catalog describes (%s, the catalog says %s)", e.Name, got, e.SHA256)})
		}
	}
	return ob, nil
}

// now is the clock of the temp sweep, a variable so it is one place to pin.
var now = time.Now

// staleTempAge is how old a .storage-tmp-* file must be before Reconcile treats
// it as the debris of a killed fetch. A fetch in progress is minutes old at most
// for any payload a sync would move; an hour is slow enough to be safe.
const staleTempAge = time.Hour

// sweepTemps removes the .storage-tmp-* files under storage/ that are older than
// staleTempAge, and the directories that leaves empty. A fetch that was killed
// (Ctrl-C, a power cut) leaves its temp file; it is dot-prefixed, so neither the
// orphan scan nor doctor ever mentioned it, and it was never cleaned up (X-10,
// F-9). Only lw's own prefix is touched.
func (s *Storage) sweepTemps() int {
	cutoff := now().Add(-staleTempAge)
	n := 0
	filepath.WalkDir(s.storageDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), ".storage-tmp-") || !d.Type().IsRegular() {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().Before(cutoff) {
			if os.Remove(p) == nil {
				n++
				s.pruneEmptyDirs(p)
			}
		}
		return nil
	})
	return n
}

// removeStored deletes storage/<name> and the directories that leaves empty.
func (s *Storage) removeStored(name string) error {
	p := s.pathOf(name)
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: remove %s: %w", name, err)
	}
	s.pruneEmptyDirs(p)
	return nil
}

// Reconcile brings storage/ in line with the catalog after the catalog moved
// (a pull) (066 D1, plan M4):
//
//   - a ledger name the catalog no longer lists is deleted iff the file still
//     holds the bytes lw put there (its sha equals the ledger row) AND a copy of
//     those bytes is known to be on the remote (rule P, B7): either the sha is
//     confirmed in the ledger - then it needs no network - or ensure uploads it
//     now. This is what makes `lw storage rm` on one PC reach the others (B1)
//     without ever deleting the only copy of anything. A file whose bytes changed
//     is kept and reported - the user's edit is not lw's to destroy - and becomes
//     an orphan. A file with no copy on the remote is kept too (NoCopy): on a
//     transient failure with its ledger row, so the next call retries and the
//     line is said once; on a permanent one without, so it is an orphan;
//   - a catalog entry whose file already holds exactly the catalog's bytes but
//     has no (or an older) ledger row is adopted: files that v2.49.0 put there
//     have no row, and without one a later rm elsewhere could never delete them;
//   - lw's own temp files older than an hour are swept (X-10);
//   - while a rebase is in progress nothing is decided (Skipped, X-5).
//
// The decision is made in three steps so the network is never under the lock and
// never trusted across a pause: plan (lock): decide what needs no network and list
// the blobs that need a copy shown; ensure (no lock): ask the remote, once, for all
// of them; settle (lock): re-read the catalog, the ledger and every file, and
// destroy only what is still exactly what was planned (X-3). The ledger row of
// every name decided is dropped, so a second call decides nothing. Ledger names
// are read from a file and are not trusted: one that CleanName would not return
// unchanged is dropped without touching the disk.
func (s *Storage) Reconcile(ctx context.Context, ensure Ensure) (ReconcileResult, error) {
	var res ReconcileResult
	if s.rebaseInProgress() {
		res.Skipped = true
		return res, nil
	}
	res.Swept = s.sweepTemps()
	// A vault that never stored anything has nothing to reconcile and gets no
	// lock file or ledger from this call: Reconcile runs after every pull of every
	// synced vault (042's tests pin that such a vault is left as it was).
	if _, err := os.Stat(s.ledgerPath()); errors.Is(err, os.ErrNotExist) {
		if m, err := s.loadManifest(); err == nil && len(m.Entries) == 0 {
			return res, nil
		}
	}
	need, firstErr, err := s.reconcilePlan(&res)
	if err != nil {
		return res, err
	}
	if len(need) > 0 {
		var answers map[string]EnsureResult
		if ensure != nil {
			answers = ensure(ctx, need)
		}
		if err := s.reconcileSettle(&res, need, answers, ensure == nil, &firstErr); err != nil {
			return res, err
		}
	}
	sort.Strings(res.Removed)
	sort.Strings(res.Kept)
	sort.Strings(res.Adopted)
	sort.Strings(res.Preserved)
	sort.Slice(res.NoCopy, func(i, j int) bool { return res.NoCopy[i].Name < res.NoCopy[j].Name })
	return res, firstErr
}

// reconcilePlan is Reconcile's first step. It does everything that needs no
// network and returns the blobs that do.
func (s *Storage) reconcilePlan(res *ReconcileResult) (need []Blob, firstErr, err error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, nil, err
	}
	defer unlock()

	m, err := s.loadManifest()
	if err != nil {
		return nil, nil, err
	}
	l, err := s.loadLedger()
	if err != nil {
		return nil, nil, err
	}
	// Every write of the catalog leaves the file in place, even with no entries,
	// so a missing file with rows in the ledger is not "everything was removed":
	// it was deleted or lost. Acting on it would delete every stored file.
	if _, err := os.Stat(s.manifestPath()); errors.Is(err, os.ErrNotExist) && len(l.Materialized) > 0 {
		return nil, nil, fmt.Errorf("storage: %s is missing but %d file(s) were stored here; restore it from git before syncing, nothing was deleted", s.manifestPath(), len(l.Materialized))
	}
	inCatalog := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		inCatalog[e.Name] = true
	}
	changed := false

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
				if !l.Confirmed[sha] {
					// lw wrote these bytes; nothing says a copy exists. They wait for the
					// remote to be asked (and keep their row until it has answered).
					need = append(need, Blob{Name: name, SHA256: sha, Size: d.size, Path: s.Path(name)})
					continue
				}
				if err := s.removeStored(name); err != nil {
					if firstErr == nil {
						firstErr = err
					}
					continue // keep the row: the next pass retries
				}
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
		if !s.usable(e) {
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
	if l.pruneTold() || changed || l.scopeReset {
		if err := s.saveLedger(l); err != nil {
			return nil, firstErr, err
		}
	}
	return need, firstErr, nil
}

// reconcileSettle is Reconcile's last step. The world may have moved while the
// remote was being asked, so nothing planned is trusted: the catalog and the
// ledger are read again, a name that is catalogued again or whose row changed is
// left to the next call, and a file is deleted only if it is, right now, still
// the bytes the plan was made for (X-3: the upload took time, and the user may
// have edited the file in it).
func (s *Storage) reconcileSettle(res *ReconcileResult, need []Blob, answers map[string]EnsureResult, noEnsure bool, firstErr *error) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()

	m, err := s.loadManifest()
	if err != nil {
		return err
	}
	l, err := s.loadLedger()
	if err != nil {
		return err
	}
	// A rebase can begin while the remote is being asked; the catalog is then
	// mid-replay and says nothing reliable. Nothing is decided: every row stays for
	// the next call. What the remote said is still true, though, so the copies it
	// holds are recorded and the next call needs no network for them.
	if s.rebaseInProgress() {
		res.Skipped = true
		held := false
		for _, b := range need {
			if answers[b.SHA256].Held {
				l.Confirmed[b.SHA256] = true
				delete(l.Damaged, b.SHA256)
				held = true
			}
		}
		if held || l.scopeReset {
			return s.saveLedger(l)
		}
		return nil
	}
	inCatalog := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		inCatalog[e.Name] = true
	}
	changed := false
	for _, b := range need {
		if row, has := l.Materialized[b.Name]; !has || row != b.SHA256 || inCatalog[b.Name] {
			continue
		}
		r, ok := answers[b.SHA256]
		switch {
		case noEnsure:
			r = EnsureResult{Err: errors.New("no remote to ask"), Transient: true}
		case !ok:
			r = EnsureResult{Err: errors.New("the remote gave no answer for it"), Transient: true}
		}
		if r.Held {
			l.Confirmed[b.SHA256] = true
			delete(l.Damaged, b.SHA256)
			changed = true
			d := s.inspect(b.Name)
			switch {
			case !d.present:
				// gone while the copy was being made: nothing to delete
			case d.regular && d.sha == b.SHA256 && d.size == b.Size:
				if err := s.removeStored(b.Name); err != nil {
					if *firstErr == nil {
						*firstErr = err
					}
					continue // keep the row: the next pass retries
				}
				res.Removed = append(res.Removed, b.Name)
				if r.Uploaded {
					res.Preserved = append(res.Preserved, b.Name)
				}
			default:
				res.Kept = append(res.Kept, b.Name)
			}
			delete(l.Materialized, b.Name)
			continue
		}

		reason := "the remote did not say why"
		if r.Err != nil {
			reason = oneLine(r.Err.Error())
		}
		if r.Transient {
			// The file and its row stay: a later call retries. Say so once.
			if key := toldKey(b.Name, b.SHA256); !l.Told[key] {
				l.Told[key] = true
				res.NoCopy = append(res.NoCopy, NoCopy{Name: b.Name, Reason: reason})
				changed = true
			}
			continue
		}
		res.NoCopy = append(res.NoCopy, NoCopy{Name: b.Name, Reason: reason})
		delete(l.Materialized, b.Name) // permanent: the file is an orphan now
		changed = true
	}
	if l.pruneTold() || changed || l.scopeReset {
		return s.saveLedger(l)
	}
	return nil
}

// oneLine collapses err text to a single line of single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Materialize writes r to storage/<name> for the catalog entry named name,
// verifying sha256 and size against the catalog before the rename, and records
// it in the ledger. It refuses an entry whose current state is StateCorrupt, and
// an entry whose catalog record is unusable.
//
// r is the remote's object (that is the only caller), so the new bytes are
// recorded as confirmed. The bytes land in a temp file beside the target and are
// renamed only when both checks pass, so a cut or tampered stream leaves nothing:
// no file, no ledger row, no temp file, no directory it had to create (R9). At
// most catalog-size+1 bytes are read, so a hostile remote cannot fill the disk.
//
// Rule P (F-1): replacing a stale file destroys the version lw put there. Unless
// that version's sha256 is confirmed on the remote, ensure must first show a copy
// (uploading it if need be); if it cannot, the file is left untouched, r is not
// read and the error is a *NoCopyError. A nil ensure cannot, so it refuses.
//
// Nothing is held across the download: the state of the target is read before
// it and again immediately before the rename (X-3), and a file that appeared or
// changed in between is not overwritten.
func (s *Storage) Materialize(ctx context.Context, name string, r io.Reader, ensure Ensure) error {
	clean, err := s.CleanName(name)
	if err != nil {
		return fmt.Errorf("storage: invalid name %q: %w", name, err)
	}

	// Plan.
	entry, before, preserve, err := s.materializePlan(clean)
	if err != nil {
		return err
	}
	if preserve != nil {
		var res EnsureResult
		switch {
		case ensure == nil:
			res = EnsureResult{Err: errors.New("no remote to ask"), Transient: true}
		default:
			var ok bool
			if res, ok = ensure(ctx, []Blob{*preserve})[preserve.SHA256]; !ok {
				res = EnsureResult{Err: errors.New("the remote gave no answer for it"), Transient: true}
			}
		}
		if !res.Held {
			reason := "the remote did not say why"
			if res.Err != nil {
				reason = oneLine(res.Err.Error())
			}
			return &NoCopyError{Name: clean, Reason: reason, Transient: res.Transient}
		}
	}

	// Work, with no lock.
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
		return &VerifyError{Name: clean, msg: fmt.Sprintf("storage: %s: received %d bytes, the catalog says %d (sha256 %s)", clean, n, entry.Size, entry.SHA256)}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != entry.SHA256 {
		return &VerifyError{Name: clean, msg: fmt.Sprintf("storage: %s: sha256 mismatch (catalog %s, got %s)", clean, entry.SHA256, got)}
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("storage: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close temp file: %w", err)
	}

	// Settle.
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	m, err := s.loadManifest()
	if err != nil {
		return err
	}
	var cur *Entry
	for i := range m.Entries {
		if m.Entries[i].Name == clean {
			cur = &m.Entries[i]
		}
	}
	if cur == nil || cur.SHA256 != entry.SHA256 || cur.Size != entry.Size {
		return fmt.Errorf("storage: %s: the catalog changed while it was being fetched; not writing it", clean)
	}
	l, err := s.loadLedger()
	if err != nil {
		return err
	}
	if after := s.inspect(clean); after != before {
		return fmt.Errorf("storage: %s changed on disk while it was being fetched; not overwriting it", clean)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("storage: rename file: %w", err)
	}
	done = true
	l.Materialized[clean] = entry.SHA256
	l.Confirmed[entry.SHA256] = true
	delete(l.Damaged, entry.SHA256)
	if preserve != nil {
		l.Confirmed[preserve.SHA256] = true
	}
	l.pruneTold()
	if err := s.saveLedger(l); err != nil {
		return fmt.Errorf("storage: %s was written but this PC's ledger could not be updated: %w", clean, err)
	}
	return nil
}

// materializePlan is Materialize's first step: the catalog entry, what is on disk
// now, and - for a stale file whose version has no confirmed copy - the blob that
// must be shown a copy before it is replaced.
func (s *Storage) materializePlan(clean string) (entry Entry, before diskFile, preserve *Blob, err error) {
	unlock, err := s.lock()
	if err != nil {
		return Entry{}, diskFile{}, nil, err
	}
	defer unlock()

	m, err := s.loadManifest()
	if err != nil {
		return Entry{}, diskFile{}, nil, err
	}
	found := false
	for _, e := range m.Entries {
		if e.Name == clean {
			entry, found = e, true
			break
		}
	}
	if !found {
		return Entry{}, diskFile{}, nil, fmt.Errorf("storage: entry %q is not in the catalog", clean)
	}
	if !s.usable(entry) {
		return Entry{}, diskFile{}, nil, fmt.Errorf("storage: %s: the catalog's record is unusable (sha256 %q is not 64 lower-case hex digits, or the size is negative); not fetching it", clean, entry.SHA256)
	}
	l, err := s.loadLedger()
	if err != nil {
		return Entry{}, diskFile{}, nil, err
	}

	row, has := l.Materialized[clean]
	d := s.inspect(clean)
	state, stale := classify(entry, d, row, has)
	if state == StateCorrupt {
		why := fmt.Sprintf("its bytes are sha256 %s, the catalog says %s", d.sha, entry.SHA256)
		if !d.regular {
			why = "it is not a readable regular file"
		}
		return Entry{}, diskFile{}, nil, fmt.Errorf("storage: %s is corrupt on disk (%s); not overwriting it — restore the file or lw storage rm it", clean, why)
	}
	if stale && !l.Confirmed[row] {
		preserve = &Blob{Name: clean, SHA256: row, Size: d.size, Path: s.Path(clean)}
	}
	return entry, d, preserve, nil
}

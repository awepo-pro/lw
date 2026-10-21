package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Manifest represents the catalog of stored files in .llmwiki/storage.json.
type Manifest struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Entry describes one stored file.
type Entry struct {
	Name   string    `json:"name"`
	SHA256 string    `json:"sha256"`
	Size   int64     `json:"size"`
	Added  time.Time `json:"added"`
	Note   string    `json:"note,omitempty"`
}

// Storage manages non-wiki storage files in a vault. A handle holds no state
// between calls but the vault's path and the remote its confirmations refer to
// (BindRemote): the catalog, the ledger and the disk are re-read by every call,
// and the writers serialize on a file lock (lock.go), not on the handle - every
// lw process has handles of its own.
type Storage struct {
	vaultDir string
	remote   string // see BindRemote
}

// Open creates a new Storage handle for vaultDir.
func Open(vaultDir string) (*Storage, error) {
	if vaultDir == "" {
		return nil, errors.New("storage: vaultDir cannot be empty")
	}
	return &Storage{vaultDir: filepath.Clean(vaultDir)}, nil
}

// CleanName validates and sanitizes a storage entry name.
func (s *Storage) CleanName(name string) (string, error) {
	if name == "" {
		return "", errors.New("name cannot be empty")
	}
	if strings.ContainsRune(name, 0) {
		return "", errors.New("name cannot contain null bytes")
	}
	if strings.Contains(name, "..") {
		return "", errors.New("name cannot contain '..'")
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") || filepath.IsAbs(name) {
		return "", errors.New("name cannot be an absolute path")
	}

	slashed := filepath.ToSlash(name)
	cleaned := path.Clean(slashed)
	if cleaned == "." || cleaned == "/" || strings.HasPrefix(cleaned, "/") {
		return "", errors.New("invalid storage path")
	}

	storageBase := filepath.Clean(filepath.Join(s.vaultDir, "storage"))
	targetPath := filepath.Clean(filepath.Join(storageBase, filepath.FromSlash(cleaned)))
	rel, err := filepath.Rel(storageBase, targetPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("target path escapes storage directory")
	}

	return cleaned, nil
}

func (s *Storage) manifestPath() string {
	return filepath.Join(s.vaultDir, ".llmwiki", "storage.json")
}

func (s *Storage) storageDir() string {
	return filepath.Join(s.vaultDir, "storage")
}

func (s *Storage) loadManifest() (*Manifest, error) {
	m, _, err := s.readManifest()
	return m, err
}

// readManifest is loadManifest that also says whether the catalog file was there:
// a missing file reads as an empty catalog, and the one caller that must tell the
// two apart (Reconcile) gets both from the single read, not from a read and a
// stat that a checkout can slip between (066 Y-6, G-6).
func (s *Storage) readManifest() (m *Manifest, exists bool, err error) {
	path := s.manifestPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Manifest{Version: 1, Entries: []Entry{}}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("storage: read manifest %s: %w", path, err)
	}
	m = &Manifest{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, false, fmt.Errorf("storage: parse manifest %s: %w", path, err)
	}
	if m.Version == 0 {
		m.Version = 1
	}
	if m.Entries == nil {
		m.Entries = []Entry{}
	}
	return m, true, nil
}

func (s *Storage) saveManifest(m *Manifest) error {
	m.Version = 1
	sort.Slice(m.Entries, func(i, j int) bool {
		return m.Entries[i].Name < m.Entries[j].Name
	})

	dir := filepath.Dir(s.manifestPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage: create manifest directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".storage-json-tmp-*")
	if err != nil {
		return fmt.Errorf("storage: create temp manifest: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: marshal manifest: %w", err)
	}
	data = append(data, '\n')

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: write temp manifest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: sync temp manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close temp manifest: %w", err)
	}

	if err := os.Rename(tmpName, s.manifestPath()); err != nil {
		return fmt.Errorf("storage: rename manifest: %w", err)
	}
	tmpName = ""
	return nil
}

// tempPrefix starts the name of every temp file lw makes under storage/ (a copy
// being stored, a payload being fetched). Reconcile sweeps those that are old, so
// a stored file may not carry the prefix (reservedName).
const tempPrefix = ".storage-tmp-"

// reservedName reports whether clean, a name under storage/, is one lw keeps for
// its own temp files: the sweep of killed fetches deletes an old regular file by
// that name, and a payload called .storage-tmp-keep was deleted as debris an hour
// after it was stored (066 Y-2, G-2).
func reservedName(clean string) bool { return strings.HasPrefix(path.Base(clean), tempPrefix) }

// addHook runs inside Add's copy of the source, after the hash that decided what
// to do and before the first byte is read: the window in which a source can
// change (X-9), and the one in which a test can look at what else is allowed to
// happen while a large copy is under way (Y-6). Tests set it.
var addHook func()

// stageCopy copies srcPath into a temp file beside destPath and returns the temp's
// name, only if the bytes copied are exactly wantSize bytes of sha256 wantSHA - the
// hash and size Add computed from the source a moment earlier and is about to write
// into the catalog. The first version hashed the source and then copied it in a
// second pass, so a source that changed in between (an editor saving, a download
// still writing) was stored as bytes the catalog did not describe, and the object
// store then refused the file forever (066 X-9, F-8). Nothing is left behind on an
// error. It takes no lock: it is the slow part of Add, and the caller renames the
// result into place under one (Y-6).
func (s *Storage) stageCopy(srcPath, destPath, wantSHA string, wantSize int64) (string, error) {
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("storage: create directory: %w", err)
	}

	tmp, err := os.CreateTemp(destDir, tempPrefix+"*")
	if err != nil {
		s.pruneEmptyDirs(destPath)
		return "", fmt.Errorf("storage: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			s.pruneEmptyDirs(destPath)
		}
	}()

	if addHook != nil {
		addHook()
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("storage: open source file: %w", err)
	}
	defer src.Close()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), src)
	if err != nil {
		return "", fmt.Errorf("storage: copy file content: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); n != wantSize || got != wantSHA {
		return "", fmt.Errorf("storage: %s changed while it was being stored (sha256 %s, %d bytes when read; %s, %d bytes as copied); nothing was stored", srcPath, wantSHA, wantSize, got, n)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("storage: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("storage: close temp file: %w", err)
	}
	done = true
	return tmpName, nil
}

// expectsCopy is the guess that lets Add copy before it takes the lock: yes when
// nothing is at the target (a new entry, or a pending one being healed) or the
// caller said --force, unless the source already IS the target. The lock decides for
// good; a wrong guess costs a discarded copy, or one made under the lock.
func expectsCopy(src os.FileInfo, target string, force bool) bool {
	tfi, err := os.Stat(target)
	if err == nil && os.SameFile(src, tfi) {
		return false
	}
	return force || err != nil
}

// Add copies srcPath to storage/<name> and records it in the manifest and in
// this PC's ledger (066: the ledger says lw put these bytes here, which is what
// lets a later removal on another PC delete them). Order matters for a crash:
// the file first, then the catalog, then the ledger row. The copy is verified
// against the hash the catalog will carry before anything moves (X-9).
//
// The copy is the slow part and is made outside the storage lock, to a temp file
// in storage/; the lock (X-5) covers only the re-read of the catalog and the
// ledger, the decision, the rename and the two writes (Y-6). A second verb is
// therefore never kept waiting for a large file to be copied.
//
// A new catalog entry never replaces a file that is already at storage/<name>
// and is not the catalog's: such a file is one Reconcile kept because it holds the
// user's edit, or one a sync of an older lw left, and renaming over it destroyed
// bytes that exist nowhere else. The same bytes are adopted without a copy; other
// bytes are refused (Y-1, G-1). That is a statement about a NEW entry, whatever
// --force says: --force is about an entry that exists.
//
// Add --force over a tracked entry and Remove are the user's own verbs: they
// replace or delete bytes on request and are exempt from rule P, which is about
// what lw does on its own.
func (s *Storage) Add(srcPath string, customName string, note string, force bool) (Entry, error) {
	fi, err := os.Stat(srcPath)
	if err != nil {
		return Entry{}, fmt.Errorf("storage: stat source file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return Entry{}, fmt.Errorf("storage: %s is not a regular file", srcPath)
	}

	name := customName
	if name == "" {
		name = filepath.Base(srcPath)
	}
	clean, err := s.CleanName(name)
	if err != nil {
		return Entry{}, fmt.Errorf("storage: invalid name %q: %w", name, err)
	}
	if reservedName(clean) {
		return Entry{}, fmt.Errorf("storage: invalid name %q: names starting with %s are reserved for lw's own temp files", name, tempPrefix)
	}

	f, err := os.Open(srcPath)
	if err != nil {
		return Entry{}, fmt.Errorf("storage: open source file: %w", err)
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	_ = f.Close()
	if err != nil {
		return Entry{}, fmt.Errorf("storage: hash source file: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	targetPath := filepath.Join(s.storageDir(), filepath.FromSlash(clean))

	// staged is the verified copy waiting to be renamed into place; whatever is left
	// of it when Add returns is thrown away.
	var staged string
	defer func() {
		if staged != "" {
			_ = os.Remove(staged)
			s.pruneEmptyDirs(targetPath)
		}
	}()
	if expectsCopy(fi, targetPath, force) {
		if staged, err = s.stageCopy(srcPath, targetPath, sum, size); err != nil {
			return Entry{}, err
		}
	}
	// place puts the verified copy at storage/<name>. It copies now if the guess
	// above was wrong, which is the only time the lock is held across a copy.
	place := func() error {
		if staged == "" {
			var err error
			if staged, err = s.stageCopy(srcPath, targetPath, sum, size); err != nil {
				return err
			}
		}
		if err := os.Rename(staged, targetPath); err != nil {
			return fmt.Errorf("storage: rename file: %w", err)
		}
		staged = ""
		return nil
	}

	unlock, err := s.lock()
	if err != nil {
		return Entry{}, err
	}
	defer unlock()

	m, err := s.loadManifest()
	if err != nil {
		return Entry{}, err
	}
	// Read the ledger before changing anything: an unreadable one stops Add
	// here, not after the file and the catalog already moved.
	led, err := s.loadLedger()
	if err != nil {
		return Entry{}, err
	}

	// Check if entry already exists in manifest
	idx := -1
	for i, e := range m.Entries {
		if e.Name == clean {
			idx = i
			break
		}
	}

	if idx >= 0 {
		existing := m.Entries[idx]
		if existing.SHA256 == sum {
			// Identical SHA256: the catalog already describes these bytes. If the file is
			// absent they are what this PC is waiting for, and the user is holding them:
			// put them there (066 X-4, F-7). Without this a PC with the right bytes in
			// hand could not repair its own pending entry, nor, by uploading them, a
			// damaged object on the remote. A file that is there is left alone, whatever
			// it holds: healing an absent file is not licence to replace somebody's bytes.
			if _, err := os.Lstat(targetPath); errors.Is(err, os.ErrNotExist) {
				if err := place(); err != nil {
					return Entry{}, err
				}
				if err := s.recordAdded(led, clean, sum); err != nil {
					return Entry{}, err
				}
			}
			// update the note if one was given
			if note != "" && existing.Note != note {
				existing.Note = note
				m.Entries[idx] = existing
				if err := s.saveManifest(m); err != nil {
					return Entry{}, err
				}
			}
			return existing, nil
		}
		// Different SHA256
		if !force {
			return Entry{}, fmt.Errorf("storage: entry %q already exists with different sha256 (use --force to overwrite)", clean)
		}

		// Force overwrite
		targetFi, err := os.Stat(targetPath)
		if err != nil || !os.SameFile(fi, targetFi) {
			if err := place(); err != nil {
				return Entry{}, err
			}
		}

		entryNote := note
		if entryNote == "" {
			entryNote = existing.Note
		}
		entry := Entry{
			Name:   clean,
			SHA256: sum,
			Size:   size,
			Added:  time.Now().UTC(),
			Note:   entryNote,
		}
		m.Entries[idx] = entry
		if err := s.saveManifest(m); err != nil {
			return Entry{}, err
		}
		if err := s.recordAdded(led, clean, sum); err != nil {
			return Entry{}, err
		}
		return entry, nil
	}

	// New entry. Anything already at the target is not in the catalog (Y-1): the same
	// bytes - the source itself among them - are taken as they are; others are refused.
	if _, err := os.Lstat(targetPath); err == nil {
		if d := s.inspect(clean); !d.regular || d.sha != sum || d.size != size {
			return Entry{}, fmt.Errorf("storage: storage/%s exists and is not in the catalog (kept from an earlier sync?) — move it out of storage/, or add it under another --name", clean)
		}
	} else if err := place(); err != nil {
		return Entry{}, err
	}

	entry := Entry{
		Name:   clean,
		SHA256: sum,
		Size:   size,
		Added:  time.Now().UTC(),
		Note:   note,
	}
	m.Entries = append(m.Entries, entry)
	if err := s.saveManifest(m); err != nil {
		return Entry{}, err
	}
	if err := s.recordAdded(led, clean, sum); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// recordAdded puts the bytes Add just stored into this PC's ledger (066), after
// the file and the catalog are in place. A --force replacement overwrites the
// old row, so the old bytes can no longer be mistaken for "what lw put here"
// (B2: the replaced file is verified here and stale on the other PCs).
func (s *Storage) recordAdded(led *ledger, clean, sum string) error {
	if err := s.record(led, clean, sum); err != nil {
		return fmt.Errorf("storage: %q is catalogued, but this PC's ledger could not be updated: %w", clean, err)
	}
	return nil
}

// List returns all entries in the storage manifest, sorted by name.
func (s *Storage) List() ([]Entry, error) {
	m, err := s.loadManifest()
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, len(m.Entries))
	copy(entries, m.Entries)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// Show returns the entry for the given name, and whether it exists in the manifest.
func (s *Storage) Show(name string) (Entry, bool, error) {
	clean, err := s.CleanName(name)
	if err != nil {
		return Entry{}, false, fmt.Errorf("storage: invalid name %q: %w", name, err)
	}

	m, err := s.loadManifest()
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range m.Entries {
		if e.Name == clean {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// Get returns a reader for the stored file content.
func (s *Storage) Get(name string) (io.ReadCloser, Entry, error) {
	clean, err := s.CleanName(name)
	if err != nil {
		return nil, Entry{}, fmt.Errorf("storage: invalid name %q: %w", name, err)
	}

	m, err := s.loadManifest()
	if err != nil {
		return nil, Entry{}, err
	}
	var entry Entry
	found := false
	for _, e := range m.Entries {
		if e.Name == clean {
			entry = e
			found = true
			break
		}
	}

	if !found {
		return nil, Entry{}, fmt.Errorf("storage: entry %q not found", clean)
	}

	targetPath := filepath.Join(s.storageDir(), filepath.FromSlash(clean))
	f, err := os.Open(targetPath)
	if err != nil {
		return nil, Entry{}, fmt.Errorf("storage: open %s: %w", clean, err)
	}
	return f, entry, nil
}

// Remove deletes the file and its entry from the manifest and drops its ledger
// row. Empty parent directories under storage/ are cleaned up. Other PCs learn
// of the removal from the catalog and delete their copy in Reconcile (066 B1).
func (s *Storage) Remove(name string) error {
	clean, err := s.CleanName(name)
	if err != nil {
		return fmt.Errorf("storage: invalid name %q: %w", name, err)
	}

	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()

	m, err := s.loadManifest()
	if err != nil {
		return err
	}
	led, err := s.loadLedger()
	if err != nil {
		return err
	}
	idx := -1
	for i, e := range m.Entries {
		if e.Name == clean {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("storage: entry %q not found", clean)
	}

	m.Entries = append(m.Entries[:idx], m.Entries[idx+1:]...)
	if err := s.saveManifest(m); err != nil {
		return err
	}

	targetPath := filepath.Join(s.storageDir(), filepath.FromSlash(clean))
	if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: remove file: %w", err)
	}
	s.pruneEmptyDirs(targetPath)

	// The row goes last. A crash before this line leaves a row for a name the
	// catalog no longer lists and a file already gone, which Reconcile drops.
	if _, ok := led.Materialized[clean]; ok {
		delete(led.Materialized, clean)
		if err := s.saveLedger(led); err != nil {
			return fmt.Errorf("storage: %q was removed, but this PC's ledger could not be updated: %w", clean, err)
		}
	}
	return nil
}

// pruneEmptyDirs removes the empty parent directories of targetPath, nearest
// first, stopping at storage/ itself (which stays).
func (s *Storage) pruneEmptyDirs(targetPath string) {
	storageBase := filepath.Clean(s.storageDir())
	dir := filepath.Dir(targetPath)
	for dir != storageBase && strings.HasPrefix(dir, storageBase+string(filepath.Separator)) {
		if err := os.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}
}

// Validate checks every manifest entry against disk (existence, size, sha256).
func (s *Storage) Validate() (total int, ok bool, issues []string, err error) {
	if _, err := os.Stat(s.manifestPath()); errors.Is(err, os.ErrNotExist) {
		return 0, true, nil, nil
	}

	m, err := s.loadManifest()
	if err != nil {
		return 0, false, nil, err
	}

	total = len(m.Entries)
	if total == 0 {
		return 0, true, nil, nil
	}

	storageBase := s.storageDir()
	for _, e := range m.Entries {
		targetPath := filepath.Join(storageBase, filepath.FromSlash(e.Name))
		fi, err := os.Stat(targetPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				issues = append(issues, fmt.Sprintf("%s: missing on disk", e.Name))
			} else {
				issues = append(issues, fmt.Sprintf("%s: cannot stat: %v", e.Name, err))
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			issues = append(issues, fmt.Sprintf("%s: not a regular file", e.Name))
			continue
		}
		if fi.Size() != e.Size {
			issues = append(issues, fmt.Sprintf("%s: size mismatch (recorded %d, disk %d)", e.Name, e.Size, fi.Size()))
			continue
		}
		f, err := os.Open(targetPath)
		if err != nil {
			issues = append(issues, fmt.Sprintf("%s: cannot open: %v", e.Name, err))
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			issues = append(issues, fmt.Sprintf("%s: read error: %v", e.Name, err))
			continue
		}
		actualSHA := hex.EncodeToString(h.Sum(nil))
		if actualSHA != e.SHA256 {
			issues = append(issues, fmt.Sprintf("%s: sha256 mismatch (recorded %s, disk %s)", e.Name, e.SHA256, actualSHA))
			continue
		}
	}

	if len(issues) > 0 {
		return total, false, issues, nil
	}
	return total, true, nil, nil
}

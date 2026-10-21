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
	"sync"
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

// Storage manages non-wiki storage files in a vault.
type Storage struct {
	vaultDir string
	mu       sync.Mutex
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
	path := s.manifestPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Manifest{Version: 1, Entries: []Entry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("storage: parse manifest: %w", err)
	}
	if m.Version == 0 {
		m.Version = 1
	}
	if m.Entries == nil {
		m.Entries = []Entry{}
	}
	return &m, nil
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

func (s *Storage) copyFileAtomic(srcPath, destPath string) error {
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("storage: create directory: %w", err)
	}

	tmp, err := os.CreateTemp(destDir, ".storage-tmp-*")
	if err != nil {
		return fmt.Errorf("storage: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	src, err := os.Open(srcPath)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: open source file: %w", err)
	}
	defer src.Close()

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: copy file content: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close temp file: %w", err)
	}

	if err := os.Rename(tmpName, destPath); err != nil {
		return fmt.Errorf("storage: rename file: %w", err)
	}
	tmpName = ""
	return nil
}

// Add copies srcPath to storage/<name> and records it in the manifest and in
// this PC's ledger (066: the ledger says lw put these bytes here, which is what
// lets a later removal on another PC delete them). Order matters for a crash:
// the file first, then the catalog, then the ledger row.
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

	s.mu.Lock()
	defer s.mu.Unlock()

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

	targetPath := filepath.Join(s.storageDir(), filepath.FromSlash(clean))

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
			// Identical SHA256: no-op, update note if provided
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
			if err := s.copyFileAtomic(srcPath, targetPath); err != nil {
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

	// New entry
	targetFi, err := os.Stat(targetPath)
	if err != nil || !os.SameFile(fi, targetFi) {
		if err := s.copyFileAtomic(srcPath, targetPath); err != nil {
			return Entry{}, err
		}
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
	s.mu.Lock()
	defer s.mu.Unlock()

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

	s.mu.Lock()
	defer s.mu.Unlock()

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

	s.mu.Lock()
	m, err := s.loadManifest()
	if err != nil {
		s.mu.Unlock()
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
	s.mu.Unlock()

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

	s.mu.Lock()
	defer s.mu.Unlock()

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
	s.mu.Lock()
	defer s.mu.Unlock()

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

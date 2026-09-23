package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// This file implements 033's attachment hashing surface: the sha of an
// original's bytes, answered — where possible — without reading them, so
// that lint's src-integrity check and every projection build scale with
// the number of CHANGED files rather than with the vault's byte size.

// attachmentEntry is one persisted cache record: the file's stat signature
// plus the sha256 hex its bytes hashed to when that signature was current.
type attachmentEntry struct {
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtime_unix_nano"`
	SHA256  string `json:"sha256"`
}

// attachmentCache is Vault's persistent stat cache for attachment hashes,
// stored at <root>/.llmwiki/cache/attachments.json (map path → entry).
//
// A lookup stats the file and returns the cached sha when both size and
// mtime match the recorded entry — the file is NOT opened. On any
// mismatch (or a first sighting) the bytes are read, hashed, and the
// entry is rewritten and persisted with a write-temp-then-rename, so a
// crash mid-write cannot leave a torn cache behind.
//
// Trade-off, deliberate: an edit that changes neither size nor mtime is
// not detected and keeps answering the old sha. mtime is the standard
// stat-cache trade-off used by make and git; a same-size, same-mtime edit
// is the accepted blind spot. There is no `lw lint --rehash`: rebuilding
// the cache is a matter of deleting the file.
//
// Cache READ problems are not errors either — a missing or corrupt
// attachments.json simply starts empty and is rewritten by the next
// persist. Cache WRITE failures are logged at debug and the freshly
// computed sha is returned anyway: the cache is an optimization, and
// losing it costs one re-read per attachment, never a wrong answer (a
// stale entry can only be stale together with a size/mtime change, which
// the stat check catches).
//
// All methods are safe for concurrent use within one process.
type attachmentCache struct {
	mu      sync.Mutex
	file    string // absolute path of attachments.json
	fsys    fs.FS  // the vault's own FS, for stat and read
	entries map[string]attachmentEntry
	loaded  bool
}

func newAttachmentCache(file string, fsys fs.FS) *attachmentCache {
	return &attachmentCache{file: file, fsys: fsys, entries: map[string]attachmentEntry{}}
}

// sha256For returns the sha256 hex of the attachment at vpath (a cleaned,
// vault-relative path), via the cache when the stat signature allows.
// userPath is the caller's original spelling, for error messages only.
func (c *attachmentCache) sha256For(vpath, userPath string) (string, error) {
	info, err := fs.Stat(c.fsys, vpath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("vault: attachment %s: %w", userPath, fs.ErrNotExist)
		}
		return "", fmt.Errorf("vault: stat attachment %s: %w", userPath, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.loadLocked()
	sig := attachmentEntry{Size: info.Size(), MtimeNS: info.ModTime().UnixNano()}
	if e, ok := c.entries[vpath]; ok && e.Size == sig.Size && e.MtimeNS == sig.MtimeNS {
		return e.SHA256, nil
	}

	b, err := fs.ReadFile(c.fsys, vpath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("vault: attachment %s: %w", userPath, fs.ErrNotExist)
		}
		return "", fmt.Errorf("vault: read attachment %s: %w", userPath, err)
	}
	sum := sha256.Sum256(b)
	sig.SHA256 = hex.EncodeToString(sum[:])

	c.entries[vpath] = sig
	c.persistLocked()
	return sig.SHA256, nil
}

// loadLocked reads attachments.json once per process, treating a missing
// or unreadable cache as empty. Callers hold c.mu.
func (c *attachmentCache) loadLocked() {
	if c.loaded {
		return
	}
	c.loaded = true
	b, err := os.ReadFile(c.file)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Debug("vault attachment cache read", "file", c.file, "err", err)
		}
		return
	}
	entries := map[string]attachmentEntry{}
	if err := json.Unmarshal(b, &entries); err != nil {
		slog.Debug("vault attachment cache corrupt, starting empty", "file", c.file, "err", err)
		return
	}
	c.entries = entries
}

// persistLocked rewrites attachments.json atomically (temp + rename). A
// failure is logged at debug and otherwise swallowed — see the type
// comment. Callers hold c.mu.
func (c *attachmentCache) persistLocked() {
	if c.entries == nil {
		c.entries = map[string]attachmentEntry{}
	}
	b, err := json.Marshal(c.entries)
	if err != nil {
		slog.Debug("vault attachment cache encode", "file", c.file, "err", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.file), 0o755); err != nil {
		slog.Debug("vault attachment cache mkdir", "file", c.file, "err", err)
		return
	}
	if err := writeTemp(c.file, b); err != nil {
		slog.Debug("vault attachment cache write", "file", c.file, "err", err)
	}
}

// writeTemp writes b to a temp file beside dst and renames it into place,
// so a crash mid-write cannot leave a torn cache. The temp file is
// removed on any failure.
func writeTemp(dst string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), "attachments-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		return errors.Join(werr, cerr)
	}
	if rerr := os.Rename(name, dst); rerr != nil {
		_ = os.Remove(name)
		return rerr
	}
	return nil
}

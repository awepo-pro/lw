// Package cache wraps an Extractor with a content-addressed on-disk cache
// (007 T2). Extraction is expensive — the Docling sidecar takes seconds to
// minutes per document and its version probe costs ~4 s — so a hit must
// never reach the backend, and the version must be probed at most once per
// wrapped Extractor. The cache key is the source file's content hash plus
// the extractor ID plus the extractor's version string, so a re-run after
// either the bytes or the backend changed misses cleanly.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/awepo-pro/lw/internal/extract"
)

// cached is the Extractor New returns.
type cached struct {
	inner       extract.Extractor
	dir         string
	extractorID string

	// version is probed lazily and at most once per Extractor: it shells
	// out to the sidecar and costs ~4 s, so a hit path must not re-pay it
	// on every call (007 T2 K5).
	versionOnce sync.Once
	versionVal  string
	versionErr  error
	versionFn   func(context.Context) (string, error)
}

// New wraps inner with a content-addressed cache under dir. version is
// called at most once per returned Extractor, and only when an Extract
// needs a key. dir == "" returns inner unchanged.
func New(inner extract.Extractor, dir, extractorID string,
	version func(context.Context) (string, error)) extract.Extractor {
	if dir == "" {
		return inner
	}
	return &cached{inner: inner, dir: dir, extractorID: extractorID, versionFn: version}
}

// CanHandle delegates: the cache changes where a Doc comes from, never
// which URIs the chain accepts (007 T2 F.K1).
func (c *cached) CanHandle(uri string) bool {
	return c.inner.CanHandle(uri)
}

// entry is the JSON shape of one cache file. Doc keeps only the fields
// that make up the extracted content; SourceURL is deliberately excluded —
// it is provenance of the *request*, not of the bytes, and is re-stamped
// from the requested path on every hit (007 T2 F.K3).
type entry struct {
	FileSHA256 string `json:"file_sha256"`
	Extractor  string `json:"extractor"`
	Version    string `json:"version"`
	Doc        struct {
		Title     string `json:"title"`
		Markdown  string `json:"markdown"`
		Kind      string `json:"kind"`
		Extractor string `json:"extractor"`
	} `json:"doc"`
}

// isRemote reports whether uri parses with an http or https scheme — the
// same semantics as extract's unexported isRemoteURL, reimplemented here
// because an import in the other direction would cycle (007 T2). A URL is
// never cached: its bytes have no stable identity on disk.
func isRemote(uri string) bool {
	u, err := url.Parse(uri)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}

// Extract serves uri from the cache when the bytes, extractor ID and
// version all match a stored entry, and delegates to inner otherwise.
// Every cache-internal failure — unreadable file, version error, corrupt
// entry, unwritable dir — degrades to the uncached path or to a logged
// warning, never to an error the caller would not have seen without the
// cache (007 T2 F.K2, F.K5, F.K6).
func (c *cached) Extract(ctx context.Context, uri string) (*extract.Doc, error) {
	// F.K1: a URL is never cached — delegate.
	if isRemote(uri) {
		return c.inner.Extract(ctx, uri)
	}

	// A local file we cannot hash cannot be keyed — delegate so the
	// backend's own error surfaces.
	fileSHA, err := hashFile(uri)
	if err != nil {
		return c.inner.Extract(ctx, uri)
	}

	ver, err := c.versionValue(ctx)
	if err != nil {
		// F.K2: the version func's error is the backend's real cause
		// (e.g. ErrSidecarMissing) — delegate uncached and let it report.
		return c.inner.Extract(ctx, uri)
	}

	key := entryKey(fileSHA, c.extractorID, ver)
	path := filepath.Join(c.dir, key+".json")

	if d := c.readEntry(path, fileSHA, ver); d != nil {
		// F.K3: record the requested path as provenance — the same bytes
		// at a new path are a hit whose Doc must name the new path.
		d.SourceURL = uri
		return d, nil
	}

	doc, err := c.inner.Extract(ctx, uri)
	if err != nil {
		// F.K4: inner errors pass through unchanged, nothing is written.
		return nil, err
	}
	c.writeEntry(path, fileSHA, ver, doc)
	return doc, nil
}

// versionValue resolves the backend version through versionFn exactly once
// per Extractor.
func (c *cached) versionValue(ctx context.Context) (string, error) {
	c.versionOnce.Do(func() {
		c.versionVal, c.versionErr = c.versionFn(ctx)
	})
	return c.versionVal, c.versionErr
}

// hashFile returns the hex sha256 of path's contents.
func hashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// entryKey derives the cache key: hex(sha256(fileSHA256hex + "\x00" +
// extractorID + "\x00" + version)) (007 T2 F.K2). The NUL separators keep
// the three fields unambiguous — none of them can contain a NUL byte.
func entryKey(fileSHA, extractorID, version string) string {
	h := sha256.Sum256([]byte(fileSHA + "\x00" + extractorID + "\x00" + version))
	return hex.EncodeToString(h[:])
}

// readEntry loads and validates the entry at path. A file that is missing,
// undecodable, or whose three key fields do not match this call's identity
// yields nil — a miss, never an error (007 T2 F.K5).
func (c *cached) readEntry(path, fileSHA, version string) *extract.Doc {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var e entry
	if err := json.Unmarshal(b, &e); err != nil {
		return nil
	}
	if e.FileSHA256 != fileSHA || e.Extractor != c.extractorID || e.Version != version {
		return nil
	}
	return &extract.Doc{
		Title:     e.Doc.Title,
		Markdown:  e.Doc.Markdown,
		Kind:      e.Doc.Kind,
		Extractor: e.Doc.Extractor,
	}
}

// writeEntry stores doc at path atomically: a temp file in the same
// directory, renamed over the entry, so a crash mid-write never leaves a
// half-written entry that a later run would misread as a hit — the reader
// sees either the old entry or the complete new one (007 T2 F.K4). The
// write is not fsynced: a cache entry is a pure optimization, and losing
// the last write to a power cut only costs one re-extraction. A failure is
// logged and otherwise swallowed — the caller already has the Doc.
func (c *cached) writeEntry(path, fileSHA, version string, doc *extract.Doc) {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		slog.Warn("extract cache write", "err", err)
		return
	}
	var e entry
	e.FileSHA256 = fileSHA
	e.Extractor = c.extractorID
	e.Version = version
	e.Doc.Title = doc.Title
	e.Doc.Markdown = doc.Markdown
	e.Doc.Kind = doc.Kind
	e.Doc.Extractor = doc.Extractor
	b, err := json.Marshal(&e)
	if err != nil {
		slog.Warn("extract cache write", "err", err)
		return
	}
	tmp, err := os.CreateTemp(c.dir, ".tmp-*")
	if err != nil {
		slog.Warn("extract cache write", "err", err)
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		slog.Warn("extract cache write", "err", err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		slog.Warn("extract cache write", "err", err)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		slog.Warn("extract cache write", "err", err)
	}
}

// Stat reports the entries and total bytes under dir; a missing dir is
// 0, 0, nil (007 T2).
func Stat(dir string) (entries int, bytes int64, err error) {
	d, err := os.Open(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("cache: stat %s: %w", dir, err)
	}
	defer d.Close()

	dirents, err := d.ReadDir(-1)
	if err != nil {
		return 0, 0, fmt.Errorf("cache: stat %s: %w", dir, err)
	}
	for _, de := range dirents {
		if de.IsDir() {
			continue
		}
		info, err := de.Info()
		if err != nil {
			// The entry vanished between ReadDir and Info — count the
			// ones that survived; a cache directory is advisory data.
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		entries++
		bytes += info.Size()
	}
	return entries, bytes, nil
}

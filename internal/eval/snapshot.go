package eval

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// snapshotExcluded are the vault paths a snapshot leaves out: lw's own
// trace and debug-log output. They are per-run artifacts, not vault state —
// a frozen copy that carried the live vault's traces would hand every run a
// trace history the real vault only has because of what was asked of it
// earlier, and the runner copies each run's own traces out of the scratch
// vault, which must therefore start without any.
var snapshotExcluded = []string{".llmwiki/traces", ".llmwiki/logs"}

// openChangesetsDir is where lw keeps a changeset still under review.
const openChangesetsDir = ".llmwiki/changesets/open"

// sha256Hex returns the lowercase hex sha256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fileSHA256 returns the lowercase hex sha256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// snapEntry is one vault entry on its way into the tarball.
type snapEntry struct {
	rel  string // slash path relative to the vault root
	full string
	info fs.FileInfo
}

// Snapshot freezes vaultDir into a tar.gz at outTarGz and returns the
// sha256 of the tar.gz's bytes (037 T1).
//
// The tarball is the eval set's one fixed input, so it is built to be
// reproducible and to be refused when it would not be the vault anyone
// asked about:
//
//   - entries are sorted by path, owner and group are zeroed, and mtimes are
//     whole seconds with no gzip header time — the same vault always yields
//     the same bytes, which is what lets cases.toml pin it by hash;
//   - an open changeset (any entry in .llmwiki/changesets/open/) is refused:
//     a vault frozen mid-review would make every run start with a pending
//     proposal that lw ingest would join and lw query would have to step
//     around;
//   - a symlink or any other non-regular file is refused: Extract refuses
//     them, so a snapshot carrying one could never be unpacked;
//   - .llmwiki/traces and .llmwiki/logs are left out (snapshotExcluded).
//
// The file is written beside its destination and renamed into place, so a
// failed snapshot leaves nothing behind and never a half-written tarball.
func Snapshot(vaultDir, outTarGz string) (sha256hex string, err error) {
	root, err := filepath.EvalSymlinks(vaultDir)
	if err != nil {
		return "", fmt.Errorf("eval: snapshot: %w", err)
	}
	if info, err := os.Stat(root); err != nil {
		return "", fmt.Errorf("eval: snapshot: %w", err)
	} else if !info.IsDir() {
		return "", fmt.Errorf("eval: snapshot: %s is not a directory", vaultDir)
	}

	entries, err := collectSnapshotEntries(root)
	if err != nil {
		return "", err
	}

	outDir := filepath.Dir(outTarGz)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("eval: snapshot: %w", err)
	}
	tmp, err := os.CreateTemp(outDir, ".snapshot-*.tmp")
	if err != nil {
		return "", fmt.Errorf("eval: snapshot: %w", err)
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	h := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(tmp, h)) // zero Header: no name, no mtime
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := writeSnapshotEntry(tw, e); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("eval: snapshot: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("eval: snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("eval: snapshot: %w", err)
	}
	// CreateTemp makes the file 0600; a snapshot is a shareable set file, and
	// the rename below should leave it as readable as any other.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", fmt.Errorf("eval: snapshot: %w", err)
	}
	if err := os.Rename(tmp.Name(), outTarGz); err != nil {
		return "", fmt.Errorf("eval: snapshot: %w", err)
	}
	done = true
	return hex.EncodeToString(h.Sum(nil)), nil
}

// collectSnapshotEntries walks root and returns what goes into the tarball,
// sorted by path, or the first reason the vault cannot be snapshotted.
func collectSnapshotEntries(root string) ([]snapEntry, error) {
	var entries []snapEntry
	err := filepath.WalkDir(root, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relOS, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		if relOS == "." {
			return nil
		}
		rel := filepath.ToSlash(relOS)
		for _, ex := range snapshotExcluded {
			if rel == ex || strings.HasPrefix(rel, ex+"/") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if path.Dir(rel) == openChangesetsDir {
			return fmt.Errorf("eval: snapshot: open changeset %s in %s: commit or reject it first",
				d.Name(), filepath.Join(root, filepath.FromSlash(openChangesetsDir)))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("eval: snapshot: %s is a symlink; a snapshot holds regular files and directories only", rel)
		case !info.Mode().IsRegular() && !info.IsDir():
			return fmt.Errorf("eval: snapshot: %s is not a regular file or directory", rel)
		}
		entries = append(entries, snapEntry{rel: rel, full: full, info: info})
		return nil
	})
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return nil, fmt.Errorf("eval: snapshot: %w", err)
		}
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	return entries, nil
}

// writeSnapshotEntry writes one entry's header and, for a file, its bytes.
func writeSnapshotEntry(tw *tar.Writer, e snapEntry) error {
	hdr := &tar.Header{
		Name:    e.rel,
		Mode:    int64(e.info.Mode().Perm()),
		ModTime: e.info.ModTime().UTC().Truncate(time.Second),
	}
	if e.info.IsDir() {
		hdr.Typeflag = tar.TypeDir
		hdr.Name += "/"
		return wrapSnapshotErr(tw.WriteHeader(hdr))
	}
	hdr.Typeflag = tar.TypeReg
	hdr.Size = e.info.Size()
	if err := tw.WriteHeader(hdr); err != nil {
		return wrapSnapshotErr(err)
	}
	f, err := os.Open(e.full)
	if err != nil {
		return wrapSnapshotErr(err)
	}
	defer f.Close()
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("eval: snapshot: %s: %w", e.rel, err)
	}
	return nil
}

func wrapSnapshotErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("eval: snapshot: %w", err)
}

// dirMeta is a directory's mode and mtime, applied after its children are
// written (writing a child moves the parent's mtime).
type dirMeta struct {
	path  string
	depth int
	mode  fs.FileMode
	mtime time.Time
}

// Extract unpacks tarGz into dst, which must be empty or absent (037 T1).
//
// It is the only way a scratch vault comes into being, so it trusts nothing
// in the tarball: an entry that is absolute, names a ".." component, or is
// anything but a regular file or directory (a symlink could redirect a later
// entry out of dst; a hard link or device has no business in a vault) fails
// the whole extract, as does an entry under .llmwiki/changesets/open/ (a
// tarball that smuggles in a pending changeset would start every run on
// top of it). A file is created O_EXCL, so a duplicate entry fails instead
// of overwriting.
//
// On any failure dst is returned to what it was — removed if Extract made
// it, emptied if it was an existing empty directory — so a caller never has
// to wonder what a half-extracted scratch vault holds.
//
// Modes (&0o777) and mtimes are restored: lw's index and the vault's own
// staleness checks read them, and a scratch vault that differs in mtime from
// the frozen one is not the same vault. Directory modes and mtimes are
// applied last, deepest first, so a read-only directory never blocks
// writing its own contents.
func Extract(tarGz, dst string) (err error) {
	f, err := os.Open(tarGz)
	if err != nil {
		return fmt.Errorf("eval: extract: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("eval: extract %s: %w", filepath.Base(tarGz), err)
	}
	defer gz.Close()

	absDst, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("eval: extract: %w", err)
	}
	created := false
	switch info, statErr := os.Stat(absDst); {
	case statErr == nil:
		if !info.IsDir() {
			return fmt.Errorf("eval: extract: %s is not a directory", dst)
		}
		kids, err := os.ReadDir(absDst)
		if err != nil {
			return fmt.Errorf("eval: extract: %w", err)
		}
		if len(kids) > 0 {
			return fmt.Errorf("eval: extract: %s is not empty", dst)
		}
	case errors.Is(statErr, fs.ErrNotExist):
		if err := os.MkdirAll(absDst, 0o755); err != nil {
			return fmt.Errorf("eval: extract: %w", err)
		}
		created = true
	default:
		return fmt.Errorf("eval: extract: %w", statErr)
	}
	defer func() {
		if err == nil {
			return
		}
		if created {
			removeAll(absDst)
			return
		}
		if kids, rerr := os.ReadDir(absDst); rerr == nil {
			for _, k := range kids {
				removeAll(filepath.Join(absDst, k.Name()))
			}
		}
	}()

	wrap := func(err error) error {
		return fmt.Errorf("eval: extract %s: %w", filepath.Base(tarGz), err)
	}
	var dirs []dirMeta
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return wrap(err)
		}
		rel, err := safeEntryName(h.Name)
		if err != nil {
			return wrap(err)
		}
		if rel == "." {
			continue // the archive's own root entry
		}
		if rest, ok := strings.CutPrefix(rel, openChangesetsDir+"/"); ok {
			name, _, _ := strings.Cut(rest, "/")
			return wrap(fmt.Errorf("open changeset %s in the tarball: a snapshot never carries one", name))
		}
		target := filepath.Join(absDst, filepath.FromSlash(rel))
		perm := fs.FileMode(h.Mode) & 0o777
		switch h.Typeflag {
		case tar.TypeDir:
			// Owner rwx while extracting; the real mode goes on at the end.
			if err := os.MkdirAll(target, 0o700); err != nil {
				return wrap(err)
			}
			dirs = append(dirs, dirMeta{path: target, depth: strings.Count(rel, "/"), mode: perm, mtime: h.ModTime})
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return wrap(err)
			}
			if err := extractFile(tr, target, perm, h.ModTime); err != nil {
				return wrap(err)
			}
		default:
			return wrap(fmt.Errorf("%s: unsupported entry type %q (only regular files and directories)", h.Name, string(h.Typeflag)))
		}
	}
	// Drain the stream so the gzip trailer's checksum is verified: a
	// truncated or corrupt tarball must not pass for a complete one.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return wrap(err)
	}

	sort.SliceStable(dirs, func(i, j int) bool { return dirs[i].depth > dirs[j].depth })
	for _, d := range dirs {
		if err := os.Chmod(d.path, d.mode); err != nil {
			return wrap(err)
		}
		if err := os.Chtimes(d.path, d.mtime, d.mtime); err != nil {
			return wrap(err)
		}
	}
	return nil
}

// extractFile writes one regular file: created exclusively, then given its
// real mode (the creation mode is subject to the umask) and mtime.
func extractFile(r io.Reader, target string, perm fs.FileMode, mtime time.Time) error {
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(target, perm); err != nil {
		return err
	}
	return os.Chtimes(target, mtime, mtime)
}

// safeEntryName returns the clean, slash-separated, vault-relative path a
// tar entry names, "." for the archive root, or an error for a name that is
// absolute, empty, holds a NUL, or has a ".." component anywhere — even one
// that would cancel out, since no honest snapshot writes one.
func safeEntryName(name string) (string, error) {
	if name == "" {
		return "", errors.New("entry with an empty name")
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("entry %q has a NUL in its name", name)
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("entry %q is an absolute path", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("entry %q has a .. component", name)
		}
	}
	return path.Clean(name), nil
}

// removeAll is os.RemoveAll that also copes with a read-only directory
// inside the tree (a vault can carry one, and Extract restores its mode): a
// failed removal makes every directory below p owner-writable and retries.
func removeAll(p string) error {
	if err := os.RemoveAll(p); err == nil {
		return nil
	}
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(q, 0o700)
		}
		return nil
	})
	return os.RemoveAll(p)
}

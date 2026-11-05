// Package safewrite provides filesystem write operations that guard a vault
// directory against being modified or overwritten, directly or through symlinks.
package safewrite

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RefusalError is returned when a write or directory operation is refused because
// it would touch the vault or violate symlink safety invariants.
type RefusalError struct {
	Path   string
	Reason string
}

// Error implements the error interface, formatting the refusal as:
// refusing to write <path>: <reason>.
func (e *RefusalError) Error() string {
	return fmt.Sprintf("refusing to write %s: %s", e.Path, e.Reason)
}

// Guard knows the vault root and refuses any write that would land inside it.
type Guard struct {
	vaultRoot string
}

// NewGuard creates a Guard for the given vault root. It evaluates symlinks on
// the absolute path of vaultRoot to establish the canonical root boundary.
func NewGuard(vaultRoot string) (*Guard, error) {
	if vaultRoot == "" {
		return nil, errors.New("safewrite: empty vault root")
	}
	absRoot, err := filepath.Abs(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("safewrite: resolve vault root %s: %w", vaultRoot, err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("safewrite: eval symlinks vault root %s: %w", absRoot, err)
	}
	return &Guard{
		vaultRoot: filepath.Clean(realRoot),
	}, nil
}

// VaultRoot returns the canonical, symlink-evaluated vault root directory.
func (g *Guard) VaultRoot() string {
	return g.vaultRoot
}

// Within reports whether real path p is the vault root or inside it (separator-aware:
// /vault2 is NOT inside /vault).
func (g *Guard) Within(p string) bool {
	cleanP := filepath.Clean(p)
	cleanRoot := filepath.Clean(g.vaultRoot)
	if cleanP == cleanRoot {
		return true
	}
	prefix := cleanRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(cleanP, prefix)
}

// CheckDir returns an error unless dir (created if needed by the caller) resolves,
// via EvalSymlinks, to a directory outside the vault.
func (g *Guard) CheckDir(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("safewrite: resolve dir %s: %w", dir, err)
	}
	realDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return fmt.Errorf("safewrite: eval symlinks %s: %w", dir, err)
	}
	fi, err := os.Stat(realDir)
	if err != nil {
		return fmt.Errorf("safewrite: stat %s: %w", realDir, err)
	}
	if !fi.IsDir() {
		return &RefusalError{Path: dir, Reason: fmt.Sprintf("%s is not a directory", realDir)}
	}
	if g.Within(realDir) {
		return &RefusalError{Path: dir, Reason: fmt.Sprintf("resolves inside vault root (%s)", g.vaultRoot)}
	}
	return nil
}

// WriteFile writes data to path only when:
//   - path's parent directory resolves (EvalSymlinks) outside the vault;
//   - path does not exist OR Lstat says it is a regular file (a symlink, directory,
//     device, fifo -> refuse with an error naming the path);
//   - the final real path (EvalSymlinks(parent) + base) is outside the vault.
//
// It writes via a temp file in the same directory + os.Rename (atomic, replacing
// an existing file rather than following a symlink).
func (g *Guard) WriteFile(path string, data []byte) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("safewrite: resolve path %s: %w", path, err)
	}

	parentDir := filepath.Dir(absPath)
	realParentDir, err := filepath.EvalSymlinks(parentDir)
	if err != nil {
		return fmt.Errorf("safewrite: eval symlinks parent directory %s: %w", parentDir, err)
	}

	parentFi, err := os.Stat(realParentDir)
	if err != nil {
		return fmt.Errorf("safewrite: stat parent directory %s: %w", realParentDir, err)
	}
	if !parentFi.IsDir() {
		return &RefusalError{Path: path, Reason: fmt.Sprintf("parent directory %s is not a directory", realParentDir)}
	}

	if g.Within(realParentDir) {
		return &RefusalError{Path: path, Reason: fmt.Sprintf("parent directory resolves inside vault root (%s)", g.vaultRoot)}
	}

	fi, err := os.Lstat(absPath)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return &RefusalError{Path: path, Reason: "target path is a symlink"}
		}
		if fi.IsDir() {
			return &RefusalError{Path: path, Reason: "target path is a directory"}
		}
		if !fi.Mode().IsRegular() {
			return &RefusalError{Path: path, Reason: fmt.Sprintf("target path is not a regular file (mode: %s)", fi.Mode().Type())}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("safewrite: lstat %s: %w", path, err)
	}

	finalRealPath := filepath.Join(realParentDir, filepath.Base(absPath))
	if g.Within(finalRealPath) {
		return &RefusalError{Path: path, Reason: fmt.Sprintf("resolves inside vault root (%s)", g.vaultRoot)}
	}

	tmpFile, err := os.CreateTemp(parentDir, ".safewrite-tmp-*")
	if err != nil {
		return fmt.Errorf("safewrite: create temp file in %s: %w", parentDir, err)
	}
	tmpName := tmpFile.Name()
	cleanedUp := false
	cleanup := func() {
		if !cleanedUp {
			_ = os.Remove(tmpName)
			cleanedUp = true
		}
	}
	defer cleanup()

	if err := tmpFile.Chmod(0644); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("safewrite: chmod temp file: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("safewrite: write temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("safewrite: close temp file: %w", err)
	}

	if err := os.Rename(tmpName, absPath); err != nil {
		return fmt.Errorf("safewrite: rename %s to %s: %w", tmpName, absPath, err)
	}
	cleanedUp = true

	return nil
}

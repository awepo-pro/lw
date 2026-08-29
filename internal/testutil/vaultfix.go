package testutil

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// fixtureRoot walks up from start looking for a "spec/fixtures" directory
// and returns its absolute path. Kept separate from FixtureRoot so it can be
// tested against a synthetic tree without depending on the real repository
// layout (which a concurrent subtask may still be populating).
func fixtureRoot(start string) (string, error) {
	dir := start
	for {
		candidate := filepath.Join(dir, "spec", "fixtures")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("testutil: no spec/fixtures directory found above %s", start)
		}
		dir = parent
	}
}

// FixtureRoot locates spec/fixtures by walking up from the test's current
// working directory, so it works from any package depth. It fails t if no
// such directory is found.
func FixtureRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("testutil: getwd: %v", err)
	}

	root, err := fixtureRoot(wd)
	if err != nil {
		t.Fatalf("testutil: %v", err)
	}
	return root
}

// CopyFixture copies spec/fixtures/<name> into t.TempDir() and returns the
// new path. The result is a private copy — tests may mutate it freely — with
// the original relative tree and file modes preserved. It refuses to follow
// any symlink found inside the fixture.
func CopyFixture(t *testing.T, name string) string {
	t.Helper()

	root := FixtureRoot(t)
	src := filepath.Join(root, filepath.FromSlash(name))
	dst := filepath.Join(t.TempDir(), filepath.Base(name))

	if err := copyTree(src, dst); err != nil {
		t.Fatalf("testutil: copy fixture %s: %v", name, err)
	}
	return dst
}

// copyTree recursively copies src to dst, preserving file modes and
// refusing to follow symlinks. It is unexported so it can be tested for its
// error path directly, without needing to observe a *testing.T failure.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to follow symlink %s", path)
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("rel %s: %w", path, err)
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}

		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

// copyFile copies one regular file from src to dst with the given mode,
// creating dst's parent directory if needed.
func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

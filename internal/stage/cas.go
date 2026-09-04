// cas.go implements the content-addressed object store (backbone §5.1).
// Owned by S2-T1.
package stage

import (
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Store is the content-addressed object store rooted at .llmwiki/objects.
//
// Contract (backbone §5.1, MASTER §9 D-AS "what is hashed vs what is
// stored"): the sha Put returns is sha256 of the UNCOMPRESSED bytes it was
// handed; the file on disk holds the zlib stream of those bytes. Hashing the
// compressed form would make every object sha, and every .tree snapshot,
// move under a Go upgrade of the compressor.
type Store struct {
	dir string
}

// OpenStore opens (creating if absent) the content-addressed store rooted
// at dir.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("stage: open store %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// shardPath returns the path objects/<sha[0:2]>/<sha[2:]> for sha.
func (s *Store) shardPath(sha string) (dir, path string) {
	dir = filepath.Join(s.dir, sha[:2])
	path = filepath.Join(dir, sha[2:])
	return dir, path
}

// Put stores b, content-addressed, and returns the hex sha256 of b — the
// uncompressed bytes (backbone §5.1). Put on content already present is a
// no-op that returns the same sha. The write goes through a temp file
// created in, and renamed within, the same shard directory as the final
// object, so the rename never crosses a filesystem boundary; the temp file
// is removed on any error path.
func (s *Store) Put(b []byte) (string, error) {
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])

	if s.Has(sha) {
		return sha, nil
	}

	shardDir, objPath := s.shardPath(sha)
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		return "", fmt.Errorf("stage: put %s: %w", sha, err)
	}

	tmp, err := os.CreateTemp(shardDir, "tmp-*")
	if err != nil {
		return "", fmt.Errorf("stage: put %s: %w", sha, err)
	}
	tmpPath := tmp.Name()

	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	zw, err := zlib.NewWriterLevel(tmp, zlib.BestCompression)
	if err != nil {
		return "", fmt.Errorf("stage: put %s: %w", sha, err)
	}
	if _, err := zw.Write(b); err != nil {
		zw.Close()
		return "", fmt.Errorf("stage: put %s: %w", sha, err)
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("stage: put %s: %w", sha, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("stage: put %s: %w", sha, err)
	}
	if err := os.Rename(tmpPath, objPath); err != nil {
		return "", fmt.Errorf("stage: put %s: %w", sha, err)
	}
	ok = true
	return sha, nil
}

// Get returns the uncompressed bytes stored under sha.
//
// Contract (backbone §5.1): a sha the store does not hold returns an error
// satisfying errors.Is(err, fs.ErrNotExist).
func (s *Store) Get(sha string) ([]byte, error) {
	if len(sha) < 2 {
		return nil, fmt.Errorf("stage: get %s: %w", sha, fs.ErrNotExist)
	}
	_, objPath := s.shardPath(sha)

	f, err := os.Open(objPath)
	if err != nil {
		return nil, fmt.Errorf("stage: get %s: %w", sha, err)
	}
	defer f.Close()

	zr, err := zlib.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("stage: get %s: %w", sha, err)
	}
	defer zr.Close()

	b, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("stage: get %s: %w", sha, err)
	}
	return b, nil
}

// Has reports whether the store holds sha.
func (s *Store) Has(sha string) bool {
	if len(sha) < 2 {
		return false
	}
	_, objPath := s.shardPath(sha)
	_, err := os.Stat(objPath)
	return err == nil
}

package stage

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStorePutIdempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	content := []byte("the quick brown fox jumps over the lazy dog")

	sha1, err := s.Put(content)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	sha2, err := s.Put(content)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if sha1 != sha2 {
		t.Fatalf("Put returned different shas for the same content: %q vs %q", sha1, sha2)
	}

	// Exactly one object file must exist across the whole store.
	var files []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk store: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("object files in store = %v, want exactly 1", files)
	}
	wantPath := filepath.Join(dir, sha1[:2], sha1[2:])
	if files[0] != wantPath {
		t.Fatalf("object file = %s, want %s", files[0], wantPath)
	}
}

func TestStoreGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	content := []byte("round trip me")
	sha, err := s.Put(content)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if !s.Has(sha) {
		t.Fatalf("Has(%q) = false after Put", sha)
	}

	got, err := s.Get(sha)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("Get returned %q, want %q", got, content)
	}
}

// TestStoreHashesUncompressedBytes proves the sha Put returns is sha256 of
// the plaintext, and the on-disk bytes are a zlib stream of that plaintext
// (backbone §5.1, MASTER §9 D-AS).
func TestStoreHashesUncompressedBytes(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	content := bytes.Repeat([]byte("compress me please "), 50)
	sha, err := s.Put(content)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, sha[:2], sha[2:]))
	if err != nil {
		t.Fatalf("read object file: %v", err)
	}
	if bytes.Equal(raw, content) {
		t.Fatalf("object file holds plaintext, want a zlib stream")
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("object file is not a valid zlib stream: %v", err)
	}
	defer zr.Close()
}

func TestStoreGetUnknownSHA(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	_, err = s.Get("0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("Get of an unknown sha returned no error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Get of an unknown sha = %v, want errors.Is(err, fs.ErrNotExist)", err)
	}
}

// TestStoreConcurrentPutSameContent Puts identical bytes from many
// goroutines at once: exactly one object file must result, all callers must
// agree on the sha, and no temp files may be left behind in the shard
// directory (S2-T1 probe c).
func TestStoreConcurrentPutSameContent(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	content := []byte("concurrent content, put from many goroutines")
	const n = 50

	var wg sync.WaitGroup
	shas := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			shas[i], errs[i] = s.Put(content)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Put goroutine %d: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if shas[i] != shas[0] {
			t.Fatalf("Put returned different shas across goroutines: %q vs %q", shas[0], shas[i])
		}
	}

	shardDir := filepath.Join(dir, shas[0][:2])
	entries, err := os.ReadDir(shardDir)
	if err != nil {
		t.Fatalf("read shard dir: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("shard dir contains %v, want exactly one object file (no leftover temp files)", names)
	}
	if entries[0].Name() != shas[0][2:] {
		t.Fatalf("shard dir's only entry is %q, want %q", entries[0].Name(), shas[0][2:])
	}
}

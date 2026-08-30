package index

import (
	"path/filepath"
	"testing"
)

// TestSaveLoad proves a Save -> Load round-trip answers identically to the
// original index: same paths, same order, same scores, same snippets, for
// several different queries and filter combinations.
func TestSaveLoad(t *testing.T) {
	v := openMinimal(t)
	original := Build(v)

	path := filepath.Join(t.TempDir(), "index.gob")
	if err := original.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Len() != original.Len() {
		t.Fatalf("loaded.Len() = %d, original.Len() = %d", loaded.Len(), original.Len())
	}

	cases := []struct {
		name string
		q    string
		opts Options
	}{
		{"plain query", "speculative decoding", Options{}},
		{"tag match", "cache", Options{}},
		{"type filter", "model", Options{Type: "entity"}},
		{"tags AND filter", "cache", Options{Tags: []string{"inference", "decoding"}}},
		{"date filter", "attention", Options{Before: mustDate(t, "2026-08-28")}},
		{"limit", "cache", Options{Limit: 1}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertSameHits(t, c.q, original.Search(c.q, c.opts), loaded.Search(c.q, c.opts))
		})
	}
}

// TestSaveAtomicWrite proves Save leaves no temp file behind on success and
// that Save is safe to call again to the same path (overwrite).
func TestSaveAtomicWrite(t *testing.T) {
	v := openMinimal(t)
	ix := Build(v)

	dir := t.TempDir()
	path := filepath.Join(dir, "index.gob")

	if err := ix.Save(path); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := ix.Save(path); err != nil {
		t.Fatalf("second Save (overwrite): %v", err)
	}

	entries, err := filepath.Glob(filepath.Join(dir, ".index-*.tmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover temp files after Save: %v", entries)
	}

	if _, err := Load(path); err != nil {
		t.Fatalf("Load after overwrite: %v", err)
	}
}

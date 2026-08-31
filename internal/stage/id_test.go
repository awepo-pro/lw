package stage

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNewChangesetIDDeterministic proves the clock and the entropy source
// are truly injected: fixing both must produce the same id twice
// (00-conventions.md §3).
func TestNewChangesetIDDeterministic(t *testing.T) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	entropy := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	id1, err := newChangesetID(fixed, bytes.NewReader(entropy))
	if err != nil {
		t.Fatalf("newChangesetID (1st): %v", err)
	}
	id2, err := newChangesetID(fixed, bytes.NewReader(entropy))
	if err != nil {
		t.Fatalf("newChangesetID (2nd): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("newChangesetID with a fixed clock and reader produced %q then %q, want equal", id1, id2)
	}
	if !changesetIDPattern.MatchString(id1) {
		t.Fatalf("id %q does not match ^cs-[0-9a-f]{7}$", id1)
	}

	// A different entropy stream at the same instant must change the id.
	id3, err := newChangesetID(fixed, bytes.NewReader([]byte{8, 7, 6, 5, 4, 3, 2, 1}))
	if err != nil {
		t.Fatalf("newChangesetID (3rd): %v", err)
	}
	if id3 == id1 {
		t.Fatalf("newChangesetID ignored the entropy source: got %q for two different readers", id3)
	}
}

// TestNewChangesetIDUnique generates 1000 ids from real entropy and checks
// every one matches the pattern and none collide.
func TestNewChangesetIDUnique(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id, err := newChangesetID(now, rand.Reader)
		if err != nil {
			t.Fatalf("newChangesetID iteration %d: %v", i, err)
		}
		if !changesetIDPattern.MatchString(id) {
			t.Fatalf("id %q (iteration %d) does not match ^cs-[0-9a-f]{7}$", id, i)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q at iteration %d", id, i)
		}
		seen[id] = true
	}
}

func TestNewChangesetIDShortRead(t *testing.T) {
	if _, err := newChangesetID(time.Now(), bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Fatal("newChangesetID with too little entropy returned no error")
	}
}

func TestNextCommitIDAbsentDir(t *testing.T) {
	dir := t.TempDir()
	id, err := nextCommitID(filepath.Join(dir, "does-not-exist"))
	if err != nil {
		t.Fatalf("nextCommitID on an absent directory: %v", err)
	}
	if id != "000001" {
		t.Fatalf("nextCommitID(absent) = %q, want %q", id, "000001")
	}
}

func TestNextCommitIDEmptyDir(t *testing.T) {
	dir := t.TempDir()
	id, err := nextCommitID(dir)
	if err != nil {
		t.Fatalf("nextCommitID on an empty directory: %v", err)
	}
	if id != "000001" {
		t.Fatalf("nextCommitID(empty) = %q, want %q", id, "000001")
	}
}

func TestNextCommitIDHighestPlusOne(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000001.tree", "000002.tree", "000042.tree", "not-a-tree.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	id, err := nextCommitID(dir)
	if err != nil {
		t.Fatalf("nextCommitID: %v", err)
	}
	if id != "000043" {
		t.Fatalf("nextCommitID = %q, want %q", id, "000043")
	}
}

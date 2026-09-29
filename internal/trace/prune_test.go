package trace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fatTurn lays down a turn dir holding one 1 MiB file, with every file mtime
// aged by age, the way a turn traces on disk.
func fatTurn(t *testing.T, root, id string, age time.Duration) {
	t.Helper()
	d := filepath.Join(root, id)
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(d, "blob.bin")
	if err := os.WriteFile(blob, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		past := time.Now().Add(-age)
		if err := os.Chtimes(blob, past, past); err != nil {
			t.Fatal(err)
		}
	}
}

// remainingTurns lists the turn-named dirs still under root.
func remainingTurns(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range entries {
		if turnNameRE.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPrune pins W8: the oldest turn dirs go first while the total sits over
// the cap, a dir with a file touched within 10 minutes is untouchable even
// when it alone is over the cap, and non-turn entries are never counted nor
// removed.
func TestPrune(t *testing.T) {
	dir := t.TempDir()
	ids := []string{testID(1), testID(2), testID(3), testID(4), testID(5), testID(6)}
	for _, id := range ids[:5] {
		fatTurn(t, dir, id, time.Hour) // five old 1 MiB turns
	}
	fatTurn(t, dir, ids[5], 0) // a sixth, modified now
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a turn"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	// 6 MiB over a 2.5 MiB cap: exactly the four oldest go, down to 2 MiB.
	removed, err := Prune(dir, int64(2.5*float64(1<<20)), now, "")
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(removed, ids[:4]) {
		t.Errorf("Prune removed %v, want %v", removed, ids[:4])
	}
	if got := remainingTurns(t, dir); !sameIDs(got, ids[4:]) {
		t.Errorf("remaining turns %v, want %v", got, ids[4:])
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("notes.txt did not survive: %v", err)
	}

	// 2 MiB over a 0.5 MiB cap: only the fifth old dir can go — the recent
	// dir stays although it alone is over the cap.
	removed, err = Prune(dir, 512*1024, now, "")
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(removed, ids[4:5]) {
		t.Errorf("second Prune removed %v, want %v", removed, ids[4:5])
	}
	if got := remainingTurns(t, dir); !sameIDs(got, ids[5:]) {
		t.Errorf("remaining turns %v, want %v", got, ids[5:])
	}
}

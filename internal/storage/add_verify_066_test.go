package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddVerifiesCopy is X-9 / F-8. Add hashes the source to decide what to do
// (a no-op, a refusal, a copy) and then copies it in a second pass. A source
// that changes between the two passes - an editor saving, a download still
// writing - used to be stored as bytes whose sha256 was not the one the catalog
// recorded: the catalog said one thing, the disk another, and the object store
// refused the file forever ("not the catalog's bytes"). The copy is now hashed
// as it is written and compared with the first pass before the catalog moves.
//
// It is an internal test: the window between the two passes has no other way in.
func TestAddVerifiesCopy(t *testing.T) {
	vault := t.TempDir()
	st, err := Open(vault)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, []byte("the bytes at the first pass"), 0o644); err != nil {
		t.Fatal(err)
	}
	addHook = func() {
		if err := os.WriteFile(src, []byte("the bytes at the second!!!!"), 0o644); err != nil { // same length
			t.Error(err)
		}
	}
	defer func() { addHook = nil }()

	if _, err := st.Add(src, "x.bin", "", false); err == nil {
		t.Fatal("Add stored a source that changed while it was being copied")
	} else if !strings.Contains(err.Error(), "changed while") {
		t.Errorf("error = %q, want it to say the source changed while it was being stored", err)
	}
	entries, err := st.List()
	if err != nil || len(entries) != 0 {
		t.Errorf("catalog = %v, %v; want no entry (the catalog never moved)", entries, err)
	}
	if _, err := os.Lstat(filepath.Join(vault, "storage", "x.bin")); err == nil {
		t.Error("storage/x.bin exists although the copy was refused")
	}
	var left []string
	filepath.WalkDir(filepath.Join(vault, "storage"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			left = append(left, p)
		}
		return nil
	})
	if len(left) != 0 {
		t.Errorf("files left under storage/: %v", left)
	}
	if _, err := os.Lstat(st.ledgerPath()); err == nil {
		if l, err := st.loadLedger(); err != nil || len(l.Materialized) != 0 {
			t.Errorf("ledger rows after a refused Add: %v (err %v)", l, err)
		}
	}

	// With a steady source the same Add works, and the entry is the bytes on disk.
	addHook = nil
	if err := os.WriteFile(src, []byte("the bytes at the first pass"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := st.Add(src, "x.bin", "", false)
	if err != nil {
		t.Fatalf("Add of a steady source: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(vault, "storage", "x.bin"))
	if err != nil || string(got) != "the bytes at the first pass" || e.Size != int64(len(got)) {
		t.Errorf("stored %q (err %v), entry %+v", got, err, e)
	}
}

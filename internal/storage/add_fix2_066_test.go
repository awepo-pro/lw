package storage

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestAddDoesNotHoldLockDuringCopy is Y-6 (G-6). Add used to hold the storage lock
// for the whole copy, and the lock wait is 30 s, so one slow copy of a large file
// made every other verb that writes the ledger or the catalog (lw sync's fetch, a
// second add, a doctor) fail with "another lw is changing storage/". The copy is
// now made, and verified, outside the lock; the lock covers only the re-check of
// the catalog and the rename and writes that follow.
//
// It is an internal test: addHook is the only way to be inside the copy.
func TestAddDoesNotHoldLockDuringCopy(t *testing.T) {
	vault := t.TempDir()
	st, err := Open(vault)
	if err != nil {
		t.Fatal(err)
	}
	src := func(name, body string) string {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	slow, other := src("slow.bin", "a large file whose copy takes a long time"), src("other.bin", "a small file stored meanwhile")

	inCopy, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool // sync.Once would make the second Add wait for the first to return
	addHook = func() {
		if first.CompareAndSwap(false, true) {
			close(inCopy)
			<-release
		}
	}
	defer func() { addHook = nil }()

	slowDone := make(chan error, 1)
	go func() {
		_, err := st.Add(slow, "slow.bin", "", false)
		slowDone <- err
	}()
	select {
	case <-inCopy:
	case <-time.After(10 * time.Second):
		t.Fatal("Add never reached the copy")
	}

	otherDone := make(chan error, 1)
	go func() {
		_, err := st.Add(other, "other.bin", "", false)
		otherDone <- err
	}()
	select {
	case err := <-otherDone:
		if err != nil {
			t.Errorf("second Add while the first one copies: %v", err)
		}
		otherDone = nil
	case <-time.After(3 * time.Second):
		t.Error("a second writer was blocked while the first Add was copying: the lock is held across the copy (G-6)")
	}

	close(release)
	for name, ch := range map[string]chan error{"first": slowDone, "second": otherDone} {
		if ch == nil {
			continue
		}
		select {
		case err := <-ch:
			if err != nil {
				t.Errorf("%s Add: %v", name, err)
			}
		case <-time.After(40 * time.Second):
			t.Fatalf("%s Add did not finish", name)
		}
	}
	entries, err := st.List()
	if err != nil || len(entries) != 2 {
		t.Errorf("catalog = %v, %v; want both entries (the slow copy lost nothing and overwrote nothing)", entries, err)
	}
	for _, name := range []string{"slow.bin", "other.bin"} {
		if _, err := os.Stat(filepath.Join(vault, "storage", name)); err != nil {
			t.Errorf("storage/%s: %v", name, err)
		}
	}
}

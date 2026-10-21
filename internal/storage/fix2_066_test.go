package storage_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/storage"
)

// 066 FIX2 tests for internal/storage (review2, runs/review2/report.md): Add over
// an untracked file (G-1), the temp sweep (G-2), the proof rule P asks for before
// anything is destroyed (G-4), and the guards review2's mutations found unpinned
// (G-3). Each Y-1..Y-3 test was written BEFORE its fix and recorded RED on
// 40cddf7; the Y-4 pins pass there by design and go RED under their mutation.

// srcFile is a file outside the vault holding content, for Add.
func srcFile(t *testing.T, content string) string {
	t.Helper()
	return createTestFile(t, t.TempDir(), "src.bin", content)
}

// TestAddRefusesUntrackedOccupant is Y-1 (G-1): a new catalog entry never
// replaces a file that is already at storage/<name> and is not the catalog's.
// Reconcile keeps such a file ("kept: changed locally since it was removed
// elsewhere") precisely because it may be the only copy of the user's edit; the
// next `lw storage add <other> --name f.bin` used to rename straight over it.
func TestAddRefusesUntrackedOccupant(t *testing.T) {
	const kept = "original + the user's precious edit"
	cases := []struct {
		name  string
		file  string // the name Add is asked to store under
		setup func(t *testing.T, vault string, st *storage.Storage)
	}{
		{"a file Reconcile kept", "f.bin", func(t *testing.T, vault string, st *storage.Storage) {
			addFile(t, st, "f.bin", "original")
			writeDisk(t, vault, "f.bin", kept)
			writeCatalog(t, vault) // another PC removed it
			res, err := st.Reconcile(t.Context(), holdsAll)
			if err != nil || !reflect.DeepEqual(res.Kept, []string{"f.bin"}) {
				t.Fatalf("setup: Reconcile = %+v, %v; want f.bin kept", res, err)
			}
		}},
		{"a file nobody catalogued", "f.bin", func(t *testing.T, vault string, st *storage.Storage) {
			writeDisk(t, vault, "f.bin", kept)
		}},
		{"a file in a directory", "d/f.bin", func(t *testing.T, vault string, st *storage.Storage) {
			writeDisk(t, vault, "d/f.bin", kept)
		}},
	}
	for _, tc := range cases {
		for _, force := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "", true: ", --force"}[force], func(t *testing.T) {
				vault, st := newStore(t)
				tc.setup(t, vault, st)
				name := tc.file
				_, err := st.Add(srcFile(t, "something unrelated"), name, "", force)
				if err == nil {
					t.Fatal("Add over an untracked file reported success")
				}
				for _, want := range []string{"storage/" + name + " exists and is not in the catalog", "--name"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error = %q, want it to contain %q", err, want)
					}
				}
				if b, _ := os.ReadFile(diskPath(vault, name)); string(b) != kept {
					t.Errorf("storage/%s = %q: the untracked bytes were replaced", name, b)
				}
				if entries, _ := st.List(); len(entries) != 0 {
					t.Errorf("catalog = %v, want no entry", entries)
				}
				if rows := readLedger(t, vault); rows[name] != "" {
					t.Errorf("ledger = %v: a refused Add recorded a row", rows)
				}
				if l := leftovers(t, vault); len(l) != 0 {
					t.Errorf("temp files left behind: %v", l)
				}
			})
		}
	}

	t.Run("--force over a tracked entry still replaces it", func(t *testing.T) {
		vault, st := newStore(t)
		addFile(t, st, "f.bin", "version one")
		e, err := st.Add(srcFile(t, "version two!"), "f.bin", "", true)
		if err != nil {
			t.Fatalf("Add --force over a tracked entry: %v", err)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != "version two!" || e.SHA256 != fileSHA256("version two!") {
			t.Errorf("storage/f.bin = %q, entry %+v; want the replacement", b, e)
		}
	})
}

// TestAddAdoptsIdenticalOccupant is Y-1's other half: bytes that are already the
// source's are adopted - catalogued and recorded, not copied over themselves.
func TestAddAdoptsIdenticalOccupant(t *testing.T) {
	const body = "bytes a sync left here and the user now stores for real"
	vault, st := newStore(t)
	writeDisk(t, vault, "f.bin", body)
	before, err := os.Stat(diskPath(vault, "f.bin"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := st.Add(srcFile(t, body), "f.bin", "", false)
	if err != nil {
		t.Fatalf("Add of the bytes already there: %v", err)
	}
	after, err := os.Stat(diskPath(vault, "f.bin"))
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("storage/f.bin was replaced by a copy (same inode: %v, %v); identical bytes are adopted", os.SameFile(before, after), err)
	}
	if e.SHA256 != fileSHA256(body) || e.Size != int64(len(body)) {
		t.Errorf("entry = %+v", e)
	}
	if rows := readLedger(t, vault); rows["f.bin"] != e.SHA256 {
		t.Errorf("ledger = %v, want a row for the adopted file", rows)
	}
	if rep, _ := st.Status(); len(rep.Entries) != 1 || rep.Entries[0].State != storage.StateVerified || len(rep.Orphans) != 0 {
		t.Errorf("status = %+v, want one verified entry and no orphan", rep)
	}
	if l := leftovers(t, vault); len(l) != 0 {
		t.Errorf("temp files left behind: %v", l)
	}

	t.Run("the source is the file already in storage/", func(t *testing.T) {
		vault, st := newStore(t)
		writeDisk(t, vault, "g.bin", body)
		if _, err := st.Add(diskPath(vault, "g.bin"), "", "", false); err != nil {
			t.Fatalf("Add of a file that is already storage/g.bin: %v", err)
		}
		if rep, _ := st.Status(); len(rep.Entries) != 1 || rep.Entries[0].State != storage.StateVerified {
			t.Errorf("status = %+v, want one verified entry", rep)
		}
	})
}

// TestAddRefusesReservedTempName is Y-2 (G-2): lw's own temp files are called
// .storage-tmp-*, and Reconcile sweeps those that are old. A payload with such a
// name is deleted by the sweep once it is an hour old.
func TestAddRefusesReservedTempName(t *testing.T) {
	for _, name := range []string{".storage-tmp-keep", "d/.storage-tmp-x", ".storage-tmp-"} {
		t.Run(name, func(t *testing.T) {
			vault, st := newStore(t)
			_, err := st.Add(srcFile(t, "my only data"), name, "", false)
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("Add %q = %v, want a refusal that says the name is reserved", name, err)
			}
			if entries, _ := st.List(); len(entries) != 0 {
				t.Errorf("catalog = %v, want no entry", entries)
			}
			if exists(filepath.Join(vault, "storage")) {
				t.Error("storage/ was created by a refused Add")
			}
		})
	}
	t.Run("only a name that starts with the prefix is reserved", func(t *testing.T) {
		_, st := newStore(t)
		for _, name := range []string{"a.storage-tmp-x", "storage-tmp-x", ".storage-tmp/x"} {
			if _, err := st.Add(srcFile(t, "ordinary "+name), name, "", false); err != nil {
				t.Errorf("Add %q: %v", name, err)
			}
		}
	})
}

// TestReservedNameInCatalogIsCorrupt is Y-2's catalog side: another PC (or an older
// lw) may have catalogued a name that is lw's temp prefix. That record is corrupt
// here, named in the status and never fetched, like any record that cannot be
// acted on (X-8); `lw storage rm` still removes it.
func TestReservedNameInCatalogIsCorrupt(t *testing.T) {
	const body = "payload under a reserved name"
	vault, st := newStore(t)
	writeCatalog(t, vault, entryFor(".storage-tmp-keep", body))
	rep, err := st.Status()
	if err != nil || len(rep.Entries) != 1 || rep.Entries[0].State != storage.StateCorrupt {
		t.Fatalf("Status = %+v, %v; want the record corrupt", rep, err)
	}
	if err := st.Materialize(t.Context(), ".storage-tmp-keep", strings.NewReader(body), holdsAll); err == nil {
		t.Error("Materialize wrote a payload under a reserved name")
	}
	if exists(diskPath(vault, ".storage-tmp-keep")) {
		t.Error("storage/.storage-tmp-keep was written")
	}
	if err := st.Remove(".storage-tmp-keep"); err != nil {
		t.Errorf("Remove of the record: %v", err)
	}
}

// TestSweepSkipsCataloguedName is Y-2's second guard: whatever put a catalogued
// file under a temp-looking name (an older lw, another PC's catalog), the sweep
// never reads that name as debris.
func TestSweepSkipsCataloguedName(t *testing.T) {
	vault, st := newStore(t)
	const body = "my only data"
	writeCatalog(t, vault, entryFor(".storage-tmp-keep", body), entryFor("d/.storage-tmp-deep", body+" too"))
	old := time.Now().Add(-2 * time.Hour)
	for name, content := range map[string]string{
		".storage-tmp-keep":      body,
		"d/.storage-tmp-deep":    body + " too",
		".storage-tmp-debris":    "a killed fetch",
		"d/.storage-tmp-debris2": "another one",
	} {
		writeDisk(t, vault, name, content)
		if err := os.Chtimes(diskPath(vault, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Reconcile(t.Context(), holdsAll); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, name := range []string{".storage-tmp-keep", "d/.storage-tmp-deep"} {
		if !exists(diskPath(vault, name)) {
			t.Errorf("the sweep deleted the catalogued %s (the only copy) because of its name", name)
		}
	}
	for _, name := range []string{".storage-tmp-debris", "d/.storage-tmp-debris2"} {
		if exists(diskPath(vault, name)) {
			t.Errorf("%s (2 h old, not catalogued) survived the sweep", name)
		}
	}
}

// TestReconcileProvesEvenConfirmed is Y-3 (G-4) for the delete: the confirmed bit
// is a note this PC made once about a remote that may since have lost the object
// (a store restored from a git-only backup, a wiped directory). Before bytes are
// destroyed the remote is asked, whatever the ledger says - and when it lacks the
// object it is given it first.
func TestReconcileProvesEvenConfirmed(t *testing.T) {
	const body = "a payload the remote used to hold"
	vault, st, e := storedAndRemoved(t, "f.bin", body)
	if err := st.Confirm(e.SHA256); err != nil {
		t.Fatal(err)
	}
	remote := newFakeRemote() // an empty store at the same URL
	res, err := st.Reconcile(t.Context(), remote.Ensure)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if remote.calls != 1 || !remote.have[e.SHA256] {
		t.Errorf("ensure calls %d, remote holds the payload: %v; want one call that saved it (the confirmation was a claim, not a proof)", remote.calls, remote.have[e.SHA256])
	}
	if !reflect.DeepEqual(res.Removed, []string{"f.bin"}) || !reflect.DeepEqual(res.Preserved, []string{"f.bin"}) {
		t.Errorf("Reconcile = %+v, want f.bin removed after it was uploaded", res)
	}
	if exists(diskPath(vault, "f.bin")) {
		t.Error("f.bin is still on disk")
	}
}

// TestReconcileOfflineKeepsConfirmedFile is Y-3 for a remote that cannot be asked:
// the file stays, its row stays, the user is told once, and the next call that
// can ask finishes the job.
func TestReconcileOfflineKeepsConfirmedFile(t *testing.T) {
	const body = "a payload this PC believes the remote holds"
	vault, st, e := storedAndRemoved(t, "f.bin", body)
	if err := st.Confirm(e.SHA256); err != nil {
		t.Fatal(err)
	}
	remote := newFakeRemote()
	remote.err = errors.New("connection refused")
	remote.transient = true

	for i, wantReports := range []int{1, 0} {
		res, err := st.Reconcile(t.Context(), remote.Ensure)
		if err != nil {
			t.Fatalf("Reconcile #%d: %v", i+1, err)
		}
		if len(res.Removed) != 0 || len(res.NoCopy) != wantReports {
			t.Fatalf("Reconcile #%d = %+v, want nothing removed and %d report(s)", i+1, res, wantReports)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != body {
			t.Fatalf("Reconcile #%d: storage/f.bin = %q, want it kept while the remote cannot be asked", i+1, b)
		}
		if rows := readLedger(t, vault); rows["f.bin"] != e.SHA256 {
			t.Fatalf("Reconcile #%d: ledger = %v, want the row kept so a later call retries", i+1, rows)
		}
	}
	if remote.calls != 2 {
		t.Errorf("ensure was called %d times, want 2 (every call asks again)", remote.calls)
	}

	remote.err = nil
	remote.have[e.SHA256] = true
	res, err := st.Reconcile(t.Context(), remote.Ensure)
	if err != nil || !reflect.DeepEqual(res.Removed, []string{"f.bin"}) || len(res.Preserved) != 0 {
		t.Errorf("Reconcile after the remote came back = %+v, %v; want f.bin removed", res, err)
	}
	if exists(diskPath(vault, "f.bin")) {
		t.Error("f.bin is still on disk")
	}
}

// TestMaterializeProvesStaleEvenConfirmed is Y-3 for the overwrite: replacing a
// stale file destroys the version lw put there, so that version is proven on the
// remote first, confirmed or not.
func TestMaterializeProvesStaleEvenConfirmed(t *testing.T) {
	const oldBody, newBody = "version one, put here by lw", "version two, newer and longer"
	setup := func(t *testing.T) (string, *storage.Storage, storage.Entry) {
		t.Helper()
		vault, st := newStore(t)
		old := addFile(t, st, "f.bin", oldBody)
		if err := st.Confirm(old.SHA256); err != nil {
			t.Fatal(err)
		}
		writeCatalog(t, vault, entryFor("f.bin", newBody)) // another PC replaced it
		return vault, st, old
	}

	t.Run("the remote lost the old version: it is saved, then replaced", func(t *testing.T) {
		vault, st, old := setup(t)
		remote := newFakeRemote()
		if err := st.Materialize(t.Context(), "f.bin", strings.NewReader(newBody), remote.Ensure); err != nil {
			t.Fatalf("Materialize: %v", err)
		}
		if remote.calls != 1 || !remote.have[old.SHA256] {
			t.Errorf("ensure calls %d, old version on the remote: %v; want it saved before it was replaced", remote.calls, remote.have[old.SHA256])
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != newBody {
			t.Errorf("storage/f.bin = %q, want the new version", b)
		}
	})

	t.Run("the remote cannot be asked: the file stays and the stream is not read", func(t *testing.T) {
		vault, st, old := setup(t)
		remote := newFakeRemote()
		remote.err = errors.New("no route to host")
		remote.transient = true
		r := &countingReader{r: strings.NewReader(newBody)}
		err := st.Materialize(t.Context(), "f.bin", r, remote.Ensure)
		var nc *storage.NoCopyError
		if !errors.As(err, &nc) || !nc.Transient {
			t.Fatalf("Materialize = %v, want a transient *NoCopyError", err)
		}
		if r.n != 0 {
			t.Errorf("the stream was read (%d bytes) although the old version could not be proven", r.n)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != oldBody {
			t.Errorf("storage/f.bin = %q, want the old version kept", b)
		}
		if rows := readLedger(t, vault); rows["f.bin"] != old.SHA256 {
			t.Errorf("ledger = %v, want the old row kept", rows)
		}
	})
}

// lockHeld takes the storage lock the way another lw process does and returns the
// function that lets it go.
func lockHeld(t *testing.T, vault string) (release func()) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(vault, ".llmwiki", "tmp", "storage.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatal(err)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
}

// TestConfirmAndRemoveTakeTheLock is Y-4 (R15, R18): every writer of the ledger or
// the catalog excludes the others (X-5). Only Add was raced by a test; Confirm
// and Remove each read, change and replace a file, and lost updates there are as
// real.
func TestConfirmAndRemoveTakeTheLock(t *testing.T) {
	vault, st := newStore(t)
	seed := addFile(t, st, "seed.bin", "seed") // creates .llmwiki/tmp
	addFile(t, st, "other.bin", "other")
	for name, call := range map[string]func() error{
		"Confirm":     func() error { return st.Confirm(fileSHA256("anything")) },
		"Remove":      func() error { return st.Remove("other.bin") },
		"MarkDamaged": func() error { return st.MarkDamaged(seed.SHA256) },
	} {
		t.Run(name, func(t *testing.T) {
			release := lockHeld(t, vault)
			released := false
			defer func() {
				if !released {
					release()
				}
			}()
			done := make(chan error, 1)
			go func() { done <- call() }()
			select {
			case err := <-done:
				t.Fatalf("%s finished (%v) while another process held the storage lock: its read-modify-write is not serialized (X-5)", name, err)
			case <-time.After(300 * time.Millisecond):
			}
			release()
			released = true
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s after the lock was released: %v", name, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s did not finish after the lock was released", name)
			}
		})
	}
}

// TestMaterializeRefusesCatalogChangedDuringFetch is Y-4 (R22): the bytes were
// fetched for the entry as it was; if the catalog names other bytes by the time
// they arrive, they are not written.
func TestMaterializeRefusesCatalogChangedDuringFetch(t *testing.T) {
	const body = "the bytes the entry named when the fetch began"
	for name, changed := range map[string]storage.Entry{
		"another sha256, same size": entryFor("f.bin", strings.ToUpper(body)),
		"another size":              func() storage.Entry { e := entryFor("f.bin", body); e.Size++; return e }(),
	} {
		t.Run(name, func(t *testing.T) {
			vault, st := newStore(t)
			writeCatalog(t, vault, entryFor("f.bin", body))
			r := &hookReader{r: strings.NewReader(body), hook: func() { writeCatalog(t, vault, changed) }}
			err := st.Materialize(t.Context(), "f.bin", r, holdsAll)
			if err == nil || !strings.Contains(err.Error(), "catalog changed") {
				t.Fatalf("Materialize = %v, want a refusal that says the catalog changed", err)
			}
			if exists(diskPath(vault, "f.bin")) {
				t.Error("storage/f.bin was written although the catalog named other bytes by then")
			}
			if rows := readLedger(t, vault); len(rows) != 0 {
				t.Errorf("ledger = %v, want no rows", rows)
			}
			if l := leftovers(t, vault); len(l) != 0 {
				t.Errorf("temp files left behind: %v", l)
			}
		})
	}
}

// repeatReader yields n bytes of 'x'.
func repeatReader(n int64) io.Reader { return io.LimitReader(xReader{}, n) }

type xReader struct{}

func (xReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// TestFetchCutsOversizedStream is Y-4 (R9): a remote object longer than the
// catalog says is cut at one byte past the size, not read to its end into a temp
// file. A hostile or damaged remote could otherwise fill the disk before the size
// check ran.
func TestFetchCutsOversizedStream(t *testing.T) {
	const body = "ten bytes!"
	vault, st := newStore(t)
	writeCatalog(t, vault, entryFor("f.bin", body))
	r := &countingReader{r: repeatReader(8 << 20)}
	err := st.Materialize(t.Context(), "f.bin", r, holdsAll)
	var ve *storage.VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("Materialize = %v, want a *VerifyError for the oversized stream", err)
	}
	if limit := len(body) + 1; r.n > limit {
		t.Errorf("Materialize read %d bytes of an oversized object, want at most %d (the catalog's size + 1)", r.n, limit)
	}
	if exists(diskPath(vault, "f.bin")) {
		t.Error("storage/f.bin was written from an oversized stream")
	}
	if l := leftovers(t, vault); len(l) != 0 {
		t.Errorf("temp files left behind: %v", l)
	}
}

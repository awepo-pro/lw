package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/storage"
)

// 066 FIX1 tests for internal/storage: rule P (preserve before destroy, X-1),
// the re-check before a destroy (X-3), the lock (X-5), the stat-first paths
// (X-6), bad catalog records (X-8), the temp sweep (X-10) and Verify (X-13).
//
// The remote is a fake: fakeRemote answers the storage.Ensure the way cmd/lw's
// does (a HaveObjects question, an upload for what is missing) without a network,
// and lets a test make the answer fail, or make the world move while it is
// being given.

// fakeRemote stands in for the remote object store. It reads the file at
// Blob.Path to "upload" it, so a Blob whose Path is wrong shows.
type fakeRemote struct {
	mu          sync.Mutex
	have        map[string]bool
	err         error // when set: a blob the remote lacks fails with it
	transient   bool  // ... and the failure is the kind a later call can get past
	calls       int
	asked       [][]string // the names of each call, in order
	afterEnsure func()     // runs once the answer is ready and before it is returned: the world moving during a slow upload
}

func newFakeRemote() *fakeRemote { return &fakeRemote{have: map[string]bool{}} }

func (f *fakeRemote) Ensure(ctx context.Context, blobs []storage.Blob) map[string]storage.EnsureResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	var names []string
	for _, b := range blobs {
		names = append(names, b.Name)
	}
	f.asked = append(f.asked, names)
	out := map[string]storage.EnsureResult{}
	for _, b := range blobs {
		switch {
		case f.have[b.SHA256]:
			out[b.SHA256] = storage.EnsureResult{Held: true}
		case f.err != nil:
			out[b.SHA256] = storage.EnsureResult{Err: f.err, Transient: f.transient}
		default:
			data, err := os.ReadFile(b.Path)
			if err != nil || fileSHA256(string(data)) != b.SHA256 || int64(len(data)) != b.Size {
				out[b.SHA256] = storage.EnsureResult{Err: errors.New("fake remote: the file is not the blob")}
				continue
			}
			f.have[b.SHA256] = true
			out[b.SHA256] = storage.EnsureResult{Held: true, Uploaded: true}
		}
	}
	if f.afterEnsure != nil {
		f.afterEnsure()
	}
	return out
}

// holdsAll is the Ensure of a remote that takes anything: for the tests whose
// subject is not the remote.
var holdsAll storage.Ensure = func(_ context.Context, blobs []storage.Blob) map[string]storage.EnsureResult {
	out := map[string]storage.EnsureResult{}
	for _, b := range blobs {
		out[b.SHA256] = storage.EnsureResult{Held: true, Uploaded: true}
	}
	return out
}

// rawLedger is the whole ledger file, including the sets readLedger leaves out.
type rawLedger struct {
	Version      int               `json:"version"`
	Materialized map[string]string `json:"materialized"`
	Scope        string            `json:"scope"`
	Confirmed    map[string]bool   `json:"confirmed"`
	Told         map[string]bool   `json:"told"`
	Damaged      map[string]bool   `json:"damaged"`
}

func readRawLedger(t *testing.T, vault string) rawLedger {
	t.Helper()
	data, err := os.ReadFile(ledgerFile(vault))
	if errors.Is(err, os.ErrNotExist) {
		return rawLedger{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var l rawLedger
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatalf("ledger is not JSON: %v\n%s", err, data)
	}
	return l
}

// storedAndRemoved is the situation rule P exists for: this PC put name there
// (a ledger row, unconfirmed), and a pull moved the catalog on without it.
func storedAndRemoved(t *testing.T, name, content string) (vault string, st *storage.Storage, e storage.Entry) {
	t.Helper()
	vault, st = newStore(t)
	e = addFile(t, st, name, content)
	writeCatalog(t, vault) // another PC removed every entry
	return vault, st, e
}

func TestReconcileKeepsOnlyCopy(t *testing.T) {
	const body = "the only copy of this payload"

	t.Run("the upload fails for good: file kept, reported, row dropped", func(t *testing.T) {
		vault, st, _ := storedAndRemoved(t, "f.bin", body)
		remote := newFakeRemote()
		remote.err = errors.New("the source changed")
		res, err := st.Reconcile(t.Context(), remote.Ensure)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(res.Removed) != 0 {
			t.Errorf("Removed = %q: the only copy was deleted without a copy elsewhere (B7)", res.Removed)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != body {
			t.Errorf("storage/f.bin = %q, want it kept", b)
		}
		if len(res.NoCopy) != 1 || res.NoCopy[0].Name != "f.bin" || !strings.Contains(res.NoCopy[0].Reason, "the source changed") {
			t.Errorf("NoCopy = %+v, want f.bin with the reason", res.NoCopy)
		}
		if rows := readLedger(t, vault); len(rows) != 0 {
			t.Errorf("ledger = %v: a permanent failure drops the row (the file is an orphan now)", rows)
		}
		if rep, _ := st.Status(); !reflect.DeepEqual(rep.Orphans, []string{"f.bin"}) {
			t.Errorf("orphans = %q, want [f.bin]", rep.Orphans)
		}
	})

	t.Run("no ensure: nothing proves a copy, so nothing is deleted", func(t *testing.T) {
		vault, st, e := storedAndRemoved(t, "f.bin", body)
		res, err := st.Reconcile(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Removed) != 0 || !exists(diskPath(vault, "f.bin")) {
			t.Errorf("a nil ensure deleted the file: %+v", res)
		}
		if len(res.NoCopy) != 1 {
			t.Errorf("NoCopy = %+v, want the file reported once", res.NoCopy)
		}
		if got := readLedger(t, vault); got["f.bin"] != e.SHA256 {
			t.Errorf("ledger = %v: with no remote to ask the row stays, so a later call can retry", got)
		}
	})

	t.Run("the upload works: the copy is kept on the remote, then the file goes", func(t *testing.T) {
		vault, st, e := storedAndRemoved(t, "d/f.bin", body)
		remote := newFakeRemote()
		res, err := st.Reconcile(t.Context(), remote.Ensure)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(res.Removed, []string{"d/f.bin"}) || !reflect.DeepEqual(res.Preserved, []string{"d/f.bin"}) {
			t.Errorf("Removed %q Preserved %q, want d/f.bin in both", res.Removed, res.Preserved)
		}
		if exists(diskPath(vault, "d/f.bin")) || exists(diskPath(vault, "d")) {
			t.Error("the file (or its emptied directory) is still on disk")
		}
		if !remote.have[e.SHA256] {
			t.Error("the remote never got the payload")
		}
		if led := readRawLedger(t, vault); !led.Confirmed[e.SHA256] || len(led.Materialized) != 0 {
			t.Errorf("ledger = %+v, want the sha confirmed and no rows", led)
		}
	})

	t.Run("already on the remote: removed, and not called preserved", func(t *testing.T) {
		_, st, e := storedAndRemoved(t, "f.bin", body)
		remote := newFakeRemote()
		remote.have[e.SHA256] = true
		res, err := st.Reconcile(t.Context(), remote.Ensure)
		if err != nil || !reflect.DeepEqual(res.Removed, []string{"f.bin"}) || len(res.Preserved) != 0 {
			t.Errorf("Reconcile = %+v, %v; want f.bin removed, none preserved", res, err)
		}
	})

	// FIX2 Y-3 amendment (was "a confirmed copy needs no network", with neverEnsure):
	// a confirmation is a note, not a proof, so the remote is asked all the same.
	t.Run("a confirmed copy is proven again before the file goes", func(t *testing.T) {
		vault, st, e := storedAndRemoved(t, "f.bin", body)
		if err := st.Confirm(e.SHA256); err != nil {
			t.Fatal(err)
		}
		remote := newFakeRemote()
		remote.have[e.SHA256] = true
		res, err := st.Reconcile(t.Context(), remote.Ensure)
		if err != nil || !reflect.DeepEqual(res.Removed, []string{"f.bin"}) || len(res.Preserved) != 0 || remote.calls != 1 {
			t.Errorf("Reconcile = %+v, %v, ensure calls %d; want f.bin removed after one question", res, err, remote.calls)
		}
		if exists(diskPath(vault, "f.bin")) {
			t.Error("f.bin is still on disk")
		}
	})

	// FIX2 Y-3 amendment (was "only the unconfirmed blobs are asked", b.bin and c.bin):
	// every file about to be deleted is asked about, confirmed or not.
	t.Run("every blob about to be deleted is asked, in one call", func(t *testing.T) {
		vault, st := newStore(t)
		ea := addFile(t, st, "a.bin", "alpha payload")
		addFile(t, st, "b.bin", "bravo payload")
		addFile(t, st, "c.bin", "charlie payload")
		addFile(t, st, "d.bin", "delta payload")
		if err := st.Confirm(ea.SHA256); err != nil {
			t.Fatal(err)
		}
		writeCatalog(t, vault)
		writeDisk(t, vault, "d.bin", "edited by hand!") // changed locally: kept, not asked
		remote := newFakeRemote()
		res, err := st.Reconcile(t.Context(), remote.Ensure)
		if err != nil {
			t.Fatal(err)
		}
		if remote.calls != 1 || !reflect.DeepEqual(remote.asked[0], []string{"a.bin", "b.bin", "c.bin"}) {
			t.Errorf("ensure calls %d, asked %v; want one call for a.bin, b.bin and c.bin (d.bin was edited: kept, not asked)", remote.calls, remote.asked)
		}
		if want := []string{"a.bin", "b.bin", "c.bin"}; !reflect.DeepEqual(res.Removed, want) {
			t.Errorf("Removed = %q, want %q", res.Removed, want)
		}
		if want := []string{"d.bin"}; !reflect.DeepEqual(res.Kept, want) {
			t.Errorf("Kept = %q, want %q", res.Kept, want)
		}
	})
}

func TestReconcileRetriesTransientEnsure(t *testing.T) {
	vault, st, e := storedAndRemoved(t, "f.bin", "a payload whose upload keeps failing")
	remote := newFakeRemote()
	remote.err = errors.New("connection refused")
	remote.transient = true

	res, err := st.Reconcile(t.Context(), remote.Ensure)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 0 || len(res.NoCopy) != 1 || !strings.Contains(res.NoCopy[0].Reason, "connection refused") {
		t.Fatalf("first Reconcile = %+v, want the file kept and reported with the reason", res)
	}
	if got := readLedger(t, vault); got["f.bin"] != e.SHA256 {
		t.Fatalf("ledger = %v: a transient failure must keep the row so a later sync retries (a dropped row makes the file an orphan nobody retries)", got)
	}

	// The same failure again: still kept, still retried, but said once.
	res, err = st.Reconcile(t.Context(), remote.Ensure)
	if err != nil || len(res.Removed) != 0 || len(res.NoCopy) != 0 {
		t.Errorf("second Reconcile = %+v, %v; want the same state and no repeated line", res, err)
	}
	if remote.calls != 2 {
		t.Errorf("ensure was called %d times, want 2 (every call retries)", remote.calls)
	}
	if got := readLedger(t, vault); got["f.bin"] != e.SHA256 {
		t.Errorf("ledger after the second call = %v, want the row kept", got)
	}

	// The remote comes back.
	remote.err = nil
	res, err = st.Reconcile(t.Context(), remote.Ensure)
	if err != nil || !reflect.DeepEqual(res.Removed, []string{"f.bin"}) || !reflect.DeepEqual(res.Preserved, []string{"f.bin"}) {
		t.Fatalf("third Reconcile = %+v, %v; want f.bin uploaded and removed", res, err)
	}
	if exists(diskPath(vault, "f.bin")) {
		t.Error("f.bin is still on disk")
	}
	if led := readRawLedger(t, vault); len(led.Materialized) != 0 || len(led.Told) != 0 {
		t.Errorf("ledger = %+v, want no rows and no leftover throttle entry", led)
	}
}

// countingReader counts the bytes read from it.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestMaterializePreservesUnconfirmedStale(t *testing.T) {
	const oldBody, newBody = "version one, put here by lw", "version two, newer and longer"
	setup := func(t *testing.T) (string, *storage.Storage, storage.Entry) {
		t.Helper()
		vault, st := newStore(t)
		old := addFile(t, st, "f.bin", oldBody)
		writeCatalog(t, vault, entryFor("f.bin", newBody)) // another PC replaced it
		return vault, st, old
	}

	t.Run("no copy of the old version: the file stays, the stream is not read", func(t *testing.T) {
		vault, st, old := setup(t)
		remote := newFakeRemote()
		remote.err = errors.New("no route to host")
		remote.transient = true
		r := &countingReader{r: strings.NewReader(newBody)}
		err := st.Materialize(t.Context(), "f.bin", r, remote.Ensure)
		var nc *storage.NoCopyError
		if !errors.As(err, &nc) || nc.Name != "f.bin" || !nc.Transient || !strings.Contains(nc.Reason, "no route to host") {
			t.Fatalf("Materialize = %v, want a *NoCopyError for f.bin, transient, with the reason", err)
		}
		if want := "kept f.bin: the replaced version has no copy on the remote yet (no route to host)"; !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != oldBody {
			t.Errorf("storage/f.bin = %q: the only copy of version one was overwritten (F-1)", b)
		}
		if r.n != 0 {
			t.Errorf("%d bytes of the stream were read before the old version was safe", r.n)
		}
		if got := readLedger(t, vault); got["f.bin"] != old.SHA256 {
			t.Errorf("ledger = %v, want the old row unchanged", got)
		}
		if l := leftovers(t, vault); len(l) != 0 {
			t.Errorf("temp files left behind: %v", l)
		}
		if rep, _ := st.Status(); len(rep.Entries) != 1 || rep.Entries[0].State != storage.StatePending || !rep.Entries[0].Stale {
			t.Errorf("Status = %+v, want f.bin still pending and stale", rep)
		}
	})

	t.Run("a nil ensure is the same refusal", func(t *testing.T) {
		vault, st, _ := setup(t)
		var nc *storage.NoCopyError
		if err := st.Materialize(t.Context(), "f.bin", strings.NewReader(newBody), nil); !errors.As(err, &nc) {
			t.Fatalf("Materialize with no ensure = %v, want a *NoCopyError", err)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != oldBody {
			t.Errorf("storage/f.bin = %q", b)
		}
	})

	t.Run("the old version is uploaded first, then replaced", func(t *testing.T) {
		vault, st, old := setup(t)
		remote := newFakeRemote()
		if err := st.Materialize(t.Context(), "f.bin", strings.NewReader(newBody), remote.Ensure); err != nil {
			t.Fatalf("Materialize: %v", err)
		}
		if !remote.have[old.SHA256] {
			t.Error("the old version never reached the remote")
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != newBody {
			t.Errorf("storage/f.bin = %q, want the new version", b)
		}
		led := readRawLedger(t, vault)
		if led.Materialized["f.bin"] != fileSHA256(newBody) || !led.Confirmed[old.SHA256] || !led.Confirmed[fileSHA256(newBody)] {
			t.Errorf("ledger = %+v, want the new row and both versions confirmed (the new one came from the remote)", led)
		}
	})

	// FIX2 Y-3 amendment (was "a confirmed old version needs no network", neverEnsure).
	t.Run("a confirmed old version is proven again before it is replaced", func(t *testing.T) {
		vault, st, old := setup(t)
		if err := st.Confirm(old.SHA256); err != nil {
			t.Fatal(err)
		}
		remote := newFakeRemote()
		remote.have[old.SHA256] = true
		if err := st.Materialize(t.Context(), "f.bin", strings.NewReader(newBody), remote.Ensure); err != nil {
			t.Fatalf("Materialize: %v", err)
		}
		if remote.calls != 1 {
			t.Errorf("ensure calls %d, want 1", remote.calls)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != newBody {
			t.Errorf("storage/f.bin = %q", b)
		}
	})

	t.Run("an absent file has nothing to preserve", func(t *testing.T) {
		vault, st := newStore(t)
		writeCatalog(t, vault, entryFor("g.bin", newBody))
		if err := st.Materialize(t.Context(), "g.bin", strings.NewReader(newBody), nil); err != nil {
			t.Fatalf("Materialize of an absent file with no ensure: %v", err)
		}
	})
}

// hookReader runs hook once, before the first byte: the world moving while the
// download is in progress.
type hookReader struct {
	r    io.Reader
	once sync.Once
	hook func()
}

func (h *hookReader) Read(p []byte) (int, error) {
	h.once.Do(h.hook)
	return h.r.Read(p)
}

func TestReconcileRecheckBeforeRemove(t *testing.T) {
	t.Run("edited while the copy was being made", func(t *testing.T) {
		vault, st, _ := storedAndRemoved(t, "f.bin", "bytes lw put here")
		remote := newFakeRemote()
		remote.afterEnsure = func() { writeDisk(t, vault, "f.bin", "bytes the user wrote during the upload") }
		res, err := st.Reconcile(t.Context(), remote.Ensure)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != "bytes the user wrote during the upload" {
			t.Errorf("storage/f.bin = %q: the user's edit was deleted (the upload took time; the decision was made before it)", b)
		}
		if len(res.Removed) != 0 || !reflect.DeepEqual(res.Kept, []string{"f.bin"}) {
			t.Errorf("Reconcile = %+v, want f.bin kept, nothing removed", res)
		}
		if rows := readLedger(t, vault); len(rows) != 0 {
			t.Errorf("ledger = %v, want the row dropped (the file is the user's now)", rows)
		}
	})

	t.Run("deleted while the copy was being made", func(t *testing.T) {
		vault, st, _ := storedAndRemoved(t, "f.bin", "bytes lw put here")
		remote := newFakeRemote()
		remote.afterEnsure = func() { os.Remove(diskPath(vault, "f.bin")) }
		res, err := st.Reconcile(t.Context(), remote.Ensure)
		if err != nil || len(res.Removed) != 0 || len(res.Kept) != 0 {
			t.Errorf("Reconcile = %+v, %v; want nothing to report", res, err)
		}
		if rows := readLedger(t, vault); len(rows) != 0 {
			t.Errorf("ledger = %v, want the row dropped", rows)
		}
	})

	t.Run("re-added while the copy was being made", func(t *testing.T) {
		vault, st, e := storedAndRemoved(t, "f.bin", "bytes lw put here")
		remote := newFakeRemote()
		remote.afterEnsure = func() { writeCatalog(t, vault, e) } // another pull brought the entry back
		res, err := st.Reconcile(t.Context(), remote.Ensure)
		if err != nil || len(res.Removed) != 0 {
			t.Errorf("Reconcile = %+v, %v; want nothing removed: the entry is catalogued again", res, err)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != "bytes lw put here" {
			t.Errorf("storage/f.bin = %q, want it kept", b)
		}
	})
}

func TestMaterializeRecheckBeforeRename(t *testing.T) {
	const body = "the catalog's bytes for this file"

	t.Run("a file appeared while the stream was being read", func(t *testing.T) {
		vault, st := newStore(t)
		writeCatalog(t, vault, entryFor("f.bin", body))
		r := &hookReader{r: strings.NewReader(body), hook: func() { writeDisk(t, vault, "f.bin", "the user's own file") }}
		err := st.Materialize(t.Context(), "f.bin", r, holdsAll)
		if err == nil || !strings.Contains(err.Error(), "changed on disk") {
			t.Fatalf("Materialize = %v, want a refusal that says the file changed on disk", err)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != "the user's own file" {
			t.Errorf("storage/f.bin = %q: the user's file was overwritten (F-5)", b)
		}
		if rows := readLedger(t, vault); len(rows) != 0 {
			t.Errorf("ledger = %v, want no rows", rows)
		}
		if l := leftovers(t, vault); len(l) != 0 {
			t.Errorf("temp files left behind: %v", l)
		}
	})

	t.Run("a stale file was edited while the stream was being read", func(t *testing.T) {
		vault, st := newStore(t)
		addFile(t, st, "f.bin", "the old version")
		writeCatalog(t, vault, entryFor("f.bin", body))
		r := &hookReader{r: strings.NewReader(body), hook: func() { writeDisk(t, vault, "f.bin", "edited during the download") }}
		err := st.Materialize(t.Context(), "f.bin", r, holdsAll)
		if err == nil || !strings.Contains(err.Error(), "changed on disk") {
			t.Fatalf("Materialize = %v, want a refusal that says the file changed on disk", err)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != "edited during the download" {
			t.Errorf("storage/f.bin = %q: the edit was overwritten", b)
		}
		if l := leftovers(t, vault); len(l) != 0 {
			t.Errorf("temp files left behind: %v", l)
		}
	})

	t.Run("the catalog moved on while the stream was being read", func(t *testing.T) {
		vault, st := newStore(t)
		writeCatalog(t, vault, entryFor("f.bin", body))
		r := &hookReader{r: strings.NewReader(body), hook: func() { writeCatalog(t, vault) }}
		if err := st.Materialize(t.Context(), "f.bin", r, holdsAll); err == nil {
			t.Fatal("Materialize wrote a file the catalog no longer lists")
		}
		if exists(diskPath(vault, "f.bin")) {
			t.Error("storage/f.bin exists although the entry was removed during the fetch")
		}
	})
}

func TestAddHealsPending(t *testing.T) {
	vault, st := newStore(t)
	const body = "the exact catalog bytes"
	first := addFile(t, st, "d/f.bin", body)
	if err := os.Remove(diskPath(vault, "d/f.bin")); err != nil {
		t.Fatal(err)
	}
	if rep, _ := st.Status(); rep.Entries[0].State != storage.StatePending {
		t.Fatalf("setup: state = %v, want pending", rep.Entries[0].State)
	}

	src := createTestFile(t, t.TempDir(), "again.bin", body)
	got, err := st.Add(src, "d/f.bin", "", false)
	if err != nil {
		t.Fatalf("Add of the catalog's own bytes: %v", err)
	}
	if got.SHA256 != first.SHA256 || got.Size != first.Size {
		t.Errorf("Add returned %+v, want the catalogued entry %+v", got, first)
	}
	if b, _ := os.ReadFile(diskPath(vault, "d/f.bin")); string(b) != body {
		t.Fatalf("storage/d/f.bin = %q: Add did not materialize the absent file (F-7: a PC holding the right bytes cannot repair itself)", b)
	}
	if rep, _ := st.Status(); rep.Entries[0].State != storage.StateVerified {
		t.Errorf("state after Add = %v, want verified", rep.Entries[0].State)
	}
	if rows := readLedger(t, vault); rows["d/f.bin"] != first.SHA256 {
		t.Errorf("ledger = %v, want the row set", rows)
	}

	// A file that is there is left alone: the same Add is a no-op.
	before, err := os.Stat(diskPath(vault, "d/f.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(src, "d/f.bin", "", false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(diskPath(vault, "d/f.bin"))
	if !os.SameFile(before, after) {
		t.Error("Add rewrote a file that was already verified")
	}

	// And so is a file that is there but is not the catalog's: healing an
	// absent file is not licence to replace somebody's bytes.
	writeDisk(t, vault, "d/f.bin", "somebody else's bytes")
	if _, err := st.Add(src, "d/f.bin", "", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(diskPath(vault, "d/f.bin")); string(b) != "somebody else's bytes" {
		t.Errorf("Add overwrote a corrupt file: %q", b)
	}
}

func TestStorageLockSerializesWriters(t *testing.T) {
	t.Run("concurrent writers lose nothing", func(t *testing.T) {
		vault := t.TempDir()
		const n = 24
		var wg sync.WaitGroup
		errs := make(chan error, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				st, err := storage.Open(vault) // its own handle, as every lw process has
				if err != nil {
					errs <- err
					return
				}
				src := createTestFile(t, t.TempDir(), "src.bin", "payload number "+strconv.Itoa(i))
				<-start
				if _, err := st.Add(src, "f"+strconv.Itoa(i)+".bin", "", false); err != nil {
					errs <- err
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("Add: %v", err)
		}
		st, _ := storage.Open(vault)
		entries, err := st.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != n {
			t.Errorf("the catalog lists %d entries after %d concurrent Adds (a lost update)", len(entries), n)
		}
		if rows := readLedger(t, vault); len(rows) != n {
			t.Errorf("the ledger has %d rows after %d concurrent Adds (a lost update)", len(rows), n)
		}
	})

	t.Run("a held lock stops a writer until it is released", func(t *testing.T) {
		vault, st := newStore(t)
		addFile(t, st, "seed.bin", "seed") // creates .llmwiki/tmp
		lockFile := filepath.Join(vault, ".llmwiki", "tmp", "storage.lock")
		f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			src := createTestFile(t, t.TempDir(), "src.bin", "written under contention")
			_, err := st.Add(src, "late.bin", "", false)
			done <- err
		}()
		select {
		case err := <-done:
			t.Fatalf("Add finished (%v) while another process held %s: ledger and catalog writes are not serialized (F-2)", err, lockFile)
		case <-time.After(300 * time.Millisecond):
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Add after the lock was released: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Add did not finish after the lock was released")
		}
	})
}

func TestReconcileSkippedDuringRebase(t *testing.T) {
	for _, marker := range []string{".git/rebase-merge", ".git/rebase-apply", ".git/lw-rebase"} {
		t.Run(marker, func(t *testing.T) {
			vault, st, _ := storedAndRemoved(t, "f.bin", "a local-only entry, briefly absent from the catalog")
			if err := os.MkdirAll(filepath.Join(vault, filepath.FromSlash(marker)), 0o755); err != nil {
				t.Fatal(err)
			}
			res, err := st.Reconcile(t.Context(), holdsAll)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if !res.Skipped || len(res.Removed) != 0 || len(res.Kept) != 0 || len(res.NoCopy) != 0 {
				t.Errorf("Reconcile during a rebase = %+v, want Skipped and nothing else (F-2: the work tree is in flux, an entry the rebase will bring back looks removed)", res)
			}
			if !exists(diskPath(vault, "f.bin")) {
				t.Error("f.bin was deleted during a rebase")
			}
			if rows := readLedger(t, vault); len(rows) != 1 {
				t.Errorf("ledger = %v, want the row kept", rows)
			}

			if err := os.RemoveAll(filepath.Join(vault, filepath.FromSlash(marker))); err != nil {
				t.Fatal(err)
			}
			res, err = st.Reconcile(t.Context(), holdsAll)
			if err != nil || res.Skipped || !reflect.DeepEqual(res.Removed, []string{"f.bin"}) {
				t.Errorf("Reconcile after the rebase = %+v, %v; want f.bin removed", res, err)
			}
		})
	}
}

// A rebase that begins while the remote is being asked is the same hazard seen
// from the settle step: the catalog is half-replayed when it is read again.
func TestReconcileSkippedWhenARebaseBeginsDuringTheCopy(t *testing.T) {
	vault, st, _ := storedAndRemoved(t, "f.bin", "bytes lw put here")
	remote := newFakeRemote()
	remote.afterEnsure = func() {
		if err := os.MkdirAll(filepath.Join(vault, ".git", "rebase-merge"), 0o755); err != nil {
			t.Error(err)
		}
	}
	res, err := st.Reconcile(t.Context(), remote.Ensure)
	if err != nil {
		t.Fatal(err)
	}
	remote.afterEnsure = nil
	if !res.Skipped || len(res.Removed) != 0 || !exists(diskPath(vault, "f.bin")) {
		t.Errorf("Reconcile = %+v, f.bin present: %v; want Skipped and the file kept", res, exists(diskPath(vault, "f.bin")))
	}
	if rows := readLedger(t, vault); len(rows) != 1 {
		t.Errorf("ledger = %v, want the row kept for the next call", rows)
	}
	os.RemoveAll(filepath.Join(vault, ".git", "rebase-merge"))
	callsBefore := remote.calls
	// FIX2 Y-3 amendment (was neverEnsure and "no network"): what the first call
	// learned is a note, so the call after the rebase asks again - and finds the copy.
	res, err = st.Reconcile(t.Context(), remote.Ensure)
	if err != nil || !reflect.DeepEqual(res.Removed, []string{"f.bin"}) || remote.calls != callsBefore+1 {
		t.Errorf("Reconcile after the rebase = %+v, %v, ensure calls %d (was %d); want f.bin removed after one more question", res, err, remote.calls, callsBefore)
	}
}

func TestReconcileSweepsStaleTemps(t *testing.T) {
	vault, st := newStore(t)
	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now().Add(-10 * time.Minute)
	plant := func(rel, body string, at time.Time) {
		t.Helper()
		writeDisk(t, vault, rel, body)
		if err := os.Chtimes(diskPath(vault, rel), at, at); err != nil {
			t.Fatal(err)
		}
	}
	plant(".storage-tmp-111", "a killed fetch", old)
	plant("deep/er/.storage-tmp-222", "another one", old)
	plant(".storage-tmp-333", "a fetch in progress", recent)
	plant(".keep-me", "somebody's dot-file", old)
	plant("x.tmp", "somebody's temp-named file", old)

	if _, err := st.Reconcile(t.Context(), holdsAll); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, rel := range []string{".storage-tmp-111", "deep/er/.storage-tmp-222"} {
		if exists(diskPath(vault, rel)) {
			t.Errorf("%s (2 h old) survived the sweep (F-9: a killed fetch leaves a hidden file forever)", rel)
		}
	}
	if exists(diskPath(vault, "deep")) {
		t.Error("the directory the sweep emptied was not pruned")
	}
	for _, rel := range []string{".storage-tmp-333", ".keep-me", "x.tmp"} {
		if !exists(diskPath(vault, rel)) {
			t.Errorf("%s was removed: only lw's own temp files older than an hour are swept", rel)
		}
	}
}

func TestStatusBadShaIsCorrupt(t *testing.T) {
	vault, st := newStore(t)
	good := addFile(t, st, "good.bin", "a fine entry")
	bad := func(name, sha string) storage.Entry {
		return storage.Entry{Name: name, SHA256: sha, Size: 3, Added: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)}
	}
	entries := []storage.Entry{
		good,
		bad("short.bin", "abc"),
		bad("upper.bin", strings.ToUpper(good.SHA256)),
		bad("long.bin", good.SHA256+"0"),
		bad("empty.bin", ""),
		bad("nonhex.bin", strings.Repeat("g", 64)),
	}
	writeCatalog(t, vault, entries...)
	writeDisk(t, vault, "short.bin", "xyz")

	for name, status := range map[string]func() (storage.Report, error){"Status": st.Status, "StatusQuick": st.StatusQuick} {
		rep, err := status()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		states := map[string]storage.EntryState{}
		for _, es := range rep.Entries {
			states[es.Entry.Name] = es.State
		}
		if states["good.bin"] != storage.StateVerified {
			t.Errorf("%s: good.bin = %v, want verified (one bad record does not spoil the rest)", name, states["good.bin"])
		}
		for _, n := range []string{"short.bin", "upper.bin", "long.bin", "empty.bin", "nonhex.bin"} {
			if states[n] != storage.StateCorrupt {
				t.Errorf("%s: %s = %v, want corrupt", name, n, states[n])
			}
		}
	}

	ob, err := st.Outbox()
	if err != nil {
		t.Fatalf("Outbox: %v", err)
	}
	for _, b := range ob.Blobs {
		if b.Name != "good.bin" {
			t.Errorf("Outbox lists %s: a record with a bad sha256 must never reach the remote's batch question", b.Name)
		}
	}
	es, err := st.Verify("short.bin")
	if err != nil || es.State != storage.StateCorrupt {
		t.Errorf("Verify(short.bin) = %+v, %v; want corrupt", es, err)
	}
	if err := st.Materialize(t.Context(), "short.bin", strings.NewReader("abc"), holdsAll); err == nil {
		t.Error("Materialize accepted an entry with a bad sha256")
	}
	if _, err := st.Reconcile(t.Context(), holdsAll); err != nil {
		t.Errorf("Reconcile: %v", err)
	}
	if b, _ := os.ReadFile(diskPath(vault, "short.bin")); string(b) != "xyz" {
		t.Errorf("a file behind a bad record was touched: %q", b)
	}
}

func TestConfirmIsMonotonicAndRemoteScoped(t *testing.T) {
	vault, st := newStore(t)
	a := addFile(t, st, "a.bin", "alpha")
	b := addFile(t, st, "b.bin", "bravo")

	open := func(scope string) *storage.Storage {
		t.Helper()
		h, err := storage.Open(vault)
		if err != nil {
			t.Fatal(err)
		}
		h.BindRemote(scope)
		return h
	}
	names := func(ob storage.Outbox) []string {
		var out []string
		for _, bl := range ob.Blobs {
			out = append(out, bl.Name)
		}
		return out
	}

	hA := open("remote-A")
	ob, err := hA.Outbox()
	if err != nil || !reflect.DeepEqual(names(ob), []string{"a.bin", "b.bin"}) {
		t.Fatalf("Outbox before any confirmation = %v, %v", names(ob), err)
	}
	if err := hA.Confirm(a.SHA256); err != nil {
		t.Fatal(err)
	}
	if led := readRawLedger(t, vault); led.Scope != "remote-A" || !led.Confirmed[a.SHA256] || led.Confirmed[b.SHA256] {
		t.Fatalf("ledger = %+v, want a.bin confirmed for remote-A", led)
	}
	if ob, _ := open("remote-A").Outbox(); !reflect.DeepEqual(names(ob), []string{"b.bin"}) {
		t.Errorf("Outbox for the same remote = %v, want only b.bin", names(ob))
	}

	// A confirmation is a statement about one remote. Another remote has none.
	if ob, _ := open("remote-B").Outbox(); !reflect.DeepEqual(names(ob), []string{"a.bin", "b.bin"}) {
		t.Errorf("Outbox for another remote = %v, want both: remote-B was never asked (a moved vault must upload again, not delete its only copy)", names(ob))
	}
	// Reading under another scope changed nothing; writing under it starts over.
	if led := readRawLedger(t, vault); !led.Confirmed[a.SHA256] {
		t.Error("a read under another scope erased the confirmations")
	}
	if err := open("remote-B").Confirm(b.SHA256); err != nil {
		t.Fatal(err)
	}
	if led := readRawLedger(t, vault); led.Scope != "remote-B" || led.Confirmed[a.SHA256] || !led.Confirmed[b.SHA256] {
		t.Errorf("ledger after confirming under remote-B = %+v, want only b.bin confirmed", led)
	}

	// Monotonic: removing the entry does not un-confirm the object.
	if err := open("remote-B").Confirm(a.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := st.Remove("a.bin"); err != nil {
		t.Fatal(err)
	}
	if led := readRawLedger(t, vault); !led.Confirmed[a.SHA256] {
		t.Error("Remove dropped a confirmation: objects are never deleted from the remote, so the fact stays true")
	}
	// No scope bound (the verbs that never touch a remote) leave the ledger's scope alone.
	if _, err := st.Add(createTestFile(t, t.TempDir(), "c.bin", "charlie"), "c.bin", "", false); err != nil {
		t.Fatal(err)
	}
	if led := readRawLedger(t, vault); led.Scope != "remote-B" || !led.Confirmed[b.SHA256] {
		t.Errorf("an Add with no remote bound rewrote the scope: %+v", led)
	}
}

func TestOutbox(t *testing.T) {
	vault, st := newStore(t)
	v := addFile(t, st, "v.bin", "verified, never uploaded")
	dup := addFile(t, st, "dup.bin", "verified, never uploaded") // the same bytes under another name
	c := addFile(t, st, "c.bin", "verified and confirmed")
	addFile(t, st, "p.bin", "absent")
	addFile(t, st, "x.bin", "to be damaged")
	addFile(t, st, "s.bin", "to go stale")
	if err := st.Confirm(c.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(diskPath(vault, "p.bin")); err != nil {
		t.Fatal(err)
	}
	writeDisk(t, vault, "x.bin", "changed by hand!")
	list, _ := st.List()
	var cat []storage.Entry
	for _, e := range list {
		if e.Name == "s.bin" {
			e = entryFor("s.bin", "the replacement from another PC")
		}
		cat = append(cat, e)
	}
	writeCatalog(t, vault, cat...)

	ob, err := st.Outbox()
	if err != nil {
		t.Fatalf("Outbox: %v", err)
	}
	if len(ob.Blobs) != 1 || ob.Blobs[0].SHA256 != v.SHA256 || ob.Blobs[0].Size != v.Size {
		t.Fatalf("Blobs = %+v, want the one object v.bin and dup.bin share, once", ob.Blobs)
	}
	if dup.SHA256 != v.SHA256 {
		t.Fatal("setup: dup.bin and v.bin differ")
	}
	if ob.Blobs[0].Name != "dup.bin" || ob.Blobs[0].Path != diskPath(vault, "dup.bin") {
		t.Errorf("blob = %+v, want the first name by sort order and its absolute path %s", ob.Blobs[0], diskPath(vault, "dup.bin"))
	}
	if len(ob.Unready) != 1 || ob.Unready[0].Name != "x.bin" || !strings.Contains(ob.Unready[0].Why, "x.bin") {
		t.Errorf("Unready = %+v, want only x.bin (a file here that is not the catalog's: pending and stale entries are not this PC's to upload)", ob.Unready)
	}
}

func TestStatusQuickTrustsStatAndLedger(t *testing.T) {
	vault, st := newStore(t)
	addFile(t, st, "a.bin", "alpha payload")
	addFile(t, st, "b.bin", "bravo payload")
	addFile(t, st, "c.bin", "charlie payload")

	h0 := storage.HashCount()
	rep, err := st.StatusQuick()
	if err != nil {
		t.Fatal(err)
	}
	for _, es := range rep.Entries {
		if es.State != storage.StateVerified {
			t.Errorf("%s = %v, want verified", es.Entry.Name, es.State)
		}
	}
	if d := storage.HashCount() - h0; d != 0 {
		t.Errorf("StatusQuick hashed %d file(s): a file the ledger put here, still the catalog's size, is trusted (F-3: 1 GiB cost 1.9 s on every lw sync)", d)
	}
	if _, err := st.Outbox(); err != nil {
		t.Fatal(err)
	}
	if d := storage.HashCount() - h0; d != 0 {
		t.Errorf("Outbox hashed %d file(s)", d)
	}

	h1 := storage.HashCount()
	if _, err := st.Status(); err != nil {
		t.Fatal(err)
	}
	if d := storage.HashCount() - h1; d != 3 {
		t.Errorf("Status hashed %d file(s), want 3: the full check is the one that reads everything", d)
	}

	// The trade-off, written down: an in-place edit that keeps the size is not
	// seen by the stat-first path, and is by the full one (doctor, list -v).
	writeDisk(t, vault, "a.bin", "ALPHA PAYLOAD")
	rep, _ = st.StatusQuick()
	if rep.Entries[0].State != storage.StateVerified {
		t.Errorf("StatusQuick of a same-size edit = %v, want the documented trust (verified)", rep.Entries[0].State)
	}
	rep, _ = st.Status()
	if rep.Entries[0].State != storage.StateCorrupt {
		t.Errorf("Status of a same-size edit = %v, want corrupt", rep.Entries[0].State)
	}

	// A different size is read, because only the bytes say whether it is stale or damaged.
	h2 := storage.HashCount()
	writeDisk(t, vault, "b.bin", "bravo payload, longer")
	rep, _ = st.StatusQuick()
	if rep.Entries[1].State != storage.StateCorrupt || storage.HashCount()-h2 != 1 {
		t.Errorf("StatusQuick of a resized file = %v after %d hash(es), want corrupt after 1", rep.Entries[1].State, storage.HashCount()-h2)
	}

	// An absent file is pending without a read.
	os.Remove(diskPath(vault, "c.bin"))
	h3 := storage.HashCount()
	rep, _ = st.StatusQuick()
	if rep.Entries[2].State != storage.StatePending || storage.HashCount()-h3 != 1 {
		t.Errorf("StatusQuick of an absent file = %v after %d hash(es), want pending (b.bin is the 1)", rep.Entries[2].State, storage.HashCount()-h3)
	}
}

func TestVerify(t *testing.T) {
	vault, st := newStore(t)
	ok := addFile(t, st, "ok.bin", "fine")
	addFile(t, st, "old.bin", "old version")
	addFile(t, st, "bad.bin", "good bytes")
	writeCatalog(t, vault, ok, entryFor("old.bin", "a newer, longer version"), entryFor("bad.bin", "good bytes"), entryFor("gone.bin", "never here"))
	writeDisk(t, vault, "bad.bin", "evil bytes")

	cases := []struct {
		name  string
		state storage.EntryState
		stale bool
		disk  string
	}{
		{"ok.bin", storage.StateVerified, false, fileSHA256("fine")},
		{"old.bin", storage.StatePending, true, fileSHA256("old version")},
		{"bad.bin", storage.StateCorrupt, false, fileSHA256("evil bytes")},
		{"gone.bin", storage.StatePending, false, ""},
	}
	for _, c := range cases {
		es, err := st.Verify(c.name)
		if err != nil {
			t.Errorf("Verify(%s): %v", c.name, err)
			continue
		}
		if es.State != c.state || es.Stale != c.stale || es.DiskSHA != c.disk {
			t.Errorf("Verify(%s) = state %v stale %v disk %q; want %v %v %q", c.name, es.State, es.Stale, es.DiskSHA, c.state, c.stale, c.disk)
		}
	}
	if _, err := st.Verify("nope.bin"); err == nil || !strings.Contains(err.Error(), "nope.bin") {
		t.Errorf("Verify of an unknown name = %v, want an error naming it", err)
	}
	if _, err := st.Verify("../escape"); err == nil {
		t.Error("Verify accepted a name that escapes storage/")
	}
}

func TestStatusDamagedFlag(t *testing.T) {
	vault, st := newStore(t)
	gone := addFile(t, st, "gone.bin", "pending and damaged remotely")
	fine := addFile(t, st, "fine.bin", "verified")
	if err := os.Remove(diskPath(vault, "gone.bin")); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDamaged(gone.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDamaged(fine.SHA256); err != nil {
		t.Fatal(err)
	}
	rep, err := st.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, es := range rep.Entries {
		want := es.Entry.Name == "gone.bin"
		if es.Damaged != want {
			t.Errorf("%s: Damaged = %v, want %v (only a pending entry can be waiting on a damaged object)", es.Entry.Name, es.Damaged, want)
		}
	}
	if err := st.Confirm(gone.SHA256); err != nil {
		t.Fatal(err)
	}
	if led := readRawLedger(t, vault); led.Damaged[gone.SHA256] {
		t.Error("a confirmed object is still marked damaged: Confirm follows an upload that replaced it")
	}
}

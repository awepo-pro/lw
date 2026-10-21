package storage_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/awepo-pro/lw/internal/storage"
)

// 066 S2 tests: the per-PC ledger, the four states, Reconcile and Materialize.
// They stand in for the other PC by rewriting .llmwiki/storage.json (what a
// `git pull` of the catalog does) — the catalog is the only thing two PCs share.

// ledgerFile is where a PC records the bytes lw itself put under storage/.
func ledgerFile(vault string) string {
	return filepath.Join(vault, ".llmwiki", "tmp", "storage-local.json")
}

// readLedger returns the ledger's name -> sha256 rows, nil when there is no file.
func readLedger(t *testing.T, vault string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(ledgerFile(vault))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var l struct {
		Version      int               `json:"version"`
		Materialized map[string]string `json:"materialized"`
	}
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatalf("ledger is not JSON: %v\n%s", err, data)
	}
	if l.Version != 1 {
		t.Fatalf("ledger version = %d, want 1", l.Version)
	}
	return l.Materialized
}

// writeCatalog replaces the catalog as a pull of another PC's commit would.
func writeCatalog(t *testing.T, vault string, entries ...storage.Entry) {
	t.Helper()
	if entries == nil {
		entries = []storage.Entry{}
	}
	data, err := json.MarshalIndent(storage.Manifest{Version: 1, Entries: entries}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(vault, ".llmwiki", "storage.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// entryFor is the catalog row another PC would have written for content.
func entryFor(name, content string) storage.Entry {
	return storage.Entry{
		Name:   name,
		SHA256: fileSHA256(content),
		Size:   int64(len(content)),
		Added:  time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
	}
}

// newStore opens a Storage on a fresh vault directory.
func newStore(t *testing.T) (string, *storage.Storage) {
	t.Helper()
	vault := t.TempDir()
	st, err := storage.Open(vault)
	if err != nil {
		t.Fatal(err)
	}
	return vault, st
}

// addFile stores content under name through the public Add.
func addFile(t *testing.T, st *storage.Storage, name, content string) storage.Entry {
	t.Helper()
	src := createTestFile(t, t.TempDir(), "src.bin", content)
	e, err := st.Add(src, name, "", false)
	if err != nil {
		t.Fatalf("Add %s: %v", name, err)
	}
	return e
}

func diskPath(vault, name string) string {
	return filepath.Join(vault, "storage", filepath.FromSlash(name))
}

func writeDisk(t *testing.T, vault, name, content string) {
	t.Helper()
	p := diskPath(vault, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// leftovers lists every temp file a write left behind under storage/ and
// .llmwiki/tmp (the storage and ledger temp names both contain "tmp"; the
// ledger itself, storage-local.json, does not).
func leftovers(t *testing.T, vault string) []string {
	t.Helper()
	var out []string
	for _, root := range []string{filepath.Join(vault, "storage"), filepath.Join(vault, ".llmwiki", "tmp")} {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.Contains(d.Name(), "tmp") {
				out = append(out, p)
			}
			return nil
		})
	}
	return out
}

func requireNotRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not bind")
	}
}

func TestEntryStateString(t *testing.T) {
	cases := []struct {
		s    storage.EntryState
		want string
	}{
		{storage.StateVerified, "verified"},
		{storage.StatePending, "pending"},
		{storage.StateCorrupt, "corrupt"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("EntryState(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

// B6 (catalog before payload) and B2 (same-size replace): a PC must tell
// "the bytes have not arrived" from "the bytes are damaged".
func TestStatusStates(t *testing.T) {
	vault, st := newStore(t)

	addFile(t, st, "ok.bin", "ok bytes")
	addFile(t, st, "gone.bin", "gone bytes")
	addFile(t, st, "old.bin", "old version")
	addFile(t, st, "bad.bin", "good bytes")

	// gone.bin: the catalog knows it, this PC never received (or lost) the file.
	if err := os.Remove(diskPath(vault, "gone.bin")); err != nil {
		t.Fatal(err)
	}
	// old.bin: another PC replaced it; the pull moved the catalog on, the bytes
	// on disk are the ones lw put here earlier.
	list, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	var cat []storage.Entry
	for _, e := range list {
		if e.Name == "old.bin" {
			e = entryFor("old.bin", "a newer, longer version")
		}
		cat = append(cat, e)
	}
	writeCatalog(t, vault, cat...)
	// bad.bin: same size, different bytes, written by something other than lw.
	writeDisk(t, vault, "bad.bin", "evil bytes")
	// Orphans (and the files an orphan scan must not report).
	writeDisk(t, vault, "stray.bin", "no catalog row")
	writeDisk(t, vault, "deep/er/orphan.bin", "nested orphan")
	writeDisk(t, vault, ".hidden", "dot-file")
	writeDisk(t, vault, "half.tmp", "temp name")
	writeDisk(t, vault, "d/.dotfile", "nested dot-file")
	writeDisk(t, vault, "d/x.tmp", "nested temp name")
	writeDisk(t, vault, ".git/config", "dot directory")

	rep, err := st.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	type row struct {
		name  string
		state storage.EntryState
		stale bool
	}
	var got []row
	for _, es := range rep.Entries {
		got = append(got, row{es.Entry.Name, es.State, es.Stale})
	}
	want := []row{
		{"bad.bin", storage.StateCorrupt, false},
		{"gone.bin", storage.StatePending, false},
		{"ok.bin", storage.StateVerified, false},
		{"old.bin", storage.StatePending, true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Status entries = %+v\nwant %+v", got, want)
	}
	wantOrphans := []string{"deep/er/orphan.bin", "stray.bin"}
	if !reflect.DeepEqual(rep.Orphans, wantOrphans) {
		t.Errorf("Status orphans = %q, want %q", rep.Orphans, wantOrphans)
	}

	// Status only reads: the ledger and the disk are as they were.
	if rows := readLedger(t, vault); len(rows) != 4 {
		t.Errorf("Status changed the ledger: %v", rows)
	}

	// The upgrade path (v2.49.0 had no ledger): a verified file needs no row, but
	// a replaced file cannot be told from a damaged one, so it is corrupt — never
	// overwritten on a guess.
	if err := os.Remove(ledgerFile(vault)); err != nil {
		t.Fatal(err)
	}
	rep, err = st.Status()
	if err != nil {
		t.Fatalf("Status without a ledger: %v", err)
	}
	got = nil
	for _, es := range rep.Entries {
		got = append(got, row{es.Entry.Name, es.State, es.Stale})
	}
	want = []row{
		{"bad.bin", storage.StateCorrupt, false},
		{"gone.bin", storage.StatePending, false},
		{"ok.bin", storage.StateVerified, false},
		{"old.bin", storage.StateCorrupt, false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Status without a ledger = %+v\nwant %+v", got, want)
	}
}

func TestStatusEmptyVault(t *testing.T) {
	_, st := newStore(t)
	rep, err := st.Status()
	if err != nil {
		t.Fatalf("Status on a fresh vault: %v", err)
	}
	if len(rep.Entries) != 0 || len(rep.Orphans) != 0 {
		t.Errorf("fresh vault report = %+v, want empty", rep)
	}
}

// B1: a removal made on another PC reaches this one through the catalog, and
// only bytes lw itself put there are deleted.
func TestReconcileDeletesOnlyLedgerBytes(t *testing.T) {
	vault, st := newStore(t)

	addFile(t, st, "keep.bin", "keep me")
	addFile(t, st, "d/removed.bin", "removed elsewhere")
	addFile(t, st, "changed.bin", "as lw wrote it")
	addFile(t, st, "vanished.bin", "already gone")
	writeDisk(t, vault, "user.txt", "never in the catalog or the ledger")

	// The pull: another PC removed three entries.
	writeCatalog(t, vault, entryFor("keep.bin", "keep me"))
	// This PC edited one of them in the meantime and lost another.
	writeDisk(t, vault, "changed.bin", "edited by hand")
	if err := os.Remove(diskPath(vault, "vanished.bin")); err != nil {
		t.Fatal(err)
	}

	res, err := st.Reconcile(t.Context(), holdsAll)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := []string{"d/removed.bin"}; !reflect.DeepEqual(res.Removed, want) {
		t.Errorf("Removed = %q, want %q", res.Removed, want)
	}
	if want := []string{"changed.bin"}; !reflect.DeepEqual(res.Kept, want) {
		t.Errorf("Kept = %q, want %q", res.Kept, want)
	}
	if len(res.Adopted) != 0 {
		t.Errorf("Adopted = %q, want none", res.Adopted)
	}

	if exists(diskPath(vault, "d/removed.bin")) {
		t.Error("d/removed.bin (lw's own bytes) was not deleted")
	}
	if exists(diskPath(vault, "d")) {
		t.Error("the emptied parent directory storage/d was not pruned")
	}
	if !exists(filepath.Join(vault, "storage")) {
		t.Error("storage/ itself must stay")
	}
	if b, _ := os.ReadFile(diskPath(vault, "changed.bin")); string(b) != "edited by hand" {
		t.Errorf("a locally changed file was destroyed: %q", b)
	}
	if b, _ := os.ReadFile(diskPath(vault, "user.txt")); string(b) != "never in the catalog or the ledger" {
		t.Errorf("a file lw never put there was touched: %q", b)
	}
	if b, _ := os.ReadFile(diskPath(vault, "keep.bin")); string(b) != "keep me" {
		t.Errorf("a catalogued file was touched: %q", b)
	}
	if rows := readLedger(t, vault); !reflect.DeepEqual(rows, map[string]string{"keep.bin": fileSHA256("keep me")}) {
		t.Errorf("ledger after Reconcile = %v, want only keep.bin", rows)
	}

	// The kept file is now an orphan, the thing doctor reports.
	rep, err := st.Status()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"changed.bin", "user.txt"}; !reflect.DeepEqual(rep.Orphans, want) {
		t.Errorf("orphans after Reconcile = %q, want %q", rep.Orphans, want)
	}

	// Idempotent: the rows are gone, so a second pass has nothing to decide.
	res, err = st.Reconcile(t.Context(), holdsAll)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(res.Removed)+len(res.Kept)+len(res.Adopted) != 0 {
		t.Errorf("second Reconcile = %+v, want nothing", res)
	}
}

// The ledger is a file on disk; a name in it is not trusted to stay inside storage/.
func TestReconcileIgnoresHostileLedgerNames(t *testing.T) {
	vault, st := newStore(t)
	writeCatalog(t, vault) // a catalog with no entries: every ledger row is a removal
	outside := filepath.Join(vault, "outside.txt")
	if err := os.WriteFile(outside, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ledgerFile(vault)), 0o755); err != nil {
		t.Fatal(err)
	}
	led := `{"version":1,"materialized":{"../outside.txt":"` + fileSHA256("precious") + `","/etc/hostname":"` + fileSHA256("x") + `"}}`
	if err := os.WriteFile(ledgerFile(vault), []byte(led), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := st.Reconcile(t.Context(), holdsAll)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed = %q, want none", res.Removed)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "precious" {
		t.Errorf("a ledger row escaped storage/ and deleted %s: %q, %v", outside, b, err)
	}
	if rows := readLedger(t, vault); len(rows) != 0 {
		t.Errorf("hostile rows were kept: %v", rows)
	}
}

// A catalog file that is gone is not a catalog that lists nothing: Reconcile
// must not read it as "every entry was removed elsewhere".
func TestReconcileRefusesMissingCatalog(t *testing.T) {
	vault, st := newStore(t)
	addFile(t, st, "a.bin", "alpha")
	addFile(t, st, "b.bin", "bravo")
	if err := os.Remove(filepath.Join(vault, ".llmwiki", "storage.json")); err != nil {
		t.Fatal(err)
	}
	res, err := st.Reconcile(t.Context(), holdsAll)
	if err == nil {
		t.Fatal("Reconcile deleted against a missing catalog")
	}
	if !strings.Contains(err.Error(), "storage.json") {
		t.Errorf("error does not name the catalog: %v", err)
	}
	if len(res.Removed) != 0 || !exists(diskPath(vault, "a.bin")) || !exists(diskPath(vault, "b.bin")) {
		t.Errorf("files were deleted: %+v", res)
	}
	if rows := readLedger(t, vault); len(rows) != 2 {
		t.Errorf("ledger changed: %v", rows)
	}
	// An emptied catalog (the file exists, no entries) is a real removal.
	writeCatalog(t, vault)
	res, err = st.Reconcile(t.Context(), holdsAll)
	if err != nil || !reflect.DeepEqual(res.Removed, []string{"a.bin", "b.bin"}) {
		t.Errorf("Reconcile against an empty catalog = %+v, %v; want both removed", res, err)
	}
}

// Upgrade from v2.49.0: files lw put there before the ledger existed are
// adopted while the catalog still lists them, so a later removal can delete them.
func TestReconcileAdoptsVerifiedWithoutLedger(t *testing.T) {
	vault, st := newStore(t)
	addFile(t, st, "a.bin", "alpha bytes")
	addFile(t, st, "b.bin", "bravo bytes")
	addFile(t, st, "c.bin", "charlie bytes")
	addFile(t, st, "d.bin", "delta bytes")

	// v2.49.0 left no ledger. c.bin is damaged; d.bin was replaced elsewhere and
	// this PC holds the new bytes already (copied by hand) under a stale row.
	if err := os.Remove(ledgerFile(vault)); err != nil {
		t.Fatal(err)
	}
	writeDisk(t, vault, "c.bin", "damaged bytes")
	rows := map[string]string{"d.bin": fileSHA256("an older delta")}
	data, _ := json.Marshal(map[string]any{"version": 1, "materialized": rows})
	if err := os.WriteFile(ledgerFile(vault), data, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := st.Reconcile(t.Context(), holdsAll)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := []string{"a.bin", "b.bin", "d.bin"}; !reflect.DeepEqual(res.Adopted, want) {
		t.Errorf("Adopted = %q, want %q (c.bin is damaged and must not be adopted)", res.Adopted, want)
	}
	if len(res.Removed)+len(res.Kept) != 0 {
		t.Errorf("Reconcile deleted or kept something: %+v", res)
	}
	want := map[string]string{
		"a.bin": fileSHA256("alpha bytes"),
		"b.bin": fileSHA256("bravo bytes"),
		"d.bin": fileSHA256("delta bytes"),
	}
	if got := readLedger(t, vault); !reflect.DeepEqual(got, want) {
		t.Errorf("ledger = %v, want %v", got, want)
	}

	// The point of adopting: the next catalog without a.bin deletes it, and keeps c.bin.
	writeCatalog(t, vault, entryFor("b.bin", "bravo bytes"), entryFor("d.bin", "delta bytes"))
	res, err = st.Reconcile(t.Context(), holdsAll)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.bin"}; !reflect.DeepEqual(res.Removed, want) {
		t.Errorf("Removed after adoption = %q, want %q", res.Removed, want)
	}
	if exists(diskPath(vault, "a.bin")) {
		t.Error("a.bin survived its removal")
	}
	if !exists(diskPath(vault, "c.bin")) {
		t.Error("c.bin (never adopted) was deleted")
	}
}

func TestMaterializeVerifiesSHAAndSize(t *testing.T) {
	const content = "exactly these bytes"
	good := entryFor("docs/paper.pdf", content)

	t.Run("wrong bytes of the right size", func(t *testing.T) {
		vault, st := newStore(t)
		writeCatalog(t, vault, good)
		wrong := strings.ToUpper(content) // same length, different sha
		err := st.Materialize(t.Context(), "docs/paper.pdf", strings.NewReader(wrong), holdsAll)
		if err == nil {
			t.Fatal("Materialize accepted bytes with the wrong sha256")
		}
		if !strings.Contains(err.Error(), good.SHA256) || !strings.Contains(err.Error(), fileSHA256(wrong)) {
			t.Errorf("error does not name both shas (catalog %s, got %s): %v", good.SHA256, fileSHA256(wrong), err)
		}
		assertNothingLeft(t, vault, "docs/paper.pdf")
	})

	t.Run("truncated", func(t *testing.T) {
		vault, st := newStore(t)
		writeCatalog(t, vault, good)
		err := st.Materialize(t.Context(), "docs/paper.pdf", strings.NewReader(content[:5]), holdsAll)
		if err == nil {
			t.Fatal("Materialize accepted a truncated stream")
		}
		if !strings.Contains(err.Error(), good.SHA256) {
			t.Errorf("error does not name the catalog sha256: %v", err)
		}
		assertNothingLeft(t, vault, "docs/paper.pdf")
	})

	t.Run("longer than the catalog says", func(t *testing.T) {
		vault, st := newStore(t)
		writeCatalog(t, vault, good)
		err := st.Materialize(t.Context(), "docs/paper.pdf", strings.NewReader(content+" and then some"), holdsAll)
		if err == nil {
			t.Fatal("Materialize accepted a stream longer than the catalog size")
		}
		assertNothingLeft(t, vault, "docs/paper.pdf")
	})

	t.Run("stream breaks half way", func(t *testing.T) {
		vault, st := newStore(t)
		writeCatalog(t, vault, good)
		r := io.MultiReader(strings.NewReader(content[:7]), iotest.ErrReader(errors.New("connection reset")))
		err := st.Materialize(t.Context(), "docs/paper.pdf", r, holdsAll)
		if err == nil || !strings.Contains(err.Error(), "connection reset") {
			t.Fatalf("Materialize error = %v, want the stream's error", err)
		}
		assertNothingLeft(t, vault, "docs/paper.pdf")
	})

	t.Run("not in the catalog", func(t *testing.T) {
		vault, st := newStore(t)
		writeCatalog(t, vault, good)
		if err := st.Materialize(t.Context(), "other.bin", strings.NewReader(content), holdsAll); err == nil {
			t.Error("Materialize accepted a name the catalog does not list")
		}
		if err := st.Materialize(t.Context(), "../escape.bin", strings.NewReader(content), holdsAll); err == nil {
			t.Error("Materialize accepted a name that escapes storage/")
		}
		if exists(diskPath(vault, "other.bin")) || exists(filepath.Join(vault, "escape.bin")) {
			t.Error("a refused Materialize left a file")
		}
	})

	t.Run("right bytes land, verified, with a ledger row", func(t *testing.T) {
		vault, st := newStore(t)
		writeCatalog(t, vault, good)
		if err := st.Materialize(t.Context(), "docs/paper.pdf", strings.NewReader(content), holdsAll); err != nil {
			t.Fatalf("Materialize: %v", err)
		}
		if b, _ := os.ReadFile(diskPath(vault, "docs/paper.pdf")); string(b) != content {
			t.Errorf("materialized bytes = %q", b)
		}
		if got := readLedger(t, vault); !reflect.DeepEqual(got, map[string]string{"docs/paper.pdf": good.SHA256}) {
			t.Errorf("ledger = %v", got)
		}
		if l := leftovers(t, vault); len(l) != 0 {
			t.Errorf("temp files left behind: %v", l)
		}
		rep, err := st.Status()
		if err != nil || len(rep.Entries) != 1 || rep.Entries[0].State != storage.StateVerified {
			t.Errorf("Status after Materialize = %+v, %v; want verified", rep, err)
		}
	})

	t.Run("replaces a stale file", func(t *testing.T) {
		vault, st := newStore(t)
		addFile(t, st, "f.bin", "version one")
		writeCatalog(t, vault, entryFor("f.bin", "version two, newer"))
		rep, _ := st.Status()
		if len(rep.Entries) != 1 || rep.Entries[0].State != storage.StatePending || !rep.Entries[0].Stale {
			t.Fatalf("setup: Status = %+v, want f.bin pending+stale", rep)
		}
		if err := st.Materialize(t.Context(), "f.bin", strings.NewReader("version two, newer"), holdsAll); err != nil {
			t.Fatalf("Materialize over a stale file: %v", err)
		}
		if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != "version two, newer" {
			t.Errorf("stale file not replaced: %q", b)
		}
		if got := readLedger(t, vault)["f.bin"]; got != fileSHA256("version two, newer") {
			t.Errorf("ledger row = %q, want the new sha", got)
		}
	})
}

// assertNothingLeft: a refused Materialize leaves no file, no ledger row, no
// temp file, and no directory it had to create for the file.
func assertNothingLeft(t *testing.T, vault, name string) {
	t.Helper()
	if exists(diskPath(vault, name)) {
		t.Errorf("storage/%s exists after a refused Materialize", name)
	}
	if rows := readLedger(t, vault); len(rows) != 0 {
		t.Errorf("ledger has rows after a refused Materialize: %v", rows)
	}
	if l := leftovers(t, vault); len(l) != 0 {
		t.Errorf("temp files left behind: %v", l)
	}
	if first, _, ok := strings.Cut(name, "/"); ok && exists(filepath.Join(vault, "storage", first)) {
		t.Errorf("directory storage/%s created for a refused Materialize was left", first)
	}
}

func TestMaterializeRefusesCorrupt(t *testing.T) {
	vault, st := newStore(t)
	const want = "the catalog's bytes"
	writeCatalog(t, vault, entryFor("f.bin", want))
	// Bytes lw never put there and the catalog does not describe.
	writeDisk(t, vault, "f.bin", "somebody's own edit")

	err := st.Materialize(t.Context(), "f.bin", strings.NewReader(want), holdsAll)
	if err == nil {
		t.Fatal("Materialize overwrote a corrupt file")
	}
	if !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("error does not say the file is corrupt: %v", err)
	}
	if b, _ := os.ReadFile(diskPath(vault, "f.bin")); string(b) != "somebody's own edit" {
		t.Errorf("the corrupt file was modified: %q", b)
	}
	if l := leftovers(t, vault); len(l) != 0 {
		t.Errorf("temp files left behind: %v", l)
	}
	if rows := readLedger(t, vault); len(rows) != 0 {
		t.Errorf("ledger = %v", rows)
	}
}

func TestAddRemoveMaintainLedger(t *testing.T) {
	vault, st := newStore(t)

	e1 := addFile(t, st, "a.bin", "first")
	e2 := addFile(t, st, "sub/b.bin", "second")
	want := map[string]string{"a.bin": e1.SHA256, "sub/b.bin": e2.SHA256}
	if got := readLedger(t, vault); !reflect.DeepEqual(got, want) {
		t.Fatalf("ledger after Add = %v, want %v", got, want)
	}

	// A replacement (Add --force) records the new bytes, not the old ones.
	src := createTestFile(t, t.TempDir(), "src.bin", "replacement!")
	e1b, err := st.Add(src, "a.bin", "", true)
	if err != nil {
		t.Fatalf("Add --force: %v", err)
	}
	want["a.bin"] = e1b.SHA256
	if got := readLedger(t, vault); !reflect.DeepEqual(got, want) {
		t.Errorf("ledger after Add --force = %v, want %v", got, want)
	}
	if e1b.SHA256 == e1.SHA256 {
		t.Fatal("setup: the replacement has the same sha")
	}

	// A refused Add (different sha, no --force) changes nothing.
	src2 := createTestFile(t, t.TempDir(), "src.bin", "yet another")
	if _, err := st.Add(src2, "a.bin", "", false); err == nil {
		t.Fatal("Add over an existing name without --force succeeded")
	}
	if got := readLedger(t, vault); !reflect.DeepEqual(got, want) {
		t.Errorf("ledger after a refused Add = %v, want %v", got, want)
	}

	if err := st.Remove("sub/b.bin"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	delete(want, "sub/b.bin")
	if got := readLedger(t, vault); !reflect.DeepEqual(got, want) {
		t.Errorf("ledger after Remove = %v, want %v", got, want)
	}

	// The ledger is per-PC state: it never lives where git would sync it.
	if rel, _ := filepath.Rel(vault, ledgerFile(vault)); filepath.ToSlash(rel) != ".llmwiki/tmp/storage-local.json" {
		t.Errorf("ledger path = %s", rel)
	}
	// And the catalog schema is still version 1.
	var m storage.Manifest
	data, _ := os.ReadFile(filepath.Join(vault, ".llmwiki", "storage.json"))
	if err := json.Unmarshal(data, &m); err != nil || m.Version != 1 {
		t.Errorf("catalog version = %d, %v; want 1", m.Version, err)
	}

	// Add then a catalog removal elsewhere: Reconcile deletes what Add put there.
	writeCatalog(t, vault)
	res, err := st.Reconcile(t.Context(), holdsAll)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Removed, []string{"a.bin"}) {
		t.Errorf("Removed = %q, want [a.bin]", res.Removed)
	}
}

func TestPath(t *testing.T) {
	vault, st := newStore(t)
	got := st.Path("sub/dir/f.bin")
	want := filepath.Join(vault, "storage", "sub", "dir", "f.bin")
	if got != want || !filepath.IsAbs(got) {
		t.Errorf("Path = %q, want absolute %q", got, want)
	}
}

// The ledger decides what lw may delete, so a half-written one must never be
// readable: it is written to a temp file and renamed, never rewritten in place.
func TestLedgerAtomicWrite(t *testing.T) {
	vault, st := newStore(t)
	addFile(t, st, "a.bin", "one")
	before, err := os.Stat(ledgerFile(vault))
	if err != nil {
		t.Fatal(err)
	}
	addFile(t, st, "b.bin", "two")
	after, err := os.Stat(ledgerFile(vault))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Error("the ledger was rewritten in place (same inode); a crash mid-write would truncate it")
	}
	if l := leftovers(t, vault); len(l) != 0 {
		t.Errorf("temp debris after a write: %v", l)
	}

	// Debris from a crashed earlier write is neither read nor in the way.
	if err := os.WriteFile(filepath.Join(vault, ".llmwiki", "tmp", ".storage-local-tmp-123"), []byte("{trunc"), 0o644); err != nil {
		t.Fatal(err)
	}
	addFile(t, st, "c.bin", "three")
	if got := readLedger(t, vault); len(got) != 3 {
		t.Errorf("ledger = %v, want 3 rows", got)
	}

	t.Run("a failed write leaves the old ledger byte-identical", func(t *testing.T) {
		requireNotRoot(t)
		old, err := os.ReadFile(ledgerFile(vault))
		if err != nil {
			t.Fatal(err)
		}
		// Something to reconcile, so Reconcile has a row to drop and must write.
		writeCatalog(t, vault, entryFor("a.bin", "one"), entryFor("b.bin", "two"))
		tmpDir := filepath.Join(vault, ".llmwiki", "tmp")
		if err := os.Chmod(tmpDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(tmpDir, 0o755) })

		if _, err := st.Reconcile(t.Context(), holdsAll); err == nil {
			t.Fatal("Reconcile reported success although the ledger could not be written")
		}
		os.Chmod(tmpDir, 0o755)
		now, err := os.ReadFile(ledgerFile(vault))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(old, now) {
			t.Errorf("a failed write changed the ledger:\nbefore %s\nafter  %s", old, now)
		}
	})

	t.Run("an unreadable ledger is an error that says how to recover", func(t *testing.T) {
		if err := os.WriteFile(ledgerFile(vault), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := st.Status()
		if err == nil {
			t.Fatal("Status ignored a corrupt ledger")
		}
		if !strings.Contains(err.Error(), "storage-local.json") {
			t.Errorf("error does not name the ledger file: %v", err)
		}
		if _, err := st.Reconcile(t.Context(), holdsAll); err == nil {
			t.Error("Reconcile ran on a corrupt ledger")
		}
		if err := os.WriteFile(ledgerFile(vault), []byte(`{"version":2,"materialized":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Status(); err == nil {
			t.Error("Status accepted a ledger from a newer lw")
		}
	})
}

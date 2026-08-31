package stage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

// snapshotLLMWiki returns a map of every regular file under vaultDir's
// .llmwiki, keyed by its path relative to .llmwiki, to the sha256 of its
// content — used to prove OpenEngine is idempotent on disk. The lock file
// (which OpenEngine must never create, but which some tests create
// directly) is excluded: it is not part of the durable layout.
func snapshotLLMWiki(t *testing.T, vaultDir string) map[string]string {
	t.Helper()

	llDir := filepath.Join(vaultDir, ".llmwiki")
	out := make(map[string]string)
	err := filepath.WalkDir(llDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == "lock" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(llDir, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", llDir, err)
	}
	return out
}

func requireDirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s exists but is not a directory", d)
		}
	}
}

// TestOpenEngineCreatesLayout is this subtask's headline test: OpenEngine
// on a fresh vault must create the full backbone §14 .llmwiki/ layout.
func TestOpenEngineCreatesLayout(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	llDir := filepath.Join(dir, ".llmwiki")
	requireDirs(t,
		llDir,
		filepath.Join(llDir, "objects"),
		filepath.Join(llDir, "changesets", "open"),
		filepath.Join(llDir, "changesets", "committed"),
		filepath.Join(llDir, "changesets", "rejected"),
		filepath.Join(llDir, "snapshots"),
	)

	journalInfo, err := os.Stat(filepath.Join(llDir, "journal.ndjson"))
	if err != nil {
		t.Fatalf("stat journal.ndjson: %v", err)
	}
	if journalInfo.Size() != 0 {
		t.Fatalf("journal.ndjson size = %d, want 0 on a fresh vault", journalInfo.Size())
	}

	if _, err := os.Stat(filepath.Join(llDir, "index.gob")); err != nil {
		t.Fatalf("stat index.gob: %v", err)
	}

	if !filepath.IsAbs(e.root) {
		t.Fatalf("Engine.root = %q, want an absolute path", e.root)
	}
	if e.root != dir {
		t.Fatalf("Engine.root = %q, want %q (CopyFixture already returns an absolute path)", e.root, dir)
	}
}

// TestOpenEngineIdempotent proves a second OpenEngine on the same vault
// changes nothing on disk (backbone §5.4 Contract).
func TestOpenEngineIdempotent(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	e1, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("first OpenEngine: %v", err)
	}
	if err := e1.Close(); err != nil {
		t.Fatalf("close first engine: %v", err)
	}

	before := snapshotLLMWiki(t, dir)

	e2, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("second OpenEngine: %v", err)
	}
	defer e2.Close()

	after := snapshotLLMWiki(t, dir)

	if len(before) != len(after) {
		t.Fatalf("file count under .llmwiki changed: %d before second OpenEngine, %d after", len(before), len(after))
	}
	for path, sum := range before {
		got, ok := after[path]
		if !ok {
			t.Fatalf("%s existed before the second OpenEngine but not after", path)
		}
		if got != sum {
			t.Fatalf("%s content changed by a second OpenEngine (idempotence violated)", path)
		}
	}
}

// TestOpenEngineConcurrent is probe (b): two concurrent OpenEngine calls on
// one vault must both succeed and leave exactly one layout, under -race.
func TestOpenEngineConcurrent(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	const n = 8
	var wg sync.WaitGroup
	engines := make([]*Engine, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			engines[i], errs[i] = OpenEngine(dir)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("OpenEngine goroutine %d: %v", i, err)
		}
	}
	for i := range engines {
		defer engines[i].Close()
	}

	llDir := filepath.Join(dir, ".llmwiki")
	requireDirs(t,
		llDir,
		filepath.Join(llDir, "objects"),
		filepath.Join(llDir, "changesets", "open"),
		filepath.Join(llDir, "changesets", "committed"),
		filepath.Join(llDir, "changesets", "rejected"),
		filepath.Join(llDir, "snapshots"),
	)
	for _, name := range []string{"journal.ndjson", "index.gob"} {
		if _, err := os.Stat(filepath.Join(llDir, name)); err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
	}
}

// TestOpenEnginePartialLayoutCompleted is probe (d): a partially populated
// .llmwiki — the shape a crashed first run leaves behind — is completed by
// OpenEngine, not rejected.
func TestOpenEnginePartialLayoutCompleted(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	llDir := filepath.Join(dir, ".llmwiki")
	if err := os.MkdirAll(filepath.Join(llDir, "objects"), 0o755); err != nil {
		t.Fatalf("seed objects/: %v", err)
	}
	// snapshots/ is deliberately left absent.
	if err := os.WriteFile(filepath.Join(llDir, "index.gob"), nil, 0o644); err != nil {
		t.Fatalf("seed zero-length index.gob: %v", err)
	}

	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine over a partially populated .llmwiki: %v", err)
	}
	defer e.Close()

	requireDirs(t,
		filepath.Join(llDir, "objects"),
		filepath.Join(llDir, "changesets", "open"),
		filepath.Join(llDir, "changesets", "committed"),
		filepath.Join(llDir, "changesets", "rejected"),
		filepath.Join(llDir, "snapshots"),
	)

	info, err := os.Stat(filepath.Join(llDir, "index.gob"))
	if err != nil {
		t.Fatalf("stat index.gob: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("index.gob is still zero-length after OpenEngine; the disposable-cache rebuild did not run")
	}
	if e.Index() == nil || e.Index().Len() == 0 {
		t.Fatal("Engine.Index() did not come back populated from the rebuild")
	}
}

// TestEngineGettersAndDoubleClose exercises the getters and Close's
// safe-to-call-twice contract.
func TestEngineGettersAndDoubleClose(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}

	if e.Vault() == nil {
		t.Fatal("Vault() returned nil")
	}
	if e.Index() == nil {
		t.Fatal("Index() returned nil")
	}
	if e.Journal() == nil {
		t.Fatal("Journal() returned nil")
	}

	if err := e.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close (must be a no-op): %v", err)
	}
}

// TestEngineCloseReleasesLock proves Close calls unlock exactly once and
// clears the field, per the §5.4 Contract's "set it to nil so a second
// Close is a no-op".
func TestEngineCloseReleasesLock(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}

	var released int
	e.unlock = func() error {
		released++
		return nil
	}

	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if released != 1 {
		t.Fatalf("unlock called %d times, want 1", released)
	}
	if e.unlock != nil {
		t.Fatal("Close did not clear e.unlock; a second Close would call it again")
	}

	if err := e.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if released != 1 {
		t.Fatalf("unlock called %d times after second Close, want still 1", released)
	}
}

// TestOpenEngineNeverAcquiresLock proves OpenEngine does not take the lock
// (backbone §5.2: only Commit does).
func TestOpenEngineNeverAcquiresLock(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	if e.unlock != nil {
		t.Fatal("OpenEngine set e.unlock; it must never acquire the lock")
	}
	lockPath := filepath.Join(dir, ".llmwiki", "lock")
	if _, err := os.Stat(lockPath); err == nil {
		t.Fatal("OpenEngine created a lock file; only Commit may acquire the lock")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat lock: %v", err)
	}
}

// TestOpenEngineDefaultsNowAndRand proves now defaults to time.Now and rand
// to crypto/rand.Reader (backbone §5.4 Contract).
func TestOpenEngineDefaultsNowAndRand(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	if e.now == nil {
		t.Fatal("e.now is nil")
	}
	before := time.Now()
	got := e.now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("e.now() = %v, not within [%v, %v]", got, before, after)
	}

	if e.rand == nil {
		t.Fatal("e.rand is nil")
	}
	a := make([]byte, 8)
	b := make([]byte, 8)
	if _, err := io.ReadFull(e.rand, a); err != nil {
		t.Fatalf("read from e.rand: %v", err)
	}
	if _, err := io.ReadFull(e.rand, b); err != nil {
		t.Fatalf("read from e.rand: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatalf("e.rand produced identical reads %x and %x; the default should be crypto/rand.Reader", a, b)
	}
}

// TestEngineLlmwikiDir pins the D-AS seam's return value.
func TestEngineLlmwikiDir(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	want := filepath.Join(dir, ".llmwiki")
	if got := e.llmwikiDir(); got != want {
		t.Fatalf("llmwikiDir() = %q, want %q", got, want)
	}
}

// TestOpenEngineRelativeRootBecomesAbsolute proves root is stored absolute
// even when vaultRoot is given relative.
func TestOpenEngineRelativeRootBecomesAbsolute(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	rel, err := filepath.Rel(wd, dir)
	if err != nil {
		t.Skipf("fixture dir %s is not relative to cwd %s: %v", dir, wd, err)
	}

	e, err := OpenEngine(rel)
	if err != nil {
		t.Fatalf("OpenEngine(%q): %v", rel, err)
	}
	defer e.Close()

	if !filepath.IsAbs(e.root) {
		t.Fatalf("Engine.root = %q, want an absolute path", e.root)
	}
	if e.root != dir {
		t.Fatalf("Engine.root = %q, want %q", e.root, dir)
	}
}

// TestJournalHoldsNoOpenHandle proves OpenJournal does not keep the file
// open — a second, unrelated open of the same path must not fail with a
// sharing violation, and OpenJournal itself must not fail on a file another
// caller has already created (backbone §5.7, MASTER §9 D-AS).
func TestJournalHoldsNoOpenHandle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.ndjson")

	j, err := OpenJournal(path)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	if j == nil {
		t.Fatal("OpenJournal returned a nil *Journal")
	}

	// If OpenJournal kept a handle open, appending independently would still
	// work on most platforms, so the real proof is that opening it again,
	// and writing directly, succeeds without any coordination with j.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("independent open of journal path: %v", err)
	}
	if _, err := f.WriteString("{}\n"); err != nil {
		t.Fatalf("independent write to journal path: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if !strings.Contains(string(data), "{}") {
		t.Fatalf("journal content = %q, want it to contain the independently written line", data)
	}
}

func TestOpenJournalIdempotentOnExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.ndjson")

	if err := os.WriteFile(path, []byte(`{"kind":"x"}`+"\n"), 0o644); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	j, err := OpenJournal(path)
	if err != nil {
		t.Fatalf("OpenJournal over an existing file: %v", err)
	}
	if j == nil {
		t.Fatal("OpenJournal returned a nil *Journal")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if string(data) != `{"kind":"x"}`+"\n" {
		t.Fatalf("OpenJournal modified an existing journal's content: got %q", data)
	}
}

package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// writeTestFile writes content to path, which must be under a t.TempDir()
// tree (every caller here reaches path via testutil.CopyFixture) — never
// the repo's own spec/fixtures.
func writeTestFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o644)
}

// TestOpenMinimal is this subtask's headline test: opening
// spec/fixtures/minimal loads exactly 4 pages and 2 raw sources, with zero
// broken links and zero orphans.
func TestOpenMinimal(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if got := len(v.Pages()); got != 4 {
		t.Errorf("len(Pages()) = %d, want 4", got)
	}
	if got := len(v.RawSources()); got != 2 {
		t.Errorf("len(RawSources()) = %d, want 2", got)
	}
	if got := len(v.ParseErrors()); got != 0 {
		t.Errorf("len(ParseErrors()) = %d, want 0: %v", got, v.ParseErrors())
	}
	if got := len(v.Graph().Broken()); got != 0 {
		t.Errorf("len(Graph().Broken()) = %d, want 0: %v", got, v.Graph().Broken())
	}
	if got := len(v.Graph().Orphans()); got != 0 {
		t.Errorf("len(Graph().Orphans()) = %d, want 0: %v", got, v.Graph().Orphans())
	}
	if v.Schema() == nil {
		t.Fatalf("Schema() = nil")
	}
	if got := len(v.Schema().Tags); got != 12 {
		t.Errorf("len(Schema().Tags) = %d, want 12", got)
	}
}

// TestOpenDirty proves a page that fails to parse is collected in
// ParseErrors — attributed to its vault-relative path — rather than
// aborting Open, and that it contributes to neither Pages() nor graph
// edges.
func TestOpenDirty(t *testing.T) {
	dir := testutil.CopyFixture(t, "dirty")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	errs := v.ParseErrors()
	if len(errs) != 1 {
		t.Fatalf("len(ParseErrors()) = %d, want 1: %v", len(errs), errs)
	}
	if errs[0].Path != "wiki/concepts/malformed.md" {
		t.Errorf("ParseErrors()[0].Path = %q, want %q", errs[0].Path, "wiki/concepts/malformed.md")
	}
	if errs[0].Err == nil {
		t.Errorf("ParseErrors()[0].Err = nil")
	}
	if errs[0].Error() == "" {
		t.Errorf("ParseError.Error() is empty")
	}
	if !errors.Is(errs[0], errs[0].Err) {
		t.Errorf("errors.Is(ParseError, its own Err) = false; Unwrap is broken")
	}

	if _, ok := v.Page("wiki/concepts/malformed.md"); ok {
		t.Errorf("Page(malformed.md) found — an unparsable file must not be in Pages()")
	}

	// 11 wiki files under dirty/wiki/concepts, minus the one that fails to
	// parse: 10 (backbone §2.8, MASTER §9 D-W).
	if got := len(v.Pages()); got != 10 {
		t.Errorf("len(Pages()) = %d, want 10", got)
	}

	broken := v.Graph().Broken()
	if len(broken) != 1 {
		t.Fatalf("len(Broken()) = %d, want 1: %+v", len(broken), broken)
	}
	if broken[0].From != "wiki/concepts/broken-target.md" || broken[0].Raw != "nonexistent-target" {
		t.Errorf("Broken()[0] = %+v, want From=wiki/concepts/broken-target.md Raw=nonexistent-target", broken[0])
	}

	orphans := v.Graph().Orphans()
	if len(orphans) != 1 || orphans[0] != "wiki/concepts/orphan-page.md" {
		t.Errorf("Orphans() = %v, want [wiki/concepts/orphan-page.md]", orphans)
	}
}

// TestVaultReadRejectsEscape proves Read refuses an absolute path, a path
// containing "..", and a path that resolves outside the root, all with
// ErrOutsideVault.
func TestVaultReadRejectsEscape(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	cases := []string{
		"../../etc/passwd",
		"/etc/passwd",
		"wiki/../../x",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			_, err := v.Read(p)
			if !errors.Is(err, ErrOutsideVault) {
				t.Fatalf("Read(%q) error = %v, want ErrOutsideVault", p, err)
			}
		})
	}
}

// TestVaultReadAndExists proves Read/Exists work for legitimate
// vault-relative paths, including ones Open never loads as a Page (like
// SCHEMA.md).
func TestVaultReadAndExists(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if !v.Exists("SCHEMA.md") {
		t.Errorf("Exists(SCHEMA.md) = false")
	}
	if !v.Exists("wiki/concepts/kv-cache.md") {
		t.Errorf("Exists(wiki/concepts/kv-cache.md) = false")
	}
	if v.Exists("wiki/concepts/does-not-exist.md") {
		t.Errorf("Exists(does-not-exist.md) = true")
	}

	b, err := v.Read("index.md")
	if err != nil {
		t.Fatalf("Read(index.md): %v", err)
	}
	if len(b) == 0 {
		t.Errorf("Read(index.md) returned no bytes")
	}

	if _, err := v.Read("wiki/concepts/does-not-exist.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read(does-not-exist.md) error = %v, want ErrNotFound", err)
	}
}

// TestVaultRoot proves Root returns exactly what was passed to Open.
func TestVaultRoot(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if v.Root() != dir {
		t.Errorf("Root() = %q, want %q", v.Root(), dir)
	}
}

// TestVaultReload proves Reload picks up a change written to disk after
// Open, since Vault is otherwise a point-in-time snapshot.
func TestVaultReload(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := len(v.Pages()); got != 4 {
		t.Fatalf("len(Pages()) = %d, want 4 before mutation", got)
	}

	// Vault itself never writes; this test writes directly to the copied
	// fixture's temp directory to simulate an external change, which is
	// exactly the scenario Reload exists for.
	extra := filepath.Join(dir, "wiki", "concepts", "reload-check.md")
	newPage := "---\ntitle: Reload Check\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: concept\n---\n\n# Reload Check\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	if err := writeTestFile(t, extra, newPage); err != nil {
		t.Fatalf("write reload-check.md: %v", err)
	}

	if got := len(v.Pages()); got != 4 {
		t.Fatalf("len(Pages()) = %d before Reload, want unchanged 4", got)
	}

	if err := v.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := len(v.Pages()); got != 5 {
		t.Errorf("len(Pages()) = %d after Reload, want 5", got)
	}
}

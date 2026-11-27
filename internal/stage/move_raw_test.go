package stage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

const (
	moveFrom = "raw/articles/kv-cache-explained.md"
	moveTo   = "raw/papers/kv-cache-explained.md"
)

func readVaultFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// newEngineWithOriginal opens an engine over a minimal-fixture copy whose
// kv-cache-explained raw carries an original beside it, the shape a notes
// ingest leaves (x.md.orig).
func newEngineWithOriginal(t *testing.T) (*Engine, string, []byte) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	orig := []byte("the original bytes\n")
	sum := sha256.Sum256(orig)
	if err := os.WriteFile(filepath.Join(dir, "raw/articles/kv-cache-explained.md.orig"), orig, 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "raw/articles/kv-cache-explained.md")
	b, _ := os.ReadFile(p)
	s := strings.Replace(string(b), "\n---\n", "\noriginal: "+moveFrom+".orig\noriginal_sha256: "+hex.EncodeToString(sum[:])+"\n---\n", 1)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()
	return e, dir, orig
}

func TestMoveRawMovesSourceAndRewritesCitations(t *testing.T) {
	e, dir := newTestEngine(t)
	before := readVaultFile(t, dir, moveFrom)

	if _, err := e.OpenChangeset("move raw", testAuthor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Append(Op{Kind: OpMoveRaw, From: moveFrom, To: moveTo, Rationale: "misfiled"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cur, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cur.Ops) != 1 || len(cur.Ops[0].Cascade) != 2 {
		t.Fatalf("want one move_raw with 2 cascade patches (kv-cache, flash-attention), got %+v", cur.Ops)
	}
	if cur.Checks.Lint != "pass" || cur.Checks.BrokenLinks != 0 {
		t.Fatalf("checks = %+v, want lint pass and no broken links", cur.Checks)
	}

	commitID, err := e.Commit("move raw")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, moveFrom)); !os.IsNotExist(err) {
		t.Fatalf("%s still in the vault (err %v)", moveFrom, err)
	}
	if got := readVaultFile(t, dir, moveTo); got != before {
		t.Fatalf("moved raw changed:\n%s\nwant:\n%s", got, before)
	}
	if _, err := os.Stat(filepath.Join(dir, ".llmwiki", "tombstones", commitID, moveFrom)); err != nil {
		t.Fatalf("old raw not tombstoned: %v", err)
	}
	for _, p := range []string{"wiki/concepts/kv-cache.md", "wiki/concepts/flash-attention.md"} {
		s := readVaultFile(t, dir, p)
		if strings.Contains(s, moveFrom) {
			t.Errorf("%s still names %s:\n%s", p, moveFrom, s)
		}
		if !strings.Contains(s, "^["+moveTo+"]") || !strings.Contains(s, "sources: ["+moveTo+"]") {
			t.Errorf("%s does not cite %s in body and sources:\n%s", p, moveTo, s)
		}
	}
	// Dedupe follows the source to its new path.
	if _, ok := e.Vault().RawSource(moveTo); !ok {
		t.Fatalf("vault does not see %s after the commit", moveTo)
	}
}

func TestMoveRawCarriesOriginal(t *testing.T) {
	e, dir, orig := newEngineWithOriginal(t)
	if _, err := e.OpenChangeset("move raw", testAuthor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Append(Op{Kind: OpMoveRaw, From: moveFrom, To: moveTo}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cur, _ := e.Current()
	if cur.Checks.Lint != "pass" {
		t.Fatalf("checks = %+v, want lint pass (src-integrity must see the moved original)", cur.Checks)
	}
	if _, err := e.Commit("move raw"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readVaultFile(t, dir, moveTo+".orig"); got != string(orig) {
		t.Fatalf("moved original = %q, want %q", got, orig)
	}
	if _, err := os.Stat(filepath.Join(dir, moveFrom+".orig")); !os.IsNotExist(err) {
		t.Fatalf("old original still present (err %v)", err)
	}
	moved := readVaultFile(t, dir, moveTo)
	if !strings.Contains(moved, "\noriginal: "+moveTo+".orig\n") || strings.Contains(moved, moveFrom) {
		t.Fatalf("original: line not rewritten:\n%s", moved)
	}
	r, _ := e.Vault().RawSource(moveTo)
	if r == nil || r.Original != moveTo+".orig" {
		t.Fatalf("RawSource(%s) = %+v", moveTo, r)
	}
}

func TestMoveRawRefusals(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("move raw", testAuthor); err != nil {
		t.Fatal(err)
	}
	for name, op := range map[string]Op{
		"destination exists": {Kind: OpMoveRaw, From: moveFrom, To: "raw/papers/leviathan-2023.md"},
		"into assets":        {Kind: OpMoveRaw, From: moveFrom, To: "raw/assets/kv-cache-explained.md"},
		"not a raw source":   {Kind: OpMoveRaw, From: "wiki/concepts/kv-cache.md", To: moveTo},
		"missing source":     {Kind: OpMoveRaw, From: "raw/articles/nope.md", To: moveTo},
		"nested destination": {Kind: OpMoveRaw, From: moveFrom, To: "raw/papers/sub/kv-cache-explained.md"},
	} {
		if _, err := e.Append(op); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: Append err = %v, want ErrValidation", name, err)
		}
	}
}

// A page another live op already patches composes with the move's citation
// rewrite: both changes land.
func TestMoveRawComposesWithStagedPatch(t *testing.T) {
	e, dir := newTestEngine(t)
	if _, err := e.OpenChangeset("patch then move", testAuthor); err != nil {
		t.Fatal(err)
	}
	stageKVCachePatch(t, e)
	if _, err := e.Append(Op{Kind: OpMoveRaw, From: moveFrom, To: moveTo}); err != nil {
		t.Fatalf("Append move_raw over a staged patch: %v", err)
	}
	if _, err := e.Commit("patch then move"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	s := readVaultFile(t, dir, "wiki/concepts/kv-cache.md")
	if !strings.Contains(s, "reduces the memory cost") {
		t.Errorf("the staged patch was lost:\n%s", s)
	}
	if strings.Contains(s, moveFrom) || !strings.Contains(s, moveTo) {
		t.Errorf("the citation rewrite was lost:\n%s", s)
	}
}

func TestReplaceRawPathBoundaries(t *testing.T) {
	const from, to = "raw/notes/x.md", "raw/articles/x.md"
	for in, want := range map[string]string{
		"a ^[raw/notes/x.md] b":              "a ^[raw/articles/x.md] b",
		"^[raw/notes/x.md p.3]":              "^[raw/articles/x.md p.3]",
		"[l](../../raw/notes/x.md)":          "[l](../../raw/articles/x.md)",
		"^[raw/notes/x.md]^[raw/notes/x.md]": "^[raw/articles/x.md]^[raw/articles/x.md]",
		"raw/notes/x.md.orig":                "raw/notes/x.md.orig",
		"raw/notes/x.md-2":                   "raw/notes/x.md-2",
		"raw/notes/xx.md":                    "raw/notes/xx.md",
		"myraw/notes/x.md":                   "myraw/notes/x.md",
	} {
		if got := replaceRawPath(in, from, to); got != want {
			t.Errorf("replaceRawPath(%q) = %q, want %q", in, got, want)
		}
	}
}

package stage

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// TestWalkWholeTreeSkipsLedger pins 074 F2: the vault-root ledger/ directory
// holds mobile bookkeeping files (expense logs), not wiki content. Like notes/ (047)
// and storage/ (066), the whole-vault walk that seeds every projection and every
// commit's snapshot manifest must skip root-level ledger/. A nested ledger directory
// (deep/ledger/) remains ordinary content.
func TestWalkWholeTreeSkipsLedger(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"ledger/2026/10/2026-10-31.md": "- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) lunch\n",
		"notes/n.md":                   "a note\n",
		"storage/a.md":                 "# a stored file\n",
		"wiki/p.md":                    "# a page\n",
		"deep/ledger/b.md":             "# ordinary content\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tree, err := walkWholeTree(root)
	if err != nil {
		t.Fatalf("walkWholeTree: %v", err)
	}
	var got []string
	for p := range tree {
		got = append(got, p)
	}
	sort.Strings(got)

	want := []string{"deep/ledger/b.md", "wiki/p.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("walkWholeTree paths = %v, want %v", got, want)
	}
}

// TestWalkSkipSetIsNonWikiRoots pins that the whole-vault walk skips every root
// in vault.NonWikiRoots. If a fourth root is added to the set after notes/ (047),
// storage/ (066), and ledger/ (074), this ensures the walk automatically follows.
func TestWalkSkipSetIsNonWikiRoots(t *testing.T) {
	root := t.TempDir()
	for _, r := range vault.NonWikiRoots {
		p := filepath.Join(root, r, "x.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# skip me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Add an ordinary wiki file so the walk has content.
	wikiFile := filepath.Join(root, "wiki", "p.md")
	if err := os.MkdirAll(filepath.Dir(wikiFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wikiFile, []byte("# page\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tree, err := walkWholeTree(root)
	if err != nil {
		t.Fatalf("walkWholeTree: %v", err)
	}

	for _, r := range vault.NonWikiRoots {
		file := r + "/x.md"
		if _, ok := tree[file]; ok {
			t.Errorf("walkWholeTree recorded %s; every root in vault.NonWikiRoots must be skipped", file)
		}
	}
	if _, ok := tree["wiki/p.md"]; !ok {
		t.Errorf("wiki/p.md missing from walkWholeTree")
	}
}

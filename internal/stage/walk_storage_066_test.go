package stage

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// TestWalkWholeTreeSkipsStorage pins 066 R3 / B3 at its root: the vault-root
// storage/ directory is lw storage's payload area (062), not vault content, so
// the whole-vault walk that seeds every projection AND every commit's snapshot
// manifest must not enter it. A stored note.md that the walk records becomes an
// ADDED path in the next commit's delta, and `lw revert` then lists it as
// "skipped: storage/…" in a changeset about wiki pages (v2.49.0, repro.sh B3).
//
// It is the same one-root-name rule as notes/ (047: TestNotesInvisibleToHistory
// is the cmd-level twin), so the same two limits are pinned: notes/ is still
// skipped, and a directory merely CALLED storage deeper in the tree stays
// ordinary vault content.
func TestWalkWholeTreeSkipsStorage(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"storage/a.md":      "# a stored markdown file\n",
		"notes/n.md":        "a note\n",
		"wiki/p.md":         "# a page\n",
		"deep/storage/b.md": "# not the vault-root storage\n",
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

	want := []string{"deep/storage/b.md", "wiki/p.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("walkWholeTree paths = %v, want %v (storage/ at the root is outside the vault's history; a nested storage/ is content)", got, want)
	}
	if b, ok := tree["wiki/p.md"]; !ok || string(b) != "# a page\n" {
		t.Errorf("wiki/p.md = %q (present %v), want its bytes untouched", b, ok)
	}
}

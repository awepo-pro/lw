package extract

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWalkPathForms pins the root-exemption and path-shape contract: the
// root never hits the dot rule (however it is spelled), and the returned
// paths are always filepath.Join(dir, ...) form — no doubled separators
// from a trailing slash on dir, no "./" prefix when dir is ".".
func TestWalkPathForms(t *testing.T) {
	ex := Chain(NewFile())

	// A trailing separator on dir: the root is still exempt and the
	// children come back clean.
	dir := buildWalkFixtures(t)
	files, skipped, err := Walk(dir+string(filepath.Separator), ex)
	if err != nil {
		t.Fatalf("trailing slash: %v", err)
	}
	if len(files) != 4 || filepath.Join(files[0]) != filepath.Join(dir, "Z.MD") {
		t.Errorf("trailing slash files = %v, want the 4 eligible files in Join form", files)
	}
	for _, s := range skipped {
		if filepath.Clean(s.Path) == filepath.Clean(dir) {
			t.Errorf("root reported as skipped: %+v", s)
		}
	}

	// dir = ".": paths are cwd-relative Join form, and the "." root (whose
	// own name starts with a dot) is not held against the dot rule.
	cwd := t.TempDir()
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(cwd, rel), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("a.md", "# A\n")
	write(".hidden.md", "# h\n")
	if err := os.Mkdir(filepath.Join(cwd, ".hid"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(".hid", "x.md"), "# x\n")
	t.Chdir(cwd)
	files, skipped, err = Walk(".", ex)
	if err != nil {
		t.Fatalf("dot root: %v", err)
	}
	if len(files) != 1 || files[0] != "a.md" {
		t.Errorf("dot root files = %v, want [a.md]", files)
	}
	wantSkipped := []Skipped{{".hid", "hidden"}, {".hidden.md", "hidden"}}
	if len(skipped) != len(wantSkipped) {
		t.Fatalf("dot root skipped = %v, want %v", skipped, wantSkipped)
	}
	for i := range wantSkipped {
		if skipped[i] != wantSkipped[i] {
			t.Errorf("dot root skipped[%d] = %+v, want %+v", i, skipped[i], wantSkipped[i])
		}
	}

	// A hidden directory handed as the root itself is still walked: the
	// exemption is for the root, whatever its name.
	outer := t.TempDir()
	root := filepath.Join(outer, ".vault")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, skipped, err = Walk(root, ex)
	if err != nil {
		t.Fatalf("hidden root: %v", err)
	}
	if len(files) != 1 || files[0] != filepath.Join(root, "a.md") {
		t.Errorf("hidden root files = %v, want [%s]", files, filepath.Join(root, "a.md"))
	}
	if len(skipped) != 0 {
		t.Errorf("hidden root skipped = %v, want none (the root is exempt)", skipped)
	}
}

package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildWalkFixtures lays down the frozen T1 fixture tree in dir: a.md,
// b/c.markdown, b/d.txt, .hidden.md, .git/x.md, link.md → a.md, empty.md
// (0 B), img.png, Z.MD.
func buildWalkFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(rel), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("a.md", "# A\n")
	write("b/c.markdown", "# C\n")
	write("b/d.txt", "d\n")
	write(".hidden.md", "# hidden\n")
	write(".git/x.md", "# x\n")
	write("empty.md", "")
	write("img.png", "\x89PNG\r\n\x1a\n")
	write("Z.MD", "# Z\n")
	if err := os.Symlink(filepath.Join(dir, "a.md"), filepath.Join(dir, "link.md")); err != nil {
		t.Fatalf("symlink link.md: %v", err)
	}
	return dir
}

// TestWalkFixtureTree is the frozen pin: the file list, the skip list and
// the exact reason strings, for a NewFile-only chain and for the chain
// shape cmdIngest builds.
func TestWalkFixtureTree(t *testing.T) {
	dir := buildWalkFixtures(t)

	wantFiles := []string{
		filepath.Join(dir, "Z.MD"),
		filepath.Join(dir, "a.md"),
		filepath.Join(dir, "b/c.markdown"),
		filepath.Join(dir, "b/d.txt"),
	}
	wantSkipped := []Skipped{
		{filepath.Join(dir, ".git"), "hidden"},
		{filepath.Join(dir, ".hidden.md"), "hidden"},
		{filepath.Join(dir, "empty.md"), "empty"},
		{filepath.Join(dir, "img.png"), "unsupported type"},
		{filepath.Join(dir, "link.md"), "symlink"},
	}

	chains := map[string]Extractor{
		"NewFile only":    Chain(NewFile()),
		"cmdIngest shape": Chain(NewHTML(NewHTTPClient(time.Second)), NewFile()),
	}
	for name, ex := range chains {
		t.Run(name, func(t *testing.T) {
			files, skipped, err := Walk(dir, ex)
			if err != nil {
				t.Fatalf("Walk error: %v", err)
			}
			if len(files) != len(wantFiles) {
				t.Fatalf("files = %v, want %v", files, wantFiles)
			}
			for i := range wantFiles {
				if files[i] != wantFiles[i] {
					t.Errorf("files[%d] = %q, want %q", i, files[i], wantFiles[i])
				}
			}
			if len(skipped) != len(wantSkipped) {
				t.Fatalf("skipped = %v, want %v", skipped, wantSkipped)
			}
			for i := range wantSkipped {
				if skipped[i] != wantSkipped[i] {
					t.Errorf("skipped[%d] = %+v, want %+v", i, skipped[i], wantSkipped[i])
				}
			}
		})
	}
}

func TestWalkDirNotADirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(path, []byte("# a\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, _, err := Walk(path, Chain(NewFile()))
	if err == nil {
		t.Fatalf("Walk(%s) error = nil, want an error naming the path", path)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %q", err.Error(), path)
	}
}

func TestWalkMissingDir(t *testing.T) {
	_, _, err := Walk(filepath.Join(t.TempDir(), "nope"), Chain(NewFile()))
	if err == nil {
		t.Fatal("Walk of a missing directory error = nil, want an error")
	}
}

// TestWalkSymlinkedRoot pins the seam between Walk's own os.Stat
// pre-check — which follows symlinks, so a link to a directory IS a
// directory — and filepath.WalkDir, which Lstats the root and would
// otherwise descend nothing and select no files. 004 F.I1 promises that
// an argument which stats as a directory is replaced by Walk(arg, ex)'s
// files, so a symlinked source folder must walk its target, with every
// reported path in the caller's spelling of dir (filepath.Join(dir, rel)).
func TestWalkSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(real, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("a.md", "# A\n")
	write("b.txt", "b\n")
	write(".hidden.md", "# hidden\n")

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	files, skipped, err := Walk(link, Chain(NewFile()))
	if err != nil {
		t.Fatalf("Walk error: %v", err)
	}
	wantFiles := []string{filepath.Join(link, "a.md"), filepath.Join(link, "b.txt")}
	if len(files) != len(wantFiles) {
		t.Fatalf("files = %v, want %v", files, wantFiles)
	}
	for i := range wantFiles {
		if files[i] != wantFiles[i] {
			t.Errorf("files[%d] = %q, want %q", i, files[i], wantFiles[i])
		}
	}
	wantSkipped := []Skipped{{filepath.Join(link, ".hidden.md"), "hidden"}}
	if len(skipped) != len(wantSkipped) {
		t.Fatalf("skipped = %v, want %v", skipped, wantSkipped)
	}
	for i := range wantSkipped {
		if skipped[i] != wantSkipped[i] {
			t.Errorf("skipped[%d] = %+v, want %+v", i, skipped[i], wantSkipped[i])
		}
	}
}

package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestFixtureRootWalksUp exercises the unexported walk-up logic against a
// synthetic tree, so it passes whether or not the real spec/fixtures exists
// yet — a concurrent subtask is populating it in this same worktree.
func TestFixtureRootWalksUp(t *testing.T) {
	tests := []struct {
		name    string
		build   func(t *testing.T, root string) string // returns the starting dir
		wantErr bool
	}{
		{
			name: "found from a deeply nested package directory",
			build: func(t *testing.T, root string) string {
				mustMkdirAll(t, filepath.Join(root, "spec", "fixtures", "minimal"))
				start := filepath.Join(root, "internal", "pkg", "deep")
				mustMkdirAll(t, start)
				return start
			},
		},
		{
			name: "found starting at the repo root itself",
			build: func(t *testing.T, root string) string {
				mustMkdirAll(t, filepath.Join(root, "spec", "fixtures"))
				return root
			},
		},
		{
			name: "not found anywhere above start",
			build: func(t *testing.T, root string) string {
				start := filepath.Join(root, "a", "b", "c")
				mustMkdirAll(t, start)
				return start
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			start := tt.build(t, root)

			got, err := fixtureRoot(start)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("fixtureRoot(%s) = %q, nil; want an error", start, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("fixtureRoot(%s): %v", start, err)
			}
			if want := filepath.Join(root, "spec", "fixtures"); got != want {
				t.Fatalf("fixtureRoot(%s) = %q, want %q", start, got, want)
			}
		})
	}
}

// TestFixtureRoot proves the public wrapper against a synthetic repo tree,
// reached via t.Chdir so the real spec/fixtures (S0-T3's concurrent work) is
// never involved.
func TestFixtureRoot(t *testing.T) {
	root := t.TempDir()
	fixturesDir := filepath.Join(root, "spec", "fixtures")
	mustMkdirAll(t, fixturesDir)

	deep := filepath.Join(root, "internal", "pkg", "deep")
	mustMkdirAll(t, deep)

	t.Chdir(deep)

	if got := FixtureRoot(t); got != fixturesDir {
		t.Fatalf("FixtureRoot() = %q, want %q", got, fixturesDir)
	}
}

// TestCopyFixture proves the copy semantics — tree and mode preserved,
// source left untouched by mutating the copy — against a fixture the test
// builds itself, again reached via t.Chdir into a synthetic repo tree.
func TestCopyFixture(t *testing.T) {
	root := t.TempDir()
	srcFixture := filepath.Join(root, "spec", "fixtures", "sample")

	mustWriteFile(t, filepath.Join(srcFixture, "SCHEMA.md"), "# schema\n", 0o644)
	mustWriteFile(t, filepath.Join(srcFixture, "wiki", "concepts", "kv-cache.md"),
		"---\ntitle: x\n---\nbody\n", 0o600)

	deep := filepath.Join(root, "internal", "pkg", "deep")
	mustMkdirAll(t, deep)
	t.Chdir(deep)

	dst := CopyFixture(t, "sample")

	if dst == srcFixture {
		t.Fatalf("CopyFixture() returned the source path %q, want a copy", dst)
	}

	wantFiles := map[string]string{
		"SCHEMA.md":                 "# schema\n",
		"wiki/concepts/kv-cache.md": "---\ntitle: x\n---\nbody\n",
	}
	for rel, want := range wantFiles {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read copied %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("copied %s = %q, want %q", rel, got, want)
		}
	}

	// File mode is preserved.
	info, err := os.Stat(filepath.Join(dst, "wiki", "concepts", "kv-cache.md"))
	if err != nil {
		t.Fatalf("stat copied file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("copied file mode = %o, want 0600", perm)
	}

	// The copy is independent: mutating it must not touch the source.
	mutated := filepath.Join(dst, "SCHEMA.md")
	if err := os.WriteFile(mutated, []byte("mutated\n"), 0o644); err != nil {
		t.Fatalf("mutate copy: %v", err)
	}
	origSrc, err := os.ReadFile(filepath.Join(srcFixture, "SCHEMA.md"))
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if string(origSrc) != "# schema\n" {
		t.Fatalf("source was mutated: %q", origSrc)
	}
}

// TestCopyTreeRefusesSymlink exercises the failure path directly against
// the unexported helper, since CopyFixture itself would call t.Fatalf.
func TestCopyTreeRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustMkdirAll(t, src)
	mustWriteFile(t, filepath.Join(src, "a.md"), "a\n", 0o644)

	outside := filepath.Join(root, "outside.md")
	mustWriteFile(t, outside, "outside\n", 0o644)

	if err := os.Symlink(outside, filepath.Join(src, "link.md")); err != nil {
		t.Skipf("symlinks unsupported on this filesystem: %v", err)
	}

	dst := filepath.Join(root, "dst")
	if err := copyTree(src, dst); err == nil {
		t.Fatalf("copyTree() error = nil, want an error for a symlink inside the fixture")
	}
}

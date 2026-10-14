package vaultsync

// global_excludes_test.go pins 058's D7: since 058 a vault holds originals
// that no `.gitignore` of lw's would ever list — x.md.orig, y.html, z.txt —
// and *.orig / *.html are exactly what a developer's global git ignore often
// names. The managed .gitignore (Ignore) must stay the only thing that keeps
// a vault file out of a sync; a user's own excludes must not, or the original
// never reaches the other PC and its lint reports src-integrity "original …
// is missing".

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncIgnoresGlobalExcludes: whichever way the user's global ignore is
// spelled, a markdown source's x.md.orig and a page's y.html are committed,
// pushed and checked out on the second PC like any other vault file. The
// managed .gitignore still applies in the same run: the per-PC paths it
// names stay untracked (TestInitLocalBareAndPush pins the same on a plain
// vault).
func TestSyncIgnoresGlobalExcludes(t *testing.T) {
	const globalIgnore = "*.orig\n*.html\n*.txt\n"
	originals := map[string]string{
		"raw/articles/x.md":      "# X\n\nbody\n",
		"raw/articles/x.md.orig": "# X\r\n\r\nbody\r\n",
		"raw/articles/y.md":      "# Y\n\nbody\n",
		"raw/articles/y.html":    "<html><body><h1>Y</h1></body></html>\r\n",
		"raw/articles/z.md":      "# Z\n\nbody\n",
		"raw/articles/z.txt":     "# Z\n\nbody\n",
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, home string)
	}{
		{"core.excludesFile in the global config", func(t *testing.T, home string) {
			put(t, home, "global-ignore", globalIgnore)
			writeGlobalGitConfig(t, "[core]\n\texcludesFile = "+filepath.Join(home, "global-ignore")+"\n")
		}},
		{"the XDG default ~/.config/git/ignore", func(t *testing.T, home string) {
			put(t, home, ".config/git/ignore", globalIgnore)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := hermetic(t)
			ctx := t.Context()
			tc.setup(t, home)

			vault := makeVault(t)
			for rel, body := range originals {
				put(t, vault, rel, body)
			}
			remote := filepath.Join(t.TempDir(), "remote.git")
			if _, err := Init(ctx, opts(vault, remote)); err != nil {
				t.Fatalf("Init: %v", err)
			}

			tracked := func(label, dir string) {
				t.Helper()
				files := strings.Split(git(t, dir, "ls-files"), "\n")
				for rel := range originals {
					if !contains(files, rel) {
						t.Errorf("%s: git ls-files lacks %s; the user's global ignore kept it out of the sync", label, rel)
					}
				}
				for _, rel := range ignoredFiles {
					if contains(files, rel) {
						t.Errorf("%s: git ls-files has %s, which the managed .gitignore keeps out", label, rel)
					}
				}
			}
			tracked("after Init", vault)

			// A later ingest on this PC: a new source and its original, one
			// sync commit, one push.
			put(t, vault, "raw/articles/w.md", "# W\n")
			put(t, vault, "raw/articles/w.md.orig", "# W\r\n")
			put(t, vault, "raw/articles/v.html", "<p>v</p>")
			if committed, err := CommitWork(opts(vault, remote), "lw ingest"); err != nil || !committed {
				t.Fatalf("CommitWork = %v, %v", committed, err)
			}
			if _, err := Push(ctx, opts(vault, remote)); err != nil {
				t.Fatalf("Push: %v", err)
			}
			for rel, body := range map[string]string{
				"raw/articles/x.md.orig": originals["raw/articles/x.md.orig"],
				"raw/articles/y.html":    originals["raw/articles/y.html"],
				"raw/articles/z.txt":     originals["raw/articles/z.txt"],
				"raw/articles/w.md.orig": "# W\r\n",
				"raw/articles/v.html":    "<p>v</p>",
			} {
				if got := gitRaw(t, remote, "cat-file", "blob", "main:"+rel); got != body {
					t.Errorf("remote %s = %q, want %q", rel, got, body)
				}
			}

			// The second PC receives every original, byte for byte.
			clone := filepath.Join(t.TempDir(), "pc-b")
			if err := Clone(ctx, opts(clone, remote), 1); err != nil {
				t.Fatalf("Clone: %v", err)
			}
			for rel, body := range originals {
				if got := readFile(t, filepath.Join(clone, rel)); got != body {
					t.Errorf("PC B %s = %q, want %q", rel, got, body)
				}
			}

			// And an original made on PC B travels back the other way.
			put(t, clone, "raw/articles/b.md.orig", "# B\r\n")
			put(t, clone, "raw/articles/b.html", "<p>b</p>")
			if committed, err := CommitWork(opts(clone, remote), "lw b"); err != nil || !committed {
				t.Fatalf("B CommitWork = %v, %v", committed, err)
			}
			tracked("PC B after its own commit", clone)
			for _, rel := range []string{"raw/articles/b.md.orig", "raw/articles/b.html"} {
				if !contains(strings.Split(git(t, clone, "ls-files"), "\n"), rel) {
					t.Errorf("PC B git ls-files lacks %s", rel)
				}
			}
		})
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

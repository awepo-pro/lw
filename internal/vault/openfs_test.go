package vault

import (
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/awepo-pro/lw/internal/testutil"
)

// This file holds every test for S2-T0 (vault.OpenFS). All other _test.go
// files in this package are pre-existing S1 regression surface and are left
// untouched.

// --- synthetic in-memory vault, shared by several tests below ---

const synthSchemaMD = "# SCHEMA\n" +
	"\n" +
	"## Domain\n" +
	"\n" +
	"synthetic-domain — a small in-memory vault for OpenFS tests.\n" +
	"\n" +
	"## Tags\n" +
	"\n" +
	"- `alpha` — first synthetic tag.\n" +
	"- `beta` — second synthetic tag.\n"

const synthPageA = "---\n" +
	"title: A\n" +
	"created: 2026-01-01\n" +
	"updated: 2026-01-01\n" +
	"type: concept\n" +
	"tags: [alpha]\n" +
	"---\n" +
	"\n" +
	"# A\n" +
	"\n" +
	"See [[b]] and [[c]].\n"

const synthPageB = "---\n" +
	"title: B\n" +
	"created: 2026-01-01\n" +
	"updated: 2026-01-01\n" +
	"type: concept\n" +
	"tags: [beta]\n" +
	"---\n" +
	"\n" +
	"# B\n" +
	"\n" +
	"See [[c]].\n"

const synthPageC = "---\n" +
	"title: C\n" +
	"created: 2026-01-01\n" +
	"updated: 2026-01-01\n" +
	"type: concept\n" +
	"tags: [alpha, beta]\n" +
	"---\n" +
	"\n" +
	"# C\n" +
	"\n" +
	"See [[a]].\n"

const synthRawSource = "---\n" +
	"source_url: https://example.org/source-one\n" +
	"ingested: 2026-01-01\n" +
	"sha256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n" +
	"---\n" +
	"\n" +
	"# Source One\n" +
	"\n" +
	"Some raw content cited by page A.\n"

const synthIndexMD = "# Index\n" +
	"\n" +
	"- [[a]]\n" +
	"- [[b]]\n" +
	"- [[c]]\n"

const synthLogMD = "# Log\n" +
	"\n" +
	"- 2026-01-01 — vault created.\n"

// newSynthFS builds the small in-memory vault used by several tests below:
// a SCHEMA.md, three interlinked pages under wiki/concepts (a -> b, a -> c,
// b -> c, c -> a, so nothing is broken or orphaned), one raw source, and
// top-level index.md/log.md — everything OpenFS needs to load a complete
// vault that never touches disk.
func newSynthFS() fstest.MapFS {
	return fstest.MapFS{
		"SCHEMA.md":                  {Data: []byte(synthSchemaMD)},
		"wiki/concepts/a.md":         {Data: []byte(synthPageA)},
		"wiki/concepts/b.md":         {Data: []byte(synthPageB)},
		"wiki/concepts/c.md":         {Data: []byte(synthPageC)},
		"raw/articles/source-one.md": {Data: []byte(synthRawSource)},
		"index.md":                   {Data: []byte(synthIndexMD)},
		"log.md":                     {Data: []byte(synthLogMD)},
	}
}

// TestOpenFSEquivalence proves Open(dir) and OpenFS(os.DirFS(dir)) agree
// exactly over the minimal and dirty fixtures: same pages, same raw
// sources, same parse errors, same graph, same Read result. OpenFS is only
// useful if it is a drop-in replacement for the disk-backed path everything
// else in the package already exercises.
func TestOpenFSEquivalence(t *testing.T) {
	for _, name := range []string{"minimal", "dirty"} {
		t.Run(name, func(t *testing.T) {
			dir := testutil.CopyFixture(t, name)

			vDisk, err := Open(dir)
			if err != nil {
				t.Fatalf("Open(%s): %v", name, err)
			}
			vFS, err := OpenFS(os.DirFS(dir))
			if err != nil {
				t.Fatalf("OpenFS(%s): %v", name, err)
			}

			pagesDisk, pagesFS := vDisk.Pages(), vFS.Pages()
			if len(pagesDisk) != len(pagesFS) {
				t.Fatalf("len(Pages()) disk=%d fs=%d", len(pagesDisk), len(pagesFS))
			}
			for i := range pagesDisk {
				if pagesDisk[i].Path != pagesFS[i].Path {
					t.Errorf("Pages()[%d].Path disk=%q fs=%q", i, pagesDisk[i].Path, pagesFS[i].Path)
				}
			}

			rawDisk, rawFS := vDisk.RawSources(), vFS.RawSources()
			if len(rawDisk) != len(rawFS) {
				t.Fatalf("len(RawSources()) disk=%d fs=%d", len(rawDisk), len(rawFS))
			}
			for i := range rawDisk {
				if rawDisk[i].Path != rawFS[i].Path {
					t.Errorf("RawSources()[%d].Path disk=%q fs=%q", i, rawDisk[i].Path, rawFS[i].Path)
				}
			}

			errDisk, errFS := vDisk.ParseErrors(), vFS.ParseErrors()
			if len(errDisk) != len(errFS) {
				t.Fatalf("len(ParseErrors()) disk=%d fs=%d", len(errDisk), len(errFS))
			}
			for i := range errDisk {
				if errDisk[i].Path != errFS[i].Path {
					t.Errorf("ParseErrors()[%d].Path disk=%q fs=%q", i, errDisk[i].Path, errFS[i].Path)
				}
			}

			brokenDisk, brokenFS := vDisk.Graph().Broken(), vFS.Graph().Broken()
			if len(brokenDisk) != len(brokenFS) {
				t.Fatalf("len(Graph().Broken()) disk=%d fs=%d", len(brokenDisk), len(brokenFS))
			}
			for i := range brokenDisk {
				if brokenDisk[i] != brokenFS[i] {
					t.Errorf("Graph().Broken()[%d] disk=%+v fs=%+v", i, brokenDisk[i], brokenFS[i])
				}
			}

			orphansDisk, orphansFS := vDisk.Graph().Orphans(), vFS.Graph().Orphans()
			if len(orphansDisk) != len(orphansFS) {
				t.Fatalf("len(Graph().Orphans()) disk=%d fs=%d", len(orphansDisk), len(orphansFS))
			}
			for i := range orphansDisk {
				if orphansDisk[i] != orphansFS[i] {
					t.Errorf("Graph().Orphans()[%d] disk=%q fs=%q", i, orphansDisk[i], orphansFS[i])
				}
			}

			bDisk, err := vDisk.Read("index.md")
			if err != nil {
				t.Fatalf("disk Read(index.md): %v", err)
			}
			bFS, err := vFS.Read("index.md")
			if err != nil {
				t.Fatalf("fs Read(index.md): %v", err)
			}
			if string(bDisk) != string(bFS) {
				t.Errorf("Read(index.md) bytes differ between Open and OpenFS")
			}
		})
	}
}

// TestOpenFSInMemory proves OpenFS loads a complete, correct vault from a
// purely in-memory fs.FS — no disk anywhere — with the pages, raw sources
// and graph edges that were actually constructed, and Root() == "".
func TestOpenFSInMemory(t *testing.T) {
	v, err := OpenFS(newSynthFS())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}

	if got := v.Root(); got != "" {
		t.Errorf("Root() = %q, want \"\"", got)
	}

	wantPages := []string{"wiki/concepts/a.md", "wiki/concepts/b.md", "wiki/concepts/c.md"}
	pages := v.Pages()
	if len(pages) != len(wantPages) {
		t.Fatalf("len(Pages()) = %d, want %d: %v", len(pages), len(wantPages), pages)
	}
	for i, p := range pages {
		if p.Path != wantPages[i] {
			t.Errorf("Pages()[%d].Path = %q, want %q", i, p.Path, wantPages[i])
		}
	}
	if pages[0].FM.Title != "A" {
		t.Errorf("Pages()[0].FM.Title = %q, want %q", pages[0].FM.Title, "A")
	}

	rawSources := v.RawSources()
	if len(rawSources) != 1 {
		t.Fatalf("len(RawSources()) = %d, want 1: %v", len(rawSources), rawSources)
	}
	if got := rawSources[0].Path; got != "raw/articles/source-one.md" {
		t.Errorf("RawSources()[0].Path = %q, want %q", got, "raw/articles/source-one.md")
	}
	if got := rawSources[0].SourceURL; got != "https://example.org/source-one" {
		t.Errorf("RawSources()[0].SourceURL = %q", got)
	}

	g := v.Graph()
	if got := g.Broken(); len(got) != 0 {
		t.Errorf("Graph().Broken() = %+v, want empty", got)
	}
	if got := g.Orphans(); len(got) != 0 {
		t.Errorf("Graph().Orphans() = %v, want empty", got)
	}

	out := g.Outbound("wiki/concepts/a.md")
	if len(out) != 2 || out[0].To != "wiki/concepts/b.md" || out[1].To != "wiki/concepts/c.md" {
		t.Errorf("Outbound(a) = %+v, want edges to b then c", out)
	}

	back := g.Backlinks("wiki/concepts/c.md")
	if len(back) != 2 || back[0].From != "wiki/concepts/a.md" || back[1].From != "wiki/concepts/b.md" {
		t.Errorf("Backlinks(c) = %+v, want edges from a then b", back)
	}
}

// TestOpenFSReadNonPageFiles proves Read and Exists work over an
// in-memory vault for files that are never parsed as pages — index.md,
// log.md, SCHEMA.md. This is load-bearing (backbone §5.4): lint's
// index-sync and log-rotate checks call Vault.Read("index.md") /
// Read("log.md") and silently report no findings when that read fails, so
// a Read that only worked on disk would turn Engine.Append's in-memory
// projection into a silent false "lint: pass".
func TestOpenFSReadNonPageFiles(t *testing.T) {
	v, err := OpenFS(newSynthFS())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}

	cases := []struct {
		path string
		want string
	}{
		{"index.md", synthIndexMD},
		{"log.md", synthLogMD},
		{"SCHEMA.md", synthSchemaMD},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if !v.Exists(tc.path) {
				t.Errorf("Exists(%q) = false, want true", tc.path)
			}
			b, err := v.Read(tc.path)
			if err != nil {
				t.Fatalf("Read(%q): %v", tc.path, err)
			}
			if string(b) != tc.want {
				t.Errorf("Read(%q) = %q, want %q", tc.path, b, tc.want)
			}
		})
	}

	if v.Exists("does-not-exist.md") {
		t.Errorf("Exists(does-not-exist.md) = true, want false")
	}
}

// TestOpenFSEscapeAndNotFound proves the escape checks and ErrNotFound
// identity hold over an in-memory vault exactly as they do over a
// disk-backed one (TestVaultReadRejectsEscape / TestVaultReadAndExists in
// vault_test.go).
func TestOpenFSEscapeAndNotFound(t *testing.T) {
	v, err := OpenFS(newSynthFS())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}

	escapes := []string{
		"",
		"../../etc/passwd",
		"/etc/passwd",
		"wiki/../../x",
	}
	for _, p := range escapes {
		t.Run("escape/"+p, func(t *testing.T) {
			if _, err := v.Read(p); !errors.Is(err, ErrOutsideVault) {
				t.Fatalf("Read(%q) error = %v, want ErrOutsideVault", p, err)
			}
			if v.Exists(p) {
				t.Errorf("Exists(%q) = true, want false", p)
			}
		})
	}

	if _, err := v.Read("wiki/concepts/does-not-exist.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read(does-not-exist.md) error = %v, want ErrNotFound", err)
	}
	if v.Exists("wiki/concepts/does-not-exist.md") {
		t.Errorf("Exists(does-not-exist.md) = true, want false")
	}
}

// TestOpenFSNoRawDir proves a vault with no raw/ directory at all loads
// cleanly with zero raw sources and no error — a fresh vault may not have
// ingested anything yet (backbone §2.8).
func TestOpenFSNoRawDir(t *testing.T) {
	fsys := newSynthFS()
	delete(fsys, "raw/articles/source-one.md")

	v, err := OpenFS(fsys)
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	if got := len(v.RawSources()); got != 0 {
		t.Errorf("len(RawSources()) = %d, want 0: %v", got, v.RawSources())
	}
	if got := len(v.ParseErrors()); got != 0 {
		t.Errorf("len(ParseErrors()) = %d, want 0: %v", got, v.ParseErrors())
	}
	if got := len(v.Pages()); got != 3 {
		t.Errorf("len(Pages()) = %d, want 3", got)
	}
}

// TestOpenFSSubdirNotADirectory proves a wiki/ that exists but is a regular
// file, not a directory, is still a fatal error over an in-memory fs.FS —
// the same behaviour Open has always had on disk.
func TestOpenFSSubdirNotADirectory(t *testing.T) {
	fsys := fstest.MapFS{
		"SCHEMA.md": {Data: []byte(synthSchemaMD)},
		"wiki":      {Data: []byte("not a directory")},
	}
	_, err := OpenFS(fsys)
	if err == nil {
		t.Fatalf("OpenFS: want error when wiki is a file, not a directory")
	}
	// walkMarkdown returns a plain fmt.Errorf here, with no sentinel to match
	// on, so assert the message: without this the test would also pass if
	// OpenFS started failing on this fixture for an unrelated reason.
	if !strings.Contains(err.Error(), "is not a directory") {
		t.Errorf("OpenFS error = %v, want it to mention \"is not a directory\"", err)
	}
}

// TestResolvePathNormalizesNonCanonical locks in the rule that Read/Exists
// reject exactly the three shapes backbone §2.8 freezes — empty, absolute,
// ".."-bearing — and NORMALIZE every other spelling rather than refusing it
// (MASTER §9 D-AP).
//
// This is a regression guard with real history: S2-T0 first shipped a bare
// fs.ValidPath rejection here, which refused "./index.md", "wiki//a.md" and
// "wiki/a.md/" with ErrOutsideVault even though none of them escapes. A
// differential fuzz over the pre-S2-T0 implementation found 160 such inputs.
// Nothing observable broke at the time, because the only callers in the tree
// pass literal clean paths — but S3's tool layer hands Vault.Read a
// model-supplied path string, and "./wiki/concepts/kv-cache.md" failing with
// "path escapes vault root" is a bug that would surface a stage later.
func TestResolvePathNormalizesNonCanonical(t *testing.T) {
	v, err := OpenFS(newSynthFS())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}

	// Every spelling below denotes an existing in-bounds file and must resolve.
	for _, p := range []string{
		"index.md",
		"./index.md",
		"wiki/concepts/a.md",
		"./wiki/concepts/a.md",
		"wiki//concepts/a.md",
		"wiki/./concepts/a.md",
		"wiki/concepts/./a.md",
	} {
		t.Run("accept/"+p, func(t *testing.T) {
			if !v.Exists(p) {
				t.Errorf("Exists(%q) = false, want true", p)
			}
			if _, err := v.Read(p); err != nil {
				t.Errorf("Read(%q) = %v, want success", p, err)
			}
		})
	}

	// A trailing slash on a directory-free name normalizes away too.
	if _, err := v.Read("index.md/"); err != nil {
		t.Errorf("Read(%q) = %v, want success", "index.md/", err)
	}

	// The three frozen rejections still reject, with the frozen identity.
	for _, p := range []string{"", "/etc/passwd", "../../etc/passwd", "wiki/../../x"} {
		t.Run("reject/"+p, func(t *testing.T) {
			if _, err := v.Read(p); !errors.Is(err, ErrOutsideVault) {
				t.Errorf("Read(%q) error = %v, want ErrOutsideVault", p, err)
			}
		})
	}
}

package eval

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// treeEntry is what the round-trip test compares for one vault entry: kind,
// permission bits, mtime (whole seconds) and content.
type treeEntry struct {
	dir   bool
	mode  fs.FileMode
	mtime int64
	data  string
}

// readTree walks root and returns every entry below it, keyed by its
// slash-separated path relative to root. skip names top-level-relative
// prefixes to leave out (the dirs Snapshot must exclude).
func readTree(t *testing.T, root string, skip ...string) map[string]treeEntry {
	t.Helper()
	out := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		for _, s := range skip {
			if rel == s || strings.HasPrefix(rel, s+"/") {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := treeEntry{dir: d.IsDir(), mode: info.Mode().Perm(), mtime: info.ModTime().Unix()}
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			e.data = string(b)
		}
		out[rel] = e
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// vaultFile is one file the synthetic vault is built from.
type vaultFile struct {
	path  string
	data  []byte
	mode  fs.FileMode
	mtime time.Time
}

// allBytes is every byte value, so a test file holds binary content no text
// handling could survive unchanged.
func allBytes() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// buildVault writes a synthetic vault shaped like a real one — pages, a
// binary original, an empty directory, a read-only CAS object, the
// engine's state — plus the trace and log files Snapshot must leave out.
// Mtimes carry sub-second parts and differ per file, and every directory's
// mtime is set last (bottom-up) so it holds.
func buildVault(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "vault")
	base := time.Date(2026, 9, 1, 12, 0, 0, 123456789, time.UTC)
	files := []vaultFile{
		{"SCHEMA.md", []byte("# SCHEMA\n"), 0o644, base},
		{"index.md", []byte("# Index\n"), 0o644, base.Add(1 * time.Hour)},
		{"wiki/concepts/a.md", []byte("---\ntitle: A\n---\n\nbody\n"), 0o644, base.Add(2 * time.Hour)},
		// A file whose name continues a sibling directory's name: a
		// directory-by-directory walk visits it after concepts/'s contents,
		// a sort by path puts it before them ('.' sorts below '/').
		{"wiki/concepts.md", []byte("# Concepts\n"), 0o644, base.Add(2 * time.Hour)},
		{"wiki/concepts/run.sh", []byte("#!/bin/sh\n"), 0o755, base.Add(3 * time.Hour)},
		{"raw/papers/bin.pdf", allBytes(), 0o600, base.Add(4 * time.Hour)},
		{".llmwiki/objects/ab/abcdef", []byte("blob"), 0o444, base.Add(5 * time.Hour)},
		{".llmwiki/index.gob", []byte("gob"), 0o644, base.Add(6 * time.Hour)},
		{".llmwiki/journal.ndjson", []byte("{}\n"), 0o644, base.Add(7 * time.Hour)},
		{".llmwiki/changesets/committed/cs-1/changeset.json", []byte("{}\n"), 0o644, base.Add(8 * time.Hour)},
		// The three things Snapshot must not carry.
		{".llmwiki/traces/20260101T000000Z-aaaa/events.ndjson", []byte("{}\n"), 0o600, base},
		{".llmwiki/logs/lw.log", []byte("log\n"), 0o644, base},
		{".llmwiki/logs/lw.log.1", []byte("log\n"), 0o644, base},
	}
	for _, f := range files {
		full := filepath.Join(root, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, f.data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(full, f.mtime, f.mtime); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"raw/empty", ".llmwiki/changesets/open", ".llmwiki/changesets/rejected", ".llmwiki/snapshots"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A restrictive directory mode must round-trip too, and must not stop
	// Extract from writing the files inside it.
	if err := os.Chmod(filepath.Join(root, "raw", "papers"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Directory mtimes last, deepest first: creating a child bumps its
	// parent's mtime, so the order is what makes them stick.
	var dirs []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for i, d := range dirs {
		mt := base.Add(time.Duration(100+i) * time.Hour)
		if err := os.Chtimes(d, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// tarNames lists the entry names of a tar.gz in file order.
func tarNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(z)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

// TestSnapshotRoundTrip pins 037 T1: Snapshot leaves out traces and logs
// and keeps everything else; Extract into an empty dir reproduces every
// kept entry byte for byte with its mode bits and mtime (to the second);
// the returned sha is the sha256 of the tar.gz bytes.
func TestSnapshotRoundTrip(t *testing.T) {
	src := buildVault(t)
	out := filepath.Join(t.TempDir(), "vault-20261005.tar.gz")

	sha, err := Snapshot(src, out)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if want := hex.EncodeToString(sum[:]); sha != want {
		t.Fatalf("returned sha %s != sha256 of the tar.gz bytes %s", sha, want)
	}

	names := tarNames(t, out)
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = strings.TrimSuffix(n, "/") // a directory entry's name ends in "/"; its path does not
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("tar entries are not sorted by path: %q", names)
	}
	for _, n := range names {
		if strings.Contains(n, ".llmwiki/traces") || strings.Contains(n, ".llmwiki/logs") {
			t.Errorf("tar carries excluded entry %q", n)
		}
	}

	dst := filepath.Join(t.TempDir(), "scratch")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Extract(out, dst); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := readTree(t, src, ".llmwiki/traces", ".llmwiki/logs")
	got := readTree(t, dst)
	if !reflect.DeepEqual(got, want) {
		var diff []string
		for p, w := range want {
			if g, ok := got[p]; !ok {
				diff = append(diff, "missing "+p)
			} else if g != w {
				diff = append(diff, "differs "+p)
			}
		}
		for p := range got {
			if _, ok := want[p]; !ok {
				diff = append(diff, "extra "+p)
			}
		}
		sort.Strings(diff)
		t.Fatalf("extracted tree != source tree (minus traces/logs): %s", strings.Join(diff, ", "))
	}
	// The kept set really includes the awkward entries.
	for _, p := range []string{"raw/empty", ".llmwiki/changesets/open", ".llmwiki/objects/ab/abcdef", "raw/papers/bin.pdf"} {
		if _, ok := got[p]; !ok {
			t.Errorf("extracted tree lacks %s", p)
		}
	}
	if m := got["wiki/concepts/run.sh"].mode; m != 0o755 {
		t.Errorf("run.sh mode = %o, want 755", m)
	}
	if m := got[".llmwiki/objects/ab/abcdef"].mode; m != 0o444 {
		t.Errorf("read-only object mode = %o, want 444", m)
	}
	if m := got["raw/papers"].mode; m != 0o750 {
		t.Errorf("raw/papers dir mode = %o, want 750", m)
	}
}

// TestSnapshotDeterministic pins the determinism the cases.toml hash relies
// on: the same vault snapshotted twice yields the same bytes.
func TestSnapshotDeterministic(t *testing.T) {
	src := buildVault(t)
	dir := t.TempDir()
	a, err := Snapshot(src, filepath.Join(dir, "a.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Snapshot(src, filepath.Join(dir, "b.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("two snapshots of one vault differ: %s vs %s", a, b)
	}
}

// TestSnapshotRefusesSymlinks pins that Snapshot never writes an entry
// Extract would refuse: a symlink in the vault fails the snapshot.
func TestSnapshotRefusesSymlinks(t *testing.T) {
	src := buildVault(t)
	if err := os.Symlink("index.md", filepath.Join(src, "link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	out := filepath.Join(t.TempDir(), "x.tar.gz")
	if _, err := Snapshot(src, out); err == nil || !strings.Contains(err.Error(), "link.md") {
		t.Fatalf("Snapshot over a symlink = %v; want an error naming link.md", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("Snapshot left %s behind after failing", out)
	}
}

// tarEntry is one hand-built tarball entry: the header exactly as given
// (so a test can craft one Snapshot would never write) and, for a regular
// file, its content.
type tarEntry struct {
	hdr  tar.Header
	body string
}

// writeTarGz writes entries to path as a tar.gz, headers exactly as given.
func writeTarGz(t *testing.T, path string, entries []tarEntry) {
	t.Helper()
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	tw := tar.NewWriter(z)
	for _, e := range entries {
		h := e.hdr
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSnapshotRefusesOpenChangeset pins that a vault with any entry in
// .llmwiki/changesets/open/ cannot be snapshotted (a frozen vault with a
// half-reviewed changeset is not the vault anyone asked about), that the
// message names the entry, and that no file is written; and that Extract
// refuses a tarball carrying such an entry the same way.
func TestSnapshotRefusesOpenChangeset(t *testing.T) {
	const entry = "cs-20261005T101500Z-ab12"
	src := buildVault(t)
	openDir := filepath.Join(src, ".llmwiki", "changesets", "open", entry)
	if err := os.MkdirAll(openDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(openDir, "changeset.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	out := filepath.Join(outDir, "snap.tar.gz")
	sha, err := Snapshot(src, out)
	if err == nil {
		t.Fatal("Snapshot of a vault with an open changeset succeeded")
	}
	for _, want := range []string{"open changeset", entry} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if sha != "" {
		t.Errorf("Snapshot returned sha %q alongside its error", sha)
	}
	if left, _ := os.ReadDir(outDir); len(left) != 0 {
		t.Errorf("Snapshot left files behind after refusing: %v", left)
	}

	// The same entry in a tarball: Extract refuses, with a leading "./"
	// too, and leaves dst empty.
	for _, name := range []string{
		".llmwiki/changesets/open/cs-x/changeset.json",
		"./.llmwiki/changesets/open/cs-x/",
	} {
		tgz := filepath.Join(t.TempDir(), "evil.tar.gz")
		typeflag := byte(tar.TypeReg)
		if strings.HasSuffix(name, "/") {
			typeflag = tar.TypeDir
		}
		writeTarGz(t, tgz, []tarEntry{
			{hdr: tar.Header{Name: "ok.md", Typeflag: tar.TypeReg, Mode: 0o644}, body: "ok"},
			{hdr: tar.Header{Name: name, Typeflag: typeflag, Mode: 0o644}, body: "{}"},
		})
		dst := filepath.Join(t.TempDir(), "scratch")
		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		err := Extract(tgz, dst)
		if err == nil {
			t.Fatalf("Extract of a tarball carrying %q succeeded", name)
		}
		for _, want := range []string{"open changeset", "cs-x"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: error %q does not contain %q", name, err, want)
			}
		}
		if left, _ := os.ReadDir(dst); len(left) != 0 {
			t.Errorf("%q: Extract left %d entries in dst after refusing", name, len(left))
		}
	}
}

// TestExtractRejectsUnsafeEntries pins that an absolute path, a ".." entry
// and a symlink each fail Extract — and that nothing lands outside dst.
func TestExtractRejectsUnsafeEntries(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		hdr  tar.Header
	}{
		{"absolute path", tar.Header{Name: filepath.Join(outside, "abs.txt"), Typeflag: tar.TypeReg, Mode: 0o644}},
		{"dot-dot entry", tar.Header{Name: "../outside/dotdot.txt", Typeflag: tar.TypeReg, Mode: 0o644}},
		{"dot-dot in the middle", tar.Header{Name: "a/../../outside/mid.txt", Typeflag: tar.TypeReg, Mode: 0o644}},
		{"bare dot-dot dir", tar.Header{Name: "..", Typeflag: tar.TypeDir, Mode: 0o755}},
		{"symlink", tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: outside, Mode: 0o777}},
		{"symlink pointing inside", tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "ok.txt", Mode: 0o777}},
		{"hard link", tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "ok.txt", Mode: 0o644}},
		{"fifo", tar.Header{Name: "pipe", Typeflag: tar.TypeFifo, Mode: 0o644}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tgz := filepath.Join(root, "evil.tar.gz")
			writeTarGz(t, tgz, []tarEntry{
				{hdr: tar.Header{Name: "ok.txt", Typeflag: tar.TypeReg, Mode: 0o644}, body: "ok"},
				{hdr: tc.hdr, body: "evil"},
			})
			dst := filepath.Join(root, "dst")
			if err := os.MkdirAll(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dst) })

			if err := Extract(tgz, dst); err == nil {
				t.Fatalf("Extract accepted the %s entry", tc.name)
			}
			if left, _ := os.ReadDir(dst); len(left) != 0 {
				t.Errorf("dst holds %d entries after a refused extract, want 0", len(left))
			}
			if left, _ := os.ReadDir(outside); len(left) != 0 {
				t.Errorf("a file landed in the sibling dir: %v", left)
			}
			top, _ := os.ReadDir(root)
			var names []string
			for _, e := range top {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			if want := []string{"dst", "evil.tar.gz", "outside"}; !reflect.DeepEqual(names, want) {
				t.Errorf("extract wrote outside dst: %v, want %v", names, want)
			}
		})
	}
}

// TestExtractNeedsEmptyDst pins that Extract never merges into a populated
// directory — a stale scratch vault would silently poison a run.
func TestExtractNeedsEmptyDst(t *testing.T) {
	tgz := filepath.Join(t.TempDir(), "ok.tar.gz")
	writeTarGz(t, tgz, []tarEntry{{hdr: tar.Header{Name: "a.md", Typeflag: tar.TypeReg, Mode: 0o644}, body: "a"}})
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "stale.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Extract(tgz, dst); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("Extract into a populated dir = %v; want a 'not empty' error", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "stale.md")); string(b) != "stale" {
		t.Errorf("Extract disturbed the existing file")
	}
}

// TestExtractCreatesMissingDst pins that Extract makes dst when it does not
// exist, and removes it again when the extract fails.
func TestExtractCreatesMissingDst(t *testing.T) {
	tgz := filepath.Join(t.TempDir(), "ok.tar.gz")
	writeTarGz(t, tgz, []tarEntry{{hdr: tar.Header{Name: "a.md", Typeflag: tar.TypeReg, Mode: 0o644}, body: "a"}})
	dst := filepath.Join(t.TempDir(), "deep", "scratch")
	if err := Extract(tgz, dst); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "a.md")); err != nil || string(b) != "a" {
		t.Fatalf("a.md = %q, %v", b, err)
	}

	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	writeTarGz(t, bad, []tarEntry{{hdr: tar.Header{Name: "../x", Typeflag: tar.TypeReg, Mode: 0o644}, body: "x"}})
	gone := filepath.Join(t.TempDir(), "gone")
	if err := Extract(bad, gone); err == nil {
		t.Fatal("Extract of an unsafe tarball succeeded")
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("a failed Extract left the dst it created: %v", err)
	}
}

package cache

// cache_test.go pins the extraction cache against a counting fake inner
// Extractor (007 T2). Extraction is expensive — seconds to minutes per
// Docling call — so every pin here guards either a hit that must NOT reach
// inner, or a miss that must reach it exactly once.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
)

// fakeInner is a counting Extractor: it records how many times Extract was
// called and hands back a fixed Doc (stamped with the requested uri, the
// way a real backend records provenance).
type fakeInner struct {
	calls int
	doc   extract.Doc
	err   error
}

func (f *fakeInner) CanHandle(uri string) bool { return true }

func (f *fakeInner) Extract(_ context.Context, uri string) (*extract.Doc, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	d := f.doc
	d.SourceURL = uri
	return &d, nil
}

// newFakeDoc returns a distinctive Doc for the fake to serve.
func newFakeDoc() extract.Doc {
	return extract.Doc{
		Title:     "Pinned Title",
		Markdown:  "# Pinned Title\n\nDeterministic body.\n",
		Kind:      "paper",
		Extractor: "sidecar/docling",
	}
}

// constVersion returns a version func returning v, counting its calls.
func constVersion(v string, count *int) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		*count++
		return v, nil
	}
}

// writeSource writes content to a fresh .md file under dir and returns its
// path. .md so the extension is one a real extractor chain would accept.
func writeSource(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// captureSlog redirects the default logger into buf for the duration of f,
// so K8 can assert a warn was logged.
func captureSlog(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
}

// K1 — two Extracts of one file: inner called once, Docs equal, Markdown
// byte-identical.
func TestK1SecondExtractHitsCache(t *testing.T) {
	home := t.TempDir()
	src := writeSource(t, home, "a.md", "# A\n\nbody\n")
	dir := t.TempDir()
	var vc int
	inner := &fakeInner{doc: newFakeDoc()}
	c := New(inner, dir, "sidecar/docling", constVersion("v1", &vc))
	ctx := context.Background()

	d1, err := c.Extract(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := c.Extract(ctx, src)
	if err != nil {
		t.Fatal(err)
	}

	if inner.calls != 1 {
		t.Fatalf("inner called %d times, want 1", inner.calls)
	}
	if d1.Markdown != d2.Markdown {
		t.Fatalf("markdown differs between calls")
	}
	if *d1 != *d2 {
		t.Fatalf("docs differ:\n%+v\n%+v", d1, d2)
	}
}

// K2 — same bytes at a second path: hit, SourceURL is the second path.
func TestK2SameBytesNewPathRecordsNewProvenance(t *testing.T) {
	home := t.TempDir()
	content := "# Same\n\nbytes\n"
	p1 := writeSource(t, home, "one.md", content)
	p2 := writeSource(t, home, "two.md", content)
	dir := t.TempDir()
	var vc int
	inner := &fakeInner{doc: newFakeDoc()}
	c := New(inner, dir, "sidecar/docling", constVersion("v1", &vc))
	ctx := context.Background()

	if _, err := c.Extract(ctx, p1); err != nil {
		t.Fatal(err)
	}
	d2, err := c.Extract(ctx, p2)
	if err != nil {
		t.Fatal(err)
	}

	if inner.calls != 1 {
		t.Fatalf("inner called %d times, want 1", inner.calls)
	}
	if d2.SourceURL != p2 {
		t.Fatalf("SourceURL = %q, want the requested path %q", d2.SourceURL, p2)
	}
}

// K3 — file bytes change: miss.
func TestK3ChangedBytesMiss(t *testing.T) {
	home := t.TempDir()
	src := writeSource(t, home, "a.md", "# A\n\nv1 body\n")
	dir := t.TempDir()
	var vc int
	inner := &fakeInner{doc: newFakeDoc()}
	c := New(inner, dir, "sidecar/docling", constVersion("v1", &vc))
	ctx := context.Background()

	if _, err := c.Extract(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("# A\n\nv2 body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Extract(ctx, src); err != nil {
		t.Fatal(err)
	}

	if inner.calls != 2 {
		t.Fatalf("inner called %d times, want 2 (changed bytes must miss)", inner.calls)
	}
}

// K4 — version string changes: miss.
func TestK4VersionChangeMiss(t *testing.T) {
	home := t.TempDir()
	src := writeSource(t, home, "a.md", "# A\n\nbody\n")
	dir := t.TempDir()
	var vc int
	inner := &fakeInner{doc: newFakeDoc()}
	ctx := context.Background()

	c1 := New(inner, dir, "sidecar/docling", constVersion("docling-1.0", &vc))
	if _, err := c1.Extract(ctx, src); err != nil {
		t.Fatal(err)
	}
	c2 := New(inner, dir, "sidecar/docling", constVersion("docling-2.0", &vc))
	if _, err := c2.Extract(ctx, src); err != nil {
		t.Fatal(err)
	}

	if inner.calls != 2 {
		t.Fatalf("inner called %d times, want 2 (version change must miss)", inner.calls)
	}
	// The version must live in the KEY, not only in the entry validation:
	// each version gets its own entry file, so an old entry is never
	// overwritten by a new version (the file would otherwise collide and
	// correctness would hang on the field check alone).
	entries, _, err := Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if entries != 2 {
		t.Fatalf("dir holds %d entries, want 2 (version must be part of the key)", entries)
	}
}

// K5 — version is called at most once across 3 Extracts, and never for a URL.
func TestK5VersionCalledOnceAndNeverForURL(t *testing.T) {
	home := t.TempDir()
	src := writeSource(t, home, "a.md", "# A\n\nbody\n")
	dir := t.TempDir()
	var vc int
	inner := &fakeInner{doc: newFakeDoc()}
	c := New(inner, dir, "sidecar/docling", constVersion("v1", &vc))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.Extract(ctx, src); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Extract(ctx, "https://example.com/page"); err != nil {
		t.Fatal(err)
	}

	if vc != 1 {
		t.Fatalf("version called %d times, want 1", vc)
	}
	if inner.calls != 2 { // once for the file, once for the URL
		t.Fatalf("inner called %d times, want 2", inner.calls)
	}
}

// K6 — inner error: same error (errors.Is), no entry written.
func TestK6InnerErrorPassedThroughUncached(t *testing.T) {
	home := t.TempDir()
	src := writeSource(t, home, "a.md", "# A\n\nbody\n")
	dir := t.TempDir()
	var vc int
	sentinel := errors.New("sidecar missing")
	inner := &fakeInner{err: sentinel}
	c := New(inner, dir, "sidecar/docling", constVersion("v1", &vc))

	_, err := c.Extract(context.Background(), src)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want errors.Is %v", err, sentinel)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil || len(entries) != 0 {
		t.Fatalf("dir has %d entries (err %v), want none written", len(entries), rerr)
	}
}

// K7 — garbage bytes in the entry file: treated as a miss, inner called,
// entry rewritten, no error.
func TestK7CorruptEntryReExtractsAndRewrites(t *testing.T) {
	home := t.TempDir()
	src := writeSource(t, home, "a.md", "# A\n\nbody\n")
	dir := t.TempDir()
	var vc int
	inner := &fakeInner{doc: newFakeDoc()}
	c := New(inner, dir, "sidecar/docling", constVersion("v1", &vc))
	ctx := context.Background()

	// Populate a valid entry first, then corrupt it AT ITS REAL KEY PATH.
	// Garbage at any other name would never be read — the lookup is by
	// content-derived key, so only the entry file itself exercises F.K5.
	d1, err := c.Extract(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner called %d times, want 1", inner.calls)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly 1 entry file, got %v", matches)
	}
	if err := os.WriteFile(matches[0], []byte("\x00\xffnot json"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Corrupt entry → miss: inner called again, no error surfaces.
	d2, err := c.Extract(ctx, src)
	if err != nil {
		t.Fatalf("corrupt entry must never error: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("inner called %d times, want 2 (corrupt entry must miss)", inner.calls)
	}
	if d2.Markdown != d1.Markdown {
		t.Fatalf("re-extracted doc serves different markdown")
	}

	// The miss rewrote the entry validly: the next call hits again.
	if _, err := c.Extract(ctx, src); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 2 {
		t.Fatalf("inner called %d times after rewrite, want 2 (entry was not rewritten validly)", inner.calls)
	}
}

// K8 — dir read-only: the Doc is still returned and a warn is logged.
func TestK8ReadOnlyDirStillReturnsDoc(t *testing.T) {
	home := t.TempDir()
	src := writeSource(t, home, "a.md", "# A\n\nbody\n")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	var buf bytes.Buffer
	captureSlog(t, &buf)

	var vc int
	inner := &fakeInner{doc: newFakeDoc()}
	c := New(inner, dir, "sidecar/docling", constVersion("v1", &vc))
	d, err := c.Extract(context.Background(), src)
	if err != nil {
		t.Fatalf("doc must be returned despite write failure: %v", err)
	}
	if d == nil || d.Title != "Pinned Title" {
		t.Fatalf("unexpected doc %+v", d)
	}
	if !strings.Contains(buf.String(), "extract cache write") {
		t.Fatalf("warn not logged; got: %q", buf.String())
	}
}

// K9 — dir == "": the returned value IS inner.
func TestK9EmptyDirReturnsInnerUnchanged(t *testing.T) {
	var vc int
	inner := &fakeInner{doc: newFakeDoc()}
	got := New(inner, "", "sidecar/docling", constVersion("v1", &vc))
	if got != extract.Extractor(inner) {
		t.Fatalf("New with empty dir returned a different Extractor, want inner itself")
	}
}

// K10 — Stat: 2 entries report 2 and the byte sum; a missing dir is 0, 0, nil.
func TestK10Stat(t *testing.T) {
	dir := t.TempDir()
	b1 := []byte(strings.Repeat("a", 100))
	b2 := []byte(strings.Repeat("b", 250))
	if err := os.WriteFile(filepath.Join(dir, "one.json"), b1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.json"), b2, 0o600); err != nil {
		t.Fatal(err)
	}

	entries, bytesN, err := Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if entries != 2 {
		t.Fatalf("entries = %d, want 2", entries)
	}
	if bytesN != int64(len(b1)+len(b2)) {
		t.Fatalf("bytes = %d, want %d", bytesN, len(b1)+len(b2))
	}

	missing := filepath.Join(dir, "does-not-exist")
	e2, b2n, err := Stat(missing)
	if err != nil {
		t.Fatalf("Stat on missing dir: err = %v, want nil", err)
	}
	if e2 != 0 || b2n != 0 {
		t.Fatalf("Stat on missing dir = %d, %d, want 0, 0", e2, b2n)
	}
}

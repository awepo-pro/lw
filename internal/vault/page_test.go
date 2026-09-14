package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// readPageFixture reads a fixture file relative to spec/fixtures and fails
// t on error.
func readPageFixture(t *testing.T, root string, parts ...string) []byte {
	t.Helper()
	p := filepath.Join(append([]string{root}, parts...)...)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", p, err)
	}
	return b
}

// TestParsePage proves ParsePage wires ParseFrontmatter, ParseSections and
// ParseWikilinks together correctly over a real fixture: frontmatter is
// parsed, the body is normalized, sections are found, wikilinks are found,
// and a heading inside a fenced code block does not leak into Sections.
func TestParsePage(t *testing.T) {
	root := testutil.FixtureRoot(t)
	b := readPageFixture(t, root, "minimal", "wiki", "concepts", "kv-cache.md")

	p, err := ParsePage("wiki/concepts/kv-cache.md", b)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}

	if p.Path != "wiki/concepts/kv-cache.md" {
		t.Errorf("Path = %q, want %q", p.Path, "wiki/concepts/kv-cache.md")
	}
	if p.FM.Title != "KV Cache" {
		t.Errorf("FM.Title = %q, want %q", p.FM.Title, "KV Cache")
	}
	if p.Body == "" || !strings.HasSuffix(p.Body, "\n") {
		t.Errorf("Body does not end with exactly one trailing newline: %q", lastBytes(p.Body, 20))
	}
	if strings.HasSuffix(p.Body, "\n\n") {
		t.Errorf("Body ends with more than one trailing newline: %q", lastBytes(p.Body, 20))
	}

	wantHeadings := []string{"# KV Cache", "## Why it matters", "## Example", "## Related"}
	if len(p.Sections) != len(wantHeadings) {
		t.Fatalf("len(Sections) = %d, want %d: %+v", len(p.Sections), len(wantHeadings), p.Sections)
	}
	for i, sec := range p.Sections {
		if sec.Heading != wantHeadings[i] {
			t.Errorf("Sections[%d].Heading = %q, want %q", i, sec.Heading, wantHeadings[i])
		}
		if strings.Contains(sec.Title, "not a heading") {
			t.Errorf("a fenced-code-block line leaked into Sections: %+v", sec)
		}
	}

	wantLinks := []string{"flash-attention", "speculative-decoding"}
	if len(p.Links) != len(wantLinks) {
		t.Fatalf("len(Links) = %d, want %d: %+v", len(p.Links), len(wantLinks), p.Links)
	}
	for i, l := range p.Links {
		if l.Target != wantLinks[i] {
			t.Errorf("Links[%d].Target = %q, want %q", i, l.Target, wantLinks[i])
		}
	}
}

// lastBytes returns the last n bytes of s (or all of s if shorter), for
// compact failure messages.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TestPageSection proves Section looks pages up by their full raw heading
// line, matching how /.dev-notes/PLAN-v1.md §7's stage.patch_page examples address a
// section (e.g. "## Related"), and reports ok == false for a heading that
// is not present.
func TestPageSection(t *testing.T) {
	root := testutil.FixtureRoot(t)
	b := readPageFixture(t, root, "minimal", "wiki", "concepts", "kv-cache.md")

	p, err := ParsePage("wiki/concepts/kv-cache.md", b)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}

	sec, ok := p.Section("## Related")
	if !ok {
		t.Fatalf(`Section("## Related") not found`)
	}
	if sec.Title != "Related" {
		t.Errorf("Title = %q, want %q", sec.Title, "Related")
	}

	if _, ok := p.Section("## Does Not Exist"); ok {
		t.Errorf("Section(%q) unexpectedly found", "## Does Not Exist")
	}
}

// TestPageSHA256 proves SHA256 hashes exactly Serialize()'s output, as
// 64-character lowercase hex.
func TestPageSHA256(t *testing.T) {
	created, err := ParseDate("2026-01-01")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	p := &Page{
		Path: "wiki/concepts/example.md",
		FM: Frontmatter{
			Title:   "Example",
			Created: created,
			Updated: created,
			Type:    TypeConcept,
		},
		Body: "# Example\n\nHello.\n",
	}

	sum := sha256.Sum256(p.Serialize())
	want := hex.EncodeToString(sum[:])

	got := p.SHA256()
	if got != want {
		t.Errorf("SHA256() = %q, want %q", got, want)
	}
	if len(got) != 64 {
		t.Errorf("SHA256() has length %d, want 64: %q", len(got), got)
	}
}

// TestPageSerializeEmptyBody pins the empty-body contract (backbone §2.3,
// MASTER §9 D-U): Serialize() ends "---\n\n" and nothing after when Body is
// "" — never a manufactured "\n" body.
func TestPageSerializeEmptyBody(t *testing.T) {
	root := testutil.FixtureRoot(t)
	in := readPageFixture(t, root, "pages", "empty-body.in.md")
	want := readPageFixture(t, root, "pages", "empty-body.want.md")

	p, err := ParsePage("x", in)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if p.Body != "" {
		t.Fatalf("Body = %q, want \"\"", p.Body)
	}

	got := p.Serialize()
	if !bytes.Equal(got, want) {
		t.Fatalf("Serialize() mismatch:\n got=%q\nwant=%q", got, want)
	}
	if !bytes.HasSuffix(got, []byte("---\n\n")) {
		t.Fatalf("Serialize() does not end \"---\\n\\n\": %q", lastBytes(string(got), 10))
	}
}

// The following property tests mirror TestRoundTrip in roundtrip_test.go
// (S1-T1), but exercise the real Page.Serialize/Canonical this subtask
// implements, rather than that test's ParseFrontmatter-only stand-in —
// page.go did not exist yet when roundtrip_test.go was written. Per this
// subtask's brief, the comparator here is a fresh, read-only copy: it never
// calls testutil.Golden, which would silently rewrite spec/fixtures/** under
// `go test ./... -update` (00-conventions.md §6, MASTER §9 D-X).

// TestCanonicalFixedPoint asserts Canonical(b) == b for every
// spec/fixtures/pages/*.want.md and every page under
// spec/fixtures/minimal/wiki/.
func TestCanonicalFixedPoint(t *testing.T) {
	root := testutil.FixtureRoot(t)

	t.Run("pages-want", func(t *testing.T) {
		pagesDir := filepath.Join(root, "pages")
		entries, err := os.ReadDir(pagesDir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", pagesDir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".want.md") {
				continue
			}
			t.Run(name, func(t *testing.T) {
				assertCanonicalFixedPoint(t, filepath.Join(pagesDir, name))
			})
		}
	})

	t.Run("minimal-wiki", func(t *testing.T) {
		wikiDir := filepath.Join(root, "minimal", "wiki")
		for _, path := range listMarkdown(t, wikiDir) {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				t.Fatalf("Rel: %v", err)
			}
			t.Run(filepath.ToSlash(rel), func(t *testing.T) {
				assertCanonicalFixedPoint(t, path)
			})
		}
	})
}

// assertCanonicalFixedPoint asserts Canonical(path's bytes) == path's bytes
// and that canonicalizing the result again changes nothing.
func assertCanonicalFixedPoint(t *testing.T, path string) {
	t.Helper()

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}

	got, err := Canonical(path, want)
	if err != nil {
		t.Fatalf("Canonical(%s): %v", path, err)
	}
	assertBytesMatchFile(t, path, got)
	assertCanonicalIdempotent(t, path, got)
}

// TestCanonicalInProducesWant asserts Canonical(<name>.in.md) ==
// <name>.want.md for every pair under spec/fixtures/pages/.
func TestCanonicalInProducesWant(t *testing.T) {
	root := testutil.FixtureRoot(t)
	pagesDir := filepath.Join(root, "pages")

	entries, err := os.ReadDir(pagesDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", pagesDir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if base, ok := strings.CutSuffix(e.Name(), ".in.md"); ok {
			names = append(names, base)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatalf("no *.in.md fixtures found under %s", pagesDir)
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			inPath := filepath.Join(pagesDir, name+".in.md")
			wantPath := filepath.Join(pagesDir, name+".want.md")

			in, err := os.ReadFile(inPath)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", inPath, err)
			}

			got, err := Canonical(inPath, in)
			if err != nil {
				t.Fatalf("Canonical(%s): %v", inPath, err)
			}
			assertBytesMatchFile(t, wantPath, got)
			assertCanonicalIdempotent(t, wantPath, got)
		})
	}
}

// noncanonicalTwin is one row of spec/fixtures/noncanonical/MAPPING.md,
// hand-transcribed because the table is prose documentation, not a
// machine-readable fixture list.
type noncanonicalTwin struct {
	noncanonical string
	canonical    string
}

var noncanonicalTwins = []noncanonicalTwin{
	{"noncanonical/wiki/concepts/speculative-decoding.md", "minimal/wiki/concepts/speculative-decoding.md"},
	{"noncanonical/wiki/concepts/kv-cache.md", "minimal/wiki/concepts/kv-cache.md"},
	{"noncanonical/wiki/concepts/flash-attention.md", "minimal/wiki/concepts/flash-attention.md"},
	{"noncanonical/wiki/entities/gpt-4.md", "minimal/wiki/entities/gpt-4.md"},
}

// TestCanonicalNoncanonicalTwin asserts Canonical(noncanonical file) == its
// canonical twin, for every pair in spec/fixtures/noncanonical/MAPPING.md.
func TestCanonicalNoncanonicalTwin(t *testing.T) {
	root := testutil.FixtureRoot(t)

	for _, pair := range noncanonicalTwins {
		t.Run(pair.noncanonical, func(t *testing.T) {
			ncPath := filepath.Join(root, filepath.FromSlash(pair.noncanonical))
			canonPath := filepath.Join(root, filepath.FromSlash(pair.canonical))

			nc, err := os.ReadFile(ncPath)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", ncPath, err)
			}

			got, err := Canonical(ncPath, nc)
			if err != nil {
				t.Fatalf("Canonical(%s): %v", ncPath, err)
			}
			assertBytesMatchFile(t, canonPath, got)
			assertCanonicalIdempotent(t, canonPath, got)
		})
	}
}

// assertCanonicalIdempotent asserts Canonical(canonical) == canonical.
func assertCanonicalIdempotent(t *testing.T, path string, canonical []byte) {
	t.Helper()
	twice, err := Canonical(path, canonical)
	if err != nil {
		t.Fatalf("Canonical(%s) on already-canonical input: %v", path, err)
	}
	if !bytes.Equal(twice, canonical) {
		t.Fatalf("Canonical is not idempotent for %s:\n first=%q\nsecond=%q", path, canonical, twice)
	}
}

// assertBytesMatchFile asserts got is byte-identical to the file at path.
// It only ever reads path — never testutil.Golden, which writes under
// `-update` and would corrupt a spec/fixtures/** ground-truth file
// (00-conventions.md §6, MASTER §9 D-X).
func assertBytesMatchFile(t *testing.T, path string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("mismatch against fixture %s:\n%s", path, pageByteDiff(want, got))
	}
}

// pageByteDiff renders a compact line-level diff between want and got.
func pageByteDiff(want, got []byte) string {
	const context = 3
	wantLines := strings.Split(string(want), "\n")
	gotLines := strings.Split(string(got), "\n")

	first := 0
	for first < len(wantLines) && first < len(gotLines) && wantLines[first] == gotLines[first] {
		first++
	}

	var b strings.Builder
	fmt.Fprintf(&b, "first difference at line %d\n", first+1)
	start := first - context
	if start < 0 {
		start = 0
	}
	line := func(lines []string, i int) string {
		if i < 0 || i >= len(lines) {
			return "<EOF>"
		}
		return lines[i]
	}
	for i := start; i < first; i++ {
		fmt.Fprintf(&b, "  %s\n", line(wantLines, i))
	}
	for i := first; i < min(first+context, len(wantLines)); i++ {
		fmt.Fprintf(&b, "-want: %s\n", line(wantLines, i))
	}
	for i := first; i < min(first+context, len(gotLines)); i++ {
		fmt.Fprintf(&b, "+got:  %s\n", line(gotLines, i))
	}
	return b.String()
}

// listMarkdown returns every *.md file under dir, sorted.
func listMarkdown(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s): %v", dir, err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatalf("no *.md files found under %s", dir)
	}
	return paths
}

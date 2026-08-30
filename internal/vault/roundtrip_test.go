package vault

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// roundTrip mimics S1-T3's Page.Serialize for property tests, since page.go
// (backbone §2.3) does not exist yet at this subtask. It applies §2.3's body
// rule directly here: a non-empty body ends with exactly one "\n"; an empty
// body stays "" (MASTER §9 D-U) — Serialize then ends "---\n\n" and nothing
// after.
func roundTrip(b []byte) ([]byte, error) {
	fm, body, err := ParseFrontmatter(b)
	if err != nil {
		return nil, err
	}
	body = normalizeBody(body)
	out := fm.Encode()
	out = append(out, '\n')
	out = append(out, body...)
	return out, nil
}

// normalizeBody applies the §2.3 body rule to a raw ParseFrontmatter body:
// a non-empty body always ends with exactly one "\n"; an empty body is left
// alone rather than growing a manufactured newline.
func normalizeBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	trimmed := bytes.TrimRight(body, "\n")
	if len(trimmed) == 0 {
		return nil
	}
	out := make([]byte, 0, len(trimmed)+1)
	out = append(out, trimmed...)
	out = append(out, '\n')
	return out
}

// TestRoundTrip is the property test at the heart of this subtask: for every
// canonical fixture, parsing and re-encoding reproduces it byte for byte,
// and for every non-canonical fixture, parsing and re-encoding reproduces
// its canonical twin byte for byte.
func TestRoundTrip(t *testing.T) {
	root := testutil.FixtureRoot(t)

	t.Run("want-is-fixed-point", func(t *testing.T) {
		testWantIsFixedPoint(t, root)
	})
	t.Run("minimal-wiki-is-fixed-point", func(t *testing.T) {
		testMinimalWikiIsFixedPoint(t, root)
	})
	t.Run("in-produces-want", func(t *testing.T) {
		testInProducesWant(t, root)
	})
	t.Run("noncanonical-produces-canonical-twin", func(t *testing.T) {
		testNoncanonicalProducesTwin(t, root)
	})
	t.Run("comparator-never-writes-fixtures", func(t *testing.T) {
		testComparatorNeverWritesFixtures(t, root)
	})
}

// testWantIsFixedPoint asserts roundTrip(b) == b for every
// spec/fixtures/pages/*.want.md.
func testWantIsFixedPoint(t *testing.T, root string) {
	t.Helper()

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
			checkFixedPoint(t, filepath.Join(pagesDir, name))
		})
	}
}

// testMinimalWikiIsFixedPoint asserts roundTrip(b) == b for every *.md under
// spec/fixtures/minimal/wiki/.
func testMinimalWikiIsFixedPoint(t *testing.T, root string) {
	t.Helper()

	wikiDir := filepath.Join(root, "minimal", "wiki")
	paths := collectMarkdownFiles(t, wikiDir)

	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("Rel(%s, %s): %v", root, path, err)
		}
		t.Run(filepath.ToSlash(rel), func(t *testing.T) {
			checkFixedPoint(t, path)
		})
	}
}

// checkFixedPoint asserts roundTrip(file) == file's own bytes, that
// re-parsing the output deep-equals the first parse, and that encoding
// twice changes nothing.
func checkFixedPoint(t *testing.T, path string) {
	t.Helper()

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}

	got, err := roundTrip(want)
	if err != nil {
		t.Fatalf("roundTrip(%s): %v", path, err)
	}
	checkFixtureMatches(t, path, got)

	checkDeepEqualReparse(t, want, got)
	checkIdempotent(t, got)
}

// testInProducesWant asserts roundTrip(in) == want for every
// spec/fixtures/pages/<name>.in.md / <name>.want.md pair.
func testInProducesWant(t *testing.T, root string) {
	t.Helper()

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
			want, err := os.ReadFile(wantPath)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", wantPath, err)
			}

			got, err := roundTrip(in)
			if err != nil {
				t.Fatalf("roundTrip(%s): %v", inPath, err)
			}
			checkFixtureMatches(t, wantPath, got)

			checkDeepEqualReparse(t, want, got)
			checkIdempotent(t, got)
		})
	}
}

// noncanonicalPair is one row of spec/fixtures/noncanonical/MAPPING.md.
type noncanonicalPair struct {
	noncanonical string
	canonical    string
}

// noncanonicalPairs mirrors spec/fixtures/noncanonical/MAPPING.md exactly.
// It is hand-transcribed rather than parsed from the markdown table because
// the table is prose documentation, not a machine-readable fixture list.
var noncanonicalPairs = []noncanonicalPair{
	{
		noncanonical: "noncanonical/wiki/concepts/speculative-decoding.md",
		canonical:    "minimal/wiki/concepts/speculative-decoding.md",
	},
	{
		noncanonical: "noncanonical/wiki/concepts/kv-cache.md",
		canonical:    "minimal/wiki/concepts/kv-cache.md",
	},
	{
		noncanonical: "noncanonical/wiki/concepts/flash-attention.md",
		canonical:    "minimal/wiki/concepts/flash-attention.md",
	},
	{
		noncanonical: "noncanonical/wiki/entities/gpt-4.md",
		canonical:    "minimal/wiki/entities/gpt-4.md",
	},
}

// testNoncanonicalProducesTwin asserts roundTrip(noncanonical file) == its
// canonical twin, for every pair in spec/fixtures/noncanonical/MAPPING.md.
func testNoncanonicalProducesTwin(t *testing.T, root string) {
	t.Helper()

	for _, pair := range noncanonicalPairs {
		t.Run(pair.noncanonical, func(t *testing.T) {
			ncPath := filepath.Join(root, filepath.FromSlash(pair.noncanonical))
			canonPath := filepath.Join(root, filepath.FromSlash(pair.canonical))

			nc, err := os.ReadFile(ncPath)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", ncPath, err)
			}
			want, err := os.ReadFile(canonPath)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", canonPath, err)
			}

			got, err := roundTrip(nc)
			if err != nil {
				t.Fatalf("roundTrip(%s): %v", ncPath, err)
			}
			checkFixtureMatches(t, canonPath, got)

			checkDeepEqualReparse(t, want, got)
			checkIdempotent(t, got)
		})
	}
}

// checkDeepEqualReparse asserts that re-parsing got deep-equals parsing
// want — including the nil-vs-empty distinction on Tags/Sources — which is
// the property that makes byte-stability meaningful rather than
// coincidental.
func checkDeepEqualReparse(t *testing.T, want, got []byte) {
	t.Helper()

	wantFM, wantBody, err := ParseFrontmatter(want)
	if err != nil {
		t.Fatalf("ParseFrontmatter(want): %v", err)
	}
	gotFM, gotBody, err := ParseFrontmatter(got)
	if err != nil {
		t.Fatalf("ParseFrontmatter(got): %v", err)
	}

	if !reflect.DeepEqual(wantFM, gotFM) {
		t.Fatalf("Frontmatter mismatch after round-trip:\n want=%#v\n  got=%#v", wantFM, gotFM)
	}
	if !bytes.Equal(wantBody, gotBody) {
		t.Fatalf("body mismatch after round-trip:\n want=%q\n  got=%q", wantBody, gotBody)
	}
}

// checkIdempotent asserts that encoding an already-canonical file a second
// time changes nothing.
func checkIdempotent(t *testing.T, canonical []byte) {
	t.Helper()

	twice, err := roundTrip(canonical)
	if err != nil {
		t.Fatalf("roundTrip(canonical) on already-canonical input: %v", err)
	}
	if !bytes.Equal(twice, canonical) {
		t.Fatalf("Encode is not idempotent:\n first=%q\nsecond=%q", canonical, twice)
	}
}

// checkFixtureMatches asserts that got is byte-identical to the fixture
// file at path.
//
// This exists instead of reaching for the shared testutil comparison
// helper, which doubles as a rewriter under the sanctioned
// `go test ./... -update` (00-conventions.md §6). spec/fixtures/** is
// hand-authored ground truth (S0-T3), not test output, and this subtask's
// non-goals forbid touching it — using the writing helper here once
// silently rewrote spec/fixtures/pages/empty-body.want.md under -update,
// stripping the trailing blank line that pins the empty-body rule
// (MASTER §9 D-U). checkFixtureMatches only ever reads path; see
// TestRoundTrip/comparator-never-writes-fixtures for the regression test.
func checkFixtureMatches(t *testing.T, path string, got []byte) {
	t.Helper()

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if bytes.Equal(want, got) {
		return
	}
	t.Errorf("round-trip output does not match fixture %s:\n%s", path, byteDiff(want, got))
}

// byteDiff renders a compact line-level diff between want and got: the
// first differing line, plus up to three lines of context on each side.
func byteDiff(want, got []byte) string {
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
	for i := start; i < first; i++ {
		fmt.Fprintf(&b, "  %s\n", lineOrEOF(wantLines, i))
	}
	for i := first; i < min(first+context, len(wantLines)); i++ {
		fmt.Fprintf(&b, "-want: %s\n", lineOrEOF(wantLines, i))
	}
	for i := first; i < min(first+context, len(gotLines)); i++ {
		fmt.Fprintf(&b, "+got:  %s\n", lineOrEOF(gotLines, i))
	}
	return b.String()
}

// lineOrEOF returns lines[i], or "<EOF>" when i runs past the end — one
// side of a diff can run out of lines before the other.
func lineOrEOF(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return "<EOF>"
	}
	return lines[i]
}

// testComparatorNeverWritesFixtures is the regression test for the defect
// fixed in repair-1 (see checkFixtureMatches's doc comment): it proves the
// comparator every other TestRoundTrip subtest uses only ever reads its
// fixture, by capturing the fixture's bytes and mtime, running the
// comparator against its own unchanged content, and asserting both are
// identical afterward.
func testComparatorNeverWritesFixtures(t *testing.T, root string) {
	t.Helper()

	// Any fixture file works for this; empty-body.want.md is the one the
	// defect actually corrupted, so it doubles as documentation.
	path := filepath.Join(root, "pages", "empty-body.want.md")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}

	// Feed the fixture its own bytes back: the comparison must succeed, so
	// any observed change can only be a side effect of the comparator
	// itself, not a legitimate mismatch report.
	checkFixtureMatches(t, path, before)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("checkFixtureMatches modified %s:\n before=%q\n  after=%q", path, before, after)
	}

	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatalf("checkFixtureMatches touched %s's mtime: before=%v after=%v", path, beforeInfo.ModTime(), afterInfo.ModTime())
	}
}

// collectMarkdownFiles returns every *.md file under dir, sorted.
func collectMarkdownFiles(t *testing.T, dir string) []string {
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

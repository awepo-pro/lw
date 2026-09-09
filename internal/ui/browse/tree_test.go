package browse

import (
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestBuildTreeOrderedNodeSet is s4-tui.md S4-T4's pinned expectation: "tree
// construction over spec/fixtures/minimal yields the expected node set" —
// the exact ordered node paths, as a table, not a count.
func TestBuildTreeOrderedNodeSet(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	v := engine.Vault()

	var pagePaths, rawPaths []string
	for _, p := range v.Pages() {
		pagePaths = append(pagePaths, p.Path)
	}
	for _, r := range v.RawSources() {
		rawPaths = append(rawPaths, r.Path)
	}

	got := allPaths(buildTree(pagePaths, rawPaths))
	want := []string{
		"raw",
		"raw/articles",
		"raw/articles/kv-cache-explained.md",
		"raw/papers",
		"raw/papers/leviathan-2023.md",
		"wiki",
		"wiki/concepts",
		"wiki/concepts/flash-attention.md",
		"wiki/concepts/kv-cache.md",
		"wiki/concepts/speculative-decoding.md",
		"wiki/entities",
		"wiki/entities/gpt-4.md",
	}

	if len(got) != len(want) {
		t.Fatalf("allPaths returned %d nodes, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("node %d = %q, want %q\nfull got: %v", i, got[i], want[i], got)
		}
	}
}

// TestBuildTreeIsPureFunction proves buildTree needs no *vault.Vault at all
// (s4-tui.md S4-T4 item 3): two synthetic path slices, no fixture, no engine.
// It also pins the sort rule that a directory sorts before a file even when
// the file's name is lexicographically earlier.
func TestBuildTreeIsPureFunction(t *testing.T) {
	got := allPaths(buildTree(
		[]string{"wiki/aaa.md", "wiki/zzz/nested.md"},
		nil,
	))
	want := []string{
		"raw",
		"wiki",
		"wiki/zzz",
		"wiki/zzz/nested.md",
		"wiki/aaa.md",
	}
	if len(got) != len(want) {
		t.Fatalf("allPaths = %v (%d nodes), want %v (%d nodes)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("node %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestBuildTreeBothRootsAlwaysPresent: an empty raw side still yields a
// "raw" root node (s4-tui.md S4-T4 item 3: "two roots, raw then wiki").
func TestBuildTreeBothRootsAlwaysPresent(t *testing.T) {
	roots := buildTree(nil, nil)
	if len(roots) != 2 {
		t.Fatalf("buildTree(nil, nil) returned %d roots, want 2", len(roots))
	}
	if roots[0].Path != "raw" || roots[1].Path != "wiki" {
		t.Fatalf("buildTree(nil, nil) roots = [%q, %q], want [raw, wiki]", roots[0].Path, roots[1].Path)
	}
	if len(roots[0].Children) != 0 || len(roots[1].Children) != 0 {
		t.Fatalf("empty roots have children: raw=%v wiki=%v", roots[0].Children, roots[1].Children)
	}
}

// TestAncestorDirs pins the ancestor list ancestorDirs returns, used to force
// a finder match's parents open (s4-tui.md S4-T4 item 10).
func TestAncestorDirs(t *testing.T) {
	tests := []struct {
		path string
		want []string
	}{
		{"wiki/concepts/kv-cache.md", []string{"wiki", "wiki/concepts"}},
		{"raw/papers/leviathan-2023.md", []string{"raw", "raw/papers"}},
		{"wiki", nil},
	}
	for _, tt := range tests {
		got := ancestorDirs(tt.path)
		if len(got) != len(tt.want) {
			t.Errorf("ancestorDirs(%q) = %v, want %v", tt.path, got, tt.want)
			continue
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Errorf("ancestorDirs(%q)[%d] = %q, want %q", tt.path, i, got[i], tt.want[i])
			}
		}
	}
}

// TestParentPath pins parentPath's root case: a root node has no parent.
func TestParentPath(t *testing.T) {
	tests := []struct{ path, want string }{
		{"wiki/concepts/kv-cache.md", "wiki/concepts"},
		{"wiki/concepts", "wiki"},
		{"wiki", ""},
		{"raw", ""},
	}
	for _, tt := range tests {
		if got := parentPath(tt.path); got != tt.want {
			t.Errorf("parentPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// TestVisibleNodesRespectsExpanded: a directory's children are hidden until
// its own path is in the expanded set.
func TestVisibleNodesRespectsExpanded(t *testing.T) {
	roots := buildTree([]string{"wiki/concepts/a.md", "wiki/concepts/b.md"}, nil)

	allCollapsed := map[string]bool{}
	got := visibleNodes(roots, allCollapsed)
	if len(got) != 2 {
		t.Fatalf("with nothing expanded, visibleNodes returned %d nodes (want just the 2 roots): %v", len(got), pathsOf(got))
	}

	expanded := defaultExpanded(roots)
	got = visibleNodes(roots, expanded)
	want := []string{"raw", "wiki", "wiki/concepts", "wiki/concepts/a.md", "wiki/concepts/b.md"}
	if p := pathsOf(got); !equalStrings(p, want) {
		t.Fatalf("with everything expanded, visibleNodes = %v, want %v", p, want)
	}
}

func pathsOf(nodes []*treeNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Path
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSubsequenceMatch is a table test of the pure matcher fuzzyFind is
// built on.
func TestSubsequenceMatch(t *testing.T) {
	tests := []struct {
		s, pattern  string
		wantOK      bool
		first, last int
	}{
		{"kv-cache.md", "kv", true, 0, 1},
		{"kv-cache.md", "kvc", true, 0, 3},
		{"kv-cache.md", "cache", true, 3, 7},
		{"kv-cache.md", "zzz", false, 0, 0},
		{"", "k", false, 0, 0},
		{"kv-cache.md", "", false, 0, 0},
	}
	for _, tt := range tests {
		first, last, ok := subsequenceMatch(tt.s, tt.pattern)
		if ok != tt.wantOK {
			t.Errorf("subsequenceMatch(%q, %q) ok = %v, want %v", tt.s, tt.pattern, ok, tt.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if first != tt.first || last != tt.last {
			t.Errorf("subsequenceMatch(%q, %q) = (%d, %d), want (%d, %d)", tt.s, tt.pattern, first, last, tt.first, tt.last)
		}
	}
}

// TestMatchPathTiers pins matchPath's tier assignment: a match confined to
// the base name is tier 0; one that needs directory segments is tier 1
// (s4-tui.md S4-T4 item 10 rule 1).
func TestMatchPathTiers(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		path     string
		wantTier int
		wantOK   bool
	}{
		{"basename match", "kv", "wiki/concepts/kv-cache.md", 0, true},
		{"directory-only match", "concepts", "wiki/concepts/flash-attention.md", 1, true},
		{"no match at all", "zzz", "wiki/concepts/flash-attention.md", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier, _, _, ok := matchPath(tt.query, tt.path)
			if ok != tt.wantOK {
				t.Fatalf("matchPath(%q, %q) ok = %v, want %v", tt.query, tt.path, ok, tt.wantOK)
			}
			if ok && tier != tt.wantTier {
				t.Fatalf("matchPath(%q, %q) tier = %d, want %d", tt.query, tt.path, tier, tt.wantTier)
			}
		})
	}
}

// TestFuzzyFindKVRanksWikiPageFirst is s4-tui.md S4-T4's pinned expectation:
// "fuzzy find for kv returns wiki/concepts/kv-cache.md first (ahead of
// raw/articles/kv-cache-explained.md)". Both match tier 0 (their base names
// both start "kv"), with the same span and first-match index, so this is a
// direct test of rule 4: shorter full path wins.
func TestFuzzyFindKVRanksWikiPageFirst(t *testing.T) {
	items := []findItem{
		{path: "raw/articles/kv-cache-explained.md", title: "KV Cache, Explained"},
		{path: "wiki/concepts/kv-cache.md", title: "KV Cache"},
		{path: "wiki/concepts/flash-attention.md", title: "Flash Attention"},
	}

	got := fuzzyFind("kv", items)
	if len(got) < 2 {
		t.Fatalf("fuzzyFind(\"kv\") returned %d matches, want at least 2: %v", len(got), got)
	}
	if got[0].path != "wiki/concepts/kv-cache.md" {
		t.Fatalf("fuzzyFind(\"kv\")[0] = %q, want wiki/concepts/kv-cache.md\nfull results: %v", got[0].path, got)
	}
	if got[1].path != "raw/articles/kv-cache-explained.md" {
		t.Fatalf("fuzzyFind(\"kv\")[1] = %q, want raw/articles/kv-cache-explained.md\nfull results: %v", got[1].path, got)
	}
}

// TestFuzzyFindMatchesTitleWhenPathDoesNot proves item 10's "path and its
// FM.Title" clause: a query that appears only in the title, nowhere in the
// path, still matches (tier 2, the weakest).
func TestFuzzyFindMatchesTitleWhenPathDoesNot(t *testing.T) {
	items := []findItem{
		{path: "wiki/concepts/other.md", title: "Zephyr Notes"},
	}
	got := fuzzyFind("zep", items)
	if len(got) != 1 || got[0].path != "wiki/concepts/other.md" {
		t.Fatalf("fuzzyFind(\"zep\") = %v, want a single title-only match on wiki/concepts/other.md", got)
	}
}

// TestFuzzyFindEmptyQueryMatchesNothing: there is no useful ranking of
// "everything", so an empty query returns no results.
func TestFuzzyFindEmptyQueryMatchesNothing(t *testing.T) {
	items := []findItem{{path: "wiki/concepts/kv-cache.md", title: "KV Cache"}}
	if got := fuzzyFind("", items); got != nil {
		t.Fatalf("fuzzyFind(\"\") = %v, want nil", got)
	}
}

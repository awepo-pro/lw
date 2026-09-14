// tree.go builds and walks the browse screen's vault tree: two roots, "raw"
// and "wiki" (/docs/design.md §9's sketch), populated from the vault-relative paths
// backbone §2.8's Vault.Pages() and Vault.RawSources() return.
//
// buildTree is a pure function of two path slices — no *vault.Vault involved
// — so tree construction is unit-testable without a model (s4-tui.md S4-T4
// item 3).
package browse

import (
	"path"
	"sort"
	"strings"
)

// nodeKind identifies what a treeNode represents.
type nodeKind int

const (
	nodeDir nodeKind = iota
	nodePage
	nodeRawSource
)

// treeNode is one entry in the browse tree: a directory (including the two
// roots) or a leaf page/raw source. Path is the node's vault-relative path —
// for a leaf this is the exact path a *vault.Vault indexes it under; for a
// directory it is the path prefix every descendant shares, e.g.
// "wiki/concepts". Depth, for both indentation and ancestor-walking, is
// simply strings.Count(Path, "/") — every path is vault-relative and
// slash-separated (00-conventions.md §3), so no separate field is needed.
type treeNode struct {
	Path     string
	Name     string
	Kind     nodeKind
	Children []*treeNode // nil for a leaf
}

// IsDir reports whether n is a directory node (including the two roots).
func (n *treeNode) IsDir() bool { return n.Kind == nodeDir }

// buildTree builds the two-root vault tree (backbone §2.8/§2.9, s4-tui.md
// S4-T4 item 3) from the vault-relative paths Vault.Pages() and
// Vault.RawSources() return.
//
// Contract: two roots, "raw" then "wiki", always both present even when one
// side is empty — the browse screen's layout is a fixed shape, not a function
// of vault contents. Children are sorted lexicographically, directories
// before files, at every level.
func buildTree(pagePaths, rawPaths []string) []*treeNode {
	root := newTrieNode()
	for _, p := range rawPaths {
		insertPath(root, p, nodeRawSource)
	}
	for _, p := range pagePaths {
		insertPath(root, p, nodePage)
	}

	roots := [2]string{"raw", "wiki"}
	out := make([]*treeNode, 0, len(roots))
	for _, name := range roots {
		child, ok := root.children[name]
		if !ok {
			out = append(out, &treeNode{Path: name, Name: name, Kind: nodeDir})
			continue
		}
		out = append(out, trieToNode(name, name, child))
	}
	return out
}

// trieNode is buildTree's scratch structure while paths are being inserted;
// it is discarded once trieToNode converts it into the treeNode tree callers
// see.
type trieNode struct {
	children map[string]*trieNode
	isLeaf   bool
	kind     nodeKind
}

func newTrieNode() *trieNode { return &trieNode{children: map[string]*trieNode{}} }

// insertPath walks p's slash-separated segments into root, creating an
// intermediate trieNode per segment and marking the final one as a leaf of
// kind.
func insertPath(root *trieNode, p string, kind nodeKind) {
	segs := strings.Split(p, "/")
	cur := root
	for i, seg := range segs {
		child, ok := cur.children[seg]
		if !ok {
			child = newTrieNode()
			cur.children[seg] = child
		}
		if i == len(segs)-1 {
			child.isLeaf = true
			child.kind = kind
		}
		cur = child
	}
}

// trieToNode converts one trie subtree rooted at t — named name, at
// nodePath — into a treeNode, sorting children directories-before-files and
// lexicographically within each group (s4-tui.md S4-T4 item 3).
func trieToNode(name, nodePath string, t *trieNode) *treeNode {
	if t.isLeaf && len(t.children) == 0 {
		return &treeNode{Path: nodePath, Name: name, Kind: t.kind}
	}

	var dirNames, fileNames []string
	for childName, child := range t.children {
		if child.isLeaf && len(child.children) == 0 {
			fileNames = append(fileNames, childName)
		} else {
			dirNames = append(dirNames, childName)
		}
	}
	sort.Strings(dirNames)
	sort.Strings(fileNames)

	children := make([]*treeNode, 0, len(dirNames)+len(fileNames))
	for _, childName := range dirNames {
		children = append(children, trieToNode(childName, nodePath+"/"+childName, t.children[childName]))
	}
	for _, childName := range fileNames {
		children = append(children, trieToNode(childName, nodePath+"/"+childName, t.children[childName]))
	}

	return &treeNode{Path: nodePath, Name: name, Kind: nodeDir, Children: children}
}

// allPaths flattens roots into every node's Path, in the same
// directories-before-files, lexicographic pre-order buildTree already
// produces. It exists so a test can assert the exact ordered node set a tree
// construction yields, as a table, rather than a count.
func allPaths(roots []*treeNode) []string {
	var out []string
	var walk func(n *treeNode)
	walk = func(n *treeNode) {
		out = append(out, n.Path)
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

// defaultExpanded returns every directory path in roots (including the two
// roots themselves) mapped to true — the browse screen's initial state shows
// the whole tree, since a personal wiki is small enough that this is more
// useful than starting collapsed. h/l and enter (s4-tui.md S4-T4 item 4)
// collapse or expand individual directories from there.
func defaultExpanded(roots []*treeNode) map[string]bool {
	m := map[string]bool{}
	var walk func(n *treeNode)
	walk = func(n *treeNode) {
		if !n.IsDir() {
			return
		}
		m[n.Path] = true
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return m
}

// visibleNodes flattens roots into the nodes currently shown, given
// expanded's set of open directory paths: a directory's children are walked
// only when expanded reports it open. Root nodes are themselves subject to
// expanded, same as any other directory.
func visibleNodes(roots []*treeNode, expanded map[string]bool) []*treeNode {
	var out []*treeNode
	var walk func(n *treeNode)
	walk = func(n *treeNode) {
		out = append(out, n)
		if n.IsDir() && expanded[n.Path] {
			for _, c := range n.Children {
				walk(c)
			}
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

// ancestorDirs returns every directory path on the way to p, shallowest
// first — e.g. "wiki", "wiki/concepts" for p == "wiki/concepts/kv-cache.md".
// Used to force a finder match's ancestors open so the tree cursor can land
// on it (s4-tui.md S4-T4 item 10).
func ancestorDirs(p string) []string {
	segs := strings.Split(p, "/")
	var out []string
	for i := 1; i < len(segs); i++ {
		out = append(out, strings.Join(segs[:i], "/"))
	}
	return out
}

// parentPath returns p's parent directory path, or "" when p is already a
// root ("raw" or "wiki") with nothing above it.
func parentPath(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

// findItem is one candidate the `/` finder searches over: a page or raw
// source's path and display title (Frontmatter.Title for a page,
// RawSource.Title for a raw source).
type findItem struct {
	path  string
	title string
}

// findMatch is one fuzzyFind result.
type findMatch struct {
	path  string
	title string
}

// fuzzyFind returns every item in items whose path or title contains query
// as a case-insensitive subsequence, ranked per s4-tui.md S4-T4 item 10:
//  1. a match inside the base name beats a match only in directory segments
//     beats a match found only in the title, not the path at all;
//  2. smaller match span (last index − first index);
//  3. earlier first-match index;
//  4. shorter full path;
//  5. lexicographic path.
//
// An empty query matches nothing — there is no useful ranking of "everything".
func fuzzyFind(query string, items []findItem) []findMatch {
	if query == "" {
		return nil
	}
	q := strings.ToLower(query)

	type ranked struct {
		match findMatch
		tier  int
		span  int
		first int
	}
	var out []ranked
	for _, it := range items {
		tier, span, first, ok := matchPath(q, it.path)
		if !ok {
			tf, tl, tok := subsequenceMatch(strings.ToLower(it.title), q)
			if !tok {
				continue
			}
			tier, span, first = 2, tl-tf, tf
		}
		out = append(out, ranked{match: findMatch{path: it.path, title: it.title}, tier: tier, span: span, first: first})
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.span != b.span {
			return a.span < b.span
		}
		if a.first != b.first {
			return a.first < b.first
		}
		if len(a.match.path) != len(b.match.path) {
			return len(a.match.path) < len(b.match.path)
		}
		return a.match.path < b.match.path
	})

	matches := make([]findMatch, len(out))
	for i, r := range out {
		matches[i] = r.match
	}
	return matches
}

// matchPath reports how query matches p as a subsequence: tier 0 when the
// match is found entirely within p's base name, tier 1 when it requires
// directory segments too. span and first are the match's index span and
// first index, within whichever substring (base name or full path) produced
// the tier-0/tier-1 match.
func matchPath(query, p string) (tier, span, first int, ok bool) {
	base := path.Base(p)
	if f, l, mok := subsequenceMatch(strings.ToLower(base), query); mok {
		return 0, l - f, f, true
	}
	if f, l, mok := subsequenceMatch(strings.ToLower(p), query); mok {
		return 1, l - f, f, true
	}
	return 0, 0, 0, false
}

// subsequenceMatch reports whether pattern's runes occur in s in order (not
// necessarily contiguously), via the leftmost greedy match. first and last
// are the rune indices, into s, of the first and last characters matched.
func subsequenceMatch(s, pattern string) (first, last int, ok bool) {
	if pattern == "" {
		return 0, 0, false
	}
	sr := []rune(s)
	pr := []rune(pattern)

	first = -1
	si := 0
	for _, pc := range pr {
		found := false
		for ; si < len(sr); si++ {
			if sr[si] == pc {
				if first == -1 {
					first = si
				}
				last = si
				si++
				found = true
				break
			}
		}
		if !found {
			return 0, 0, false
		}
	}
	return first, last, true
}

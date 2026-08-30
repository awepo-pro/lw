package vault

import (
	"path"
	"sort"
	"strings"
)

// Ref is one directed wikilink edge.
type Ref struct {
	From    string // page path
	To      string // resolved page path, or "" when unresolved
	Raw     string // the target as written
	Line    int
	Context string // the source line, trimmed, for display
}

// Graph is the wikilink graph built over a Vault's pages.
type Graph struct {
	pages     []string         // every page path, sorted — the graph's node set
	outbound  map[string][]Ref // page -> its outbound refs, in body order
	backlinks map[string][]Ref // page -> refs pointing at it, grouped by From (sorted), then by Line
}

// BuildGraph builds the wikilink graph for v.
//
// Contract (backbone §2.9, S1 correction C-7): walks Vault.Pages() only.
// index.md, log.md, SCHEMA.md and curator-memory.md are not pages (§2.8
// loads only wiki/ and raw/), so their [[links]] contribute no edges and
// cannot give a page an inbound ref.
func BuildGraph(v *Vault) *Graph {
	pages := v.Pages() // already sorted by Path

	g := &Graph{
		pages:     make([]string, 0, len(pages)),
		outbound:  map[string][]Ref{},
		backlinks: map[string][]Ref{},
	}
	for _, p := range pages {
		g.pages = append(g.pages, p.Path)
	}

	for _, p := range pages {
		for _, link := range p.Links {
			to, _ := Resolve(v, link.Target)
			ref := Ref{
				From:    p.Path,
				To:      to,
				Raw:     link.Target,
				Line:    link.Line,
				Context: lineContext(p.Body, link.Line),
			}
			g.outbound[p.Path] = append(g.outbound[p.Path], ref)
			if to != "" {
				g.backlinks[to] = append(g.backlinks[to], ref)
			}
		}
	}
	return g
}

// lineContext returns body's line-th line (1-based), trimmed of leading and
// trailing whitespace, or "" if line is out of range.
func lineContext(body string, line int) string {
	lines := strings.Split(body, "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

// Outbound returns every wikilink ref originating at page, in body order.
func (g *Graph) Outbound(page string) []Ref {
	return append([]Ref(nil), g.outbound[page]...)
}

// Backlinks returns every wikilink ref that resolves to page, grouped by
// the linking page's path (ascending), then by line within that page.
func (g *Graph) Backlinks(page string) []Ref {
	return append([]Ref(nil), g.backlinks[page]...)
}

// Neighbors returns every page reachable from page within depth hops,
// sorted and excluding page itself. A hop follows a resolved wikilink edge
// in either direction: page A is adjacent to page B if A links to B or B
// links to A. depth <= 0 yields an empty slice.
func (g *Graph) Neighbors(page string, depth int) []string {
	visited := map[string]bool{page: true}
	frontier := []string{page}

	for d := 0; d < depth; d++ {
		var next []string
		for _, p := range frontier {
			for _, adj := range g.adjacent(p) {
				if !visited[adj] {
					visited[adj] = true
					next = append(next, adj)
				}
			}
		}
		frontier = next
	}

	delete(visited, page)
	out := make([]string, 0, len(visited))
	for p := range visited {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// adjacent returns every page directly connected to page by a resolved
// wikilink edge, in either direction, with duplicates possible (callers
// deduplicate via a visited set).
func (g *Graph) adjacent(page string) []string {
	var out []string
	for _, ref := range g.outbound[page] {
		if ref.To != "" {
			out = append(out, ref.To)
		}
	}
	for _, ref := range g.backlinks[page] {
		out = append(out, ref.From)
	}
	return out
}

// Broken returns every ref whose target did not resolve (To == ""), sorted
// by the linking page's path, then by line within that page.
func (g *Graph) Broken() []Ref {
	var out []Ref
	for _, p := range g.pages {
		for _, ref := range g.outbound[p] {
			if ref.To == "" {
				out = append(out, ref)
			}
		}
	}
	return out
}

// Orphans returns every page with no inbound refs, sorted.
func (g *Graph) Orphans() []string {
	var out []string
	for _, p := range g.pages {
		if len(g.backlinks[p]) == 0 {
			out = append(out, p)
		}
	}
	return out
}

// Resolve attempts to resolve target — a wikilink's Target, fragment
// already excluded (backbone §2.5, MASTER §9 D-Y) — to a page path in v.
//
// Contract (backbone §2.9): tried in order — exact vault-relative path;
// "<target>.md"; basename match against every page (unique match only,
// case-insensitive); "<dir>/<target>.md" for each wiki/* subdirectory.
// Ambiguous at any matching step means that step fails, not that Resolve
// returns a wrong answer.
func Resolve(v *Vault, target string) (string, bool) {
	if target == "" {
		return "", false
	}
	if _, ok := v.Page(target); ok {
		return target, true
	}
	if p, ok := resolveWithMDSuffix(v, target); ok {
		return p, true
	}
	if p, ok := resolveByBasename(v, target); ok {
		return p, true
	}
	if p, ok := resolveByDir(v, target); ok {
		return p, true
	}
	return "", false
}

// resolveWithMDSuffix tries target+".md" as an exact vault-relative path.
func resolveWithMDSuffix(v *Vault, target string) (string, bool) {
	if strings.HasSuffix(target, ".md") {
		return "", false
	}
	candidate := target + ".md"
	if _, ok := v.Page(candidate); ok {
		return candidate, true
	}
	return "", false
}

// resolveByBasename matches target, case-insensitively and with any ".md"
// suffix stripped, against every page's basename. Ambiguous (more than one
// match) resolves to nothing, same as no match.
func resolveByBasename(v *Vault, target string) (string, bool) {
	want := strings.ToLower(strings.TrimSuffix(target, ".md"))

	var matches []string
	for _, p := range v.Pages() {
		base := strings.TrimSuffix(path.Base(p.Path), ".md")
		if strings.ToLower(base) == want {
			matches = append(matches, p.Path)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return "", false
}

// resolveByDir tries "<dir>/<target>.md" for each directory that currently
// holds at least one page under wiki/. Ambiguous (more than one directory
// matching) resolves to nothing.
func resolveByDir(v *Vault, target string) (string, bool) {
	t := strings.TrimSuffix(target, ".md")

	var matches []string
	for _, dir := range wikiDirs(v) {
		candidate := dir + "/" + t + ".md"
		if _, ok := v.Page(candidate); ok {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return "", false
}

// wikiDirs returns the distinct immediate parent directories of every page
// in v, sorted — e.g. ["wiki/concepts", "wiki/entities"].
func wikiDirs(v *Vault) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, p := range v.Pages() {
		d := path.Dir(p.Path)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	return dirs
}

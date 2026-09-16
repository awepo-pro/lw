// links.go draws the Links panel's rows (mockgen.links_lines,
// s2-screens.md T07): the pages that link to the selection, the selection's
// outbound wikilinks in body order, and its frontmatter sources — one
// `Name  N` header (bold name, faint count), one `  item` row per entry,
// and a blank row between sections.
package browse

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// linksLines builds the Links panel's content lines for the node under the
// cursor. A directory (or a missing vault) has no links; it says so instead
// of drawing three empty sections.
func (m *Model) linksLines() []string {
	n := m.selectedNode()
	if n == nil || n.IsDir() || m.deps.Engine == nil {
		return []string{m.deps.Theme.Faint.Render("(select a page to inspect links)")}
	}

	backlinks := m.backlinkSlugs(n.Path)
	outbound := m.outboundTargets(n)
	sources := m.frontmatterSources(n)

	var out []string
	section := func(name string, items []string) {
		out = append(out,
			m.deps.Theme.Bold.Render(name)+m.deps.Theme.Faint.Render(fmt.Sprintf("  %d", len(items))))
		for _, it := range items {
			out = append(out, m.deps.Theme.Fg.Render("  "+it))
		}
		out = append(out, "")
	}
	section("Backlinks", backlinks)
	section("Links out", outbound)
	section("Sources", sources)
	return out
}

// backlinkSlugs returns the base names, without `.md`, of the pages that
// link to path — one row per linking page, sorted by slug
// (mockgen.links_lines sorts the slugs, not the paths).
func (m *Model) backlinkSlugs(path string) []string {
	seen := map[string]bool{}
	var slugs []string
	for _, ref := range m.deps.Engine.Vault().Graph().Backlinks(path) {
		if seen[ref.From] {
			continue
		}
		seen[ref.From] = true
		slugs = append(slugs, strings.TrimSuffix(filepath.Base(ref.From), ".md"))
	}
	sort.Strings(slugs)
	return slugs
}

// outboundTargets returns the selection's outbound wikilink targets in body
// order, de-duplicated — the graph's own view of the body, so links inside
// code spans and fences (which the parser skips) count no more than they
// link.
func (m *Model) outboundTargets(n *treeNode) []string {
	seen := map[string]bool{}
	var out []string
	for _, ref := range m.deps.Engine.Vault().Graph().Outbound(n.Path) {
		if ref.Raw == "" || seen[ref.Raw] {
			continue
		}
		seen[ref.Raw] = true
		out = append(out, ref.Raw)
	}
	return out
}

// frontmatterSources returns the selected page's `sources` list as written.
// A raw source carries no sources key, so its list is empty.
func (m *Model) frontmatterSources(n *treeNode) []string {
	if n.Kind != nodePage {
		return nil
	}
	p, ok := m.deps.Engine.Vault().Page(n.Path)
	if !ok {
		return nil
	}
	return p.FM.Sources
}

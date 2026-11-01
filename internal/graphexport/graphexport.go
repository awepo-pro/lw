// Package graphexport builds, filters, and exports wikilink graphs to standard
// formats (Graphviz DOT, JSON, Mermaid) and text summaries.
package graphexport

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// Node represents one wiki page in the exported graph.
// In and Out measure the number of distinct resolved neighbor pages
// linking in or linked out, with self-links ignored.
type Node struct {
	Path  string   `json:"id"`
	Title string   `json:"title"`
	Type  string   `json:"type"`
	Tags  []string `json:"tags"`
	In    int      `json:"in"`
	Out   int      `json:"out"`
}

// Edge represents one directed wikilink connection between two pages.
// Edges are deduplicated per (From, To) pair with Count recording the number
// of links. Self-links are dropped.
type Edge struct {
	From  string `json:"source"`
	To    string `json:"target"`
	Count int    `json:"count"`
}

// Graph is the exported wikilink graph. Nodes are sorted by Path ascending,
// and Edges are sorted by From then To ascending.
type Graph struct {
	Nodes  []Node `json:"nodes"`
	Edges  []Edge `json:"edges"`
	Broken int    `json:"broken"`
	Focus  string `json:"focus,omitempty"`
	Depth  int    `json:"depth,omitempty"`
}

// Options configures the graph export building process.
// Focus is a resolved page path (or empty for the entire vault).
// Depth is the search radius for ego-graph focus (>= 1).
// Type filters nodes to those matching Type, always retaining the focus node.
type Options struct {
	Focus string // page path, already resolved; "" = whole vault
	Depth int    // >=1 when Focus set
	Type  string // "" = any; keep only nodes of this type, plus the focus node
}

// Build constructs a Graph from v according to o.
// In focus mode, the node set includes the focus page plus every page within
// Depth hops treating edges as undirected. Only edges with both ends in the
// node set are included. Broken counts unresolved references from nodes in the set.
func Build(v *vault.Vault, o Options) *Graph {
	vg := v.Graph()
	if vg == nil {
		return &Graph{
			Nodes: []Node{},
			Edges: []Edge{},
		}
	}

	nodeSet := make(map[string]bool)
	if o.Focus != "" {
		if _, ok := v.Page(o.Focus); ok {
			nodeSet[o.Focus] = true
			depth := o.Depth
			if depth <= 0 {
				depth = 1
			}
			for _, neighbor := range vg.Neighbors(o.Focus, depth) {
				if o.Type != "" {
					pg, ok := v.Page(neighbor)
					if !ok || string(pg.FM.Type) != o.Type {
						continue
					}
				}
				nodeSet[neighbor] = true
			}
		}
	} else {
		for _, pg := range v.Pages() {
			if o.Type != "" && string(pg.FM.Type) != o.Type {
				continue
			}
			nodeSet[pg.Path] = true
		}
	}

	brokenCount := 0
	type edgeKey struct {
		from string
		to   string
	}
	edgeCounts := make(map[edgeKey]int)

	for p := range nodeSet {
		for _, ref := range vg.Outbound(p) {
			if ref.To == "" {
				brokenCount++
				continue
			}
			if ref.To == p {
				// self-link dropped
				continue
			}
			if !nodeSet[ref.To] {
				// both ends must be in the node set
				continue
			}
			edgeCounts[edgeKey{from: p, to: ref.To}]++
		}
	}

	edges := make([]Edge, 0, len(edgeCounts))
	inCounts := make(map[string]int, len(nodeSet))
	outCounts := make(map[string]int, len(nodeSet))

	for k, count := range edgeCounts {
		edges = append(edges, Edge{
			From:  k.from,
			To:    k.to,
			Count: count,
		})
		inCounts[k.to]++
		outCounts[k.from]++
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	nodes := make([]Node, 0, len(nodeSet))
	for p := range nodeSet {
		title := ""
		pType := ""
		tags := []string{}
		if pg, ok := v.Page(p); ok {
			title = pg.FM.Title
			pType = string(pg.FM.Type)
			if len(pg.FM.Tags) > 0 {
				tags = append([]string(nil), pg.FM.Tags...)
			}
		}
		nodes = append(nodes, Node{
			Path:  p,
			Title: title,
			Type:  pType,
			Tags:  tags,
			In:    inCounts[p],
			Out:   outCounts[p],
		})
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Path < nodes[j].Path
	})

	return &Graph{
		Nodes:  nodes,
		Edges:  edges,
		Broken: brokenCount,
		Focus:  o.Focus,
		Depth:  o.Depth,
	}
}

// Components returns weakly connected components of g.
// Each component is sorted by path, and components are sorted by size descending,
// then by their first path ascending.
func Components(g *Graph) [][]string {
	if len(g.Nodes) == 0 {
		return [][]string{}
	}

	adj := make(map[string][]string, len(g.Nodes))
	for _, n := range g.Nodes {
		adj[n.Path] = nil
	}
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
		adj[e.To] = append(adj[e.To], e.From)
	}

	visited := make(map[string]bool, len(g.Nodes))
	var comps [][]string

	for _, n := range g.Nodes {
		p := n.Path
		if visited[p] {
			continue
		}
		visited[p] = true
		queue := []string{p}
		comp := []string{p}

		for len(queue) > 0 {
			curr := queue[0]
			queue = queue[1:]
			for _, neighbor := range adj[curr] {
				if !visited[neighbor] {
					visited[neighbor] = true
					queue = append(queue, neighbor)
					comp = append(comp, neighbor)
				}
			}
		}

		sort.Strings(comp)
		comps = append(comps, comp)
	}

	sort.Slice(comps, func(i, j int) bool {
		if len(comps[i]) != len(comps[j]) {
			return len(comps[i]) > len(comps[j])
		}
		return comps[i][0] < comps[j][0]
	})

	return comps
}

func dotEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}

func colorForType(t string) string {
	switch t {
	case "entity":
		return "#cfe2ff"
	case "concept":
		return "#d1f2d9"
	case "comparison":
		return "#ffe8cc"
	case "query":
		return "#f3d9fa"
	case "summary":
		return "#fff3bf"
	default:
		return "#e9ecef"
	}
}

// WriteDOT serializes g into Graphviz DOT format.
// Escapes backslash and double quote in quoted strings.
// Applies a fixed color palette by page type and highlights the focus node with penwidth=3.
func WriteDOT(w io.Writer, g *Graph) error {
	if _, err := fmt.Fprintln(w, "digraph lw {"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "  graph [rankdir=LR, overlap=false];"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "  node [shape=box, style=\"rounded,filled\", fontname=\"Helvetica\"];"); err != nil {
		return err
	}

	for _, n := range g.Nodes {
		escapedPath := dotEscape(n.Path)
		escapedTitle := dotEscape(n.Title)
		color := colorForType(n.Type)
		var line string
		if g.Focus != "" && n.Path == g.Focus {
			line = fmt.Sprintf("  \"%s\" [label=\"%s\", fillcolor=\"%s\", tooltip=\"%s\", penwidth=3];\n",
				escapedPath, escapedTitle, color, escapedPath)
		} else {
			line = fmt.Sprintf("  \"%s\" [label=\"%s\", fillcolor=\"%s\", tooltip=\"%s\"];\n",
				escapedPath, escapedTitle, color, escapedPath)
		}
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}

	for _, e := range g.Edges {
		escapedFrom := dotEscape(e.From)
		escapedTo := dotEscape(e.To)
		var line string
		if e.Count > 1 {
			penwidth := e.Count
			if penwidth > 5 {
				penwidth = 5
			}
			line = fmt.Sprintf("  \"%s\" -> \"%s\" [penwidth=%d];\n", escapedFrom, escapedTo, penwidth)
		} else {
			line = fmt.Sprintf("  \"%s\" -> \"%s\";\n", escapedFrom, escapedTo)
		}
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}

	_, err := fmt.Fprintln(w, "}")
	return err
}

type jsonOutput struct {
	Nodes  []Node `json:"nodes"`
	Edges  []Edge `json:"edges"`
	Broken int    `json:"broken"`
}

// WriteJSON serializes g into indented JSON suitable for D3/Sigma/Gephi imports.
// tags is always an array ([]), never null.
func WriteJSON(w io.Writer, g *Graph) error {
	out := jsonOutput{
		Nodes:  make([]Node, len(g.Nodes)),
		Edges:  make([]Edge, len(g.Edges)),
		Broken: g.Broken,
	}
	copy(out.Nodes, g.Nodes)
	for i := range out.Nodes {
		if out.Nodes[i].Tags == nil {
			out.Nodes[i].Tags = []string{}
		}
	}
	copy(out.Edges, g.Edges)

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func mermaidEscape(s string) string {
	return strings.ReplaceAll(s, "\"", "#quot;")
}

// WriteMermaid serializes g into Mermaid format (graph LR).
func WriteMermaid(w io.Writer, g *Graph) error {
	if _, err := fmt.Fprintln(w, "graph LR"); err != nil {
		return err
	}

	nodeIdx := make(map[string]int, len(g.Nodes))
	for i, n := range g.Nodes {
		nodeIdx[n.Path] = i
		line := fmt.Sprintf("  n%d[\"%s\"]\n", i, mermaidEscape(n.Title))
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}

	for _, e := range g.Edges {
		fromIdx, okFrom := nodeIdx[e.From]
		toIdx, okTo := nodeIdx[e.To]
		if okFrom && okTo {
			line := fmt.Sprintf("  n%d --> n%d\n", fromIdx, toIdx)
			if _, err := io.WriteString(w, line); err != nil {
				return err
			}
		}
	}

	return nil
}

// WriteText serializes g into a human-readable text summary including hubs, orphans, and islands.
func WriteText(w io.Writer, g *Graph, top int) error {
	if top < 0 {
		top = 10
	}

	if g.Focus != "" {
		if _, err := fmt.Fprintf(w, "focus %s depth %d\n", g.Focus, g.Depth); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintf(w, "pages %d  links %d  broken %d\n", len(g.Nodes), len(g.Edges), g.Broken); err != nil {
		return err
	}

	comps := Components(g)
	largest := 0
	if len(comps) > 0 {
		largest = len(comps[0])
	}
	if _, err := fmt.Fprintf(w, "components %d (largest %d)\n", len(comps), largest); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(w, "hubs (most linked-to):"); err != nil {
		return err
	}

	var hubs []Node
	for _, n := range g.Nodes {
		if n.In > 0 {
			hubs = append(hubs, n)
		}
	}
	sort.Slice(hubs, func(i, j int) bool {
		if hubs[i].In != hubs[j].In {
			return hubs[i].In > hubs[j].In
		}
		return hubs[i].Path < hubs[j].Path
	})
	hubLimit := top
	if hubLimit > len(hubs) {
		hubLimit = len(hubs)
	}
	for i := 0; i < hubLimit; i++ {
		line := fmt.Sprintf("  %d  %s  (%s)\n", hubs[i].In, hubs[i].Path, hubs[i].Title)
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}

	var orphans []Node
	for _, n := range g.Nodes {
		if n.In == 0 {
			orphans = append(orphans, n)
		}
	}
	if _, err := fmt.Fprintf(w, "orphans (no inbound links) %d:\n", len(orphans)); err != nil {
		return err
	}
	orphanLimit := top
	if orphanLimit > len(orphans) {
		orphanLimit = len(orphans)
	}
	for i := 0; i < orphanLimit; i++ {
		line := fmt.Sprintf("  %s\n", orphans[i].Path)
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
	if len(orphans) > top {
		line := fmt.Sprintf("  ... and %d more\n", len(orphans)-top)
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}

	var islands [][]string
	for i, c := range comps {
		if i > 0 && len(c) >= 1 && len(c) <= 3 {
			islands = append(islands, c)
		}
	}
	if _, err := fmt.Fprintf(w, "islands (components of size 1..3 that are not the largest) %d:\n", len(islands)); err != nil {
		return err
	}
	islandLimit := top
	if islandLimit > len(islands) {
		islandLimit = len(islands)
	}
	for i := 0; i < islandLimit; i++ {
		line := fmt.Sprintf("  %s\n", strings.Join(islands[i], ", "))
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}

	return nil
}

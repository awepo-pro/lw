package graphexport

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

func createSixPageVault(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	dir := t.TempDir()

	schemaContent := `# SCHEMA

## Domain
test

## Tags
- inference
`
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}

	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatalf("mkdir concepts: %v", err)
	}

	pages := map[string]string{
		"hub.md": `---
title: Hub Page
created: 2026-08-20
updated: 2026-08-20
type: concept
tags: [inference]
---

# Hub Page

Self reference [[hub]].
`,
		"a.md": `---
title: Page A
created: 2026-08-20
updated: 2026-08-20
type: concept
tags: [inference]
---

# Page A

Links to [[b]] and [[hub]].
`,
		"b.md": `---
title: Page B
created: 2026-08-20
updated: 2026-08-20
type: concept
tags: [inference]
---

# Page B

Chain link to [[c]].
`,
		"c.md": `---
title: Page C
created: 2026-08-20
updated: 2026-08-20
type: concept
tags: [inference]
---

# Page C

Links to [[hub]] and broken [[missing-page]].
`,
		"d.md": `---
title: Page D
created: 2026-08-20
updated: 2026-08-20
type: concept
tags: []
---

# Page D

Links twice to hub: [[hub]] and [[hub]].
`,
		"orphan.md": `---
title: Orphan Page
created: 2026-08-20
updated: 2026-08-20
type: concept
tags: []
---

# Orphan Page

No links at all.
`,
	}

	for name, content := range pages {
		p := filepath.Join(conceptsDir, name)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write page %s: %v", name, err)
		}
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	return v, dir
}

// G1 Build whole vault: node count, edge count, Count==2 for the double link,
// self-link dropped, In/Out values for the hub, Broken==1.
func TestG1BuildWholeVault(t *testing.T) {
	v, _ := createSixPageVault(t)

	g := Build(v, Options{})
	if len(g.Nodes) != 6 {
		t.Fatalf("len(Nodes) = %d, want 6", len(g.Nodes))
	}
	if len(g.Edges) != 5 {
		t.Fatalf("len(Edges) = %d, want 5", len(g.Edges))
	}

	var doubleEdge *Edge
	for i := range g.Edges {
		if g.Edges[i].From == "wiki/concepts/d.md" && g.Edges[i].To == "wiki/concepts/hub.md" {
			doubleEdge = &g.Edges[i]
			break
		}
	}
	if doubleEdge == nil {
		t.Fatalf("edge d -> hub not found")
	}
	if doubleEdge.Count != 2 {
		t.Errorf("doubleEdge.Count = %d, want 2", doubleEdge.Count)
	}

	for _, e := range g.Edges {
		if e.From == e.To {
			t.Errorf("self-link not dropped: %+v", e)
		}
	}

	var hubNode *Node
	for i := range g.Nodes {
		if g.Nodes[i].Path == "wiki/concepts/hub.md" {
			hubNode = &g.Nodes[i]
			break
		}
	}
	if hubNode == nil {
		t.Fatalf("hub node not found")
	}
	if hubNode.In != 3 {
		t.Errorf("hubNode.In = %d, want 3", hubNode.In)
	}
	if hubNode.Out != 0 {
		t.Errorf("hubNode.Out = %d, want 0", hubNode.Out)
	}

	if g.Broken != 1 {
		t.Errorf("g.Broken = %d, want 1", g.Broken)
	}
}

// G2 Focus on the chain middle with Depth 1 gives exactly {a,b,c};
// Depth 2 from a gives a,b,c (+ whatever is 2 hops away).
func TestG2Focus(t *testing.T) {
	v, _ := createSixPageVault(t)

	// Focus on chain middle (b) with Depth 1
	gB := Build(v, Options{
		Focus: "wiki/concepts/b.md",
		Depth: 1,
	})
	if len(gB.Nodes) != 3 {
		t.Fatalf("focus b depth 1: len(Nodes) = %d, want 3", len(gB.Nodes))
	}
	expectedB := map[string]bool{
		"wiki/concepts/a.md": true,
		"wiki/concepts/b.md": true,
		"wiki/concepts/c.md": true,
	}
	for _, n := range gB.Nodes {
		if !expectedB[n.Path] {
			t.Errorf("unexpected node in focus b depth 1: %s", n.Path)
		}
	}

	// Depth 2 from a
	gA := Build(v, Options{
		Focus: "wiki/concepts/a.md",
		Depth: 2,
	})
	nodesA := make(map[string]bool)
	for _, n := range gA.Nodes {
		nodesA[n.Path] = true
	}
	for _, want := range []string{"wiki/concepts/a.md", "wiki/concepts/b.md", "wiki/concepts/c.md"} {
		if !nodesA[want] {
			t.Errorf("depth 2 from a missing node: %s", want)
		}
	}
	if nodesA["wiki/concepts/orphan.md"] {
		t.Errorf("orphan should not be in depth 2 from a")
	}
}

// G3 Components: correct count and order.
func TestG3Components(t *testing.T) {
	v, _ := createSixPageVault(t)
	g := Build(v, Options{})

	comps := Components(g)
	if len(comps) != 2 {
		t.Fatalf("len(comps) = %d, want 2", len(comps))
	}

	// First component is the connected cluster of 5 pages
	if len(comps[0]) != 5 {
		t.Errorf("len(comps[0]) = %d, want 5", len(comps[0]))
	}
	// Second component is the orphan page
	if len(comps[1]) != 1 {
		t.Errorf("len(comps[1]) = %d, want 1", len(comps[1]))
	}
	if comps[1][0] != "wiki/concepts/orphan.md" {
		t.Errorf("comps[1][0] = %q, want orphan", comps[1][0])
	}
}

// G4 WriteDOT exact golden string for a 2-node 1-edge graph, including escaping of a title containing a double quote.
func TestG4WriteDOTGolden(t *testing.T) {
	g := &Graph{
		Nodes: []Node{
			{
				Path:  "wiki/concepts/alpha.md",
				Title: "Alpha \"quote\"",
				Type:  "concept",
				Tags:  []string{"inference"},
				In:    0,
				Out:   1,
			},
			{
				Path:  "wiki/entities/beta.md",
				Title: "Beta",
				Type:  "entity",
				Tags:  []string{},
				In:    1,
				Out:   0,
			},
		},
		Edges: []Edge{
			{
				From:  "wiki/concepts/alpha.md",
				To:    "wiki/entities/beta.md",
				Count: 1,
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteDOT(&buf, g); err != nil {
		t.Fatalf("WriteDOT: %v", err)
	}

	want := `digraph lw {
  graph [rankdir=LR, overlap=false];
  node [shape=box, style="rounded,filled", fontname="Helvetica"];
  "wiki/concepts/alpha.md" [label="Alpha \"quote\"", fillcolor="#d1f2d9", tooltip="wiki/concepts/alpha.md"];
  "wiki/entities/beta.md" [label="Beta", fillcolor="#cfe2ff", tooltip="wiki/entities/beta.md"];
  "wiki/concepts/alpha.md" -> "wiki/entities/beta.md";
}
`
	if buf.String() != want {
		t.Errorf("WriteDOT mismatch.\nGot:\n%s\nWant:\n%s", buf.String(), want)
	}
}

// G5 WriteJSON round-trips through encoding/json into a struct and matches; empty tags marshal as [].
func TestG5WriteJSON(t *testing.T) {
	g := &Graph{
		Nodes: []Node{
			{
				Path:  "wiki/concepts/a.md",
				Title: "A",
				Type:  "concept",
				Tags:  []string{},
				In:    0,
				Out:   1,
			},
			{
				Path:  "wiki/concepts/b.md",
				Title: "B",
				Type:  "concept",
				Tags:  []string{"tag1"},
				In:    1,
				Out:   0,
			},
		},
		Edges: []Edge{
			{
				From:  "wiki/concepts/a.md",
				To:    "wiki/concepts/b.md",
				Count: 2,
			},
		},
		Broken: 1,
	}

	var buf bytes.Buffer
	if err := WriteJSON(&buf, g); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	jsonStr := buf.String()
	if !strings.Contains(jsonStr, `"tags": []`) {
		t.Errorf("JSON does not contain empty tags array []: %s", jsonStr)
	}

	var decoded struct {
		Nodes  []Node `json:"nodes"`
		Edges  []Edge `json:"edges"`
		Broken int    `json:"broken"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.Broken != g.Broken {
		t.Errorf("decoded Broken = %d, want %d", decoded.Broken, g.Broken)
	}
	if len(decoded.Nodes) != len(g.Nodes) {
		t.Fatalf("decoded len(Nodes) = %d, want %d", len(decoded.Nodes), len(g.Nodes))
	}
	for i := range g.Nodes {
		if decoded.Nodes[i].Path != g.Nodes[i].Path || decoded.Nodes[i].Title != g.Nodes[i].Title {
			t.Errorf("decoded Node[%d] mismatch", i)
		}
	}
	if len(decoded.Edges) != len(g.Edges) {
		t.Fatalf("decoded len(Edges) = %d, want %d", len(decoded.Edges), len(g.Edges))
	}
	if decoded.Edges[0].Count != 2 {
		t.Errorf("decoded Edge[0].Count = %d, want 2", decoded.Edges[0].Count)
	}
}

// G6 WriteMermaid exact golden for the 2-node graph.
func TestG6WriteMermaidGolden(t *testing.T) {
	g := &Graph{
		Nodes: []Node{
			{
				Path:  "wiki/concepts/alpha.md",
				Title: "Alpha \"quote\"",
				Type:  "concept",
				Tags:  []string{},
			},
			{
				Path:  "wiki/entities/beta.md",
				Title: "Beta",
				Type:  "entity",
				Tags:  []string{},
			},
		},
		Edges: []Edge{
			{
				From:  "wiki/concepts/alpha.md",
				To:    "wiki/entities/beta.md",
				Count: 1,
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteMermaid(&buf, g); err != nil {
		t.Fatalf("WriteMermaid: %v", err)
	}

	want := `graph LR
  n0["Alpha #quot;quote#quot;"]
  n1["Beta"]
  n0 --> n1
`
	if buf.String() != want {
		t.Errorf("WriteMermaid mismatch.\nGot:\n%s\nWant:\n%s", buf.String(), want)
	}
}

// G7 WriteText on the 6-page vault contains the hub line first under hubs, the orphan under orphans.
func TestG7WriteText(t *testing.T) {
	v, _ := createSixPageVault(t)
	g := Build(v, Options{})

	var buf bytes.Buffer
	if err := WriteText(&buf, g, 10); err != nil {
		t.Fatalf("WriteText: %v", err)
	}

	out := buf.String()
	lines := strings.Split(out, "\n")

	// Find hubs section
	hubIndex := -1
	orphanIndex := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "hubs (most linked-to):") {
			hubIndex = i
		}
		if strings.HasPrefix(line, "orphans (no inbound links)") {
			orphanIndex = i
		}
	}

	if hubIndex == -1 {
		t.Fatalf("hubs section not found in output:\n%s", out)
	}
	if orphanIndex == -1 {
		t.Fatalf("orphans section not found in output:\n%s", out)
	}

	firstHubLine := lines[hubIndex+1]
	if !strings.Contains(firstHubLine, "3  wiki/concepts/hub.md  (Hub Page)") {
		t.Errorf("first hub line = %q, want hub with 3 in-links", firstHubLine)
	}

	foundOrphan := false
	for i := orphanIndex + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "islands") {
			break
		}
		if strings.Contains(lines[i], "wiki/concepts/orphan.md") {
			foundOrphan = true
			break
		}
	}
	if !foundOrphan {
		t.Errorf("orphan not found under orphans section:\n%s", out)
	}
}

// G8 determinism: Build+WriteDOT twice gives identical bytes.
func TestG8Determinism(t *testing.T) {
	v, _ := createSixPageVault(t)

	g1 := Build(v, Options{})
	var buf1 bytes.Buffer
	if err := WriteDOT(&buf1, g1); err != nil {
		t.Fatalf("WriteDOT 1: %v", err)
	}

	g2 := Build(v, Options{})
	var buf2 bytes.Buffer
	if err := WriteDOT(&buf2, g2); err != nil {
		t.Fatalf("WriteDOT 2: %v", err)
	}

	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Errorf("non-deterministic WriteDOT output")
	}
}

// TestR8NewlineEscapesInDOTAndMermaid verifies finding R-8:
// dotEscape replaces \r with "" and \n with \n; mermaidEscape replaces \r\n / \n with a space.
func TestR8NewlineEscapesInDOTAndMermaid(t *testing.T) {
	g := &Graph{
		Nodes: []Node{
			{Path: "wiki/p1.md", Title: "Line1\r\nLine2", Type: "concept"},
		},
	}

	var dotBuf bytes.Buffer
	if err := WriteDOT(&dotBuf, g); err != nil {
		t.Fatalf("WriteDOT: %v", err)
	}
	dotOut := dotBuf.String()
	if strings.Contains(dotOut, "Line1\nLine2") || strings.Contains(dotOut, "\r") {
		t.Errorf("WriteDOT contains raw unescaped newline or carriage return:\n%s", dotOut)
	}
	if !strings.Contains(dotOut, `label="Line1\nLine2"`) {
		t.Errorf("WriteDOT missing escaped newline label:\n%s", dotOut)
	}

	var mermaidBuf bytes.Buffer
	if err := WriteMermaid(&mermaidBuf, g); err != nil {
		t.Fatalf("WriteMermaid: %v", err)
	}
	mermaidOut := mermaidBuf.String()
	if strings.Contains(mermaidOut, "Line1\nLine2") || strings.Contains(mermaidOut, "\r") {
		t.Errorf("WriteMermaid contains raw unescaped newline or carriage return:\n%s", mermaidOut)
	}
	if !strings.Contains(mermaidOut, `n0["Line1 Line2"]`) {
		t.Errorf("WriteMermaid missing space-replaced newline label:\n%s", mermaidOut)
	}
}

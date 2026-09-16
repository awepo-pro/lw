package browse

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// TestBacklinkSlugsSortedAndDistinct: autolyse is linked by four distinct
// pages in the public fixture (windowpane-test twice, the others once); the
// panel lists one slug per linking page, sorted by slug — not by path.
func TestBacklinkSlugsSortedAndDistinct(t *testing.T) {
	v := uitest.PublicVault(t, "public")
	d := uitest.Deps(v, true, nil)

	m := New(d).(*Model)
	got := m.backlinkSlugs("wiki/concepts/autolyse.md")
	want := []string{
		"commercial-yeast-vs-sourdough",
		"dutch-oven",
		"kneading-vs-folding",
		"windowpane-test",
	}
	if len(got) != len(want) {
		t.Fatalf("backlinkSlugs(autolyse) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("backlinkSlugs(autolyse)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestOutboundTargetsBodyOrderDedup: the Links-out rows follow the body's
// order, de-duplicated — autolyse links windowpane-test then
// kneading-vs-folding and nothing else.
func TestOutboundTargetsBodyOrderDedup(t *testing.T) {
	v := uitest.PublicVault(t, "public")
	d := uitest.Deps(v, true, nil)

	m := New(d).(*Model)
	n := &treeNode{Path: "wiki/concepts/autolyse.md", Name: "autolyse.md", Kind: nodePage}
	got := m.outboundTargets(n)
	want := []string{"windowpane-test", "kneading-vs-folding"}
	if len(got) != len(want) {
		t.Fatalf("outboundTargets(autolyse) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("outboundTargets(autolyse)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestFrontmatterSourcesRawSourceHasNone: a wiki page lists its `sources`
// frontmatter as written; a raw source carries none.
func TestFrontmatterSourcesRawSourceHasNone(t *testing.T) {
	v := uitest.PublicVault(t, "public")
	d := uitest.Deps(v, true, nil)

	m := New(d).(*Model)
	page := &treeNode{Path: "wiki/concepts/autolyse.md", Name: "autolyse.md", Kind: nodePage}
	got := m.frontmatterSources(page)
	if len(got) != 1 || got[0] != "raw/articles/the-science-of-a-good-loaf.md" {
		t.Fatalf("frontmatterSources(autolyse) = %v, want the page's one source path", got)
	}

	raw := &treeNode{Path: "raw/articles/the-science-of-a-good-loaf.md", Name: "the-science-of-a-good-loaf.md", Kind: nodeRawSource}
	if got := m.frontmatterSources(raw); got != nil {
		t.Fatalf("frontmatterSources(raw source) = %v, want nil", got)
	}
}

// TestLinksLinesSections: the panel's rows are the three sections in order —
// `Name  N` headers (bold name, faint count), `  item` rows, blank rows
// between — for the node under the cursor.
func TestLinksLinesSections(t *testing.T) {
	v := uitest.PublicVault(t, "public")
	d := uitest.Deps(v, true, nil)

	m := New(d).(*Model)
	m.selectPath("wiki/concepts/autolyse.md")

	lines := m.linksLines()
	joined := ansi.Strip(strings.Join(lines, "\n"))

	for _, want := range []string{
		"Backlinks  4",
		"  commercial-yeast-vs-sourdough",
		"Links out  2",
		"  windowpane-test",
		"  kneading-vs-folding",
		"Sources  1",
		"  raw/articles/the-science-of-a-good-loaf.md",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("linksLines() missing %q:\n%s", want, joined)
		}
	}

	// Section order: Backlinks header before Links out header before
	// Sources header.
	iBack := strings.Index(joined, "Backlinks")
	iOut := strings.Index(joined, "Links out")
	iSrc := strings.Index(joined, "Sources")
	if !(0 <= iBack && iBack < iOut && iOut < iSrc) {
		t.Fatalf("linksLines() sections out of order:\n%s", joined)
	}
}

// TestLinksLinesOnDirectoryIsAPlaceholder: a directory has no links; the
// panel says so instead of drawing three empty sections.
func TestLinksLinesOnDirectoryIsAPlaceholder(t *testing.T) {
	v := uitest.PublicVault(t, "public")
	d := uitest.Deps(v, true, nil)

	m := New(d).(*Model)
	m.selectPath("wiki")
	lines := m.linksLines()
	if len(lines) != 1 {
		t.Fatalf("linksLines() on a directory = %d lines, want the single placeholder", len(lines))
	}
	if !strings.Contains(ansi.Strip(lines[0]), "select a page") {
		t.Fatalf("linksLines() on a directory = %q, want a select-a-page hint", lines[0])
	}
}

// TestLinksSpecRendersInPanel: at w >= 180 the View carries the Links panel,
// titled Links, and below 180 it is absent (the Links panel appears at
// ≥ 180 columns, s2-screens.md T07).
func TestLinksSpecRendersInPanel(t *testing.T) {
	v := uitest.PublicVault(t, "public")

	wide := New(uitest.Deps(v, true, nil))
	_, plainWide := uitest.PaneScreen(wide, 180, 40)
	if !strings.Contains(plainWide, "╭ Links ") {
		t.Fatalf("View(180, 40) has no Links panel:\n%s", plainWide)
	}

	narrow := New(uitest.Deps(v, true, nil))
	_, plainNarrow := uitest.PaneScreen(narrow, 179, 40)
	if strings.Contains(plainNarrow, "╭ Links ") {
		t.Fatalf("View(179, 40) shows a Links panel below the 180-column gate:\n%s", plainNarrow)
	}
}

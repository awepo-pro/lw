package extract

// pdf_anchor_test.go pins 033 T1: page anchors in the PDF backend's
// markdown, Doc.Original, and the +anchors1 cache-version suffix. The
// anchor tests drive the same fake sidecar as pdf_test.go, extended with a
// JSONDOC=<testdata file> directive that makes it emit a real-shape
// DoclingDocument — body children as $ref, texts/tables/pictures with prov
// page_no, tables as data.table_cells, pages keyed by page number — so the
// suite still needs no Python, no Docling install and no network. Skipped
// on Windows, where the fake cannot run.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPDFPageAnchors pins the anchor placement (anchors_placed): a 4-page
// document whose pages 1, 2 and 4 carry text and whose page 3 carries only
// a picture gets anchors for 1, 2 and 4 — page 1 always first line, pages 2
// and 4 immediately before the line holding their first locatable item, and
// none for the picture-only page — while stripping every anchor line and
// its following blank line restores today's markdown byte for byte.
func TestPDFPageAnchors(t *testing.T) {
	cfg := fakeDocling(t)
	head := "## Aligned Paper\n\nAlpha opening sentence for page one.\n\n" +
		"Beta second page sentence here.\n\nGamma final page sentence here."
	// 4 pages × 100 = the F.P5 floor; padTo glues the filler to the last
	// line, which the page-4 probe must survive.
	base := padTo(head, 450) + "\n"
	uri := pdfFixture(t, t.TempDir(), "aligned.pdf", "JSONDOC=anchors.json\n"+base)

	doc, err := NewPDF(cfg).Extract(context.Background(), uri)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	want := "<!-- page 1 -->\n\n## Aligned Paper\n\nAlpha opening sentence for page one.\n\n" +
		"<!-- page 2 -->\n\nBeta second page sentence here.\n\n" +
		"<!-- page 4 -->\n\nGamma final page sentence here." +
		strings.TrimSuffix(base, "\n")[len(head):] + "\n"
	if doc.Markdown != want {
		t.Errorf("Markdown =\n%q\nwant\n%q", doc.Markdown, want)
	}
	if strings.Contains(doc.Markdown, "<!-- page 3 -->") {
		t.Error("picture-only page 3 must carry no anchor")
	}
	if got := stripAnchors(doc.Markdown); got != base {
		t.Errorf("markdown without anchors =\n%q\nwant today's markdown\n%q", got, base)
	}
}

// TestPDFTablePageAnchored pins table_page_anchored: a page whose only item
// is a table still gets its anchor — the probe that found 57/58 anchors on
// a real 58-page paper missed exactly the table-only page, so table cell
// texts must be probed like text items are.
func TestPDFTablePageAnchored(t *testing.T) {
	cfg := fakeDocling(t)
	head := "## Table Paper\n\nAlpha opening sentence for page one.\n\n" +
		"| First | Second |\n| ----- | ------ |\n| Cell one text | Cell two words here |"
	base := padTo(head, 300) + "\n"
	uri := pdfFixture(t, t.TempDir(), "tables.pdf", "JSONDOC=table-page.json\n"+base)

	doc, err := NewPDF(cfg).Extract(context.Background(), uri)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	// The page-2 anchor goes before the line holding the first locatable
	// item — the "| Cell one text | …" row, not the header: the header
	// cells ("First", "Second") are under the 12-rune trust threshold.
	want := "<!-- page 1 -->\n\n## Table Paper\n\nAlpha opening sentence for page one.\n\n" +
		"| First | Second |\n| ----- | ------ |\n" +
		"<!-- page 2 -->\n\n" + "| Cell one text | Cell two words here |" +
		strings.TrimSuffix(base, "\n")[len(head):] + "\n"
	if doc.Markdown != want {
		t.Errorf("Markdown =\n%q\nwant\n%q", doc.Markdown, want)
	}
}

// TestPDFCaptionOnlyPageAnchored pins caption_only_page_anchored: a page
// whose only locatable text is a figure caption nested as the picture's
// CHILD in the DoclingDocument (real 2.130.0 shape: body.children carries
// {"$ref":"#/pictures/N"} and pictures[N].children carries
// {"$ref":"#/texts/M"} — measured on the user's 58-page DeepSeek-V4 paper,
// pages 43 and 56) still gets its anchor. The serializer emits the picture
// placeholder, then the caption text verbatim, so the caption's own text
// locates its page — but only if the alignment walks the resolved item's
// own children. Stripping the anchors restores today's markdown byte for
// byte.
func TestPDFCaptionOnlyPageAnchored(t *testing.T) {
	cfg := fakeDocling(t)
	head := "## Caption Paper\n\nAlpha opening sentence for page one.\n\n" +
		"Figure 11 shows the win-rate comparison across model pairs."
	base := padTo(head, 300) + "\n"
	uri := pdfFixture(t, t.TempDir(), "captions.pdf", "JSONDOC=caption-page.json\n"+base)

	doc, err := NewPDF(cfg).Extract(context.Background(), uri)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	want := "<!-- page 1 -->\n\n## Caption Paper\n\nAlpha opening sentence for page one.\n\n" +
		"<!-- page 2 -->\n\nFigure 11 shows the win-rate comparison across model pairs." +
		strings.TrimSuffix(base, "\n")[len(head):] + "\n"
	if doc.Markdown != want {
		t.Errorf("Markdown =\n%q\nwant\n%q", doc.Markdown, want)
	}
	if got := stripAnchors(doc.Markdown); got != base {
		t.Errorf("markdown without anchors =\n%q\nwant today's markdown\n%q", got, base)
	}
}

// TestPDFOriginal pins original_set: Doc.Original is the absolute input
// path for a PDF, and "" from NewHTML and NewFile.
func TestPDFOriginal(t *testing.T) {
	cfg := fakeDocling(t)
	dir := t.TempDir()

	t.Run("pdf", func(t *testing.T) {
		uri := pdfFixture(t, dir, "paper.pdf", "PAGES=1\n"+padTo("", 150)+"\n")
		doc, err := NewPDF(cfg).Extract(context.Background(), uri)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if doc.Original != uri {
			t.Errorf("Original = %q, want the absolute input path %q", doc.Original, uri)
		}
	})

	t.Run("html", func(t *testing.T) {
		uri := filepath.Join(dir, "page.html")
		if err := os.WriteFile(uri, []byte("<html><body><h1>Hi</h1></body></html>"), 0o644); err != nil {
			t.Fatalf("write html: %v", err)
		}
		doc, err := NewHTML(nil).Extract(context.Background(), uri)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if doc.Original != "" {
			t.Errorf("HTML Original = %q, want \"\"", doc.Original)
		}
	})

	t.Run("file", func(t *testing.T) {
		uri := filepath.Join(dir, "note.md")
		if err := os.WriteFile(uri, []byte("# Note\n"), 0o644); err != nil {
			t.Fatalf("write md: %v", err)
		}
		doc, err := NewFile().Extract(context.Background(), uri)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if doc.Original != "" {
			t.Errorf("file Original = %q, want \"\"", doc.Original)
		}
	})
}

// TestPDFCacheVersion pins cache_version: the PDF extractor's cache key
// version is the sidecar's version plus the frozen +anchors1 suffix, so
// every entry cached before 033 misses and re-converts.
func TestPDFCacheVersion(t *testing.T) {
	v, err := PDFCacheVersion(context.Background(), fakeDocling(t))
	if err != nil {
		t.Fatalf("PDFCacheVersion: %v", err)
	}
	if v != "2.130.0+anchors1" {
		t.Errorf("PDFCacheVersion = %q, want %q", v, "2.130.0+anchors1")
	}
	if !strings.HasSuffix(v, "+anchors1") {
		t.Errorf("PDFCacheVersion = %q, want the +anchors1 suffix", v)
	}
}

// TestInsertPageAnchors pins the alignment's edges at unit level, where a
// synthetic docItem list can reach states the two end-to-end fixtures
// cannot: a page whose first locatable item shares the previous anchor's
// line, a page whose first items are unlocatable but whose later item is
// not, a body tree that yields no items at all, and an empty body. Each
// row is a tooth: letting anchors repeat or regress fails the first row,
// anchoring without alignment information fails the third.
func TestInsertPageAnchors(t *testing.T) {
	p1 := "First page long sentence here."
	p2 := "Second page long sentence follows."
	body := p1 + "\n" + p2 + "\n"

	t.Run("same-line-page-stays-unanchored", func(t *testing.T) {
		// Page 2's probe only occurs on page 1's anchored line: a second
		// anchor there would repeat line 1 and break strict monotonicity,
		// so page 2 gets none.
		got, n := insertPageAnchors(body, []docItem{
			{page: 1, probes: []string{p1}},
			{page: 2, probes: []string{p1}},
		})
		want := "<!-- page 1 -->\n\n" + body
		if got != want || n != 1 {
			t.Errorf("got %d anchors:\n%q\nwant\n%q", n, got, want)
		}
	})

	t.Run("later-item-recovers-its-page", func(t *testing.T) {
		// Page 2's first item carries no probe a ≥12-rune trust threshold
		// would grant; its later item does, and anchors page 2 there.
		got, n := insertPageAnchors(body, []docItem{
			{page: 1, probes: []string{p1}},
			{page: 2},
			{page: 2, probes: []string{p2}},
		})
		want := "<!-- page 1 -->\n\n" + p1 + "\n<!-- page 2 -->\n\n" + p2 + "\n"
		if got != want || n != 2 {
			t.Errorf("got %d anchors:\n%q\nwant\n%q", n, got, want)
		}
	})

	t.Run("item-less-body-untouched", func(t *testing.T) {
		// A body tree that yields no items leaves the markdown alone: a
		// real DoclingDocument always carries one, so this is the pinned
		// degenerate case, not the page-1-always rule — page 1's anchor is
		// seeded at line 0 whenever there is anything to align.
		got, n := insertPageAnchors(body, nil)
		if got != body || n != 0 {
			t.Errorf("item-less body =\n%q\n(%d anchors), want untouched", got, n)
		}
	})

	t.Run("empty-body-untouched", func(t *testing.T) {
		if got, n := insertPageAnchors("", []docItem{{page: 1, probes: []string{p1}}}); got != "" || n != 0 {
			t.Errorf("empty body = %q, %d anchors, want untouched", got, n)
		}
	})

	t.Run("rendered-probe-finds-escaped-text", func(t *testing.T) {
		// The serializer rendered the item's `_` as `\_`; the rendered
		// variant of the same text locates its line.
		got, n := insertPageAnchors("Intro sentence for page one.\nCost \\_of\\_the\\_model rises.\n", []docItem{
			{page: 1, probes: []string{"Intro sentence for page"}},
			{page: 2, probes: []string{"Cost _of_the_model rises", `Cost \_of\_the\_model rises`}},
		})
		want := "<!-- page 1 -->\n\nIntro sentence for page one.\n<!-- page 2 -->\n\nCost \\_of\\_the\\_model rises.\n"
		if got != want || n != 2 {
			t.Errorf("got %d anchors:\n%q\nwant\n%q", n, got, want)
		}
	})
}

// TestProbeVariants pins the probe variants parseDocItems stores per item
// text: plain text keeps its single raw probe; text the serializer would
// rewrite (`_ & < > |`) also carries its rendered form, so a page of
// snake_case anchors instead of silently dropping out. Text under the
// 12-rune trust threshold yields no probe at all.
func TestProbeVariants(t *testing.T) {
	plain := "Plain sentence, no specials"
	if got := probeVariants(plain); len(got) != 1 || got[0] != plain {
		t.Errorf("probeVariants(plain) = %q, want [%q]", got, plain)
	}
	if got := probeVariants("tooshort"); len(got) != 0 {
		t.Errorf("probeVariants(short) = %q, want none", got)
	}
	got := probeVariants("Cost_of_the_model")
	if len(got) != 2 || got[0] != "Cost_of_the_model" || got[1] != `Cost\_of\_the\_model` {
		t.Errorf("probeVariants(escaped) = %q, want [Cost_of_the_model Cost\\_of\\_the\\_model]", got)
	}
}
func stripAnchors(md string) string {
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "<!-- page ") && strings.HasSuffix(lines[i], " -->") {
			if i+1 < len(lines) && lines[i+1] == "" {
				i++ // drop the anchor's own blank line too
			}
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

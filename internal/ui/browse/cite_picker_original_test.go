package browse

// cite_picker_original_test.go pins 058's D5: since 058 a raw's original is
// not always a PDF — it can be the saved .html, a .txt, or an x.md.orig —
// and the `o` picker hands originals to open.pdf, a PDF viewer. Only a .pdf
// original is openable; any other is, to the picker, no original at all.

import (
	"path/filepath"
	"strings"
	"testing"
)

// writeCiteOriginals adds raws whose originals are of every kind, and one
// page citing each of them.
func writeCiteOriginals(t *testing.T, root string) {
	t.Helper()
	writeCiteRaw(t, root, "raw/papers/p.md", "raw/papers/p.pdf")
	writeCiteRaw(t, root, "raw/papers/upper.md", "raw/papers/upper.PDF")
	writeCiteRaw(t, root, "raw/articles/h.md", "raw/articles/h.html")
	writeCiteRaw(t, root, "raw/articles/htm.md", "raw/articles/htm.htm")
	writeCiteRaw(t, root, "raw/articles/t.md", "raw/articles/t.txt")
	writeCiteRaw(t, root, "raw/articles/m.md", "raw/articles/m.md.orig")
	writeCitePage(t, root, "wiki/concepts/mixed-originals.md", `# Mixed

A pdf.^[raw/papers/p.md p.4] A shouted pdf.^[raw/papers/upper.md p.2] A web page.^[raw/articles/h.md]
An htm page.^[raw/articles/htm.md] Text.^[raw/articles/t.md] Markdown.^[raw/articles/m.md]
`)
}

// TestCitePickerHTMLOriginalNotPDF: a page's citations of raws whose original
// is not a .pdf take the "no PDF" row and Enter on one launches nothing;
// the .pdf rows (either case) are exactly today's.
func TestCitePickerHTMLOriginalNotPDF(t *testing.T) {
	d, engine, root := newCiteDeps(t, writeCiteOriginals)
	defer engine.Close()

	spy := &openerSpy{}
	d.OpenPDF = spy.open

	m := openPickerOn(t, New(d).(*Model), "wiki/concepts/mixed-originals.md")

	want := []string{
		"p.4     p.md",
		"p.2     upper.md",
		"no PDF  h.md",
		"no PDF  htm.md",
		"no PDF  t.md",
		"no PDF  m.md",
	}
	if got := pickerPlainRows(m); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("picker rows = %q, want %q", got, want)
	}
	for i, r := range m.picker.rows {
		wantNoPDF := i >= 2
		if r.noPDF != wantNoPDF {
			t.Errorf("row %d (%s) noPDF = %v, want %v", i, r.source, r.noPDF, wantNoPDF)
		}
		if wantNoPDF && r.pdf != "" {
			t.Errorf("row %d (%s) carries pdf %q, want \"\" — a non-PDF original is not openable", i, r.source, r.pdf)
		}
	}

	// Enter on each no-PDF row reports the missing original and launches
	// nothing; the two PDF rows still open.
	for step := 0; step < len(want); step++ {
		p, _ := m.Update(keyMsg("enter"))
		m = p.(*Model)
		if step >= 2 {
			if got, wantStatus := m.picker.status, "no original PDF: "+m.picker.rows[step].source; got != wantStatus {
				t.Errorf("row %d status = %q, want %q", step, got, wantStatus)
			}
		}
		p, _ = m.Update(keyMsg("j"))
		m = p.(*Model)
	}
	if len(spy.calls) != 2 {
		t.Fatalf("OpenPDF calls = %d (%v), want 2 — only the two .pdf originals", len(spy.calls), spy.calls)
	}
	if spy.calls[0].pdf != filepath.Join(root, "raw", "papers", "p.pdf") || spy.calls[0].page != 4 {
		t.Errorf("first call = %+v, want raw/papers/p.pdf at page 4", spy.calls[0])
	}
	if spy.calls[1].pdf != filepath.Join(root, "raw", "papers", "upper.PDF") || spy.calls[1].page != 2 {
		t.Errorf("second call = %+v, want raw/papers/upper.PDF at page 2", spy.calls[1])
	}
}

// TestCitePickerHTMLOriginalRawSourceHasNoRow: with a raw source selected, a
// non-PDF original yields no row at all ("(no citations)"), while a .pdf
// original still yields its one "—" row at page 1.
func TestCitePickerHTMLOriginalRawSourceHasNoRow(t *testing.T) {
	d, engine, _ := newCiteDeps(t, writeCiteOriginals)
	defer engine.Close()

	spy := &openerSpy{}
	d.OpenPDF = spy.open

	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{"raw/articles/h.md", nil},
		{"raw/articles/htm.md", nil},
		{"raw/articles/t.md", nil},
		{"raw/articles/m.md", nil},
		{"raw/papers/p.md", []string{"—  p.pdf"}},
		{"raw/papers/upper.md", []string{"—  upper.PDF"}},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			m := openPickerOn(t, New(d).(*Model), tc.raw)
			if got := pickerPlainRows(m); strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("picker rows = %q, want %q", got, tc.want)
			}
			if len(tc.want) == 0 {
				m.Update(keyMsg("enter"))
				if len(spy.calls) != 0 {
					t.Fatalf("Enter on an empty picker launched a viewer: %v", spy.calls)
				}
			}
		})
	}
}

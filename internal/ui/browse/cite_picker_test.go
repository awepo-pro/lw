// cite_picker_test.go pins Browse's `o` citation picker (034 T5): the rows
// a wiki page's markers compile to, the page each row opens, and the exact
// status line Enter leaves — including the refusal to hand a viewer an
// original that escapes raw/, and the unset-opener hint. The vault is the
// minimal fixture plus the citation corpus written in below, built the way
// browse_test.go's newTestDeps does: a private copy, a real engine, and
// hermetic theme/keys.
package browse

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// citeCorpusBody is the page TestBrowseOpenPickerRows pins: every marker
// shape the picker must distinguish — paged, unpaged, duplicated, fenced,
// malformed, and a well-formed non-raw marker no viewer can ever open.
const citeCorpusBody = `# Cites

Cache hits dominate the cost model.^[raw/papers/x.md p.12] The second claim
sits on a later page.^[raw/papers/x.md p.30] The article stands
alone.^[raw/articles/y.md] A repeat of the first.^[raw/papers/x.md p.12]

A wiki page is not a PDF: ^[wiki/concepts/kv-cache.md]

` + "```text" + `
^[raw/papers/x.md p.99]
` + "```" + `

Broken: ^[raw/papers/x.md p.7-q]
`

// citeEscapeBody is the page whose only citation points at a raw source
// whose original: leaves raw/.
const citeEscapeBody = `# Escape

A path that leaves raw/.^[raw/papers/esc.md p.3]
`

// newCiteDeps copies the minimal fixture, runs write against the copy, and
// opens the engine on it — the same shape browse_test.go's newTestDeps
// builds, with the citation corpus added between the copy and the open.
// The vault root comes back so tests can assert the absolute path OpenPDF
// receives. XDG_CONFIG_HOME points at a fresh temp dir for the test's
// duration, so LoadTheme/LoadKeys never read a real user config.
func newCiteDeps(t *testing.T, write func(t *testing.T, root string)) (ui.Deps, *stage.Engine, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := testutil.CopyFixture(t, "minimal")
	if write != nil {
		write(t, root)
	}
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}

	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	return ui.Deps{Engine: engine, Theme: theme, Keys: keys}, engine, root
}

// writeCiteRaw writes a raw source at vault-relative path, with original
// ("" for none) as its frontmatter original: and a sha256 that matches the
// body, the way the ingest writes the pair (033).
func writeCiteRaw(t *testing.T, root, path, original string) {
	t.Helper()
	body := "# " + strings.TrimSuffix(filepath.Base(path), ".md") + "\n\nextracted text\n"
	sum := sha256.Sum256([]byte(body))
	fm := fmt.Sprintf("---\nsource_url: https://example.test/%s\ningested: 2026-09-01\nsha256: %s\n",
		path, hex.EncodeToString(sum[:]))
	if original != "" {
		fm += "original: " + original + "\n"
	}
	writeCiteFile(t, root, path, fm+"---\n\n"+body)
}

// writeCitePage writes a wiki page at vault-relative path with body.
func writeCitePage(t *testing.T, root, path, body string) {
	t.Helper()
	writeCiteFile(t, root, path, "---\ntitle: Cites\ncreated: 2026-09-01\nupdated: 2026-09-01\ntype: concept\ntags: []\nconfidence: high\n---\n\n"+body)
}

// writeCiteFile creates every parent directory and writes b.
func writeCiteFile(t *testing.T, root, path, b string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(b), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// citePlainBody is a page with no markers at all — every fixture page
// carries one, so the "(no citations)" case needs its own.
const citePlainBody = `# Plain

No provenance markers here.
`

// writeCiteCorpus adds the x/y/esc sources and the citing pages.
func writeCiteCorpus(t *testing.T, root string) {
	t.Helper()
	writeCiteRaw(t, root, "raw/papers/x.md", "raw/papers/x.pdf")
	writeCiteRaw(t, root, "raw/articles/y.md", "")
	writeCiteRaw(t, root, "raw/papers/esc.md", "../outside.pdf")
	writeCitePage(t, root, "wiki/concepts/cites.md", citeCorpusBody)
	writeCitePage(t, root, "wiki/concepts/escape.md", citeEscapeBody)
	writeCitePage(t, root, "wiki/concepts/plain.md", citePlainBody)
}

// pickerPlainRows renders each picker row the way the picker draws it,
// minus styling: the row text plus the faint " · no PDF" suffix.
func pickerPlainRows(m *Model) []string {
	out := make([]string, 0, len(m.picker.rows))
	for _, r := range m.picker.rows {
		row := r.text
		if r.noPDF {
			row += " · no PDF"
		}
		out = append(out, row)
	}
	return out
}

// openPickerOn selects path and presses o, returning the model with the
// picker open.
func openPickerOn(t *testing.T, m *Model, path string) *Model {
	t.Helper()
	if !m.selectPath(path) {
		t.Fatalf("selectPath: %s not found in tree", path)
	}
	p, _ := m.Update(keyMsg("o"))
	m, ok := p.(*Model)
	if !ok || !m.picker.open {
		t.Fatalf("after o, picker open = %v (model %T), want open", m.picker.open, p)
	}
	return m
}

// openerSpy returns a fake OpenPDF recording every call.
type openerSpy struct {
	calls []struct {
		pdf  string
		page int
	}
}

func (s *openerSpy) open(pdf string, page int) error {
	s.calls = append(s.calls, struct {
		pdf  string
		page int
	}{pdf, page})
	return nil
}

// TestBrowseOpenPickerRows pins the rows a wiki page's markers compile to
// (034 T5): one per distinct well-formed raw/ Cite, in body order; fenced,
// malformed and non-raw markers produce nothing, and a source without an
// original: carries the faint " · no PDF" suffix.
func TestBrowseOpenPickerRows(t *testing.T) {
	d, engine, _ := newCiteDeps(t, writeCiteCorpus)
	defer engine.Close()

	m := openPickerOn(t, New(d).(*Model), "wiki/concepts/cites.md")

	got := pickerPlainRows(m)
	want := []string{"x.md p.12", "x.md p.30", "y.md · no PDF"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("picker rows = %q, want %q", got, want)
	}
}

// TestBrowseOpenCallsOpener pins the Enter path: the wired OpenPDF gets the
// absolute PDF path and the cite's From page, the picker stays open, and
// its last line names what was opened.
func TestBrowseOpenCallsOpener(t *testing.T) {
	d, engine, root := newCiteDeps(t, writeCiteCorpus)
	defer engine.Close()

	spy := &openerSpy{}
	d.OpenPDF = spy.open

	m := openPickerOn(t, New(d).(*Model), "wiki/concepts/cites.md")
	p, _ := m.Update(keyMsg("enter"))
	m = p.(*Model)

	if len(spy.calls) != 1 {
		t.Fatalf("OpenPDF calls = %d, want 1", len(spy.calls))
	}
	if want := filepath.Join(root, "raw", "papers", "x.pdf"); spy.calls[0].pdf != want {
		t.Fatalf("OpenPDF pdf = %q, want %q", spy.calls[0].pdf, want)
	}
	if spy.calls[0].page != 12 {
		t.Fatalf("OpenPDF page = %d, want 12 (the cite's From)", spy.calls[0].page)
	}
	if got := m.picker.status; got != "opened x.pdf at page 12" {
		t.Fatalf("picker status = %q, want %q", got, "opened x.pdf at page 12")
	}
	if !m.picker.open {
		t.Fatal("picker closed after enter, want it to stay open")
	}
}

// TestBrowseOpenNoPDF pins the status for a row whose source carries no
// original: — named by source, and no viewer is ever asked.
func TestBrowseOpenNoPDF(t *testing.T) {
	d, engine, _ := newCiteDeps(t, writeCiteCorpus)
	defer engine.Close()

	spy := &openerSpy{}
	d.OpenPDF = spy.open

	m := openPickerOn(t, New(d).(*Model), "wiki/concepts/cites.md")
	p, _ := m.Update(keyMsg("j"))
	m = p.(*Model)
	p, _ = m.Update(keyMsg("j"))
	m = p.(*Model)
	p, _ = m.Update(keyMsg("enter"))
	m = p.(*Model)

	if len(spy.calls) != 0 {
		t.Fatalf("OpenPDF called for a source with no original: %v", spy.calls)
	}
	if got := m.picker.status; got != "raw/articles/y.md has no original PDF" {
		t.Fatalf("picker status = %q, want %q", got, "raw/articles/y.md has no original PDF")
	}
}

// TestBrowseOpenEscapingOriginal pins the guard: an original: that escapes
// raw/ is never handed to the viewer — its status names the path as
// written, not the cleaned one the check ran on.
func TestBrowseOpenEscapingOriginal(t *testing.T) {
	d, engine, _ := newCiteDeps(t, writeCiteCorpus)
	defer engine.Close()

	spy := &openerSpy{}
	d.OpenPDF = spy.open

	m := openPickerOn(t, New(d).(*Model), "wiki/concepts/escape.md")
	p, _ := m.Update(keyMsg("enter"))
	m = p.(*Model)

	if len(spy.calls) != 0 {
		t.Fatalf("OpenPDF called with an original outside raw/: %v", spy.calls)
	}
	if got := m.picker.status; got != "original ../outside.pdf is outside raw/" {
		t.Fatalf("picker status = %q, want %q", got, "original ../outside.pdf is outside raw/")
	}
}

// TestBrowseOpenUnset pins the status when no opener is wired at all
// (Deps.OpenPDF nil — a harness, or a shell built without the seam): the
// exact command that fixes it.
func TestBrowseOpenUnset(t *testing.T) {
	d, engine, _ := newCiteDeps(t, writeCiteCorpus)
	defer engine.Close()

	m := openPickerOn(t, New(d).(*Model), "wiki/concepts/cites.md")
	p, _ := m.Update(keyMsg("enter"))
	m = p.(*Model)

	if got := m.picker.status; got != `set open.pdf first: lw config set open.pdf "papers -i {page} {file}"` {
		t.Fatalf("picker status = %q, want the unset hint", got)
	}
}

// TestBrowseOpenRawSource pins the raw-source-selected shape: exactly one
// row — the PDF's base name — and Enter opens it at page 1.
func TestBrowseOpenRawSource(t *testing.T) {
	d, engine, root := newCiteDeps(t, writeCiteCorpus)
	defer engine.Close()

	spy := &openerSpy{}
	d.OpenPDF = spy.open

	m := openPickerOn(t, New(d).(*Model), "raw/papers/x.md")

	if got, want := pickerPlainRows(m), []string{"x.pdf"}; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("picker rows = %q, want %q", got, want)
	}
	p, _ := m.Update(keyMsg("enter"))
	m = p.(*Model)

	if len(spy.calls) != 1 {
		t.Fatalf("OpenPDF calls = %d, want 1", len(spy.calls))
	}
	if want := filepath.Join(root, "raw", "papers", "x.pdf"); spy.calls[0].pdf != want {
		t.Fatalf("OpenPDF pdf = %q, want %q", spy.calls[0].pdf, want)
	}
	if spy.calls[0].page != 1 {
		t.Fatalf("OpenPDF page = %d, want 1 (the raw source's first page)", spy.calls[0].page)
	}
	if got := m.picker.status; got != "opened x.pdf at page 1" {
		t.Fatalf("picker status = %q, want %q", got, "opened x.pdf at page 1")
	}
}

// TestBrowseOpenPickerNoCursorOnEmpty pins the empty picker's cursor
// (review pass, 034 T5): "(no citations)" is a placeholder, not a row —
// Enter no-ops on it — so it must not wear the cursor gutter that names a
// launchable row. With rows, exactly one gutter comes back.
func TestBrowseOpenPickerNoCursorOnEmpty(t *testing.T) {
	d, engine, _ := newCiteDeps(t, writeCiteCorpus)
	defer engine.Close()

	p := New(d)
	m := openPickerOn(t, p.(*Model), "wiki/concepts/plain.md")
	rows := m.renderCitePicker(80, 20)
	for _, r := range rows {
		if strings.Contains(r, "▌") {
			t.Fatalf("empty picker draws a cursor gutter on the placeholder:\n%s",
				strings.Join(rows, "\n"))
		}
	}

	p, _ = m.Update(keyMsg("esc")) // close before reopening on a citing page
	m = p.(*Model)
	m = openPickerOn(t, m, "wiki/concepts/cites.md")
	rows = m.renderCitePicker(80, 20)
	gutters := 0
	for _, r := range rows {
		if strings.Contains(r, "▌") {
			gutters++
		}
	}
	if gutters != 1 {
		t.Fatalf("picker with rows draws %d cursor gutters, want 1:\n%s",
			gutters, strings.Join(rows, "\n"))
	}
}

// TestBrowseOpenPickerScrollsStatusLast pins the scrolled picker (review
// pass, 034 T5): when the rows outgrow the box, the window slides so the
// cursor row stays visible, and the status line keeps the box's last
// content row — it is appended after the scrolled window, never scrolled
// away with the rows or clipped by the panel.
func TestBrowseOpenPickerScrollsStatusLast(t *testing.T) {
	d, engine, _ := newCiteDeps(t, writeCiteCorpus)
	defer engine.Close()

	spy := &openerSpy{}
	d.OpenPDF = spy.open

	m := openPickerOn(t, New(d).(*Model), "wiki/concepts/cites.md")
	p, _ := m.Update(keyMsg("enter")) // leave a status line worth finding
	m = p.(*Model)
	p, _ = m.Update(keyMsg("j")) // cursor onto the last row
	m = p.(*Model)
	p, _ = m.Update(keyMsg("j"))
	m = p.(*Model)

	// h=5: two content rows inside the box — three cannot fit, so the
	// window must slide past x.md p.12 to keep the cursor in view.
	_, plain := uitest.PaneScreen(p, 80, 5)
	if strings.Contains(plain, "x.md p.12") {
		t.Fatalf("row above the window still rendered:\n%s", plain)
	}
	if !strings.Contains(plain, "x.md p.30") {
		t.Fatalf("cursor row scrolled out of view:\n%s", plain)
	}
	if !strings.Contains(plain, "opened x.pdf at page 12") {
		t.Fatalf("status line did not survive the scroll:\n%s", plain)
	}
}

// TestBrowseOpenPickerKeysAndRender is the picker's shell-side shape (034
// T5, supporting): esc and o close it, a page without usable citations
// says "(no citations)", and the render carries the rows with the status
// line last inside the box over the preview area.
func TestBrowseOpenPickerKeysAndRender(t *testing.T) {
	d, engine, _ := newCiteDeps(t, writeCiteCorpus)
	defer engine.Close()

	spy := &openerSpy{}
	d.OpenPDF = spy.open

	p := New(d)
	m := openPickerOn(t, p.(*Model), "wiki/concepts/cites.md")

	// esc closes, and nothing else lingers: a second o rebuilds it fresh.
	p, _ = m.Update(keyMsg("esc"))
	m = p.(*Model)
	if m.picker.open {
		t.Fatal("esc left the picker open")
	}
	m = openPickerOn(t, m, "wiki/concepts/cites.md")
	p, _ = m.Update(keyMsg("o"))
	m = p.(*Model)
	if m.picker.open {
		t.Fatal("o left the picker open")
	}

	// A page with no raw/ markers says so instead of drawing an empty box.
	m = openPickerOn(t, m, "wiki/concepts/plain.md")
	if rows := pickerPlainRows(m); len(rows) != 0 {
		t.Fatalf("plain.md picker rows = %q, want none", rows)
	}
	_, plain := uitest.PaneScreen(p, 120, 38)
	if !strings.Contains(plain, "(no citations)") {
		t.Fatalf("View without citations does not say so:\n%s", plain)
	}

	// With rows, the render shows them and the status line sits last.
	p, _ = m.Update(keyMsg("esc")) // o would close it again — that is the key's close half
	m = p.(*Model)
	m = openPickerOn(t, m, "wiki/concepts/cites.md")
	p, _ = m.Update(keyMsg("enter"))
	m = p.(*Model)
	styled, plain := uitest.PaneScreen(p, 120, 38)
	// The picker's box honours the exactly-h-lines-of-exactly-w-cells
	// invariant like every other render (conventions §4 rule 2).
	uitest.AssertGrid(t, plain, 120, 38)
	if !strings.Contains(plain, "x.md p.12") || !strings.Contains(plain, "y.md · no PDF") {
		t.Fatalf("View does not show the picker rows:\n%s", plain)
	}
	if got := m.picker.status; got != "opened x.pdf at page 12" {
		t.Fatalf("picker status = %q, want the opened line", got)
	}
	rows := strings.Split(plain, "\n")
	statusAt, rowsAt := -1, -1
	for i, line := range rows {
		if strings.Contains(line, "opened x.pdf at page 12") {
			statusAt = i
		}
		if strings.Contains(line, "y.md · no PDF") {
			rowsAt = i
		}
	}
	if statusAt < 0 || rowsAt < 0 || statusAt <= rowsAt {
		t.Fatalf("status line (row %d) is not after the last row (row %d):\n%s", statusAt, rowsAt, plain)
	}
	if len(styled) == 0 {
		t.Fatal("styled render is empty")
	}
}

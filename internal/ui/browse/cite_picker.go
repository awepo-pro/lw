// cite_picker.go is Browse's `o` picker (034 T5): the citations of the
// selection, one row per PDF you can jump to, drawn as a bordered box over
// the preview area. Markers like ^[raw/papers/x.md p.12] now carry the
// physical page a claim sits on, and checking one means leaving the TUI for
// a viewer — the picker exists so several citations can be checked in turn
// without losing the tree cursor or the preview. The grammar is cite's
// (internal/cite); this file only decides which markers are openable and
// what a launch reports.
package browse

import (
	"path"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/cite"
	"github.com/awepo-pro/lw/internal/ui"
)

// pickerState holds the `o` picker's transient state. It is its own struct,
// rather than fields on Model, so opening and closing the picker is one
// assignment, the way the finder works.
type pickerState struct {
	open   bool
	rows   []pickerRow
	cursor int    // index into rows
	status string // what the last Enter did, shown on the box's last line
}

// pickerRow is one entry the picker offers: a viewer launch waiting on
// Enter. pdf is vault-relative and slash-separated (00-conventions.md §3);
// the absolute path is built at launch time from the vault root.
type pickerRow struct {
	// label is the row's left column — the one fact that matters, kept
	// first so no box width can clip it away (A-034-5): "p.N" or "p.N-M"
	// for a paged cite, "—" for an unpaged cite of a source that has an
	// original (Enter opens page 1), "no PDF" for a source without one.
	label string
	// base is the name after the label: the original's base name when the
	// source has one, else the source's own base name.
	base string
	// source is the vault-relative md source the row stands for — the
	// "no original PDF" status names it.
	source string
	// pdf is the source's original: path, "" when it has none.
	pdf string
	// page is what Enter opens: the cite's From, or 1 when unpaged.
	page int
	// noPDF renders the whole row faint.
	noPDF bool
}

// pickerTexts lays the rows out: each label left-aligned, padded with
// spaces to the widest label in the current list, then two spaces, then the
// base (which the panel clips when the box is narrower than the name).
// Padding counts display cells (conventions §4 rule 1) — the "—" label is
// East-Asian-ambiguous width, and a byte or rune count would misalign the
// base column on a wide-glyph terminal. Label first is the whole point of
// A-034-5: a 71-character raw base name must never push the page number
// past the box edge again.
func pickerTexts(rows []pickerRow) []string {
	w := 0
	for _, r := range rows {
		if n := ansi.StringWidth(r.label); n > w {
			w = n
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.label + strings.Repeat(" ", w-ansi.StringWidth(r.label)) + "  " + r.base
	}
	return out
}

// openPicker opens the citation picker for the selection, rebuilding the
// rows from the vault as it stands now.
func (m *Model) openPicker() {
	m.picker = pickerState{open: true, rows: m.citeRows()}
	m.clampPickerCursor()
}

// closePicker closes the picker and discards its rows and status.
func (m *Model) closePicker() { m.picker = pickerState{} }

// clampPickerCursor keeps the picker's cursor inside the rows, or 0 when
// there are none.
func (m *Model) clampPickerCursor() {
	if m.picker.cursor >= len(m.picker.rows) {
		m.picker.cursor = len(m.picker.rows) - 1
	}
	if m.picker.cursor < 0 {
		m.picker.cursor = 0
	}
}

// handlePickerKey handles one key press while the picker is open. The set
// is deliberately smaller than the finder's: the picker takes no text, so
// every key it does not name is ignored rather than swallowed into a query.
func (m *Model) handlePickerKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "esc", "o":
		m.closePicker()
	case "enter":
		m.pickerEnter()
	case "up", "k":
		if m.picker.cursor > 0 {
			m.picker.cursor--
		}
	case "down", "j":
		if m.picker.cursor < len(m.picker.rows)-1 {
			m.picker.cursor++
		}
	}
}

// pickerEnter runs the row under the picker's cursor: resolve the original,
// guard it, launch the viewer, and report the outcome on the box's last
// line — the picker stays open either way, so several citations can be
// checked in turn (034 T5).
func (m *Model) pickerEnter() {
	if len(m.picker.rows) == 0 {
		return
	}
	row := m.picker.rows[m.picker.cursor]

	// No original: nothing to launch, and the status names the source —
	// the md file is what the curator knows the marker by, not the PDF
	// that was never ingested beside it.
	if row.pdf == "" {
		m.picker.status = "no original PDF: " + row.source
		return
	}

	// An original: that escapes raw/ is never handed to the viewer. The
	// check runs on the cleaned path — "./raw/x.pdf" and "raw/a/../x.pdf"
	// are fine, "../outside.pdf" is not — while the message repeats the
	// path as written, the bytes the curator put in the frontmatter. It is
	// a textual guard, not a filesystem one: a symlink under raw/ pointing
	// outside the vault passes (Clean does not resolve links), and that is
	// the curator's own arrangement in their own vault — this seam is
	// never reachable from the agent, so there is nothing to sandbox.
	clean := path.Clean(row.pdf)
	if !strings.HasPrefix(clean, "raw/") || strings.Contains(clean, "..") {
		m.picker.status = "outside raw/: " + row.pdf
		return
	}

	// No opener wired: the hint is the fix.
	if m.deps.OpenPDF == nil {
		m.picker.status = ui.OpenPDFUnsetHint
		return
	}

	abs := filepath.Join(m.deps.Engine.Vault().Root(), filepath.FromSlash(row.pdf))
	if err := m.deps.OpenPDF(abs, row.page); err != nil {
		m.picker.status = err.Error()
		return
	}
	m.picker.status = "page " + strconv.Itoa(row.page) + " opened: " + filepath.Base(row.pdf)
}

// citeRows builds the picker's rows for the node under the tree cursor. A
// wiki page contributes one row per distinct well-formed raw/ Cite (by
// canonical String(), so a marker repeated in the body offers one launch,
// not two), in body order. A raw source with an original contributes one
// row for its PDF at page 1. Anything else — a directory, a page without
// raw/ markers, a raw source without an original, no vault — yields no
// rows, and the box says "(no citations)".
func (m *Model) citeRows() []pickerRow {
	if m.deps.Engine == nil {
		return nil
	}
	n := m.selectedNode()
	if n == nil || n.IsDir() {
		return nil
	}
	v := m.deps.Engine.Vault()

	if n.Kind == nodeRawSource {
		r, ok := v.RawSource(n.Path)
		if !ok || r.Original == "" {
			return nil
		}
		return []pickerRow{{
			label: "—", base: filepath.Base(r.Original),
			source: r.Path, pdf: r.Original, page: 1,
		}}
	}

	p, ok := v.Page(n.Path)
	if !ok {
		return nil
	}
	var rows []pickerRow
	seen := map[string]bool{}
	for _, c := range cite.Scan(p.Body) {
		// Malformed markers are lint's business, not the picker's; non-raw
		// sources have no PDF original by construction (A-034-4: pages are
		// what a page number can mean).
		if c.Err != "" || seen[c.String()] || !strings.HasPrefix(c.Source, "raw/") {
			continue
		}
		seen[c.String()] = true

		row := pickerRow{source: c.Source, base: filepath.Base(c.Source), page: 1}
		if c.From > 0 {
			row.page = c.From
			if c.To > c.From {
				row.label = "p." + strconv.Itoa(c.From) + "-" + strconv.Itoa(c.To)
			} else {
				row.label = "p." + strconv.Itoa(c.From)
			}
		}
		if r, ok := v.RawSource(c.Source); ok && r.Original != "" {
			row.pdf = r.Original
			if row.label == "" {
				row.label = "—" // an unpaged cite of a source with an original
			}
		} else {
			row.noPDF = true
			row.base = filepath.Base(c.Source)
			row.label = "no PDF"
		}
		rows = append(rows, row)
	}
	return rows
}

// renderCitePicker draws the picker box at exactly w columns by h rows: the
// preview panel's area, which it replaces for as long as the picker is
// open — the same Panels-over-region contract View follows, so no row is
// ever hand-spliced out of a styled render.
func (m *Model) renderCitePicker(w, h int) []string {
	texts := pickerTexts(m.picker.rows)
	rows := make([]string, 0, len(texts)+1)
	for i, r := range m.picker.rows {
		// Faint stays the no-PDF row's tell — now the whole row, since the
		// suffix that used to carry it is gone.
		if r.noPDF {
			rows = append(rows, m.deps.Theme.Faint.Render(texts[i]))
		} else {
			rows = append(rows, m.deps.Theme.Fg.Render(texts[i]))
		}
	}
	if len(rows) == 0 {
		// Nothing openable — say so rather than draw an empty box.
		rows = append(rows, m.deps.Theme.Faint.Render("(no citations)"))
	}

	// Cursor -1 means none: with no rows there is nothing Enter can act on,
	// and the "(no citations)" placeholder must not wear the gutter that
	// names a launchable row.
	cursor := -1
	if len(m.picker.rows) > 0 {
		// Scroll the rows so the cursor stays in view once the list
		// outgrows the box, the way the Pages tree does.
		rows, cursor = scrollWindow(rows, m.picker.cursor, h-3)
	}

	// The status line is the box's last row, always present: empty until
	// the first Enter, then the outcome of the latest one. It sits outside
	// the scrolled window, so it never scrolls away with the rows.
	lines := append(rows, m.deps.Theme.Muted.Render(m.picker.status))

	return ui.Panel(m.deps.Theme, ui.PanelSpec{
		Title:     "Citations",
		Focused:   true,
		Lines:     lines,
		Overflow:  true,
		CursorRow: cursor,
	}, w, h)
}

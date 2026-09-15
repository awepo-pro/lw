// frame.go composes the shell's own two rows — the header and the footer
// (contract §5 frame notes 1-2, mockgen.header / mockgen.footer) — plus the
// row primitive panel.go and overlay.go build on: a fixed-width buffer of
// independently styled cells, written left to right, a later write
// overwriting an earlier one, then flattened into one line by grouping
// consecutive cells that share an owner into a single styled run. This is
// what lets header/footer/panel borders be composed the way mockgen.Grid
// composes them without reasoning about escape-sequence byte offsets.
package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/stage"
)

// row is a fixed-width line of cells, each owned by at most one put call
// (or by none — literal, unstyled). put may be called more than once at
// overlapping positions; the later call wins for every cell it touches.
type row struct {
	w      int
	ch     []rune
	owner  []int // index into styles, or -1 for an untouched, literal cell
	styles []lipgloss.Style
}

// newRow returns a row of w blank (space), unstyled cells.
func newRow(w int) *row {
	if w < 0 {
		w = 0
	}
	ch := make([]rune, w)
	owner := make([]int, w)
	for i := range ch {
		ch[i] = ' '
		owner[i] = -1
	}
	return &row{w: w, ch: ch, owner: owner}
}

// put writes text starting at x, styled with st; characters that land
// outside [0,w) are dropped (mirroring mockgen.Grid.put). It returns
// x + the rune width of text, so callers can chain placement the way
// mockgen's g.put(...) return value does.
func (r *row) put(x int, text string, st lipgloss.Style) int {
	id := len(r.styles)
	r.styles = append(r.styles, st)
	for _, c := range text {
		if x >= 0 && x < r.w {
			r.ch[x] = c
			r.owner[x] = id
		}
		x++
	}
	return x
}

// render flattens the row into one string of exactly w cells, grouping
// consecutive same-owner cells into a single styled Render call.
func (r *row) render() string {
	var b strings.Builder
	i := 0
	for i < r.w {
		j := i + 1
		for j < r.w && r.owner[j] == r.owner[i] {
			j++
		}
		text := string(r.ch[i:j])
		if r.owner[i] == -1 {
			b.WriteString(text)
		} else {
			b.WriteString(r.styles[r.owner[i]].Render(text))
		}
		i = j
	}
	return b.String()
}

// screenTabName is a Screen's header/tab-bar label (mockgen.TABS).
var screenTabName = map[Screen]string{
	ScreenReview: "Review",
	ScreenAsk:    "Ask",
	ScreenLint:   "Lint",
	ScreenLog:    "Log",
	ScreenBrowse: "Browse",
}

// tabLabels is the header's tab bar, in screenOrder's order (app.go).
func tabLabels() []string {
	labels := make([]string, len(screenOrder))
	for i, s := range screenOrder {
		labels[i] = screenTabName[s]
	}
	return labels
}

// fitPaneLines forces content to exactly h lines of exactly w cells — the
// shell composes chrome around a pane it does not implement (backbone §12),
// so it enforces its own bound (Pad) rather than trusting every screen to
// get contract §4.2's invariant exactly right.
func fitPaneLines(content string, w, h int) []string {
	src := strings.Split(content, "\n")
	out := make([]string, h)
	for i := range out {
		var line string
		if i < len(src) {
			line = src[i]
		}
		out[i] = Pad(line, w)
	}
	return out
}

// footerContent picks the footer's row for the active pane p (which may be
// nil): its StatusReporter message when it has one, else its FooterHelper
// (or Help) bindings with the shell's own suffix appended (contract §5
// frame note 2, and the StatusReporter path).
//
// The suffix (ORCH-13/D-3T) is NextPane — "tab screen" — always, and Quit —
// "q quit" — unless p is taking text input (TextCapturer), appended after
// the pane's list and before "? help". No pane lists either binding any
// more: every screen's footer ends the same way, and the drop-from-end rule
// in footerLine applies to the combined list, so the suffix drops first at
// narrow widths.
func footerContent(t Theme, keys KeyMap, p Pane, w int) string {
	if sr, ok := p.(StatusReporter); ok {
		if msg, level := sr.Status(); msg != "" {
			return statusFooterLine(t, w, msg, level)
		}
	}
	var bindings []key.Binding
	if p != nil {
		if fh, ok := p.(FooterHelper); ok {
			bindings = fh.FooterHelp()
		} else {
			bindings = p.Help()
		}
		// Copy before appending: a pane may hand back a slice it shares, and
		// the suffix must never leak into it.
		combined := make([]key.Binding, 0, len(bindings)+2)
		combined = append(combined, bindings...)
		combined = append(combined, keys.NextPane)
		if !capturesText(p) {
			combined = append(combined, keys.Quit)
		}
		bindings = combined
	}
	return footerLine(t, w, bindings)
}

// capturesText reports whether p is taking text input (contract §5's
// TextCapturer): it implements the interface and reports true. A pane that
// types — Ask's input box — keeps `q` out of its footer, because `q` types.
func capturesText(p Pane) bool {
	tc, ok := p.(TextCapturer)
	return ok && tc.CapturesText()
}

// headerStage is the header's changeset summary (contract §5 frame note 6).
type headerStage struct {
	ID     string
	Ops    int
	Checks stage.Checks
	Has    bool
}

// opWord is the header's op-count noun (contract §5 note 1, clarified
// ORCH-3): singular "op" for exactly one, plural "ops" otherwise —
// "1 ops" reads as broken English even though mockgen.py's own fixture
// (4 ops) never exercises the singular.
func opWord(n int) string {
	if n == 1 {
		return "op"
	}
	return "ops"
}

// headerLine renders the shell's header row (contract §5 frame note 1,
// mockgen.header): the vault name, the tab bar in tabs' order with active
// highlighted, and a right-aligned summary ending at column w-2. The stats
// group (pages/raw/lint) is dropped first when the row is too narrow to
// hold everything.
func headerLine(t Theme, w int, vault string, tabs []string, active string,
	pages, raw, lintErrs int, stg headerStage) string {

	r := newRow(w)
	x := r.put(1, vault, t.Bold) + 3
	for _, name := range tabs {
		style := t.Muted
		if name == active {
			style = t.Accent.Bold(true)
		}
		x = r.put(x, name, style) + 2
	}

	type seg struct {
		text  string
		style lipgloss.Style
	}

	var right []seg
	if stg.Has {
		cs9 := stg.ID
		if len(cs9) > 9 {
			cs9 = cs9[:9]
		}
		glyph, style := "✗", t.Bad
		if stg.Checks.Schema == "pass" && stg.Checks.Lint == "pass" &&
			stg.Checks.Orphans == 0 && stg.Checks.BrokenLinks == 0 {
			glyph, style = "✓", t.Good
		}
		right = []seg{
			{cs9, t.Fg},
			{fmt.Sprintf(" · %d %s · checks ", stg.Ops, opWord(stg.Ops)), t.Muted},
			{glyph, style},
		}
	} else {
		right = []seg{{"no changeset", t.Faint}}
	}

	rlen := 0
	for _, s := range right {
		rlen += utf8.RuneCountInString(s.text)
	}

	stats := fmt.Sprintf("%d pages · %d raw · %d lint", pages, raw, lintErrs)
	if x+utf8.RuneCountInString(stats)+3+rlen+1 <= w {
		right = append([]seg{{stats, t.Muted}, {"   ", lipgloss.Style{}}}, right...)
		rlen += utf8.RuneCountInString(stats) + 3
	}

	rx := w - 1 - rlen
	for _, s := range right {
		rx = r.put(rx, s.text, s.style)
	}

	return r.render()
}

// footerEntry is one key/description pair the footer prints.
type footerEntry struct{ key, desc string }

// footerBindingEntries converts bindings into footerEntry rows, dropping any
// binding whose Keys() is empty (unbound) — contract §5 frame note 2.
func footerBindingEntries(bindings []key.Binding) []footerEntry {
	var out []footerEntry
	for _, b := range bindings {
		if len(b.Keys()) == 0 {
			continue
		}
		h := b.Help()
		out = append(out, footerEntry{key: h.Key, desc: h.Desc})
	}
	return out
}

// footerEntriesWidth is mockgen.footer's width(bs): the cell width bs would
// occupy if rendered as "key desc  key desc  …", plus the leading column.
func footerEntriesWidth(bs []footerEntry) int {
	n := 0
	for _, e := range bs {
		n += utf8.RuneCountInString(e.key) + 1 + utf8.RuneCountInString(e.desc)
	}
	return n + 2*(len(bs)-1) + 1
}

// footerLine renders the shell's footer row from bindings (contract §5
// frame note 2, mockgen.footer): "key desc" pairs from column 1, separated
// by two spaces, always ending with "? help". Whole bindings are dropped
// from the end — never mid-word — until the row fits within w-1.
func footerLine(t Theme, w int, bindings []key.Binding) string {
	last := footerEntry{key: "?", desc: "help"}
	bs := footerBindingEntries(bindings)
	for len(bs) > 0 && footerEntriesWidth(append(append([]footerEntry{}, bs...), last)) > w-1 {
		bs = bs[:len(bs)-1]
	}

	r := newRow(w)
	x := 1
	for _, e := range append(bs, last) {
		x = r.put(x, e.key, t.Bold) + 1
		x = r.put(x, e.desc, t.Muted) + 2
	}
	return r.render()
}

// statusFooterLine renders the footer's StatusReporter path (contract §5):
// the pane's transient message, styled by level and clipped to w-10, then
// "? help" — instead of the pane's bindings.
func statusFooterLine(t Theme, w int, msg string, level StatusLevel) string {
	style := t.Muted
	switch level {
	case StatusGood:
		style = t.Good
	case StatusWarn:
		style = t.Warn
	case StatusBad:
		style = t.Bad
	}
	msg = Clip(msg, w-10)

	r := newRow(w)
	x := r.put(1, msg, style) + 2
	x = r.put(x, "?", t.Bold) + 1
	r.put(x, "help", t.Muted)
	return r.render()
}

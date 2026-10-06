// deletemark_test.go is 047 S3's evidence: Review marks a patch_page op
// that deletes text. The reviewer sees the loss as one Bad-styled line
// directly under the op's header in the Diff panel —
//
//	deletes <N> line(s), <size> — text this op does not re-add
//
// — and a `−` after the basename in the Ops row when a cell is free for it.
// "Deletes" means what the op's OpDiff windows remove and never put back:
// a '-' line whose trimmed text no '+' line of the same op carries. Two
// shapes of window feed it — synthetic ones (op8-shaped: three paragraphs
// gone, one moved, a link added) so every count is exact, and ones from a
// real engine (a chained second patch, the op8 case: its windows exist but
// carry no hunk id) so the real input shape is proven end to end. The
// frozen 80-column check renders the real pane, never a hand-built string
// (the 033 clip lesson).
package review

import (
	"fmt"
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// dmTail is the part of the mark every line carries after the count and
// the size, byte for byte as the brief froze it (em dash included).
const dmTail = " — text this op does not re-add"

// dmDel, dmAdd and dmCtx build one DisplayLine of each kind.
func dmDel(text string) stage.DisplayLine { return stage.DisplayLine{Kind: '-', Text: text} }
func dmAdd(text string) stage.DisplayLine { return stage.DisplayLine{Kind: '+', Text: text} }
func dmCtx(text string) stage.DisplayLine { return stage.DisplayLine{Kind: ' ', Text: text} }

// dmText returns exactly n bytes of readable prose ending in a full stop —
// never whitespace-only, so TrimSpace never empties it.
func dmText(seed string, n int) string {
	s := strings.Repeat(seed+" ", n/(len(seed)+1)+1)[:n]
	return s[:n-1] + "."
}

// dmWindow is one unattributed DisplayHunk — the shape a chained patch's
// windows have (HunkID "": the trace cannot prove ownership) — over lines.
func dmWindow(lines ...stage.DisplayLine) stage.DisplayHunk {
	return stage.DisplayHunk{Header: "@@ -1,9 +1,3 @@", Lines: lines}
}

// dmFiles wraps windows as the one FileOpDiff a patch_page op carries.
func dmFiles(opID string, windows ...stage.DisplayHunk) []stage.FileOpDiff {
	return []stage.FileOpDiff{{
		Path:  "wiki/concepts/kv-cache.md",
		OpID:  opID,
		Kind:  stage.OpPatchPage,
		Hunks: windows,
	}}
}

// dmLoad opens a review pane over the minimal fixture with the two-hunk
// kv-cache patch staged and returns it with that patch_page op. A test then
// overwrites m.opDiffs[op.ID] with the windows it wants counted — the same
// move original_line_test makes for a dropped ingest.
func dmLoad(t *testing.T) (*Model, stage.Op) {
	t.Helper()
	d, e, _ := newTestDeps(t, "minimal")
	if _, err := e.OpenChangeset("047 S3 patch review", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	id := appendTwoHunkPatch(t, e)
	m, ok := initModel(t, d).(*Model)
	if !ok {
		t.Fatalf("pane is %T, want *review.Model", m)
	}
	op, found := findOp(m.ops, id)
	if !found {
		t.Fatalf("pane has no op %q", id)
	}
	if op.Kind != stage.OpPatchPage {
		t.Fatalf("op %s is %s, want patch_page", id, op.Kind)
	}
	return m, op
}

// dmRowHasMarker reports whether op's Ops row, at a roomy width, carries
// the `−` marker.
func dmRowHasMarker(m *Model, op stage.Op) bool {
	return strings.Contains(ansi.Strip(m.opsRow(op, 60)), "−")
}

// dmMarkLines renders op's Diff block at content width cw, returns its
// plain lines, and the index of the one that starts with "deletes " (-1
// when none does). More than one such line is a failure: the mark is one
// line.
func dmMarkLines(t *testing.T, m *Model, op stage.Op, cw int) (plain []string, at int) {
	t.Helper()
	at = -1
	for i, l := range m.opDiffLines(op, cw, false) {
		p := ansi.Strip(l.text)
		plain = append(plain, p)
		if strings.HasPrefix(p, "deletes ") {
			if at >= 0 {
				t.Fatalf("op block carries two delete marks (lines %d and %d)", at, i)
			}
			at = i
		}
	}
	return plain, at
}

// dmWantMark asserts the block shows exactly want directly under the op
// header (line 1, line 0 being the header).
func dmWantMark(t *testing.T, m *Model, op stage.Op, want string) {
	t.Helper()
	plain, at := dmMarkLines(t, m, op, 200)
	if at != 1 {
		t.Fatalf("delete mark at line %d of the block, want line 1 (directly under the header); block:\n%s",
			at, strings.Join(plain, "\n"))
	}
	if plain[at] != want {
		t.Errorf("delete mark = %q, want %q", plain[at], want)
	}
}

// dmWantNoMark asserts no line of the block is a delete mark.
func dmWantNoMark(t *testing.T, m *Model, op stage.Op) {
	t.Helper()
	plain, at := dmMarkLines(t, m, op, 200)
	if at >= 0 {
		t.Errorf("delete mark shown (%q), want none; block:\n%s", plain[at], strings.Join(plain, "\n"))
	}
}

// TestDeleteMarkCounts is the frozen op8 shape: three paragraphs removed
// (blank separators between them, one whitespace-only), a fourth removed in
// one window and re-added verbatim in another, and a link added. Only the
// three paragraphs count — N = 3, 120+150+142 = 412 B.
func TestDeleteMarkCounts(t *testing.T) {
	m, op := dmLoad(t)
	paraA, paraB, paraC := dmText("alpha", 120), dmText("bravo", 150), dmText("charlie", 142)
	kept := dmText("delta", 90)
	m.opDiffs[op.ID] = dmFiles(op.ID,
		dmWindow(
			dmCtx("## Abstract"),
			dmCtx(""),
			dmDel(paraA), dmDel(""), dmDel(paraB), dmDel("   "), dmDel(paraC), dmDel(""), dmDel(kept),
			dmCtx("## Why it matters"),
		),
		dmWindow(
			dmCtx("## Related"),
			dmAdd(""), dmAdd(kept),
			dmAdd("- [[nvitop]] — a GPU process monitor"),
		),
	)

	const want = "deletes 3 lines, 412 B" + dmTail
	dmWantMark(t, m, op, want)

	// Bad style, one line, nothing clipped at a wide panel: the styled
	// text is exactly the line in the theme's Bad style.
	lines := m.opDiffLines(op, 200, false)
	if styled := m.theme.Bad.Render(want); lines[1].text != styled {
		t.Errorf("mark is not Bad-styled: got %q, want %q", lines[1].text, styled)
	}
	if lines[1].cursor {
		t.Error("the mark is display-only: it must not carry the cursor mark")
	}
	if head := ansi.Strip(lines[0].text); !strings.Contains(head, op.ID) {
		t.Errorf("line 0 = %q, want the op header naming %s", head, op.ID)
	}
}

// TestDeleteMarkRenameOnlyNoMark: a patch whose every '-' line comes back
// verbatim as a '+' line — reordered, one re-indented — deletes nothing,
// however large it is. The control proves the shape is above both
// thresholds on its own, so the absence of a mark is the re-add rule's and
// not a threshold's.
func TestDeleteMarkRenameOnlyNoMark(t *testing.T) {
	m, op := dmLoad(t)
	lines := []string{dmText("one", 80), dmText("two", 80), dmText("three", 80), dmText("four", 80)}

	control := []stage.DisplayLine{dmDel(lines[0]), dmDel(lines[1]), dmDel(lines[2]), dmDel(lines[3])}
	m.opDiffs[op.ID] = dmFiles(op.ID, dmWindow(control...))
	dmWantMark(t, m, op, "deletes 4 lines, 320 B"+dmTail)

	withAdds := append([]stage.DisplayLine{}, control...)
	withAdds = append(withAdds, dmAdd(lines[2]), dmAdd(lines[0]), dmAdd("  "+lines[3]+"  "), dmAdd(lines[1]))
	m.opDiffs[op.ID] = dmFiles(op.ID, dmWindow(withAdds...))
	dmWantNoMark(t, m, op)
}

// TestDeleteMarkSmallRemovalNoMark: two lines and 100 bytes is below both
// thresholds. The control (a third line) crosses the line threshold with
// the same bytes per line.
func TestDeleteMarkSmallRemovalNoMark(t *testing.T) {
	m, op := dmLoad(t)
	a, b, c := dmText("small", 50), dmText("tiny", 50), dmText("minor", 50)

	m.opDiffs[op.ID] = dmFiles(op.ID, dmWindow(dmCtx("## Related"), dmDel(a), dmDel(b), dmAdd("- [[x]] — y")))
	dmWantNoMark(t, m, op)

	m.opDiffs[op.ID] = dmFiles(op.ID, dmWindow(dmDel(a), dmDel(b), dmDel(c)))
	dmWantMark(t, m, op, "deletes 3 lines, 150 B"+dmTail)
}

// TestDeleteMarkThresholds pins both comparisons at their edges: three
// lines OR 200 bytes, inclusive, over '-' lines counted by their raw len
// (indentation included) with blank lines ignored.
func TestDeleteMarkThresholds(t *testing.T) {
	rows := []struct {
		name string
		dels []string
		want string // "" = no mark
	}{
		{"two lines, 198 B", []string{dmText("a", 99), dmText("b", 99)}, ""},
		{"two lines, exactly 200 B", []string{dmText("a", 100), dmText("b", 100)}, "deletes 2 lines, 200 B"},
		{"one line, 199 B", []string{dmText("a", 199)}, ""},
		{"one line, exactly 200 B", []string{dmText("a", 200)}, "deletes 1 line, 200 B"},
		{"three short lines", []string{"x.", "y.", "z."}, "deletes 3 lines, 6 B"},
		{"blank lines do not count", []string{"", "  ", "\t", "", "x."}, ""},
		{"raw len, indentation included", []string{strings.Repeat(" ", 60) + dmText("a", 150)}, "deletes 1 line, 210 B"},
		{"1023 B stays in bytes", []string{dmText("a", 1023)}, "deletes 1 line, 1023 B"},
		{"1024 B is 1.0 KB", []string{dmText("a", 1024)}, "deletes 1 line, 1.0 KB"},
		{"1434 B is 1.4 KB", []string{dmText("a", 1434)}, "deletes 1 line, 1.4 KB"},
	}
	m, op := dmLoad(t)
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var ls []stage.DisplayLine
			for _, d := range row.dels {
				ls = append(ls, dmDel(d))
			}
			m.opDiffs[op.ID] = dmFiles(op.ID, dmWindow(ls...))
			if row.want == "" {
				dmWantNoMark(t, m, op)
				return
			}
			dmWantMark(t, m, op, row.want+dmTail)
		})
	}
}

// TestDeleteMarkOnlyPatchPage: the mark is for patch_page ops. A '-' line
// in any other kind's windows (a rename's, an ingest's) is not a loss this
// line speaks for.
func TestDeleteMarkOnlyPatchPage(t *testing.T) {
	m, op := dmLoad(t)
	m.opDiffs[op.ID] = dmFiles(op.ID, dmWindow(dmDel(dmText("a", 120)), dmDel(dmText("b", 120)), dmDel(dmText("c", 120))))
	dmWantMark(t, m, op, "deletes 3 lines, 360 B"+dmTail)

	for _, kind := range []stage.OpKind{stage.OpCreatePage, stage.OpIngestSource, stage.OpRenamePage} {
		other := op
		other.Kind = kind
		dmWantNoMark(t, m, other)
		if dmRowHasMarker(m, other) {
			t.Errorf("Ops row marks a %s op", kind)
		}
	}
}

// TestDeleteMarkIgnoresDroppedWindows: a window the reviewer dropped will
// not land, so it neither makes the loss nor re-adds anything. Dropping the
// hunk that deletes clears the mark; dropping the hunk that re-adds
// brings it back; a dropped op shows none.
func TestDeleteMarkIgnoresDroppedWindows(t *testing.T) {
	a, b, c := dmText("a", 110), dmText("b", 110), dmText("c", 110)
	deleting := func(dropped bool) stage.DisplayHunk {
		w := dmWindow(dmDel(a), dmDel(b), dmDel(c))
		w.HunkID, w.Dropped = "h1", dropped
		return w
	}
	readding := func(dropped bool) stage.DisplayHunk {
		w := dmWindow(dmAdd(a))
		w.HunkID, w.Dropped = "h2", dropped
		return w
	}

	m, op := dmLoad(t)

	m.opDiffs[op.ID] = dmFiles(op.ID, deleting(false), readding(false))
	dmWantMark(t, m, op, "deletes 2 lines, 220 B"+dmTail)

	m.opDiffs[op.ID] = dmFiles(op.ID, deleting(true), readding(false))
	dmWantNoMark(t, m, op)

	m.opDiffs[op.ID] = dmFiles(op.ID, deleting(false), readding(true))
	dmWantMark(t, m, op, "deletes 3 lines, 330 B"+dmTail)

	// Every window dropped: the op presents as dropped, struck through,
	// with no mark and no marker.
	m.opDiffs[op.ID] = dmFiles(op.ID, deleting(true), readding(true))
	dmWantNoMark(t, m, op)
	if dmRowHasMarker(m, op) {
		t.Error("Ops row marks an op whose every window is dropped")
	}
}

// TestDeleteMarkClip is the frozen 80-column check, on the real pane over a
// real engine: one patch_page that drops three real kv-cache lines for a
// link. At 80x22 the Diff panel's line under the op header starts with
// `deletes 3 lines`, and when the panel is narrower than the line the fact
// survives and the tail is what the clip eats.
func TestDeleteMarkClip(t *testing.T) {
	d, e, _ := newTestDeps(t, "minimal")
	id := appendDeletingKvPatch(t, e, "")
	m, ok := initModel(t, d).(*Model)
	if !ok {
		t.Fatalf("pane is %T, want *review.Model", m)
	}
	if _, found := findOp(m.ops, id); !found {
		t.Fatalf("pane has no op %q", id)
	}

	// The whole pane at 80 columns: header row, then the mark row.
	_, screen := uitest.PaneScreen(m, 80, 22)
	rows := strings.Split(screen, "\n")
	top := -1
	for i, r := range rows {
		if strings.HasPrefix(r, "╭ Diff") {
			top = i
			break
		}
	}
	if top < 0 || top+2 >= len(rows) {
		t.Fatalf("no Diff panel in the 80x22 pane:\n%s", screen)
	}
	if !strings.Contains(rows[top+1], "kv-cache.md") {
		t.Fatalf("first Diff row is not the op header: %q", rows[top+1])
	}
	if got := strings.TrimSuffix(strings.TrimPrefix(rows[top+2], "│ "), "│"); !strings.HasPrefix(got, "deletes 3 lines") {
		t.Errorf("row under the op header at 80 columns = %q, want it to start with %q", got, "deletes 3 lines")
	}
	if !strings.Contains(rows[top+2], "deletes 3 lines, 218 B"+dmTail) {
		t.Errorf("row under the op header = %q, want the full line at 80 columns", rows[top+2])
	}

	// A panel narrower than the line: fact first, "…" last, never wider.
	for _, cw := range []int{40, 20, 16} {
		_, _, lines := m.detailContent(cw)
		var got string
		for i, l := range lines {
			if strings.Contains(ansi.Strip(l.text), "kv-cache.md") && i+1 < len(lines) {
				got = ansi.Strip(lines[i+1].text)
				break
			}
		}
		if !strings.HasPrefix(got, "deletes 3 lines") {
			t.Errorf("cw=%d: line under the header = %q, want it to start with %q", cw, got, "deletes 3 lines")
		}
		if w := lipgloss.Width(got); w > cw {
			t.Errorf("cw=%d: mark is %d cells wide: %q", cw, w, got)
		}
		if !strings.HasSuffix(got, "…") {
			t.Errorf("cw=%d: clipped mark lost its trailing ellipsis: %q", cw, got)
		}
	}
}

// TestDeleteMarkNoTabs is A-047-1's regression test. tmux 3.6 `capture-pane`
// showed three literal TAB bytes between the mark and the panel border in
// the live TUI. Those tabs are not in anything this package builds: the
// bubbletea renderer erases a run of blank cells (ECH) and moves the cursor
// past it with hard tabs (HT), and tmux 3.6 records the cells it crossed as
// tab cells — the same `…\t\t\t│` shows on Ask and Browse lines that have
// nothing to do with the mark (wire capture: `\x1b[19X\t\t\t\t│`). What
// this package owns is the string View hands the renderer, and that must
// pad with spaces and never carry a tab: asserted on the real pane at the
// three widths the acceptance run uses, styled and plain, and on the Detail
// lines themselves. The marked line must be present at every size, or the
// absence of tabs proves nothing.
func TestDeleteMarkNoTabs(t *testing.T) {
	d, e, _ := newTestDeps(t, "minimal")
	appendDeletingKvPatch(t, e, "")
	m, ok := initModel(t, d).(*Model)
	if !ok {
		t.Fatalf("pane is %T, want *review.Model", m)
	}

	for _, sz := range [][2]int{{80, 22}, {120, 30}, {200, 58}} {
		w, h := sz[0], sz[1]
		styled, plain := uitest.PaneScreen(m, w, h)
		if !strings.Contains(plain, "deletes 3 lines, 218 B"+dmTail) {
			t.Fatalf("%dx%d: pane carries no full delete mark:\n%s", w, h, plain)
		}
		uitest.AssertGrid(t, plain, w, h)
		for _, view := range []struct{ name, text string }{{"styled", styled}, {"plain", plain}} {
			if i := strings.IndexByte(view.text, '\t'); i >= 0 {
				t.Errorf("%dx%d %s pane carries a TAB at byte %d: %q", w, h, view.name, i, view.text[max(0, i-40):min(len(view.text), i+8)])
			}
		}

		_, _, lines := m.detailContent(w - 4)
		for i, l := range lines {
			if strings.ContainsRune(l.text, '\t') {
				t.Errorf("%dx%d: Detail line %d carries a TAB: %q", w, h, i, l.text)
			}
		}
	}
}

// TestDeleteMarkChainedOp is the op8 case end to end: a second patch on a
// page the changeset already patched chains on the first's staged After, its
// windows exist but carry no hunk id (the 030 debt), and the mark still
// counts what they remove. The first op, which only adds a line, shows no
// mark.
func TestDeleteMarkChainedOp(t *testing.T) {
	d, e, _ := newTestDeps(t, "minimal")
	first := appendAddOnlyKvPatch(t, e)
	second := appendDeletingKvPatch(t, e, first)
	m, ok := initModel(t, d).(*Model)
	if !ok {
		t.Fatalf("pane is %T, want *review.Model", m)
	}

	for _, f := range m.opDiffs[second] {
		for _, w := range f.Hunks {
			if w.HunkID != "" {
				t.Fatalf("chained op's window carries hunk id %q; the test is no longer the op8 shape", w.HunkID)
			}
		}
	}
	if countWindows(m.opDiffs[second], false) == 0 {
		t.Fatal("chained op has no windows")
	}

	op1, _ := findOp(m.ops, first)
	op2, _ := findOp(m.ops, second)
	dmWantNoMark(t, m, op1)
	dmWantMark(t, m, op2, "deletes 3 lines, 218 B"+dmTail)
}

// TestDeleteMarkOpsRow: the Ops row gains a `−` in the Bad style after the
// basename, only when a cell is free there. A swept width proves the
// basename is never clipped to make room: wherever the marker is absent the
// row is byte-identical to the row of an op that deletes nothing.
func TestDeleteMarkOpsRow(t *testing.T) {
	m, op := dmLoad(t)
	deleting := dmFiles(op.ID, dmWindow(dmDel(dmText("a", 120)), dmDel(dmText("b", 120)), dmDel(dmText("c", 120))))
	harmless := dmFiles(op.ID, dmWindow(dmCtx("## Related"), dmAdd("- [[x]] — y")))

	rowAt := func(files []stage.FileOpDiff, cw int) string {
		m.opDiffs[op.ID] = files
		return m.opsRow(op, cw)
	}

	// Roomy: marker right after the basename, then the usual directory hint.
	if got, want := ansi.Strip(rowAt(deleting, 60)), "● patch kv-cache.md −  wiki/concepts/"; got != want {
		t.Errorf("row at cw=60 = %q, want %q", got, want)
	}
	if styled := rowAt(deleting, 60); !strings.Contains(styled, m.theme.Bad.Render("−")) {
		t.Errorf("marker is not Bad-styled in %q", styled)
	}
	if got := ansi.Strip(rowAt(harmless, 60)); strings.Contains(got, "−") {
		t.Errorf("row of an op that deletes nothing carries a marker: %q", got)
	}

	const base = "kv-cache.md"
	edge := 8 + len(base) // the width at which the basename exactly fits
	for cw := 0; cw <= 80; cw++ {
		marked, plain := ansi.Strip(rowAt(deleting, cw)), ansi.Strip(rowAt(harmless, cw))
		hasMarker := strings.Contains(marked, "−")
		if want := cw >= edge+2; hasMarker != want {
			t.Errorf("cw=%d: marker present = %v, want %v (basename edge %d): %q", cw, hasMarker, want, edge, marked)
		}
		if !hasMarker {
			if marked != plain {
				t.Errorf("cw=%d: no marker, yet the row changed: %q vs %q", cw, marked, plain)
			}
			continue
		}
		if !strings.Contains(marked, base+" −") {
			t.Errorf("cw=%d: basename lost or marker detached: %q", cw, marked)
		}
		if w := lipgloss.Width(marked); w > cw {
			t.Errorf("cw=%d: row is %d cells wide: %q", cw, w, marked)
		}
	}
}

// TestDeleteMarkChangesetPanelUnchanged: the Changeset panel gains nothing.
// The same op with one harmless window (the same hunk count) and with the
// deleting window must render the identical panel, and neither mentions a
// deletion.
func TestDeleteMarkChangesetPanelUnchanged(t *testing.T) {
	m, op := dmLoad(t)
	rows := func(files []stage.FileOpDiff) []string {
		m.opDiffs[op.ID] = files
		return m.changesetRows()
	}
	harmless := rows(dmFiles(op.ID, dmWindow(dmCtx("## Related"), dmAdd("- [[x]] — y"))))
	deleting := rows(dmFiles(op.ID, dmWindow(dmDel(dmText("a", 120)), dmDel(dmText("b", 120)), dmDel(dmText("c", 120)))))
	if len(harmless) == 0 || strings.Join(harmless, "\n") != strings.Join(deleting, "\n") {
		t.Errorf("Changeset panel changed with the deletion:\n%q\nvs\n%q", deleting, harmless)
	}
	for _, r := range deleting {
		if p := ansi.Strip(r); strings.Contains(p, "delete") || strings.Contains(p, "−") {
			t.Errorf("Changeset panel row mentions the deletion: %q", p)
		}
	}
}

// TestDeleteMarkNotInPreview: Preview renders the staged page, not the
// diff — the mark is a Diff-mode line only.
func TestDeleteMarkNotInPreview(t *testing.T) {
	m, op := dmLoad(t)
	m.opDiffs[op.ID] = dmFiles(op.ID, dmWindow(dmDel(dmText("a", 120)), dmDel(dmText("b", 120)), dmDel(dmText("c", 120))))
	m.preview = true
	_, _, lines := m.detailContent(100)
	for _, l := range lines {
		if p := ansi.Strip(l.text); strings.Contains(p, "deletes ") {
			t.Errorf("Preview shows the delete mark: %q", p)
		}
	}
}

// TestDeleteMarkSize pins the size the mark quotes — stage.HumanSize since 054
// (A-054-1), which replaced this package's same-output copy — to the rows
// stage's own TestHumanSize pins for the ingest original line, plus the unit
// boundaries the brief's examples sit between ("412 B", "1.4 KB").
func TestDeleteMarkSize(t *testing.T) {
	rows := []struct {
		n    int
		want string
	}{
		{0, "0 B"},
		{412, "412 B"},
		{900, "900 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1434, "1.4 KB"},
		{3072, "3.0 KB"},
		{1048575, "1024.0 KB"},
		{1048576, "1.0 MB"},
		{1153434, "1.1 MB"},
	}
	for _, row := range rows {
		if got := stage.HumanSize(row.n); got != row.want {
			t.Errorf("stage.HumanSize(%d) = %q, want %q", row.n, got, row.want)
		}
	}
}

// TestDeleteMarkSizeMatchesOriginalLine is the drift guard for the shared
// humanizer: for blobs of several sizes, the size stage writes into an
// ingest's original line (on a real engine) is the size stage.HumanSize
// renders for the same byte count, which is what the delete mark quotes — the
// brief's "same humanizer as the original line" (A-054-1: it once compared
// against a copy, now it pins the one function).
func TestDeleteMarkSizeMatchesOriginalLine(t *testing.T) {
	for _, n := range []int{900, 1023, 1024, 3072, 1048575, 1048576, 1153434} {
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			d, e, _ := newTestDeps(t, "minimal")
			blob := make([]byte, n)
			for i := range blob {
				blob[i] = byte('a' + i%26)
			}
			copy(blob, "%PDF-1.6 fake pdf for the 047 S3 size check\n")
			if _, err := e.OpenChangeset("047 S3 size check", stage.Author{Kind: "agent", Model: "test"}); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			if _, err := e.Append(stage.Op{
				Kind:            stage.OpIngestSource,
				Path:            origLineRawPath,
				Extractor:       "docling/pdf",
				Rationale:       "the S3 size check",
				Content:         origLineRawFile(blob),
				OriginalPath:    origLinePdfPath,
				Original:        origLineSha(blob),
				OriginalContent: blob,
			}); err != nil {
				t.Fatalf("Append ingest with a %d-byte original: %v", n, err)
			}
			m, ok := initModel(t, d).(*Model)
			if !ok {
				t.Fatalf("pane is %T, want *review.Model", m)
			}
			var line string
			for _, fs := range m.opDiffs {
				for _, f := range fs {
					if f.OriginalLine != "" {
						line = f.OriginalLine
					}
				}
			}
			size, _, found := strings.Cut(strings.TrimPrefix(line, "original ("), ",")
			if line == "" || !found {
				t.Fatalf("OpDiff carries no original line (got %q)", line)
			}
			if want := stage.HumanSize(n); size != want {
				t.Errorf("original line says %q for %d bytes, stage.HumanSize says %q", size, n, want)
			}
		})
	}
}

// kvDeletedLines are three real consecutive lines of the minimal fixture's
// kv-cache "## Abstract" paragraph: 71 + 74 + 73 = 218 bytes.
var kvDeletedLines = []string{
	"The key/value cache stores per-layer attention projections from earlier",
	"decoding steps so a new token attends to cached keys and values instead of",
	"recomputing them. This turns per-token work into a constant amount of new",
}

// appendDeletingKvPatch stages a real patch_page on the fixture's kv-cache
// page that replaces kvDeletedLines with one link line, and returns its op
// id. A non-empty chainOn is the id of an earlier op on the same page: the
// new op chains on that op's staged After (the 020 dependent-op shape).
func appendDeletingKvPatch(t *testing.T, e *stage.Engine, chainOn string) string {
	t.Helper()
	const link = "- [[nvitop]] — a GPU process monitor"
	return appendKvHunkOp(t, e, chainOn, "## Abstract", kvDeletedLines, []string{link})
}

// appendAddOnlyKvPatch stages a real patch_page that only adds one line (no
// '-' line anywhere in its windows) and returns its op id.
func appendAddOnlyKvPatch(t *testing.T, e *stage.Engine) string {
	t.Helper()
	return appendKvHunkOp(t, e, "", "## Related", nil, []string{"- [[gpt-4]] — see also"})
}

// appendKvHunkOp is the shared builder: one hunk with the given Del and Add
// lines applied to the staged (or, unchained, committed) kv-cache page.
// Content is derived from the same page bytes, so the traced and stored
// post-images agree.
func appendKvHunkOp(t *testing.T, e *stage.Engine, chainOn, section string, del, add []string) string {
	t.Helper()
	const kvPath = "wiki/concepts/kv-cache.md"
	if _, err := e.Current(); err != nil {
		if _, err := e.OpenChangeset("047 S3 patch review", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
	}
	page, ok := e.Vault().Page(kvPath)
	if !ok {
		t.Fatalf("fixture missing %s", kvPath)
	}
	before, base := page.SHA256(), string(page.Serialize())
	if chainOn != "" {
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		prev, found := cs.Op(chainOn)
		if !found {
			t.Fatalf("changeset has no op %q", chainOn)
		}
		before = prev.After
		staged, found, err := e.StagedFile(kvPath)
		if err != nil || !found {
			t.Fatalf("StagedFile(%s): found=%v err=%v", kvPath, found, err)
		}
		base = string(staged)
	}

	var content string
	switch {
	case len(del) > 0:
		old := strings.Join(del, "\n")
		if !strings.Contains(base, old) {
			t.Fatalf("page does not contain the lines to delete:\n%s", old)
		}
		content = strings.Replace(base, old, strings.Join(add, "\n"), 1)
	default:
		// Add-only: the extras land at the end of the body.
		content = strings.TrimRight(base, "\n") + "\n" + strings.Join(add, "\n") + "\n"
	}
	id, err := e.Append(stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      kvPath,
		Section:   section,
		Before:    before,
		Content:   []byte(content),
		Rationale: fmt.Sprintf("047 S3: %d lines out, %d in", len(del), len(add)),
		Hunks:     []stage.Hunk{{ID: "h1", Path: kvPath, Del: del, Add: add}},
	})
	if err != nil {
		t.Fatalf("Append patch_page (chainOn=%q): %v", chainOn, err)
	}
	return id
}

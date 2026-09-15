package lintview

import (
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// plainView renders m at w×h and returns the plain (ANSI-stripped) text
// through the harness's PaneScreen — the same render path the goldens take,
// so column arithmetic in these tests runs on cells, not escape bytes.
func plainView(m ui.Pane, w, h int) string {
	_, plain := uitest.PaneScreen(m, w, h)
	return plain
}

// syntheticDeps builds ui.Deps with no engine at all: the view tests below
// install a synthetic lint.Report straight onto the model (reportMsg is
// this package's own message), so alignment and glyphs are asserted
// independently of any fixture vault's lint state.
func syntheticDeps(t *testing.T) ui.Deps {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	return ui.Deps{Theme: theme, Keys: keys}
}

// withReport returns a loaded model showing findings.
func withReport(t *testing.T, d ui.Deps, findings []lint.Finding) *Model {
	t.Helper()
	p := feedMsg(t, New(d), reportMsg{report: lint.Report{
		Findings: findings,
		ByCheck:  groupByCheck(findings),
	}})
	return p.(*Model)
}

// groupByCheck rebuilds the ByCheck view a real lint.Run produces.
func groupByCheck(findings []lint.Finding) map[string][]lint.Finding {
	by := map[string][]lint.Finding{}
	for _, f := range findings {
		by[f.Check] = append(by[f.Check], f)
	}
	return by
}

// splitRows splits a View render into its rows.
func splitRows(plain string) []string { return strings.Split(plain, "\n") }

// nonPanelLines counts the panel's content rows: the rows between the top
// and bottom borders that are not blank padding.
func nonPanelLines(lines []string) []string {
	if len(lines) < 3 {
		return nil
	}
	var out []string
	for _, l := range lines[1 : len(lines)-1] {
		trimmed := strings.Trim(l, "│ ")
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// rowCells decodes one rendered row into runes — column arithmetic is in
// cells, and the glyphs (`✗`, `!`, `·`) are multi-byte.
func rowCells(row string) []rune { return []rune(row) }

// TestFindingRowColumnsAllSeverities is the brief's synthetic-render check:
// a report carrying all three severities renders one row per severity with
// the frozen glyphs (`✗` error, `!` warn, `·` info) and every column of
// every row starting at the same cell — glyph, check name, path, message.
func TestFindingRowColumnsAllSeverities(t *testing.T) {
	d := syntheticDeps(t)
	m := withReport(t, d, []lint.Finding{
		{Check: "link-broken", Path: "wiki/concepts/kv-cache.md", Line: 12,
			Severity: lint.SevError, Message: "[[missing]] resolves to nothing; fix the target or create the page"},
		{Check: "src-stale", Path: "wiki/entities/bench.md",
			Severity: lint.SevWarn, Message: "updated 2025-01-01 is more than 90 days before the source was ingested"},
		{Check: "fm-quality", Path: "wiki/comparisons/one.md",
			Severity: lint.SevInfo, Message: "confidence is low; corroborate with another source or raise the confidence"},
		{Check: "log-rotate", Path: "",
			Severity: lint.SevInfo, Message: "log.md exceeds 500 entries; rotate it"},
	})

	plain := plainView(m, 120, 8)
	lines := splitRows(plain)
	if len(lines) != 8 {
		t.Fatalf("View(120, 8) rendered %d rows, want 8", len(lines))
	}

	// The check column pads to the longest ID in the report: log-rotate,
	// at 10 cells (under the 18 cap). Panel content starts at cell 2
	// (border + gutter), so the columns sit at: glyph 2, check 5, path 17.
	const (
		glyphCol = 2
		nameCol  = 5
		pathCol  = nameCol + 10 + 2
	)
	// The render keeps the report's own order; each row's glyph follows its
	// severity and every column starts at the same cell.
	want := []struct {
		glyph rune
		check string
	}{
		{'✗', "link-broken"},
		{'!', "src-stale"},
		{'·', "fm-quality"},
		{'·', "log-rotate"},
	}

	rows := 0
	for i, line := range lines[1 : len(lines)-1] {
		cells := rowCells(line)
		if cells[0] != '│' {
			t.Fatalf("row %d does not start with the panel border: %q", i+1, line)
		}
		if cells[glyphCol] == ' ' {
			continue // blank padding row below the findings
		}
		if rows >= len(want) {
			t.Fatalf("row %d is an unexpected extra finding row: %q", i+1, line)
		}
		w := want[rows]
		rows++
		if got := cells[glyphCol]; got != w.glyph {
			t.Fatalf("row %d glyph = %q, want %q", i+1, got, w.glyph)
		}
		if got := string(cells[nameCol : nameCol+len(w.check)]); got != w.check {
			t.Fatalf("row %d check column = %q, want %q at cell %d", i+1, got, w.check, nameCol)
		}
		if cells[nameCol-1] != ' ' || cells[nameCol+len(w.check)] != ' ' {
			t.Fatalf("row %d check column is not surrounded by the two-space gaps: %q", i+1, line)
		}
		if cells[pathCol-1] != ' ' {
			t.Fatalf("row %d path column does not start after two spaces at cell %d: %q", i+1, pathCol, line)
		}
		if got := string(cells[pathCol : pathCol+5]); got == "     " {
			t.Fatalf("row %d has an empty path column at %d; alignment broke", i+1, pathCol)
		}
	}
	if rows != len(want) {
		t.Fatalf("rendered %d finding rows, want %d\n%s", rows, len(want), plain)
	}

	// The vault-wide finding has no path to show: its location column reads
	// (vault-wide) rather than an empty run of cells.
	foundVaultWide := false
	for _, line := range lines {
		if strings.Contains(line, "(vault-wide)") {
			foundVaultWide = true
		}
	}
	if !foundVaultWide {
		t.Fatalf("the vault-wide finding's location is not rendered:\n%s", plain)
	}
}

// TestCheckColumnCappedAt18 pins the 18-cell cap: a report whose longest
// check ID exceeds it clips the ID into an 18-cell column — ui.Pad's clip
// puts `…` in the last cell — never wider.
func TestCheckColumnCappedAt18(t *testing.T) {
	d := syntheticDeps(t)
	m := withReport(t, d, []lint.Finding{
		{Check: "a-very-long-check-id", Path: "wiki/x.md", Severity: lint.SevWarn, Message: "boom"},
	})

	plain := plainView(m, 120, 5)
	lines := splitRows(plain)
	cells := rowCells(lines[1])
	nameCol := 5
	// Cells nameCol..nameCol+17 are the clipped 18-cell name, then the
	// two-space gap, then the path.
	if got := string(cells[nameCol : nameCol+18]); got != "a-very-long-check…" {
		t.Fatalf("check column = %q, want the ID clipped into 18 cells", got)
	}
	if got := string(cells[nameCol+18 : nameCol+25]); got != "  wiki/" {
		t.Fatalf("path column starts at cell %d, got %q", nameCol+20, got)
	}
}

// TestEmptyReportShowsCleanNote pins the empty-report state (T09): the
// first content row reads `no findings` (faint), the top border's note is
// `clean`, and the bottom border carries no FootNote.
func TestEmptyReportShowsCleanNote(t *testing.T) {
	d := syntheticDeps(t)
	m := withReport(t, d, nil)

	plain := plainView(m, 80, 10)
	lines := splitRows(plain)
	if len(lines) != 10 {
		t.Fatalf("View(80, 10) rendered %d rows, want 10", len(lines))
	}
	if !strings.HasPrefix(lines[0], "╭ Findings ") {
		t.Fatalf("top border = %q, want the Findings title", lines[0])
	}
	if !strings.HasSuffix(lines[0], "─ clean ╮") {
		t.Fatalf("top border note = %q, want a right-aligned `clean`", lines[0])
	}
	if !strings.Contains(lines[1], "no findings") {
		t.Fatalf("first content row = %q, want `no findings`", lines[1])
	}
	if got := nonPanelLines(lines); len(got) != 1 {
		t.Fatalf("empty report rendered %d content rows, want 1", len(got))
	}
	if bottom := lines[len(lines)-1]; !strings.HasPrefix(bottom, "╰") || strings.Contains(bottom, "of") {
		t.Fatalf("bottom border = %q, want a bare border with no FootNote", bottom)
	}
}

// TestFootNoteAndCursorGutter pins the frame chrome on a non-empty report:
// the bottom border reads `i of N` and the gutter cell of the cursor's
// content row is ▌ while every other row keeps a blank one.
func TestFootNoteAndCursorGutter(t *testing.T) {
	d := syntheticDeps(t)
	m := withReport(t, d, []lint.Finding{
		{Check: "aaa", Path: "wiki/a.md", Severity: lint.SevWarn, Message: "first"},
		{Check: "bbb", Path: "wiki/b.md", Severity: lint.SevError, Message: "second"},
		{Check: "ccc", Path: "wiki/c.md", Severity: lint.SevInfo, Message: "third"},
	})

	p, _ := m.handleKey(keyMsg("j")) // cursor to the second finding
	m = p.(*Model)

	plain := plainView(m, 80, 10)
	lines := splitRows(plain)
	if !strings.HasSuffix(lines[len(lines)-1], "─ 2 of 3 ╯") {
		t.Fatalf("bottom border = %q, want the `2 of 3` FootNote", lines[len(lines)-1])
	}
	for i, line := range lines[1 : len(lines)-1] {
		cells := rowCells(line)
		want := ' '
		if i == 1 {
			want = '▌'
		}
		if cells[1] != want {
			t.Fatalf("content row %d gutter = %q, want %q", i+1, cells[1], want)
		}
	}
}

// TestFindingsScrollKeepsCursorVisible pins the scrolled window: with more
// findings than the panel holds rows, the window keeps the cursor row
// visible and the FootNote keeps reporting the absolute position.
func TestFindingsScrollKeepsCursorVisible(t *testing.T) {
	d := syntheticDeps(t)
	var findings []lint.Finding
	for i := 0; i < 40; i++ {
		findings = append(findings, lint.Finding{
			Check:    fmt.Sprintf("check-%02d", i),
			Path:     fmt.Sprintf("wiki/page-%02d.md", i),
			Severity: lint.SevWarn,
			Message:  fmt.Sprintf("finding %02d", i),
		})
	}
	m := withReport(t, d, findings)

	p, _ := m.handleKey(keyMsg("G")) // to the last finding
	m = p.(*Model)

	plain := plainView(m, 80, 12) // innerH = 10, half the list
	lines := splitRows(plain)
	if !strings.HasSuffix(lines[len(lines)-1], "─ 40 of 40 ╯") {
		t.Fatalf("bottom border = %q, want `40 of 40`", lines[len(lines)-1])
	}
	if !strings.Contains(lines[1], "check-30") {
		t.Fatalf("first visible row = %q, want the window to have scrolled to check-30", lines[1])
	}
	if strings.Contains(plain, "check-00") {
		t.Fatal("the list did not scroll: finding 00 is still visible past the cursor")
	}

	// j walks down one row within the same window; the FootNote follows.
	p, _ = m.handleKey(keyMsg("j"))
	m = p.(*Model)
	if got := plainView(m, 80, 12); !strings.Contains(splitRows(got)[len(splitRows(got))-1], "─ 40 of 40 ╯") {
		t.Fatalf("FootNote after j = %q, want `40 of 40`", splitRows(got)[len(splitRows(got))-1])
	}
}

// TestLoadErrorAndLoadingStates pins the two non-report states: a pane with
// no engine shows the error as the panel's first content row, and a pane
// whose report has not landed shows `loading lint report…`.
func TestLoadErrorAndLoadingStates(t *testing.T) {
	d := syntheticDeps(t)

	m := feedMsg(t, New(d), reportMsg{err: errNoEngine}).(*Model)
	plain := m.View(80, 6)
	if !strings.Contains(plain, "lint: lintview: no engine loaded") {
		t.Fatalf("error state = %q, want the load error as a content row", firstContentRow(t, plain))
	}

	m = New(d).(*Model) // no report yet
	if got := plainView(m, 80, 6); !strings.Contains(got, "loading lint report…") {
		t.Fatalf("loading state = %q, want `loading lint report…`", firstContentRow(t, got))
	}
}

// firstContentRow returns the second row of a render (the panel's first
// content row) for message assertions.
func firstContentRow(t *testing.T, plain string) string {
	t.Helper()
	lines := splitRows(plain)
	if len(lines) < 2 {
		t.Fatalf("render has %d rows, want at least 2:\n%s", len(lines), plain)
	}
	return lines[1]
}

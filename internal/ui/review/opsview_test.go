package review

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// plain strips the styling off a rendered row, so tests can compare the
// text the frozen grids show.
func plain(s string) string { return ansi.Strip(s) }

// testTheme loads the compiled-in default theme hermetically for pure
// renderer tests that need no engine.
func testTheme(t *testing.T) ui.Theme {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	return theme
}

// windowsWith builds a FileOpDiff list whose hunks are dropped when the
// matching flag is set, the shape the glyph and count helpers read.
func windowsWith(dropped ...bool) []stage.FileOpDiff {
	var hunks []stage.DisplayHunk
	for _, d := range dropped {
		hunks = append(hunks, stage.DisplayHunk{HunkID: "h1", Dropped: d})
	}
	return []stage.FileOpDiff{{Path: "wiki/x.md", Hunks: hunks}}
}

func TestGlyphFor(t *testing.T) {
	theme := testTheme(t)
	cases := []struct {
		name  string
		op    stage.Op
		files []stage.FileOpDiff
		want  string
	}{
		{"proposed", stage.Op{State: stage.StateProposed}, windowsWith(false), "●"},
		{"no windows at all", stage.Op{State: stage.StateProposed}, nil, "●"},
		{"ownerless only", stage.Op{State: stage.StateProposed},
			[]stage.FileOpDiff{{Hunks: []stage.DisplayHunk{{HunkID: ""}}}}, "●"},
		{"some hunks dropped", stage.Op{State: stage.StateProposed}, windowsWith(false, true), "◐"},
		{"all hunks dropped", stage.Op{State: stage.StateProposed}, windowsWith(true, true), "✗"},
		{"op dropped", stage.Op{State: stage.StateDropped}, windowsWith(false), "✗"},
		{"stale wins over drops", stage.Op{State: stage.StateStale}, windowsWith(true), "!"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			glyph, _ := glyphFor(theme, c.op, c.files)
			if glyph != c.want {
				t.Errorf("glyphFor = %q, want %q", glyph, c.want)
			}
		})
	}
}

func TestOpIsDropped(t *testing.T) {
	cases := []struct {
		name  string
		op    stage.Op
		files []stage.FileOpDiff
		want  bool
	}{
		{"live op with live hunks", stage.Op{}, windowsWith(false), false},
		{"live op, all hunks dropped", stage.Op{}, windowsWith(true, true), true},
		{"live op, mixed hunks", stage.Op{}, windowsWith(false, true), false},
		{"dropped op state", stage.Op{State: stage.StateDropped}, nil, true},
		{"live op, ownerless only", stage.Op{},
			[]stage.FileOpDiff{{Hunks: []stage.DisplayHunk{{HunkID: ""}}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := opIsDropped(c.op, c.files); got != c.want {
				t.Errorf("opIsDropped = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCountWindows(t *testing.T) {
	files := []stage.FileOpDiff{
		{Hunks: []stage.DisplayHunk{{HunkID: "", Dropped: false}, {HunkID: "h1", Dropped: true}}},
		{Hunks: []stage.DisplayHunk{{HunkID: "h1", Dropped: false}}},
	}
	if got := countWindows(files, false); got != 3 {
		t.Errorf("countWindows(all) = %d, want 3", got)
	}
	if got := countWindows(files, true); got != 1 {
		t.Errorf("countWindows(dropped) = %d, want 1", got)
	}
}

func TestKindLabel(t *testing.T) {
	cases := []struct {
		kind stage.OpKind
		want string
	}{
		{stage.OpIngestSource, "src"},
		{stage.OpCreatePage, "new"},
		{stage.OpPatchPage, "patch"},
		{stage.OpRenamePage, "renam"},
		{stage.OpMergePages, "merge"},
		{stage.OpSplitPage, "split"},
		{stage.OpAddLink, "add_l"},
		{stage.OpRetract, "retra"},
	}
	for _, c := range cases {
		if got := kindLabel(c.kind); got != c.want {
			t.Errorf("kindLabel(%q) = %q, want %q", c.kind, got, c.want)
		}
	}
}

// TestOpsRowColumns pins the Ops row's column layout against the frozen
// grid rows at the two widths the grids show: the full-width row with a
// directory hint (review-80x24) and the clipped rows without one
// (review-100x30).
func TestOpsRowColumns(t *testing.T) {
	m := &Model{theme: testTheme(t)}

	cases := []struct {
		name string
		op   stage.Op
		cw   int
		want string
	}{
		{"ingest with full dir", stage.Op{ID: "op1", Kind: stage.OpIngestSource, Path: "raw/articles/claude-in-a-box.md"}, 76,
			"● src   claude-in-a-box.md  raw/articles/"},
		{"create with full dir", stage.Op{ID: "op2", Kind: stage.OpCreatePage, Path: "wiki/concepts/network-namespace-egress-isolation.md"}, 76,
			"● new   network-namespace-egress-isolation.md  wiki/concepts/"},
		{"clipped basename, no dir", stage.Op{ID: "op2", Kind: stage.OpCreatePage, Path: "wiki/concepts/network-namespace-egress-isolation.md"}, 29,
			"● new   network-namespace-eg…"},
		{"last dir segment fits", stage.Op{ID: "op4", Kind: stage.OpPatchPage, Path: "wiki/entities/claude.md"}, 29,
			"● patch claude.md  entities/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := plain(m.opsRow(c.op, c.cw)); got != c.want {
				t.Errorf("opsRow(%d) = %q, want %q", c.cw, got, c.want)
			}
		})
	}
}

// TestOpsRowDroppedIsMarked checks the glyph and kind treatment of an op
// whose every window is dropped: ✗ glyph, and the same row shape as a
// live op in plain text.
func TestOpsRowDroppedIsMarked(t *testing.T) {
	m := &Model{
		theme: testTheme(t),
		opDiffs: map[string][]stage.FileOpDiff{
			"op4": windowsWith(true),
		},
	}
	op := stage.Op{ID: "op4", Kind: stage.OpPatchPage, Path: "wiki/entities/claude.md"}
	files := m.opDiffs["op4"]
	if got := plain(m.opsRow(op, 29)); got != "✗ patch claude.md  entities/" {
		t.Errorf("opsRow = %q, want %q", got, "✗ patch claude.md  entities/")
	}
	if glyph, _ := glyphFor(m.theme, op, files); glyph != "✗" {
		t.Errorf("glyph = %q, want ✗", glyph)
	}
}

// TestChangesetRows pins the Changeset panel's computed rows (MASTER §9
// C20: the mockup's numbers are the data) including the ingest intent's
// source base name.
func TestChangesetRows(t *testing.T) {
	m := &Model{
		theme: testTheme(t),
		changeset: &stage.Changeset{
			ID:     "cs-df9e11771b4c304a",
			Intent: "ingest /home/anton/Downloads/claude-vpn-namespace-tutorial (1).html",
			Checks: stage.Checks{Schema: "pass", Lint: "pass"},
		},
		ops: []stage.Op{
			{ID: "op1", State: stage.StateProposed},
			{ID: "op2", State: stage.StateProposed},
			{ID: "op3", State: stage.StateProposed},
			{ID: "op4", State: stage.StateProposed},
		},
		opDiffs: map[string][]stage.FileOpDiff{
			"op1": {{Hunks: []stage.DisplayHunk{{HunkID: ""}}}},
			"op2": {{Hunks: []stage.DisplayHunk{{HunkID: ""}, {HunkID: ""}}}},
			"op3": {{Hunks: []stage.DisplayHunk{{HunkID: "h1"}}}},
			"op4": {{Hunks: []stage.DisplayHunk{{HunkID: "h1", Dropped: true}}}},
		},
	}

	rows := m.changesetRows()
	want := []string{
		"source  claude-vpn-namespace-tutorial (1).html",
		"id      cs-df9e11771b4c304a",
		"ops     4 · 1 dropped · 0 stale",
		"hunks   4 kept · 1 dropped",
		"",
	}
	for i, w := range want {
		if got := plain(rows[i]); got != w {
			t.Errorf("changesetRows[%d] = %q, want %q", i, got, w)
		}
	}
	if got := plain(rows[5]); got != "checks  ✓ schema   ✓ lint" {
		t.Errorf("checks row = %q, want %q", got, "checks  ✓ schema   ✓ lint")
	}
	if got := plain(rows[6]); got != "        ✓ orphans  ✓ links" {
		t.Errorf("checks row 2 = %q, want %q", got, "        ✓ orphans  ✓ links")
	}
}

// TestChangesetRowsFailingChecks shows a failing check as ✗, and an
// intent that is not an ingest as the source row verbatim.
func TestChangesetRowsFailingChecks(t *testing.T) {
	m := &Model{
		theme: testTheme(t),
		changeset: &stage.Changeset{
			ID:     "cs-1",
			Intent: "rework the entities pages",
			Checks: stage.Checks{Schema: "fail", Lint: "pass", Orphans: 2, BrokenLinks: 1},
		},
		ops: []stage.Op{{ID: "op1", State: stage.StateProposed}},
		opDiffs: map[string][]stage.FileOpDiff{
			"op1": {{Hunks: []stage.DisplayHunk{{HunkID: "h1"}}}},
		},
	}
	rows := m.changesetRows()
	if got := plain(rows[0]); got != "source  rework the entities pages" {
		t.Errorf("source row = %q, want the intent verbatim", got)
	}
	if got := plain(rows[3]); got != "hunks   1 kept · 0 dropped" {
		t.Errorf("hunks row = %q", got)
	}
	if got := plain(rows[5]); got != "checks  ✗ schema   ✓ lint" {
		t.Errorf("checks row = %q, want %q", got, "checks  ✗ schema   ✓ lint")
	}
	if got := plain(rows[6]); got != "        ✗ orphans  ✗ links" {
		t.Errorf("checks row 2 = %q, want %q", got, "        ✗ orphans  ✗ links")
	}
}

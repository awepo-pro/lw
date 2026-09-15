package review

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

var errFixture = errors.New("boom")

var stageChangesetFixture = stage.Changeset{ID: "cs-fixture", Intent: "fixture intent"}

func stageOpFixture(i int) stage.Op {
	return stage.Op{
		ID:   fmt.Sprintf("op%d", i+1),
		Kind: stage.OpPatchPage,
		Path: fmt.Sprintf("wiki/concepts/page-%d.md", i+1),
	}
}

func mustRenderer(t *testing.T) *markdown.Renderer {
	t.Helper()
	return markdown.NewRenderer()
}

// TestCursorRowsMatchPanelCursorBranch is the load-bearing guarantee
// behind panelcursor.go: re-rendering a ui.Panel row as a cursor row must
// produce the same bytes ui.Panel itself produces for that row when it is
// the one CursorRow. Every colour and layout property of the frozen grids'
// multi-row cursor window rests on this.
func TestCursorRowsMatchPanelCursorBranch(t *testing.T) {
	th := testTheme(t)

	content := []string{
		"",
		"plain text row",
		th.Muted.Render("muted text row"),
		ui.Wrap("a longer row that wraps nowhere but is wide, plus … and · glyphs", 30, 0)[0],
		th.Good.Render("+ added"),
		"",
	}
	for _, focused := range []bool{true, false} {
		for _, cursor := range []int{1, 3, 5} {
			want := ui.Panel(th, ui.PanelSpec{
				Title:     "Diff",
				Note:      "p preview",
				Focused:   focused,
				Lines:     content,
				CursorRow: cursor,
			}, 40, 8)

			got := ui.Panel(th, ui.PanelSpec{
				Title:     "Diff",
				Note:      "p preview",
				Focused:   focused,
				Lines:     content,
				CursorRow: -1,
			}, 40, 8)
			got = applyCursorRows(got, th, []int{cursor})

			for i := range want {
				if want[i] != got[i] {
					t.Errorf("focused=%v cursor=%d row %d:\n want %q\n  got %q", focused, cursor, i, want[i], got[i])
				}
			}
		}
	}
}

// TestCursorRowsLeaveOtherRowsAlone shows the splice is scoped: only the
// marked rows change, and an empty mark set is the panel verbatim.
func TestCursorRowsLeaveOtherRowsAlone(t *testing.T) {
	th := testTheme(t)
	content := []string{"one", "two", "three"}

	base := ui.Panel(th, ui.PanelSpec{Title: "Ops", Lines: content, CursorRow: -1}, 30, 5)
	if got := applyCursorRows(base, th, nil); len(got) != len(base) {
		t.Fatalf("empty mark set changed the row count: %d", len(got))
	}

	marked := applyCursorRows(base, th, []int{1})
	for i := range base {
		// Marked line 1 is panel row 2 (the top border takes row 0); every
		// other panel row must be the panel verbatim.
		if i == 2 {
			continue
		}
		if marked[i] != base[i] {
			t.Errorf("row %d changed though only line 1 was marked", i+1)
		}
	}
}

// layoutModel builds a loaded-looking model from hand-set fields, enough
// for the layout composition tests (no engine).
func layoutModel(t *testing.T, ops int) *Model {
	t.Helper()
	m := &Model{
		theme:        testTheme(t),
		hasChangeset: true,
		changeset:    &stageChangesetFixture,
	}
	for i := 0; i < ops; i++ {
		m.ops = append(m.ops, stageOpFixture(i))
	}
	return m
}

// TestViewLayoutRegions pins the frozen layout's boundaries: where the
// Ops panel ends and Detail begins, and when the Changeset panel appears
// (s2-screens.md T06; mockgen.review).
func TestViewLayoutRegions(t *testing.T) {
	t.Run("stacked below 100 columns", func(t *testing.T) {
		m := layoutModel(t, 4) // ops height = min(4+2, 6) = 6
		rows := strings.Split(m.View(99, 28), "\n")
		if len(rows) != 28 {
			t.Fatalf("View(99,28) has %d rows", len(rows))
		}
		if got := plain(rows[0]); !strings.HasPrefix(got, "╭ Ops ") {
			t.Errorf("row 1 = %q, want the Ops panel's top border", got)
		}
		if got := plain(rows[5]); !strings.HasPrefix(got, "╰") {
			t.Errorf("row 6 = %q, want the Ops panel's bottom border", got)
		}
		if got := plain(rows[6]); !strings.HasPrefix(got, "╭ Diff ") {
			t.Errorf("row 7 = %q, want the Detail panel's top border", got)
		}
	})

	t.Run("side by side without changeset below 30 rows", func(t *testing.T) {
		m := layoutModel(t, 4)
		rows := strings.Split(m.View(100, 28), "\n")
		opsW := (33*100 + 50) / 100 // 33
		if got := plain(rows[0]); !strings.HasPrefix(got, "╭ Ops ") || !strings.Contains(got, "╭ Diff ") {
			t.Errorf("row 1 = %q, want Ops and Diff side by side", got)
		}
		for i, row := range rows {
			if strings.Contains(plain(row), "Changeset") {
				t.Errorf("row %d shows a Changeset panel at 28 rows", i+1)
			}
		}
		last := []rune(plain(rows[27]))
		if last[opsW-1] != '╯' || last[opsW] != '╰' {
			t.Errorf("bottom row does not close Ops at column %d: %q", opsW, string(last[:opsW+2]))
		}
	})

	t.Run("changeset panel from 30 rows", func(t *testing.T) {
		m := layoutModel(t, 4)
		rows := strings.Split(m.View(120, 38), "\n")
		// opsH = 38 - 9 = 29: Ops occupies rows 1..30 (1-based), Changeset
		// rows 31..39, and the ops panel closes exactly at row 30.
		if got := plain(rows[28]); !strings.HasPrefix(got, "╰") {
			t.Errorf("row 29 = %q, want the Ops panel's bottom border", got)
		}
		if got := plain(rows[29]); !strings.HasPrefix(got, "╭ Changeset ") {
			t.Errorf("row 30 = %q, want the Changeset panel's top border", got)
		}
		if got := plain(rows[37]); !strings.HasPrefix(got, "╰") {
			t.Errorf("row 38 = %q, want the Changeset panel's bottom border", got)
		}
	})
}

// TestViewEmptyStates pins the pane's no-changeset face: the same frame,
// with the reason on the Detail panel's first content row.
func TestViewEmptyStates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}

	t.Run("nothing staged", func(t *testing.T) {
		m := &Model{theme: theme, md: mustRenderer(t)}
		rows := strings.Split(m.View(80, 22), "\n")
		found := false
		for _, r := range rows {
			if strings.Contains(plain(r), "(nothing staged)") {
				found = true
			}
		}
		if !found {
			t.Errorf("View shows no (nothing staged) line:\n%s", plain(strings.Join(rows, "\n")))
		}
	})

	t.Run("load error", func(t *testing.T) {
		m := &Model{theme: theme, md: mustRenderer(t), loadErr: errFixture}
		rows := strings.Split(m.View(80, 22), "\n")
		found := false
		for _, r := range rows {
			if strings.Contains(plain(r), "review: boom") {
				found = true
			}
		}
		if !found {
			t.Errorf("View shows no load error line")
		}
	})
}

// TestViewTinySizes keeps the exactly-w-by-h invariant where no panel can
// be drawn at all.
func TestViewTinySizes(t *testing.T) {
	m := &Model{theme: testTheme(t), md: mustRenderer(t)}
	for _, sz := range [][2]int{{1, 1}, {3, 1}, {5, 2}, {2, 5}} {
		out := m.View(sz[0], sz[1])
		lines := strings.Split(out, "\n")
		if len(lines) != sz[1] {
			t.Errorf("View(%d,%d) has %d lines", sz[0], sz[1], len(lines))
			continue
		}
		for i, l := range lines {
			if len(l) != sz[0] {
				t.Errorf("View(%d,%d) line %d is %d bytes, want %d", sz[0], sz[1], i+1, len(l), sz[0])
			}
		}
	}
}

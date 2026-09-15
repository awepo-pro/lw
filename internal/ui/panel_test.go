package ui

import "testing"

// TestPanelGolden renders Panel for the two frozen panel cases (MASTER §5
// T03) and compares plain output against testdata/frozen.
func TestPanelGolden(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	t.Run("panel-ops-40x7", func(t *testing.T) {
		spec := PanelSpec{
			Title:    "Ops",
			FootNote: "3 of 4",
			Lines: []string{
				"● src   a.md",
				"● new   b.md",
				"● patch c.md",
				"✗ patch d.md",
			},
			CursorRow: 2,
		}
		got := joinLines(Panel(th, spec, 40, 7))
		assertPlainMatchesFrozen(t, got, "panel-ops-40x7.txt")
	})

	t.Run("panel-diff-overflow-60x5", func(t *testing.T) {
		spec := PanelSpec{
			Title:   "Diff",
			Note:    "p preview",
			Focused: true,
			Lines: []string{
				"line 1", "line 2", "line 3", "line 4", "line 5",
			},
			CursorRow: -1,
			Overflow:  true,
		}
		got := joinLines(Panel(th, spec, 60, 5))
		assertPlainMatchesFrozen(t, got, "panel-diff-overflow-60x5.txt")
	})
}

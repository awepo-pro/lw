// profile_test.go pins the pane's half of W5 F1 (contract §5 frame note
// 8): on tea.ColorProfileMsg this pane rebuilds its theme copy exactly the
// way it does for tea.BackgroundColorMsg (C-81), so its cursor row's tint
// follows the terminal's colour profile — xterm grey 236 at ANSI256, never
// the TrueColor hex that rounds to navy there (runs/G4-user-findings.md F1).
package lintview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// TestLintProfile asserts the pane re-resolves its cursor tint when the
// shell reports the terminal's colour profile.
func TestLintProfile(t *testing.T) {
	t.Run("ansi256_cursor_row_uses_grey", func(t *testing.T) {
		v := findingsVault(t)
		p := initPane(t, uitest.Deps(v, true, nil))
		p, _ = p.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI256})

		styled, _ := uitest.PaneScreen(p, 120, 38)
		assertCursorRow256(t, styled)
	})
}

// assertCursorRow256 finds the styled cursor row — the one carrying the
// accent `▌` gutter — and asserts it tints with the fixed ANSI256 grey
// (48;5;236) and carries no TrueColor background at all.
func assertCursorRow256(t *testing.T, styled string) {
	t.Helper()
	rows := strings.Split(styled, "\n")
	found := -1
	for i, row := range rows {
		if strings.Contains(row, "▌") {
			if found >= 0 {
				t.Fatalf("more than one cursor row: lines %d and %d", found+1, i+1)
			}
			found = i
		}
	}
	if found < 0 {
		t.Fatal("no cursor row (no ▌ gutter) in the render")
	}
	row := rows[found]
	if !strings.Contains(row, "48;5;236") {
		t.Fatalf("cursor row does not carry the ANSI256 grey tint 48;5;236:\n%s", row)
	}
	if strings.Contains(row, "48;2;") {
		t.Fatalf("cursor row still carries a TrueColor background (48;2;):\n%s", row)
	}
}

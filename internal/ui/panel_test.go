package ui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

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

// TestPanelNoteLevel pins PanelSpec.NoteLevel (ORCH-12/D-3T): the zero
// value StatusInfo renders the Note Muted — every pre-existing panel's
// rendering, unchanged — and StatusGood renders it in Good. The row stays
// exactly w cells either way.
func TestPanelNoteLevel(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	const w = 40
	top := func(spec PanelSpec) string {
		t.Helper()
		spec.Title, spec.Note, spec.Focused = "Findings", "clean", true
		rows := Panel(th, spec, w, 3)
		if got := lipgloss.Width(rows[0]); got != w {
			t.Fatalf("top border is %d cells wide, want %d", got, w)
		}
		return rows[0]
	}

	// Zero value — NoteLevel unset: Muted, exactly as every panel before the
	// field rendered.
	zero := top(PanelSpec{})
	if !strings.Contains(zero, th.Muted.Render(" clean ")) {
		t.Fatalf("zero NoteLevel top border = %q, want the note in Muted", zero)
	}
	if strings.Contains(zero, th.Good.Render(" clean ")) {
		t.Fatalf("zero NoteLevel top border = %q, want no Good note", zero)
	}

	good := top(PanelSpec{NoteLevel: StatusGood})
	if !strings.Contains(good, th.Good.Render(" clean ")) {
		t.Fatalf("StatusGood top border = %q, want the note in Good", good)
	}
	if strings.Contains(good, th.Muted.Render(" clean ")) {
		t.Fatalf("StatusGood top border = %q, want no Muted note", good)
	}
}

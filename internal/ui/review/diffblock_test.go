package review

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/stage"
)

// TestOpHeadPadRule pins mockgen.op_head's note rule: the note is
// right-aligned to cw when at least two cells of padding remain, and
// omitted otherwise — the reason review-preview-100x30's head has no
// `op3 · staged page` while the 80- and 120-column grids do.
func TestOpHeadPadRule(t *testing.T) {
	th := testTheme(t)
	op := stage.Op{ID: "op3", Kind: stage.OpPatchPage, Path: "wiki/concepts/private-network-access.md"}

	cases := []struct {
		cw       int
		note     string
		wantNote bool
	}{
		{cw: 76, note: "op3 · 1 hunk", wantNote: true},
		{cw: 63, note: "op3 · 1 hunk", wantNote: true},
		{cw: 58, note: "op3 · 1 hunk", wantNote: false}, // pad would be 1
		{cw: 140, note: "op3 · staged page", wantNote: true},
		{cw: 63, note: "op3 · staged page", wantNote: false}, // pad would be 0
	}
	for _, c := range cases {
		head := opHead(th, op, c.cw, c.note, false)
		if got := lipgloss.Width(head); got > c.cw {
			t.Errorf("opHead(cw=%d) is %d cells wide", c.cw, got)
		}
		hasNote := strings.HasSuffix(plain(head), c.note)
		if hasNote != c.wantNote {
			t.Errorf("opHead(cw=%d, note=%q): note present = %v, want %v", c.cw, c.note, hasNote, c.wantNote)
		}
		if hasNote && lipgloss.Width(head) != c.cw {
			// Right-aligned: head + padding + note lands exactly at cw.
			t.Errorf("opHead(cw=%d) is %d cells wide with the note, want %d", c.cw, lipgloss.Width(head), c.cw)
		}
	}

	// A dropped op's note rides in bad colour, plain text unchanged.
	dropped := opHead(th, stage.Op{ID: "op4", Kind: stage.OpPatchPage, Path: "wiki/entities/claude.md"}, 76, "op4 · dropped", true)
	if !strings.HasSuffix(plain(dropped), "op4 · dropped") {
		t.Errorf("dropped head lacks its note: %q", dropped)
	}
}

// TestWrapCut pins the rationale wrap: at most four lines, the last
// ending with "…" when cut (mockgen.diff_block).
func TestWrapCut(t *testing.T) {
	short := "one two three"
	if got := wrapCut(short, 40, 4); len(got) != 1 || got[0] != short {
		t.Errorf("wrapCut(short) = %q, want the line untouched", got)
	}

	long := strings.Repeat("word ", 30)
	got := wrapCut(long, 20, 4)
	if len(got) != 4 {
		t.Fatalf("wrapCut(long) = %d lines, want 4", len(got))
	}
	if !strings.HasSuffix(got[3], "…") {
		t.Errorf("fourth line %q does not end with the cut mark", got[3])
	}
	for i, l := range got {
		if len(l) > 23 { // 20 ASCII cells plus the 3-byte cut mark
			t.Errorf("line %d is %d bytes wide", i+1, len(l))
		}
	}
}

// TestWindowLines pins the per-window lines: blank line, header with the
// muted hunk id and the bad "dropped" marker, then each diff line prefixed
// with its kind — ownerless windows carrying no id at all.
func TestWindowLines(t *testing.T) {
	th := testTheme(t)
	m := &Model{theme: th}

	t.Run("attributed window", func(t *testing.T) {
		w := stage.DisplayHunk{
			Header: "@@ -34,6 +34,10 @@",
			HunkID: "h1",
			Lines: []stage.DisplayLine{
				{Kind: ' ', Text: "context"},
				{Kind: '+', Text: "added line"},
				{Kind: '-', Text: "removed line"},
				{Kind: '+', Text: ""},
			},
		}
		lines := m.windowLines(w, 40, false)
		if got := plain(lines[0].text); got != "" {
			t.Errorf("first line = %q, want blank", got)
		}
		if got := plain(lines[1].text); got != "@@ -34,6 +34,10 @@  h1" {
			t.Errorf("header = %q", got)
		}
		want := []string{"  context", "+ added line", "- removed line", "+ "}
		for i, w2 := range want {
			if got := plain(lines[2+i].text); got != w2 {
				t.Errorf("line %d = %q, want %q", i+3, got, w2)
			}
		}
	})

	t.Run("dropped window", func(t *testing.T) {
		w := stage.DisplayHunk{
			Header:  "@@ -21,6 +21,10 @@",
			HunkID:  "h1",
			Dropped: true,
			Lines:   []stage.DisplayLine{{Kind: '+', Text: "## Local egress confinement"}},
		}
		lines := m.windowLines(w, 40, false)
		if got := plain(lines[1].text); got != "@@ -21,6 +21,10 @@  h1  dropped" {
			t.Errorf("header = %q", got)
		}
		if got := plain(lines[2].text); got != "+ ## Local egress confinement" {
			t.Errorf("line = %q", got)
		}
		if !strings.Contains(lines[2].text, "\x1b[") {
			t.Errorf("dropped window line carries no styling: %q", lines[2].text)
		}
	})

	t.Run("ownerless window has no id", func(t *testing.T) {
		w := stage.DisplayHunk{Header: "@@ -1 +1 @@", Lines: []stage.DisplayLine{{Kind: '+', Text: "x"}}}
		lines := m.windowLines(w, 40, false)
		if got := plain(lines[1].text); got != "@@ -1 +1 @@" {
			t.Errorf("header = %q, want no hunk id", got)
		}
	})
}

// TestAddedTexts collects non-blank '+' texts of non-dropped windows only
// (mockgen.preview_block's `p == '+' and t.strip()`).
func TestAddedTexts(t *testing.T) {
	files := []stage.FileOpDiff{
		{Hunks: []stage.DisplayHunk{
			{HunkID: "h1", Lines: []stage.DisplayLine{
				{Kind: '+', Text: "kept one"},
				{Kind: '+', Text: "   "},
				{Kind: '-', Text: "removed"},
			}},
			{HunkID: "h2", Dropped: true, Lines: []stage.DisplayLine{
				{Kind: '+', Text: "dropped addition"},
			}},
		}},
	}
	got := addedTexts(files)
	if len(got) != 1 || got[0] != "kept one" {
		t.Errorf("addedTexts = %q, want [kept one]", got)
	}
}

// TestPreviewLinesBranches pins the Preview panel's two degenerate
// branches: a missing staged file shows the deleted line (s2-screens.md
// T06), and no current op shows the nothing-staged line.
func TestPreviewLinesBranches(t *testing.T) {
	th := testTheme(t)

	t.Run("staged file missing", func(t *testing.T) {
		m := &Model{
			theme:        th,
			md:           mustRenderer(t),
			hasChangeset: true,
			ops:          []stage.Op{{ID: "op9", Kind: stage.OpPatchPage, Path: "wiki/nowhere.md"}},
			stops:        []cursorStop{{fileIdx: 0, hunkIdx: 0}},
			diff: stage.Diff{Files: []stage.FileDiff{
				{OpID: "op9", Hunks: []stage.Hunk{{ID: "h1"}}},
			}},
			opDiffs: map[string][]stage.FileOpDiff{
				"op9": {{Path: "wiki/nowhere.md", Hunks: []stage.DisplayHunk{{HunkID: "h1"}}}},
			},
			// deps.Engine nil: nothing is staged anywhere.
		}
		lines := m.previewLines(40)
		if got := plain(lines[2].text); got != "(deleted in this changeset)" {
			t.Errorf("preview body = %q, want the deleted line", got)
		}
	})

	t.Run("nothing staged", func(t *testing.T) {
		m := &Model{theme: th, md: mustRenderer(t)}
		lines := m.previewLines(40)
		if got := plain(lines[0].text); got != "(nothing staged)" {
			t.Errorf("preview body = %q, want the nothing-staged line", got)
		}
	})
}

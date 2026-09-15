package review

import (
	"testing"

	"github.com/awepo-pro/lw/internal/ui"
)

// footerTestModel builds a Model with default keys and theme and no
// engine: the footer and overlay never touch the engine, so the pane
// under test needs none.
func footerTestModel(t *testing.T) *Model {
	t.Helper()
	m := New(ui.Deps{Theme: testTheme(t), Keys: defaultTestKeys(t)})
	return m.(*Model)
}

// defaultTestKeys loads the compiled-in keymap hermetically.
func defaultTestKeys(t *testing.T) ui.KeyMap {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	return keys
}

// TestFooterHelpOrder pins the frozen footer list (s2-screens.md T06):
// ten bindings in order, the merged movement labels included and the
// per-key ones (MoveUp, Bottom) omitted.
func TestFooterHelpOrder(t *testing.T) {
	m := footerTestModel(t)

	want := [][2]string{
		{"y", "accept hunk"},
		{"n", "drop hunk"},
		{"j/k", "move"},
		{"p", "preview"},
		{"A", "accept all"},
		{"C", "commit"},
		{"X", "reject changeset"},
		{"g/G", "top/bottom"},
		{"tab", "screen"},
		{"q", "quit"},
	}
	bindings := m.FooterHelp()
	if len(bindings) != len(want) {
		t.Fatalf("FooterHelp has %d bindings, want %d", len(bindings), len(want))
	}
	for i, w := range want {
		h := bindings[i].Help()
		if h.Key != w[0] || h.Desc != w[1] {
			t.Errorf("FooterHelp[%d] = (%q, %q), want (%q, %q)", i, h.Key, h.Desc, w[0], w[1])
		}
	}
}

// TestFooterHelpPreviewSwap shows the frozen Preview-mode label: `p diff`
// in place of `p preview`, everything else unchanged.
func TestFooterHelpPreviewSwap(t *testing.T) {
	m := footerTestModel(t)
	m.preview = true

	bindings := m.FooterHelp()
	if len(bindings) != 10 {
		t.Fatalf("FooterHelp has %d bindings, want 10", len(bindings))
	}
	h := bindings[3].Help()
	if h.Key != "p" || h.Desc != "diff" {
		t.Errorf("FooterHelp[3] in Preview = (%q, %q), want (p, diff)", h.Key, h.Desc)
	}
	if other := bindings[0].Help(); other.Key != "y" {
		t.Errorf("FooterHelp[0] moved: (%q, %q)", other.Key, other.Desc)
	}
}

// TestOverlayHelp pins the ? overlay's Review section against the keys-*
// grids' left column, including the disabled split-hunk entry (D9).
func TestOverlayHelp(t *testing.T) {
	m := footerTestModel(t)

	title, entries := m.OverlayHelp()
	if title != "Review" {
		t.Errorf("OverlayHelp title = %q, want Review", title)
	}
	want := []ui.HelpEntry{
		{Key: "y", Desc: "accept hunk"},
		{Key: "n", Desc: "drop hunk"},
		{Key: "A", Desc: "accept all"},
		{Key: "X", Desc: "reject changeset"},
		{Key: "C", Desc: "commit"},
		{Key: "p", Desc: "preview / diff"},
		{Key: "j/k", Desc: "down / up"},
		{Key: "g/G", Desc: "top / bottom"},
		{Key: "s", Desc: "split hunk · not built", Disabled: true},
	}
	if len(entries) != len(want) {
		t.Fatalf("OverlayHelp has %d entries, want %d", len(entries), len(want))
	}
	for i, w := range want {
		if entries[i] != w {
			t.Errorf("OverlayHelp[%d] = %+v, want %+v", i, entries[i], w)
		}
	}
}

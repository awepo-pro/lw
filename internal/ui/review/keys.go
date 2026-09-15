// keys.go is the review screen's face in the shell's chrome: the footer
// binding list (contract §5 FooterHelper) and the ? overlay's Review
// section (contract §5 OverlayHelper), both in the frozen order of
// s2-screens.md T06 and the keys-* grids.
package review

import (
	"charm.land/bubbles/v2/key"

	"github.com/awepo-pro/lw/internal/ui"
)

// FooterHelp returns the review footer's bindings in display order; the
// shell appends "? help" and drops whole bindings from the end until the
// row fits (contract §5 frame note 2). The merged movement labels live on
// the KeyMap (MoveDown is "j/k move", Top is "g/G top/bottom"), so
// MoveUp and Bottom are omitted here.
func (m *Model) FooterHelp() []key.Binding {
	k := m.deps.Keys
	preview := k.Preview
	if m.preview {
		// In Preview mode the frozen footer shows "p diff" — what the key
		// does now — not the binding's static "preview" help. A fresh
		// binding carries the user's own keys and the swapped label.
		h := k.Preview.Help()
		preview = key.NewBinding(key.WithKeys(k.Preview.Keys()...), key.WithHelp(h.Key, "diff"))
	}
	return []key.Binding{
		k.AcceptHunk, // y accept hunk
		k.DropHunk,   // n drop hunk
		k.MoveDown,   // j/k move
		preview,      // p preview (p diff in Preview)
		k.AcceptAll,  // A accept all
		k.Commit,     // C commit
		k.Reject,     // X reject changeset
		k.Top,        // g/G top/bottom
		k.NextPane,   // tab screen
		k.Quit,       // q quit
	}
}

// OverlayHelp returns the Review section of the ? overlay (contract §5
// OverlayHelper; the keys-* grids' left column). The disabled split-hunk
// entry is D9's "not built" marker (C-90/TD-2).
func (m *Model) OverlayHelp() (string, []ui.HelpEntry) {
	return "Review", []ui.HelpEntry{
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
}

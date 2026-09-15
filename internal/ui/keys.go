// keys.go implements backbone §12's KeyMap and LoadKeys.
//
// hotkeys.toml follows yorukot/superfile's pattern: a flat TOML document
// mapping one key name to a list of keystrokes, e.g. `quit = ["q", "esc"]`.
// See NOTICE for the full attribution.
package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"charm.land/bubbles/v2/key"
	"github.com/BurntSushi/toml"

	"github.com/awepo-pro/lw/internal/config"
)

// KeyMap is lw's resolved key bindings (backbone §12), loaded from
// hotkeys.toml. Every field is a charm.land/bubbles/v2/key.Binding; a screen
// matches it against a tea.KeyPressMsg with key.Matches — never against
// tea.KeyMsg, which double-fires once keyboard enhancements are negotiated
// (C-80).
type KeyMap struct {
	// Warnings collects non-fatal problems found while loading hotkeys.toml
	// — unknown keys — so a caller can surface them without LoadKeys failing.
	Warnings []string

	// Review screen keys, fixed by /docs/design.md §9.
	AcceptHunk key.Binding // y — undrop-and-advance (Engine.UndropHunk, D-CL)
	DropHunk   key.Binding // n — drop-and-advance (Engine.DropHunk)
	SplitHunk  key.Binding // s — not built in v0.1 (C-90/TD-2)
	AcceptAll  key.Binding // A — refused unless lint is clean
	Reject     key.Binding // X — reject the changeset
	Commit     key.Binding // C — commit
	MoveDown   key.Binding // j, down
	MoveUp     key.Binding // k, up
	Top        key.Binding // g
	Bottom     key.Binding // G
	// Preview toggles Review's detail panel between Diff and Preview
	// (contract §4).
	Preview key.Binding // p

	// Shell-wide keys.
	NextPane key.Binding // tab
	Quit     key.Binding // q, ctrl+c
	Help     key.Binding // ?
}

// defaultKeyMap is lw's compiled-in bindings, so a fresh install works with
// no hotkeys.toml at all.
func defaultKeyMap() KeyMap {
	return KeyMap{
		AcceptHunk: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "accept hunk")),
		DropHunk:   key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "drop hunk")),
		SplitHunk:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "split hunk")),
		AcceptAll:  key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "accept all")),
		Reject:     key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "reject changeset")),
		Commit:     key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "commit")),
		// MoveDown's help is the merged "j/k" / "move" footer label (contract
		// §4): the footer shows one entry for both movement keys, and a pane
		// omits MoveUp from its own footer list.
		MoveDown: key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/k", "move")),
		MoveUp:   key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k", "up")),
		// Top's help is likewise the merged "g/G" / "top/bottom" label; a
		// pane omits Bottom from its own footer list.
		Top:      key.NewBinding(key.WithKeys("g"), key.WithHelp("g/G", "top/bottom")),
		Bottom:   key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "bottom")),
		Preview:  key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "preview")),
		NextPane: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "screen")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	}
}

// keysFile is the on-disk shape of <config.ConfigDir()>/hotkeys.toml. A nil
// slice means "not present in the file, keep the default"; an explicit
// empty list (`quit = []`) unbinds the key.
type keysFile struct {
	AcceptHunk []string `toml:"accept_hunk"`
	DropHunk   []string `toml:"drop_hunk"`
	SplitHunk  []string `toml:"split_hunk"`
	AcceptAll  []string `toml:"accept_all"`
	Reject     []string `toml:"reject_changeset"`
	Commit     []string `toml:"commit"`
	MoveDown   []string `toml:"move_down"`
	MoveUp     []string `toml:"move_up"`
	Top        []string `toml:"top"`
	Bottom     []string `toml:"bottom"`
	Preview    []string `toml:"preview"`
	NextPane   []string `toml:"next_pane"`
	Quit       []string `toml:"quit"`
	Help       []string `toml:"help"`
}

// applyOverrides replaces the keystrokes of every binding f names, leaving
// every binding it does not name — and their help text — untouched.
func (f keysFile) applyOverrides(km *KeyMap) {
	set := func(b *key.Binding, keys []string) {
		if keys != nil {
			b.SetKeys(keys...)
		}
	}
	set(&km.AcceptHunk, f.AcceptHunk)
	set(&km.DropHunk, f.DropHunk)
	set(&km.SplitHunk, f.SplitHunk)
	set(&km.AcceptAll, f.AcceptAll)
	set(&km.Reject, f.Reject)
	set(&km.Commit, f.Commit)
	set(&km.MoveDown, f.MoveDown)
	set(&km.MoveUp, f.MoveUp)
	set(&km.Top, f.Top)
	set(&km.Bottom, f.Bottom)
	set(&km.Preview, f.Preview)
	set(&km.NextPane, f.NextPane)
	set(&km.Quit, f.Quit)
	set(&km.Help, f.Help)
}

// LoadKeys returns lw's key bindings, with the compiled-in defaults
// overlaid by <config.ConfigDir()>/hotkeys.toml when that file is present
// (backbone §12). A missing file is not an error. A partial file overrides
// only the bindings it names; an unknown key is recorded on the returned
// KeyMap's Warnings field, never fatal and never printed here.
func LoadKeys() (KeyMap, error) {
	km := defaultKeyMap()
	var warnings []string

	path := filepath.Join(config.ConfigDir(), "hotkeys.toml")
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// No overlay file: compiled-in defaults only.
	case err != nil:
		return KeyMap{}, fmt.Errorf("ui: read %s: %w", path, err)
	default:
		var f keysFile
		meta, decErr := toml.Decode(string(b), &f)
		if decErr != nil {
			return KeyMap{}, fmt.Errorf("ui: parse %s: %w", path, decErr)
		}
		f.applyOverrides(&km)
		for _, k := range meta.Undecoded() {
			warnings = append(warnings, fmt.Sprintf("hotkeys.toml: unknown key %q ignored", k.String()))
		}
	}

	km.Warnings = warnings
	return km, nil
}

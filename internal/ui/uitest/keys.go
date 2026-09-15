// keys.go implements contract §6's Key: a tea.KeyPressMsg built from the
// key name string bubbles/key matches against ("j", "tab", "ctrl+r", "?",
// "esc", "enter").
//
// The parse mirrors how the runtime names a key (ultraviolet's
// Key.Keystroke and its match table): modifiers joined by "+", then a known
// key name or a single rune. Building the message the same way the namer
// works is what makes Key(s).String() round-trip for every name the
// runtime itself can produce, so key.Matches behaves in a headless test
// exactly as it does under a real terminal.
package uitest

import (
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// keyNames maps a key name to the code the runtime uses for it. Only
// non-printable keys need to be here: a printable rune is its own code.
var keyNames = map[string]rune{
	"backspace": tea.KeyBackspace,
	"tab":       tea.KeyTab,
	"enter":     tea.KeyEnter,
	"return":    tea.KeyReturn,
	"esc":       tea.KeyEscape,
	"escape":    tea.KeyEscape,
	"space":     tea.KeySpace,
	"up":        tea.KeyUp,
	"down":      tea.KeyDown,
	"left":      tea.KeyLeft,
	"right":     tea.KeyRight,
	"insert":    tea.KeyInsert,
	"delete":    tea.KeyDelete,
	"pgup":      tea.KeyPgUp,
	"pgdown":    tea.KeyPgDown,
	"home":      tea.KeyHome,
	"end":       tea.KeyEnd,
	"f1":        tea.KeyF1,
	"f2":        tea.KeyF2,
	"f3":        tea.KeyF3,
	"f4":        tea.KeyF4,
	"f5":        tea.KeyF5,
	"f6":        tea.KeyF6,
	"f7":        tea.KeyF7,
	"f8":        tea.KeyF8,
	"f9":        tea.KeyF9,
	"f10":       tea.KeyF10,
	"f11":       tea.KeyF11,
	"f12":       tea.KeyF12,
}

// Key builds a tea.KeyPressMsg for a key string as bubbles/key names it
// ("j", "tab", "ctrl+r", "?", "esc"). The result's String() is s for every
// name the runtime itself produces.
func Key(s string) tea.KeyPressMsg {
	var (
		mod  tea.KeyMod
		code rune
		text string
	)
	for _, part := range strings.Split(s, "+") {
		switch part {
		case "ctrl":
			mod |= tea.ModCtrl
		case "alt":
			mod |= tea.ModAlt
		case "shift":
			mod |= tea.ModShift
		case "meta":
			mod |= tea.ModMeta
		case "hyper":
			mod |= tea.ModHyper
		case "super":
			mod |= tea.ModSuper
		default:
			if c, ok := keyNames[part]; ok {
				code = c
			} else if utf8.RuneCountInString(part) == 1 {
				code, _ = utf8.DecodeRuneInString(part)
			} else {
				// A multi-rune name is not a key the table knows; carry it
				// as the runtime carries extended keys, by its text.
				text = part
			}
		}
	}

	// A printable key carries its text, and Text is what String() returns;
	// modified and non-printable keys carry no text, so String() falls
	// through to the keystroke name.
	if mod&^(tea.ModShift|tea.ModCapsLock) == 0 && text == "" && unicode.IsPrint(code) {
		if mod&(tea.ModShift|tea.ModCapsLock) != 0 {
			text = string(unicode.ToUpper(code))
		} else {
			text = string(code)
		}
	}

	return tea.KeyPressMsg{Text: text, Mod: mod, Code: code}
}

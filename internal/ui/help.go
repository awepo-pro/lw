// help.go declares contract §5's optional shell interfaces — the vocabulary
// a screen package uses to shape its own footer and its own section of the
// `?` overlay, and to report a transient status message — plus the two
// constants that gate the whole frame on a too-small terminal (D11).
package ui

import "charm.land/bubbles/v2/key"

// HelpEntry is one row of the ? overlay.
type HelpEntry struct {
	Key, Desc string
	Disabled  bool // drawn faint, e.g. "s  split hunk · not built" (D9)
}

// FooterHelper is implemented by a pane that wants the footer to show a
// list other than Help()'s.
type FooterHelper interface {
	FooterHelp() []key.Binding // in display order; the shell appends "? help"
}

// OverlayHelper is implemented by a pane that wants its own section in the
// ? overlay.
type OverlayHelper interface {
	OverlayHelp() (title string, entries []HelpEntry)
}

// StatusLevel colours a pane's transient status message.
type StatusLevel int

const (
	StatusInfo StatusLevel = iota // Muted
	StatusGood                    // Good
	StatusWarn                    // Warn
	StatusBad                     // Bad
)

// StatusReporter is implemented by a pane with a transient message
// ("commit refused: lint has 2 errors"). While Status returns a non-empty
// msg, the footer shows it (styled by level, clipped to w-10) followed by
// "? help", instead of the bindings. Panes no longer draw a status line
// inside their own View.
type StatusReporter interface {
	Status() (msg string, level StatusLevel)
}

// MinWidth and MinHeight are D11's minimum terminal size.
const MinWidth, MinHeight = 80, 24

// help.go declares contract §5's optional shell interfaces — the vocabulary
// a screen package uses to shape its own footer and its own section of the
// `?` overlay, to report a transient status message, and to take text
// input — plus the two constants that gate the whole frame on a too-small
// terminal (D11).
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

// TextCapturer is implemented by a pane that is taking text input. While the ACTIVE pane's CapturesText() is true,
// the shell delivers every printable key (non-empty Text, no modifier other than shift) straight to that pane
// instead of matching it against the global bindings, so `q` and `?` type. Non-printable global bindings still
// apply: ctrl+c quits, tab switches screen. An open overlay and the "Terminal too small" notice keep their current
// key handling.
type TextCapturer interface {
	CapturesText() bool
}

// MinWidth and MinHeight are D11's minimum terminal size.
const MinWidth, MinHeight = 80, 24

// pane.go implements backbone §12's shared shell vocabulary: the Screen
// enum, the Pane interface every screen package implements, the Deps and
// Options a screen (and the shell itself) are constructed with, and the
// three messages the shell broadcasts to panes.
//
// Nothing here imports a screen package. Options.Panes is injected by
// cmd/lw once a screen exists (s4-tui.md S4-T2 item 1) — that is what lets
// review, browse, ask, lintview and logview build as five independent
// packages in the same wave (00-conventions.md §1 rule 5a).
package ui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
)

// Screen identifies one of the app's top-level views (backbone §12,
// /PLAN.md §9). tab cycles through all five regardless of whether
// Options.Panes carries an entry for one yet.
type Screen int

// The five screens /PLAN.md §9 defines, in tab order.
const (
	ScreenBrowse Screen = iota
	ScreenReview
	ScreenAsk
	ScreenLint
	ScreenLog
)

// Pane is what every screen package implements: a tea.Model with identity,
// except View renders a plain string at an explicit size instead of
// tea.Model's own View() tea.View. The shell composes each screen's string
// into the one tea.View it returns (backbone §12, C-79) — screens never
// build a tea.View themselves.
type Pane interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Pane, tea.Cmd)
	View(w, h int) string // OURS: the shell composes pane strings into tea.View.Content
	Title() string
	Help() []key.Binding
}

// Deps is what a screen (and the shell itself) is handed at construction.
type Deps struct {
	Engine *stage.Engine
	Agent  agent.Agent // nil until S5 wires a real agent.Agent (backbone §12)
	Theme  Theme
	Keys   KeyMap
}

// Options configures NewApp. Panes is injected by cmd/lw; the shell never
// constructs a screen and never imports one (backbone §12).
type Options struct {
	Deps
	Panes map[Screen]Pane // injected by cmd/lw
	Start Screen
}

// StageChangedMsg is broadcast whenever a stage.Engine mutation changes the
// open changeset, so every pane — and the shell's own STAGE panel — can
// re-render with the new id and op count live.
type StageChangedMsg struct {
	ChangesetID string
	Ops         int
}

// VaultReloadedMsg is broadcast after the vault is re-read from disk (for
// example, right after a commit), so a pane holding cached vault state
// knows to refresh it.
type VaultReloadedMsg struct{}

// SwitchScreenMsg asks the shell to change the active screen — for example
// Ask's Ctrl-R jumping to Review, or Log's r reverting a commit into a new
// changeset and switching to Review to show it.
type SwitchScreenMsg struct {
	To Screen
}

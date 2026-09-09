// pane.go implements backbone §12's shared shell vocabulary: the Screen
// enum, the Pane interface every screen package implements, the Deps and
// Options a screen (and the shell itself) are constructed with, and the
// messages the shell broadcasts to panes.
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
	// Agent is the agent.Agent the Ask pane drives. nil is a supported
	// state, not a stub: cmd/lw builds it only when the provider config
	// resolves, and a nil here means Ask reports why it cannot answer while
	// every other screen works untouched (backbone §12; S5-T5).
	Agent agent.Agent
	Theme Theme
	Keys  KeyMap
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

// StreamMsg hands the ask pane the agent.Event channel to consume for one
// turn, and arms its pump (internal/ui/ask's Listen, which re-arms itself
// after every event through this same channel). Declared here rather than
// in internal/ui/ask because the shell has to route it — an ask pane that
// is off screen must keep draining its stream (C-117/D-DA) — and the shell
// never imports a screen package (backbone §12).
type StreamMsg struct{ Ch <-chan agent.Event }

// EventMsg carries one agent.Event the ask pane's pump has read. Routed
// through the shell's fan-out like StreamMsg: the pane consuming it may be
// off screen for a whole turn (C-117/D-DA).
type EventMsg struct{ Ev agent.Event }

// StreamClosedMsg reports that the channel the ask pane's pump was reading
// has closed — the turn's event source is gone, so the pane stops
// re-arming. Routed like the other two, so a turn that outlives its screen
// still ends cleanly in the pane that owns it.
type StreamClosedMsg struct{}

// SwitchScreenMsg asks the shell to change the active screen — for example
// Ask's Ctrl-R jumping to Review, or Log's r reverting a commit into a new
// changeset and switching to Review to show it.
type SwitchScreenMsg struct {
	To Screen
}

// OpenPathMsg asks the shell to switch to Browse and select a vault-relative
// path — Lint's `enter` on a finding ("jump to the page in Browse",
// /PLAN.md §9) is the case it exists for.
//
// It is separate from SwitchScreenMsg because that message carries only a
// Screen, and a screen switch that cannot say *where* to land makes the jump
// a no-op. Declared by the orchestrator before wave 3's batch B, since the
// shell's message vocabulary is not a screen's to invent (MASTER §8 rule 3,
// C-108/D-CU). The shell's handling — switch, then deliver this to the Browse
// pane — and Browse's handler both land in S4-T8, wave 4; until then a screen
// may emit it and nothing acts on it.
type OpenPathMsg struct {
	Path string // vault-relative, slash-separated, e.g. "wiki/concepts/kv-cache.md"
}

// paneMsg is the shell's private envelope: it tags a tea.Msg with the Screen
// whose pane produced it, so App.Update can hand a screen's own message back
// to that screen instead of to whichever screen happens to be active.
//
// It exists because a pane's tea.Cmd results reach the shell, not the pane —
// review's loadCmd, lintview's runReportCmd and logview's queryCmd all
// resolve to a message the shell then has to route. A message that is not in
// the named fan-out set (StreamMsg and its two siblings, pane.go above) used
// to land on the active pane, which ignores it, so a pane that was off screen
// when it issued a command never heard its own answer: press `r` on Log and
// the Review pane's loadCmd result was dropped, leaving Review stale when the
// jump landed.
//
// Only a pane's *own* messages are enveloped — never one the shell or the
// runtime consumes (see App.producedBy), which travel exactly as they did
// before, because a StageChangedMsg or SwitchScreenMsg a pane emits is a
// command to the shell, not that pane's answer.
//
// paneMsg is unexported and never delivered to a pane: Update unwraps it
// before any other case runs, so no screen ever sees one and no screen can
// produce one. Declared here with the rest of the shell's message
// vocabulary, because Update routes it and the shell never imports a screen
// package (backbone §12).
type paneMsg struct {
	from Screen // the pane that produced msg
	msg  tea.Msg
}

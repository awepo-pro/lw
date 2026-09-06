// app.go implements backbone §12's App: the Bubble Tea v2 shell that lays
// out the title bar, the STAGE sidebar, the active screen and the footer
// (/PLAN.md §9), cycles screens on tab, and quits cleanly on q/Ctrl-C.
//
// The shell never constructs a screen and never imports one — Options.Panes
// is injected by cmd/lw (backbone §12; s4-tui.md S4-T2 item 1).
package ui

import (
	"fmt"
	"path/filepath"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/lint"
)

// screenOrder is the fixed tab cycle — /PLAN.md §9's reading order —
// deterministic because it is a slice, never a map range.
var screenOrder = []Screen{ScreenBrowse, ScreenReview, ScreenAsk, ScreenLint, ScreenLog}

// screenNames labels a Screen for the placeholder the shell renders when
// Options.Panes has no entry for it yet.
var screenNames = map[Screen]string{
	ScreenBrowse: "browse",
	ScreenReview: "review",
	ScreenAsk:    "ask",
	ScreenLint:   "lint",
	ScreenLog:    "log",
}

// App is the Bubble Tea v2 shell (backbone §12). cmd/lw's cmdTUI is the
// only constructor of a real one; tests build one directly with NewApp and
// a fake Pane.
type App struct {
	deps  Deps
	panes map[Screen]Pane
	order []Screen
	cur   int

	width, height int

	vaultName  string
	pages      int
	raw        int
	lintErrors int

	stageID  string
	stageOps int

	quitting bool
}

var _ tea.Model = (*App)(nil)

// NewApp constructs the shell from o (backbone §12). When o.Engine is
// present, it is queried once for the vault counts the title bar shows and
// the changeset the STAGE panel shows; both are refreshed later by
// VaultReloadedMsg and StageChangedMsg rather than re-queried every frame.
// o.Engine may be nil — a headless test with no vault at all — in which
// case NewApp leaves the counts at zero instead of failing to construct.
func NewApp(o Options) *App {
	a := &App{
		deps:   o.Deps,
		panes:  o.Panes,
		order:  screenOrder,
		width:  80,
		height: 24,
	}
	if a.panes == nil {
		a.panes = map[Screen]Pane{}
	}
	for i, s := range a.order {
		if s == o.Start {
			a.cur = i
			break
		}
	}
	if o.Engine != nil {
		a.vaultName = filepath.Base(o.Engine.Vault().Root())
		a.refreshVaultCounts()
		a.refreshStage()
	}
	return a
}

// refreshVaultCounts recomputes the title bar's page, raw and lint-error
// counts from a.deps.Engine's current vault and index. A nil Engine leaves
// the counts untouched — there is nothing to read.
func (a *App) refreshVaultCounts() {
	if a.deps.Engine == nil {
		return
	}
	v := a.deps.Engine.Vault()
	a.pages = len(v.Pages())
	a.raw = len(v.RawSources())

	report := lint.Run(&lint.Context{
		Vault: v,
		Index: a.deps.Engine.Index(),
		Graph: v.Graph(),
	}, nil)
	a.lintErrors = report.Errors
}

// refreshStage recomputes the STAGE panel's changeset id and live op count
// from a.deps.Engine's currently open changeset, or clears both when none
// is open. A nil Engine leaves the panel showing no changeset.
func (a *App) refreshStage() {
	if a.deps.Engine == nil {
		return
	}
	c, err := a.deps.Engine.Current()
	if err != nil {
		a.stageID, a.stageOps = "", 0
		return
	}
	a.stageID = c.ID
	a.stageOps = len(c.Live())
}

// Init returns tea.RequestBackgroundColor so the shell learns the
// terminal's real polarity (backbone §12, C-81), batched with every
// injected pane's own Init.
func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor}
	for _, s := range a.order {
		if p := a.panes[s]; p != nil {
			if cmd := p.Init(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	return tea.Batch(cmds...)
}

// Update handles the shell-wide messages (resize, background polarity, the
// two shell-level keys, and the shell's own broadcast/routing messages) and
// forwards everything else — including keys the shell does not bind itself
// — to the active pane.
//
// Fan-out (backbone §12, s4-tui.md S4-T8, C-106/TD-4): StageChangedMsg,
// VaultReloadedMsg, tea.WindowSizeMsg and tea.BackgroundColorMsg go to
// every injected pane via propagateAll, since a pane that is off-screen
// still needs to know the vault or terminal changed under it. Every other
// message — tea.KeyPressMsg above all — stays on the active pane only via
// propagate: a keypress belongs to whichever screen the user is looking at.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, a.propagateAll(msg)

	case tea.BackgroundColorMsg:
		a.deps.Theme = a.deps.Theme.WithDark(msg.IsDark())
		return a, a.propagateAll(msg)

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, a.deps.Keys.Quit):
			a.quitting = true
			return a, tea.Quit
		case key.Matches(msg, a.deps.Keys.NextPane):
			a.cur = (a.cur + 1) % len(a.order)
			return a, nil
		}
		return a, a.propagate(msg)

	case StageChangedMsg:
		a.stageID = msg.ChangesetID
		a.stageOps = msg.Ops
		return a, a.propagateAll(msg)

	case VaultReloadedMsg:
		a.refreshVaultCounts()
		return a, a.propagateAll(msg)

	case SwitchScreenMsg:
		a.switchTo(msg.To)
		return a, nil

	case OpenPathMsg:
		// C-108/D-CU: switch to Browse first, then deliver the same message
		// straight to the Browse pane — not through propagate, which only
		// ever reaches whichever screen is *currently* active. S4-T5 emits
		// this batched with SwitchScreenMsg{ScreenBrowse}, and the two
		// arrive as separate Update calls in an order tea.Batch does not
		// guarantee, so this handler must reach the same end state
		// (Browse active, path selected) no matter which lands first.
		a.switchTo(ScreenBrowse)
		return a, a.deliverTo(ScreenBrowse, msg)

	default:
		return a, a.propagate(msg)
	}
}

// switchTo moves focus to s, a no-op if s is not one of the five screens in
// a.order.
func (a *App) switchTo(s Screen) {
	for i, sc := range a.order {
		if sc == s {
			a.cur = i
			return
		}
	}
}

// propagate forwards msg to the active pane's Update, if one is injected
// for the current screen, and stores the pane it returns back into the map
// — Pane.Update returns a (possibly new) Pane the same way tea.Model.Update
// returns a (possibly new) Model.
func (a *App) propagate(msg tea.Msg) tea.Cmd {
	return a.deliverTo(a.order[a.cur], msg)
}

// propagateAll forwards msg to every injected pane's Update, active or not
// (backbone §12, s4-tui.md S4-T8, C-106/TD-4) — used for the messages a
// pane must never miss regardless of which screen is on top: StageChangedMsg,
// VaultReloadedMsg, tea.WindowSizeMsg and tea.BackgroundColorMsg. Iterates
// a.order, a fixed slice, rather than ranging a.panes directly, so which
// pane's Update runs first stays deterministic even though no pane's
// returned Cmd depends on that order (00-conventions.md §3).
func (a *App) propagateAll(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for _, s := range a.order {
		if cmd := a.deliverTo(s, msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// deliverTo forwards msg to the pane at screen s specifically — regardless
// of which screen is currently active — and stores the (possibly new) pane
// it returns back into the map. propagate and propagateAll are both built
// on this; OpenPathMsg's handler in Update also calls it directly so the
// message reaches the Browse pane even on the frame Browse becomes active
// (C-108/D-CU).
func (a *App) deliverTo(s Screen, msg tea.Msg) tea.Cmd {
	p, ok := a.panes[s]
	if !ok || p == nil {
		return nil
	}
	updated, cmd := p.Update(msg)
	a.panes[s] = updated
	return cmd
}

// View renders the shell (backbone §12, C-79: v2's tea.Model returns
// tea.View, not string). AltScreen is a per-frame field in v2 — there is no
// tea.WithAltScreen program option (C-82).
func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	return v
}

// render composes one frame at the shell's current width and height. The
// active pane is given exactly the width and height bodyDimensions carves
// out for it — nothing may assume 80x24.
func (a *App) render() string {
	title := titleBarText(a.vaultName, a.pages, a.raw, a.lintErrors)
	sidebar := stageLines(a.stageID, a.stageOps)

	_, _, mainW, bodyH := bodyDimensions(a.width, a.height)

	s := a.order[a.cur]
	var mainContent string
	if p, ok := a.panes[s]; ok && p != nil {
		mainContent = p.View(mainW, bodyH)
	} else {
		mainContent = fmt.Sprintf("(%s screen not loaded yet)", screenNames[s])
	}

	return composeFrame(a.deps.Theme, a.width, a.height, title, footerBarText, sidebar, mainContent)
}

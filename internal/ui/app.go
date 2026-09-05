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
// two shell-level keys, and the three broadcast messages) and forwards
// everything else — including keys the shell does not bind itself — to the
// active pane.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, a.propagate(msg)

	case tea.BackgroundColorMsg:
		a.deps.Theme = a.deps.Theme.WithDark(msg.IsDark())
		return a, a.propagate(msg)

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
		return a, a.propagate(msg)

	case VaultReloadedMsg:
		a.refreshVaultCounts()
		return a, a.propagate(msg)

	case SwitchScreenMsg:
		a.switchTo(msg.To)
		return a, nil

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
	s := a.order[a.cur]
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

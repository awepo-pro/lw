// app.go implements backbone §12's App: the Bubble Tea v2 shell that lays
// out the title bar, the STAGE sidebar, the active screen and the footer
// (/.dev-notes/PLAN-v1.md §9), cycles screens on tab, and quits cleanly on q/Ctrl-C.
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

// screenOrder is the fixed tab cycle — /.dev-notes/PLAN-v1.md §9's reading order —
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
//
// A pane's Init command is enveloped with its screen (producedBy), exactly
// as Update envelops the commands that pane returns later: at startup every
// pane is off screen except the one we begin on, and review's Init is
// loadCmd — an unenveloped loadedMsg went to the start screen and was
// dropped, so Review rendered empty until something else happened to reload
// it.
func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor}
	for _, s := range a.order {
		if p := a.panes[s]; p != nil {
			if cmd := p.Init(); cmd != nil {
				cmds = append(cmds, producedBy(s, cmd))
			}
		}
	}
	return tea.Batch(cmds...)
}

// Update handles the shell-wide messages (resize, background polarity, the
// two shell-level keys, and the shell's own broadcast/routing messages). A
// message that arrives in a producer envelope (paneMsg — the answer to some
// pane's own command) is unwrapped and delivered to the pane that produced
// it, through the same switch; everything else, including keys the shell
// does not bind itself, goes to the active pane.
//
// Fan-out (backbone §12, s4-tui.md S4-T8, C-106/TD-4, C-117/D-DA): routing
// is by named type, not by "key or not". A message in the fan-out set goes
// to every injected pane via propagateAll, because the pane that needs it
// may be off screen — the vault or terminal changed under it, or, the case
// that motivated the rule, a background pump has to keep draining. Ask's
// event pump is exactly that: StreamMsg, EventMsg and StreamClosedMsg
// (pane.go) arrive as ordinary tea.Cmd results, so a mid-turn ctrl+r used
// to starve the pane — scrollback frozen, pump never re-armed, and past
// ask's 64-event buffer Agent.Send blocked with the pane stuck turnActive.
//
// Everything else goes to the active pane only, via propagate: a keypress
// belongs to whichever screen the user is looking at, and an off-screen
// pane must never be able to eat one. The guard tests the tea.KeyMsg
// *interface*, not tea.KeyPressMsg, so a key-release message cannot leak to
// an inactive pane either (C-80: v2 has both, and both satisfy tea.KeyMsg).
//
// The set is closed on purpose. C-117/D-DA's first fix broadcast every
// non-key message, which was fail-open: it routed a message by *not naming
// it*, so the next message type a pane emitted was silently re-routed to
// panes that have no business seeing it. Naming the three pump types here
// instead — they live in pane.go precisely so the shell, which never
// imports a screen package (backbone §12), can — closes that set for
// messages that arrive bare; and a message that arrives in a producer
// envelope (paneMsg) is closed the other way: it goes to the pane that
// produced it and to nothing else, so an off-screen pane's own traffic
// cannot leak onto the screen the user is looking at.
//
// Envelopes (paneMsg) are unwrapped before any case below runs — an envelope
// is never itself a key and never itself a broadcast — but only a pane's own
// message is ever enveloped (producedBy): the shell-level messages a pane
// emits (logview's revert emits StageChangedMsg and SwitchScreenMsg, ask's
// ctrl+r emits SwitchScreenMsg, lintview's enter emits OpenPathMsg) reach
// Update bare, and so are routed by exactly the cases below, unchanged. The
// default branch is the one whose behaviour changes: a message the shell
// neither acts on nor names now goes to the pane that produced it, instead of
// to whichever pane is active.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Unwrap first: an envelope is never itself a key or a broadcast, and
	// every case below matches on the payload, not on the envelope.
	var from Screen
	enveloped := false
	if env, ok := msg.(paneMsg); ok {
		from, msg, enveloped = env.from, env.msg, true
	}

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

	case StreamMsg:
		// C-117/D-DA: the ask pump, routed by name. Only ask consumes these
		// three, and it may be off screen for a whole turn.
		return a, a.propagateAll(msg)

	case EventMsg:
		return a, a.propagateAll(msg)

	case StreamClosedMsg:
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
		// A pane's own message goes back to the pane that produced it, and
		// to nothing else: this branch is where review's loadedMsg,
		// lintview's reportMsg and logview's eventsMsg arrive, and the pane
		// that issued the command may be off screen (press `r` on Log, and
		// Review — not active — still has to hear its own loadCmd result).
		// See Update's doc comment for why the fan-out set is named rather
		// than "everything not a key".
		//
		// A message that arrives bare — not from a pane's command — keeps
		// the S6 tightening's rule: active pane only, keys and non-keys
		// alike. Keys matched on the way in — tea.KeyPressMsg, never the
		// tea.KeyMsg interface, which double-fires once keyboard
		// enhancements are negotiated (C-80) — are the ordinary case here.
		if enveloped {
			return a, a.deliverTo(from, msg)
		}
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
// returns a (possibly new) Model. Whatever command the pane returns comes
// back tagged with its screen (deliverTo's producedBy), so its answer
// reaches it however the active screen has moved on in the meantime.
func (a *App) propagate(msg tea.Msg) tea.Cmd {
	return a.deliverTo(a.order[a.cur], msg)
}

// propagateAll forwards msg to every injected pane's Update, active or not
// (backbone §12, s4-tui.md S4-T8, C-106/TD-4, C-117/D-DA) — used for the
// named set of messages a pane must never miss regardless of which screen
// is on top: the shell's own StageChangedMsg, VaultReloadedMsg,
// tea.WindowSizeMsg and tea.BackgroundColorMsg, and the ask screen's stream
// pump (StreamMsg, EventMsg, StreamClosedMsg), which pane.go declares so
// Update can route them by name. Iterates a.order, a fixed slice, rather
// than ranging a.panes directly, so which pane's Update runs first stays
// deterministic even though no pane's returned Cmd depends on that order
// (00-conventions.md §3).
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
//
// The command the pane returns is enveloped with s (producedBy) before it
// goes back: a tea.Cmd's result is delivered to App.Update, not to the pane,
// so without the tag the answer to an off-screen pane's own command would be
// routed to whichever pane is active — the gap this closes.
func (a *App) deliverTo(s Screen, msg tea.Msg) tea.Cmd {
	p, ok := a.panes[s]
	if !ok || p == nil {
		return nil
	}
	updated, cmd := p.Update(msg)
	a.panes[s] = updated
	if cmd == nil {
		return nil
	}
	return producedBy(s, cmd)
}

// producedBy wraps cmd so the message it produces reaches Update tagged with
// the screen whose pane produced it (paneMsg). A nil cmd stays nil, and a
// cmd that produces no message stays a cmd that produces no message — the
// runtime treats a nil tea.Msg as nothing to deliver, and so does the
// envelope.
//
// A message the shell or the runtime itself consumes (shellOwned) is passed
// through untouched: it is a command to the shell, not the producing pane's
// answer, and Update would route it by the same named case either way.
// Leaving it bare keeps the shell's message stream exactly what it was
// before the envelope existed — which is what logview's revert, ask's ctrl+r
// and lintview's enter all depend on, and what anything watching that stream
// from outside ui is entitled to.
//
// A tea.BatchMsg is the one message the envelope must not carry: the runtime
// expands a batch itself and never hands one to Update, so an enveloped
// batch would arrive at Update's default branch as an ordinary message and
// be delivered, unexpanded, to a single pane — three quarters of logview's
// revert (its query, the StageChangedMsg and the jump to Review) would
// vanish into the log pane. Each constituent is wrapped with the same
// producer instead, which is what the runtime would have done with them.
func producedBy(from Screen, cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		msg := cmd()
		if msg == nil {
			return nil
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			var wrapped tea.BatchMsg
			for _, c := range batch {
				if c == nil {
					continue
				}
				wrapped = append(wrapped, producedBy(from, c))
			}
			if len(wrapped) == 0 {
				return nil
			}
			return wrapped
		}
		if shellOwned(msg) {
			return msg
		}
		return paneMsg{from: from, msg: msg}
	}
}

// shellOwned reports whether msg is addressed to the shell or the runtime
// rather than to a pane: the shell's own message vocabulary (pane.go) plus
// the runtime's key and quit traffic. See producedBy for why those are left
// bare. A message missing from this set is still routed correctly when it is
// enveloped — Update unwraps before any of its cases — so this list shapes
// who may watch the shell's message stream, never where a message goes.
func shellOwned(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.KeyReleaseMsg, tea.WindowSizeMsg,
		tea.BackgroundColorMsg, tea.QuitMsg,
		StageChangedMsg, VaultReloadedMsg, StreamMsg, EventMsg,
		StreamClosedMsg, SwitchScreenMsg, OpenPathMsg:
		return true
	default:
		return false
	}
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

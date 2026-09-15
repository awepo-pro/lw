// app.go implements contract §5's App: the Bubble Tea v2 shell that lays out
// the header, the active screen's body and the footer (the lazygit-style
// frame, contract §5 frame notes 1-6), cycles screens on tab, opens the `?`
// overlay, gates the whole frame below the 80×24 minimum, and quits cleanly
// on q/Ctrl-C.
//
// The shell never constructs a screen and never imports one — Options.Panes
// is injected by cmd/lw (backbone §12; s4-tui.md S4-T2 item 1).
package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
)

// screenOrder is the fixed tab cycle, in the header's reading order (D1:
// "Review Ask Lint Log Browse") — deterministic because it is a slice,
// never a map range.
var screenOrder = []Screen{ScreenReview, ScreenAsk, ScreenLint, ScreenLog, ScreenBrowse}

// screenNames labels a Screen for the placeholder the shell renders when
// Options.Panes has no entry for it yet.
var screenNames = map[Screen]string{
	ScreenBrowse: "browse",
	ScreenReview: "review",
	ScreenAsk:    "ask",
	ScreenLint:   "lint",
	ScreenLog:    "log",
}

// App is the Bubble Tea v2 shell (backbone §12, contract §5). cmd/lw's
// cmdTUI is the only constructor of a real one; tests build one directly
// with NewApp and a fake Pane.
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

	// The header's changeset summary (contract §5 frame note 6).
	stageID     string
	stageOps    int
	stageChecks stage.Checks
	hasStage    bool

	// overlayOpen is the `?` overlay's toggle state (contract §5 frame note 4).
	overlayOpen bool

	quitting bool
}

var _ tea.Model = (*App)(nil)

// NewApp constructs the shell from o (contract §5). When o.Engine is
// present, it is queried once for the vault counts the header shows and the
// changeset summary the header's right side shows; both are refreshed later
// by VaultReloadedMsg and StageChangedMsg rather than re-queried every
// frame. o.Engine may be nil — a headless test with no vault at all — in
// which case NewApp leaves the counts at zero instead of failing to
// construct.
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

// refreshVaultCounts recomputes the header's page, raw and lint-error
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

// refreshStage recomputes the header's changeset summary — id, live op
// count and checks — from a.deps.Engine's currently open changeset, or
// clears it when none is open. A nil Engine leaves the summary as it was
// (StageChangedMsg's own fields are the only source of truth in that case).
func (a *App) refreshStage() {
	if a.deps.Engine == nil {
		return
	}
	c, err := a.deps.Engine.Current()
	if err != nil {
		a.stageID, a.stageOps, a.stageChecks, a.hasStage = "", 0, stage.Checks{}, false
		return
	}
	a.stageID = c.ID
	a.stageOps = len(c.Live())
	a.stageChecks = c.Checks
	a.hasStage = true
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

// tooSmall reports whether the current size is below D11's minimum, in
// which case the shell renders only the too-small notice and refuses every
// key but quit (contract §5 frame note 3).
func (a *App) tooSmall() bool {
	return a.width < MinWidth || a.height < MinHeight
}

// Update handles the shell-wide messages (resize, background polarity, the
// shell-level keys, the `?` overlay's toggle, and the shell's own
// broadcast/routing messages). A message that arrives in a producer
// envelope (paneMsg — the answer to some pane's own command) is unwrapped
// and delivered to the pane that produced it, through the same switch;
// everything else, including keys the shell does not bind itself, goes to
// the active pane — except while the terminal is too small or the overlay
// is open, when no key reaches a pane at all (contract §5 frame notes 3-4).
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
		if a.tooSmall() {
			// Close the `?` overlay rather than let it survive a shrink
			// below D11's minimum: growing back would otherwise re-show it
			// unasked, with no key that opened it this time (repair-1,
			// Tier-1 review Minor finding).
			a.overlayOpen = false
		}
		return a, a.propagateAll(msg)

	case tea.BackgroundColorMsg:
		a.deps.Theme = a.deps.Theme.WithDark(msg.IsDark())
		return a, a.propagateAll(msg)

	case tea.KeyPressMsg:
		return a.handleKey(msg)

	case StageChangedMsg:
		a.stageID = msg.ChangesetID
		a.stageOps = msg.Ops
		a.hasStage = msg.ChangesetID != ""
		a.refreshStage()
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

// handleKey is tea.KeyPressMsg's own case, split out of Update because it
// has three modes rather than one: too small (only quit), the `?` overlay
// open (quit and close only, everything else swallowed) and the ordinary
// case (the shell's own keys, then the active pane) — contract §5 frame
// notes 3-4.
func (a *App) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if a.tooSmall() {
		if key.Matches(msg, a.deps.Keys.Quit) {
			a.quitting = true
			return a, tea.Quit
		}
		return a, nil
	}

	if a.overlayOpen {
		switch {
		case key.Matches(msg, a.deps.Keys.Quit):
			a.quitting = true
			return a, tea.Quit
		case key.Matches(msg, a.deps.Keys.Help), msg.String() == "esc":
			a.overlayOpen = false
			return a, nil
		}
		return a, nil
	}

	switch {
	case key.Matches(msg, a.deps.Keys.Quit):
		a.quitting = true
		return a, tea.Quit
	case key.Matches(msg, a.deps.Keys.Help):
		a.overlayOpen = true
		return a, nil
	case key.Matches(msg, a.deps.Keys.NextPane):
		a.cur = (a.cur + 1) % len(a.order)
		return a, nil
	}
	return a, a.propagate(msg)
}

// switchTo, activePane, propagate, propagateAll, deliverTo, producedBy and
// shellOwned moved to route.go (Tier-1 review, MASTER §8 ORCH-4/repair-1),
// so app.go stays under conventions §2's ~400-line guideline. Update's
// switch above still calls them unchanged — Go methods bind to the type,
// not the file.

// View renders the shell (backbone §12, C-79: v2's tea.Model returns
// tea.View, not string). AltScreen is a per-frame field in v2 — there is no
// tea.WithAltScreen program option (C-82).
func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	return v
}

// render composes one frame at the shell's current width and height:
// below D11's minimum, only the too-small notice (contract §5 frame note
// 3); otherwise the header, the active pane's body and the footer, with the
// `?` overlay composited on top when it is open (contract §5 frame note 4).
// Nothing here assumes 80x24 beyond the minimum itself.
func (a *App) render() string {
	if a.tooSmall() {
		return strings.Join(tooSmallView(a.deps.Theme, a.width, a.height), "\n")
	}

	bodyH := a.height - 2

	s := a.order[a.cur]
	var content string
	if p, ok := a.panes[s]; ok && p != nil {
		content = p.View(a.width, bodyH)
	} else {
		content = fmt.Sprintf("(%s screen not loaded yet)", screenNames[s])
	}

	lines := make([]string, 0, a.height)
	lines = append(lines, headerLine(a.deps.Theme, a.width, a.vaultName, tabLabels(), screenTabName[s],
		a.pages, a.raw, a.lintErrors,
		headerStage{ID: a.stageID, Ops: a.stageOps, Checks: a.stageChecks, Has: a.hasStage}))
	lines = append(lines, fitPaneLines(content, a.width, bodyH)...)
	lines = append(lines, footerContent(a.deps.Theme, a.activePane(), a.width))

	frame := strings.Join(lines, "\n")
	if !a.overlayOpen {
		return frame
	}

	var oh OverlayHelper
	if p := a.activePane(); p != nil {
		oh, _ = p.(OverlayHelper)
	}
	box, bw, bh, x, y := overlayBox(a.deps.Theme, oh, a.width, a.height)
	plain := stripFrame(frame)
	return strings.Join(compositeOverlay(a.deps.Theme, plain, box, x, y, bw, bh), "\n")
}

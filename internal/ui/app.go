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
	"log/slog"
	"path/filepath"
	"time"

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

	// reloadEvery is Options.ReloadEvery: the period of the periodic
	// Engine.ReloadIfChanged tick (008 contract §5, reload.go). Zero
	// disables it — every test harness and conformance grid runs at zero.
	reloadEvery time.Duration

	// Launch timing (025 T3): frameStart anchors the once-only "tui first
	// frame" line at the NewApp moment — internal/ui cannot read a cmd/lw
	// var and Options carries no start time, so the pre-NewApp stretch
	// (config, engine open, pane construction) stays outside it; engine
	// open has its own launch line. firstFrameLogged keeps that line to
	// one emission: Update re-enters on every resize.
	frameStart       time.Time
	firstFrameLogged bool

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
		deps:        o.Deps,
		panes:       o.Panes,
		order:       screenOrder,
		width:       80,
		height:      24,
		reloadEvery: o.ReloadEvery,
	}
	a.frameStart = o.ProcessStart
	if a.frameStart.IsZero() {
		a.frameStart = time.Now()
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
		// The launch refresh: the header counts (whose lint.Run is a real
		// slice of cold start) and the changeset summary. 025 T3 logs its
		// duration; the refresh itself is untouched.
		start := time.Now()
		a.refreshVaultCounts()
		a.refreshStage()
		slog.Info("tui shell ready", "dur_ms", msSince(start), "lint_errors", a.lintErrors)
	}
	return a
}

// logFirstFrame emits the launch's once-only "tui first frame" line. It
// fires on the first tea.WindowSizeMsg — the moment the shell first knows
// the geometry the real frame renders at; tea's renderer composes that
// frame straight after. View itself re-enters for every frame, so the
// once-only guard lives here on the App, not in the render path. The
// duration runs from Options.ProcessStart — cmd/lw's package-init clock,
// so it covers config loads, vault root discovery, OpenEngine, the launch
// refresh, tea program construction, terminal setup and the wait for first
// geometry (F.W8: "since process start"). A harness that leaves
// ProcessStart zero measures from NewApp instead. The panes' first loads
// are not in it — tea delivers the initial size before model.Init's
// commands have landed.
func (a *App) logFirstFrame() {
	if a.firstFrameLogged {
		return
	}
	a.firstFrameLogged = true
	if a.frameStart.IsZero() {
		return // never anchored (a zero-value App) — nothing honest to report
	}
	slog.Info("tui first frame", "dur_ms", msSince(a.frameStart))
}

// msSince returns milliseconds since t as a fractional float — the dur_ms
// field every 025 T3 launch line carries (integer milliseconds would round
// the fast paths down to 0).
func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000
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
	// 008 contract §5: the periodic vault reload. Zero — every test harness
	// and every conformance grid — schedules nothing.
	if a.reloadEvery > 0 {
		cmds = append(cmds, reloadTickCmd(a.reloadEvery))
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
		a.logFirstFrame()
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

	case tea.ColorProfileMsg:
		// The terminal's real colour profile is known: re-resolve the
		// cursor tint for it (contract §5 frame note 8, W5 F1/C35) and let
		// every pane rebuild its own copy the way it does for polarity.
		a.deps.Theme = a.deps.Theme.WithProfile(msg.Profile)
		return a, a.propagateAll(msg)

	case tea.MouseWheelMsg:
		return a, a.handleWheel(msg)

	case tea.MouseMsg:
		// Mouse mode is on, so clicks, releases and motion arrive too: the
		// wheel is the only mouse input a pane sees (contract §5 frame
		// note 7 — selection is shift+drag, D-3W).
		return a, nil

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

	case reloadTickMsg:
		// 008 contract §5: the periodic-reload tick (reload.go). The engine
		// read happens here, on Update's goroutine — never inside a tea.Cmd.
		return a, a.handleReloadTick()

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
// case (a text-taking pane's printable keys first, C27/D-3Q, then the
// shell's own keys, then the active pane) — contract §5 frame notes 3-4.
func (a *App) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if a.tooSmall() {
		if key.Matches(msg, a.deps.Keys.Quit) {
			a.quitting = true
			return a, tea.Quit
		}
		// A key swallowed below the minimum still tells the active pane that
		// a key went by (008 A-801 F-3) — review's raw-only confirmation
		// disarms on it exactly as on every other shell-consumed key.
		return a, a.propagate(ShellKeyMsg{})
	}

	if a.overlayOpen {
		switch {
		case key.Matches(msg, a.deps.Keys.Quit):
			a.quitting = true
			return a, tea.Quit
		case key.Matches(msg, a.deps.Keys.Help), msg.String() == "esc":
			a.overlayOpen = false
			return a, a.propagate(ShellKeyMsg{})
		}
		// Any other key is swallowed while the overlay is open — but a pane
		// whose state keys can undo (review's raw-only confirmation, A-801)
		// must still hear that one did.
		return a, a.propagate(ShellKeyMsg{})
	}

	// A pane that is taking text input types the printable keys itself: `q`
	// and `?` belong to its input box, not to Quit and Help (C27/D-3Q). The
	// non-printable globals — ctrl+c, tab — still match below.
	if a.paneCapturesKey(msg) {
		return a, a.propagate(msg)
	}

	switch {
	case key.Matches(msg, a.deps.Keys.Quit):
		a.quitting = true
		return a, tea.Quit
	case key.Matches(msg, a.deps.Keys.Help):
		a.overlayOpen = true
		return a, a.propagate(ShellKeyMsg{})
	case key.Matches(msg, a.deps.Keys.NextPane):
		return a, a.nextPane()
	}
	return a, a.propagate(msg)
}

// switchTo, activePane, propagate, propagateAll, deliverTo, producedBy,
// shellOwned and handleWheel live in route.go, and View and render in
// view.go — file splits so app.go stays under conventions §2's ~400-line
// guideline (Tier-1 review, MASTER §8 ORCH-4/repair-1). Update's switch
// above still calls them unchanged — Go methods bind to the type, not the
// file.

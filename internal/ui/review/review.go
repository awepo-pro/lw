// review.go implements the changeset review screen's model: walking the
// open changeset's ops and hunks, accepting and dropping hunks through
// stage.Engine, and committing with the same lint-regression gate
// cmd/lw's `lw commit` applies — Engine.Commit itself performs no such
// check. LintBaseline (S6-C127) is the one shared implementation of that
// gate's baseline computation; cmd/lw imports this package already
// (cmd_tui.go) and calls it directly, which closes that half of TD-3.
//
// Rendering lives in view.go (frame layout), diffblock.go (the Detail
// panel's Diff and Preview modes) and opsview.go (Ops and Changeset
// panels); the cursor model is hunks.go and the load round trip is
// load.go. Every mutation goes through stage.Engine; nothing in this
// package writes a vault file directly (00-conventions.md §5.4).
package review

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// Model is the review screen (backbone §12 ui.Pane). New captures Deps
// and nothing else; Init is what loads the open changeset and its diff.
type Model struct {
	deps  ui.Deps
	theme ui.Theme // C-81: a copy taken at construction, rebuilt locally on tea.BackgroundColorMsg
	md    *markdown.Renderer

	hasChangeset bool
	loadErr      error
	changeset    *stage.Changeset
	diff         stage.Diff
	ops          []stage.Op // every op, dropped included: the Ops panel's list (load.go)
	opDiffs      map[string][]stage.FileOpDiff
	stops        []cursorStop
	cursor       int
	preview      bool // p toggles the Detail panel between Diff and Preview

	// off is the Detail panel's scroll offset: the content lines hidden
	// above the panel (scroll.go; contract §5 frame note 10, W5 F2/C36).
	off int
	// paneW and paneH are the pane's last known View size, from the
	// shell's tea.WindowSizeMsg (View receives w, h-2). The scroll keys'
	// steps and clamps read the Detail panel's geometry from it.
	paneW, paneH int

	status string         // transient StatusReporter message; "" shows the bindings
	level  ui.StatusLevel // the level the footer styles status with

	// commitArmedFor is the raw-only commit confirmation (008 contract §5,
	// commit.go): the first C on a raw-only changeset warns and arms with
	// that changeset's id; the next C commits only while the engine's open
	// changeset still has that id. Any other key disarms, and so does a
	// load of a different id (C-807): a swap with no key press — another
	// process's commit reloading in a new changeset — must not inherit the
	// arm and commit unwarned.
	commitArmedFor string

	// loadsInFlight counts the load commands this pane has returned whose
	// loadedMsg has not landed back in Update yet (008 contract §8, A-801,
	// load()'s doc comment). A count, not a flag, because loads overlap: a
	// commit batches one beside the broadcast handlers' own.
	loadsInFlight int
}

var (
	_ ui.Pane           = (*Model)(nil)
	_ ui.FooterHelper   = (*Model)(nil)
	_ ui.OverlayHelper  = (*Model)(nil)
	_ ui.StatusReporter = (*Model)(nil)
	_ ui.Scroller       = (*Model)(nil)
	_ ui.EngineUser     = (*Model)(nil)
)

// New constructs the review screen (backbone §12). It captures d and
// creates the shared markdown renderer — nothing else; Init is what loads
// the open changeset and its diff, and every mutation from then on goes
// through d.Engine.
func New(d ui.Deps) ui.Pane {
	return &Model{deps: d, theme: d.Theme, md: markdown.NewRenderer()}
}

// Title returns the pane's name for the shell's tab bar (backbone §12).
func (m *Model) Title() string { return "Review" }

// Help returns the review screen's key bindings (backbone §12) — the
// fallback list a shell shows when a pane implements no FooterHelper.
// The footer itself is FooterHelp (keys.go), whose order is the frozen
// one; this list keeps the same bindings.
func (m *Model) Help() []key.Binding {
	k := m.deps.Keys
	return []key.Binding{
		k.AcceptHunk, k.DropHunk, k.MoveDown, k.Preview, k.AcceptAll,
		k.Commit, k.Reject, k.Top, k.NextPane, k.Quit,
	}
}

// Init loads the currently open changeset and its diff (s2-screens.md
// T06; the load round trip is load.go).
func (m *Model) Init() tea.Cmd {
	return m.load()
}

// Update handles the review keymap and the shell messages this screen
// cares about. The shell's App.propagate delivers StageChangedMsg and
// VaultReloadedMsg only while this pane is active, so this Update must
// never assume it saw every event — it reloads cheaply whenever one does
// arrive, and every mutation reloads on its own besides.
func (m *Model) Update(msg tea.Msg) (ui.Pane, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.theme = m.theme.WithDark(msg.IsDark())
		return m, nil

	case tea.ColorProfileMsg:
		// Contract §5 frame note 8 (W5 F1): rebuild the theme for the
		// terminal's real colour profile — the cursor tint re-resolves,
		// exactly as WithDark rebuilds it for polarity.
		m.theme = m.theme.WithProfile(msg.Profile)
		return m, nil

	case tea.WindowSizeMsg:
		// The shell gives the pane View(w, h-2); the scroll keys read the
		// Detail panel's geometry from that size (scroll.go).
		m.paneW, m.paneH = msg.Width, msg.Height-2
		return m, nil

	case ui.WheelMsg:
		return m.handleWheel(msg)

	case ui.StageChangedMsg:
		return m, m.load()

	case ui.VaultReloadedMsg:
		return m, m.load()

	case loadedMsg:
		// The load round trip's answer (load.go): the trip this counts is
		// over, so the in-flight count comes down before the result installs.
		if m.loadsInFlight > 0 {
			m.loadsInFlight--
		}
		m.applyLoaded(msg)
		return m, nil

	case ui.ShellKeyMsg:
		// 008 A-801 (G5 review M-1): a key the shell consumed — tab's
		// screen switch, the `?` overlay opening, closing, or swallowing a
		// key — never reaches handleKey, so contract §5's "any other key
		// disarms" silently failed for exactly the keys a curator presses
		// while walking between screens. The shell reports them here; the
		// arm dies like it would for any pane-visible key.
		m.commitArmedFor = ""
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// load arms one load round trip: it counts the trip as in flight and
// returns the loadCmd (load.go). EngineBusy is therefore already true when
// the command leaves this pane and stays true until its loadedMsg has been
// applied back in Update — the window in which loadCmd reads the Engine
// (Current, Diff, OpDiff) on a tea.Cmd goroutine, the window the shell's
// reload tick must not reload under (008 contract §8, A-801, G5 review
// I-1). Every loadCmd this pane issues goes through here.
func (m *Model) load() tea.Cmd {
	m.loadsInFlight++
	return loadCmd(m.deps.Engine)
}

// EngineBusy implements ui.EngineUser (008 contract §8, A-801): true while
// a load command this pane returned has not delivered its loadedMsg yet.
func (m *Model) EngineBusy() bool { return m.loadsInFlight > 0 }

// handleKey dispatches one tea.KeyPressMsg against the review keymap. A
// changeset that is not open makes every key a no-op — there is nothing
// to accept, drop, reject or commit, and no cursor to move.
func (m *Model) handleKey(msg tea.KeyPressMsg) (ui.Pane, tea.Cmd) {
	if !m.hasChangeset {
		return m, nil
	}
	k := m.deps.Keys
	// D-8B (008 contract §5): the raw-only commit confirmation is
	// single-shot. Any key but C — movement, accept, drop, reject, scroll —
	// disarms it, so a stale confirmation can never survive into what the
	// reviewer looks at next.
	if !key.Matches(msg, k.Commit) {
		m.commitArmedFor = ""
	}
	switch {
	case key.Matches(msg, k.MoveDown):
		m.cursor = clampCursor(m.cursor+1, len(m.stops))
		m.resetScroll()
	case key.Matches(msg, k.MoveUp):
		m.cursor = clampCursor(m.cursor-1, len(m.stops))
		m.resetScroll()
	case key.Matches(msg, k.Top):
		m.cursor = 0
		m.resetScroll()
	case key.Matches(msg, k.Bottom):
		m.cursor = clampCursor(len(m.stops)-1, len(m.stops))
		m.resetScroll()
	case key.Matches(msg, k.Preview):
		// s2-screens.md T06: p toggles Diff ↔ Preview and keeps the
		// cursor; the Preview follows whatever op the cursor lands on.
		// The toggle resets the Detail scroll too — different content
		// under the fold (s2-screens.md T06 Scroll).
		m.preview = !m.preview
		m.resetScroll()
	case key.Matches(msg, k.AcceptHunk):
		return m.acceptHunk()
	case key.Matches(msg, k.DropHunk):
		return m.dropHunk()
	case key.Matches(msg, k.SplitHunk):
		// C-90/TD-2: hunk splitting is not built (the ? overlay says so).
		// No engine call.
		m.setStatus(ui.StatusInfo, "hunk split is not built yet — drop the op and re-propose")
	case key.Matches(msg, k.AcceptAll):
		return m.acceptAll()
	case key.Matches(msg, k.Reject):
		return m.reject()
	case key.Matches(msg, k.Commit):
		return m.commit()
	case key.Matches(msg, k.ScrollPageDown):
		// The Detail panel scrolls, whichever mode p has it in
		// (contract §5 frame note 10).
		m.scrollPage(1)
	case key.Matches(msg, k.ScrollPageUp):
		m.scrollPage(-1)
	case key.Matches(msg, k.ScrollHalfDown):
		m.scrollHalf(1)
	case key.Matches(msg, k.ScrollHalfUp):
		m.scrollHalf(-1)
	case key.Matches(msg, k.ScrollTop):
		m.resetScroll()
	case key.Matches(msg, k.ScrollBottom):
		m.off = m.maxScrollOff()
	}
	return m, nil
}

// reviewRefusal names the reason the cursor's hunk must not be accepted
// or dropped, or "" when the key may proceed. Two refusals, both decided
// BEFORE any engine call (s2-screens.md T06 keys, MASTER §8 ORCH-9):
//
//   - a stale op: Engine.DropHunk/UndropHunk have no stale guard, and
//     OpDiff shows a stale op's windows with HunkID "" — acting on a key
//     would touch content the reviewer cannot see as attributed;
//   - an ownerless window: a window with HunkID "" (a create, an ingest,
//     a derived index.md) has no persisted hunk to accept or drop.
func (m *Model) reviewRefusal(opID, hunkID string) string {
	if op, ok := findOp(m.ops, opID); ok && op.State == stage.StateStale {
		return fmt.Sprintf("op %s is stale: refresh before reviewing its hunks", opID)
	}
	if !hasWindow(m.opDiffs[opID], hunkID) {
		return "this window has no hunk id — it cannot be accepted or dropped individually"
	}
	return ""
}

// acceptHunk is `y`: Engine.UndropHunk (C-89/D-CL). Hunks are live by
// default, so undropping one that is already live succeeds as a no-op —
// the engine's own contract, not a special case here — and the cursor
// still advances so walking the review with `y` alone visits every hunk.
func (m *Model) acceptHunk() (ui.Pane, tea.Cmd) {
	opID, hunkID, ok := resolveCursor(m.diff, m.stops, m.cursor)
	if !ok {
		return m, nil
	}
	if refusal := m.reviewRefusal(opID, hunkID); refusal != "" {
		m.setStatus(ui.StatusWarn, refusal)
		return m, nil
	}
	if err := m.deps.Engine.UndropHunk(opID, hunkID); err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("accept hunk failed: %v", err))
		return m, nil
	}
	m.setStatus(ui.StatusInfo, "")
	m.cursor = clampCursor(m.cursor+1, len(m.stops))
	m.resetScroll() // the y advance is a cursor-stop change (T06 Scroll)
	return m, tea.Batch(m.load(), stageChangedCmd(m.deps.Engine))
}

// dropHunk is `n`: Engine.DropHunk.
func (m *Model) dropHunk() (ui.Pane, tea.Cmd) {
	opID, hunkID, ok := resolveCursor(m.diff, m.stops, m.cursor)
	if !ok {
		return m, nil
	}
	if refusal := m.reviewRefusal(opID, hunkID); refusal != "" {
		m.setStatus(ui.StatusWarn, refusal)
		return m, nil
	}
	if err := m.deps.Engine.DropHunk(opID, hunkID); err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("drop hunk failed: %v", err))
		return m, nil
	}
	m.setStatus(ui.StatusInfo, "")
	m.cursor = clampCursor(m.cursor+1, len(m.stops))
	m.resetScroll() // the n advance is a cursor-stop change (T06 Scroll)
	return m, tea.Batch(m.load(), stageChangedCmd(m.deps.Engine))
}

// acceptAll is `A`: refused on a stale op (before ANY engine call — a
// refusal must not touch the changeset it warns about), then refused
// unless the projected tree lints clean (s4-tui.md S4-T3 item 8,
// /docs/design.md §14's review-fatigue mitigation). A lint refusal
// changes nothing — no hunk is touched and no reload is issued.
func (m *Model) acceptAll() (ui.Pane, tea.Cmd) {
	e := m.deps.Engine

	for _, op := range m.ops {
		if op.State == stage.StateStale {
			m.setStatus(ui.StatusWarn, fmt.Sprintf("op %s is stale: refresh before reviewing its hunks", op.ID))
			return m, nil
		}
	}

	report, err := e.ProjectedReport()
	if err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("accept-all failed: %v", err))
		return m, nil
	}
	if !report.Clean() {
		m.setStatus(ui.StatusWarn, fmt.Sprintf(
			"accept-all refused: %d lint error(s), %d warning(s) projected — fix them or accept hunk by hunk",
			report.Errors, report.Warns))
		return m, nil
	}

	cs, err := e.Current()
	if err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("accept-all failed: %v", err))
		return m, nil
	}
	for _, op := range flattenLiveOps(cs) {
		for _, h := range op.Hunks {
			if !h.Dropped {
				continue
			}
			if err := e.UndropHunk(op.ID, h.ID); err != nil {
				m.setStatus(ui.StatusWarn, fmt.Sprintf("accept-all failed on %s/%s: %v", op.ID, h.ID, err))
				return m, tea.Batch(m.load(), stageChangedCmd(e))
			}
		}
	}
	m.setStatus(ui.StatusInfo, "")
	return m, tea.Batch(m.load(), stageChangedCmd(e))
}

// reject is `X`: Engine.Reject. There is no changeset left to show
// afterward, so the emitted StageChangedMsg is the empty one directly —
// Current would return stage.ErrNoChangeset and produce the identical
// value, but stating it directly needs no round trip through the engine.
func (m *Model) reject() (ui.Pane, tea.Cmd) {
	if err := m.deps.Engine.Reject("rejected in review"); err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("reject failed: %v", err))
		return m, nil
	}
	m.setStatus(ui.StatusInfo, "")
	return m, tea.Batch(m.load(), func() tea.Msg { return ui.StageChangedMsg{} })
}

// setStatus records the message the footer renders until the next key
// handler replaces or clears it (contract §5 StatusReporter; panes no
// longer draw a status line inside their own View).
func (m *Model) setStatus(level ui.StatusLevel, msg string) {
	m.level = level
	m.status = msg
}

// Status is the shell's StatusReporter hook (contract §5): the transient
// message and the level the footer styles it with. An empty message keeps
// the footer on the bindings.
func (m *Model) Status() (string, ui.StatusLevel) { return m.status, m.level }

// findOp returns the op with id from ops, and false when no such op is
// listed.
func findOp(ops []stage.Op, id string) (stage.Op, bool) {
	if id == "" {
		return stage.Op{}, false
	}
	for _, op := range ops {
		if op.ID == id {
			return op, true
		}
	}
	return stage.Op{}, false
}

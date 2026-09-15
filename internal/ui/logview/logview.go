// logview.go is the log screen's model and lifecycle: construction, the
// message loop, the journal query plumbing, and the optional shell
// interfaces the frame redesign hands out (003 contract §5/§7 —
// FooterHelper, OverlayHelper, StatusReporter). The view itself lives in
// view.go, the row rendering in rows.go, the keys in keys.go and the
// filter in filter.go.
package logview

import (
	"errors"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// errNoEngine is what queryCmd reports when this pane was constructed
// with a nil Deps.Engine (a headless shell with no vault at all, matching
// S4-T2's own "constructible headless" contract) — rendered visibly, never
// panicking.
var errNoEngine = errors.New("logview: no engine loaded")

// Model is the log screen (backbone §12 ui.Pane).
type Model struct {
	deps  ui.Deps
	theme ui.Theme // C-81: a copy taken at construction, rebuilt locally on tea.BackgroundColorMsg

	filter  filterKind
	hasLoad bool
	loadErr error
	events  []stage.Event // Query's own order: oldest-first, i.e. newest-last

	cursor int

	status string
	level  ui.StatusLevel
}

var (
	_ ui.Pane           = (*Model)(nil)
	_ ui.FooterHelper   = (*Model)(nil)
	_ ui.OverlayHelper  = (*Model)(nil)
	_ ui.StatusReporter = (*Model)(nil)
)

// New constructs the log screen (backbone §12). It captures d and nothing
// else — Init is what runs the first query.
func New(d ui.Deps) ui.Pane {
	return &Model{deps: d, theme: d.Theme}
}

// Title returns the pane's name for the shell's tab bar (backbone §12).
func (m *Model) Title() string { return "Log" }

// Help returns the log screen's key bindings (backbone §12). f and r have
// no KeyMap field of their own — ui.KeyMap belongs to the shell and gets
// no additions here (s4-tui.md's precedent in browse.go) — so they are
// described with ad-hoc bindings for display purposes only; only
// MoveDown/MoveUp/Top/Bottom are matched through d.Keys. The footer shows
// FooterHelp's merged list instead; this is the full per-key list.
func (m *Model) Help() []key.Binding {
	k := m.deps.Keys
	return []key.Binding{
		k.MoveDown, k.MoveUp, k.Top, k.Bottom,
		key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "revert")),
	}
}

// FooterHelp is the footer's display list (003 contract §5): today's
// bindings in today's order, with the merged labels contract §4 gives the
// movement pairs (MoveDown carries the "j/k move" label, Top the
// "g/G top/bottom" one, so MoveUp and Bottom are omitted) and one-word
// descriptions (s2-screens.md T09's footer rule, which T10 defers to). The
// shell appends "? help" itself.
func (m *Model) FooterHelp() []key.Binding {
	k := m.deps.Keys
	return []key.Binding{
		k.MoveDown, k.Top,
		key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "revert")),
	}
}

// OverlayHelp is the Log section of the `?` overlay (003 contract §5/§7).
func (m *Model) OverlayHelp() (string, []ui.HelpEntry) {
	return "Log", []ui.HelpEntry{
		{Key: "j/k", Desc: "down / up"},
		{Key: "g/G", Desc: "top / bottom"},
		{Key: "f", Desc: "cycle filter"},
		{Key: "r", Desc: "revert commit"},
	}
}

// Status reports the pane's transient message (003 contract §5): while it
// is non-empty the shell's footer shows it instead of the bindings, so the
// pane draws no status line of its own any more.
func (m *Model) Status() (string, ui.StatusLevel) {
	return m.status, m.level
}

// eventsMsg carries the result of running a journal query. Unexported:
// nothing outside this package's Update ever sees one.
type eventsMsg struct {
	events []stage.Event
	err    error
}

// queryCmd returns a tea.Cmd that runs f against e's journal.
func queryCmd(e *stage.Engine, f stage.Filter) tea.Cmd {
	return func() tea.Msg {
		if e == nil {
			return eventsMsg{err: errNoEngine}
		}
		evs, err := e.Journal().Query(f)
		if err != nil {
			return eventsMsg{err: err}
		}
		return eventsMsg{events: evs}
	}
}

// Init runs the first (all) journal query.
func (m *Model) Init() tea.Cmd {
	return queryCmd(m.deps.Engine, m.filter.query())
}

// Update handles the log keymap and the shell messages this screen cares
// about (C-80: matches tea.KeyPressMsg, never tea.KeyMsg).
//
// Contract (C-106/TD-4): this pane is only sent messages while it is the
// active screen, so it must never assume it saw every StageChangedMsg or
// VaultReloadedMsg — it re-runs the current filter's query cheaply
// whenever one does arrive, never touching the shell to fix the gap.
func (m *Model) Update(msg tea.Msg) (ui.Pane, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.theme = m.theme.WithDark(msg.IsDark())
		return m, nil

	case tea.ColorProfileMsg:
		// C-81 again, for the profile: the cursor tint is the one token
		// that depends on it (contract §5 frame note 8, W5 F1).
		m.theme = m.theme.WithProfile(msg.Profile)
		return m, nil

	case ui.StageChangedMsg:
		return m, queryCmd(m.deps.Engine, m.filter.query())

	case ui.VaultReloadedMsg:
		return m, queryCmd(m.deps.Engine, m.filter.query())

	case eventsMsg:
		m.applyEvents(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// applyEvents installs a queryCmd result onto m. The cursor jumps to the
// newest event (the end of the list) only on the very first successful
// load; every later reload preserves and merely clamps the cursor, so a
// filter cycle or an unrelated StageChangedMsg does not yank the view away
// from whatever row the reviewer was looking at.
func (m *Model) applyEvents(msg eventsMsg) {
	if msg.err != nil {
		m.loadErr = msg.err
		m.hasLoad = false
		return
	}
	firstLoad := !m.hasLoad
	m.hasLoad = true
	m.loadErr = nil
	m.events = msg.events
	if firstLoad {
		m.cursor = len(m.events) - 1
	}
	m.clampCursor()
}

// clampCursor keeps m.cursor inside [0, len(m.events)-1], or 0 when
// m.events is empty.
func (m *Model) clampCursor() {
	if len(m.events) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.events) {
		m.cursor = len(m.events) - 1
	}
}

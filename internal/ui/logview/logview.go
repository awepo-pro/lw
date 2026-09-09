// logview.go implements the journal/log screen (backbone §12 Pane, §5.7
// Journal/Event/Filter, §5.8 Revert; s4-tui.md S4-T5): the journal
// rendered newest-last, filterable by five stage.Filter queries cycled
// with `f`, and `r` reverting a commit-bearing event into a new
// reviewable changeset.
//
// Every mutation goes through stage.Engine; nothing here writes to the
// vault directly (00-conventions.md §5.4).
package logview

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// errNoEngine is what queryCmd reports when this pane was constructed
// with a nil Deps.Engine (a headless shell with no vault at all, matching
// S4-T2's own "constructible headless" contract) — rendered visibly, never
// panicking.
var errNoEngine = errors.New("logview: no engine loaded")

// statusKind selects which of Theme's status colours a Model's status
// line renders with (mirrors internal/ui/review's own statusKind).
type statusKind int

const (
	statusNone statusKind = iota
	statusGood
	statusWarn
)

// filterKind is one of the five stage.Filter queries `f` cycles through
// (pinned item 4), in this fixed order.
type filterKind int

const (
	filterAll filterKind = iota
	filterAccepted
	filterRejected
	filterAgent
	filterHuman
	filterCount // sentinel: the number of states, for cycling with %
)

// label is filterKind's name, shown in the header line.
func (k filterKind) label() string {
	switch k {
	case filterAccepted:
		return "accepted"
	case filterRejected:
		return "rejected"
	case filterAgent:
		return "agent"
	case filterHuman:
		return "human"
	default:
		return "all"
	}
}

// query is the stage.Filter k selects with (pinned item 4, backbone §5.7).
// EvOpAccepted has no writer in v0.1 (backbone §5.7's own contract), so
// "accepted" resolves in practice to every commit_end; it stays in the
// Kinds list because a future writer of op_accepted must not need this
// screen edited to pick it up.
func (k filterKind) query() stage.Filter {
	switch k {
	case filterAccepted:
		return stage.Filter{Kinds: []stage.EventKind{stage.EvOpAccepted, stage.EvCommitEnd}}
	case filterRejected:
		return stage.Filter{Kinds: []stage.EventKind{stage.EvOpDropped, stage.EvHunkDropped, stage.EvChangesetRejected}}
	case filterAgent:
		return stage.Filter{ActorKind: "agent"}
	case filterHuman:
		return stage.Filter{ActorKind: "human"}
	default:
		return stage.Filter{} // "all": a zero Filter matches every event (backbone §5.7)
	}
}

// Model is the log screen (backbone §12 ui.Pane).
type Model struct {
	deps  ui.Deps
	theme ui.Theme // C-81: a copy taken at construction, rebuilt locally on tea.BackgroundColorMsg

	filter  filterKind
	hasLoad bool
	loadErr error
	events  []stage.Event // Query's own order: oldest-first, i.e. newest-last

	cursor int

	status     string
	statusKind statusKind
}

var _ ui.Pane = (*Model)(nil)

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
// MoveDown/MoveUp/Top/Bottom are matched through d.Keys.
func (m *Model) Help() []key.Binding {
	k := m.deps.Keys
	return []key.Binding{
		k.MoveDown, k.MoveUp, k.Top, k.Bottom,
		key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "cycle filter")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "revert commit")),
	}
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

// setStatus records the message the status line renders until the next
// key handler replaces or clears it.
func (m *Model) setStatus(kind statusKind, msg string) {
	m.statusKind = kind
	m.status = msg
}

// selectedEvent returns the event under the cursor, or false when the
// list is empty.
func (m *Model) selectedEvent() (stage.Event, bool) {
	if m.cursor < 0 || m.cursor >= len(m.events) {
		return stage.Event{}, false
	}
	return m.events[m.cursor], true
}

// handleKey dispatches one tea.KeyPressMsg. A query that has not loaded
// yet makes every key a no-op — there is nothing to navigate.
func (m *Model) handleKey(msg tea.KeyPressMsg) (ui.Pane, tea.Cmd) {
	if !m.hasLoad {
		return m, nil
	}
	k := m.deps.Keys
	switch {
	case key.Matches(msg, k.MoveDown):
		m.cursor++
		m.clampCursor()
		return m, nil
	case key.Matches(msg, k.MoveUp):
		m.cursor--
		m.clampCursor()
		return m, nil
	case key.Matches(msg, k.Top):
		m.cursor = 0
		return m, nil
	case key.Matches(msg, k.Bottom):
		m.cursor = len(m.events) - 1
		m.clampCursor()
		return m, nil
	}

	switch msg.String() {
	case "f":
		return m.cycleFilter()
	case "r":
		return m.revert()
	}
	return m, nil
}

// cycleFilter is `f` (pinned item 4): advances through the five states in
// order and re-queries the journal.
func (m *Model) cycleFilter() (ui.Pane, tea.Cmd) {
	m.filter = (m.filter + 1) % filterCount
	m.setStatus(statusNone, "")
	return m, queryCmd(m.deps.Engine, m.filter.query())
}

// revert is `r` (pinned item 5): acts only on a row whose Event.Commit is
// set. Any other row — and any Engine.Revert error — renders a one-line
// message and makes no further engine call; nothing here ever panics.
func (m *Model) revert() (ui.Pane, tea.Cmd) {
	ev, ok := m.selectedEvent()
	if !ok || ev.Commit == "" {
		m.setStatus(statusWarn, "revert refused: select a row that carries a commit id (commit_begin, commit_end or reverted)")
		return m, nil
	}
	if m.deps.Engine == nil {
		m.setStatus(statusWarn, "revert failed: no engine loaded")
		return m, nil
	}

	cs, err := m.deps.Engine.Revert(ev.Commit)
	if err != nil {
		m.setStatus(statusWarn, fmt.Sprintf("revert failed: %v", err))
		return m, nil
	}

	m.setStatus(statusGood, fmt.Sprintf("reverted %s into %s", ev.Commit, cs.ID))
	return m, tea.Batch(
		queryCmd(m.deps.Engine, m.filter.query()),
		func() tea.Msg { return ui.StageChangedMsg{ChangesetID: cs.ID, Ops: len(cs.Live())} },
		func() tea.Msg { return ui.SwitchScreenMsg{To: ui.ScreenReview} },
	)
}

// statusStyle returns the Theme style m.statusKind renders the status
// line with.
func (m *Model) statusStyle() lipgloss.Style {
	switch m.statusKind {
	case statusGood:
		return m.theme.Good
	case statusWarn:
		return m.theme.Warn
	default:
		return m.theme.Base
	}
}

// summarizeEvent renders one Event as a single line: timestamp, kind,
// actor, then whichever of Changeset/Op/Hunk/Commit/Paths/Message it
// carries (backbone §5.7's Event fields).
func summarizeEvent(ev stage.Event) string {
	actor := ev.Actor.Kind
	if ev.Actor.Model != "" {
		actor = actor + ":" + ev.Actor.Model
	}
	parts := []string{ev.TS.UTC().Format(time.RFC3339), string(ev.Kind), actor}
	if ev.Changeset != "" {
		parts = append(parts, "cs="+ev.Changeset)
	}
	if ev.Op != "" {
		parts = append(parts, "op="+ev.Op)
	}
	if ev.Hunk != "" {
		parts = append(parts, "hunk="+ev.Hunk)
	}
	if ev.Commit != "" {
		parts = append(parts, "commit="+ev.Commit)
	}
	if len(ev.Paths) > 0 {
		parts = append(parts, strings.Join(ev.Paths, ","))
	}
	if ev.Message != "" {
		parts = append(parts, ev.Message)
	}
	return strings.Join(parts, "  ")
}

// View renders the log screen at exactly w by h (backbone §12) — no line
// wider than w, never assuming 80x24, never panicking at small or large
// sizes.
func (m *Model) View(w, h int) string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	var body string
	switch {
	case m.loadErr != nil:
		body = fmt.Sprintf("log: %v", m.loadErr)
	case !m.hasLoad:
		body = "loading journal..."
	default:
		body = m.renderBody(w, h)
	}
	return strings.Join(fitLines(body, w, h), "\n")
}

// renderBody lays out the filter/count header, the scrollable event list,
// and a status line beneath when one is set.
func (m *Model) renderBody(w, h int) string {
	statusH := 0
	if m.status != "" {
		statusH = 1
	}
	listH := h - 1 - statusH
	if listH < 0 {
		listH = 0
	}

	header := m.theme.Title.Render(fitLine(fmt.Sprintf(
		"filter: %s (%d event(s)) — f cycles, r reverts a commit row",
		m.filter.label(), len(m.events)), w))

	var visible []string
	if len(m.events) == 0 {
		visible = fitLines(m.theme.Muted.Render("no matching events"), w, listH)
	} else {
		lines := make([]string, len(m.events))
		for i, ev := range m.events {
			style := m.theme.Base
			if i == m.cursor {
				style = m.theme.Selected
			}
			lines[i] = style.Render(fitLine(summarizeEvent(ev), w))
		}
		visible = fitLines(strings.Join(scrollWindow(lines, m.cursor, listH), "\n"), w, listH)
	}

	rows := make([]string, 0, 2+listH)
	rows = append(rows, header)
	rows = append(rows, visible...)
	if statusH > 0 {
		rows = append(rows, m.statusStyle().Render(fitLine(m.status, w)))
	}
	return strings.Join(rows, "\n")
}

// fitLine clips s to at most w display columns, ANSI-aware via lipgloss,
// and pads it with spaces up to exactly w when it is shorter.
func fitLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	if cur := lipgloss.Width(s); cur < w {
		s += strings.Repeat(" ", w-cur)
	}
	return s
}

// fitLines splits s on "\n" and returns exactly n lines, each fitLine'd to
// w: lines beyond n are dropped, missing ones come back blank padding.
func fitLines(s string, w, n int) []string {
	if n < 0 {
		n = 0
	}
	src := strings.Split(s, "\n")
	out := make([]string, n)
	for i := range out {
		var line string
		if i < len(src) {
			line = src[i]
		}
		out[i] = fitLine(line, w)
	}
	return out
}

// scrollWindow returns at most h consecutive lines from lines, positioned
// so index cursor is inside the window whenever the full list is longer
// than h.
func scrollWindow(lines []string, cursor, h int) []string {
	if h <= 0 || len(lines) == 0 {
		return nil
	}
	start := 0
	if cursor >= h {
		start = cursor - h + 1
	}
	if max := len(lines) - h; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}
	end := start + h
	if end > len(lines) {
		end = len(lines)
	}
	return lines[start:end]
}

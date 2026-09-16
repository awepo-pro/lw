// keys.go is the log screen's key handling: movement through d.Keys, `f`
// cycling the five filters (pinned item 4), and `r` reverting a
// commit-bearing event into a new reviewable changeset (backbone §5.8,
// pinned item 5). Refusals and failures surface through StatusReporter —
// the shell's footer shows them; the pane draws no status line of its own.
package logview

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

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
	m.clearStatus()
	return m, queryCmd(m.deps.Engine, m.filter.query())
}

// revert is `r` (pinned item 5): acts only on a row whose Event.Commit is
// set. Any other row — and any Engine.Revert error — renders a one-line
// message through StatusReporter and makes no further engine call; nothing
// here ever panics.
func (m *Model) revert() (ui.Pane, tea.Cmd) {
	ev, ok := m.selectedEvent()
	if !ok || ev.Commit == "" {
		m.setStatus(ui.StatusWarn,
			"revert refused: select a row that carries a commit id (commit_begin, commit_end or reverted)")
		return m, nil
	}
	if m.deps.Engine == nil {
		m.setStatus(ui.StatusWarn, "revert failed: no engine loaded")
		return m, nil
	}

	cs, err := m.deps.Engine.Revert(ev.Commit)
	if err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("revert failed: %v", err))
		return m, nil
	}

	m.setStatus(ui.StatusGood, fmt.Sprintf("reverted %s into %s", ev.Commit, cs.ID))
	return m, tea.Batch(
		queryCmd(m.deps.Engine, m.filter.query()),
		func() tea.Msg { return ui.StageChangedMsg{ChangesetID: cs.ID, Ops: len(cs.Live())} },
		func() tea.Msg { return ui.SwitchScreenMsg{To: ui.ScreenReview} },
	)
}

// selectedEvent returns the event under the cursor, or false when the
// list is empty.
func (m *Model) selectedEvent() (stage.Event, bool) {
	if m.cursor < 0 || m.cursor >= len(m.events) {
		return stage.Event{}, false
	}
	return m.events[m.cursor], true
}

// setStatus records the transient message the shell's footer shows until
// the next key handler replaces or clears it.
func (m *Model) setStatus(level ui.StatusLevel, msg string) {
	m.level = level
	m.status = msg
}

// clearStatus empties the transient message, handing the footer back to
// the bindings list.
func (m *Model) clearStatus() {
	m.setStatus(ui.StatusInfo, "")
}

// state.go is the ask screen's scrollback state machine: how the six
// agent.Event kinds (backbone §9) fold into m.entries, and how the ↑/↓
// selection and the enter toggle move over the tool-call entries. The
// rendering of these entries is view.go; the pump that delivers the events
// is stream.go.
package ask

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// entryKind selects which of the ask screen's five scrollback shapes an
// entry renders as.
type entryKind int

const (
	kindUser entryKind = iota
	kindAssistant
	kindTool
	kindStatus // DoneEv: a one-line turn-boundary marker
	kindError  // ErrorEv: the same marker, styled as an error (S5-T5)
)

// toolCall is the render state of one ToolCallEv, updated in place by the
// matching ToolResEv — backbone §9 gives both the same ID. It renders as
// one collapsible line regardless of how many of its fields are filled in
// yet.
type toolCall struct {
	name     string
	args     string
	expanded bool

	resolved bool // a ToolResEv for this ID has arrived
	content  string
	isError  bool
}

// entry is one block of the ask screen's scrollback, in arrival order: a
// question, a tool call, (a run of deltas folding into) one assistant
// message, or a turn boundary.
type entry struct {
	kind entryKind
	text string    // kindUser, kindAssistant, kindStatus, kindError
	tool *toolCall // kindTool only
}

// applyEvent folds one agent.Event into m's scrollback (backbone §9's six
// event types) and returns the extra tea.Cmd a StageEv needs to notify the
// shell — nil for every other kind. It never panics, including on
// ErrorEv{Err: nil}. The fold runs through mutateEntries, so an event that
// lands while the pane is scrolled up appends its lines below the visible
// window instead of moving it (scroll.go, W5 F2/C36).
func (m *Model) applyEvent(ev agent.Event) tea.Cmd {
	var cmd tea.Cmd
	m.mutateEntries(func() {
		switch e := ev.(type) {
		case agent.TextDelta:
			m.appendAssistantText(e.Text)
		case agent.ToolCallEv:
			m.startToolCall(e)
		case agent.ToolResEv:
			m.resolveToolCall(e)
		case agent.StageEv:
			cmd = stageChangedCmd(e)
		case agent.DoneEv:
			// The frozen conversation grids render the turn boundary as
			// `done · N rounds` (s2-screens.md T08, from DoneEv.Rounds); the
			// stop reason stays in the session transcript.
			m.endTurn(fmt.Sprintf("done · %d rounds", e.Rounds))
		case agent.ErrorEv:
			msg := "unknown error"
			if e.Err != nil {
				msg = e.Err.Error()
			}
			m.endTurnError(msg)
		}
	})
	return cmd
}

// stageChangedCmd reports e as a ui.StageChangedMsg (s4-tui.md S4-T6
// pinned item 3), so the shell's header — and any other pane active when
// it arrives — stays live.
func stageChangedCmd(e agent.StageEv) tea.Cmd {
	return func() tea.Msg {
		return ui.StageChangedMsg{ChangesetID: e.ChangesetID, Ops: e.Ops}
	}
}

// appendAssistantText folds a TextDelta into the scrollback: consecutive
// deltas within one turn accumulate onto the same kindAssistant entry, in
// arrival order, so streamed prose renders as one growing message rather
// than one line per delta.
func (m *Model) appendAssistantText(text string) {
	if n := len(m.entries); n > 0 && m.turnActive && m.entries[n-1].kind == kindAssistant {
		m.entries[n-1].text += text
	} else {
		m.entries = append(m.entries, entry{kind: kindAssistant, text: text})
	}
	m.turnActive = true
}

// startToolCall appends a new collapsible tool-call line and remembers its
// entry index by ID, so the matching ToolResEv updates this line in place
// instead of appending a second one.
//
// It does not move the ↑/↓ selection. The frozen conversation grids show a
// finished turn with no cursor gutter anywhere (ask-conversation-*), so a
// selection exists only where the curator put it; the gutter would also
// jump around the transcript while calls stream in.
func (m *Model) startToolCall(e agent.ToolCallEv) {
	m.entries = append(m.entries, entry{kind: kindTool, tool: &toolCall{name: e.Name, args: e.Args}})
	if m.toolIndex == nil {
		m.toolIndex = map[string]int{}
	}
	m.toolIndex[e.ID] = len(m.entries) - 1
	m.turnActive = true
}

// resolveToolCall attaches a ToolResEv's result to the line its ID's
// ToolCallEv already rendered. A result with no matching call — a
// malformed or truncated fake stream, never the real Loop (backbone §9)
// — still renders as its own line instead of being silently dropped.
// Like startToolCall, it leaves the ↑/↓ selection alone.
func (m *Model) resolveToolCall(e agent.ToolResEv) {
	idx, ok := m.toolIndex[e.ID]
	if !ok || idx < 0 || idx >= len(m.entries) || m.entries[idx].tool == nil {
		m.entries = append(m.entries, entry{kind: kindTool, tool: &toolCall{
			name: e.Name, resolved: true, content: e.Content, isError: e.IsError,
		}})
		m.turnActive = true
		return
	}
	tc := m.entries[idx].tool
	tc.resolved = true
	tc.content = e.Content
	tc.isError = e.IsError
	m.turnActive = true
}

// endTurn appends a kindStatus line and closes the turn: the next
// TextDelta starts a new assistant entry rather than gluing onto whatever
// this turn's last one held (backbone §9: DoneEv and ErrorEv are the two,
// mutually exclusive, terminal events).
//
// The selection clears with the turn: the frozen ask-conversation grids
// show no cursor gutter on a finished transcript, and a gutter left
// pointing at yesterday's tool call is not a selection the curator made.
func (m *Model) endTurn(status string) {
	m.entries = append(m.entries, entry{kind: kindStatus, text: status})
	m.turnActive = false
	m.selected = -1
}

// endTurnError is endTurn's ErrorEv twin (backbone §9's other terminal
// event): the same turn boundary, but the entry renders as an error —
// theme.Bad, not the muted status grey — because a turn that died is the
// one thing in the scrollback a curator must not scroll past (s5-agent-
// loop.md S5-T5: "render ErrorEv visibly rather than panicking").
func (m *Model) endTurnError(msg string) {
	m.entries = append(m.entries, entry{kind: kindError, text: msg})
	m.turnActive = false
	m.selected = -1
}

// toolEntryIndexes returns, in ascending order, the indexes of every
// kindTool entry in m.entries — the set moveSelection cycles over.
func (m *Model) toolEntryIndexes() []int {
	var idxs []int
	for i, e := range m.entries {
		if e.kind == kindTool {
			idxs = append(idxs, i)
		}
	}
	return idxs
}

// moveSelection moves m.selected by delta among the tool-call entries only
// — there is nothing to select on any other kind — clamped to the
// first/last one.
func (m *Model) moveSelection(delta int) {
	idxs := m.toolEntryIndexes()
	if len(idxs) == 0 {
		return
	}
	pos := -1
	for i, idx := range idxs {
		if idx == m.selected {
			pos = i
			break
		}
	}
	switch {
	case pos < 0:
		if delta > 0 {
			pos = 0
		} else {
			pos = len(idxs) - 1
		}
	default:
		pos += delta
		if pos < 0 {
			pos = 0
		}
		if pos >= len(idxs) {
			pos = len(idxs) - 1
		}
	}
	m.selected = idxs[pos]
}

// toggleSelectedExpand flips the expanded flag of the currently selected
// tool-call entry, if any (s4-tui.md S4-T6: "expandable with enter").
func (m *Model) toggleSelectedExpand() {
	if m.selected < 0 || m.selected >= len(m.entries) {
		return
	}
	e := &m.entries[m.selected]
	if e.kind != kindTool || e.tool == nil {
		return
	}
	e.tool.expanded = !e.tool.expanded
}

// rearm returns a tea.Cmd that resumes Listen on the channel most recently
// handed to this pane, or nil once StreamClosedMsg has cleared it.
func (m *Model) rearm() tea.Cmd {
	if m.ch == nil {
		return nil
	}
	return Listen(m.ch)
}

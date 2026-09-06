// stream.go implements the ask screen's agent.Event pump (backbone §9,
// §12; s4-tui.md S4-T6 pinned item 2): the exported StreamMsg/Listen/
// EventMsg/StreamClosedMsg seam a caller drives with nothing but a
// ui.Pane and a <-chan agent.Event, and the unexported scrollback state
// each event kind folds into. The pane is the only thing that ever reads
// from that channel — Update re-arms Listen after every event (ask.go),
// the ordinary Bubble Tea pump pattern, rather than a goroutine draining
// the channel on its own.
package ask

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// StreamMsg hands the pane the channel to consume for one turn (repair-1,
// S4-T6: a caller holding only the ui.Pane New returns has no way to reach
// the unexported field Listen's re-arm depends on, so the exported
// injection point must be a message, not just a function). Update installs
// msg.Ch as the pane's active stream and immediately arms Listen on it —
// the same call an external caller could have made itself, except now the
// pane keeps making it after every event, which is the whole point:
// nothing outside this package ever reads from the channel directly.
type StreamMsg struct{ Ch <-chan agent.Event }

// EventMsg carries one agent.Event pulled off the channel Listen is
// reading (s4-tui.md S4-T6 pinned item 2). Update applies it to the
// scrollback and re-arms Listen for the next one.
type EventMsg struct{ Ev agent.Event }

// StreamClosedMsg reports that the channel Listen was reading has closed
// — the turn's event source is gone, so Update stops re-arming.
type StreamClosedMsg struct{}

// Listen returns a tea.Cmd that reads exactly one event off ch, delivering
// it as an EventMsg, or StreamClosedMsg once ch is drained and closed. A
// caller that only holds a ui.Pane cannot use Listen's result to sustain a
// stream by itself — Update's re-arm needs the channel again after every
// event, and there is no concrete method to hand it one directly. StreamMsg
// is what closes that gap: send `ask.StreamMsg{Ch: ch}` through Update
// (exactly as the Bubble Tea runtime would deliver any other tea.Msg) and
// the pane arms and re-arms Listen(ch) on its own from then on. The
// scripted fake this subtask ships is just a channel handed to StreamMsg
// the same way S5-T5's real Agent.Send channel will be.
func Listen(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return StreamClosedMsg{}
		}
		return EventMsg{Ev: ev}
	}
}

// entryKind selects which of the ask screen's four scrollback shapes an
// entry renders as.
type entryKind int

const (
	kindUser entryKind = iota
	kindAssistant
	kindTool
	kindStatus // DoneEv / ErrorEv: a one-line turn-boundary marker
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

// entry is one line (or, expanded, one block) of the ask screen's
// scrollback, in arrival order.
type entry struct {
	kind entryKind
	text string    // kindUser, kindAssistant, kindStatus
	tool *toolCall // kindTool only
}

// applyEvent folds one agent.Event into m's scrollback (backbone §9's six
// event types) and returns the extra tea.Cmd a StageEv needs to notify the
// shell — nil for every other kind. It never panics, including on
// ErrorEv{Err: nil}.
func (m *Model) applyEvent(ev agent.Event) tea.Cmd {
	switch e := ev.(type) {
	case agent.TextDelta:
		m.appendAssistantText(e.Text)
	case agent.ToolCallEv:
		m.startToolCall(e)
	case agent.ToolResEv:
		m.resolveToolCall(e)
	case agent.StageEv:
		return stageChangedCmd(e)
	case agent.DoneEv:
		m.endTurn(fmt.Sprintf("done: %s (%d round(s))", e.Reason, e.Rounds))
	case agent.ErrorEv:
		msg := "unknown error"
		if e.Err != nil {
			msg = e.Err.Error()
		}
		m.endTurn("error: " + msg)
	}
	return nil
}

// stageChangedCmd reports e as a ui.StageChangedMsg (s4-tui.md S4-T6
// pinned item 3), so the shell's STAGE badge — and any other pane active
// when it arrives — stays live.
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
// instead of appending a second one. It becomes the selected entry, so
// enter expands the tool call a human is watching stream in without an
// extra keypress to find it.
func (m *Model) startToolCall(e agent.ToolCallEv) {
	m.entries = append(m.entries, entry{kind: kindTool, tool: &toolCall{name: e.Name, args: e.Args}})
	if m.toolIndex == nil {
		m.toolIndex = map[string]int{}
	}
	m.toolIndex[e.ID] = len(m.entries) - 1
	m.selected = len(m.entries) - 1
	m.turnActive = true
}

// resolveToolCall attaches a ToolResEv's result to the line its ID's
// ToolCallEv already rendered. A result with no matching call — a
// malformed or truncated fake stream, never the real Loop (backbone §9)
// — still renders as its own line instead of being silently dropped.
func (m *Model) resolveToolCall(e agent.ToolResEv) {
	idx, ok := m.toolIndex[e.ID]
	if !ok || idx < 0 || idx >= len(m.entries) || m.entries[idx].tool == nil {
		m.entries = append(m.entries, entry{kind: kindTool, tool: &toolCall{
			name: e.Name, resolved: true, content: e.Content, isError: e.IsError,
		}})
		m.selected = len(m.entries) - 1
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
func (m *Model) endTurn(status string) {
	m.entries = append(m.entries, entry{kind: kindStatus, text: status})
	m.turnActive = false
}

// rearm returns a tea.Cmd that resumes Listen on the channel most recently
// handed to this pane, or nil once StreamClosedMsg has cleared it.
func (m *Model) rearm() tea.Cmd {
	if m.ch == nil {
		return nil
	}
	return Listen(m.ch)
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
// — there is nothing to expand on any other kind — clamped to the
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

// singleLine collapses text to one line for a collapsed tool line's
// preview — internal newlines would otherwise break the "one collapsible
// line" contract (s4-tui.md S4-T6).
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes returns s if it is at most max runes, otherwise its first
// max-1 runes plus a single "…" — the same elision convention backbone
// §3's index.Hit.Snippet uses.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// state.go is the ask screen's scrollback state machine: how the agent
// event kinds (backbone §9) fold into m.entries — including 022 T2's
// ReasoningDelta, which accumulates pane-only thinking state instead of an
// entry — and how the ↑/↓ selection and the enter toggle move over the
// tool-call entries. The rendering of these entries is view.go; the pump
// that delivers the events is stream.go.
package ask

import (
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

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
			m.roundSawText = true
			m.appendAssistantText(e.Text)
		case agent.ReasoningDelta:
			// 022 T2: pane-only accumulation — a per-turn rune count and an
			// 8-KiB rolling tail for the ctrl+t view. Nothing enters the
			// scrollback here; the thinking shows as the status line below.
			m.reasonChars += utf8.RuneCountInString(e.Text)
			m.reasonTail = appendReasoningTail(m.reasonTail, e.Text)
			m.roundSawReasoning = true
		case agent.ToolCallEv:
			m.resetReasoningRound() // a tool round starts: the thinking line hides until reasoning resumes
			m.roundToolInFlight = true
			m.startToolCall(e)
		case agent.ToolResEv:
			m.resetReasoningRound()
			m.roundToolInFlight = false
			m.resolveToolCall(e)
		case agent.StageEv:
			// Only the pane's own auto-reject streams an empty StageEv while
			// its turn is active (stream.go forwardTurn) — the one turn that
			// owes the `nothing staged` hint. The id is captured now because
			// the StageChangedMsg this command produces comes back through
			// changesetGone, which clears m.sessionID on its way; the hint
			// itself lands after the turn's terminal line (title.go
			// appendKeptHint).
			if e.ChangesetID == "" && m.turnActive {
				m.hintAfterTurn = m.sessionID
			}
			cmd = stageChangedCmd(e)
		case agent.DoneEv:
			// The frozen conversation grids render a clean stop's turn
			// boundary as `done · N rounds` (s2-screens.md T08, from
			// DoneEv.Rounds). A turn the loop cut off at its round limit is
			// not a clean stop: its boundary says so (008 contract §5). The
			// scrollback line is the only place the reason is surfaced.
			if e.Reason == "max_rounds" {
				m.endTurn(fmt.Sprintf("stopped: round limit · %d rounds", e.Rounds))
				// 009 §3.2: a max_rounds turn leaves nothing fileable.
				m.forgetLastAnswer()
			} else {
				m.endTurn(fmt.Sprintf("done · %d rounds", e.Rounds))
				// 009 §3.2: record what a clean turn left behind, so ctrl+s
				// can file it (file.go).
				m.recordLastAnswer()
			}
			// A-806: this turn ended cleanly, so if it is the one
			// hintAfterTurn names — self-opened, staged nothing, auto-
			// rejected — its kept state word displays as `answered`.
			// Captured before appendKeptHint consumes the hint; an ErrorEv
			// turn reaches appendKeptHint without this, and stays
			// `rejected`.
			m.answeredID = m.hintAfterTurn
			m.appendKeptHint()
			// 009 §3.2: the file hint lands after the terminal line and the
			// kept hint, and only when the answer is fileable.
			m.appendFileHint()
		case agent.ErrorEv:
			msg := "unknown error"
			if e.Err != nil {
				msg = e.Err.Error()
			}
			// A truncated turn (008 contract §5, agent.ErrTruncated)
			// replaces the provider's wrapped detail with the one sentence
			// a curator can act on: the output cap ate the turn, nothing
			// after it was proposed, and llm.max_tokens is the knob.
			if errors.Is(e.Err, agent.ErrTruncated) {
				msg = "stopped: output limit reached — nothing after this was proposed; raise llm.max_tokens"
			}
			// Before endTurnError: it drops the filing marker (C-907), and
			// forgetLastAnswer must still see it to keep a filing turn's
			// pair (C-908). An ordinary errored turn leaves nothing
			// fileable (009 §3.2).
			m.forgetLastAnswer()
			m.endTurnError(msg)
			m.appendKeptHint()
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

// echoUser appends text as a kindUser scrollback entry and clears the input
// box — the submit path's one shared "the curator said this" step.
func (m *Model) echoUser(text string) {
	m.entries = append(m.entries, entry{kind: kindUser, text: text})
	m.input = ""
}

// appendStatus appends one kindStatus line: a pane-local notice (a refused
// submit, a failed Close) rather than a turn boundary, which endTurn and
// endTurnError own. Through mutateEntries, the notice lands below a
// scrolled-up window instead of moving it.
func (m *Model) appendStatus(text string) {
	m.mutateEntries(func() {
		m.entries = append(m.entries, entry{kind: kindStatus, text: text})
	})
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
	m.turnErrored = false   // a clean stop is the mascot's reset (016 F.M4)
	m.resetReasoningRound() // the turn ended: its thinking line goes with it
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
	m.turnErrored = true    // the last turn died: the mascot reverses until a clean stop
	m.resetReasoningRound() // the turn ended: its thinking line goes with it
	// A turn that ends in error can never reach recordLastAnswer, so its
	// filing marker must not outlive it (009 §3.4): without this, a filing
	// turn that failed before its stream existed (turnStartedMsg's error —
	// no StreamClosedMsg ever comes) would flag the NEXT ordinary turn's
	// answer as a filing turn's, unfileable. The recorded pair itself
	// stays: only an ErrorEv or max_rounds clears it (009 §3.2), and
	// forgetLastAnswer has already run on the ErrorEv path above.
	m.filingTurn = false
}

// EngineBusy implements ui.EngineUser (008 contract §8, A-801): true from
// the moment a turn starts — submitInput sets turnActive — until its
// terminal event has been processed by Update: DoneEv and ErrorEv through
// endTurn/endTurnError, a cancelled or closed-off stream through
// StreamClosedMsg. turnActive is the lifetime this file's state machine
// already tracks, and it is exactly the window in which the turn goroutine
// is calling the engine (session resolution, tool handlers reading the
// vault), the window the shell's reload tick must not reload under (G5
// review I-1).
func (m *Model) EngineBusy() bool { return m.turnActive }

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
// tool-call entry, if any (s4-tui.md S4-T6: "expandable with enter"). The
// flip runs through mutateEntries (W5d/T34): expanding or collapsing
// changes the rendered line count, and the accounting — not the render-side
// clamp — decides what the window does about it. Without it, expanding a
// call on screen pushed the window's top rows out of view.
func (m *Model) toggleSelectedExpand() {
	if m.selected < 0 || m.selected >= len(m.entries) {
		return
	}
	e := &m.entries[m.selected]
	if e.kind != kindTool || e.tool == nil {
		return
	}
	m.mutateEntries(func() { e.tool.expanded = !e.tool.expanded })
}

// rearm returns a tea.Cmd that resumes Listen on the channel most recently
// handed to this pane, or nil once StreamClosedMsg has cleared it.
func (m *Model) rearm() tea.Cmd {
	if m.ch == nil {
		return nil
	}
	return Listen(m.ch)
}

// reasoningTailCap is the byte cap of the rolling reasoning tail kept for
// the ctrl+t view — the last 8 KiB of the current turn's thinking (022 T2).
const reasoningTailCap = 8 * 1024

// reasoningViewMaxLines bounds the ctrl+t view: at most the tail's last
// twelve wrapped lines render, whatever the turn is still thinking.
const reasoningViewMaxLines = 12

// resetReasoningTurn clears the per-turn reasoning state — the char count
// and the tail buffer — when a new turn starts (beginTurn). Round flags
// clear with it; they are per-round state inside the turn.
func (m *Model) resetReasoningTurn() {
	m.reasonChars = 0
	m.reasonTail = ""
	m.resetReasoningRound()
}

// resetReasoningRound clears the round flags behind the thinking status
// line: a new tool round (ToolCallEv/ToolResEv), a turn's end, or a stream
// cut off mid-round all start from "no reasoning, no text in this round",
// and no tool of the previous round is in flight any more. The turn count
// and tail are deliberately left alone.
func (m *Model) resetReasoningRound() {
	m.roundSawReasoning = false
	m.roundSawText = false
	m.roundToolInFlight = false
}

// thinkingVisible reports whether the `· thinking…` status line shows: the
// turn is running, its CURRENT round has received at least one
// ReasoningDelta and no TextDelta yet — the line hides the moment text
// streams and stays hidden between tool rounds until reasoning resumes.
func (m *Model) thinkingVisible() bool {
	return m.turnActive && m.roundSawReasoning && !m.roundSawText
}

// sendingStatusLine is the waiting status line's exact text (025 F.W4):
// U+00B7, space, "sending", U+2026. It holds the window the thinking line
// cannot — everything between Enter and the provider's first response
// byte, before any ReasoningDelta has ever arrived this round.
const sendingStatusLine = "· sending…"

// waitingVisible reports whether the pane is in 025's cold-start window:
// the turn is running and this round has seen nothing yet — no reasoning,
// no text, no tool executing. That is the stretch between Enter and the
// provider's first response byte (measured median 5.80s, recurring before
// every round), where the pane used to render idle. Inside a no-text
// round exactly one of three things holds — reasoning seen (the thinking
// line), nothing seen (this window), a tool in flight (deliberately
// still) — so waitingVisible and thinkingVisible are mutually exclusive
// and the first ReasoningDelta replaces the waiting row with the thinking
// one. The tool leg is deliberately excluded — tool-round motion is
// workflow 024's question, not this one.
func (m *Model) waitingVisible() bool {
	return m.turnActive && !m.roundSawReasoning && !m.roundSawText && !m.roundToolInFlight
}

// thinkingStatusLine is the status line's exact text (022 T2):
// `· thinking… (1.2k chars)` — the count as `%.1fk` from 1000 up, plain
// below it.
func (m *Model) thinkingStatusLine() string {
	return "· thinking… (" + formatThinkingCount(m.reasonChars) + " chars)"
}

// formatThinkingCount renders a char count for the thinking line: `%.1fk`
// at 1000 and above (1234 → `1.2k`), the plain integer below (950 → `950`).
func formatThinkingCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return strconv.Itoa(n)
}

// appendReasoningTail appends text to a rolling tail buffer and trims it
// back under the cap, whole runes from the front, so the ctrl+t view holds
// the RECENT thinking no matter how long the turn thinks.
func appendReasoningTail(tail, text string) string {
	tail += text
	if len(tail) <= reasoningTailCap {
		return tail
	}
	drop := len(tail) - reasoningTailCap
	i := 0
	for drop > 0 && i < len(tail) {
		_, sz := utf8.DecodeRuneInString(tail[i:])
		i += sz
		drop -= sz
	}
	return tail[i:]
}

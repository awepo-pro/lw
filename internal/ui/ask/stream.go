// stream.go implements the ask screen's agent.Event pump (backbone §9,
// §12; s4-tui.md S4-T6 pinned item 2) and, since S5-T5, the turn start
// that feeds it: the exported StreamMsg/Listen/EventMsg/StreamClosedMsg
// seam a caller drives with nothing but a ui.Pane and a <-chan
// agent.Event, and the unexported scrollback state each event kind folds
// into. The pane is the only thing that ever reads from that channel —
// Update re-arms Listen after every event (ask.go), the ordinary Bubble
// Tea pump pattern, rather than a goroutine draining the channel on its
// own. Submitting a question starts a real agent turn (backbone §9,
// C-105: the pane makes the channel, the pane's goroutine runs Send) and
// hands that channel to the pump through the very same StreamMsg.
//
// Since C-124/D-DH (S6), a turn that started with no changeset open
// resolves and opens one itself (runTurn, resolveTurnChangeset below)
// rather than the pane refusing at submit; forwardTurn rejects that
// changeset afterwards if the turn staged nothing.
package ask

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// The pump's three message types are declared in internal/ui/pane.go and
// aliased here (D-DA's move, S6 wrap-up). They used to live in this package,
// but the shell has to route them by name — an ask pane left off screen
// mid-turn must keep draining its stream, and a shell that cannot name a
// type cannot route it except by broadcasting everything, which is the
// fail-open fan-out D-DA accepted only as a stopgap. Type aliases keep this
// package's own API, tests and callers exactly as they were: ask.StreamMsg
// and ui.StreamMsg are the same type, and neither the pane's Update nor an
// external caller's `ask.StreamMsg{Ch: ch}` can tell the move happened.
//
// StreamMsg hands the pane the channel to consume for one turn (repair-1,
// S4-T6: a caller holding only the ui.Pane New returns has no way to reach
// the unexported field Listen's re-arm depends on, so the exported
// injection point must be a message, not just a function). Update installs
// msg.Ch as the pane's active stream and immediately arms Listen on it —
// the same call an external caller could have made itself, except now the
// pane keeps making it after every event, which is the whole point:
// nothing outside this package ever reads from the channel directly.
type StreamMsg = ui.StreamMsg

// EventMsg carries one agent.Event pulled off the channel Listen is
// reading (s4-tui.md S4-T6 pinned item 2). Update applies it to the
// scrollback and re-arms Listen for the next one.
type EventMsg = ui.EventMsg

// StreamClosedMsg reports that the channel Listen was reading has closed
// — the turn's event source is gone, so Update stops re-arming.
type StreamClosedMsg = ui.StreamClosedMsg

// sessionClosedMsg reports the outcome of archiving the session a turn ran
// under, after the changeset it belonged to was committed or rejected
// (backbone §9, C-102). Unexported: only this pane produces and consumes
// it, and the pane has no other way to surface a Close failure than its
// own scrollback.
type sessionClosedMsg struct{ err error }

// Listen returns a tea.Cmd that reads exactly one event off ch, delivering
// it as an EventMsg, or StreamClosedMsg once ch is drained and closed. A
// caller that only holds a ui.Pane cannot use Listen's result to sustain a
// stream by itself — Update's re-arm needs the channel again after every
// event, and there is no concrete method to hand it one directly. StreamMsg
// is what closes that gap: send `ask.StreamMsg{Ch: ch}` through Update
// (exactly as the Bubble Tea runtime would deliver any other tea.Msg) and
// the pane arms and re-arms Listen(ch) on its own from then on — which is
// how startTurn installs the channel it hands Agent.Send, and how the
// scripted tests hand it a fake one.
func Listen(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return StreamClosedMsg{}
		}
		return EventMsg{Ev: ev}
	}
}

// turnEventBuffer is the capacity of the channel a turn streams through.
// One event per streamed delta and tool call adds up faster than the pump
// drains them frame by frame, and a full channel would stall Agent.Send —
// backbone §9's C-105 guard makes that non-fatal (it declines on ctx.Done)
// but it would still stall the turn. 64 events is far more than one frame
// renders and a few orders of magnitude below anything memory-relevant.
const turnEventBuffer = 64

// turnStartedMsg reports the changeset id a turn resolved to run under —
// C-124/D-DH: opened fresh by this turn, reused from what was already open
// at submit, or a failure that ends the turn before Agent.Send ever runs —
// plus, on success, the channel to install as the pane's active stream. It
// is delivered before any agent.Event so the pane's own bookkeeping
// (sessionID, ctrl+r → Review, commit/close handling) is never behind the
// turn it tracks.
//
// Unexported on purpose: the shell already routes any message a pane's own
// tea.Cmd produces back to that same pane, on screen or not
// (internal/ui/app.go's producedBy/deliverTo) — nothing needs adding to
// pane.go's named fan-out set for a message only this package ever sees.
type turnStartedMsg struct {
	sessionID string
	ch        <-chan agent.Event
	err       error
}

// startTurn launches one agent turn (backbone §9, C-105). sessionID is the
// changeset id already known at submit time, or "" when none was open —
// C-124/D-DH: runTurn resolves it, opening one itself when it must, because
// that is filesystem and lint work that has to stay off Update. Every value
// the goroutine needs is passed in, never read off m: Update owns the
// model, and a goroutine writing to it would be a data race.
//
// ctx is cancellable and remembered on m, so the pane can abandon a turn
// whose changeset was committed or rejected underneath it (see
// changesetGone); Send closes its channel promptly on cancellation and
// delivers no terminal event of its own (backbone §9, C-105).
func (m *Model) startTurn(sessionID, msg string) tea.Cmd {
	ag := m.deps.Agent
	e := m.deps.Engine
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	started := make(chan turnStartedMsg, 1)
	out := make(chan agent.Event, turnEventBuffer)
	go runTurn(ctx, ag, e, sessionID, msg, started, out)

	return func() tea.Msg { return <-started }
}

// runTurn is startTurn's goroutine body (backbone §9, C-105; C-124/D-DH).
// It resolves the changeset this turn runs in — reusing sessionID when one
// was already open at submit, or opening a fresh one itself via
// resolveTurnChangeset when nothing was — reports that resolution (or a
// failure) back through started before Agent.Send ever runs, then relays
// Send's events through forwardTurn, which is also what rejects a
// self-opened changeset that ends up with nothing staged. runTurn never
// touches Update or m: everything it needs is a parameter, and everything
// it produces goes out through started or out.
func runTurn(ctx context.Context, ag agent.Agent, e *stage.Engine, sessionID, msg string, started chan<- turnStartedMsg, out chan agent.Event) {
	fail := func(err error) {
		started <- turnStartedMsg{err: err}
		close(started)
		close(out)
	}

	openedHere := false
	if sessionID != "" {
		// A changeset was already open when the pane checked (backbone §9,
		// C-102 — a session is keyed by its changeset). Re-read it: the
		// window between that keypress and this goroutine running is where
		// C-118's race lives, and creating a session for a changeset that
		// just vanished would fabricate an empty directory the engine could
		// never clear on its own.
		if e != nil {
			cs, err := e.Current()
			if err != nil {
				fail(fmt.Errorf("ask: the changeset this turn runs in is gone: %w", err))
				return
			}
			if cs.ID != sessionID {
				fail(fmt.Errorf("ask: the open changeset changed under the turn: %s is open, not %s", cs.ID, sessionID))
				return
			}
		}
	} else {
		// C-124/D-DH: nothing was open, so this turn opens its own
		// changeset — the same seam `lw ingest` already uses — rather than
		// refusing. intent is the question itself, so `lw log`/`lw status`
		// show what prompted it.
		id, opened, err := resolveTurnChangeset(e, msg)
		if err != nil {
			fail(err)
			return
		}
		sessionID, openedHere = id, opened
	}

	ss := ag.Sessions()
	if ss == nil {
		fail(errors.New("ask: the agent has no session store"))
		return
	}

	if openedHere {
		// Created immediately after OpenChangeset returns, with no other
		// engine call in between — the "session created WITH the
		// changeset" seam C-118 asked for, closing that race for this path.
		if _, err := ss.Create(sessionID); err != nil {
			fail(fmt.Errorf("ask: open a session for changeset %s: %w", sessionID, err))
			return
		}
	} else if err := ensureSession(ss, sessionID); err != nil {
		fail(err)
		return
	}

	started <- turnStartedMsg{sessionID: sessionID, ch: out}
	close(started)

	sendCh := make(chan agent.Event, turnEventBuffer)
	go func() {
		// The returned error is deliberately dropped: when Send fails it has
		// already delivered that same error as the turn's ErrorEv (backbone
		// §9, C-105), and forwardTurn relays events, not return values.
		_ = ag.Send(ctx, sessionID, msg, sendCh)
	}()

	forwardTurn(ctx, e, sessionID, openedHere, sendCh, out)
}

// resolveTurnChangeset opens a changeset for a turn that started with none
// open (C-124/D-DH), or falls back to whatever is open if something raced
// ahead of it. It never returns an empty id without a non-nil err.
func resolveTurnChangeset(e *stage.Engine, msg string) (id string, openedHere bool, err error) {
	if e == nil {
		return "", false, errors.New("ask: no engine to open a changeset in")
	}
	cs, err := e.OpenChangeset(askIntent(msg), stage.Author{Kind: "agent"})
	if err == nil {
		return cs.ID, true, nil
	}
	if !errors.Is(err, stage.ErrOpenChangeset) {
		return "", false, fmt.Errorf("ask: open a changeset for this turn: %w", err)
	}
	// Something opened one between the pane's read and this goroutine
	// running: fall back to it, exactly as a turn that found one open at
	// submit would.
	cur, curErr := e.Current()
	if curErr != nil {
		return "", false, fmt.Errorf("ask: no changeset open to run this turn in: %w", curErr)
	}
	return cur.ID, false, nil
}

// askIntentRunes bounds the intent D-DH stamps on a changeset this pane
// opens for itself, so `lw log`/`lw status` show a readable one-liner
// instead of a raw, possibly multi-line question.
const askIntentRunes = 60

// askIntent turns msg into that intent: "ask: " plus the question collapsed
// to one line and truncated to askIntentRunes runes.
func askIntent(msg string) string {
	return "ask: " + truncateRunes(singleLine(msg), askIntentRunes)
}

// forwardTurn relays sendCh — the channel Agent.Send actually writes into —
// onto out, the channel this pane's pump reads (backbone §9, C-105: Send
// closes sendCh on every exit path, so the range below terminates on its
// own). It holds the turn's one terminal event back until a turn that
// opened its own changeset (D-DH) and staged nothing has been rejected, so
// the pane never renders "done" against a changeset that is about to
// disappear.
//
// A synthetic, zero-value agent.StageEv precedes the terminal event when
// that reject happens: ask.go's existing StageEv handling already turns any
// StageEv into a ui.StageChangedMsg, and the empty form is exactly what
// review's own Commit/Reject broadcast to say "no changeset is open any
// more" (changesetGone, below) — reusing it here refreshes the shell's
// STAGE badge and archives this turn's session without a new message type.
func forwardTurn(ctx context.Context, e *stage.Engine, sessionID string, openedHere bool, sendCh <-chan agent.Event, out chan<- agent.Event) {
	var terminal agent.Event
	for ev := range sendCh {
		if isTerminalEvent(ev) {
			terminal = ev
			continue
		}
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}

	if openedHere && e != nil {
		if cs, err := e.Current(); err == nil && cs.ID == sessionID && len(cs.Live()) == 0 {
			if err := e.Reject("ask turn staged nothing"); err == nil {
				select {
				case out <- agent.StageEv{}:
				case <-ctx.Done():
				}
			}
		}
	}

	if terminal != nil {
		select {
		case out <- terminal:
		case <-ctx.Done():
		}
	}
	close(out)
}

// isTerminalEvent reports whether ev is one of backbone §9's two terminal
// events (DoneEv, ErrorEv) — exactly one ends a turn, never both (C-105).
func isTerminalEvent(ev agent.Event) bool {
	switch ev.(type) {
	case agent.DoneEv, agent.ErrorEv:
		return true
	default:
		return false
	}
}

// ensureSession makes sure a turn can actually run under id, when id names a
// changeset this turn did not just open itself. Loop.Send resolves its
// session through Sessions.Get before it does anything else (backbone §9),
// and a file-backed store keys a session by the changeset id (backbone §9,
// C-102) — so a changeset opened by another verb (`lw stage --from`, `lw
// revert`) has no session yet and needs one created, while a changeset `lw
// ingest` opened already has the session that verb created, and reusing it
// is the point: the transcript stays one continuous record per changeset
// across processes.
//
// Create is only ever called here after id has been confirmed open —
// Engine.Current for a changeset already open at submit, or resolveTurnChangeset's
// own Current fallback (C-124/D-DH) — so the directory Create writes into
// (changesets/open/<id>/) is the changeset's own, never a fabricated one. A
// changeset this turn opened itself calls ss.Create directly instead
// (runTurn), immediately after OpenChangeset returns, which is what closes
// C-118's race for that path.
func ensureSession(ss agent.SessionStore, id string) error {
	if _, err := ss.Get(id); err == nil {
		return nil
	}
	if _, err := ss.Create(id); err != nil {
		return fmt.Errorf("ask: open a session for changeset %s: %w", id, err)
	}
	return nil
}

// closeSessionCmd archives id's session in its own tea.Cmd, so the
// filesystem work (and the directory fsync a file-backed Close performs)
// never blocks Update. The outcome comes back as a sessionClosedMsg so a
// failure is visible in the scrollback instead of vanishing.
func closeSessionCmd(ss agent.SessionStore, id string) tea.Cmd {
	return func() tea.Msg {
		return sessionClosedMsg{err: ss.Close(id)}
	}
}

// changesetGone archives the session a turn ran under once the changeset it
// is bound to has gone — the empty ui.StageChangedMsg review emits after
// Engine.Commit or Engine.Reject, which the shell broadcasts to every pane
// (backbone §12, C-106/TD-4). Closing it here, rather than in the review
// screen, archives the transcript beside the audit trail this pane's turns
// wrote without review ever having to know a session exists.
//
// A populated ChangesetID is a change, not a removal, and leaves the
// session alone.
func (m *Model) changesetGone(changesetID string) tea.Cmd {
	if changesetID != "" || m.sessionID == "" {
		return nil
	}
	ag := m.deps.Agent
	if ag == nil {
		m.sessionID = ""
		return nil
	}
	ss := ag.Sessions()
	if ss == nil {
		m.sessionID = ""
		return nil
	}
	id := m.sessionID
	m.sessionID = ""

	// A turn still running against a now-closed session has nothing left to
	// stage: cancelling it is what stops it writing records into a session
	// that has just been archived. Send unwinds promptly and closes the
	// channel with no terminal event of its own (backbone §9, C-105), which
	// lands here as an ordinary StreamClosedMsg.
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	return closeSessionCmd(ss, id)
}

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
		m.endTurnError(msg)
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

// endTurnError is endTurn's ErrorEv twin (backbone §9's other terminal
// event): the same turn boundary, but the entry renders as an error —
// theme.Bad, not the muted status grey — because a turn that died is the
// one thing in the scrollback a curator must not scroll past (s5-agent-
// loop.md S5-T5: "render ErrorEv visibly rather than panicking").
func (m *Model) endTurnError(msg string) {
	m.entries = append(m.entries, entry{kind: kindError, text: msg})
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

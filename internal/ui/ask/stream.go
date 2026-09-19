// stream.go implements the ask screen's agent.Event pump (backbone §9,
// §12; s4-tui.md S4-T6 pinned item 2) and, since S5-T5, the turn start
// that feeds it: the exported StreamMsg/Listen/EventMsg/StreamClosedMsg
// seam a caller drives with nothing but a ui.Pane and a <-chan
// agent.Event. The pane is the only thing that ever reads from that
// channel — Update re-arms Listen after every event (state.go's rearm),
// the ordinary Bubble Tea pump pattern, rather than a goroutine draining
// the channel on its own. Submitting a question starts a real agent turn
// (backbone §9, C-105: the pane makes the channel, the pane's goroutine
// runs Send) and hands that channel to the pump through the very same
// StreamMsg.
//
// Since C-124/D-DH (S6), a turn that started with no changeset open
// resolves and opens one itself (runTurn, session.go's
// resolveTurnChangeset) rather than the pane refusing at submit; the turn
// rejects that changeset afterwards if it staged nothing.
package ask

import (
	"context"
	"errors"
	"fmt"

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
// that is filesystem and lint work that has to stay off Update. carryFrom
// is the pane's convID (009 §3.1): the session a fresh session for this
// turn is seeded from, "" on the pane's first turn. Every value the
// goroutine needs is passed in, never read off m: Update owns the model,
// and a goroutine writing to it would be a data race.
//
// ctx is cancellable and remembered on m, so the pane can abandon a turn
// whose changeset was committed or rejected underneath it (see
// changesetGone); Send closes its channel promptly on cancellation and
// delivers no terminal event of its own (backbone §9, C-105).
func (m *Model) startTurn(sessionID, carryFrom, msg string) tea.Cmd {
	ag := m.deps.Agent
	e := m.deps.Engine
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	started := make(chan turnStartedMsg, 1)
	out := make(chan agent.Event, turnEventBuffer)
	go runTurn(ctx, ag, e, sessionID, carryFrom, msg, started, out)

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
func runTurn(ctx context.Context, ag agent.Agent, e *stage.Engine, sessionID, carryFrom, msg string, started chan<- turnStartedMsg, out chan agent.Event) {
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

	created := false
	if openedHere {
		// Created immediately after OpenChangeset returns, with no other
		// engine call in between — the "session created WITH the
		// changeset" seam C-118 asked for, closing that race for this path.
		if _, err := ss.Create(sessionID); err != nil {
			fail(fmt.Errorf("ask: open a session for changeset %s: %w", sessionID, err))
			return
		}
		created = true
	} else {
		createdHere, err := ensureSession(ss, sessionID)
		if err != nil {
			fail(err)
			return
		}
		created = createdHere
	}

	// 009 §3.1: a session this turn just created starts empty — its
	// predecessor was archived with its changeset — so the conversation is
	// carried over before Send ever runs. A session this turn merely
	// reused (its own still-open changeset, or another verb's) keeps the
	// records it already has and is never seeded. After Create, before
	// Send; a seed failure ends the turn here.
	if created && carryFrom != "" && carryFrom != sessionID {
		if _, err := agent.SeedSession(ss, carryFrom, sessionID); err != nil {
			fail(fmt.Errorf("ask: carry the conversation into %s: %w", sessionID, err))
			return
		}
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

// forwardTurn relays sendCh — the channel Agent.Send actually writes into —
// onto out, the channel this pane's pump reads (backbone §9, C-105: Send
// closes sendCh on every exit path, so the range below terminates on its
// own). It holds the turn's one terminal event back until a turn that
// opened its own changeset (D-DH) and staged nothing has been rejected, so
// the pane never renders "done" against a changeset that is about to
// disappear.
//
// A synthetic, zero-value agent.StageEv precedes the terminal event when
// that reject happens: the pane's existing StageEv handling already turns
// any StageEv into a ui.StageChangedMsg, and the empty form is exactly what
// review's own Commit/Reject broadcast to say "no changeset is open any
// more" (changesetGone, session.go) — reusing it here refreshes the
// shell's header and archives this turn's session without a new message
// type.
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

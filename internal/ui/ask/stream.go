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
// Since 057 a turn runs in the pane's one in-memory conversation (session.go)
// and opens no changeset of its own: the agent opens one at the turn's first
// staging call and writes the turn into that changeset's session.ndjson as it
// runs (the ask store's tee). forwardTurn's adopt step is the fallback for a turn
// that was not teed. Before 057 every question opened a changeset here
// (C-124/D-DH, resolveTurnChangeset) and rejected it unread when the turn staged
// nothing — half of a real vault's changesets.
package ask

import (
	"context"
	"errors"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/logging"
	"github.com/awepo-pro/lw/internal/trace"
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

// turnStartedMsg reports that a turn's conversation is open — sessionID is the
// pane's conversation id (057; it named the turn's changeset before) — or the
// failure that ends the turn before Agent.Send ever runs, plus, on success, the
// channel to install as the pane's active stream. It is delivered before any
// agent.Event so the pane's own bookkeeping (convID, ctrl+r → Review) is never
// behind the turn it tracks.
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

// turnSpec is everything runTurn's goroutine needs about one turn, fixed on the
// Update thread before it starts: Update owns the model, and a goroutine reading
// it would be a data race.
type turnSpec struct {
	convID string // the pane's conversation (session.go)
	turnID string // minted here so the adopt step can find the turn's records
	msg    string // what the agent receives
	verb   string // trace.VerbAsk or trace.VerbFile (039)
}

// startTurn launches one agent turn (backbone §9, C-105) in the pane's
// conversation m.convID, which beginTurnAs has already minted. The turn id is
// minted here, on the Update thread, and runTurn puts it on the ctx: Loop.Send
// stamps it on every record the turn appends (038), which is how AdoptTurn
// later picks this turn's records out of the conversation.
//
// ctx is cancellable and remembered on m, so the pane can abandon a turn whose
// changeset was committed or rejected underneath it (see changesetGone); Send
// closes its channel promptly on cancellation and delivers no terminal event of
// its own (backbone §9, C-105).
//
// verb is the turn's ctx verb — trace.VerbAsk or trace.VerbFile (039): a plain value, like
// every other input the goroutine takes, so what the turn is called is decided
// on the Update thread by whoever began it.
func (m *Model) startTurn(msg, verb string) tea.Cmd {
	ag := m.deps.Agent
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	spec := turnSpec{convID: m.convID, turnID: trace.NewID(time.Now()), msg: msg, verb: verb}
	started := make(chan turnStartedMsg, 1)
	out := make(chan agent.Event, turnEventBuffer)
	adopted := make(chan error, 1)
	m.adopted = adopted
	go runTurn(ctx, ag, spec, started, out, adopted)

	return func() tea.Msg { return <-started }
}

// runTurn is startTurn's goroutine body (backbone §9, C-105). It makes sure the
// conversation exists in the agent's session store, reports that (or a failure)
// back through started before Agent.Send ever runs, then relays Send's events
// through forwardTurn, which also adopts a turn that staged into its
// changeset's session. runTurn never touches Update or m: everything it needs
// is a parameter, and everything it produces goes out through started, out or
// adopted.
func runTurn(ctx context.Context, ag agent.Agent, spec turnSpec, started chan<- turnStartedMsg, out chan agent.Event, adopted chan<- error) {
	fail := func(err error) {
		started <- turnStartedMsg{err: err}
		close(started)
		close(out)
	}

	ss := ag.Sessions()
	if ss == nil {
		fail(errors.New("ask: the agent has no session store"))
		return
	}
	if err := ensureConversation(ss, spec.convID); err != nil {
		fail(err)
		return
	}

	started <- turnStartedMsg{sessionID: spec.convID, ch: out}
	close(started)

	sendCh := make(chan agent.Event, turnEventBuffer)
	go func() {
		// The returned error is deliberately dropped: when Send fails it has
		// already delivered that same error as the turn's ErrorEv (backbone
		// §9, C-105), and forwardTurn relays events, not return values.
		// 038: the pane's turn is tagged with its verb — "ask" for a
		// question, "file" for a ctrl+s filing turn (039) — and the tag rides
		// the ctx (038 C-3), beside the turn id the adopt step needs and the
		// intent a changeset the turn's first staging call opens is given
		// (057 D2).
		tctx := trace.WithVerb(ctx, spec.verb)
		tctx = logging.WithTurn(tctx, spec.turnID)
		tctx = agent.WithOpenIntent(tctx, askIntent(spec.msg))
		_ = ag.Send(tctx, spec.convID, spec.msg, sendCh)
	}()

	forwardTurn(ctx, ss, spec, sendCh, out, adopted)
}

// forwardTurn relays sendCh — the channel Agent.Send actually writes into —
// onto out, the channel this pane's pump reads (backbone §9, C-105: Send
// closes sendCh on every exit path, so the range below terminates on its
// own). It holds the turn's one terminal event back until the adopt step has
// run, so the pane never renders "done" before the turn's records are where
// `lw session show` will look for them.
//
// Adopt (057 D4, the FALLBACK since FIX2 R2): the last StageEv says which
// changeset, X, the turn staged into ("" when the turn's own empty StageEv came
// last: it is gone, rejected empty or never there). Once sendCh has closed —
// every record the turn appends is written by then — agent.AdoptTurn copies the
// turn into X's session.ndjson, but only if nothing did already: the Loop tees a
// staging turn into its changeset as it runs, and AdoptTurn appends nothing for
// a teed turn. A changeset that is no longer open adopts nothing (AdoptTurn logs
// "ask turn not adopted" and creates no directory). A failure never fails the
// finished turn: it is logged and handed to the pane through adopted, which shows
// it as a status line when the stream closes. adopted is buffered, so this never
// blocks.
//
// A turn that staged nothing — the normal ask, since 039 — has no changeset to
// reject: it opened none (057), so unlike before there is no synthetic empty
// StageEv here and no journal line.
func forwardTurn(ctx context.Context, ss agent.SessionStore, spec turnSpec, sendCh <-chan agent.Event, out chan<- agent.Event, adopted chan<- error) {
	var terminal agent.Event
	stagedInto := ""
	for ev := range sendCh {
		if isTerminalEvent(ev) {
			terminal = ev
			continue
		}
		if sev, ok := ev.(agent.StageEv); ok {
			stagedInto = sev.ChangesetID // "": the changeset is gone (rejected empty, or never there); nothing to adopt into
		}
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}

	if stagedInto != "" && ss != nil {
		if _, err := agent.AdoptTurn(ss, spec.convID, stagedInto, spec.turnID); err != nil {
			slog.Warn("ask turn not adopted", "changeset", stagedInto, "turn", spec.turnID, "error", err)
			adopted <- err
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

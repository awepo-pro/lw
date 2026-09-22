// fakeagent.go implements contract §6's FakeAgent: a scripted agent.Agent
// whose Send replays Events in order on out and then closes it — the
// channel ownership contract Loop.Send honours (backbone §9, C-105: the
// caller makes the channel, Send closes it on every exit path).
//
// The pattern is internal/ui/ask's fakeTurnAgent lifted into the shared
// harness, with the pause plumbing dropped: a conformance script wants a
// turn that runs to quiescence, not one held mid-stream.
package uitest

import (
	"context"

	"github.com/awepo-pro/lw/internal/agent"
)

// FakeAgent is a scripted agent.Agent: Send replays Events in order on out,
// then closes out (backbone §9 ownership). Sessions returns Store, which
// must be a real agent.NewFileSessions store rooted at the vault — the ask
// pane exercises the real file-backed session store through it, never a
// stub.
//
// The script may carry any event kind, including agent.ReasoningDelta (022
// T2) — ReasoningTurn is the shorthand for the thinking-then-answer shape a
// thinking-mode provider streams.
type FakeAgent struct {
	Events []agent.Event
	Store  agent.SessionStore
}

var _ agent.Agent = (*FakeAgent)(nil)

// ReasoningTurn scripts the visible half of one thinking-mode round: every
// string in reasoning streams as its own agent.ReasoningDelta, in order,
// before text streams as one agent.TextDelta — the event order Loop.Send
// produces for a round that thinks and then answers. The caller appends the
// turn's terminal event (DoneEv or ErrorEv); the helper never ends the turn
// on its own.
func ReasoningTurn(reasoning []string, text string) []agent.Event {
	events := make([]agent.Event, 0, len(reasoning)+1)
	for _, r := range reasoning {
		events = append(events, agent.ReasoningDelta{Text: r})
	}
	if text != "" {
		events = append(events, agent.TextDelta{Text: text})
	}
	return events
}

// Send replays f.Events on out in order, then closes out. A caller that
// stops reading cannot wedge the replay: every send selects on ctx.Done,
// and the cancellation path closes out before returning the context error,
// exactly as Loop.Send must.
func (f *FakeAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	for _, ev := range f.Events {
		select {
		case out <- ev:
		case <-ctx.Done():
			close(out)
			return ctx.Err()
		}
	}
	close(out)
	return nil
}

// Sessions returns f.Store — the store the caller built, shared by identity.
func (f *FakeAgent) Sessions() agent.SessionStore { return f.Store }

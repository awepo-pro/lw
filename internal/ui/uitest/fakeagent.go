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
type FakeAgent struct {
	Events []agent.Event
	Store  agent.SessionStore
}

var _ agent.Agent = (*FakeAgent)(nil)

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

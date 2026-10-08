// terminal.go implements the terminal-event hook (042 D4): one callback an
// embedder installs to hear that a changeset reached a terminal state —
// committed or rejected. lw sync uses it to push the vault the moment a
// commit lands, from the CLI verbs and from the TUI's review pane alike,
// without cmd/lw or internal/ui knowing about sync and without a polling
// loop watching the journal.
package stage

import "log/slog"

// TerminalEvent describes one changeset that just reached a terminal state.
type TerminalEvent struct {
	Kind      string // "commit" | "reject"
	Changeset string // the changeset id
	CommitID  string // commit only: the six-digit commit id
	Message   string // the commit message, or the reject reason
}

// Terminal event kinds.
const (
	terminalCommit = "commit"
	terminalReject = "reject"
)

// OnTerminal installs fn as the engine's terminal-event hook, replacing any
// previous one; nil removes it. At most one hook is installed — the sync
// layer is the only consumer, and two would have to agree on ordering.
//
// Contract (042 D4): fn runs after a Commit has returned its commit_end to
// the journal and moved the changeset to committed/, and after a Reject has
// journalled changeset_rejected. It runs on the goroutine that called Commit
// or Reject, after the call has released every engine mutex and the vault
// lock, so fn may call straight back into the engine — and may itself take
// the vault lock, which is exactly what a push does. It is never called when
// Commit or Reject returns an error, including a Commit whose commit_end was
// already journalled but whose later step failed: that commit is the
// caller's to recover, not an event to announce. fn blocks the verb that
// triggered it, so a slow consumer should hand the work to its own goroutine.
//
// A panic in fn is recovered and logged (lw.log, "terminal hook panicked");
// the Commit or Reject that triggered it returns its normal result.
//
// OnTerminal is safe to call while other goroutines commit or reject; the
// hook is read once per terminal event.
func (e *Engine) OnTerminal(fn func(TerminalEvent)) {
	if fn == nil {
		e.onTerminal.Store(nil)
		return
	}
	e.onTerminal.Store(&fn)
}

// fireTerminal calls the installed hook, if any, with ev. Callers invoke it
// only after releasing writeMu: the hook is user code and must never run
// inside the single-writer lock (A-803), where a call back into any mutating
// verb would deadlock.
//
// A panic in the hook is recovered and logged. By the time it runs the commit
// or rejection is durable, so letting the panic unwind would crash a verb or
// the TUI and report a failure for work that landed. The hook is the sync
// layer's code and a bug there must cost a push, never the user's commit.
func (e *Engine) fireTerminal(ev TerminalEvent) {
	fn := e.onTerminal.Load()
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("terminal hook panicked", "panic", r, "kind", ev.Kind)
		}
	}()
	(*fn)(ev)
}

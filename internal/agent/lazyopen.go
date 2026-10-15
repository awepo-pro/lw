package agent

// lazyopen.go is 057 D2: the changeset a turn stages into is opened by the
// loop at the turn's first staging call, not by whoever started the turn. The
// TUI's ask pane used to open one for every question — the web flow and the
// ctrl+s filing turn only worked because it had — and reject it unread when the
// turn read and answered, which since 039 is the normal case (16 of 41 real
// changesets; each a changeset_opened/changeset_rejected pair, a rejected/
// directory and a sync push). Nothing in the prompts tells the model to call
// stage.open, so the first stage.ingest_source or stage.create_page of such a
// turn arrives at an engine with nothing open; the loop opens one there.
//
// A changeset opened that way can end the turn with nothing in it — the call
// that opened it came back IsError (a create_page without a rationale), and the
// model gave up. The pane used to reject such a changeset itself ("ask turn
// staged nothing"); the loop is the only place that knows which changeset its
// own lazy open created, so the loop rejects it at the end of the turn
// (settleLazyOpen).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/trace"
)

// openIntentKey is the ctx key WithOpenIntent stores under.
type openIntentKey struct{}

// WithOpenIntent returns ctx carrying the intent a changeset the loop opens
// lazily (D2) is given — the one-line "why" `lw log` and `lw status` show. The
// Ask pane sets it to the question ("ask: …"), which is byte for byte the
// intent the changeset it used to open for itself carried. Without it the loop
// falls back to "agent: <verb>".
func WithOpenIntent(ctx context.Context, intent string) context.Context {
	return context.WithValue(ctx, openIntentKey{}, intent)
}

// lazyOpenEmptyReason is the reason an empty lazily opened changeset is rejected
// with — the pane's old reject reason, so the journal reads as it always has.
const lazyOpenEmptyReason = "ask turn staged nothing"

// lazyOpened is the one thing a turn remembers about its lazy open: the id of the
// changeset THIS turn's open created. It rides the ctx as a pointer, because the
// open happens deep in dispatchToolCall and the verdict is Send's, at the end of
// the turn. Only a changeset OpenOrJoin created is recorded — one it joined (an
// `lw ingest` opened it between the Current check and the open) is not ours —
// and a turn whose staging verb pre-opened its changeset never records
// anything. Send is synchronous and dispatch sequential, so it needs no lock.
type lazyOpened struct {
	id      string
	settled bool
}

// lazyOpenedKey is the ctx key withLazyOpened stores under.
type lazyOpenedKey struct{}

// withLazyOpened returns ctx carrying a fresh lazyOpened, and the pointer to it.
func withLazyOpened(ctx context.Context) (context.Context, *lazyOpened) {
	lz := &lazyOpened{}
	return context.WithValue(ctx, lazyOpenedKey{}, lz), lz
}

// lazyOpenedFrom returns the turn's lazyOpened, or nil outside a Send.
func lazyOpenedFrom(ctx context.Context) *lazyOpened {
	lz, _ := ctx.Value(lazyOpenedKey{}).(*lazyOpened)
	return lz
}

// openIntentFrom returns the intent WithOpenIntent stored in ctx, or "" when
// there is none.
func openIntentFrom(ctx context.Context) string {
	s, _ := ctx.Value(openIntentKey{}).(string)
	return s
}

// stagesOps reports whether the canonical tool name is a stage.* tool that
// needs a changeset to stage into: all of them except stage.open, which opens
// its own on request, and stage.close, which summarizes what is open and
// answers a vault with nothing open with today's tool error — a model that
// closes before it staged must not conjure a changeset to close.
func stagesOps(canonical string) bool {
	return strings.HasPrefix(canonical, "stage.") && canonical != "stage.open" && canonical != "stage.close"
}

// lazyOpenFailedFmt is the tool error a call gets when the engine could not
// open its changeset. The wording is a frozen contract (lazyopen_test.go).
const lazyOpenFailedFmt = "stage: could not open a changeset for this call: %v"

// lazyOpen opens a changeset for canonical's call when the call stages and
// nothing is open, and returns "" — or the tool error text when it could not.
// Called by dispatchToolCall once the call is certain to run: after the offered,
// withheld, repeat and budget refusals and the argument parse, and only for a
// tool the registry knows, so a refused, malformed or invented call never
// opens one.
//
// It opens with OpenOrJoin, the seam `lw ingest`, `lint --fix` and stage.open
// share: if a changeset appeared between the Current check and the open (an `lw
// ingest` started beside the TUI), the call joins it instead of failing. The
// author is the model the loop already records in its turn event, with the
// conversation's session id — the pane's "ask-…" id, so a reader of
// changeset.json can tell which conversation staged it. `lw ingest` and `lint
// --fix` open before they send, so they never reach the open: Current answers.
func (l *Loop) lazyOpen(ctx context.Context, sessionID, canonical string) string {
	if l.engine == nil || !stagesOps(canonical) {
		return ""
	}
	if _, ok := l.tools.Get(canonical); !ok {
		return ""
	}
	if _, err := l.engine.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		return ""
	}

	if l.beforeLazyOpen != nil {
		l.beforeLazyOpen()
	}

	intent := openIntentFrom(ctx)
	if intent == "" {
		verb := trace.VerbFrom(ctx)
		if verb == "" {
			verb = "turn"
		}
		intent = "agent: " + verb
	}
	cs, joined, err := l.engine.OpenOrJoin(intent, stage.Author{Kind: "agent", Model: l.cfg.TraceMeta.Model, Session: sessionID})
	if err != nil {
		return fmt.Sprintf(lazyOpenFailedFmt, err)
	}
	if lz := lazyOpenedFrom(ctx); lz != nil && !joined {
		lz.id = cs.ID
	}
	slog.InfoContext(ctx, "agent lazy open", "changeset", cs.ID, "tool", canonical, "joined", joined)
	return ""
}

// settleLazyOpen rejects the changeset this turn's lazy open created if the turn
// is ending with no live op in it (reason lazyOpenEmptyReason, through
// Engine.Reject — journalled, moved to rejected/, and the sync hook told, like
// any rejection), and streams the empty StageEv the pane's own reject used to,
// so the shell's header and the pane's staged-changeset bookkeeping drop it. It
// is called before every terminal event Send emits and, once more, as Send
// returns (a cancelled turn has no terminal event); the first call decides.
//
// The scope is exact. Nothing happens when the turn did not lazily open a
// changeset (an `lw ingest` pre-opened its own, or the turn only read), or when
// OpenOrJoin joined one it did not create. And the changeset must still be open
// under the id this turn recorded: one Review committed or rejected, or that
// another verb replaced, is not ours to touch — Current names a different id or
// none. A changeset with a live op is the curator's to review and is left as it
// is. The remaining window — an op appended by another process between the Live
// check and the Reject — is the engine's: Reject takes no id.
func (l *Loop) settleLazyOpen(ctx context.Context, out chan<- Event) {
	lz := lazyOpenedFrom(ctx)
	if lz == nil || lz.id == "" || lz.settled {
		return
	}
	lz.settled = true

	cs, err := l.engine.Current()
	if err != nil || cs.ID != lz.id || len(cs.Live()) > 0 {
		return
	}
	if err := l.engine.Reject(lazyOpenEmptyReason); err != nil {
		slog.WarnContext(ctx, "agent lazy open reject failed", "changeset", lz.id, "error", err)
		return
	}
	slog.InfoContext(ctx, "agent lazy open rejected", "changeset", lz.id, "reason", lazyOpenEmptyReason)
	l.send(ctx, out, StageEv{})
}

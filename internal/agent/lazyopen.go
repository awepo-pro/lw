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
	slog.InfoContext(ctx, "agent lazy open", "changeset", cs.ID, "tool", canonical, "joined", joined)
	return ""
}

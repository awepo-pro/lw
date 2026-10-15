// session.go is the ask screen's conversation lifecycle (057): the one
// in-memory session every turn of the pane runs in, the id it is minted under,
// what a Review commit or reject means for a turn that is running, and the
// one-line intent a changeset the turn's first staging call opens is stamped
// with.
//
// Before 057 a session was keyed by its changeset (backbone §9, C-102), so every
// question that found nothing open opened a changeset, ran inside it, and
// rejected it unread — half of the changesets in a real vault, each a journal
// pair, a rejected/ directory and a sync push. The conversation is now its own
// session (agent.NewAskSessions); a changeset exists only when a turn stages
// into one (agent's lazy open) and the staging turn is adopted into it
// (agent.AdoptTurn, stream.go).
package ask

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/awepo-pro/lw/internal/agent"
)

// convIDPrefix starts every conversation id; agent.NewAskSessions keeps ids
// with it in memory.
const convIDPrefix = "ask-"

// newConvID mints a conversation id from r: "ask-" and 16 lowercase hex digits.
// It reads r rather than crypto/rand directly so a test can pin the bytes.
func newConvID(r io.Reader) string {
	var b [8]byte
	_, _ = io.ReadFull(r, b[:]) // crypto/rand.Reader never fails on supported platforms
	return convIDPrefix + hex.EncodeToString(b[:])
}

// mintConvID is the pane's id source: crypto/rand, so two panes (or two TUIs on
// one vault) never share a conversation.
func mintConvID() string { return newConvID(rand.Reader) }

// ensureConversation makes sure the session store holds conversation id,
// creating it when it does not. It reports a failure as the turn's error: a turn
// whose conversation cannot be opened ends before Agent.Send, exactly as a turn
// whose changeset could not be opened used to. With the production store
// (agent.NewAskSessions) Create is a map insert and touches no file.
func ensureConversation(ss agent.SessionStore, id string) error {
	if _, err := ss.Get(id); err == nil {
		return nil
	}
	if _, err := ss.Create(id); err != nil {
		return fmt.Errorf("ask: open the conversation %s: %w", id, err)
	}
	return nil
}

// askIntentRunes bounds the intent a changeset opened for a turn is stamped
// with, so `lw log`/`lw status` show a readable one-liner instead of a raw,
// possibly multi-line question.
const askIntentRunes = 60

// askIntent turns msg into that intent: "ask: " plus the question collapsed
// to one line and truncated to askIntentRunes runes. The turn hands it to the
// agent on its ctx (agent.WithOpenIntent); the loop uses it when the turn's
// first staging call has to open the changeset (057 D2) — byte for byte the
// intent the changeset this pane used to open for itself carried.
func askIntent(msg string) string {
	return "ask: " + truncateRunes(singleLine(msg), askIntentRunes)
}

// singleLine collapses text to one line — internal newlines would
// otherwise make `lw log`'s intent column multi-line.
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

// changesetGone reacts to the empty ui.StageChangedMsg review emits after
// Engine.Commit or Engine.Reject, which the shell broadcasts to every pane
// (backbone §12, C-106/TD-4): no changeset is open any more.
//
// The conversation is untouched — it is not the changeset's (057 D3). Before
// 057 this closed the session the turn ran under and cancelled whatever turn was
// running, because the session and the changeset were one thing; now a turn is
// cancelled ONLY when it has staged into the changeset that went away
// (m.stagedID, set from the turn's StageEv): it has nothing left to stage into,
// and cancelling it is what stops it appending records its adopt step could not
// place. A question still being answered while an unrelated changeset (an
// `lw ingest`'s) is rejected goes on. Send unwinds promptly and closes the
// channel with no terminal event of its own (backbone §9, C-105), which lands
// here as an ordinary StreamClosedMsg.
//
// A populated ChangesetID is a change, not a removal, and leaves everything
// alone.
func (m *Model) changesetGone(changesetID string) {
	if changesetID != "" || m.stagedID == "" {
		return
	}
	m.stagedID = ""
	if m.turnActive && m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// session.go is the ask screen's session lifecycle: how a turn finds or
// opens the changeset it runs under (a session is keyed by its changeset,
// backbone §9, C-102), how the session is archived once that changeset is
// committed or rejected, and the one-line intent a self-opened changeset
// stamps into the journal.
package ask

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
)

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

// ensureSession makes sure a turn can actually run under id, when id names a
// changeset this turn did not just open itself, and reports whether it
// CREATED the session — 009 §3.1's seeding condition: only a session the
// turn created is seeded with the previous conversation, never one another
// verb or turn already owns. Loop.Send resolves its session through
// Sessions.Get before it does anything else (backbone §9), and a file-backed
// store keys a session by the changeset id (backbone §9, C-102) — so a
// changeset opened by another verb (`lw stage --from`, `lw revert`) has no
// session yet and needs one created, while a changeset `lw ingest` opened
// already has the session that verb created, and reusing it is the point:
// the transcript stays one continuous record per changeset across processes.
//
// Create is only ever called here after id has been confirmed open —
// Engine.Current for a changeset already open at submit, or resolveTurnChangeset's
// own Current fallback (C-124/D-DH) — so the directory Create writes into
// (changesets/open/<id>/) is the changeset's own, never a fabricated one. A
// changeset this turn opened itself calls ss.Create directly instead
// (runTurn), immediately after OpenChangeset returns, which is what closes
// C-118's race for that path.
func ensureSession(ss agent.SessionStore, id string) (created bool, err error) {
	if _, err := ss.Get(id); err == nil {
		return false, nil
	}
	if _, err := ss.Create(id); err != nil {
		return false, fmt.Errorf("ask: open a session for changeset %s: %w", id, err)
	}
	return true, nil
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

// sessionClosedMsg reports the outcome of archiving the session a turn ran
// under, after the changeset it belonged to was committed or rejected
// (backbone §9, C-102). Unexported: only this pane produces and consumes
// it, and the pane has no other way to surface a Close failure than its
// own scrollback.
type sessionClosedMsg struct{ err error }

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

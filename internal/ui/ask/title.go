// title.go is the Transcript panel title's state machine (005 contract §6,
// as amended by R-509/D-5L, the user's U3 finding): the open changeset's
// short id — and, once that changeset stops being open, the last session
// the title showed, kept beside the state word `committed` or `rejected`
// that `lw session list` would print for it. A turn that opened its own
// changeset and staged nothing is auto-rejected the moment its answer
// finishes, and the id used to vanish with it; keeping it here is what
// tells the curator the conversation still exists and where to read it
// back. Like titleID, the kept id is Update-thread bookkeeping the render
// path only reads; the state lookup is a tea.Cmd, so Update does no
// filesystem I/O either.
package ask

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// titleFateMsg carries the answer stage.Engine.ChangesetState gave for the
// kept id: the state word the title shows once it lands. Unexported — only
// this pane produces and consumes it, and the shell already routes a pane's
// own command's message back to that pane (internal/ui/app.go's
// deliverTo), the same way turnStartedMsg stays unexported. state is ""
// when the lookup failed; the title renders that as no suffix.
type titleFateMsg struct {
	id    string
	state string
}

// noteStageChanged folds a ui.StageChangedMsg into the title's ids. A
// populated ChangesetID is the changeset now open: it replaces any kept id.
// The empty form — review's Commit/Reject broadcast, and the pane's own
// auto-reject reaching the pane the same way — is when the id the title
// currently shows stops being open and becomes the kept one. The returned
// command is the kept id's state lookup, nil when nothing was kept.
func (m *Model) noteStageChanged(changesetID string) tea.Cmd {
	var fate tea.Cmd
	if changesetID == "" {
		fate = m.keepTitleID()
	} else {
		m.dropKeptTitle()
	}
	m.titleID = changesetID
	return fate
}

// keepTitleID moves whatever id the title currently shows into the kept
// slot and owes it a state lookup. An id already kept is left exactly as it
// is — a second empty broadcast, or a reload that finds nothing while a
// kept id is showing, takes nothing away: the kept id is replaced by the
// next open changeset or turn, never cleared or re-kept by a broadcast.
func (m *Model) keepTitleID() tea.Cmd {
	if m.titleID == "" {
		return nil
	}
	m.keptID = m.titleID
	m.keptState = "" // not known yet; the lookup below is what names it
	return m.fateCmd(m.keptID)
}

// dropKeptTitle clears the kept id: a populated broadcast or a started turn
// has named the changeset open now, and the title shows that one instead.
func (m *Model) dropKeptTitle() {
	m.keptID = ""
	m.keptState = ""
}

// fateCmd builds the command that resolves id's state off the render path:
// ChangesetState is a pure stat, but it is still filesystem I/O, and
// Update never does filesystem I/O. A failed lookup comes back as an empty
// state — a title that cannot know says nothing rather than guessing.
func (m *Model) fateCmd(id string) tea.Cmd {
	e := m.deps.Engine
	if e == nil {
		return nil
	}
	return func() tea.Msg {
		state, err := e.ChangesetState(id)
		if err != nil {
			return titleFateMsg{id: id}
		}
		return titleFateMsg{id: id, state: state}
	}
}

// noteTitleFate records a lookup's answer. A message is stale — and
// ignored — unless it names the id currently kept: between the lookup
// leaving and its answer landing, a new turn or broadcast may have replaced
// the kept id, and an answer for a replaced id would pin yesterday's state
// onto today's title.
func (m *Model) noteTitleFate(msg titleFateMsg) {
	if m.keptID == "" || msg.id != m.keptID {
		return
	}
	m.keptState = msg.state
}

// fateIfOwed re-arms the state lookup when it is still outstanding. The
// empty StageChangedMsg that keeps an id usually owes the session its
// archive too (changesetGone), and the archive's command is the one Update
// returns — the session lifecycle pins the empty broadcast's command being
// the archive itself — so the lookup rides the archive's outcome message
// (sessionClosedMsg) instead of batching beside it.
func (m *Model) fateIfOwed() tea.Cmd {
	if m.keptID == "" || m.keptState != "" {
		return nil
	}
	return m.fateCmd(m.keptID)
}

// appendKeptHint appends the one status entry an auto-rejected empty turn
// owes — `nothing staged · conversation kept · lw session show <short id>`
// — directly after the terminal line the turn just ended with, which is
// exactly when the id leaves the title. Only the pane's own auto-reject
// streams an empty StageEv while the turn is active (stream.go
// forwardTurn), so only that turn hints: a turn whose changeset stays open
// and a Review commit or reject add none. The id is the one captured at
// that StageEv (m.hintAfterTurn) and the capture is consumed here, once, so
// no later turn can inherit the hint.
func (m *Model) appendKeptHint() {
	id := m.hintAfterTurn
	m.hintAfterTurn = ""
	if id == "" {
		return
	}
	m.entries = append(m.entries, entry{kind: kindStatus,
		text: "nothing staged · conversation kept · lw session show " + ui.ShortID(id)})
}

// refreshTitleID re-reads the open changeset's id off the engine into
// m.titleID (005 contract §6) — the pane's copy of what app.go's
// refreshStage computes for the header. It runs on the Update thread only
// (ui.VaultReloadedMsg): Engine.Current both does filesystem I/O and
// writes the engine's unlocked open/nextOp fields, and the turn goroutine
// calls Current concurrently with frames rendering, so the render path
// reads the field and never the engine.
//
// Since R-509 a reload also decides the kept id: one that finds nothing
// open keeps whatever the title showed, exactly the way the empty
// StageChangedMsg does, and one that finds a changeset open replaces the
// kept id with it. A genuine read failure still just clears the open id —
// the engine could not say what is open, and the title keeps only ids it
// saw stop being open, not ones it failed to check.
func (m *Model) refreshTitleID() tea.Cmd {
	if m.deps.Engine == nil {
		m.titleID = ""
		return nil
	}
	cs, err := m.deps.Engine.Current()
	if err == nil {
		m.titleID = cs.ID
		m.dropKeptTitle() // the open one replaces whatever was kept
		return nil
	}
	if errors.Is(err, stage.ErrNoChangeset) {
		fate := m.keepTitleID()
		m.titleID = ""
		return fate
	}
	m.titleID = ""
	return nil
}

// transcriptTitle is the Transcript panel's title (005 contract §6, as
// amended by R-509): `Transcript — <short id>` for the open changeset —
// the same ShortID the frame header's cs9 uses, so the two spellings
// cannot drift — and, once that changeset stops being open, the same id
// kept with its state word: `Transcript — <short id> · committed` or
// `· rejected`, the words `lw session list` prints. Plain `Transcript`
// for a pane that never saw a changeset, and no suffix while the kept
// id's state is still unknown (in flight, failed, or still "open"). It
// reads only m's fields — the engine is never touched on the render path.
func (m *Model) transcriptTitle() string {
	id, state := m.titleID, ""
	if id == "" {
		id, state = m.keptID, m.keptState
	}
	if id == "" {
		return "Transcript"
	}
	title := "Transcript — " + ui.ShortID(id)
	if state == "committed" || state == "rejected" {
		title += " · " + state
	}
	return title
}

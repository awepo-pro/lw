package agent

// asksessions.go is 057's in-memory ask conversation. Before it, every TUI ask
// with no changeset open opened one, ran inside it, and rejected it unread
// ("ask turn staged nothing") because a session was keyed by its changeset
// (backbone §9, C-102): 16 of 41 changesets in a real vault were that — each a
// journal pair, a rejected/ directory and, in a vault that syncs, a git push.
// Since 039 an ask is read-only, so "staged nothing" is its normal outcome and
// the changeset was pure ceremony.
//
// NewAskSessions removes the key. A conversation whose id starts "ask-" lives in
// memory for as long as the TUI does and touches no file; every other id is the
// file store's, unchanged. A turn that does stage gets its changeset lazily
// (lazyopen.go) and is then adopted into that changeset's session.ndjson
// (AdoptTurn), so design D9 — a session is bound to a changeset and travels with
// it — still holds for every changeset that exists.

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// askSessionPrefix marks an id as an in-memory ask conversation. A changeset id
// is "cs-…" (stage.newChangesetID), so the two namespaces cannot meet.
const askSessionPrefix = "ask-"

// isAskID reports whether id names an in-memory ask conversation.
func isAskID(id string) bool { return strings.HasPrefix(id, askSessionPrefix) }

// errChangesetNotOpen is what attachOpen reports for a changeset with no
// directory under changesets/open/ — committed, rejected or never there.
var errChangesetNotOpen = errors.New("agent: changeset is not open")

// askConv is one in-memory conversation: every record ever appended, tool
// records included, in append order.
type askConv struct {
	started time.Time
	records []Record
}

// askSessions is the SessionStore NewAskSessions returns.
type askSessions struct {
	file SessionStore
	now  func() time.Time // time.Now; a field so a test can freeze Created times

	mu    sync.Mutex
	convs map[string]*askConv
}

var _ SessionStore = (*askSessions)(nil)

// NewAskSessions wraps file so that ids starting "ask-" are held in memory and
// every other id is delegated to file unchanged (057 D1). It is what the TUI
// hands its agent: the Ask pane runs all of its turns in one "ask-…" conversation
// and a changeset exists only once a turn stages into it.
//
// An ask conversation's Get returns the PROSE VIEW of what was appended — only
// the user and assistant records that carry text, each as a Carried record —
// which is exactly what 009's SeedSession copied between the per-changeset
// sessions this replaces. That is deliberate and load-bearing: a Get that
// replayed tool records would send every earlier turn's tool calls back to the
// model as real tool_calls/tool pairs (046) and Compact never shrinks a tool
// record, so a long conversation's context would grow without bound. Append
// still stores the full record; RawRecords (and AdoptTurn through it) is the one
// reader of the unfiltered log.
func NewAskSessions(file SessionStore) SessionStore {
	return &askSessions{file: file, now: time.Now, convs: make(map[string]*askConv)}
}

// Create starts the in-memory conversation id, or delegates a non-ask id to the
// file store. Nothing is written to disk for an ask id, and it has no changeset.
func (a *askSessions) Create(id string) (*Session, error) {
	if !isAskID(id) {
		return a.file.Create(id)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.convs[id]; ok {
		return nil, fmt.Errorf("%w: %s", errSessionExists, id)
	}
	started := a.now().UTC()
	a.convs[id] = &askConv{started: started}
	return &Session{ID: id, Started: started}, nil
}

// Get returns the conversation id as its prose view (see NewAskSessions), or
// delegates a non-ask id to the file store. The Records are a copy.
func (a *askSessions) Get(id string) (*Session, error) {
	if !isAskID(id) {
		return a.file.Get(id)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.convs[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errSessionNotFound, id)
	}
	var view []Record
	for _, r := range c.records {
		if carried, ok := carryRecord(r); ok {
			view = append(view, carried)
		}
	}
	return &Session{ID: id, Started: c.started, Records: view}, nil
}

// Append stores r, whole, at the end of the conversation id (tool records
// included — AdoptTurn needs them), or delegates a non-ask id to the file store.
func (a *askSessions) Append(id string, r Record) error {
	if !isAskID(id) {
		return a.file.Append(id, r)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.convs[id]
	if !ok {
		return fmt.Errorf("%w: %s", errSessionNotFound, id)
	}
	c.records = append(c.records, r)
	return nil
}

// Close drops the conversation id from memory, or delegates a non-ask id to the
// file store. The Ask pane never closes its conversation: it ends with the TUI.
func (a *askSessions) Close(id string) error {
	if !isAskID(id) {
		return a.file.Close(id)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.convs[id]; !ok {
		return fmt.Errorf("%w: %s", errSessionNotFound, id)
	}
	delete(a.convs, id)
	return nil
}

// List is the file store's list. An in-memory conversation is not listed:
// `lw session` is a file-store CLI and an ask conversation has no file for it
// to read, which is the cost 057 accepts for not writing one.
func (a *askSessions) List() ([]string, error) { return a.file.List() }

// rawRecords is the unfiltered log of an ask conversation.
func (a *askSessions) rawRecords(id string) ([]Record, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.convs[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errSessionNotFound, id)
	}
	return append([]Record(nil), c.records...), nil
}

// attachOpen forwards to the file store; see openAttacher.
func (a *askSessions) attachOpen(id string) (bool, error) {
	return attachOpen(a.file, id)
}

// RawRecords returns every record appended to session id in append order, with
// nothing filtered: for an in-memory ask conversation that is the whole log,
// tool calls and results included, where Get returns only the prose view. For
// any other store it is Get's records — a file session has no second view.
func RawRecords(ss SessionStore, id string) ([]Record, error) {
	if a, ok := ss.(*askSessions); ok && isAskID(id) {
		return a.rawRecords(id)
	}
	s, err := ss.Get(id)
	if err != nil {
		return nil, err
	}
	return s.Records, nil
}

// openAttacher is what AdoptTurn needs of a store that holds changeset
// sessions: put a session.ndjson beside the changeset's changeset.json — never
// make the changeset's directory. Create cannot promise that: it MkdirAlls
// changesets/open/<id>, so adopting into a changeset Review committed a moment
// ago would fabricate an open/<id> holding only a session.ndjson, the C-118
// shape that makes Engine.Current fail for every verb after it.
type openAttacher interface {
	// attachOpen reports whether it created the file. errChangesetNotOpen when
	// changesets/open/<id>/ is not there — gone, or caught mid-rename.
	attachOpen(id string) (created bool, err error)
}

// attachOpen asks ss to attach a session to the open changeset id. A store that
// cannot say (a wrapper that hides the file store, in a test) is asked with Get
// and Create instead, which is the pre-057 ensureSession and has its C-118
// window; production stores all answer through openAttacher.
func attachOpen(ss SessionStore, id string) (bool, error) {
	if oa, ok := ss.(openAttacher); ok {
		return oa.attachOpen(id)
	}
	if _, err := ss.Get(id); err == nil {
		return false, nil
	}
	if _, err := ss.Create(id); err != nil {
		return false, err
	}
	return true, nil
}

// AdoptTurn copies the records of the conversation's turn turnID into the
// session.ndjson of changeset csID (057 D4), and reports how many it appended.
// It is how a turn that did stage keeps design D9's audit trail — "a session is
// bound to a changeset" — now that the conversation itself lives in memory and
// the changeset was opened by the staging call, not by the pane.
//
// When csID has no session yet, the earlier prose of the conversation (the
// records of every other turn, as Get's prose view would give them) is appended
// first, as Carried records: what a seeded session used to start with, so
// `lw session show` of the changeset reads as the conversation that led to it.
// Then every raw record of turn turnID is appended, tool records and Staged
// flags included. When csID already has a session (the ingest that opened the
// changeset, or an earlier adopted turn) only this turn's records are appended —
// someone else's transcript is not prefixed with a conversation it was not part
// of.
//
// A changeset that is no longer open (Review committed or rejected it while the
// turn ran) is not adopted into: the call logs "ask turn not adopted" and
// returns 0, nil, and creates nothing — in particular no changesets/open/<id>
// directory (openAttacher). ss is the store holding both the conversation and
// the changeset's session, i.e. the one NewAskSessions returned.
func AdoptTurn(ss SessionStore, convID, csID, turnID string) (int, error) {
	if turnID == "" {
		return 0, errors.New("agent: adopt turn: turn id required")
	}
	if isAskID(csID) {
		return 0, fmt.Errorf("agent: adopt turn: %s is a conversation, not a changeset", csID)
	}
	raw, err := RawRecords(ss, convID)
	if err != nil {
		return 0, fmt.Errorf("agent: adopt turn %s into %s: %w", turnID, csID, err)
	}

	created, err := attachOpen(ss, csID)
	if errors.Is(err, errChangesetNotOpen) {
		slog.Warn("ask turn not adopted", "changeset", csID, "turn", turnID, "reason", "the changeset is no longer open")
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("agent: adopt turn %s into %s: %w", turnID, csID, err)
	}

	n := 0
	if created {
		for _, r := range raw {
			if r.Turn == turnID {
				continue
			}
			if carried, ok := carryRecord(r); ok {
				if err := ss.Append(csID, carried); err != nil {
					return n, fmt.Errorf("agent: adopt turn %s into %s: %w", turnID, csID, err)
				}
				n++
			}
		}
	}
	for _, r := range raw {
		if r.Turn != turnID {
			continue
		}
		if err := ss.Append(csID, r); err != nil {
			return n, fmt.Errorf("agent: adopt turn %s into %s: %w", turnID, csID, err)
		}
		n++
	}
	return n, nil
}

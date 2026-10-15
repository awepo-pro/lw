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
// (lazyopen.go), and from its first staged op on the store TEEs the turn into
// that changeset's session.ndjson as it happens (beginTee), so design D9 — a
// session is bound to a changeset and travels with it — holds for every
// changeset that has an op, through a crash or a Review commit mid-turn.
// AdoptTurn, the copy-at-the-end 057 first shipped, is kept only as the
// fallback for a turn nothing teed.

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
	tees  map[string]*teeState // conversation id -> the changeset its running turn is mirrored into
	teed  map[string]*teedTurn // conversation id -> the changesets its last tee attempt touched (AdoptTurn's skip)
}

// teedTurn is a conversation's record of the turn it last tried to tee: which
// turn, and every changeset the tee attached to or began writing into. A turn can
// move through several (the curator rejects one mid-turn and the model's next
// staging call opens another), and AdoptTurn must skip exactly those.
type teedTurn struct {
	turn string
	ids  map[string]bool
}

// teeState is one running tee: where the turn's records are mirrored to.
type teeState struct {
	csID string
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
	return &askSessions{
		file:  file,
		now:   time.Now,
		convs: make(map[string]*askConv),
		tees:  make(map[string]*teeState),
		teed:  make(map[string]*teedTurn),
	}
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
	// The tee (beginTee): while a turn that staged is running, the record is
	// also the changeset's. fileSessions.Append finds the session in open/,
	// committed/ or rejected/, so a Review verdict mid-turn does not lose it. A
	// failure is the turn's: a file session fails its turn on a failed append too.
	if t := a.tees[id]; t != nil {
		if err := a.file.Append(t.csID, r); err != nil {
			return fmt.Errorf("agent: tee to changeset %s: %w", t.csID, err)
		}
	}
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

// turnTee is the optional capability the Loop uses to write a staging turn's
// audit trail as it happens (057 FIX2 R2): the ask conversation store has it, a
// plain file store (`lw ingest`, `lint --fix`, which write into the changeset's
// own session already) does not, and the loop skips it there.
type turnTee interface {
	// beginTee starts mirroring conversation convID into changeset csID's
	// session: the turn's records so far (preceded, when csID has no session yet,
	// by the earlier prose as Carried records), then — until endTee — every
	// record appended to the conversation. attached is false, with a nil error,
	// when csID is not open (it left changesets/open/ between the staging call
	// and now); nothing is created then.
	beginTee(convID, csID, turnID string) (attached bool, err error)
	// endTee stops the mirror; the turn is over.
	endTee(convID string)
}

var _ turnTee = (*askSessions)(nil)

// teedChecker answers AdoptTurn's question: did the tee already touch this
// changeset for this turn?
type teedChecker interface {
	wasTeed(convID, csID, turnID string) bool
}

// beginTee implements turnTee. The Loop calls it right after a successful
// staging call stages into a changeset the turn is not yet teed into — the
// turn's first, and again if the curator rejects that one and the model opens
// another — with the changeset that call staged into.
//
// Why a tee and not AdoptTurn at the turn's end: the copy happened only when the
// turn finished, so a quit, a crash, or a Review commit/reject while the model
// was still working left a changeset with live ops and no session.ndjson —
// design D9 broken exactly when someone asks "why did it propose this?". From
// the first op on, the conversation's records now reach the changeset's session
// as they are appended.
//
// The attach never makes a directory (attachOpen, as AdoptTurn): the changeset
// must be open with its changeset.json, or nothing happens.
//
// The attempt is recorded (teed) as soon as the attach succeeds and BEFORE the
// backlog is written (FIX3 L2): the attach may have created the session file and
// the backlog may fail halfway, and the pane's fallback AdoptTurn must not then
// copy the turn in again on top of what is there. A turn the tee touched is the
// tee's, finished or failed.
func (a *askSessions) beginTee(convID, csID, turnID string) (bool, error) {
	if !isAskID(convID) || isAskID(csID) {
		return false, nil
	}
	if turnID == "" {
		return false, errors.New("agent: tee turn: turn id required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.convs[convID]
	if !ok {
		return false, fmt.Errorf("%w: %s", errSessionNotFound, convID)
	}

	created, err := attachOpen(a.file, csID)
	if errors.Is(err, errChangesetNotOpen) {
		slog.Warn("ask turn not teed", "changeset", csID, "turn", turnID, "reason", "the changeset is no longer open")
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("agent: tee turn %s into %s: %w", turnID, csID, err)
	}
	tt := a.teed[convID]
	if tt == nil || tt.turn != turnID {
		tt = &teedTurn{turn: turnID, ids: make(map[string]bool)}
		a.teed[convID] = tt
	}
	tt.ids[csID] = true
	if _, err := appendBacklog(a.file, c.records, csID, turnID, created); err != nil {
		return false, fmt.Errorf("agent: tee turn %s into %s: %w", turnID, csID, err)
	}
	a.tees[convID] = &teeState{csID: csID}
	return true, nil
}

// endTee implements turnTee.
func (a *askSessions) endTee(convID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.tees, convID)
}

// wasTeed reports whether the tee touched changeset csID for turn turnID of
// conversation convID (beginTee attached it, whether or not every write
// succeeded): AdoptTurn's cue that the trail is the tee's, not to be copied
// again. It is per changeset — a turn teed into one changeset and staged into
// another the tee never reached still gets AdoptTurn's copy there.
func (a *askSessions) wasTeed(convID, csID, turnID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	tt := a.teed[convID]
	return tt != nil && tt.turn == turnID && tt.ids[csID]
}

// appendBacklog appends to changeset csID's session what a turn's trail starts
// with, from the conversation's raw records: when carry is set (csID had no
// session), the prose of every OTHER turn as Carried records — what a seeded
// session used to start with, so `lw session show` of the changeset reads as the
// conversation that led to it — and then every raw record of turn turnID, tool
// records and Staged flags included. It reports how many it appended. When csID
// already has a session (the ingest that opened the changeset, or an earlier
// turn) only the turn's own records go in: someone else's transcript is not
// prefixed with a conversation it was not part of.
func appendBacklog(ss SessionStore, raw []Record, csID, turnID string, carry bool) (int, error) {
	n := 0
	if carry {
		for _, r := range raw {
			if r.Turn == turnID {
				continue
			}
			if carried, ok := carryRecord(r); ok {
				if err := ss.Append(csID, carried); err != nil {
					return n, err
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
			return n, err
		}
		n++
	}
	return n, nil
}

// AdoptTurn copies the records of the conversation's turn turnID into the
// session.ndjson of changeset csID (057 D4), and reports how many it appended.
// Since 057 FIX2 R2 it is the FALLBACK: the Loop tees a staging turn into its
// changeset as it runs (beginTee), so for any turn the ask store teed — every
// turn a Loop ran in the TUI — AdoptTurn finds the trail written and appends
// nothing. It remains for a turn nothing teed (an agent that does not use the
// Loop's tee, as the pane's scripted test agents do), where the copy at the end
// is what the changeset gets; it carries the earlier prose first when csID has no
// session, and only the turn's records when it has one (appendBacklog).
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
	if t, ok := ss.(teedChecker); ok && t.wasTeed(convID, csID, turnID) {
		return 0, nil
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
	n, err := appendBacklog(ss, raw, csID, turnID, created)
	if err != nil {
		return n, fmt.Errorf("agent: adopt turn %s into %s: %w", turnID, csID, err)
	}
	return n, nil
}

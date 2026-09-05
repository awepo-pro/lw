package agent

// session.go implements backbone §9's file-backed SessionStore: one
// append-only session.ndjson per changeset, living BESIDE changeset.json —
// inside the changeset's own directory — rather than in a fourth directory
// of its own (backbone §9's "Contract — where session.ndjson lives", C-102,
// corrected at S5-T2 verification 2026-09-06; /PLAN.md D9 — "a session is
// bound to a changeset"). One open changeset means one live session
// (/PLAN.md D7), so this store uses the changeset id as the session id
// directly: there is no separate session-id namespace to invent, and
// Create's signature — Create(changesetID string) — takes no other
// identifier to derive one from.
//
// Because session.ndjson lives inside the changeset directory, and
// stage.Engine's Commit and Reject move that WHOLE directory —
// os.Rename(changesets/open/<id> -> changesets/{committed,rejected}/<id>)
// — the session travels with the changeset it audits, automatically,
// through both transitions. Create always writes into changesets/open/<id>
// (a changeset is only ever opened there), but Get, Append, Close and List
// must not assume "open": once a changeset is committed or rejected, its
// directory — and the session.ndjson inside it — has moved out from under
// them. Every lookup by id therefore searches all three state directories.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// errSessionNotFound is returned (wrapped) by Get, Append and Close when no
// session exists for the given id. Unexported: backbone §9 does not export
// a sentinel for it, and 00-conventions.md §2 reserves the exported surface
// for the backbone alone.
var errSessionNotFound = errors.New("agent: session not found")

// errSessionExists is returned (wrapped) by Create when a session already
// exists for the given changeset id.
var errSessionExists = errors.New("agent: session already exists")

// fileSessions is the SessionStore NewFileSessions returns. It holds no
// in-memory state: every method reads or writes the filesystem directly, so
// a session survives across the verb-per-process `lw` invocations that
// stage.Engine's own persistence (backbone §5.4 D-BA) already assumes.
type fileSessions struct {
	root string // vault root; .llmwiki lives at <root>/.llmwiki
}

// NewFileSessions returns a SessionStore that persists each session beside
// its changeset's changeset.json, at
// <root>/.llmwiki/changesets/{open,committed,rejected}/<id>/session.ndjson
// (backbone §9, C-102). root is the vault root — the same argument
// stage.OpenEngine takes, the directory containing SCHEMA.md and .llmwiki.
func NewFileSessions(root string) SessionStore {
	return &fileSessions{root: root}
}

// changesetsDir returns <root>/.llmwiki/changesets.
func (s *fileSessions) changesetsDir() string {
	return filepath.Join(s.root, ".llmwiki", "changesets")
}

// openDir returns the directory new changesets are opened into — the only
// state directory Create ever writes a session under, since
// stage.Engine.OpenChangeset never creates a changeset anywhere else.
func (s *fileSessions) openDir() string {
	return filepath.Join(s.changesetsDir(), "open")
}

// stateDirs returns the three directories a changeset — and the
// session.ndjson living inside it — can currently be found under, open
// first since it is the common case for Append (backbone §9, C-102).
func (s *fileSessions) stateDirs() []string {
	base := s.changesetsDir()
	return []string{
		filepath.Join(base, "open"),
		filepath.Join(base, "committed"),
		filepath.Join(base, "rejected"),
	}
}

// resolvePath locates id's session.ndjson under whichever state directory
// currently holds its changeset. It cannot assume "open": Commit and
// Reject rename the whole changeset directory into committed/ or
// rejected/, taking session.ndjson with it (backbone §9, C-102).
func (s *fileSessions) resolvePath(id string) (string, error) {
	for _, dir := range s.stateDirs() {
		p := filepath.Join(dir, id, "session.ndjson")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("agent: locate session %s: %w", id, err)
		}
	}
	return "", fmt.Errorf("%w: %s", errSessionNotFound, id)
}

// Create starts a new session bound to changesetID and creates its
// (initially empty) session.ndjson beside changeset.json under
// changesets/open/<changesetID> — the only place a changeset is ever
// opened, so the only place Create ever writes (backbone §9, C-102).
func (s *fileSessions) Create(changesetID string) (*Session, error) {
	if strings.TrimSpace(changesetID) == "" {
		return nil, fmt.Errorf("agent: create session: changeset id required")
	}

	dir := filepath.Join(s.openDir(), changesetID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("agent: create session %s: %w", changesetID, err)
	}

	f, err := os.OpenFile(filepath.Join(dir, "session.ndjson"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%w: %s", errSessionExists, changesetID)
		}
		return nil, fmt.Errorf("agent: create session %s: %w", changesetID, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("agent: create session %s: %w", changesetID, err)
	}

	return &Session{
		ID:          changesetID,
		ChangesetID: changesetID,
		Started:     time.Now().UTC(),
		Records:     nil,
	}, nil
}

// Get reads id's session.ndjson back into a Session, wherever its changeset
// currently sits — open, committed or rejected (backbone §9, C-102).
// Started is recovered as the earliest record's timestamp — session.ndjson
// is the only thing on disk carrying it — and is the zero Time for a
// session with no records yet.
func (s *fileSessions) Get(id string) (*Session, error) {
	p, err := s.resolvePath(id)
	if err != nil {
		return nil, err
	}

	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("agent: get session %s: %w", id, err)
	}

	recs, err := decodeRecords(b)
	if err != nil {
		return nil, fmt.Errorf("agent: get session %s: %w", id, err)
	}

	var started time.Time
	if len(recs) > 0 {
		started = recs[0].TS
	}

	return &Session{
		ID:          id,
		ChangesetID: id,
		Started:     started,
		Records:     recs,
	}, nil
}

// Append writes one Record as a new line of id's session.ndjson, wherever
// its changeset currently sits — open, committed or rejected (backbone §9,
// C-102). A record can legitimately arrive after Commit or Reject: e.g. the
// StageEv that fires once stage.Engine.Commit returns.
func (s *fileSessions) Append(id string, r Record) error {
	p, err := s.resolvePath(id)
	if err != nil {
		return err
	}

	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("agent: append to session %s: encode record: %w", id, err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("agent: append to session %s: %w", id, err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("agent: append to session %s: %w", id, err)
	}
	return f.Sync()
}

// Close archives id's session beside its changeset. session.ndjson has
// lived inside the changeset's own directory since Create — there is no
// separate "live" location to move it out of, and that directory itself
// moves (open -> committed/rejected) by construction under stage.Engine
// (backbone §9, C-102) — so archiving here means making the transcript
// durable in whichever directory currently holds it, not relocating it:
// Close fsyncs that directory so the completed file survives a crash, the
// same durability discipline journal.ndjson uses (backbone §14).
func (s *fileSessions) Close(id string) error {
	p, err := s.resolvePath(id)
	if err != nil {
		return err
	}

	d, err := os.Open(filepath.Dir(p))
	if err != nil {
		return fmt.Errorf("agent: close session %s: %w", id, err)
	}
	defer d.Close()
	_ = d.Sync() // best-effort: not every filesystem supports directory fsync

	return nil
}

// List returns every session id (changeset id) with a session.ndjson on
// disk, across all three changeset state directories — open, committed and
// rejected (backbone §9, C-102) — sorted for determinism
// (00-conventions.md §3).
func (s *fileSessions) List() ([]string, error) {
	var ids []string
	for _, dir := range s.stateDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("agent: list sessions: %w", err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "session.ndjson")); err == nil {
				ids = append(ids, e.Name())
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// decodeRecords parses one Record per non-blank line of b, the ndjson
// format backbone §14 specifies for session.ndjson.
func decodeRecords(b []byte) ([]Record, error) {
	var recs []Record
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("decode record: %w", err)
		}
		recs = append(recs, r)
	}
	return recs, nil
}

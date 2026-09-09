// journal.go implements the vault's append-only event log (backbone §5.7).
// Owned by S2-T3 from this wave; S2-T1 left only the Journal type and
// OpenJournal here so engine.go could compile (MASTER §9 D-AQ).
//
// The journal is append-only, forever. Nothing in this package — or
// anywhere else in the codebase — ever rewrites, truncates or compacts
// journal.ndjson: rejections are permanent history (/PLAN.md §4.4).
package stage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// EventKind is the kind of a journal Event.
type EventKind string

const (
	EvChangesetOpened   EventKind = "changeset_opened"
	EvOpProposed        EventKind = "op_proposed"
	EvOpAccepted        EventKind = "op_accepted"
	EvOpDropped         EventKind = "op_dropped"
	EvHunkDropped       EventKind = "hunk_dropped"
	EvHunkUndropped     EventKind = "hunk_undropped" // UndropHunk (MASTER §9 D-CL, S4-T0)
	EvCommitBegin       EventKind = "commit_begin"
	EvCommitEnd         EventKind = "commit_end"
	EvChangesetRejected EventKind = "changeset_rejected"
	EvReverted          EventKind = "reverted"
)

// Event is one record in the journal (backbone §5.7).
type Event struct {
	TS        time.Time       `json:"ts"`
	Kind      EventKind       `json:"kind"`
	Changeset string          `json:"changeset,omitempty"`
	Op        string          `json:"op,omitempty"`
	Hunk      string          `json:"hunk,omitempty"`
	Commit    string          `json:"commit,omitempty"`
	Actor     Author          `json:"actor"`
	Paths     []string        `json:"paths,omitempty"`
	Message   string          `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Filter selects a subset of journal Events for Query (backbone §5.7).
type Filter struct {
	Kinds     []EventKind
	Changeset string
	Path      string
	ActorKind string // "agent" | "human"
	Since     time.Time
	Until     time.Time
	Limit     int
}

// Journal is the vault's append-only event log at .llmwiki/journal.ndjson.
//
// Contract (backbone §5.7, MASTER §9 D-AS "no persistent handle"): a
// *Journal holds the path, not an open *os.File. Append opens
// O_APPEND|O_CREATE|O_WRONLY, writes one line, fsyncs and closes, on every
// call — which is why Journal has no Close method and Engine.Close has no
// journal step.
type Journal struct {
	path string
}

// OpenJournal opens the journal at path (.llmwiki/journal.ndjson),
// creating it if it does not already exist. It does not keep the file
// open.
func OpenJournal(path string) (*Journal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("stage: open journal %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("stage: open journal %s: %w", path, err)
	}
	return &Journal{path: path}, nil
}

// Append writes e to the journal.
//
// Contract (backbone §5.7, MASTER §9 D-AV): e is marshaled to compact JSON,
// a trailing "\n" is appended, and the whole record is written in exactly
// one Write call on an O_APPEND|O_CREATE|O_WRONLY handle opened, fsync'd
// and closed on this call alone — Journal keeps no handle open between
// calls. Writing the record in one Write is what makes concurrent
// appends, from several goroutines or two lw processes, interleave at
// line granularity instead of splicing mid-record. Append never calls
// time.Now(): e.TS is written exactly as handed to it, because the caller
// (Engine.Append, Commit) owns the injected clock (00-conventions.md §3)
// and its own tests must be able to freeze it.
func (j *Journal) Append(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("stage: journal append: %w", err)
	}
	b = append(b, '\n')

	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("stage: journal append: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(b); err != nil {
		return fmt.Errorf("stage: journal append: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("stage: journal append: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("stage: journal append: %w", err)
	}
	return nil
}

// readEvents reads every parsable line of the journal, in file order.
//
// Contract (backbone §5.7, MASTER §9 D-AV): a line that does not parse as a
// JSON Event is skipped — never an error — and the tolerance is not limited
// to a truncated final line. A crash mid-Append leaves a partial record
// with no terminating "\n"; the next Append opens O_APPEND and writes
// immediately after it, splicing both into a single unparsable line in the
// MIDDLE of the file, with valid history on either side. There is no
// warning channel for a skipped line: Journal exports none of its internals
// (§5.7 is the whole API), and nothing in internal/ writes to stderr.
func (j *Journal) readEvents() ([]Event, error) {
	f, err := os.Open(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stage: journal read: %w", err)
	}
	defer f.Close()

	var events []Event
	r := bufio.NewReader(f)
	for {
		line, readErr := r.ReadString('\n')
		trimmed := strings.TrimSuffix(line, "\n")
		if trimmed != "" {
			var e Event
			if json.Unmarshal([]byte(trimmed), &e) == nil {
				events = append(events, e)
			}
			// Unparsable line: skip silently (D-AV).
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, fmt.Errorf("stage: journal read: %w", readErr)
		}
	}
	return events, nil
}

// matches reports whether e satisfies every set field of f (backbone §5.7,
// MASTER §9 D-AU): fields AND together and a zero-valued field matches
// everything.
func (f Filter) matches(e Event) bool {
	if len(f.Kinds) > 0 {
		found := false
		for _, k := range f.Kinds {
			if e.Kind == k {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.Changeset != "" && e.Changeset != f.Changeset {
		return false
	}
	if f.Path != "" {
		found := false
		for _, p := range e.Paths {
			if p == f.Path {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.ActorKind != "" && e.Actor.Kind != f.ActorKind {
		return false
	}
	if !f.Since.IsZero() && e.TS.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && e.TS.After(f.Until) {
		return false
	}
	return true
}

// Query returns every journal Event matching f, in file order (oldest
// first).
//
// Contract (backbone §5.7, MASTER §9 D-AU): Limit selects the most recent N
// matching events and still returns them oldest-first. Limit == 0 is
// unlimited; Limit < 0 returns nothing, matching index.Search's rule
// (MASTER §9 D-AA).
func (j *Journal) Query(f Filter) ([]Event, error) {
	if f.Limit < 0 {
		return nil, nil
	}

	events, err := j.readEvents()
	if err != nil {
		return nil, err
	}

	var matched []Event
	for _, e := range events {
		if f.matches(e) {
			matched = append(matched, e)
		}
	}

	if f.Limit > 0 && len(matched) > f.Limit {
		matched = matched[len(matched)-f.Limit:]
	}
	return matched, nil
}

// Last returns the most recent n journal Events, oldest-first.
//
// Contract (backbone §5.7, MASTER §9 D-AU): Last(n) is exactly
// Query(Filter{Limit: n}) — Filter is the only surface that can select by
// kind, and D-AG rebuilds the lint baseline from "the most recent
// commit_end", so Last alone cannot serve that need; it exists as the
// unfiltered convenience case.
func (j *Journal) Last(n int) ([]Event, error) {
	return j.Query(Filter{Limit: n})
}

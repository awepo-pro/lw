package agent

import (
	"context"
	"time"
)

// This file declares backbone §9's event vocabulary and interfaces only:
// Event, TextDelta, ToolCallEv, ToolResEv, StageEv, DoneEv, ErrorEv, Agent,
// SessionStore, Record, Session and LoopConfig. It exists so that
// internal/ui (S4-T2, S4-T6, on the concurrent feat/s4-tui branch) has
// something to compile against — its Deps holds an agent.Agent field and its
// Ask pane renders agent.Event values. Only one branch may create these
// types, so this commit is cherry-picked onto feat/s4-tui rather than
// written twice (backbone §9 correction C-95, stage file C-92/C-93/D-CM).
//
// NewFileSessions, ContextBuilder, Compact, EstimateTokens, NewLoop and Loop
// belong to S5-T2 and S5-T3 — writing them here would hand those subtasks a
// file they do not own, so they are absent on purpose.
//
// Because internal/ui consumes this file from a second branch that has none
// of internal/llm, internal/tools or internal/stage yet, this file must
// import nothing but the standard library. Adding any such import here
// becomes an import cycle or a merge conflict several subtasks from now.

// Event is one item streamed out of an Agent turn. The concrete types are
// TextDelta, ToolCallEv, ToolResEv, StageEv, DoneEv and ErrorEv.
type Event interface{ isEvent() }

// TextDelta is one streamed chunk of assistant prose.
type TextDelta struct{ Text string }

// ToolCallEv fires when the model requests a tool call.
type ToolCallEv struct{ ID, Name, Args string }

// ToolResEv fires when a tool call returns.
type ToolResEv struct {
	ID, Name, Content string
	IsError           bool
}

// StageEv fires when a stage.* op lands, so the TUI can badge the STAGE
// panel live.
type StageEv struct {
	ChangesetID string
	Ops         int
}

// DoneEv fires when a turn ends.
type DoneEv struct {
	Reason string
	Rounds int
}

// ErrorEv fires when a turn ends in an error.
type ErrorEv struct{ Err error }

func (TextDelta) isEvent()  {}
func (ToolCallEv) isEvent() {}
func (ToolResEv) isEvent()  {}
func (StageEv) isEvent()    {}
func (DoneEv) isEvent()     {}
func (ErrorEv) isEvent()    {}

// Agent drives one curator turn, streaming Events to out and persisting the
// turn through its SessionStore. Loop (S5-T3) is the only implementation in
// this module; the interface is the seam that lets a different backend
// (letta or otherwise) slot in later (/.dev-notes/PLAN-v1.md D2).
type Agent interface {
	Send(ctx context.Context, sessionID, msg string, out chan<- Event) error
	Sessions() SessionStore
}

// LoopConfig bounds one Loop's resource usage.
type LoopConfig struct {
	MaxToolRounds int // default 24 (/.dev-notes/PLAN-v1.md §11.2)
	ContextTokens int // default 96000
}

// Record is one entry in a Session's append-only log.
type Record struct {
	TS      time.Time `json:"ts"`
	Role    string    `json:"role"`
	Content string    `json:"content,omitempty"`
	Tool    string    `json:"tool,omitempty"`
	Args    string    `json:"args,omitempty"`
	Result  string    `json:"result,omitempty"`
	Staged  bool      `json:"staged,omitempty"` // NEVER compacted away
}

// Session is one curator conversation, bound to a changeset.
type Session struct {
	ID          string
	ChangesetID string
	Started     time.Time
	Records     []Record
}

// SessionStore persists Sessions. NewFileSessions (S5-T2) is the only
// implementation in this module.
type SessionStore interface {
	Create(changesetID string) (*Session, error)
	Get(id string) (*Session, error)
	Append(id string, r Record) error
	Close(id string) error // archive session.ndjson beside the changeset
	List() ([]string, error)
}

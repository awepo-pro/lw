package agent

// agent.go holds the Loop type, its constructors and the accessor Send
// (loop.go) is defined against: NewLoop, the unexported newLoop it
// delegates to, and Sessions. Send itself and the round machinery live in
// loop.go (backbone §9; stage file S5-T3 item 9) — this file never imports
// internal/ui, keeping Agent the seam that lets a different backend (letta
// or otherwise) slot in later (/docs/design.md D2).

import (
	"context"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
)

// defaultMaxToolRounds and defaultContextTokens are LoopConfig's documented
// zero-value defaults (backbone §9: "default 24 (/docs/design.md §11.2)" and
// "default 96000").
const (
	defaultMaxToolRounds = 24
	defaultContextTokens = 96000
)

// streamer is the seam between Loop and whatever streams chat completions
// for it. *llm.Client satisfies it via Stream; tests inject a fake that
// replays scripted []llm.Chunk sequences with no network at all (backbone
// §9's "Contract — the fake-client seam", D-CS). It is unexported: NewLoop's
// exported parameter stays the frozen *llm.Client, so this seam changes no
// exported surface.
type streamer interface {
	Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error)
}

// Loop is the Agent that streams a turn from an LLM client, dispatches tool
// calls through a tools.Registry, and persists the turn through a
// SessionStore. Construct with NewLoop; Loop is the only implementation of
// Agent in this module (/docs/design.md D2).
type Loop struct {
	client   streamer
	tools    *tools.Registry
	sessions SessionStore
	engine   *stage.Engine
	ctxBldr  *ContextBuilder
	cfg      LoopConfig
}

var _ Agent = (*Loop)(nil)

// NewLoop returns a Loop that streams from c, dispatches tool calls through
// r, persists sessions through s, and reads stage.Engine e for both its own
// turn context and StageEv's changeset snapshot.
//
// Contract — why NewLoop takes a *stage.Engine (backbone §9, C-104/D-CQ).
// Two things this section requires cannot be built from (c, r, s, cfg)
// alone: ContextBuilder needs a *vault.Vault, which tools.Registry accepts
// at construction but never exposes again; and StageEv{ChangesetID, Ops}
// cannot be read off any tool Result, since the seven proposing tools put
// only the new op's id in Result.Data. e.Vault() is the same *vault.Vault
// cmd/lw already passes into tools.Deps, so the loop's context and the
// tools' reads cannot drift apart, and e.Current() is what supplies
// StageEv. NewLoop therefore builds its own
// NewContextBuilder(e.Vault(), r, cfg.ContextTokens) and applies
// LoopConfig's documented defaults (24 / 96000) to any zero field.
func NewLoop(c *llm.Client, r *tools.Registry, s SessionStore, e *stage.Engine, cfg LoopConfig) *Loop {
	return newLoop(c, r, s, e, cfg)
}

// newLoop is NewLoop's unexported delegate: it takes a streamer rather than
// a concrete *llm.Client so tests can inject a scripted fake with no
// network (backbone §9, D-CS). *llm.Client satisfies streamer, so NewLoop
// above is a one-line call and the exported surface never changes.
func newLoop(c streamer, r *tools.Registry, s SessionStore, e *stage.Engine, cfg LoopConfig) *Loop {
	if cfg.MaxToolRounds <= 0 {
		cfg.MaxToolRounds = defaultMaxToolRounds
	}
	if cfg.ContextTokens <= 0 {
		cfg.ContextTokens = defaultContextTokens
	}
	return &Loop{
		client:   c,
		tools:    r,
		sessions: s,
		engine:   e,
		ctxBldr:  NewContextBuilder(e.Vault(), r, cfg.ContextTokens),
		cfg:      cfg,
	}
}

// Sessions returns the SessionStore l was constructed with.
func (l *Loop) Sessions() SessionStore { return l.sessions }

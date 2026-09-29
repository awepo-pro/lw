package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Meta describes a turn in its "turn" event (038). cmd/lw fills what only it
// knows (Verb via WithVerb, Version, Model, Server, Thinking) through
// agent.LoopConfig.TraceMeta; agent.Send fills Session, MaxRounds and
// ContextTokens.
type Meta struct {
	Verb          string // ingest | ask | query | lint
	Session       string // the session id: a changeset id, or "query"
	Version       string // lw's version string
	Model         string // gen_ai.request.model
	Server        string // server.address: the base_url host, never the path or any key
	Thinking      string // the [llm] thinking setting as configured
	MaxRounds     int
	ContextTokens int
}

// NewID mints a turn id: now in UTC as YYYYMMDDTHHMMSSZ, a dash, and 4
// random lowercase hex digits — e.g. 20260928T101502Z-3f9a. Ids sort by
// time, are unique across concurrent processes in practice, and are safe
// as a directory name on every platform.
func NewID(now time.Time) string {
	var b [2]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

// verbKey is the context key WithVerb stores under.
type verbKey struct{}

// WithVerb returns ctx carrying the verb that started the turn (038):
// cmd/lw sets "ingest", "query" or "lint" around its Send call and the TUI
// ask pane sets "ask". agent.Send prefers it over LoopConfig.TraceMeta.Verb.
func WithVerb(ctx context.Context, verb string) context.Context {
	return context.WithValue(ctx, verbKey{}, verb)
}

// VerbFrom returns the verb WithVerb stored in ctx, or "".
func VerbFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(verbKey{}).(string)
	return v
}

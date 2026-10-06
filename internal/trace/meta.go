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
	Verb          string // ingest | ask | file | query | lint
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

// The verbs an entry point tags its turn with (WithVerb). One vocabulary for
// every package that names or reads a verb (054): agent.modeFromVerb sends an
// unknown verb to curator mode — every stage tool — so a mistyped literal at
// an entry point would be a silent privilege change, and a constant makes it
// a compile error instead. Untyped, so WithVerb's signature stays a plain
// string and a verb read back from a run's JSON compares against them as is.
const (
	VerbAsk    = "ask"    // the TUI ask pane's question
	VerbQuery  = "query"  // `lw query`
	VerbIngest = "ingest" // `lw ingest`, and lweval's ingest jobs
	VerbLint   = "lint"   // `lw lint --fix`
	VerbFile   = "file"   // the TUI ask pane's ctrl+s filing turn
)

// verbKey is the context key WithVerb stores under.
type verbKey struct{}

// WithVerb returns ctx carrying the verb that started the turn (038):
// cmd/lw sets VerbIngest, VerbQuery or VerbLint around its Send call and the
// TUI ask pane sets VerbAsk — or VerbFile for a ctrl+s filing turn (039).
// agent.Send prefers it over LoopConfig.TraceMeta.Verb, and, since 039, reads
// the same verb to decide how the turn is run (agent.modeFromVerb).
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

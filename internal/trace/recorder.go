package trace

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
)

// The event kinds, the value of every line's kind key.
const (
	kindTurn     = "turn"
	kindRequest  = "request"
	kindResponse = "response"
	kindTool     = "tool"
	kindElide    = "elide"
	kindRetry    = "retry"
	kindDone     = "done"
)

// eventsFile is the one file a turn's events append to, one JSON object per
// line (038 T2).
const eventsFile = "events.ndjson"

// ToolCall is one call the model asked for, kept exactly as it arrived: the
// arguments stay the raw JSON string the provider sent, so a trace replays
// what the model said, not what lw decoded it into.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Response is one streamed model answer. The timings are the client's view:
// FirstByteMS to the first byte off the wire, FirstDeltaMS to the first
// content delta, StreamMS to the last. Cut marks a stream that ended early
// (llm.ErrStreamTruncated); Error carries a terminal stream error, and both
// can coexist with whatever text and tool calls made it out before the cut.
// The zero Usage pointer means the provider sent no usage object.
type Response struct {
	Round        int        `json:"round"`
	Attempt      int        `json:"attempt"`
	Finish       string     `json:"finish"` // "stop" | "tool_calls" | "length" | ""
	FirstByteMS  int64      `json:"first_byte_ms"`
	FirstDeltaMS int64      `json:"first_delta_ms"`
	StreamMS     int64      `json:"stream_ms"`
	Reasoning    string     `json:"reasoning,omitempty"`
	Text         string     `json:"text,omitempty"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	Usage        *llm.Usage `json:"usage,omitempty"`
	Cut          bool       `json:"cut,omitempty"`
	Error        string     `json:"error,omitempty"`
}

// Tool is one tool the agent executed for the model: how long it ran, how
// big its result was, and whether it failed. The result itself never lands
// in the trace — tool output belongs to the session records, and a trace
// that duplicated it would double the disk bill for no replay value.
type Tool struct {
	Round       int    `json:"round"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	IsError     bool   `json:"is_error"`
	MS          int64  `json:"ms"`
	ResultBytes int    `json:"result_bytes"`
}

// Done closes the turn's story: why the loop stopped, how many rounds it
// ran, the terminal error if it failed, and the wall time from Start.
type Done struct {
	Reason string `json:"reason"` // stop | max_rounds | error | canceled
	Rounds int    `json:"rounds"`
	Error  string `json:"error,omitempty"`
	WallMS int64  `json:"wall_ms"`
}

// The event line shapes, in exactly the key order W5 pins — encoding/json
// marshals struct fields in declaration order, so the order here is the
// order on disk, and these are the only structs ever written.
type (
	turnEvent struct {
		Turn          string    `json:"turn"`
		TS            time.Time `json:"ts"`
		Kind          string    `json:"kind"`
		Verb          string    `json:"verb"`
		Session       string    `json:"session"`
		Version       string    `json:"lw_version"`
		Model         string    `json:"gen_ai.request.model"`
		Server        string    `json:"server.address"`
		Thinking      string    `json:"thinking"`
		MaxRounds     int       `json:"max_rounds"`
		ContextTokens int       `json:"context_tokens"`
	}
	requestEvent struct {
		Turn     string    `json:"turn"`
		TS       time.Time `json:"ts"`
		Kind     string    `json:"kind"`
		Round    int       `json:"round"`
		Attempt  int       `json:"attempt"`
		File     string    `json:"file"`
		Bytes    int       `json:"bytes"`
		SHA256   string    `json:"sha256"`
		Messages int       `json:"messages"`
		ToolDefs int       `json:"tools"`
	}
	// usageJSON is llm.Usage under its OpenTelemetry GenAI attribute names:
	// the trace speaks the wire's field vocabulary (llm.Usage's doc), and
	// llm.Usage itself carries no json tags to borrow.
	usageJSON struct {
		InputTokens     int `json:"gen_ai.usage.input_tokens"`
		OutputTokens    int `json:"gen_ai.usage.output_tokens"`
		CachedTokens    int `json:"gen_ai.usage.cache_read.input_tokens"`
		ReasoningTokens int `json:"gen_ai.usage.reasoning.output_tokens"`
	}
	responseEvent struct {
		Turn         string     `json:"turn"`
		TS           time.Time  `json:"ts"`
		Kind         string     `json:"kind"`
		Round        int        `json:"round"`
		Attempt      int        `json:"attempt"`
		Finish       string     `json:"finish"`
		FirstByteMS  int64      `json:"first_byte_ms"`
		FirstDeltaMS int64      `json:"first_delta_ms"`
		StreamMS     int64      `json:"stream_ms"`
		Reasoning    string     `json:"reasoning,omitempty"`
		Text         string     `json:"text,omitempty"`
		ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
		Usage        *usageJSON `json:"usage,omitempty"`
		Cut          bool       `json:"cut,omitempty"`
		Error        string     `json:"error,omitempty"`
	}
	toolEvent struct {
		Turn        string    `json:"turn"`
		TS          time.Time `json:"ts"`
		Kind        string    `json:"kind"`
		Round       int       `json:"round"`
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		IsError     bool      `json:"is_error"`
		MS          int64     `json:"ms"`
		ResultBytes int       `json:"result_bytes"`
	}
	elideEvent struct {
		Turn  string    `json:"turn"`
		TS    time.Time `json:"ts"`
		Kind  string    `json:"kind"`
		Round int       `json:"round"`
		Count int       `json:"count"`
		Bytes int       `json:"bytes"`
	}
	retryEvent struct {
		Turn    string    `json:"turn"`
		TS      time.Time `json:"ts"`
		Kind    string    `json:"kind"`
		Round   int       `json:"round"`
		Attempt int       `json:"attempt"`
		Reason  string    `json:"reason"`
	}
	doneEvent struct {
		Turn   string    `json:"turn"`
		TS     time.Time `json:"ts"`
		Kind   string    `json:"kind"`
		Reason string    `json:"reason"`
		Rounds int       `json:"rounds"`
		Error  string    `json:"error,omitempty"`
		WallMS int64     `json:"wall_ms"`
	}
)

// genUsage converts llm.Usage to its GenAI-named wire shape for writing;
// usageJSON.usage in read.go is the way back for reading.
func genUsage(u *llm.Usage) *usageJSON {
	if u == nil {
		return nil
	}
	return &usageJSON{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CachedTokens:    u.CachedTokens,
		ReasoningTokens: u.ReasoningTokens,
	}
}

// recKey is the context key Start stores the Recorder under.
type recKey struct{}

// Start opens a new turn trace under dir (one 0700 dir named id holding one
// 0600 events.ndjson), writes the turn event, and returns ctx carrying the
// Recorder. When keepBytes > 0 the oldest turns are pruned first (Prune),
// so a vault's traces stay inside the budget its config names. Tracing is
// observation only: any failure is one WARN and a nil Recorder — the turn
// runs untraced, never hindered (doc.go).
func Start(ctx context.Context, dir, id string, meta Meta, keepBytes int64) (context.Context, *Recorder) {
	if keepBytes > 0 {
		if _, err := Prune(dir, keepBytes, time.Now(), id); err != nil {
			return startFailed(ctx, id, err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return startFailed(ctx, id, err)
	}
	turnDir := filepath.Join(dir, id)
	// Mkdir, not MkdirAll: an existing turn dir means a same-id collision
	// (a crashed turn, or a NewID fluke), and appending to that would weld
	// two turns into one trace. Untraced beats corrupt.
	if err := os.Mkdir(turnDir, 0o700); err != nil {
		return startFailed(ctx, id, err)
	}
	f, err := os.OpenFile(filepath.Join(turnDir, eventsFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return startFailed(ctx, id, err)
	}
	r := &Recorder{dir: turnDir, id: id, f: f}
	if err := r.write(turnEvent{
		Turn: id, TS: time.Now().UTC(), Kind: kindTurn,
		Verb: meta.Verb, Session: meta.Session, Version: meta.Version,
		Model: meta.Model, Server: meta.Server, Thinking: meta.Thinking,
		MaxRounds: meta.MaxRounds, ContextTokens: meta.ContextTokens,
	}, false); err != nil {
		f.Close()
		return startFailed(ctx, id, err)
	}
	return context.WithValue(ctx, recKey{}, r), r
}

// startFailed reports a failed Start with the package's one WARN shape and
// gives the turn back an untraced ctx.
func startFailed(ctx context.Context, id string, err error) (context.Context, *Recorder) {
	slog.Warn("trace write failed", "turn", id, "err", err)
	return ctx, nil
}

// FromContext returns the Recorder Start put on ctx, or nil — every call
// outside a traced turn, and every consumer must treat nil as normal.
func FromContext(ctx context.Context) *Recorder {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(recKey{}).(*Recorder)
	return r
}

// Observer is the llm.Observer half of tracing: it hangs off llm.Config and
// hands each request body to whatever Recorder the ctx carries, so the bytes
// on disk are the bytes on the wire by construction — one interface, no
// copy, no second path for the body to drift along.
type Observer struct{}

var _ llm.Observer = Observer{}

// OnRequest records one exact request body. A ctx with no Recorder — every
// untraced turn — is a no-op, never a panic.
func (Observer) OnRequest(ctx context.Context, body []byte) {
	if r := FromContext(ctx); r != nil {
		r.Request(body)
	}
}

// Recorder appends one turn's events to its events.ndjson. Every method is
// safe on a nil receiver (an untraced turn's FromContext result), safe from
// any goroutine, and fails without failing its caller: the first write
// failure warns once and disables the Recorder, after which every method is
// a silent no-op (038 T2, W2).
type Recorder struct {
	mu       sync.Mutex
	dir      string // the turn's own directory
	id       string // the turn id, for the WARN's turn attribute
	f        *os.File
	dead     bool // a write failed, or the turn is done: all writes are no-ops
	round    int  // the BeginRequest values the next Request records under
	attempt  int
	messages int
	toolDefs int
}

// ID returns the turn id, or "" on a nil Recorder.
func (r *Recorder) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

// BeginRequest names the round and attempt the next Request belongs to,
// with the request shape it was built from. Round 0 and attempt 1 are the
// defaults when it was never called — an Observer-driven Request outside a
// round loop still lands in a well-named file.
func (r *Recorder) BeginRequest(round, attempt, messages, toolDefs int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.round, r.attempt = round, attempt
	r.messages, r.toolDefs = messages, toolDefs
}

// Request stores the exact bytes about to be POSTed — a gzip named
// req-%02d.json.gz (e.g. req-01.json.gz, attempt 1) or req-%02d-%d.json.gz
// (e.g. req-01-2.json.gz, a retry) — then writes the request event. The gzip
// is written first: an event without its file would point at nothing.
func (r *Recorder) Request(body []byte) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return
	}
	name := fmt.Sprintf("req-%02d.json.gz", r.round)
	if r.attempt > 1 {
		name = fmt.Sprintf("req-%02d-%d.json.gz", r.round, r.attempt)
	}
	if err := writeGzip(filepath.Join(r.dir, name), body); err != nil {
		r.fail(err)
		return
	}
	sum := sha256.Sum256(body)
	if err := r.write(requestEvent{
		Turn: r.id, TS: time.Now().UTC(), Kind: kindRequest,
		Round: r.round, Attempt: r.attempt, File: name,
		Bytes: len(body), SHA256: hex.EncodeToString(sum[:]),
		Messages: r.messages, ToolDefs: r.toolDefs,
	}, true); err != nil {
		r.fail(err)
	}
}

// Response records one streamed answer and its usage (W4: fsync'd — a
// response is the trace's least reproducible content).
func (r *Recorder) Response(v Response) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return
	}
	if err := r.write(responseEvent{
		Turn: r.id, TS: time.Now().UTC(), Kind: kindResponse,
		Round: v.Round, Attempt: v.Attempt, Finish: v.Finish,
		FirstByteMS: v.FirstByteMS, FirstDeltaMS: v.FirstDeltaMS, StreamMS: v.StreamMS,
		Reasoning: v.Reasoning, Text: v.Text, ToolCalls: v.ToolCalls,
		Usage: genUsage(v.Usage), Cut: v.Cut, Error: v.Error,
	}, true); err != nil {
		r.fail(err)
	}
}

// Tool records one executed tool call.
func (r *Recorder) Tool(v Tool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return
	}
	if err := r.write(toolEvent{
		Turn: r.id, TS: time.Now().UTC(), Kind: kindTool,
		Round: v.Round, ID: v.ID, Name: v.Name, IsError: v.IsError,
		MS: v.MS, ResultBytes: v.ResultBytes,
	}, false); err != nil {
		r.fail(err)
	}
}

// Elide records content the loop dropped (an elided tool result batch).
// Nothing dropped means nothing to record: count 0 is a no-op, so callers
// can report unconditionally.
func (r *Recorder) Elide(round, count, bytes int) {
	if r == nil || count == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return
	}
	if err := r.write(elideEvent{
		Turn: r.id, TS: time.Now().UTC(), Kind: kindElide,
		Round: round, Count: count, Bytes: bytes,
	}, false); err != nil {
		r.fail(err)
	}
}

// Retry records that a round's attempt failed and will be re-sent.
func (r *Recorder) Retry(round, attempt int, reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return
	}
	if err := r.write(retryEvent{
		Turn: r.id, TS: time.Now().UTC(), Kind: kindRetry,
		Round: round, Attempt: attempt, Reason: reason,
	}, false); err != nil {
		r.fail(err)
	}
}

// Done writes the done event and closes the trace: the turn is over, and
// every later call is a no-op.
func (r *Recorder) Done(v Done) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return
	}
	if err := r.write(doneEvent{
		Turn: r.id, TS: time.Now().UTC(), Kind: kindDone,
		Reason: v.Reason, Rounds: v.Rounds, Error: v.Error, WallMS: v.WallMS,
	}, true); err != nil {
		r.fail(err)
		return
	}
	r.dead = true
	r.f.Close()
}

// Close closes the trace without a done event — the shape of a turn that
// never finished (a crash, a ^C). Idempotent; a nil Recorder has nothing to
// close.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return nil
	}
	r.dead = true
	return r.f.Close()
}

// fail is the first-and-only write failure: one WARN, then dead. Called
// with mu held.
func (r *Recorder) fail(err error) {
	r.dead = true
	r.f.Close() // best effort; the failure already decided the trace's fate
	slog.Warn("trace write failed", "turn", r.id, "err", err)
}

// write appends one event as exactly one line in one Write call, fsync'd
// only for the events whose loss would hurt (W4): a request, a response, a
// done. Called with mu held.
func (r *Recorder) write(ev any, sync bool) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := r.f.Write(b); err != nil {
		return err
	}
	if sync {
		return r.f.Sync()
	}
	return nil
}

// writeGzip writes path as a gzip of exactly body, mode 0600 — the same
// protection the events file gets, since a request body is as private as
// anything else in the vault.
func writeGzip(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	z := gzip.NewWriter(f)
	_, err = z.Write(body)
	if err == nil {
		err = z.Close()
	}
	if err2 := f.Close(); err == nil {
		err = err2
	}
	return err
}

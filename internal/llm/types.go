package llm

import (
	"encoding/json"
	"time"
)

// Config configures a Client: which OpenAI-compatible endpoint to call,
// which model, and the generation and transport parameters that apply to
// every request it makes (backbone §8).
type Config struct {
	BaseURL     string
	Model       string
	APIKey      string // already resolved from env:/keyring:/literal
	Temperature float64
	MaxTokens   int
	// Thinking is the GLM thinking-mode switch (022): "off" sends
	// thinking:{"type":"disabled"} on every request, "on" sends
	// thinking:{"type":"enabled"}, and "default" or "" sends no thinking key
	// at all — the provider's own default applies. buildRequestBody
	// (client.go) owns the mapping, for Stream and Probe alike.
	Thinking string
	Timeout  time.Duration
	// StallTimeout is 026 T2's silence bound: the longest the client waits
	// with no bytes from the provider — before the response headers or
	// between body reads — before failing the turn with ErrStalled. It is
	// deliberately not more Timeout: Timeout bounds the WHOLE streaming
	// read and would cut a long answer, while StallTimeout only fires when
	// nothing arrives at all — a stream that keeps sending bytes is never
	// cut, however long it runs. 0 = no bound (today's behaviour).
	StallTimeout time.Duration
}

// Message is one chat-completions message, tagged exactly as the
// OpenAI-compatible wire format expects it — Request marshals a []Message
// straight through, no separate wire type needed (backbone §8).
type Message struct {
	Role    string `json:"role"` // system|user|assistant|tool
	Content string `json:"content,omitempty"`
	// ReasoningContent carries a thinking-mode provider's own reasoning back
	// to it on the assistant turn it produced (C-114/D-CZ, amended
	// 2026-09-09). deepseek-v4-flash rejects a follow-up request that omits
	// it, so the client must round-trip it rather than discard it.
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

// ToolCall is one function call the model asked for, always complete:
// Stream never emits one until every argument fragment has been
// accumulated — the one thing backbone §8 says must not be got wrong.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // always "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // raw JSON string
	} `json:"function"`
}

// ToolDef is one tool advertised to the model in a chat-completions
// request: the wire shape internal/tools.Registry.Definitions converts its
// registered Tools into (backbone §8).
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Request is one turn to send to Stream: the message history so far, plus
// the tools available to the model this turn (backbone §8). It carries no
// json tags itself — buildRequestBody (client.go) converts it, and
// especially its Tools, into the actual wire body, since a ToolDef has no
// OpenAI "type":"function" wrapper of its own.
type Request struct {
	Messages []Message
	Tools    []ToolDef
}

// Chunk is one increment of a streamed response: an incremental text
// delta, a complete ToolCall, a finish signal, or a terminal error. Stream
// closes its channel after the last Chunk on every exit path, and never
// sends a ToolCall that is not fully assembled (backbone §8).
type Chunk struct {
	Text string // incremental text delta
	// Reasoning is an incremental reasoning_content delta, exactly like
	// Text — a fragment, not the whole thing. Stream buffers nothing; the
	// consumer accumulates fragments and decides which assistant turn the
	// whole reasoning belongs to (C-114/D-CZ).
	Reasoning string
	ToolCall  *ToolCall // emitted once, complete, when a tool call finishes assembling
	Finish    string    // "stop" | "tool_calls" | "length" | ""
	Err       error
}

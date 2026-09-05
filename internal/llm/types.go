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
	Timeout     time.Duration
}

// Message is one chat-completions message, tagged exactly as the
// OpenAI-compatible wire format expects it — Request marshals a []Message
// straight through, no separate wire type needed (backbone §8).
type Message struct {
	Role       string     `json:"role"` // system|user|assistant|tool
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
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
	Text     string    // incremental text delta
	ToolCall *ToolCall // emitted once, complete, when a tool call finishes assembling
	Finish   string    // "stop" | "tool_calls" | "length" | ""
	Err      error
}

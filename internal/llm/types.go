package llm

import "encoding/json"

// ToolDef is one tool advertised to the model in a chat-completions
// request: the wire shape internal/tools.Registry.Definitions converts its
// registered Tools into (backbone §8).
//
// Only this type is declared here. Config, Message, ToolCall and Request
// belong to S5-T1 (backbone §6 correction C-73) — writing them now would
// hand that subtask a file it does not own.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

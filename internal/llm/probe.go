package llm

import (
	"context"
	"encoding/json"
	"time"
)

// ProbeResult reports what Probe found about an endpoint: whether it could
// be reached at all, which model answered, how long it took, and — the
// check "lw doctor" exists for — whether it actually returned a tool call
// rather than merely accepting one in the request (backbone §8).
type ProbeResult struct {
	Reachable   bool
	Model       string
	ToolCalling bool
	Latency     time.Duration
	Err         error
}

// pingToolSchema is the trivial JSON Schema Probe advertises: an object
// with no required properties, so any compliant model can call it with an
// empty arguments object.
var pingToolSchema = json.RawMessage(`{"type":"object","properties":{}}`)

// probeMaxTokens is the generation budget Probe uses for its request. A
// one-token probe is cut off before the model can emit a function call —
// measured against api.deepseek.com/v1 (model deepseek-v4-flash) with this
// probe's own prompt and tool definition, budgets of 1 and 8 tokens never
// returned a tool call at all (finish_reason "length"), 32 returned one only
// truncated mid-call, and 200 returned a clean "tool_calls" finish — so it
// reports false for a fully capable provider (backbone §8, C-101, D-CP).
const probeMaxTokens = 256

// Probe sends a small request carrying a trivial tool definition and
// reports, honestly, whether the endpoint is reachable and whether it
// actually returned a tool call — not merely whether it accepted one in the
// request. "lw doctor" needs that failure visible at config time, not
// mid-ingest (backbone §8).
func (c *Client) Probe(ctx context.Context) ProbeResult {
	start := time.Now()

	// Same endpoint and credentials as c, but with generation capped to
	// probeMaxTokens so the probe stays cheap and consistent regardless of
	// the caller's own configured MaxTokens.
	probeClient := &Client{cfg: c.cfg, httpClient: c.httpClient}
	probeClient.cfg.MaxTokens = probeMaxTokens

	req := Request{
		Messages: []Message{
			{Role: "user", Content: "Call the ping tool now; do not reply with text."},
		},
		Tools: []ToolDef{{
			Name:        "ping",
			Description: "A trivial tool used only to verify that this endpoint supports tool calling.",
			Parameters:  pingToolSchema,
		}},
	}

	ch, err := probeClient.Stream(ctx, req)
	if err != nil {
		return ProbeResult{
			Reachable: false,
			Model:     c.cfg.Model,
			Latency:   time.Since(start),
			Err:       err,
		}
	}

	var toolCalling bool
	var streamErr error
	for chunk := range ch {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		if chunk.ToolCall != nil {
			toolCalling = true
		}
	}

	return ProbeResult{
		Reachable:   true,
		Model:       c.cfg.Model,
		ToolCalling: toolCalling,
		Latency:     time.Since(start),
		Err:         streamErr,
	}
}

package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Stream POSTs req to {BaseURL}/chat/completions with "stream": true and
// returns a channel of incremental Chunks (backbone §8).
//
// Contract: tool-call argument deltas are accumulated by index, and each
// ToolCall is emitted on the channel exactly once — only when the stream
// signals it is complete, either a new index starting or a finish_reason
// arriving. A stream that ends any other way while a tool call is still
// being assembled is reported as a final Chunk{Err}, never as a truncated
// ToolCall — that silent truncation is the named regression this client
// exists to avoid. The returned channel is always closed, on every exit
// path, and ctx cancellation stops delivery promptly and still closes it.
func (c *Client) Stream(ctx context.Context, req Request) (<-chan Chunk, error) {
	httpReq, err := c.newHTTPRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	resp, headersAt, err := c.do(httpReq)
	if err != nil {
		slog.Warn("llm error", "err", err)
		return nil, fmt.Errorf("llm: request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("llm: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	out := make(chan Chunk)
	go consumeStreamTimed(ctx, resp.Body, out, headersAt)
	return out, nil
}

// wireStreamChunk is one SSE "data:" payload from an OpenAI-compatible
// chat-completions stream.
type wireStreamChunk struct {
	Choices []wireChoice `json:"choices"`
}

type wireChoice struct {
	Delta        wireDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type wireDelta struct {
	Content string `json:"content,omitempty"`
	// ReasoningContent is a thinking-mode provider's reasoning fragment,
	// alongside Content (C-114/D-CZ). consumeStream surfaces it as
	// Chunk.Reasoning exactly as Content becomes Chunk.Text.
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCallDelta `json:"tool_calls,omitempty"`
}

// wireToolCallDelta is one fragment of one tool call, identified by Index.
// Providers only send ID/Type/Name on the first fragment for a given index
// and repeat Arguments fragments on the rest — toolCallBuilder.merge treats
// every field except Arguments as "set once, keep if seen again empty".
type wireToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

// toolCallBuilder accumulates one tool call's deltas by index until the
// stream signals it is complete (see finish).
type toolCallBuilder struct {
	index     int
	id        string
	typ       string
	name      string
	arguments strings.Builder
}

// merge folds one delta fragment into b. Arguments always append; the
// other fields only overwrite when the fragment actually carries them, so a
// later fragment that omits id/type/name (the common case) does not erase
// what an earlier fragment already set.
func (b *toolCallBuilder) merge(d wireToolCallDelta) {
	if d.ID != "" {
		b.id = d.ID
	}
	if d.Type != "" {
		b.typ = d.Type
	}
	if d.Function.Name != "" {
		b.name = d.Function.Name
	}
	b.arguments.WriteString(d.Function.Arguments)
}

// finish converts the accumulated deltas into the complete ToolCall
// backbone §8 requires: emitted once, never partially assembled.
func (b *toolCallBuilder) finish() *ToolCall {
	tc := &ToolCall{ID: b.id, Type: b.typ}
	if tc.Type == "" {
		tc.Type = "function" // ToolCall.Type is "always \"function\"" (§8)
	}
	tc.Function.Name = b.name
	tc.Function.Arguments = b.arguments.String()
	return tc
}

// parseDataLine strips one SSE "data:" prefix and returns its payload, or
// ok=false for any other line — blank lines, "event:", ": comment", etc.
func parseDataLine(line string) (payload string, ok bool) {
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
}

// consumeStream reads body as an SSE chat-completions stream, decoding each
// "data:" line and pushing Chunks to out, until "data: [DONE]" arrives, ctx
// is canceled, or a read/parse error ends the stream early. It always
// closes out and body before returning, on every exit path.
//
// It is the no-timing entry: a direct caller did not go through the HTTP
// client, so there is no headers-arrived instant to measure from and the
// finish line omits first_delta_ms. Stream (above) uses consumeStreamTimed.
func consumeStream(ctx context.Context, body io.ReadCloser, out chan<- Chunk) {
	consumeStreamTimed(ctx, body, out, time.Time{})
}

// consumeStreamTimed is consumeStream with the 025-T4 timing anchor: t0 is
// the instant the HTTP response's headers were parsed (client.do), and the
// finish log line grows a first_delta_ms field — milliseconds from t0 to
// the first visible delta, i.e. the first reasoning or content chunk the
// UI can actually render. The existing "llm response" line only shows when
// headers arrived; when a provider sends headers eagerly and the first SSE
// chunk late, that line hides the user's real wait, and first_delta_ms is
// the part it hides. A stream that finishes with no visible delta at all
// (a tool-call-only turn, say) reports the -1 sentinel, so the field is
// always present on finish lines and cold-start analysis stays a flat
// parse rather than a conditional one.
func consumeStreamTimed(ctx context.Context, body io.ReadCloser, out chan<- Chunk, t0 time.Time) {
	defer close(out)
	defer body.Close()

	// emit delivers ch to out, or reports false without blocking further if
	// ctx is canceled first — so a caller that stops reading mid-stream
	// still lets this goroutine exit promptly instead of leaking on a
	// blocked send.
	emit := func(ch Chunk) bool {
		select {
		case out <- ch:
			return true
		case <-ctx.Done():
			return false
		}
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)

	var current *toolCallBuilder

	// firstDeltaAt is when the first visible delta was produced — marked
	// before emit, since emit blocks until the consumer takes the chunk.
	firstDeltaAt := time.Time{}
	markDelta := func() {
		if firstDeltaAt.IsZero() {
			firstDeltaAt = time.Now()
		}
	}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		payload, ok := parseDataLine(scanner.Text())
		if !ok || payload == "" {
			continue
		}
		if payload == "[DONE]" {
			return
		}

		var wc wireStreamChunk
		if err := json.Unmarshal([]byte(payload), &wc); err != nil {
			err = fmt.Errorf("llm: parse stream chunk: %w", err)
			slog.Warn("llm error", "err", err)
			emit(Chunk{Err: err})
			return
		}
		if len(wc.Choices) == 0 {
			continue
		}
		choice := wc.Choices[0]

		// A thinking-mode provider sends reasoning before content on the
		// same delta, so emit the reasoning fragment first (C-114/D-CZ) —
		// the consumer decides which assistant turn it belongs to.
		if choice.Delta.ReasoningContent != "" {
			markDelta()
			if !emit(Chunk{Reasoning: choice.Delta.ReasoningContent}) {
				return
			}
		}

		if choice.Delta.Content != "" {
			markDelta()
			if !emit(Chunk{Text: choice.Delta.Content}) {
				return
			}
		}

		for _, d := range choice.Delta.ToolCalls {
			if current != nil && d.Index != current.index {
				// A new index starting means the previous tool call is done.
				if !emit(Chunk{ToolCall: current.finish()}) {
					return
				}
				current = nil
			}
			if current == nil {
				current = &toolCallBuilder{index: d.Index}
			}
			current.merge(d)
		}

		if choice.FinishReason != nil && *choice.FinishReason != "" {
			if current != nil {
				if !emit(Chunk{ToolCall: current.finish()}) {
					return
				}
				current = nil
			}
			if !emit(Chunk{Finish: *choice.FinishReason}) {
				return
			}
			// The file log's stream-end line (010 contract §0), written
			// only once the finish chunk is actually delivered — extended
			// by first_delta_ms (025 T4, see consumeStreamTimed). The
			// measurement is taken at firstDeltaAt, not here: this line's
			// own delivery latency is not part of headers→first delta.
			// Tool-call chunks do not count as the first delta — the UI
			// renders them later, as a completed call.
			finishAttrs := []any{"finish", *choice.FinishReason}
			if !t0.IsZero() {
				if firstDeltaAt.IsZero() {
					finishAttrs = append(finishAttrs, "first_delta_ms", -1)
				} else {
					finishAttrs = append(finishAttrs, "first_delta_ms", firstDeltaAt.Sub(t0).Milliseconds())
				}
			}
			slog.Info("llm finish", finishAttrs...)
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("llm error", "err", err)
		emit(Chunk{Err: fmt.Errorf("llm: read stream: %w", err)})
		return
	}
	if current != nil {
		// The connection ended without [DONE] or a finish_reason while a
		// tool call was still being assembled: report it rather than
		// silently dispatching whatever fragments happened to arrive.
		err := errors.New("llm: stream ended before tool call finished assembling")
		slog.Warn("llm error", "err", err)
		emit(Chunk{Err: err})
	}
}

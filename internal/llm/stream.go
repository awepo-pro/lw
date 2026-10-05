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

// ErrStreamTruncated is wrapped by every error for a stream that ended
// before [DONE] or a finish_reason (035): a clean EOF, a cut last line or a
// read failure. A stall (ErrStalled), a cancelled context and an over-long
// line are not truncation. The agent loop recovers the round on it.
var ErrStreamTruncated = errors.New("llm: provider stream ended early")

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
	// The stall expiry (026 T2) needs a cancel of its own: firing it closes
	// the connection, which is what unblocks a body Read parked in the
	// provider's silence (stall.go). It is a child of ctx — the caller's
	// cancel still aborts everything — and consumeStreamTimed deliberately
	// keeps the parent ctx: a stall must not make emit's select see a Done
	// and race away the very Chunk{Err} that reports the stall.
	streamCtx := ctx
	cancel := context.CancelFunc(func() {})
	if c.cfg.StallTimeout > 0 {
		streamCtx, cancel = context.WithCancel(ctx)
	}

	httpReq, err := c.newHTTPRequest(streamCtx, req)
	if err != nil {
		cancel()
		return nil, err
	}

	resp, headersAt, err := c.do(httpReq)
	if err != nil {
		cancel()
		slog.WarnContext(ctx, "llm error", "err", err)
		return nil, fmt.Errorf("llm: request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// The error body carries the provider's actual complaint (a 429's
		// rate-limit text, a 400's validation message), so it is read BEFORE
		// the request cancel fires — firing it first lets the connection
		// close race this read and surface an empty error text. The read is
		// byte-bounded by LimitReader and silence-bounded by the same stall
		// wrapper the 200 path uses: LimitReader bounds bytes, never time,
		// so without the wrapper an error-status response that goes quiet
		// mid-body would park the turn here forever (F.S1).
		body := wrapStallBody(resp.Body, c.cfg.StallTimeout, cancel)
		b, rerr := io.ReadAll(io.LimitReader(body, 4096))
		body.Close()
		cancel()
		// A stall with no body bytes at all is the turn's real cause — the
		// status alone says nothing a curator can act on, so the stall
		// error travels. Bytes that did arrive make the status the real
		// information; the partial text travels with it.
		if rerr != nil && errors.Is(rerr, ErrStalled) && len(b) == 0 {
			return nil, rerr
		}
		return nil, fmt.Errorf("llm: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	out := make(chan Chunk)
	body := wrapStallBody(resp.Body, c.cfg.StallTimeout, cancel)
	go consumeStreamTimed(ctx, body, out, headersAt)
	return out, nil
}

// wireStreamChunk is one SSE "data:" payload from an OpenAI-compatible
// chat-completions stream.
type wireStreamChunk struct {
	Choices []wireChoice `json:"choices"`
	// Usage is the provider's token accounting, which rides whichever chunk
	// the provider puts it on (038 T1): the finish_reason chunk (z.ai) or a
	// trailing chunk with an empty choices array (DeepSeek/OpenAI). nil when
	// the chunk carries none.
	Usage *wireUsage `json:"usage"`
}

// wireUsage is the OpenAI-compatible usage object as it rides the wire
// (038 T1): flat prompt/completion counts plus two optional details
// objects, whose absence decodes as zero.
type wireUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
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

// countingBody counts the bytes pulled off the wire as the stream is read
// (035 T1). The truncation diagnostic reports "how far did the stream get"
// in bytes, and the only honest source is the read path itself — a wrapper
// around the body — because the alternative, reassembling the byte total
// from parsed lines, both misses a cut final line and tempts the log into
// carrying payload bytes, which it must never do. n counts bytes handed to
// the reader even when the same Read returns the error, so a body whose
// final bytes arrive together with io.EOF are still counted.
type countingBody struct {
	rc io.ReadCloser
	n  int
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.n += n
	return n, err
}

// Close just forwards. The wrapper owns no timers; when the wrapped body is
// a stallBody it keeps its own close-order contract untouched (stall.go).
func (c *countingBody) Close() error { return c.rc.Close() }

// consumeStreamTimed is consumeStream with the 025-T4 timing anchor: t0 is
// the instant the HTTP response's headers were parsed (client.do), and the
// finish log line grows a first_delta_ms field — milliseconds from t0 to
// the first visible delta. "Visible" is defined to match the ask pane's
// waiting state (025 T2's waitingVisible, which turns off on the first
// ReasoningDelta, TextDelta or ToolCallEv): the first chunk the UI can
// actually render — a reasoning fragment, a content fragment, or a
// COMPLETED tool call. Tool-call argument fragments do not count (they
// accumulate silently and the mascot keeps spinning through them), but the
// completed call does: it lands in the transcript the moment it is emitted
// (ask state.go startToolCall), so on a tool-first round — the shape a
// cold first ask can take, e.g. vault.orient — the user-visible wait ends
// there and the metric must too. The existing "llm response" line only
// shows when headers arrived; when a provider sends headers eagerly and
// the first SSE chunk late, that line hides the user's real wait, and
// first_delta_ms is the part it hides. A stream that finishes without
// ever producing a renderable chunk at all (a finish-only degenerate)
// reports the -1 sentinel, so the field is always present on finish lines
// and cold-start analysis stays a flat parse rather than a conditional
// one.
//
// 035 T1 adds the end-of-stream classification. [DONE] (R1) and a seen
// finish_reason (R2) end the stream clean; every other end — a silent EOF,
// a final line cut mid-flight, a read failure — is a truncation: one final
// Chunk{Err} wrapping ErrStreamTruncated, which the agent loop recovers the
// round on, plus exactly one content-free "llm stream truncated" WARN
// record. Ends that carry their own meaning are left alone: a stall
// (ErrStalled), an over-long line (bufio.ErrTooLong) and the caller's own
// cancellation are bounded, deliberate ends, not the provider's stream
// dying early.
func consumeStreamTimed(ctx context.Context, body io.ReadCloser, out chan<- Chunk, t0 time.Time) {
	defer close(out)

	// The truncation diagnostic's byte count comes off the read path
	// (countingBody): it must cover bytes the scanner buffered but never
	// parsed — a cut final line is exactly bytes-minus-lines.
	counter := &countingBody{rc: body}
	defer counter.Close()

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

	scanner := bufio.NewScanner(counter)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)

	var current *toolCallBuilder

	// The 035 T1 truncated record's counts, kept as the stream runs. They
	// are the whole diagnostic: a curator needs the stream's shape — how
	// many bytes and data lines arrived, how much parsed, how far the tool
	// call got — and never a payload byte, which is the user's answer.
	var (
		dataLines    int  // "data:" lines with a non-empty payload, cut line included
		parsedChunks int  // payloads that decoded as stream chunks
		emittedCalls int  // completed tool calls delivered to the consumer
		finished     bool // a non-empty finish_reason was seen
	)
	// usage holds the last usage object seen (038 T1: if several chunks
	// carry one, the LAST wins), held back from the channel until a clean
	// end so it can land after every other chunk. nil until one is seen.
	var usage *Usage

	// truncated is the one exit for every 035 T1 truncation: the error
	// wraps ErrStreamTruncated so the agent loop can recover the round with
	// errors.Is, and the log line is the single content-free record (R7)
	// replacing the plain "llm error" line these exits used to write. All
	// counters are read at call time.
	truncated := func(err error, partialLine int, scanErr error) {
		elapsed := int64(-1)
		if !t0.IsZero() {
			elapsed = time.Since(t0).Milliseconds()
		}
		se := ""
		if scanErr != nil {
			se = scanErr.Error()
		}
		slog.WarnContext(ctx, "llm stream truncated",
			"bytes", counter.n,
			"data_lines", dataLines,
			"chunks", parsedChunks,
			"tool_calls", emittedCalls,
			"assembling", current != nil,
			"partial_line_bytes", partialLine,
			"scan_err", se,
			"elapsed_ms", elapsed,
		)
		emit(Chunk{Err: err})
	}

	// tailLost reports a read failure AFTER the round's finish_reason
	// arrived (035 T1, A-035-1): the round is complete once its finish came
	// through (R2's premise), so — unlike truncated — it emits no Err chunk
	// a retry would ride on to discard a finished answer. It only records
	// the lost tail (usually just [DONE]) as one content-free WARN.
	tailLost := func(scanErr error) {
		elapsed := int64(-1)
		if !t0.IsZero() {
			elapsed = time.Since(t0).Milliseconds()
		}
		slog.WarnContext(ctx, "llm stream tail lost",
			"bytes", counter.n,
			"scan_err", scanErr.Error(),
			"elapsed_ms", elapsed,
		)
	}

	// firstDeltaAt is when the first visible delta was produced — marked
	// before emit, since emit blocks until the consumer takes the chunk.
	firstDeltaAt := time.Time{}
	markDelta := func() {
		if firstDeltaAt.IsZero() {
			firstDeltaAt = time.Now()
		}
	}

	// emitUsage delivers the held-back usage chunk on a CLEAN end (038 T1
	// U2): R1's [DONE], R2's finish-then-EOF, or A-035-1's tail lost. Every
	// other exit — a truncation, a stall, a cancellation — never emits
	// Usage, so a consumer retrying on the Err never mistakes a cut round
	// for an accounted one. When the chunk is actually delivered it writes
	// the one "llm usage" record (U3), after delivery exactly as the "llm
	// finish" line does.
	emitUsage := func() {
		if usage == nil {
			return
		}
		if !emit(Chunk{Usage: usage}) {
			return
		}
		slog.InfoContext(ctx, "llm usage",
			"input_tokens", usage.InputTokens,
			"output_tokens", usage.OutputTokens,
			"cached_tokens", usage.CachedTokens,
			"reasoning_tokens", usage.ReasoningTokens,
		)
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
		dataLines++
		if payload == "[DONE]" {
			emitUsage()
			// The SSE ended; the HTTP response behind it has not, necessarily.
			// Read it to its end before the deferred Close so net/http has the
			// connection back in the idle pool by the time the channel closes
			// (stallBody.drain) — a Close before EOF pools it asynchronously
			// and the next turn can dial a second connection first. Skipped
			// when ctx is already done: that is a cancellation, not a clean end.
			if ctx.Err() == nil {
				drainTail(body)
			}
			return
		}

		var wc wireStreamChunk
		if err := json.Unmarshal([]byte(payload), &wc); err != nil {
			// 035 T1 R4/R5: the same parse failure means different things
			// depending on what is behind the bad line. The scanner only
			// hands out an unterminated token at EOF, so when the bad line
			// was the body's last the look-ahead Scan below returns
			// immediately with no error — the provider was cut talking
			// (R4). A bad line with more body behind it is a live provider
			// speaking nonsense: the plain parse error it has always been,
			// never a truncation (R5).
			lineLen := len(scanner.Text())
			// "More body" means another line carrying a real data payload,
			// not merely another scanner token: SSE ends every line with a
			// blank separator, and the scanner hands that separator back as
			// an empty token — a garbage FINAL line arrives complete with
			// its "\n\n" and must still classify as the truncation it is,
			// not as a live provider's nonsense (035 T1 R4 vs R5).
			moreBody := false
			for scanner.Scan() {
				if p, ok := parseDataLine(scanner.Text()); ok && p != "" {
					moreBody = true
					break
				}
			}
			if moreBody {
				perr := fmt.Errorf("llm: parse stream chunk: %w", err)
				slog.WarnContext(ctx, "llm error", "err", perr)
				emit(Chunk{Err: perr})
				return
			}
			if serr := scanner.Err(); serr != nil {
				if finished {
					// The read died behind the bad line, but the round's
					// finish already arrived: complete, clean tail loss
					// (035 T1, A-035-1), not a truncation.
					tailLost(serr)
					emitUsage()
					return
				}
				// The read died right behind the bad line: the read
				// failure is the terminal fact (R6), not the parse.
				truncated(fmt.Errorf("%w: read failed after %d bytes: %w", ErrStreamTruncated, counter.n, serr), 0, serr)
				return
			}
			truncated(fmt.Errorf("%w: last line cut at %d bytes (%d bytes read)", ErrStreamTruncated, lineLen, counter.n), lineLen, nil)
			return
		}
		parsedChunks++
		// 038 T1 U1: a usage object is decoded from ANY data chunk — the
		// finish_reason chunk (z.ai) or a choices-less trailing chunk
		// (DeepSeek/OpenAI) — so it must be read BEFORE the empty-choices
		// skip below, which is exactly the trailing shape's whole payload.
		if wc.Usage != nil {
			usage = &Usage{
				InputTokens:     wc.Usage.PromptTokens,
				OutputTokens:    wc.Usage.CompletionTokens,
				CachedTokens:    wc.Usage.PromptTokensDetails.CachedTokens,
				ReasoningTokens: wc.Usage.CompletionTokensDetails.ReasoningTokens,
			}
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
				// Completed calls render on arrival (025 T2 waitingVisible),
				// so they anchor the first-delta measurement like any other
				// visible chunk.
				markDelta()
				if !emit(Chunk{ToolCall: current.finish()}) {
					return
				}
				emittedCalls++
				current = nil
			}
			if current == nil {
				current = &toolCallBuilder{index: d.Index}
			}
			current.merge(d)
		}

		if choice.FinishReason != nil && *choice.FinishReason != "" {
			finished = true
			if current != nil {
				markDelta() // the call completes here; the UI renders it now
				if !emit(Chunk{ToolCall: current.finish()}) {
					return
				}
				emittedCalls++
				current = nil
			}
			if !emit(Chunk{Finish: *choice.FinishReason}) {
				return
			}
			// The file log's stream-end line (010 contract §0), written
			// only once the finish chunk is actually delivered — extended
			// by first_delta_ms (025 T4, see consumeStreamTimed for the
			// visible-delta definition and its agreement with the ask
			// pane's waitingVisible). The measurement is taken at
			// firstDeltaAt, not here: this line's own delivery latency is
			// not part of headers→first delta.
			finishAttrs := []any{"finish", *choice.FinishReason}
			if !t0.IsZero() {
				if firstDeltaAt.IsZero() {
					finishAttrs = append(finishAttrs, "first_delta_ms", -1)
				} else {
					finishAttrs = append(finishAttrs, "first_delta_ms", firstDeltaAt.Sub(t0).Milliseconds())
				}
			}
			slog.InfoContext(ctx, "llm finish", finishAttrs...)
		}
	}

	// 035 T1: classify the end. Reaching here means the body's bytes ran
	// out without [DONE]; the one clean shape is a finish_reason already
	// seen (R2) — fakes and providers that end the body after the finish
	// chunk rely on it, and labelling it truncated would retry rounds the
	// provider actually completed.
	if err := scanner.Err(); err != nil {
		if errors.Is(err, ErrStalled) || errors.Is(err, bufio.ErrTooLong) || ctx.Err() != nil {
			// A bounded, deliberate end keeps its own meaning: the stall
			// the timeout machinery reported, an over-long line, or the
			// caller's own cancellation. None of these is the provider's
			// stream dying early (R6's untouched branch).
			slog.WarnContext(ctx, "llm error", "err", err)
			emit(Chunk{Err: fmt.Errorf("llm: read stream: %w", err)})
			return
		}
		if finished {
			// A-035-1: a read failure after the round's finish_reason is
			// the tail (usually just [DONE]) being lost off an ALREADY
			// COMPLETE round — clean end, no Err chunk, one tail-lost
			// record. A stall, an over-long line or a cancelled context
			// after a finish keeps the bounded-end branch above, unchanged.
			tailLost(err)
			emitUsage()
			return
		}
		// Any other read failure is the stream dying mid-flight (R6): the
		// cause travels wrapped, so the log keeps the underlying failure
		// and the consumer still matches the truncation.
		truncated(fmt.Errorf("%w: read failed after %d bytes: %w", ErrStreamTruncated, counter.n, err), 0, err)
		return
	}
	if current != nil {
		// The connection ended without [DONE] or a finish_reason while a
		// tool call was still being assembled: report it rather than
		// silently dispatching whatever fragments happened to arrive. The
		// partial call is never emitted.
		truncated(fmt.Errorf("%w: tool call %d was still assembling after %d bytes", ErrStreamTruncated, current.index, counter.n), 0, nil)
		return
	}
	if !finished {
		truncated(fmt.Errorf("%w: no [DONE] or finish_reason after %d bytes", ErrStreamTruncated, counter.n), 0, nil)
		return
	}
	// R2's clean end: a finish_reason seen, the body's bytes then just ran
	// out — no [DONE]. The held-back usage lands here, after the finish
	// chunk that is already delivered.
	emitUsage()
}

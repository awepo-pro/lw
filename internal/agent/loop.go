package agent

// loop.go implements backbone §9's Send: streaming one turn from the
// configured streamer, dispatching each complete tool call through the
// tools.Registry, feeding results back, and ending the turn with exactly
// one terminal event (backbone §9, "Contract — who owns out", C-105/D-CR).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/logging"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// maxConsecutiveBadCalls is the one-retry budget backbone §9 gives a
// model-correctable tool-call failure — malformed JSON (item 7) or an
// unknown tool name (item 8, D-CT) — before Send gives up on the turn. A
// call that parses and dispatches resets the counter; only two such
// failures IN A ROW end the turn.
const maxConsecutiveBadCalls = 2

// Send streams one turn of session sessionID, dispatching every tool call
// the model completes through l's registry and feeding each result back,
// until a round produces no tool call, the round cap fires, two
// consecutive tool calls fail to parse or name a known tool, ctx is
// canceled, or a setup step fails.
//
// Contract — ownership of out (backbone §9, C-105/D-CR): one channel per
// Send call. Send is synchronous, and closes out on every exit path
// (defer, below) including a setup failure before the first event and
// cancellation. Exactly one terminal event ends a turn on a normal exit:
// DoneEv{Reason, Rounds} with Reason "stop" or "max_rounds", or
// ErrorEv{Err} — never both, and never for a ctx cancellation, which simply
// stops promptly with no terminal event of its own since nothing gave up on
// the model's behalf. When Send reports ErrorEv it also returns that exact
// error, so a TUI can render the event and a CLI can check the return; a
// clean finish returns nil. Every send to out is guarded against a stalled
// consumer with select on ctx.Done() (send/fail below).
//
// Truncation (008, contract §1): a round whose finish reason is neither
// "", "stop" nor "tool_calls" and that completed no tool call is the model
// stopping before it finished its turn — not a clean stop. Such a round
// ends the turn with exactly one ErrorEv wrapping ErrTruncated and no
// DoneEv, so a caller cannot mistake an output-token cap for success (U1:
// 000006 committed an ingest of zero pages that way).
//
// A provider stream that ends before [DONE] or a finish_reason
// (llm.ErrStreamTruncated, 035) is recovered inside runRound — the round
// continues when a tool call was already dispatched, and is retried once
// otherwise (surfacing as RetryEv) — so only a twice-cut round reaches
// Send's caller as an ErrorEv wrapping llm.ErrStreamTruncated.
//
// Turn trace (038 T4): Send mints the turn id (or keeps the one the caller
// put on the ctx with logging.WithTurn) before any log line or session
// access, and stamps it on the ctx — every *Context log call below then
// carries it as turn=<id>, the same id on every Record this turn appends
// and on the trace's own events when cfg.TraceDir is set. The trace itself
// is observation only: Start's failure path warns once and hands back a nil
// Recorder, every Recorder call is nil-safe, and nothing here changes what
// runs. Exactly one done event closes the trace, whatever the exit — the
// deferred Done below is what makes "exactly one" true; cancellation wins
// over an error, since a turn that died because its ctx died is a canceled
// turn even when a later step also produced an error value.
func (l *Loop) Send(ctx context.Context, sessionID, msg string, out chan<- Event) (err error) {
	defer close(out)

	id := logging.TurnFrom(ctx)
	if id == "" {
		id = trace.NewID(time.Now())
	}
	ctx = logging.WithTurn(ctx, id)

	var rec *trace.Recorder
	started := time.Now()
	if l.cfg.TraceDir != "" {
		meta := l.cfg.TraceMeta
		meta.Session = sessionID
		meta.MaxRounds = l.cfg.MaxToolRounds
		meta.ContextTokens = l.cfg.ContextTokens
		if v := trace.VerbFrom(ctx); v != "" {
			meta.Verb = v
		}
		ctx, rec = trace.Start(ctx, l.cfg.TraceDir, id, meta, l.cfg.TraceKeepBytes)
	}

	rounds := 0
	// doneReason is set only by the two clean exits; every other exit is
	// classified in the defer above it.
	var doneReason string
	defer func() {
		d := trace.Done{Rounds: rounds, WallMS: time.Since(started).Milliseconds()}
		switch {
		case ctx.Err() != nil:
			d.Reason = "canceled"
		case err != nil:
			d.Reason, d.Error = "error", err.Error()
		default:
			d.Reason = doneReason
		}
		rec.Done(d)
	}()

	slog.InfoContext(ctx, "agent turn", "rounds_max", l.cfg.MaxToolRounds)

	sess, err := l.sessions.Get(sessionID)
	if err != nil {
		return l.fail(ctx, out, fmt.Errorf("agent: resolve session %s: %w", sessionID, err))
	}

	// Append the user Record to the durable store immediately — a crash
	// mid-turn must not lose it — but do NOT also fold it into sess's
	// in-memory Records: ContextBuilder.Build(s, userMsg) already renders
	// s.Records as history (backbone §9 part 4) and appends userMsg as
	// the turn's own message (part 5). sess.Records here is "history
	// before this turn"; mutating it first made parts 4 and 5 the same
	// message (repair-1, orchestrator probe 2026-09-06).
	userRec := Record{TS: time.Now().UTC(), Role: "user", Content: msg, Turn: id}
	if err := l.sessions.Append(sessionID, userRec); err != nil {
		return l.fail(ctx, out, fmt.Errorf("agent: append user record: %w", err))
	}

	msgs, err := l.ctxBldr.Build(sess, msg)
	if err != nil {
		return l.fail(ctx, out, fmt.Errorf("agent: build context: %w", err))
	}

	// 004 T0a (F.C1/F.C2): everything Build returned is fixed context —
	// system parts, compacted prior-session history, the user message —
	// and only messages this turn's own rounds append are ever elidable,
	// so the turn's first index is where boundContext's region starts.
	// elided remembers which indices were already replaced so no message
	// is elided twice across rounds. 004 A-004-2: pinned remembers which
	// distinct reads (canonical tool + compacted arguments) were already
	// elided, so a later round's re-read of any of them — however its
	// JSON is spelled — stays intact instead of being elided again; that
	// is what breaks the live-found elide→re-read→elide livelock. Both
	// live only for this Send, so a Loop reused for a second turn starts
	// clean.
	turnStart := len(msgs)
	elided := make(map[int]bool)
	pinned := make(map[string]bool)

	badCalls := 0

	for {
		rounds++

		// 004 T0a (F.C1): estimate the request before every Stream call,
		// round 1 included; runRound is the only Stream caller, so this
		// is the one checkpoint a round's request passes through.
		msgs = boundContext(ctx, msgs, turnStart, elided, pinned, rounds, l.cfg.ContextTokens)

		newMsgs, toolCalled, finish, err := l.runRound(ctx, sessionID, rounds, msgs, &badCalls, out)
		if err != nil {
			return err
		}
		msgs = newMsgs

		if !toolCalled {
			if truncated(finish) {
				return l.fail(ctx, out, fmt.Errorf("%w (finish_reason %q in round %d)", ErrTruncated, finish, rounds))
			}
			doneReason = "stop"
			slog.InfoContext(ctx, "agent done", "reason", "stop", "rounds", rounds)
			if !l.send(ctx, out, DoneEv{Reason: "stop", Rounds: rounds}) {
				return ctx.Err()
			}
			return nil
		}

		if rounds >= l.cfg.MaxToolRounds {
			doneReason = "max_rounds"
			slog.InfoContext(ctx, "agent done", "reason", "max_rounds", "rounds", rounds)
			if !l.send(ctx, out, DoneEv{Reason: "max_rounds", Rounds: rounds}) {
				return ctx.Err()
			}
			return nil
		}
	}
}

// runRound streams the round's response to completion — re-sending the
// identical request once if the provider cuts the stream before any tool
// call (035) — with text deltas becoming TextDelta events and each complete
// ToolCall dispatched immediately. At the round's end (the stream channel
// closes, or a cut recovers as case (A) below) everything the round
// produced — its full text, its full reasoning and every tool call, in
// stream order — is folded into exactly **one** assistant llm.Message,
// followed by that round's tool-result messages in call order. It returns
// the message list to send on the next round (msgs plus whatever this round
// appended), whether this round produced at least one tool call — the
// signal Send uses to decide whether to loop again — and the round's last
// non-empty Chunk.Finish ("" when the provider sent none), which Send
// checks for truncation (008, contract §1).
//
// Truncation record: a round that ends abnormally (truncated(finish) and no
// tool call) writes ONE assistant Record carrying its pending text, pending
// reasoning and the finish reason — even when both buffers are empty —
// before Send's ErrorEv, so the transcript shows what the cap cut off. It
// is the only path that sets Record.Finish; normal rounds never do.
//
// Cut recovery (035): a Chunk.Err wrapping llm.ErrStreamTruncated while ctx
// is still alive takes one of three plans (streamcut.go planCut). (A) A tool
// call was already dispatched MID-STREAM, so the round ends as if it had
// finished — finish "", toolCalled true — because re-sending would run that
// call a second time. (B) Nothing was dispatched, so the round's text and
// reasoning are discarded whole, one RetryEv goes out, and the identical
// request is streamed again, handled as a fresh stream of the same round.
// (C) The re-send was cut too, and the turn fails wrapping
// llm.ErrStreamTruncated. Only (B) ever re-calls Stream, so a retry can
// never re-dispatch a call the cut round already ran. round — the turn's
// 1-based round number, the same counter DoneEv.Rounds reports — feeds the
// recovery's log lines and RetryEv only.
//
// Contract — one assistant message per round (backbone §9, C-114/C-115/
// D-CZ; superseded by C-120/D-DG). A round that streamed text then a tool
// call with no reasoning used to become two assistant messages — a
// content-only one and a tool-call one — and with reasoning_content absent
// from both (omitempty), a thinking-mode provider 400s: it requires
// reasoning_content on every assistant turn once thinking mode is on, and a
// content-only assistant message can never carry it under C-115's old
// per-message rule either. C-120's live replay proved the fix: merge the
// round's text, reasoning and every tool call into one assistant message —
// the canonical OpenAI chat-completions shape — rather than repeating
// reasoning across several messages. That single message is built once,
// right here, when the round ends; dispatchToolCall and correctable no
// longer build assistant messages at all, only the matching tool-result
// message.
func (l *Loop) runRound(ctx context.Context, sessionID string, round int, msgs []llm.Message, badCalls *int, out chan<- Event) ([]llm.Message, bool, string, error) {
	var roundText strings.Builder        // every Text delta this round, in full — becomes the round's one assistant message Content.
	var pendingText strings.Builder      // text since the last flushRecord; drives session Record order only, not the wire message.
	var pendingReasoning strings.Builder // reasoning since the last flushRecord; same session-Record view as pendingText (005).
	var roundReasoning strings.Builder   // every Reasoning delta this round, in full — becomes the round's one ReasoningContent.
	var roundToolCalls []llm.ToolCall    // every ToolCall this round completes, in stream order.
	var roundToolMsgs []llm.Message      // the matching tool-result messages, in call order.
	toolCalled := false
	finish := "" // the round's last non-empty Chunk.Finish; "" when the provider sent none

	// cutRetried is this round's one-retry budget for a cut stream (035).
	// Per round, never per turn: each round's request is a fresh roll of
	// the provider's dice, and a round that used its retry starts clean.
	cutRetried := false
	// attempt is the round's 1-based attempt number: 1, then 2 after a
	// 035 (B) cut retry — the number the trace names each request file by
	// (038 T4, C-5).
	attempt := 1

	// tr is the turn's Recorder, nil for every untraced turn (TraceDir ""
	// or a failed Start); every call on it below is nil-safe. It is named
	// tr, not rec, because rec is this function's name for a session
	// Record.
	tr := trace.FromContext(ctx)

	// flushRecord writes any text and/or reasoning accumulated since the
	// last flush as ONE assistant Record, in the position it arrived —
	// before the next tool call's own Record (backbone §9 item 3: session
	// history still orders text before the tool result that followed it).
	// It never touches roundText or roundReasoning, the round-wide totals
	// that become the eventual wire message's Content and
	// ReasoningContent: history and the outbound message are two different
	// views of the same round, tracked separately.
	//
	// Contract — reasoning is persisted here (005, contract §3 note 2 as
	// amended by C-501/D-5F): it fires when EITHER buffer is non-empty, so
	// a round that thinks and then calls a tool with no text — the most
	// common tool round there is — still writes one assistant Record, with
	// empty Content and Reasoning set. Under the original text-only rule
	// that round's thinking was dropped entirely.
	flushRecord := func() error {
		if pendingText.Len() == 0 && pendingReasoning.Len() == 0 {
			return nil
		}
		text := pendingText.String()
		reasoning := pendingReasoning.String()
		pendingText.Reset()
		pendingReasoning.Reset()
		rec := Record{TS: time.Now().UTC(), Role: "assistant", Content: text, Reasoning: reasoning, Turn: logging.TurnFrom(ctx)}
		if err := l.sessions.Append(sessionID, rec); err != nil {
			return fmt.Errorf("agent: append assistant record: %w", err)
		}
		return nil
	}

	// assemble folds the round into its wire view — the one assistant
	// message carrying its full text, reasoning and every tool call,
	// followed by the tool-result messages in call order (C-120/D-DG) —
	// and is run exactly once, at the round's exit. Both exits, a closed
	// stream channel and a case-(A) cut (035), must produce the same
	// shape, which is why the fold lives here once rather than in each.
	assemble := func(msgs []llm.Message) []llm.Message {
		content := roundText.String()
		if content != "" || len(roundToolCalls) > 0 {
			msgs = append(msgs, llm.Message{
				Role:             "assistant",
				Content:          content,
				ReasoningContent: roundReasoning.String(),
				ToolCalls:        roundToolCalls,
			})
			msgs = append(msgs, roundToolMsgs...)
		}
		return msgs
	}

	// streamLoop runs one Stream attempt per iteration and loops only for a
	// case-(B) cut retry (035); every other path returns from the consume
	// loop below. The retried request is deep-equal to the cut one by
	// construction: nothing was dispatched (that is what made it a retry)
	// and msgs is only ever appended to by assemble, at the round's exit.
	// Abandoning the cut attempt's channel is safe (035): the truncation
	// Err is that stream's terminal emission — every Err path in llm's
	// consumeStream emits and then returns — so its producer goroutine is
	// already exiting, its deferred body close releasing the per-stream
	// stall cancel, and nothing is ever left blocked on an unread send.
streamLoop:
	for {
		// One attempt, traced (038 T4, A5): BeginRequest names the round,
		// attempt and request shape the Observer is about to record, then
		// t0 anchors the three timings — first_byte_ms off Stream's return,
		// first_delta_ms off the first content chunk, stream_ms off the
		// attempt's end, which is wherever this attempt's response is
		// recorded.
		defs := l.tools.Definitions()
		tr.BeginRequest(round, attempt, len(msgs), len(defs))
		t0 := time.Now()
		ch, err := l.client.Stream(ctx, llm.Request{Messages: msgs, Tools: defs})
		firstByteMS := time.Since(t0).Milliseconds()
		firstDeltaMS := int64(-1)
		var usage *llm.Usage

		// recordResponse writes this attempt's ONE response event (038 T4,
		// A6): the attempt's own reasoning, text and tool calls, its t0-
		// anchored timings, and — on the paths that end abnormally — the
		// cut flag and/or the terminal error. Every exit path below calls
		// it exactly once; usage rides whichever chunk carried it.
		recordResponse := func(cut bool, errText string) {
			tr.Response(trace.Response{
				Round: round, Attempt: attempt, Finish: finish,
				FirstByteMS: firstByteMS, FirstDeltaMS: firstDeltaMS,
				StreamMS:  time.Since(t0).Milliseconds(),
				Reasoning: roundReasoning.String(), Text: roundText.String(),
				ToolCalls: traceToolCalls(roundToolCalls),
				Usage:     usage, Cut: cut, Error: errText,
			})
		}

		if err != nil {
			// The stream never opened: the attempt's response is the error,
			// with no content (038 T4, A6).
			recordResponse(false, err.Error())
			return nil, false, "", l.fail(ctx, out, fmt.Errorf("agent: stream: %w", err))
		}

		for {
			select {
			case chunk, ok := <-ch:
				if !ok {
					// A channel that closes with ctx already dead is a
					// canceled turn, not a finished round (038 T4): the
					// provider's close and the cancel are simultaneous as
					// this select sees them, and Go's select picks freely
					// between two ready cases — reading the close as a clean
					// round would tell the trace the model stopped when the
					// caller had already hung up.
					if cerr := ctx.Err(); cerr != nil {
						recordResponse(false, cerr.Error())
						return nil, false, "", cerr
					}
					recordResponse(false, "")
					if truncated(finish) && !toolCalled {
						// Abnormal round end (008, contract §1): write the
						// round's one assistant Record with its pending text,
						// pending reasoning AND the finish reason, even when
						// both buffers are empty — flushRecord skips empty
						// buffers, and this record is the only trace of what
						// the cap cut off. Send turns the same condition into
						// the turn's ErrorEv, so the record lands before it.
						rec := Record{TS: time.Now().UTC(), Role: "assistant", Content: pendingText.String(), Reasoning: pendingReasoning.String(), Finish: finish, Turn: logging.TurnFrom(ctx)}
						if aerr := l.sessions.Append(sessionID, rec); aerr != nil {
							return nil, false, "", l.fail(ctx, out, fmt.Errorf("agent: append assistant record: %w", aerr))
						}
					} else if err := flushRecord(); err != nil {
						return nil, false, "", l.fail(ctx, out, err)
					}
					return assemble(msgs), toolCalled, finish, nil
				}
				if chunk.Err != nil {
					if errors.Is(chunk.Err, llm.ErrStreamTruncated) && ctx.Err() == nil {
						switch planCut(toolCalled, cutRetried) {
						case cutContinue:
							// (A) 035: the stream was cut after this round
							// dispatched a tool call — that call already ran,
							// so the round ends exactly as a finished one
							// would and the turn carries on with it. No
							// event: nothing is being thrown away, so there
							// is nothing for a consumer to drop.
							if ferr := flushRecord(); ferr != nil {
								recordResponse(false, ferr.Error())
								return nil, false, "", l.fail(ctx, out, ferr)
							}
							slog.WarnContext(ctx, "agent round cut", "round", round, "tool_calls", len(roundToolCalls), "action", "continue")
							// The round reads as finished but the stream did
							// not end cleanly — cut:true says so (A6).
							recordResponse(true, "")
							return assemble(msgs), true, "", nil
						case cutRetry:
							// (B) 035: nothing was dispatched, and nothing
							// was recorded — flushRecord has not fired — so
							// the round's partial text and reasoning are
							// discarded whole and the identical request is
							// sent again, once. RetryEv lets consumers drop
							// the text they already rendered. The trace keeps
							// what the loop throws away: the partial attempt
							// is recorded BEFORE the buffers reset, and the
							// retry event names the attempt that will be
							// sent (038 T4 A6, C-5).
							recordResponse(true, "")
							tr.Retry(round, attempt+1, "stream ended early")
							cutRetried = true
							attempt = 2
							roundText.Reset()
							roundReasoning.Reset()
							pendingText.Reset()
							pendingReasoning.Reset()
							if !l.send(ctx, out, RetryEv{Round: round, Attempt: 1, Reason: "stream ended early"}) {
								return nil, false, "", ctx.Err()
							}
							slog.WarnContext(ctx, "agent round cut", "round", round, "tool_calls", 0, "action", "retry", "attempt", 1)
							continue streamLoop
						case cutFail:
							// (C) 035: the re-send was cut too — the provider
							// failed the identical request twice in a row,
							// and one more silent retry would loop forever.
							slog.WarnContext(ctx, "agent round cut", "round", round, "tool_calls", 0, "action", "fail")
							recordResponse(true, chunk.Err.Error())
							return nil, false, "", l.fail(ctx, out, fmt.Errorf("agent: stream: %w", chunk.Err))
						}
					}
					recordResponse(false, chunk.Err.Error())
					return nil, false, "", l.fail(ctx, out, fmt.Errorf("agent: stream: %w", chunk.Err))
				}
				if chunk.Finish != "" {
					finish = chunk.Finish
				}
				if chunk.Usage != nil {
					usage = chunk.Usage // last one wins, as on the wire (038 T1)
				}
				if firstDeltaMS < 0 && (chunk.Reasoning != "" || chunk.Text != "" || chunk.ToolCall != nil) {
					firstDeltaMS = time.Since(t0).Milliseconds()
				}
				if chunk.Reasoning != "" {
					// 022 T2: the thinking streams out live, one ReasoningDelta
					// per non-empty chunk in stream order — always before the
					// round's text, which arrives later in the same stream.
					// Display-only: the accumulation below (and the records it
					// feeds) is unchanged.
					if !l.send(ctx, out, ReasoningDelta{Text: chunk.Reasoning}) {
						recordResponse(false, ctx.Err().Error())
						return nil, false, "", ctx.Err()
					}
					roundReasoning.WriteString(chunk.Reasoning)
					pendingReasoning.WriteString(chunk.Reasoning)
				}
				if chunk.Text != "" {
					if !l.send(ctx, out, TextDelta{Text: chunk.Text}) {
						recordResponse(false, ctx.Err().Error())
						return nil, false, "", ctx.Err()
					}
					roundText.WriteString(chunk.Text)
					pendingText.WriteString(chunk.Text)
				}
				if chunk.ToolCall != nil {
					if err := flushRecord(); err != nil {
						recordResponse(false, err.Error())
						return nil, false, "", l.fail(ctx, out, err)
					}
					toolCalled = true

					toolMsg, stop, tErr := l.dispatchToolCall(ctx, sessionID, round, *chunk.ToolCall, badCalls, out)
					if stop {
						recordResponse(false, tErr.Error())
						return nil, false, "", tErr
					}
					roundToolCalls = append(roundToolCalls, *chunk.ToolCall)
					roundToolMsgs = append(roundToolMsgs, toolMsg)
				}
			case <-ctx.Done():
				recordResponse(false, ctx.Err().Error())
				return nil, false, "", ctx.Err()
			}
		}
	}
}

// traceToolCalls converts a round's llm.ToolCalls to their trace shape: the
// id, the wire-spelled name the provider echoed (the same spelling
// dispatchToolCall canonicalizes), and the raw arguments JSON — what the
// model said, not what lw decoded it into (038 T4, A6).
func traceToolCalls(tcs []llm.ToolCall) []trace.ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]trace.ToolCall, len(tcs))
	for i, tc := range tcs {
		out[i] = trace.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}
	}
	return out
}

// dispatchToolCall handles one complete llm.ToolCall: it always emits
// ToolCallEv, then validates the arguments parse as a JSON object before
// ever calling the registry (backbone §9 item 7). A parse failure, or a
// Call error wrapping tools.ErrUnknownTool (item 8, D-CT), is
// model-correctable and shares the one-retry budget in badCalls; a second
// consecutive one aborts the turn. Every other non-nil error from Call
// aborts immediately. On success it emits ToolResEv (and StageEv when
// applicable), records the turn, resets badCalls to 0, and returns the
// tool-result message for the next round.
//
// It no longer builds an assistant message (C-120/D-DG): the round's one
// assistant message — carrying every tool call and the round's shared text
// and reasoning — is assembled once, by runRound, when the round ends.
func (l *Loop) dispatchToolCall(ctx context.Context, sessionID string, round int, tc llm.ToolCall, badCalls *int, out chan<- Event) (msg llm.Message, stop bool, err error) {
	// The provider echoes tc.Function.Name back to us in wire spelling —
	// underscores, never dots (backbone §6/§7's amendment, D-CY/C-112),
	// since Registry.Definitions advertised it that way. Canonicalize once,
	// here, and use canonical for everything below except the tool-result
	// message sent back to the provider, which must keep its own spelling
	// to match the tool_call_id/name pair it gave us.
	canonical := tools.CanonicalName(tc.Function.Name)
	// The file log's per-dispatch line (010 contract §0): the wire-spelled
	// name and the raw argument payload's byte count — never its content.
	slog.InfoContext(ctx, "agent tool call", "name", tc.Function.Name, "args_bytes", len(tc.Function.Arguments))

	// 038 T4 (A7): the tool event's ms runs from this send to the result —
	// the dispatch the model asked for, however it ends.
	t0 := time.Now()

	if !l.send(ctx, out, ToolCallEv{ID: tc.ID, Name: canonical, Args: tc.Function.Arguments}) {
		return llm.Message{}, true, ctx.Err()
	}

	args := strings.TrimSpace(tc.Function.Arguments)
	if args == "" {
		args = "{}" // legal for a no-argument tool (backbone §9 item 7)
	}
	if perr := json.Unmarshal([]byte(args), &map[string]any{}); perr != nil {
		return l.correctable(ctx, sessionID, round, tc, canonical, fmt.Errorf("malformed tool arguments: %w", perr), badCalls, out, t0)
	}

	res, callErr := l.tools.Call(ctx, canonical, json.RawMessage(args))
	if callErr != nil {
		if errors.Is(callErr, tools.ErrUnknownTool) {
			return l.correctable(ctx, sessionID, round, tc, canonical, callErr, badCalls, out, t0)
		}
		return llm.Message{}, true, l.fail(ctx, out, fmt.Errorf("agent: call %s: %w", canonical, callErr))
	}
	*badCalls = 0 // a dispatched call, whatever its result, resets the retry budget

	// 038 T4 (A7): one tool event per dispatched call, written the moment
	// the result exists — before the events and records below, so a turn
	// that dies mid-dispatch still shows the tool ran.
	trace.FromContext(ctx).Tool(trace.Tool{
		Round: round, ID: tc.ID, Name: canonical,
		IsError: res.IsError, MS: time.Since(t0).Milliseconds(),
		ResultBytes: len(res.Content),
	})

	slog.InfoContext(ctx, "agent tool result", "name", canonical, "is_error", res.IsError)

	if !l.send(ctx, out, ToolResEv{ID: tc.ID, Name: canonical, Content: res.Content, IsError: res.IsError}) {
		return llm.Message{}, true, ctx.Err()
	}

	staged := false
	if !res.IsError && strings.HasPrefix(canonical, "stage.") {
		ev, ferr := l.stageEvent()
		if ferr != nil {
			return llm.Message{}, true, l.fail(ctx, out, fmt.Errorf("agent: read current changeset: %w", ferr))
		}
		if !l.send(ctx, out, ev) {
			return llm.Message{}, true, ctx.Err()
		}
		staged = true
	}

	rec := Record{TS: time.Now().UTC(), Role: "tool", Tool: canonical, Args: args, Result: res.Content, Staged: staged, Turn: logging.TurnFrom(ctx)}
	if aerr := l.sessions.Append(sessionID, rec); aerr != nil {
		return llm.Message{}, true, l.fail(ctx, out, fmt.Errorf("agent: append tool record: %w", aerr))
	}

	return llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: res.Content}, false, nil
}

// correctable feeds cause back to the model as an IsError tool result — the
// shared handling for malformed JSON (item 7) and an unknown tool name
// (item 8, D-CT) — and aborts the turn once badCalls reaches
// maxConsecutiveBadCalls in a row. canonical is dispatchToolCall's
// already-canonicalized tc.Function.Name (D-CY), passed through rather than
// recomputed so both call sites — before and after the registry lookup —
// agree on one spelling for the ToolResEv this emits. round and t0 ride
// from dispatchToolCall's dispatch: round is the turn's 1-based round
// number the trace's tool event records under, and t0 is the moment the
// ToolCallEv send began, so the tool event's ms covers the whole attempted
// dispatch (038 T4, A7). Like dispatchToolCall,
// it returns only the tool-result message: the call still counts toward the
// round's one assistant message (runRound appends tc to roundToolCalls
// regardless of which branch produced its result), but no separate
// assistant message is built here (C-120/D-DG).
func (l *Loop) correctable(ctx context.Context, sessionID string, round int, tc llm.ToolCall, canonical string, cause error, badCalls *int, out chan<- Event, t0 time.Time) (llm.Message, bool, error) {
	*badCalls++
	content := cause.Error()

	// 038 T4 (A7): a correctable call is still a dispatched call — its
	// error result is what the model reads next, so the trace shows it like
	// any other tool, timed from the same t0 dispatchToolCall started.
	trace.FromContext(ctx).Tool(trace.Tool{
		Round: round, ID: tc.ID, Name: canonical, IsError: true,
		MS: time.Since(t0).Milliseconds(), ResultBytes: len(content),
	})

	slog.InfoContext(ctx, "agent tool result", "name", canonical, "is_error", true)
	if !l.send(ctx, out, ToolResEv{ID: tc.ID, Name: canonical, Content: content, IsError: true}) {
		return llm.Message{}, true, ctx.Err()
	}

	rec := Record{TS: time.Now().UTC(), Role: "tool", Tool: canonical, Args: tc.Function.Arguments, Result: content, Turn: logging.TurnFrom(ctx)}
	if aerr := l.sessions.Append(sessionID, rec); aerr != nil {
		return llm.Message{}, true, l.fail(ctx, out, fmt.Errorf("agent: append tool record: %w", aerr))
	}

	if *badCalls >= maxConsecutiveBadCalls {
		return llm.Message{}, true, l.fail(ctx, out, fmt.Errorf("agent: tool %s: two consecutive unusable calls: %w", canonical, cause))
	}

	return llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: content}, false, nil
}

// stageEvent reads l.engine's current changeset for a StageEv (backbone §9
// item 6): {cs.ID, len(cs.Live())} when one is open, or {"", 0} when
// e.Current() reports stage.ErrNoChangeset — which is not an error, since a
// stage.* call (e.g. stage.close) can succeed with none open. Any other
// error from Current is a genuine engine failure and is returned as such.
func (l *Loop) stageEvent() (StageEv, error) {
	cs, err := l.engine.Current()
	if err != nil {
		if errors.Is(err, stage.ErrNoChangeset) {
			return StageEv{ChangesetID: "", Ops: 0}, nil
		}
		return StageEv{}, err
	}
	return StageEv{ChangesetID: cs.ID, Ops: len(cs.Live())}, nil
}

// send delivers ev to out, guarded by ctx so a consumer that has stopped
// reading cannot wedge the loop (backbone §9, C-105). It reports whether
// the send was delivered; false means ctx ended first.
//
// The non-blocking check up front gives an already-canceled ctx priority
// over a send that could otherwise race it (Go's select has no preference
// among simultaneously ready cases): once cancellation is visible, every
// subsequent send deterministically declines rather than occasionally
// slipping one more event out, so a canceled turn unwinds the same way
// every time instead of depending on scheduler luck.
func (l *Loop) send(ctx context.Context, out chan<- Event, ev Event) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// fail reports err as the turn's terminal ErrorEv — best-effort, guarded
// the same way send is — and returns err, so Send's own return matches
// what the event carried (backbone §9, C-105).
func (l *Loop) fail(ctx context.Context, out chan<- Event, err error) error {
	l.send(ctx, out, ErrorEv{Err: err})
	return err
}

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
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
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
func (l *Loop) Send(ctx context.Context, sessionID, msg string, out chan<- Event) error {
	defer close(out)

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
	userRec := Record{TS: time.Now().UTC(), Role: "user", Content: msg}
	if err := l.sessions.Append(sessionID, userRec); err != nil {
		return l.fail(ctx, out, fmt.Errorf("agent: append user record: %w", err))
	}

	msgs, err := l.ctxBldr.Build(sess, msg)
	if err != nil {
		return l.fail(ctx, out, fmt.Errorf("agent: build context: %w", err))
	}

	badCalls := 0
	rounds := 0

	for {
		rounds++

		newMsgs, toolCalled, err := l.runRound(ctx, sessionID, msgs, &badCalls, out)
		if err != nil {
			return err
		}
		msgs = newMsgs

		if !toolCalled {
			if !l.send(ctx, out, DoneEv{Reason: "stop", Rounds: rounds}) {
				return ctx.Err()
			}
			return nil
		}

		if rounds >= l.cfg.MaxToolRounds {
			if !l.send(ctx, out, DoneEv{Reason: "max_rounds", Rounds: rounds}) {
				return ctx.Err()
			}
			return nil
		}
	}
}

// runRound streams one Stream call's response to completion: text deltas
// become TextDelta events, each complete ToolCall is dispatched immediately,
// and at the round's end (the stream channel closes) everything the round
// produced — its full text, its full reasoning and every tool call, in
// stream order — is folded into exactly **one** assistant llm.Message,
// followed by that round's tool-result messages in call order. It returns
// the message list to send on the next round (msgs plus whatever this round
// appended) and whether this round produced at least one tool call — the
// signal Send uses to decide whether to loop again.
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
// right here, when the channel closes; dispatchToolCall and correctable no
// longer build assistant messages at all, only the matching tool-result
// message.
func (l *Loop) runRound(ctx context.Context, sessionID string, msgs []llm.Message, badCalls *int, out chan<- Event) ([]llm.Message, bool, error) {
	ch, err := l.client.Stream(ctx, llm.Request{Messages: msgs, Tools: l.tools.Definitions()})
	if err != nil {
		return nil, false, l.fail(ctx, out, fmt.Errorf("agent: stream: %w", err))
	}

	var roundText strings.Builder      // every Text delta this round, in full — becomes the round's one assistant message Content.
	var pendingText strings.Builder    // text since the last flushRecord; drives session Record order only, not the wire message.
	var roundReasoning strings.Builder // every Reasoning delta this round, in full — becomes the round's one ReasoningContent.
	var roundToolCalls []llm.ToolCall  // every ToolCall this round completes, in stream order.
	var roundToolMsgs []llm.Message    // the matching tool-result messages, in call order.
	toolCalled := false

	// flushRecord writes any text accumulated since the last flush as an
	// assistant Record, in the position it arrived — before the next tool
	// call's own Record (backbone §9 item 3: session history still orders
	// text before the tool result that followed it). It never touches
	// roundText, the round-wide total that becomes the eventual wire
	// message's Content: history and the outbound message are two
	// different views of the same text now, tracked separately.
	flushRecord := func() error {
		if pendingText.Len() == 0 {
			return nil
		}
		text := pendingText.String()
		pendingText.Reset()
		rec := Record{TS: time.Now().UTC(), Role: "assistant", Content: text}
		if err := l.sessions.Append(sessionID, rec); err != nil {
			return fmt.Errorf("agent: append assistant record: %w", err)
		}
		return nil
	}

	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				if err := flushRecord(); err != nil {
					return nil, false, l.fail(ctx, out, err)
				}
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
				return msgs, toolCalled, nil
			}
			if chunk.Err != nil {
				return nil, false, l.fail(ctx, out, fmt.Errorf("agent: stream: %w", chunk.Err))
			}
			if chunk.Reasoning != "" {
				roundReasoning.WriteString(chunk.Reasoning)
			}
			if chunk.Text != "" {
				if !l.send(ctx, out, TextDelta{Text: chunk.Text}) {
					return nil, false, ctx.Err()
				}
				roundText.WriteString(chunk.Text)
				pendingText.WriteString(chunk.Text)
			}
			if chunk.ToolCall != nil {
				if err := flushRecord(); err != nil {
					return nil, false, l.fail(ctx, out, err)
				}
				toolCalled = true

				toolMsg, stop, tErr := l.dispatchToolCall(ctx, sessionID, *chunk.ToolCall, badCalls, out)
				if stop {
					return nil, false, tErr
				}
				roundToolCalls = append(roundToolCalls, *chunk.ToolCall)
				roundToolMsgs = append(roundToolMsgs, toolMsg)
			}
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
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
func (l *Loop) dispatchToolCall(ctx context.Context, sessionID string, tc llm.ToolCall, badCalls *int, out chan<- Event) (msg llm.Message, stop bool, err error) {
	// The provider echoes tc.Function.Name back to us in wire spelling —
	// underscores, never dots (backbone §6/§7's amendment, D-CY/C-112),
	// since Registry.Definitions advertised it that way. Canonicalize once,
	// here, and use canonical for everything below except the tool-result
	// message sent back to the provider, which must keep its own spelling
	// to match the tool_call_id/name pair it gave us.
	canonical := tools.CanonicalName(tc.Function.Name)

	if !l.send(ctx, out, ToolCallEv{ID: tc.ID, Name: canonical, Args: tc.Function.Arguments}) {
		return llm.Message{}, true, ctx.Err()
	}

	args := strings.TrimSpace(tc.Function.Arguments)
	if args == "" {
		args = "{}" // legal for a no-argument tool (backbone §9 item 7)
	}
	if perr := json.Unmarshal([]byte(args), &map[string]any{}); perr != nil {
		return l.correctable(ctx, sessionID, tc, canonical, fmt.Errorf("malformed tool arguments: %w", perr), badCalls, out)
	}

	res, callErr := l.tools.Call(ctx, canonical, json.RawMessage(args))
	if callErr != nil {
		if errors.Is(callErr, tools.ErrUnknownTool) {
			return l.correctable(ctx, sessionID, tc, canonical, callErr, badCalls, out)
		}
		return llm.Message{}, true, l.fail(ctx, out, fmt.Errorf("agent: call %s: %w", canonical, callErr))
	}
	*badCalls = 0 // a dispatched call, whatever its result, resets the retry budget

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

	rec := Record{TS: time.Now().UTC(), Role: "tool", Tool: canonical, Args: args, Result: res.Content, Staged: staged}
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
// agree on one spelling for the ToolResEv this emits. Like dispatchToolCall,
// it returns only the tool-result message: the call still counts toward the
// round's one assistant message (runRound appends tc to roundToolCalls
// regardless of which branch produced its result), but no separate
// assistant message is built here (C-120/D-DG).
func (l *Loop) correctable(ctx context.Context, sessionID string, tc llm.ToolCall, canonical string, cause error, badCalls *int, out chan<- Event) (llm.Message, bool, error) {
	*badCalls++
	content := cause.Error()

	if !l.send(ctx, out, ToolResEv{ID: tc.ID, Name: canonical, Content: content, IsError: true}) {
		return llm.Message{}, true, ctx.Err()
	}

	rec := Record{TS: time.Now().UTC(), Role: "tool", Tool: canonical, Args: tc.Function.Arguments, Result: content}
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

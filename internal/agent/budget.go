package agent

// budget.go implements 004 T0a's within-turn context bound (F.C1–F.C5).
// Build (context.go) compacts only prior-session history; within a turn,
// runRound appended every round's tool results with no check, so a long
// multi-round script over an uncapped read could push the outbound request
// past the provider's window. Before every Stream call the loop now
// estimates the request and, when it is over cfg.ContextTokens, elides
// earlier rounds' read results — oldest first — until the estimate fits.
// Elision touches only the in-memory message list sent on the wire: the
// session's durable Records are never involved (F.C4).

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
)

// elidedResultFormat is the exact placeholder F.C2 prescribes for an
// elided tool result: the tool's canonical dotted name, the original byte
// count, and the way back — call the tool again. Same voice as Compact's
// collapse placeholder (compact.go), which stands in for history.
const elidedResultFormat = "[elided to fit the context budget: %s result, %d bytes — call %s again if you still need it]"

// requestTokens estimates the token cost of everything that goes on the
// wire for msgs (004 T0a, F.C1): Σ EstimateTokens(m.Content) +
// EstimateTokens(m.ReasoningContent) over every message plus
// EstimateTokens(tc.Function.Arguments) over every tool call — the same
// len/4 estimator Compact budgets history by (compact.go). An estimate,
// never a measurement: F.C3's provider-is-the-authority rule exists
// because only the provider's tokenizer decides the real count.
func requestTokens(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateTokens(m.Content)
		total += EstimateTokens(m.ReasoningContent)
		for _, tc := range m.ToolCalls {
			total += EstimateTokens(tc.Function.Arguments)
		}
	}
	return total
}

// boundContext enforces cfg.ContextTokens on one round's outbound request
// (004 T0a). turnStart is the index of the first message this turn's own
// rounds appended — everything before it is Build's output (system parts,
// compacted history, the user message) and is never touched. elided maps
// indices already replaced in earlier rounds of this turn, so no message
// is ever elided twice; it lives only for the current Send. round is the
// round about to stream, for the F.C3/F.C5 log lines.
//
// Under budget (F.C1) msgs is returned as-is — byte-identical to a run
// with no budget logic. Over budget (F.C2) the current turn's tool-result
// messages are walked oldest first, skipping the most recent round's
// results (the model just produced them; they are the live end of the
// conversation) and every stage.* result (the audit trail behind the open
// changeset, the same protection Compact gives Staged records), each
// replaced with elidedResultFormat, re-estimating after each one and
// stopping at the first point the estimate fits. Role and ToolCallID are
// kept, so every tool call stays answered on the wire. Still over after
// all eligible elisions (F.C3), or over with nothing eligible (round 1),
// the request is sent anyway — the budget is an estimate, the provider is
// the authority — with one warn.
//
// When anything is elided the returned slice is a fresh copy: callers
// (and tests via fakeStreamer.Requests) keep slices from earlier rounds'
// requests, and mutating those in place would rewrite requests that were
// already sent. Under-budget and warn-only paths return msgs itself.
func boundContext(msgs []llm.Message, turnStart int, elided map[int]bool, round, budget int) []llm.Message {
	est := requestTokens(msgs)
	if est <= budget {
		return msgs
	}

	// The most recent round is the last assistant message carrying tool
	// calls (runRound assembles exactly one per round, followed by that
	// round's results), so its results are everything after it. Nothing
	// at or past that index is elidable.
	mostRecentStart := -1
	for i := len(msgs) - 1; i >= turnStart; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			mostRecentStart = i
			break
		}
	}

	var out []llm.Message // copied lazily, at the first elision
	elidedCount, elidedBytes := 0, 0
	for i := turnStart; i < len(msgs) && est > budget; i++ {
		m := msgs[i]
		if m.Role != "tool" || i >= mostRecentStart || elided[i] {
			continue
		}
		// dispatchToolCall and correctable set Name to the wire spelling
		// of the matching assistant ToolCall's own Function.Name, so
		// canonicalizing it here is exactly the canonical dotted name
		// F.C2 asks the placeholder to carry.
		canonical := tools.CanonicalName(m.Name)
		if strings.HasPrefix(canonical, "stage.") {
			continue
		}
		if out == nil {
			out = make([]llm.Message, len(msgs))
			copy(out, msgs)
		}
		out[i].Content = fmt.Sprintf(elidedResultFormat, canonical, len(m.Content), canonical)
		elided[i] = true
		elidedCount++
		elidedBytes += len(m.Content)
		est = requestTokens(out) // F.C2: re-estimate after each elision
	}

	if elidedCount > 0 {
		slog.Info("context elided", "round", round, "messages", elidedCount, "bytes", elidedBytes, "estimate", est, "budget", budget)
	}
	if est > budget {
		slog.Warn("context over budget", "estimate", est, "budget", budget, "round", round)
	}
	if out == nil {
		return msgs
	}
	return out
}

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
//
// 004 A-004-2: elision alone had no forward-progress guarantee. Found
// live (context_tokens=6000): the placeholder invites a re-read, the next
// round elided the re-read, and the model alternated two pages until
// max_rounds without ever answering. Hence second-chance pinning — a call
// matching one whose result was already elided this turn is pinned, never
// elided again (pinned and callSignature below) — so each distinct read
// is elided at most once per turn and the cycle cannot form.
//
// 041: a staged page's text is dead weight the moment the model has emitted
// it — it is in the open changeset, and wiki.get reads it back — yet the
// assistant tool_calls that carried it rode every later round, and the
// estimate counted it, so when the budget bound it was the source text of
// raw.get results that was pushed out first to keep a page already written
// (a 21 KB ingest request grew to 391 KB). stubStagedContent is the one
// replacement both seams use: Build (context.go) applies it to every replayed
// staged record, and boundContext's phase 0 applies it to this turn's older
// stage calls before any read result is elided. It buys cost and context
// room, not latency: first-byte time was measured flat from 20 to 390 KB.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// callSignature is A-004-2's identity of one distinct read: the canonical
// tool name plus the call's raw Function.Arguments after json.Compact —
// the whitespace and key spacing a model may vary between rounds must not
// make the same read look like two — falling back to the raw string when
// the arguments do not compact (unparseable JSON cannot match anything
// structured either way). Two calls with the same signature are the same
// read: eliding one pins the other for the rest of the turn.
func callSignature(callName, args string) string {
	canonical := tools.CanonicalName(callName)
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(args)); err != nil {
		return canonical + "\x00" + args
	}
	return canonical + "\x00" + buf.String()
}

// elidedResultFormat is the exact placeholder F.C2 prescribes for an
// elided tool result: the tool's canonical dotted name, the original byte
// count, and the way back — call the tool again. Same voice as Compact's
// collapse placeholder (compact.go), which stands in for history.
const elidedResultFormat = "[elided to fit the context budget: %s result, %d bytes — call %s again if you still need it]"

// stagedStubFormat is the text that stands in for one staged page's text
// (041, A-041-4): the byte count it replaced, and the way to see the page —
// wiki.get returns the page's CURRENT staged or committed text. It says
// "current", not "the text you sent", on purpose: by the time a stub is read
// the page may have been patched, renamed over or merged, and promising the
// model its own old bytes back would be a lie it could act on. The wording
// is frozen and byte-exact; the em dash is deliberate, as in
// elidedResultFormat. It begins "[elided:", which stage.create_page and
// stage.patch_page refuse as page text (A-041-5), so a model that copies it
// into a call is told so instead of writing a placeholder into a page.
const stagedStubFormat = "[elided: %d bytes of page text sent in this call — wiki.get returns the page's current staged or committed text]"

// stagedStubMin is the size above which a page text is stubbed: a value of
// exactly this many bytes stays, one byte more goes. Short values — an
// append_section's one-line link, a remove_section's "" — cost less than the
// stub and carry the edit itself, so stubbing them would only hide it.
const stagedStubMin = 512

// stagedContentKeys names, per canonical staged tool, the string arguments
// that carry a page's text (041, A-041-1). stage.create_page's schema calls it
// `body` (stage_page.go stageCreatePageSchema) — the frozen block said
// `content`, which the schema does not have, so a stub keyed on `content`
// alone would leave every real create_page body on the wire. `content` stays
// listed for it as well: it is a string a model may send under that name, and
// the frozen block pins it. stage.patch_page's text is `content`. Slices, not
// a map of sets, so the order the keys are visited in never depends on map
// iteration.
var stagedContentKeys = map[string][]string{
	"stage.create_page": {"body", "content"},
	"stage.patch_page":  {"content"},
}

// stubStagedContent returns args with the page text of the staged tool call
// canonical (a canonical dotted name, "stage.create_page") replaced by the
// stub, the number of bytes it replaced, and whether anything was replaced
// (041). It is the one place both seams — Build's history replay and
// boundContext's phase 0 — decide what is dead weight.
//
// It decides what to stub, never whether the call may be stubbed: that the
// call actually staged is the caller's gate (A-041-3). A call the tool refused
// staged nothing, so its text is not in the changeset, wiki.get cannot return
// it, and the call's arguments are the only copy the model has to correct and
// resend — a refused create_page or a 043 shrink refusal's replace_section
// must keep its text. Build checks Record.Staged and boundContext the loop's
// staged-call set before they call this.
//
// Only a JSON object is touched, and only a string value longer than
// stagedStubMin under one of the tool's stagedContentKeys. Anything else — an
// unknown tool, arguments that are not an object, a text of 512 bytes or less, a
// value that is not a string — comes back byte for byte as it came in, with
// ok false: the common case never re-encodes, so the model's own spelling of a
// short call is what replays. When something is stubbed the object is
// re-encoded through a map, so its keys come out sorted: the bytes depend only
// on the arguments, never on the order the model wrote them or on map
// iteration, and the provider's prefix cache keeps hitting across turns. Other
// values are carried as json.RawMessage, so a number or a nested array
// survives the round trip exactly rather than through float64.
//
// n counts bytes — len of the decoded string, not runes — summed over every key
// stubbed in this call.
func stubStagedContent(canonical, args string) (stubbed string, n int, ok bool) {
	keys, staged := stagedContentKeys[canonical]
	if !staged {
		return args, 0, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &obj); err != nil || obj == nil {
		return args, 0, false
	}
	for _, key := range keys {
		raw, present := obj[key]
		if !present {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil || len(text) <= stagedStubMin {
			continue
		}
		stub, err := json.Marshal(fmt.Sprintf(stagedStubFormat, len(text)))
		if err != nil {
			return args, 0, false // unreachable: a string always marshals
		}
		obj[key] = stub
		n += len(text)
	}
	if n == 0 {
		return args, 0, false
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return args, 0, false // unreachable: every value is already valid JSON
	}
	return string(out), n, true
}

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
// is ever elided twice; pinned maps the signatures (callSignature) of
// reads already elided this turn, so no re-read of them ever is (A-004-2);
// both live only for the current Send. staged holds the ids of this turn's
// tool calls that actually staged an op (dispatchToolCall writes it, in the
// one place that also sets Record.Staged: a stage.* call that did not come
// back IsError) — the only calls phase 0 below may stub, A-041-3; a nil set
// stubs nothing. round is the round about to stream, for the F.C3/F.C5 log
// lines.
//
// Under budget (F.C1) msgs is returned as-is — byte-identical to a run
// with no budget logic; since 041 that holds for staged page text too, which
// is never rewritten inside a request that fits. Over budget, 041's phase 0
// runs first: this turn's assistant tool calls are walked oldest first,
// skipping the most recent round's, and each stage.create_page /
// stage.patch_page call that staged (A-041-3) and whose page text is longer
// than 512 bytes has it replaced by stubStagedContent's stub, re-estimating
// after each call and stopping at the first point the request fits. A page the
// model already staged is in the open changeset and wiki.get reads it back; a
// source it has only read once is not recoverable from anywhere but a re-read,
// so the stale page gives way before the evidence does. A call the tool
// refused (a validation error, a 043 shrink refusal) staged nothing: its text
// is the only copy there is, the model is about to correct and resend it, and
// it is never stubbed. Every stub counts as one message in the
// "context elided" line and the trace's elide event, and its bytes are the
// page text it replaced. Only if the stubs are not enough does the F.C2 phase
// run, unchanged: over budget (F.C2) the current turn's tool-result
// messages are walked oldest first, skipping the most recent round's
// results (the model just produced them; they are the live end of the
// conversation), every stage.* result (the audit trail behind the open
// changeset, the same protection Compact gives Staged records), and every
// PINNED result — a call whose signature matches one already elided this
// turn (A-004-2) — each eligible visit replaced with elidedResultFormat,
// re-estimating after each one and stopping at the first point the
// estimate fits. Role and ToolCallID are kept, so every tool call stays
// answered on the wire. Still over after all eligible elisions (F.C3), or
// over with nothing eligible (round 1), the request is sent anyway — the
// budget is an estimate, the provider is the authority — with one warn.
//
// When anything is elided or stubbed the returned slice is a fresh copy:
// callers (and tests via fakeStreamer.Requests) keep slices from earlier
// rounds' requests, and mutating those in place would rewrite requests that
// were already sent. A stubbed message also gets a fresh ToolCalls array —
// copying the message slice copies each message's ToolCalls slice header, not
// the array behind it, so rewriting an argument through the copy would still
// rewrite the earlier request's. Under-budget and warn-only paths return msgs
// itself. The loop keeps the returned slice as its working list, so a stub
// persists into later rounds (and, being under 512 bytes, is never stubbed
// twice).
func boundContext(ctx context.Context, msgs []llm.Message, turnStart int, elided map[int]bool, pinned, staged map[string]bool, round, budget int) []llm.Message {
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

	// A-004-2: attribute every assistant tool call to its read's
	// signature, so the walk below can recognize a re-read of an
	// already-elided page — however the model spells its JSON this time —
	// by the result's own ToolCallID. Only over-budget rounds pay for the
	// map; the under-budget path above stays allocation-free.
	callSigs := make(map[string]string)
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			callSigs[tc.ID] = callSignature(tc.Function.Name, tc.Function.Arguments)
		}
	}

	var out []llm.Message // copied lazily, at the first stub or elision
	elidedCount, elidedBytes := 0, 0

	// 041 phase 0: stub the page text of this turn's older stage calls that
	// staged (A-041-3: staged[id], set by dispatchToolCall only for a call
	// that did not come back IsError) before any read result is elided. A
	// refused call is skipped here, not stubbed. mostRecentStart bounds the walk exactly as it
	// bounds the read-result walk: the live round's calls are the model's own
	// just-produced output and are never touched; with no tool-calling
	// assistant message in the turn it is -1 and nothing is walked. cloned
	// remembers the messages whose ToolCalls array is already this call's own
	// copy, so a round with several stage calls clones it once.
	cloned := make(map[int]bool)
	for i := turnStart; i < mostRecentStart && est > budget; i++ {
		m := msgs[i]
		if m.Role != "assistant" {
			continue
		}
		for j, tc := range m.ToolCalls {
			if !staged[tc.ID] {
				continue // A-041-3: refused or not a staged call — its text is not in the changeset
			}
			stubbed, n, ok := stubStagedContent(tools.CanonicalName(tc.Function.Name), tc.Function.Arguments)
			if !ok {
				continue
			}
			if out == nil {
				out = make([]llm.Message, len(msgs))
				copy(out, msgs)
			}
			if !cloned[i] {
				out[i].ToolCalls = append([]llm.ToolCall(nil), m.ToolCalls...)
				cloned[i] = true
			}
			out[i].ToolCalls[j].Function.Arguments = stubbed
			elidedCount++
			elidedBytes += n
			est = requestTokens(out) // re-estimate after each stub, as F.C2 does per elision
			if est <= budget {
				break
			}
		}
	}

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
		// A-004-2 second-chance pinning: a call whose signature matches
		// one already elided this turn is pinned — its result stays on
		// the wire for the rest of the turn, so the placeholder's
		// invitation to re-read cannot loop: the model can see what it
		// asked for. Skipping may leave the request over budget; F.C3's
		// provider-is-the-authority rule covers exactly that.
		sig, ok := callSigs[m.ToolCallID]
		if !ok {
			// Unreachable for messages runRound appends — every tool
			// result trails its own call — but a result whose call
			// cannot be found must not become silently re-elidable:
			// attribute it to its message name alone.
			sig = callSignature(m.Name, "")
		}
		if pinned[sig] {
			continue
		}
		if out == nil {
			out = make([]llm.Message, len(msgs))
			copy(out, msgs)
		}
		out[i].Content = fmt.Sprintf(elidedResultFormat, canonical, len(m.Content), canonical)
		elided[i] = true
		pinned[sig] = true
		elidedCount++
		elidedBytes += len(m.Content)
		est = requestTokens(out) // F.C2: re-estimate after each elision
	}

	if elidedCount > 0 {
		slog.InfoContext(ctx, "context elided", "round", round, "messages", elidedCount, "bytes", elidedBytes, "estimate", est, "budget", budget)
		// 038 T4 (A8): the same facts the log line carries, as an elide
		// event on the turn's trace. Elide is a no-op at count 0, so this
		// reports only when something was dropped.
		trace.FromContext(ctx).Elide(round, elidedCount, elidedBytes)
	}
	if est > budget {
		slog.WarnContext(ctx, "context over budget", "estimate", est, "budget", budget, "round", round)
	}
	if out == nil {
		return msgs
	}
	return out
}

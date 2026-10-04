package agent

// context.go implements backbone §9's ContextBuilder: assembling one turn's
// full message list in the exact five-part order /docs/design.md §11.3 specifies.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/vault"
)

// ContextBuilder assembles the five-part turn context backbone §9 and
// /docs/design.md §11.3 specify, in the one order that is ever legal: the system
// prompt, curator-memory.md, the orientation digest, compacted session
// history, then the user message.
type ContextBuilder struct {
	v      *vault.Vault
	r      *tools.Registry
	budget int

	mu     sync.Mutex
	orient map[string]orientEntry // session id -> its last-fetched digest
}

// orientEntry caches one session's orientation digest against the
// index.md hash that produced it, so Build can tell "index.md changed"
// apart from "nothing changed" without re-fetching every turn.
type orientEntry struct {
	digest    string
	indexHash string
}

// NewContextBuilder returns a ContextBuilder that reads pages through v,
// fetches the orientation digest by calling "vault.orient" through r, and
// compacts session history to fit budget estimated tokens (backbone §9).
func NewContextBuilder(v *vault.Vault, r *tools.Registry, budget int) *ContextBuilder {
	return &ContextBuilder{
		v:      v,
		r:      r,
		budget: budget,
		orient: make(map[string]orientEntry),
	}
}

// Build assembles one turn's messages for session s plus the new userMsg,
// in backbone §9's exact order (/docs/design.md §11.3):
//
//  1. the system prompt (prompt.go — its web-lookup paragraphs only when
//     this builder's registry offers web.search, 012 D-12B);
//  2. curator-memory.md, verbatim;
//  3. the orientation digest — vault.orient's Result.Content, injected once
//     per session and refreshed only when index.md's content changes;
//  4. session history, compacted (Compact) to fit whatever budget remains —
//     prose records as plain messages, tool records as assistant tool_calls +
//     tool result pairs (046, appendHistoryPair);
//  5. the user message.
func (b *ContextBuilder) Build(s *Session, userMsg string) ([]llm.Message, error) {
	memory, err := b.v.Read("curator-memory.md")
	if err != nil {
		return nil, fmt.Errorf("agent: build context: read curator-memory.md: %w", err)
	}

	digest, err := b.orientDigest(s)
	if err != nil {
		return nil, fmt.Errorf("agent: build context: %w", err)
	}

	// The registry is the truth about what the vault can do: the prompt
	// promises web.search only when the registry itself offers the verb
	// (012 contract §1) — never a constructor parameter, never a stored
	// field.
	_, hasSearch := b.r.Get("web.search")
	msgs := []llm.Message{
		{Role: "system", Content: systemPromptFor(hasSearch)},
		{Role: "system", Content: string(memory)},
		{Role: "system", Content: digest},
	}

	used := 0
	for _, m := range msgs {
		used += EstimateTokens(m.Content)
	}
	used += EstimateTokens(userMsg)
	remaining := b.budget - used
	if remaining < 0 {
		remaining = 0
	}

	hist := Compact(s.Records, remaining)
	pairs := 0 // history pairs emitted so far: the 1-based k of the next hist_<k> id
	for i := 0; i < len(hist); i++ {
		rec := hist[i]
		if rec.Tool != "" {
			// 046: a tool record is a spec-valid assistant tool_calls +
			// tool result pair, never a bare role:"tool" message — see
			// appendHistoryPair for the shape and why. It may consume the
			// next record too (the split shape).
			pairs++
			var used int
			msgs, used = appendHistoryPair(msgs, hist, i, pairs)
			i += used - 1
			continue
		}
		// D-5G: a record with no tool and no content — since 005, a
		// reasoning-only assistant record — would become an empty
		// assistant message, which some providers reject outright. Skip
		// it. The persisted thinking is for the human reading
		// `lw session show`, not for the wire. Tool records never match
		// this guard (their Tool is non-empty, and they are handled
		// above), so a staged op's audit trail cannot be dropped here.
		if rec.Content == "" {
			continue
		}
		msgs = append(msgs, recordToMessage(rec))
	}

	msgs = append(msgs, llm.Message{Role: "user", Content: userMsg})
	return msgs, nil
}

// orientDigest returns session s's cached orientation digest, fetching a
// fresh one through the "vault.orient" tool the first time s is seen and
// whenever index.md's content has changed since the last fetch.
func (b *ContextBuilder) orientDigest(s *Session) (string, error) {
	hash := b.indexHash()

	b.mu.Lock()
	entry, ok := b.orient[s.ID]
	b.mu.Unlock()
	if ok && entry.indexHash == hash {
		return entry.digest, nil
	}

	res, err := b.r.Call(context.Background(), "vault.orient", json.RawMessage(`{}`))
	if err != nil {
		return "", fmt.Errorf("orient: %w", err)
	}

	b.mu.Lock()
	b.orient[s.ID] = orientEntry{digest: res.Content, indexHash: hash}
	b.mu.Unlock()
	return res.Content, nil
}

// indexHash hashes index.md's current bytes so orientDigest can detect a
// change. A vault with no index.md yet hashes to a constant (sha256 of
// nil), so a session still caches correctly.
func (b *ContextBuilder) indexHash() string {
	data, _ := b.v.Read("index.md") // absence is not an error here
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// recordToMessage folds one prose Record — user, assistant or the system
// placeholder Compact writes — into the llm.Message Build sends. It never
// sees a tool record: Build routes those through appendHistoryPair, because
// a tool record is two wire messages, not one.
//
// Until 046 this function also rendered a tool record, as the single text
// message {"role":"tool","content":"raw.list({…}) -> …"}, on the argument
// that Record carries no tool-call id to rebuild the wire pairing and that
// history is context to read, not a call to re-dispatch. That argument was
// wrong. The OpenAI chat format requires every role:"tool" message to carry
// a tool_call_id answering a tool_calls entry of the assistant message before
// it, whether or not anything is ever re-dispatched; z.ai tolerated the
// violation, DeepSeek did not — `422 … messages[6]: missing field
// tool_call_id`, on every turn that resumed a changeset holding one earlier
// tool call. A strict provider validates the history exactly as it validates
// a live round, so history must have the live round's shape. The id does not
// need to be the original: a pair only has to be self-consistent, and
// appendHistoryPair mints one.
//
// Reasoning is deliberately NOT emitted (005, D-5G): it is persisted for
// the human reading `lw session show`, and replayed history must stay
// byte-identical to v1.0.0's. Changing what a replayed turn sends would be
// an untested provider-behaviour change smuggled in under a recording
// feature, and is not in this workflow's scope. 046 leaves that as it was:
// a history with no tool records sends exactly the bytes it did before.
func recordToMessage(r Record) llm.Message {
	return llm.Message{Role: r.Role, Content: r.Content}
}

// noResultRecorded is the content of a history tool message whose Record has
// no Result. A tool message with empty content is rejected or mangled by some
// providers, and "empty" is not what happened: the call was made and its
// result was not kept — an assistant tool record whose Result never arrived,
// or a tool that returned nothing.
const noResultRecorded = "(no result recorded)"

// appendHistoryPair appends, to msgs, the spec-valid pair that replays the
// tool record recs[i] as history (046):
//
//  1. {"role":"assistant","tool_calls":[{"id":ID,"type":"function",
//     "function":{"name":WireName(Tool),"arguments":ARGS}}]}, with no content;
//  2. {"role":"tool","tool_call_id":ID,"content":RESULT}.
//
// k is the 1-based count of pairs this Build call has emitted, including
// this one, and ID is "hist_<k>". The id depends only on the history, never
// on a clock, a random source or a counter that outlives the call, so the
// same history yields the same bytes on every Build and the provider's
// prefix cache keeps hitting across turns. A live round's ids come from the
// provider and are far from "hist_<k>", so the two never meet in one request.
// The wire name is tools.WireName(Tool) because Record stores the canonical
// dotted name, which every endpoint rejects in a function name, then
// sanitizeWireName(…) because a tool the model invented is recorded under
// its own spelling (A-046-3).
//
// RESULT is the record's Result, or noResultRecorded when that is empty.
//
// The split shape — an assistant-role tool record with an empty Result,
// immediately followed in recs by a tool-role record with the same Tool and
// empty Args — is one call logged as two records (the call, then its result).
// It becomes ONE pair: the first record's args and the second's result.
// Emitting two pairs would invent a call that never happened. appendHistoryPair
// reports how many records it consumed, 1 or 2.
func appendHistoryPair(msgs []llm.Message, recs []Record, i, k int) ([]llm.Message, int) {
	r := recs[i]
	result := r.Result
	used := 1
	if r.Role == "assistant" && r.Result == "" && i+1 < len(recs) {
		if next := recs[i+1]; next.Role == "tool" && next.Tool == r.Tool && next.Args == "" {
			result = next.Result
			used = 2
		}
	}
	if result == "" {
		result = noResultRecorded
	}

	id := fmt.Sprintf("hist_%d", k)
	call := llm.ToolCall{ID: id, Type: "function"}
	call.Function.Name = sanitizeWireName(tools.WireName(r.Tool))
	call.Function.Arguments = historyArgs(r.Args)
	return append(msgs,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
		llm.Message{Role: "tool", ToolCallID: id, Content: result},
	), used
}

// historyArgs is the arguments string of a replayed tool call. The wire wants
// a JSON object there, and Record.Args is whatever the call carried: usually
// the model's own object, which passes through byte for byte; empty, which
// becomes {}; or something else — a bare op id, a truncated stream, a JSON
// array or null — which is wrapped as {"raw": <args as a JSON string>} so the
// message stays valid and the text stays readable to the model. Blank
// arguments — empty or only whitespace — are empty (A-046-3): the call carried
// nothing, and {"raw":"  "} would be valid JSON that says nothing.
func historyArgs(args string) string {
	if strings.TrimSpace(args) == "" {
		return "{}"
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &obj); err == nil && obj != nil {
		return args
	}
	raw, err := json.Marshal(struct {
		Raw string `json:"raw"`
	}{Raw: args})
	if err != nil {
		return "{}" // unreachable: a struct of one string always marshals
	}
	return string(raw)
}

// maxWireNameLen is the longest function name an OpenAI-compatible endpoint
// accepts in tools[*].function.name and in a tool_calls entry.
const maxWireNameLen = 64

// sanitizeWireName makes name legal in a function-name field: every rune
// outside [a-zA-Z0-9_-] — the pattern every OpenAI-compatible endpoint
// enforces (internal/tools/names.go) — becomes '_', one underscore per rune
// (an invalid UTF-8 byte counts as one); the result is then cut to
// maxWireNameLen characters, the OpenAI function-name limit (A-046-4); and an
// empty result becomes "unknown_tool". After the mapping every rune is ASCII,
// so cutting by bytes cuts by characters.
//
// Why it exists (A-046-3): tools.WireName only maps the registry's dots. A
// tool the model invented is recorded by the unknown-tool path under its own
// spelling, so "Bad Name!" would be replayed verbatim, and a strict provider
// would refuse the history on this turn and on every later turn that resumes
// the session — the record is permanent. A name that is merely too long is
// refused the same way. A name a registry tool carries is already legal and
// passes through unchanged. An empty name cannot reach here
// from Build (a pair needs Tool != ""); the fallback keeps the function total.
func sanitizeWireName(name string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, name)
	if len(clean) > maxWireNameLen {
		clean = clean[:maxWireNameLen]
	}
	if clean == "" {
		return "unknown_tool"
	}
	return clean
}

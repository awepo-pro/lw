package agent

// context.go implements backbone §9's ContextBuilder: assembling one turn's
// full message list in the exact five-part order /docs/design.md §11.3 specifies.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
//  1. the system prompt (prompt.go, static);
//  2. curator-memory.md, verbatim;
//  3. the orientation digest — vault.orient's Result.Content, injected once
//     per session and refreshed only when index.md's content changes;
//  4. session history, compacted (Compact) to fit whatever budget remains;
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

	msgs := []llm.Message{
		{Role: "system", Content: systemPrompt},
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

	for _, rec := range Compact(s.Records, remaining) {
		// D-5G: a record with no tool and no content — since 005, a
		// reasoning-only assistant record — would become an empty
		// assistant message, which some providers reject outright. Skip
		// it. The persisted thinking is for the human reading
		// `lw session show`, not for the wire. Tool records never match
		// this guard (their Tool is non-empty), so a staged op's audit
		// trail cannot be dropped here.
		if rec.Tool == "" && rec.Content == "" {
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

// recordToMessage folds one session Record into the llm.Message shape
// Build sends. Tool calls and results are rendered as descriptive text
// rather than replayed as structured llm.ToolCalls: Record carries no
// tool-call id to reconstruct the original wire pairing, and history here
// is read by the model as context, not re-dispatched — live dispatch is
// Loop's job (S5-T3).
//
// Reasoning is deliberately NOT emitted (005, D-5G): it is persisted for
// the human reading `lw session show`, and replayed history must stay
// byte-identical to v1.0.0's. Changing what a replayed turn sends would be
// an untested provider-behaviour change smuggled in under a recording
// feature, and is not in this workflow's scope.
func recordToMessage(r Record) llm.Message {
	if r.Tool == "" {
		return llm.Message{Role: r.Role, Content: r.Content}
	}
	if r.Result != "" {
		return llm.Message{Role: r.Role, Content: fmt.Sprintf("%s(%s) -> %s", r.Tool, r.Args, r.Result)}
	}
	return llm.Message{Role: r.Role, Content: fmt.Sprintf("%s(%s)", r.Tool, r.Args)}
}

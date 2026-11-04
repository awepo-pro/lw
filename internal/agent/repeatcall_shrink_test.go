package agent

// repeatcall_shrink_test.go pins workflow 081, fix subtask FIX-R (R-2):
// shrink confirmation repeat exemption in the agent loop.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
)

const shrinkConfirmLine = `"allow_shrink" counts only as a confirmation after this refusal. If deleting this text is intended, repeat the same call now.`

func newShrinkLoopFixture(t *testing.T) *testLoopFixture {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")

	// Extend section "## Why it matters" in wiki/concepts/kv-cache.md to be >= 400 bytes.
	p := filepath.Join(root, "wiki", "concepts", "kv-cache.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read kv-cache.md: %v", err)
	}

	oldSec := "## Why it matters\n\n" +
		"Without caching, generating token n would repeat O(n) work already done for\n" +
		"earlier tokens. Caching turns that into a constant amount of new work per\n" +
		"step, at the cost of memory that grows with sequence length."

	expandedSec := "## Why it matters\n\n" +
		"Without caching, generating token n would repeat O(n) work already done for\n" +
		"earlier tokens. Caching turns that into a constant amount of new work per\n" +
		"step, at the cost of memory that grows with sequence length.\n\n" +
		"In large language models with many layers and attention heads, the memory\n" +
		"footprint of key and value states across long context windows dominates\n" +
		"inference memory consumption and requires careful management strategies.\n\n" +
		"Understanding KV cache behavior is critical for high-throughput serving\n" +
		"systems because memory capacity limits maximum concurrent batch sizes and\n" +
		"determines effective GPU hardware utilization during decoding steps."

	newContent := strings.Replace(string(b), oldSec, expandedSec, 1)
	if newContent == string(b) {
		t.Fatalf("failed to expand section in kv-cache.md")
	}
	if err := os.WriteFile(p, []byte(newContent), 0o644); err != nil {
		t.Fatalf("write kv-cache.md: %v", err)
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("stage.OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	author := stage.Author{Kind: "agent", Model: "test-model"}
	reg := tools.NewRegistry(tools.Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author})

	cs, err := e.OpenChangeset("shrink loop test", author)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	store := NewFileSessions(root)
	if _, err := store.Create(cs.ID); err != nil {
		t.Fatalf("Create session: %v", err)
	}

	return &testLoopFixture{engine: e, reg: reg, store: store, csID: cs.ID}
}

func newShrinkTestLoop(t *testing.T, rounds [][]llm.Chunk, cfg LoopConfig) (*Loop, *testLoopFixture, *fakeStreamer) {
	t.Helper()
	fx := newShrinkLoopFixture(t)
	fake := &fakeStreamer{rounds: rounds}
	l := newLoop(fake, fx.reg, fx.store, fx.engine, cfg)
	return l, fx, fake
}

// TestRepeatCallShrinkFR2_1 pins workflow 081, FR2.1:
// Loop level: round 1 the model sends stage.patch_page replace_section with "allow_shrink": true
// whose content keeps under half of a 400+ byte section -> the tool result is the 043 shrink refusal
// (IsError) whose last line is the confirm line; round 2 the model sends the IDENTICAL call ->
// the handler RUNS (not the 081 repeat refusal) and the edit stages (the result is not IsError,
// and the open changeset has exactly 1 live op).
// Uses the real tool registry (tools.NewRegistry over a test vault + engine).
func TestRepeatCallShrinkFR2_1(t *testing.T) {
	args := `{"path":"wiki/concepts/kv-cache.md","section":"## Why it matters","op":"replace_section","content":"Short condensed text.","allow_shrink":true,"rationale":"condense why it matters"}`
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_patch_page", args), {Finish: "tool_calls"}},
		{toolCallChunk("call-2", "stage_patch_page", args), {Finish: "tool_calls"}},
		{{Text: "stopping"}, {Finish: "stop"}},
	}

	l, fx, _ := newShrinkTestLoop(t, rounds, LoopConfig{MaxToolRounds: 10})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "condense the section", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	res := toolResults(events)
	if len(res) != 2 {
		t.Fatalf("toolResults len = %d, want 2", len(res))
	}

	// Round 1 must be the 043 shrink refusal with the confirm line.
	if !res[0].IsError {
		t.Fatalf("round 1 result IsError = false, want true (shrink refusal)")
	}
	if !tools.ShrinkRefusal(res[0].Content) {
		t.Fatalf("round 1 result not recognised as ShrinkRefusal:\n%s", res[0].Content)
	}
	lastLine := res[0].Content[strings.LastIndexByte(res[0].Content, '\n')+1:]
	if lastLine != shrinkConfirmLine {
		t.Fatalf("round 1 last line = %q, want %q", lastLine, shrinkConfirmLine)
	}

	// Round 2 must NOT be refused by the repeat guard; the edit must stage.
	if res[1].IsError {
		t.Fatalf("round 2 result IsError = true, want false (confirmed shrink staged); content = %q", res[1].Content)
	}

	cs, err := fx.engine.Current()
	if err != nil {
		t.Fatalf("engine.Current: %v", err)
	}
	live := cs.Live()
	if len(live) != 1 {
		t.Fatalf("open changeset has %d live ops, want exactly 1", len(live))
	}
	if live[0].Kind != stage.OpPatchPage {
		t.Fatalf("live op kind = %v, want %v", live[0].Kind, stage.OpPatchPage)
	}
}

// TestRepeatCallShrinkFR2_2 pins workflow 081, FR2.2:
// Loop level: the same loop, but round 1's failure is a find-not-found replace_text that also
// carries "allow_shrink": true -> the identical round-2 call IS refused with failedRepeatRefusalFmt
// (only shrink refusals are exempt).
func TestRepeatCallShrinkFR2_2(t *testing.T) {
	args := `{"path":"wiki/concepts/kv-cache.md","section":"## Why it matters","op":"replace_text","find":"this text is definitely absent from the section","content":"new content","allow_shrink":true,"rationale":"test find not found"}`
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_patch_page", args), {Finish: "tool_calls"}},
		{toolCallChunk("call-2", "stage_patch_page", args), {Finish: "tool_calls"}},
		{{Text: "stopping"}, {Finish: "stop"}},
	}

	l, fx, _ := newShrinkTestLoop(t, rounds, LoopConfig{MaxToolRounds: 10})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "replace absent text", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	res := toolResults(events)
	if len(res) != 2 {
		t.Fatalf("toolResults len = %d, want 2", len(res))
	}

	// Round 1 is find-not-found error.
	if !res[0].IsError {
		t.Fatalf("round 1 result IsError = false, want true (find-not-found)")
	}
	if tools.ShrinkRefusal(res[0].Content) {
		t.Fatalf("round 1 result unexpectedly recognized as ShrinkRefusal: %s", res[0].Content)
	}
	if !strings.Contains(res[0].Content, "not found in section") {
		t.Fatalf("round 1 result missing 'not found in section': %s", res[0].Content)
	}

	// Round 2 must be refused by the failed repeat guard.
	if !res[1].IsError {
		t.Fatalf("round 2 result IsError = false, want true (failed repeat refusal)")
	}
	wantRefusal := fmt.Sprintf(failedRepeatRefusalFmt, "stage.patch_page", 1)
	if res[1].Content != wantRefusal {
		t.Fatalf("round 2 refusal content = %q, want %q", res[1].Content, wantRefusal)
	}

	cs, err := fx.engine.Current()
	if err != nil {
		t.Fatalf("engine.Current: %v", err)
	}
	live := cs.Live()
	if len(live) != 0 {
		t.Fatalf("open changeset has %d live ops, want 0", len(live))
	}
}

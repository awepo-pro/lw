package agent

// repeatcall_shrink_scope_test.go pins workflow 081, fix subtask FIX-D (R2-1, FD.1):
// a shrink refusal on page B does not clear the repeat guard for page A's failed
// stage calls or page A's reads.

import (
	"context"
	"fmt"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
)

// TestRepeatCallShrinkScopeFD_1 pins workflow 081, FD.1 (R2-1):
// Round 1 sends a failing stage.patch_page on page A (find not found) and a wiki.get of page A.
// Round 2 sends a stage.patch_page on page B that triggers a shrink refusal.
// Round 3 sends the identical failing call on page A and the identical wiki.get of page A.
// The shrink refusal on page B must NOT clear g.failed or g.ran; round 3's calls must both be
// refused (failedRepeatRefusalFmt for patch_page, repeatCallRefusalFmt for wiki.get).
func TestRepeatCallShrinkScopeFD_1(t *testing.T) {
	patchArgsA := `{"path":"wiki/concepts/flash-attention.md","section":"## Abstract","op":"replace_text","find":"this text is definitely absent from the section","content":"new content","allow_shrink":true,"rationale":"test find not found"}`
	getArgsA := `{"page":"wiki/concepts/flash-attention.md"}`
	shrinkArgsB := `{"path":"wiki/concepts/kv-cache.md","section":"## Why it matters","op":"replace_section","content":"Short condensed text.","allow_shrink":true,"rationale":"condense why it matters"}`

	rounds := [][]llm.Chunk{
		{
			toolCallChunk("call-1a", "stage_patch_page", patchArgsA),
			toolCallChunk("call-1b", "wiki_get", getArgsA),
			{Finish: "tool_calls"},
		},
		{
			toolCallChunk("call-2", "stage_patch_page", shrinkArgsB),
			{Finish: "tool_calls"},
		},
		{
			toolCallChunk("call-3a", "stage_patch_page", patchArgsA),
			toolCallChunk("call-3b", "wiki_get", getArgsA),
			{Finish: "tool_calls"},
		},
		{
			{Text: "stopping"},
			{Finish: "stop"},
		},
	}

	l, fx, _ := newShrinkTestLoop(t, rounds, LoopConfig{MaxToolRounds: 10})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "test shrink scope", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	res := toolResults(events)
	if len(res) != 5 {
		t.Fatalf("toolResults len = %d, want 5", len(res))
	}

	// Round 1: patch_page fails on page A (find not found)
	if !res[0].IsError {
		t.Fatalf("res[0] IsError = false, want true (find-not-found)")
	}

	// Round 1: wiki.get succeeds on page A
	if res[1].IsError {
		t.Fatalf("res[1] IsError = true, want false (wiki.get)")
	}

	// Round 2: shrink refusal on page B
	if !res[2].IsError {
		t.Fatalf("res[2] IsError = false, want true (shrink refusal)")
	}
	if !tools.ShrinkRefusal(res[2].Content) {
		t.Fatalf("res[2] not recognized as ShrinkRefusal:\n%s", res[2].Content)
	}

	// Round 3: identical failing stage.patch_page on page A must be refused with failedRepeatRefusalFmt
	if !res[3].IsError {
		t.Fatalf("res[3] IsError = false, want true (failed repeat refusal)")
	}
	wantPatchRefusal := fmt.Sprintf(failedRepeatRefusalFmt, "stage.patch_page", 1)
	if res[3].Content != wantPatchRefusal {
		t.Fatalf("res[3] Content = %q, want %q", res[3].Content, wantPatchRefusal)
	}

	// Round 3: identical wiki.get on page A must be refused with repeatCallRefusalFmt
	if !res[4].IsError {
		t.Fatalf("res[4] IsError = false, want true (read repeat refusal)")
	}
	wantGetRefusal := fmt.Sprintf(repeatCallRefusalFmt, "wiki.get", 1)
	if res[4].Content != wantGetRefusal {
		t.Fatalf("res[4] Content = %q, want %q", res[4].Content, wantGetRefusal)
	}
}

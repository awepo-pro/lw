package agent

// repeatcall_failed_test.go pins workflow 081, subtask S1: refusing identical repeats
// of failed stage.* tool calls (R1) across turn rounds.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"unsafe"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
)

// regSpy exposes tools.Registry's unexported tools map for testing.
type regSpy struct {
	deps  tools.Deps
	tools map[string]tools.Tool
}

// TestRepeatCallFailedF1_1 pins workflow 081, F1.1:
// noteResult("stage.patch_page", A, "id1", 4, true); refusal of the same tool with A's
// keys reordered and extra whitespace -> refused, round 4, text exact.
func TestRepeatCallFailedF1_1(t *testing.T) {
	g := newRepeatGuard()
	argsA := `{"op":"replace_text","path":"wiki/concepts/test.md","find":"old","content":"new"}`
	argsAReordered := "{\n  \"content\": \"new\",\n  \"find\": \"old\",\n  \"op\": \"replace_text\",\n  \"path\": \"wiki/concepts/test.md\"\n}"

	g.noteResult("stage.patch_page", argsA, "id1", 4, true)

	text, round, refused := g.refusal("stage.patch_page", argsAReordered)
	if !refused {
		t.Fatalf("refusal returned refused=false, want true")
	}
	if round != 4 {
		t.Errorf("refusal round = %d, want 4", round)
	}
	wantText := fmt.Sprintf(failedRepeatRefusalFmt, "stage.patch_page", 4)
	if text != wantText {
		t.Errorf("refusal text = %q, want %q", text, wantText)
	}
}

// TestRepeatCallFailedF1_2 pins workflow 081, F1.2:
// as F1.1, then noteResult("stage.add_link", X, "id2", 5, false) (a successful state change)
// -> A not refused.
func TestRepeatCallFailedF1_2(t *testing.T) {
	g := newRepeatGuard()
	argsA := `{"op":"replace_text","path":"wiki/concepts/test.md","find":"old","content":"new"}`
	argsAReordered := "{\n  \"content\": \"new\",\n  \"find\": \"old\",\n  \"op\": \"replace_text\",\n  \"path\": \"wiki/concepts/test.md\"\n}"
	argsX := `{"from":"wiki/concepts/test.md","to":"wiki/entities/target.md","context":"ctx","rationale":"r"}`

	g.noteResult("stage.patch_page", argsA, "id1", 4, true)
	if _, _, refused := g.refusal("stage.patch_page", argsAReordered); !refused {
		t.Fatalf("setup: expected A to be refused initially")
	}

	g.noteResult("stage.add_link", argsX, "id2", 5, false)

	if text, round, refused := g.refusal("stage.patch_page", argsA); refused {
		t.Errorf("refusal after state change = (%q, %d, %v), want refused=false", text, round, refused)
	}
}

// TestRepeatCallFailedF1_3 pins workflow 081, F1.3:
// as F1.1, then a successful stage.open and a successful wiki.get -> A still refused
// (neither changes state).
func TestRepeatCallFailedF1_3(t *testing.T) {
	g := newRepeatGuard()
	argsA := `{"op":"replace_text","path":"wiki/concepts/test.md","find":"old","content":"new"}`
	argsAReordered := "{\n  \"content\": \"new\",\n  \"find\": \"old\",\n  \"op\": \"replace_text\",\n  \"path\": \"wiki/concepts/test.md\"\n}"

	g.noteResult("stage.patch_page", argsA, "id1", 4, true)
	g.noteResult("stage.open", `{"name":"changeset-1"}`, "id2", 5, false)
	g.noteResult("wiki.get", `{"page":"wiki/concepts/test.md"}`, "id3", 6, false)

	text, round, refused := g.refusal("stage.patch_page", argsAReordered)
	if !refused {
		t.Fatalf("refusal returned refused=false, want true (neither stage.open nor wiki.get changes state)")
	}
	if round != 4 {
		t.Errorf("refusal round = %d, want 4", round)
	}
	wantText := fmt.Sprintf(failedRepeatRefusalFmt, "stage.patch_page", 4)
	if text != wantText {
		t.Errorf("refusal text = %q, want %q", text, wantText)
	}
}

// TestRepeatCallFailedF1_4 pins workflow 081, F1.4:
// A' differing from A in one byte of find -> not refused.
func TestRepeatCallFailedF1_4(t *testing.T) {
	g := newRepeatGuard()
	argsA := `{"op":"replace_text","path":"wiki/concepts/test.md","find":"old","content":"new"}`
	argsAPrime := `{"op":"replace_text","path":"wiki/concepts/test.md","find":"old!","content":"new"}`

	g.noteResult("stage.patch_page", argsA, "id1", 4, true)

	if text, round, refused := g.refusal("stage.patch_page", argsAPrime); refused {
		t.Errorf("refusal for A' = (%q, %d, %v), want refused=false", text, round, refused)
	}
}

// TestRepeatCallFailedF1_5 pins workflow 081, F1.5:
// a failed wiki.get repeated identically -> not refused (051 unchanged).
func TestRepeatCallFailedF1_5(t *testing.T) {
	g := newRepeatGuard()
	args := `{"page":"wiki/concepts/test.md"}`

	g.noteResult("wiki.get", args, "id1", 4, true)

	if text, round, refused := g.refusal("wiki.get", args); refused {
		t.Errorf("refusal for failed wiki.get = (%q, %d, %v), want refused=false", text, round, refused)
	}
}

// TestRepeatCallFailedF1_6 pins workflow 081, F1.6:
// A fails in round 4, a state change succeeds in 5, A fails again in 6 -> the refusal
// names round 6.
func TestRepeatCallFailedF1_6(t *testing.T) {
	g := newRepeatGuard()
	argsA := `{"op":"replace_text","path":"wiki/concepts/test.md","find":"old","content":"new"}`
	argsB := `{"op":"replace_text","path":"wiki/concepts/other.md","find":"foo","content":"bar"}`

	g.noteResult("stage.patch_page", argsA, "id1", 4, true)
	g.noteResult("stage.patch_page", argsB, "id2", 5, false)
	g.noteResult("stage.patch_page", argsA, "id3", 6, true)

	text, round, refused := g.refusal("stage.patch_page", argsA)
	if !refused {
		t.Fatalf("refusal returned refused=false, want true")
	}
	if round != 6 {
		t.Errorf("refusal round = %d, want 6", round)
	}
	wantText := fmt.Sprintf(failedRepeatRefusalFmt, "stage.patch_page", 6)
	if text != wantText {
		t.Errorf("refusal text = %q, want %q", text, wantText)
	}
}

// TestRepeatCallFailedF1_7 pins workflow 081, F1.7:
// a failed call whose result message is elided by boundContext (use the existing
// syncElided test pattern) -> its identical repeat is dispatched, not refused.
func TestRepeatCallFailedF1_7(t *testing.T) {
	g := newRepeatGuard()
	argsA := `{"op":"replace_text","path":"wiki/concepts/test.md","find":"old","content":"new"}`

	g.noteResult("stage.patch_page", argsA, "id1", 4, true)
	if _, _, refused := g.refusal("stage.patch_page", argsA); !refused {
		t.Fatalf("setup: expected call to be refused before elision")
	}

	msgs := []llm.Message{{Role: "tool", ToolCallID: "id1"}}
	elided := map[int]bool{0: true}
	g.syncElided(msgs, 0, elided)

	if text, round, refused := g.refusal("stage.patch_page", argsA); refused {
		t.Errorf("refusal after elision = (%q, %d, %v), want refused=false", text, round, refused)
	}
}

// TestRepeatCallFailedF1_8 pins workflow 081, F1.8:
// Loop level, scripted provider (existing loop test harness): the model sends the same
// failing stage.patch_page (handler always returns IsError "boom") in rounds 1, 2 and 3
// -> the handler runs exactly once; the tool result content in rounds 2 and 3 equals
// fmt.Sprintf(failedRepeatRefusalFmt, "stage.patch_page", 1).
func TestRepeatCallFailedF1_8(t *testing.T) {
	args := `{"path":"wiki/concepts/test.md","section":"## Related","op":"replace_text","find":"x","content":"y","rationale":"test"}`
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_patch_page", args), {Finish: "tool_calls"}},
		{toolCallChunk("call-2", "stage_patch_page", args), {Finish: "tool_calls"}},
		{toolCallChunk("call-3", "stage_patch_page", args), {Finish: "tool_calls"}},
		{{Text: "stopping"}, {Finish: "stop"}},
	}

	l, fx, _ := newTestLoop(t, rounds, LoopConfig{MaxToolRounds: 10})

	spy := (*regSpy)(unsafe.Pointer(fx.reg))
	orig := spy.tools["stage.patch_page"]
	runs := 0
	spy.tools["stage.patch_page"] = tools.Tool{
		Name:        orig.Name,
		Description: orig.Description,
		Schema:      orig.Schema,
		ReadOnly:    orig.ReadOnly,
		Handler: func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
			runs++
			return tools.Result{IsError: true, Content: "boom"}, nil
		},
	}

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "test prompt", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	if runs != 1 {
		t.Errorf("handler ran %d times, want exactly 1", runs)
	}

	res := toolResults(events)
	if len(res) != 3 {
		t.Fatalf("toolResults len = %d, want 3", len(res))
	}

	if res[0].Content != "boom" || !res[0].IsError {
		t.Errorf("round 1 result = (%q, isError=%v), want (\"boom\", isError=true)", res[0].Content, res[0].IsError)
	}

	wantRefusal := fmt.Sprintf(failedRepeatRefusalFmt, "stage.patch_page", 1)
	if res[1].Content != wantRefusal || !res[1].IsError {
		t.Errorf("round 2 result = (%q, isError=%v), want (%q, isError=true)", res[1].Content, res[1].IsError, wantRefusal)
	}
	if res[2].Content != wantRefusal || !res[2].IsError {
		t.Errorf("round 3 result = (%q, isError=%v), want (%q, isError=true)", res[2].Content, res[2].IsError, wantRefusal)
	}
}

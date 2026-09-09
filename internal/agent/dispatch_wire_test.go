package agent

import (
	"context"
	"regexp"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
)

// wireNameRE mirrors the pattern every OpenAI-compatible endpoint enforces
// on tools[*].function.name (backbone §6/§7's amendment, D-CY/C-112).
var wireNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// wireCreatePageArgs is a known-valid stage.create_page payload — copied
// from spec/fixtures/conformance/rename-cascade.json's own create_page
// step so this test exercises a real, passing tool call rather than
// guessing at SCHEMA.md's taxonomy and outbound-link-count rules.
const wireCreatePageArgs = `{"path":"wiki/concepts/wire-name-test.md","title":"Wire Name Test","type":"concept","tags":["inference"],"sources":["raw/papers/leviathan-2023.md"],"confidence":"low","contested":false,"body":"# Wire Name Test\n\nSee [[kv-cache]] and [[gpt-4]].","rationale":"exercise wire name dispatch"}`

// TestDispatchCanonicalizesWireToolName is S5-T6's regression test for
// C-112: the provider hands dispatchToolCall its own wire spelling of a
// tool name — underscores, never dots (backbone §6/§7's amendment, D-CY),
// since Registry.Definitions advertised it that way. It scripts the wire
// spelling "stage_create_page", deliberately chosen over "stage_open"
// because the canonical name "stage.create_page" carries its own
// underscore inside the leaf component: a naive
// strings.ReplaceAll(wire, "_", ".") would turn "stage_create_page" into
// "stage.create.page", the exact defect D-CY's switch exists to prevent.
//
// It asserts three things in one turn: the registry receives the
// canonical "stage.create_page" and the call actually succeeds (an
// unrecognized name would instead retry as model-correctable, never
// reaching appendStageOp); the resulting session Record carries
// Tool="stage.create_page" with Staged=true (backbone §9's Compact
// contract must never drop it); and every outbound llm.Request advertises
// wire-safe tool names, so Registry.Definitions' own translation
// (registry.go, WireName) reaches the wire through this exact Loop.
func TestDispatchCanonicalizesWireToolName(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "proposed."}, {Finish: "stop"}},
	}
	l, fx, fake := newTestLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "create a page", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	var sawToolCall, sawSuccessRes, sawStage bool
	for _, ev := range events {
		switch e := ev.(type) {
		case ToolCallEv:
			sawToolCall = true
			if e.Name != "stage.create_page" {
				t.Errorf("ToolCallEv.Name = %q, want canonical %q", e.Name, "stage.create_page")
			}
		case ToolResEv:
			if e.Name != "stage.create_page" {
				t.Errorf("ToolResEv.Name = %q, want canonical %q", e.Name, "stage.create_page")
			}
			if !e.IsError {
				sawSuccessRes = true
			} else {
				t.Errorf("ToolResEv reported IsError; want the call to succeed: %s", e.Content)
			}
		case StageEv:
			sawStage = true
		}
	}
	if !sawToolCall {
		t.Fatalf("no ToolCallEv observed, got %#v", events)
	}
	if !sawSuccessRes {
		t.Fatalf("no successful ToolResEv for stage.create_page observed, got %#v", events)
	}
	if !sawStage {
		t.Fatalf("no StageEv observed for a successful stage.* call, got %#v", events)
	}

	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	var found bool
	for _, r := range sess.Records {
		if r.Role == "tool" && r.Tool == "stage.create_page" {
			found = true
			if !r.Staged {
				t.Errorf("Record{Tool: %q}.Staged = false, want true", r.Tool)
			}
		}
	}
	if !found {
		t.Fatalf("no session Record with Tool %q, got %+v", "stage.create_page", sess.Records)
	}

	reqs := fake.Requests()
	if len(reqs) == 0 {
		t.Fatal("fakeStreamer recorded no requests")
	}
	for _, req := range reqs {
		if len(req.Tools) == 0 {
			t.Fatal("llm.Request.Tools is empty; want the registry's definitions")
		}
		for _, def := range req.Tools {
			if !wireNameRE.MatchString(def.Name) {
				t.Errorf("outbound llm.Request.Tools name %q does not match %s", def.Name, wireNameRE)
			}
		}
	}
}

package tools

import (
	"strings"
	"testing"
)

// 025-T5 (ask cold-start). The engine injects an orientation digest into
// every turn's context (internal/agent/context.go), so a tool description
// telling the model to call vault.orient at session start just bought a
// redundant round trip for a digest it already had. The description must
// present the tool as an explicit, on-demand re-orientation — what it
// returns, and nothing that nudges a call before real work begins.
func TestOrientDescriptionHasNoCallInstruction(t *testing.T) {
	reg := minimalRegistry(t)

	tool, ok := reg.Get("vault.orient")
	if !ok {
		t.Fatal("vault.orient not registered")
	}

	desc := tool.Description

	if strings.Contains(desc, "at the start of") {
		t.Errorf("vault.orient description still instructs a session-start call: %q", desc)
	}
	for _, want := range []string{"SCHEMA.md", "index.md"} {
		if !strings.Contains(desc, want) {
			t.Errorf("vault.orient description must still name %s: %q", want, desc)
		}
	}
}

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestStageOpenJoins is 019 T1's frozen stage.open test: with one changeset
// already open, a second stage.open JOINS it — the result carries the
// joined text with the live-op count and is not an error — instead of
// failing with "a changeset is already open".
func TestStageOpenJoins(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	ctx := context.Background()

	first, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"first"}`))
	if err != nil || first.IsError {
		t.Fatalf("first stage.open: %+v %v", first, err)
	}

	// One live op before the join, so the joined text's count is 1.
	if r, err := reg.Call(ctx, "stage.patch_page", json.RawMessage(`{"path":"wiki/concepts/kv-cache.md","section":"## Related","op":"append_section","content":"- [[gpt-4]]","rationale":"connect related pages"}`)); err != nil || r.IsError {
		t.Fatalf("stage.patch_page: %+v %v", r, err)
	}

	r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"second"}`))
	if err != nil {
		t.Fatalf("second stage.open: %v", err)
	}
	if r.IsError {
		t.Fatalf("stage.open with one open must join, not error; result = %+v", r)
	}
	if !strings.Contains(r.Content, "joined the open changeset cs-") {
		t.Errorf("result content = %q, want the joined text", r.Content)
	}
	if !strings.Contains(r.Content, "(1 op(s) already staged)") {
		t.Errorf("result content = %q, want the live-op count", r.Content)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cs.Intent != "first + second" {
		t.Errorf("Intent = %q, want %q — the join appends to the open changeset", cs.Intent, "first + second")
	}
	if got := len(cs.Live()); got != 1 {
		t.Errorf("live ops = %d, want 1 (the op staged between the two opens)", got)
	}
}

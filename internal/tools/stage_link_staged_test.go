package tools

// stage_link_staged_test.go freezes 020 FIX-3b's add_link contract (G3
// review finding 5): stage.add_link computed its patches from the
// committed vault — d.Vault.Page with Before = the committed sha, and a
// pre-flight ValidateOp against that same committed vault — so on a page
// the open changeset already stages, the committed-side pre-flight passed
// and Engine.Append refused the staged-page patch with "before does not
// match the current content" and no recovery hint. Exactly the shape lint
// --fix's per-page rounds create: the round that patches a page may also
// link it. The fix routes both endpoints through stagedPatchBase — the
// same staged-first base stage.patch_page uses — so add_link composes
// with staged edits the way every other content proposal does, and its
// refusals name the staged state when one exists.

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
)

// TestAddLinkToolSeesStagedBase is the chaining pin: stage a patch on
// kv-cache, then add_link kv-cache ↔ gpt-4 in the same changeset. The
// kv-cache-side patch must carry Before = the staged sha (the first
// patch's After) — before the fix the handler proposed the committed sha
// and the engine refused the whole call. The gpt-4 side, untouched by any
// staged op, must still carry the committed sha.
func TestAddLinkToolSeesStagedBase(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	committed, ok := e.Vault().Page("wiki/entities/gpt-4.md")
	if !ok {
		t.Fatal("committed gpt-4.md not found in fixture")
	}
	if r := callTool(t, reg, "stage.open", `{"intent":"patch a page, then link it"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Example","op":"remove_section","content":"","rationale":"drop the example"}`); r.IsError {
		t.Fatalf("staged patch: %s", r.Content)
	}
	if r := callTool(t, reg, "stage.add_link", `{"from":"wiki/concepts/kv-cache.md","to":"wiki/entities/gpt-4.md","context":"serving cost"}`); r.IsError {
		t.Fatalf("add_link on the staged page: %s", r.Content)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 4 {
		t.Fatalf("len(cs.Ops) = %d, want 4 (patch, add_link marker, two page patches)", len(cs.Ops))
	}
	marker, kvPatch, gptPatch := cs.Ops[1], cs.Ops[2], cs.Ops[3]
	if marker.Kind != stage.OpAddLink {
		t.Fatalf("op 1 kind = %s, want add_link", marker.Kind)
	}
	if kvPatch.Path != "wiki/concepts/kv-cache.md" {
		t.Fatalf("op 2 path = %s, want the staged page", kvPatch.Path)
	}
	if kvPatch.Before != cs.Ops[0].After {
		t.Fatalf("chained Before = %s, want the staged sha %s", kvPatch.Before, cs.Ops[0].After)
	}
	if kvPatch.Before == cs.Ops[0].Before {
		t.Fatalf("chained Before %s equals the committed sha — the tool ignored the staged state", kvPatch.Before)
	}
	if gptPatch.Path != "wiki/entities/gpt-4.md" {
		t.Fatalf("op 3 path = %s, want the unstaged page", gptPatch.Path)
	}
	if gptPatch.Before != committed.SHA256() {
		t.Fatalf("unstaged side Before = %s, want the committed sha %s", gptPatch.Before, committed.SHA256())
	}
}

// TestAddLinkErrorNamesStagedState pins the error path: a page whose
// STAGED state has no section to receive a link is misused exactly the
// way the refusal must describe — with the staged state named, not as a
// bare committed-page complaint. Every kv-cache heading is chained away
// first — the four body sections and the H1 title line, which
// ParseSections also counts — so the staged page parses with zero
// sections while the committed page (which the tool no longer reads)
// still has them.
func TestAddLinkErrorNamesStagedState(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	if r := callTool(t, reg, "stage.open", `{"intent":"strip a page, then try to link it"}`); r.IsError {
		t.Fatal(r.Content)
	}
	for _, sec := range []string{"## Related", "## Example", "## Why it matters", "## Abstract", "# KV Cache"} {
		if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"`+sec+`","op":"remove_section","content":"","rationale":"empty the page"}`); r.IsError {
			t.Fatalf("remove %s: %s", sec, r.Content)
		}
	}
	r := callTool(t, reg, "stage.add_link", `{"from":"wiki/concepts/kv-cache.md","to":"wiki/entities/gpt-4.md"}`)
	if !r.IsError {
		t.Fatalf("add_link on a section-less staged page must be refused, got:\n%s", r.Content)
	}
	if !strings.Contains(r.Content, "has no section to receive a link") {
		t.Fatalf("refusal lost the section complaint:\n%s", r.Content)
	}
	if !strings.Contains(r.Content, "staged edits in the open changeset") {
		t.Fatalf("refusal does not name the staged state:\n%s", r.Content)
	}
}

// TestAddLinkErrorNamesStagedOnlyEndpoint pins the differentiated clause:
// an endpoint a live create_page op produced exists only in the staged
// state, and no composition can make it an add_link endpoint — the
// refusal must say that, not send the agent looking for a committed page
// that genuinely is not there.
func TestAddLinkErrorNamesStagedOnlyEndpoint(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	if r := callTool(t, reg, "stage.open", `{"intent":"create a page, then try to link it"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := callTool(t, reg, "stage.create_page", `{"path":"wiki/concepts/paged-attention.md","title":"Paged Attention","type":"concept","tags":["inference","memory"],"sources":["raw/papers/leviathan-2023.md"],"confidence":"high","contested":false,"body":"Paged attention caches sequences in blocks. See [[kv-cache]] and [[gpt-4]].","rationale":"new concept"}`); r.IsError {
		t.Fatalf("create: %s", r.Content)
	}
	r := callTool(t, reg, "stage.add_link", `{"from":"wiki/concepts/kv-cache.md","to":"wiki/concepts/paged-attention.md"}`)
	if !r.IsError {
		t.Fatalf("add_link to a staged-only page must be refused, got:\n%s", r.Content)
	}
	if !strings.Contains(r.Content, "exists only in the open changeset's staged state") {
		t.Fatalf("refusal does not name the staged-only endpoint:\n%s", r.Content)
	}
}

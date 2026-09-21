package tools

// staged_base_test.go freezes 020 T-B's contract: the stage.* tools and
// wiki.get compute from the changeset's STAGED state, not just the
// committed vault. T-A (cd80e99) taught the engine to accept chained
// content ops — a second patch_page on a staged path must carry Before =
// the staged sha — but the tool handlers still read d.Vault.Page, the
// committed page, so a tool-level chained call proposed an unchained
// Before and the engine refused it: the F2 agent's blindness lived in the
// handlers, not the engine. These four tests pin both directions of the
// fix — staged-first when the changeset holds the path, committed-first
// when it does not — and the composed on-disk result after Commit.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
)

// callTool drives one registry call end to end, failing the test on a Go
// error (a turn abort) but returning IsError results untouched — whether
// a result SHOULD be an error is each test's own assertion.
func callTool(t *testing.T, reg *Registry, name, args string) Result {
	t.Helper()
	r, err := reg.Call(context.Background(), name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return r
}

// TestPatchPageToolSeesStagedBase is the chaining pin: remove a section
// (patch A), then insert_before another one (patch B) on the SAME path via
// the registry. Both must succeed, and B's Before must be A's After — the
// staged sha — proving the tool computed its base from the changeset's
// staged state rather than the committed page the engine has already moved
// past. Before the fix, patch B was refused with "before does not match
// the current content" because the handler proposed the committed sha.
func TestPatchPageToolSeesStagedBase(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	if r := callTool(t, reg, "stage.open", `{"intent":"chained section edits on one page"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Example","op":"remove_section","content":"","rationale":"drop the example"}`); r.IsError {
		t.Fatalf("patch A (remove_section): %s", r.Content)
	}
	if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Related","op":"insert_before","content":"## Trade-offs\n\nMemory grows with sequence length; that is the price of the constant per-step work.","rationale":"state the trade-off"}`); r.IsError {
		t.Fatalf("patch B (insert_before) on the staged page: %s", r.Content)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 2 {
		t.Fatalf("len(cs.Ops) = %d, want 2", len(cs.Ops))
	}
	a, b := cs.Ops[0], cs.Ops[1]
	if b.Before != a.After {
		t.Fatalf("chained Before = %s, want the staged sha %s", b.Before, a.After)
	}
	if b.Before == a.Before {
		t.Fatalf("chained Before %s equals A's committed sha — the tool ignored the staged state", b.Before)
	}
}

// TestPatchPageToolBaseIsCommittedWhenNothingStaged pins the legacy
// direction: whenever no live content op targets the path, the tool
// computes against the committed page — Before is the committed sha — so
// every single-op changeset proposes byte-for-byte what it did before
// T-B. Both subtests run with a changeset open, because that is the only
// shape in which StagedFile could answer and must not.
func TestPatchPageToolBaseIsCommittedWhenNothingStaged(t *testing.T) {
	t.Run("first patch of a changeset", func(t *testing.T) {
		reg, e, _ := engineRegistry(t, nil)
		committed, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("committed kv-cache.md not found in fixture")
		}
		if r := callTool(t, reg, "stage.open", `{"intent":"one patch"}`); r.IsError {
			t.Fatal(r.Content)
		}
		if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Related","op":"append_section","content":"- [[gpt-4]]","rationale":"connect related pages"}`); r.IsError {
			t.Fatalf("patch: %s", r.Content)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		if len(cs.Ops) != 1 {
			t.Fatalf("len(cs.Ops) = %d, want 1", len(cs.Ops))
		}
		if cs.Ops[0].Before != committed.SHA256() {
			t.Fatalf("Before = %s, want the committed sha %s", cs.Ops[0].Before, committed.SHA256())
		}
	})
	t.Run("patch another page while one is staged", func(t *testing.T) {
		reg, e, _ := engineRegistry(t, nil)
		committed, ok := e.Vault().Page("wiki/concepts/flash-attention.md")
		if !ok {
			t.Fatal("committed flash-attention.md not found in fixture")
		}
		if r := callTool(t, reg, "stage.open", `{"intent":"two unrelated patches"}`); r.IsError {
			t.Fatal(r.Content)
		}
		// Stage kv-cache first so the changeset HAS live content ops and
		// Append validates against a projection — the flash-attention
		// patch below must still see the committed page, because nothing
		// staged targets it.
		if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Example","op":"remove_section","content":"","rationale":"drop the example"}`); r.IsError {
			t.Fatalf("staged patch: %s", r.Content)
		}
		if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/flash-attention.md","section":"## Related","op":"append_section","content":"- [[kv-cache]]","rationale":"connect related pages"}`); r.IsError {
			t.Fatalf("patch on the unstaged page: %s", r.Content)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		if len(cs.Ops) != 2 {
			t.Fatalf("len(cs.Ops) = %d, want 2", len(cs.Ops))
		}
		if cs.Ops[1].Before != committed.SHA256() {
			t.Fatalf("Before = %s, want the committed sha %s", cs.Ops[1].Before, committed.SHA256())
		}
	})
}

// TestWikiGetReadsStagedPage pins the read-back direction: after a staged
// patch, wiki.get returns the staged body and carries the staged notice —
// the F2 blindness ran through reads too, and an agent that cannot see its
// own staged edit cannot plan the next one. Before any staging, no notice.
func TestWikiGetReadsStagedPage(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	pre := callTool(t, reg, "wiki.get", `{"page":"kv-cache"}`)
	if pre.IsError {
		t.Fatalf("pre-stage wiki.get: %s", pre.Content)
	}
	if strings.Contains(pre.Content, stagedSourceMarker) {
		t.Fatalf("pre-stage wiki.get carries the staged notice:\n%s", pre.Content)
	}
	if r := callTool(t, reg, "stage.open", `{"intent":"replace a section body"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Why it matters","op":"replace_section","content":"Staged replacement body.","rationale":"rewrite the motivation"}`); r.IsError {
		t.Fatalf("patch: %s", r.Content)
	}

	got := callTool(t, reg, "wiki.get", `{"page":"kv-cache"}`)
	if got.IsError {
		t.Fatalf("staged wiki.get: %s", got.Content)
	}
	if !strings.Contains(got.Content, stagedSourceMarker) {
		t.Fatalf("staged wiki.get does not carry the staged notice:\n%s", got.Content)
	}
	if !strings.Contains(got.Content, "Staged replacement body.") {
		t.Fatalf("staged wiki.get shows the committed body, not the staged one:\n%s", got.Content)
	}
	if strings.Contains(got.Content, "Without caching, generating token n") {
		t.Fatalf("staged wiki.get still shows the replaced section's old text:\n%s", got.Content)
	}
	if !strings.Contains(got.Content, "title: KV Cache") {
		t.Fatalf("staged wiki.get lost the frontmatter:\n%s", got.Content)
	}

	sec := callTool(t, reg, "wiki.get", `{"page":"kv-cache","section":"## Why it matters"}`)
	if sec.IsError {
		t.Fatalf("staged section read: %s", sec.Content)
	}
	if !strings.Contains(sec.Content, stagedSourceMarker) || !strings.Contains(sec.Content, "Staged replacement body.") {
		t.Fatalf("staged section read = %q, want the staged notice and the staged body", sec.Content)
	}
}

// TestChainedPatchFlowCommitsCommitted drives the whole arc through the
// tools — patch A, patch B, Commit — and pins the on-disk page: the old
// section is gone, the inserted one is in place, the untouched neighbour
// survives. This is the composed result the hunks previewed; Commit
// materializing anything else would mean the staged projection and the
// applier disagree.
func TestChainedPatchFlowCommitsComposed(t *testing.T) {
	reg, e, dir := engineRegistry(t, nil)
	if r := callTool(t, reg, "stage.open", `{"intent":"remove a section, insert another"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Example","op":"remove_section","content":"","rationale":"drop the example"}`); r.IsError {
		t.Fatalf("patch A: %s", r.Content)
	}
	if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Related","op":"insert_before","content":"## Trade-offs\n\nMemory grows with sequence length; that is the price of the constant per-step work.","rationale":"state the trade-off"}`); r.IsError {
		t.Fatalf("patch B: %s", r.Content)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 2 {
		t.Fatalf("len(cs.Ops) = %d, want 2", len(cs.Ops))
	}
	after := cs.Ops[1].After
	if _, err := e.Commit("remove Example, insert Trade-offs before Related"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "wiki", "concepts", "kv-cache.md"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	if strings.Contains(page, "## Example") || strings.Contains(page, "## this is not a heading, just log text") {
		t.Fatalf("committed page still carries the removed section:\n%s", page)
	}
	if !strings.Contains(page, "## Trade-offs") || !strings.Contains(page, "Memory grows with sequence length; that is the price of the constant per-step work.") {
		t.Fatalf("committed page lost the inserted section:\n%s", page)
	}
	if !strings.Contains(page, "## Related") {
		t.Fatalf("committed page lost the untouched neighbour section:\n%s", page)
	}
	// The committed bytes must be canonical — sha-comparable with what the
	// second op's After recorded — so a later chained patch composes.
	parsed, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parsed.Close()
	got, ok := parsed.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("committed page not loadable as a wiki page")
	}
	if got.SHA256() != after {
		t.Fatalf("committed sha %s != second op's After %s — the applier wrote non-canonical bytes", got.SHA256(), after)
	}
}

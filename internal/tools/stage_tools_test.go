package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

type fakeExtractor struct{ doc *extract.Doc }

func (f fakeExtractor) CanHandle(uri string) bool                             { return strings.HasSuffix(uri, ".md") }
func (f fakeExtractor) Extract(context.Context, string) (*extract.Doc, error) { return f.doc, nil }

func engineRegistry(t *testing.T, ex extract.Extractor) (*Registry, *stage.Engine, string) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v := e.Vault()
	return NewRegistry(Deps{Vault: v, Index: index.Build(v), Engine: e, Extract: ex, Author: stage.Author{Kind: "agent", Model: "test"}}), e, dir
}

func vaultBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	_ = filepath.WalkDir(dir, func(p string, ent os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ent.IsDir() {
			if ent.Name() == ".llmwiki" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		b, e := os.ReadFile(p)
		if e == nil {
			out[rel] = b
		}
		return nil
	})
	return out
}

func TestStageToolsDoNotTouchWorkingTree(t *testing.T) {
	reg, e, dir := engineRegistry(t, nil)
	ctx := context.Background()
	before := vaultBytes(t, dir)

	call := func(name, args string) Result {
		t.Helper()
		r, err := reg.Call(ctx, name, json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return r
	}
	if r := call("stage.open", `{"intent":"test staged proposals"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := call("stage.create_page", `{"path":"wiki/concepts/bad-page.md","title":"Bad","type":"concept","tags":["not-a-tag"],"sources":["raw/papers/leviathan-2023.md"],"confidence":"medium","body":"See [[kv-cache]] and [[gpt-4]].","rationale":"test"}`); !r.IsError || r.Content == "" {
		t.Fatalf("invalid create result = %+v", r)
	}
	if r := call("stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Related","op":"append_section","content":"- [[gpt-4]]","rationale":"connect related pages"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := call("stage.rename_page", `{"from":"wiki/entities/gpt-4.md","to":"wiki/entities/gpt-four.md","rationale":"canonical spelling"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := call("stage.retract", `{"page":"wiki/concepts/flash-attention.md","reason":"test tombstone"}`); r.IsError {
		t.Fatal(r.Content)
	}
	after := vaultBytes(t, dir)
	if len(before) != len(after) {
		t.Fatalf("vault file count changed: before=%d after=%d", len(before), len(after))
	}
	for p, b := range before {
		if string(after[p]) != string(b) {
			t.Errorf("working-tree file %s changed", p)
		}
	}
	_ = e
}

// TestStagePatchPageInsertBefore proves the placement the abstract backfill
// needed: the staged hunk adds the new section purely as additions, with the
// named section's heading as the first line of trailing context — i.e. the
// insert lands immediately BEFORE it, not after, and touches nothing else.
func TestStagePatchPageInsertBefore(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"insert a section above an existing one"}`)); err != nil || r.IsError {
		t.Fatalf("open: %+v %v", r, err)
	}
	r, err := reg.Call(ctx, "stage.patch_page", json.RawMessage(`{"path":"wiki/concepts/kv-cache.md","section":"## Why it matters","op":"insert_before","content":"## Trade-offs\n\nMemory grows with sequence length; that is the price of the constant per-step work.","rationale":"state the trade-off ahead of the motivation"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatal(r.Content)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 1 {
		t.Fatalf("len(cs.Ops) = %d, want 1", len(cs.Ops))
	}
	hunks := cs.Ops[0].Hunks
	if len(hunks) != 1 {
		t.Fatalf("len(hunks) = %d, want 1", len(hunks))
	}
	h := hunks[0]
	// The trailing "" is the new blank line of the seam: the differ keeps
	// the body's original blank line as leading context and attributes the
	// inserted copy to the addition — one blank line before the heading,
	// total, exactly the seam convention.
	wantAdd := []string{"## Trade-offs", "", "Memory grows with sequence length; that is the price of the constant per-step work.", ""}
	if len(h.Add) != len(wantAdd) {
		t.Fatalf("hunk Add = %q, want %q", h.Add, wantAdd)
	}
	for i := range wantAdd {
		if h.Add[i] != wantAdd[i] {
			t.Errorf("hunk Add[%d] = %q, want %q", i, h.Add[i], wantAdd[i])
		}
	}
	if len(h.Del) != 0 {
		t.Errorf("hunk Del = %q, want none — insert_before must not delete anything", h.Del)
	}
	// diffContext is 3: the last three Before lines are the trailing context,
	// and their first entry is the line that follows the insertion point. It
	// must be the named section's heading — the insert sits immediately
	// before it, never after it (the fake-prepend failure mode this op
	// exists to retire).
	if len(h.Before) < 3 || h.Before[len(h.Before)-3] != "## Why it matters" {
		t.Errorf("trailing context = %q, want it to open with %q", h.Before, "## Why it matters")
	}
}

// TestStagePatchPageRemoveSection proves a staged pure deletion: the named
// section's heading and its whole body — fenced block included — leave as
// Del lines, nothing is added, and the surviving neighbour follows as
// trailing context.
func TestStagePatchPageRemoveSection(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"remove a section"}`)); err != nil || r.IsError {
		t.Fatalf("open: %+v %v", r, err)
	}
	r, err := reg.Call(ctx, "stage.patch_page", json.RawMessage(`{"path":"wiki/concepts/kv-cache.md","section":"## Example","op":"remove_section","content":"","rationale":"drop the example section"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatal(r.Content)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 1 {
		t.Fatalf("len(cs.Ops) = %d, want 1", len(cs.Ops))
	}
	hunks := cs.Ops[0].Hunks
	if len(hunks) != 1 {
		t.Fatalf("len(hunks) = %d, want 1", len(hunks))
	}
	h := hunks[0]
	if len(h.Add) != 0 {
		t.Errorf("hunk Add = %q, want none — remove_section must not add anything", h.Add)
	}
	if len(h.Del) == 0 || h.Del[0] != "## Example" {
		t.Fatalf("hunk Del = %q, want it to open with %q", h.Del, "## Example")
	}
	// The fenced fake-heading line is plain content of Example; removing the
	// section must take it too.
	found := false
	for _, l := range h.Del {
		if l == "## this is not a heading, just log text" {
			found = true
		}
	}
	if !found {
		t.Errorf("hunk Del does not carry the removed section's fenced line:\n%q", h.Del)
	}
	if len(h.Before) < 3 || h.Before[len(h.Before)-3] != "## Related" {
		t.Errorf("trailing context = %q, want it to open with %q", h.Before, "## Related")
	}
}

func TestOutOfTaxonomyTagRejectedAtProposal(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	if r, err := reg.Call(context.Background(), "stage.open", json.RawMessage(`{"intent":"bad tag"}`)); err != nil || r.IsError {
		t.Fatalf("open: %+v %v", r, err)
	}
	r, err := reg.Call(context.Background(), "stage.create_page", json.RawMessage(`{"path":"wiki/concepts/new-page.md","title":"New","type":"concept","tags":["bogus"],"sources":["raw/papers/leviathan-2023.md"],"confidence":"medium","body":"See [[kv-cache]] and [[gpt-4]].","rationale":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError || !strings.Contains(r.Content, "taxonomy") {
		t.Fatalf("result = %+v, want taxonomy rejection", r)
	}
}

func TestRenameCascadeComplete(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	if r, err := reg.Call(context.Background(), "stage.open", json.RawMessage(`{"intent":"rename page"}`)); err != nil || r.IsError {
		t.Fatalf("open: %+v %v", r, err)
	}
	if r, err := reg.Call(context.Background(), "stage.rename_page", json.RawMessage(`{"from":"wiki/entities/gpt-4.md","to":"wiki/entities/gpt-four.md","rationale":"canonical spelling"}`)); err != nil || r.IsError {
		t.Fatalf("rename: %+v %v", r, err)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 1 || len(cs.Ops[0].Cascade) == 0 {
		t.Fatalf("changeset = %+v, want non-empty cascade", cs.Ops)
	}
	for _, sub := range cs.Ops[0].Cascade {
		if sub.Kind != stage.OpPatchPage || sub.Path == "" || len(sub.Hunks) == 0 {
			t.Errorf("bad cascade entry: %+v", sub)
		}
	}
}

func TestStageIngestDuplicateSHARejected(t *testing.T) {
	doc := &extract.Doc{Title: "Leviathan", SourceURL: "https://example.test/leviathan", Markdown: "A unique body for this test.\n", Kind: "paper", Extractor: "test"}
	reg, _, _ := engineRegistry(t, fakeExtractor{doc: doc})
	ctx := context.Background()
	for _, args := range []string{`{"intent":"ingest"}`, `{"uri":"source.md","kind":"paper"}`} {
		if strings.Contains(args, "intent") {
			if r, err := reg.Call(ctx, "stage.open", json.RawMessage(args)); err != nil || r.IsError {
				t.Fatalf("open: %+v %v", r, err)
			}
			continue
		}
		r, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(args))
		if err != nil || r.IsError {
			t.Fatalf("ingest: %+v %v", r, err)
		}
	}
	// A second extraction with the same body is rejected by the engine's sha check.
	r, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"second.md","kind":"paper"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError || !strings.Contains(r.Content, "already ingested") {
		t.Fatalf("duplicate result = %+v", r)
	}
}

func TestStageToolSchemasAndNames(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	list := reg.List()
	if len(list) != 18 {
		t.Fatalf("tool count = %d", len(list))
	}
	names := make([]string, len(list))
	for i, tool := range list {
		names[i] = tool.Name
		var v map[string]any
		if err := json.Unmarshal(tool.Schema, &v); err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Fatal("tools are not sorted")
	}
}

var _ = vault.BodySHA256

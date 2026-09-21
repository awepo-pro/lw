package tools

// staged_cascade_test.go pins 020 FIX-1's tools-side contract: a
// stage.patch_page must chain behind a rename/merge's own cascade rewrites
// (T-B review finding 1), and a staged parse failure is a turn-aborting Go
// error, not an IsError the model can argue with (T-B review finding 2) —
// except for a vault-root file, whose cascade bytes legitimately carry no
// page structure and stay on the committed "page not found" path.

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// registryAt opens an Engine over an existing vault directory (one the
// caller may have seeded before opening) and wires a Registry over it —
// engineRegistry's shape, minus its own fixture copy, for tests that need
// non-fixture bytes on disk before the engine loads them.
func registryAt(t *testing.T, dir string) (*Registry, *stage.Engine) {
	t.Helper()
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v := e.Vault()
	return NewRegistry(Deps{Vault: v, Index: index.Build(v), Engine: e, Author: stage.Author{Kind: "agent", Model: "test"}}), e
}

// TestPatchPageToolChainsBehindRename pins the cascade chaining the T-B
// review found missing: after a staged rename, the engine attaches
// OpPatchPage sub-ops to the rename's Cascade rewriting every
// inbound-linking page, and the projection applies them — so a patch on a
// rewritten neighbour must compute its base from the cascade sub-op's
// staged bytes, its Before being that sub-op's After. Before the fix,
// StagedFile scanned top-level ops only, the tool fell back to the
// committed page, and Append refused the unchained Before.
func TestPatchPageToolChainsBehindRename(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	if r := callTool(t, reg, "stage.open", `{"intent":"rename, then patch a rewritten neighbour"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := callTool(t, reg, "stage.rename_page", `{"from":"wiki/concepts/kv-cache.md","to":"wiki/concepts/kv-cache-v2.md","rationale":"versioned name"}`); r.IsError {
		t.Fatalf("rename: %s", r.Content)
	}
	// flash-attention.md links [[kv-cache]], so the rename's cascade
	// rewrites it; the staged body's link now reads [[kv-cache-v2]] and the
	// patch below must compose against THOSE bytes.
	if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/flash-attention.md","section":"## Related","op":"append_section","content":"- [[kv-cache-v2]] — the versioned page.","rationale":"note the versioned page"}`); r.IsError {
		t.Fatalf("patch on the cascade-rewritten path: %s", r.Content)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 2 {
		t.Fatalf("len(cs.Ops) = %d, want 2", len(cs.Ops))
	}
	var subAfter string
	for _, sub := range cs.Ops[0].Cascade {
		if sub.Path == "wiki/concepts/flash-attention.md" {
			subAfter = sub.After
		}
	}
	if subAfter == "" {
		t.Fatal("rename cascade carries no rewrite for flash-attention.md")
	}
	if cs.Ops[1].Before != subAfter {
		t.Fatalf("patch Before = %s, want the cascade sub-op's After %s", cs.Ops[1].Before, subAfter)
	}
}

// TestStagedParseFailureReturnsError pins the severity contract (T-B review
// finding 2): staged bytes for a COMMITTED PAGE that fail to parse are an
// internal invariant violation and surface as a non-nil Go error — the turn
// aborts, mirroring rawSourceBody — never as an IsError result. The damage
// is produced by tampering the CAS object holding the op's After sha on
// disk; because the store zlib-decompresses on Get, the tampered file must
// be a valid zlib stream of non-page bytes — literal garbage would fail
// inside Store.Get and never reach the parser.
func TestStagedParseFailureReturnsError(t *testing.T) {
	t.Run("committed page with damaged staged bytes", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		reg, e := registryAt(t, dir)
		if r := callTool(t, reg, "stage.open", `{"intent":"tamper harness"}`); r.IsError {
			t.Fatal(r.Content)
		}
		if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Why it matters","op":"replace_section","content":"Staged replacement body.","rationale":"rewrite the motivation"}`); r.IsError {
			t.Fatalf("patch: %s", r.Content)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		after := cs.Ops[0].After
		e.Close()

		obj := filepath.Join(dir, ".llmwiki", "objects", after[:2], after[2:])
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		if _, err := zw.Write([]byte("this is not a wiki page")); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(obj, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("tamper CAS object: %v", err)
		}

		e2, err := stage.OpenEngine(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer e2.Close()
		d := Deps{Vault: e2.Vault(), Engine: e2}

		_, _, _, res, ok, err := resolvePageArg(d, "wiki.get", "kv-cache")
		if ok {
			t.Fatal("resolvePageArg ok = true over damaged staged bytes")
		}
		if err == nil {
			t.Fatalf("resolvePageArg returned IsError %q with nil err; want a turn-aborting error", res.Content)
		}
		if !strings.Contains(err.Error(), "parse staged page") {
			t.Errorf("resolvePageArg err does not name the parse failure: %v", err)
		}

		_, onFail, ok, err := stagedPatchBase(d, "wiki/concepts/kv-cache.md")
		if ok {
			t.Fatal("stagedPatchBase ok = true over damaged staged bytes")
		}
		if err == nil {
			t.Fatalf("stagedPatchBase returned IsError %q with nil err; want a turn-aborting error", onFail.Content)
		}
	})

	t.Run("root file keeps the committed not-found", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		// Seed a [[gpt-4]] wikilink into curator-memory.md so the rename's
		// engine-built cascade rewrites this vault-root file — the one shape
		// whose staged bytes legitimately do not parse as a page.
		if err := os.WriteFile(filepath.Join(dir, "curator-memory.md"),
			[]byte("## Naming\n\n- Prefer the hyphenated vendor form: [[gpt-4]], not `gpt4`.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		reg, e := registryAt(t, dir)
		if r := callTool(t, reg, "stage.open", `{"intent":"rename over a root file"}`); r.IsError {
			t.Fatal(r.Content)
		}
		if r := callTool(t, reg, "stage.rename_page", `{"from":"wiki/entities/gpt-4.md","to":"wiki/entities/gpt-four.md","rationale":"canonical spelling"}`); r.IsError {
			t.Fatalf("rename: %s", r.Content)
		}

		// The rewrite IS staged — the fall-through below must be the
		// root-file classification, not a scan miss.
		if _, staged, err := e.StagedFile("curator-memory.md"); err != nil || !staged {
			t.Fatalf("StagedFile(curator-memory.md) = staged=%v err=%v, want the cascade sub-op's bytes", staged, err)
		}

		d := Deps{Vault: e.Vault(), Engine: e}
		_, onFail, ok, err := stagedPatchBase(d, "curator-memory.md")
		if ok {
			t.Fatal("stagedPatchBase ok = true for a root file")
		}
		if err != nil {
			t.Fatalf("stagedPatchBase err = %v; a root file stays on the committed not-found path, not the abort path", err)
		}
		if !onFail.IsError || !strings.Contains(onFail.Content, "not found") {
			t.Fatalf("stagedPatchBase onFail = %+v, want the committed page-not-found IsError", onFail)
		}

		// The same classification through the public tool: IsError result,
		// no Go error.
		r, err := reg.Call(context.Background(), "stage.patch_page", json.RawMessage(`{"path":"curator-memory.md","section":"## Naming","op":"append_section","content":"- a note","rationale":"test"}`))
		if err != nil {
			t.Fatalf("stage.patch_page on a root file returned a Go error: %v", err)
		}
		if !r.IsError {
			t.Fatalf("stage.patch_page on a root file = %+v, want IsError", r)
		}
	})
}

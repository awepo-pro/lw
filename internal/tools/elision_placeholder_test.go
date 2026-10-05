package tools

// elision_placeholder_test.go pins 041 A-041-5: the text a replayed history
// call carries in place of a page's text — "[elided: N bytes of page text sent
// in this call — …]", internal/agent/budget.go stagedStubFormat — is not page
// content, and a model that copies it into a call must be refused, not
// obeyed. Without the refusal a stub copied into stage.create_page's body
// stages a page whose whole text is the placeholder, and into patch_page's
// content writes the placeholder into a real page, both of which pass every
// structural rule the engine checks.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// wantElisionRefusal is the IsError text, byte for byte.
const wantElisionRefusal = "refused: this text is an elision placeholder, not page content; send the real text"

// elisionStubs are the shapes a copied placeholder takes: the real stub, the
// same behind leading whitespace (TrimSpace), and the bare prefix.
var elisionStubs = []string{
	"[elided: 9000 bytes of page text sent in this call — wiki.get returns the page's current staged or committed text]",
	"  \n\t[elided: 12 bytes of page text sent in this call — wiki.get returns the page's current staged or committed text]",
	"[elided:",
}

func TestStagePagesRefuseElisionPlaceholder(t *testing.T) {
	ctx := context.Background()
	reg, e, _ := engineRegistry(t, nil)
	call := func(name string, args map[string]any) Result {
		t.Helper()
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		r, err := reg.Call(ctx, name, b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return r
	}
	if r := call("stage.open", map[string]any{"intent": "pin 041 A-041-5"}); r.IsError {
		t.Fatal(r.Content)
	}
	ops := func() int {
		t.Helper()
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		return len(cs.Ops)
	}
	create := func(body string) map[string]any {
		return map[string]any{
			"path": "wiki/concepts/placeholder-page.md", "title": "Placeholder Page", "type": "concept",
			"tags": []string{"inference"}, "sources": []string{"raw/papers/leviathan-2023.md"},
			"confidence": "low", "contested": false, "body": body, "rationale": "pin 041",
		}
	}
	patch := func(op, section, content string) map[string]any {
		return map[string]any{
			"path": "wiki/concepts/kv-cache.md", "section": section, "op": op,
			"content": content, "rationale": "pin 041",
		}
	}

	t.Run("create_page_body", func(t *testing.T) {
		for _, stub := range elisionStubs {
			before := ops()
			r := call("stage.create_page", create(stub))
			if !r.IsError || r.Content != wantElisionRefusal {
				t.Errorf("body %q: IsError=%v content=%q, want the refusal %q", stub, r.IsError, r.Content, wantElisionRefusal)
			}
			if ops() != before {
				t.Errorf("body %q: the refused call staged an op", stub)
			}
		}
	})

	// Every op that writes the content is refused, replace_text — the op a
	// small edit uses — first among them.
	t.Run("patch_page_content", func(t *testing.T) {
		for _, op := range []string{"replace_text", "replace_section", "append_section", "insert_after", "insert_before"} {
			for _, stub := range elisionStubs {
				args := patch(op, "## Why it matters", stub)
				if op == "replace_text" {
					args["find"] = "Caching turns that into a constant amount of new work per"
				}
				before := ops()
				r := call("stage.patch_page", args)
				if !r.IsError || r.Content != wantElisionRefusal {
					t.Errorf("%s %q: IsError=%v content=%q, want the refusal %q", op, stub, r.IsError, r.Content, wantElisionRefusal)
				}
				if ops() != before {
					t.Errorf("%s %q: the refused call staged an op", op, stub)
				}
			}
		}
	})

	// The refusal is about the text, not the tool: the same calls with real
	// text stage, including text that merely MENTIONS the marker mid-way. These
	// are what keeps the pin from passing on a tool that refuses everything.
	t.Run("real_text_still_stages", func(t *testing.T) {
		mention := "See [[kv-cache]] and [[gpt-4]].\n\nA stub reads [elided: N bytes …] but this page is real text."
		before := ops()
		if r := call("stage.create_page", create(mention)); r.IsError {
			t.Fatalf("a real body mentioning the marker was refused: %s", r.Content)
		}
		if got := ops(); got != before+1 {
			t.Fatalf("ops = %d, want %d: the real create did not stage", got, before+1)
		}
		args := patch("replace_text", "## Why it matters", "Caching reduces that to a constant amount of new work per")
		args["find"] = "Caching turns that into a constant amount of new work per"
		if r := call("stage.patch_page", args); r.IsError {
			t.Fatalf("a real replace_text was refused: %s", r.Content)
		}
		if r := call("stage.patch_page", patch("append_section", "## Related", "- a note that mentions [elided: in passing] only")); r.IsError {
			t.Fatalf("a real append mentioning the marker was refused: %s", r.Content)
		}
	})

	// remove_section ignores its content, so a placeholder there changes
	// nothing and is not what the rule is about.
	t.Run("remove_section_ignores_content", func(t *testing.T) {
		r := call("stage.patch_page", patch("remove_section", "## Example", elisionStubs[0]))
		if strings.Contains(r.Content, "elision placeholder") {
			t.Errorf("remove_section was refused for a content it ignores: %s", r.Content)
		}
	})
}

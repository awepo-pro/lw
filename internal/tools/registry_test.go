package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// newTestDeps opens the vault at dir and builds a fresh index over it. The
// stage.Engine and extract.Extractor fields are left nil — no read tool
// touches either, per backbone §6 ("reads go through Deps.Vault").
func newTestDeps(t *testing.T, dir string) Deps {
	t.Helper()

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("vault.Open(%s): %v", dir, err)
	}
	return Deps{Vault: v, Index: index.Build(v)}
}

func minimalRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	return NewRegistry(newTestDeps(t, dir))
}

// wantToolNames is every tool this subtask registers, already in the sorted
// order List() must produce.
var wantToolNames = []string{
	"raw.get",
	"vault.orient",
	"wiki.backlinks",
	"wiki.get",
	"wiki.lint",
	"wiki.neighbors",
	"wiki.search",
}

func TestRegistryListCountAndOrder(t *testing.T) {
	reg := minimalRegistry(t)
	list := reg.List()

	if len(list) != 7 {
		t.Fatalf("List() returned %d tools, want 7", len(list))
	}

	var got []string
	for _, tool := range list {
		got = append(got, tool.Name)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("List() not sorted by Name: %v", got)
	}
	for i, name := range wantToolNames {
		if got[i] != name {
			t.Errorf("List()[%d] = %q, want %q (full list: %v)", i, got[i], name, got)
		}
	}
}

func TestRegistryGet(t *testing.T) {
	reg := minimalRegistry(t)

	for _, name := range wantToolNames {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("Get(%q) not found", name)
		}
	}
	if _, ok := reg.Get("fs.write"); ok {
		t.Errorf("Get(\"fs.write\") found a tool; the registry must offer no filesystem verb")
	}
}

func TestCallUnknownToolReturnsErrUnknownTool(t *testing.T) {
	reg := minimalRegistry(t)

	_, err := reg.Call(context.Background(), "does.not.exist", nil)
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("Call(unknown) error = %v, want errors.Is(err, ErrUnknownTool)", err)
	}
}

// jsonSchema is enough of json-schema's shape to check the properties the
// brief cares about, without adding a schema-validation dependency.
type jsonSchema struct {
	Type                 string                     `json:"type"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
}

func TestSchemasAreValidJSON(t *testing.T) {
	reg := minimalRegistry(t)

	for _, tool := range reg.List() {
		t.Run(tool.Name, func(t *testing.T) {
			var s jsonSchema
			if err := json.Unmarshal(tool.Schema, &s); err != nil {
				t.Fatalf("Schema is not valid JSON: %v\nschema: %s", err, tool.Schema)
			}
			if s.Type != "object" {
				t.Errorf("Schema.type = %q, want %q", s.Type, "object")
			}
			for _, req := range s.Required {
				if _, ok := s.Properties[req]; !ok {
					t.Errorf("required %q is not a key of properties %v", req, propertyNames(s.Properties))
				}
			}
		})
	}
}

func propertyNames(props map[string]json.RawMessage) []string {
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// TestReadToolsAgainstMinimal calls every one of the 7 read tools against
// spec/fixtures/minimal and asserts each returns non-empty, non-error
// Content (backbone §6, S3-T1 goal 3).
func TestReadToolsAgainstMinimal(t *testing.T) {
	reg := minimalRegistry(t)
	ctx := context.Background()

	cases := []struct {
		name string
		args string
	}{
		{"vault.orient", `{}`},
		{"wiki.search", `{"q": "cache"}`},
		{"wiki.get", `{"page": "kv-cache"}`},
		{"wiki.neighbors", `{"page": "kv-cache"}`},
		{"wiki.backlinks", `{"page": "flash-attention"}`},
		{"raw.get", `{"source": "raw/papers/leviathan-2023.md"}`},
		{"wiki.lint", `{}`},
	}
	if len(cases) != 7 {
		t.Fatalf("test table has %d cases, want 7", len(cases))
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := reg.Call(ctx, c.name, json.RawMessage(c.args))
			if err != nil {
				t.Fatalf("Call(%s) error = %v", c.name, err)
			}
			if res.IsError {
				t.Fatalf("Call(%s) returned IsError with Content: %s", c.name, res.Content)
			}
			if res.Content == "" {
				t.Fatalf("Call(%s) returned empty Content", c.name)
			}
		})
	}
}

// TestValidationFailureIsNilErrorIsError pins the §6 error convention: a
// bad argument comes back as Result{IsError:true} with a nil Go error, so
// the agent loop can feed it back to the model instead of aborting.
func TestValidationFailureIsNilErrorIsError(t *testing.T) {
	reg := minimalRegistry(t)
	ctx := context.Background()

	tests := []struct {
		name string
		tool string
		args string
	}{
		{"wiki.search missing q", "wiki.search", `{}`},
		{"wiki.get missing page", "wiki.get", `{}`},
		{"wiki.get unknown page", "wiki.get", `{"page": "no-such-page"}`},
		{"wiki.neighbors missing page", "wiki.neighbors", `{}`},
		{"wiki.backlinks missing page", "wiki.backlinks", `{}`},
		{"raw.get missing source", "raw.get", `{}`},
		{"raw.get unknown source", "raw.get", `{"source": "raw/does-not-exist.md"}`},
		{"raw.get chunk out of range", "raw.get", `{"source": "raw/papers/leviathan-2023.md", "chunk": 99}`},
		{"wiki.lint unknown check", "wiki.lint", `{"checks": ["not-a-real-check"]}`},
		{"wiki.search malformed json", "wiki.search", `{"q": `},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := reg.Call(ctx, tt.tool, json.RawMessage(tt.args))
			if err != nil {
				t.Fatalf("Call(%s) returned a Go error %v; validation failures must return a nil error", tt.tool, err)
			}
			if !res.IsError {
				t.Fatalf("Call(%s) with bad args did not set IsError; Content: %s", tt.tool, res.Content)
			}
			if res.Content == "" {
				t.Fatalf("Call(%s) IsError result has empty Content — it must say what was wrong and how to fix it", tt.tool)
			}
		})
	}
}

func TestDefinitionsMatchList(t *testing.T) {
	reg := minimalRegistry(t)
	list := reg.List()
	defs := reg.Definitions()

	if len(defs) != len(list) {
		t.Fatalf("Definitions() returned %d, List() returned %d", len(defs), len(list))
	}
	for i, d := range defs {
		if d.Name != list[i].Name {
			t.Errorf("Definitions()[%d].Name = %q, want %q", i, d.Name, list[i].Name)
		}
		if d.Description != list[i].Description {
			t.Errorf("Definitions()[%d].Description mismatch for %q", i, d.Name)
		}
		if string(d.Parameters) != string(list[i].Schema) {
			t.Errorf("Definitions()[%d].Parameters mismatch for %q", i, d.Name)
		}
	}
}

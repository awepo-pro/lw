package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// hintIngest runs stage.ingest_source with the raw args JSON given, so a
// test controls the exact argument object — including whether the optional
// name hint is present at all. Only reg.Call's error is fatal; a test
// asserts on the Result itself (naming tests assert on both shapes).
func hintIngest(t *testing.T, reg *Registry, args string) Result {
	t.Helper()
	r, err := reg.Call(context.Background(), "stage.ingest_source", json.RawMessage(args))
	if err != nil {
		t.Fatalf("stage.ingest_source(%s): %v", args, err)
	}
	return r
}

// TestSourceNameHint pins 009 idea 011: the optional name argument on
// stage.ingest_source names a source whose title slugs to nothing (a CJK
// title like 四元數簡介 landed at untitled.md, because slug.Make keeps
// ASCII only). The order 008 §4.1 pinned is extended, not replaced: title,
// then name, then basename minus one extension, then "untitled" — and the
// -n collision suffix and the engine's validator apply to a name-derived
// path exactly as to any other.
func TestSourceNameHint(t *testing.T) {
	t.Run("cjk_title_uses_name", func(t *testing.T) {
		reg, e := namingRegistry(t, nil, namingDoc("四元數簡介", "# 四元數簡介\n\nQuaternion algebra, briefly.\n"))
		nameOpen(t, reg)
		r := hintIngest(t, reg, `{"uri":"四元數.md","kind":"article","name":"Quaternion Introduction"}`)
		if r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/quaternion-introduction.md" {
			t.Fatalf("staged at %s, want raw/articles/quaternion-introduction.md", got)
		}
	})

	t.Run("latin_title_ignores_name", func(t *testing.T) {
		reg, e := namingRegistry(t, nil, namingDoc("Quaternion Notes", "# Quaternion Notes\n\nA Latin title keeps its precedence.\n"))
		nameOpen(t, reg)
		r := hintIngest(t, reg, `{"uri":"/x/whatever.md","kind":"article","name":"other"}`)
		if r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/quaternion-notes.md" {
			t.Fatalf("staged at %s, want raw/articles/quaternion-notes.md (title wins over name)", got)
		}
	})

	t.Run("unusable_name_falls_back_to_basename", func(t *testing.T) {
		// A name that slugs to nothing (CJK only) contributes no file name,
		// exactly like an empty one: the basename fallback still fires.
		reg, e := namingRegistry(t, nil, namingDoc("四元數簡介", "# 四元數簡介\n\nA CJK hint slugs to nothing.\n"))
		nameOpen(t, reg)
		r := hintIngest(t, reg, `{"uri":"notes.md","kind":"article","name":"四元"}`)
		if r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/notes.md" {
			t.Fatalf("staged at %s, want raw/articles/notes.md", got)
		}
	})

	t.Run("no_name_keeps_untitled", func(t *testing.T) {
		// Without the hint the 1ba9387 behaviour is unchanged: a CJK title
		// and a CJK basename still land at untitled.md.
		reg, e := namingRegistry(t, nil, namingDoc("四元數簡介", "No heading, nothing slugifiable at all.\n"))
		nameOpen(t, reg)
		r := hintIngest(t, reg, `{"uri":"/downloads/四元數.md","kind":"article"}`)
		if r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/untitled.md" {
			t.Fatalf("staged at %s, want raw/articles/untitled.md", got)
		}
	})

	t.Run("name_collision_suffixes", func(t *testing.T) {
		// A name-derived candidate that already holds a DIFFERENT source
		// gets the first free -2 suffix, never a refusal and never a
		// silent overwrite.
		pre := []namedFile{{rel: "raw/articles/quaternion-introduction.md", body: "# Quaternion Introduction\n\nAlready committed body.\n"}}
		reg, e := namingRegistry(t, pre, namingDoc("四元數簡介", "# Quaternion Introduction\n\nA different incoming body.\n"))
		nameOpen(t, reg)
		r := hintIngest(t, reg, `{"uri":"四元數.md","kind":"article","name":"Quaternion Introduction"}`)
		if r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := proposedRawDiff(t, e).Path; got != "raw/articles/quaternion-introduction-2.md" {
			t.Fatalf("staged at %s, want raw/articles/quaternion-introduction-2.md", got)
		}
	})

	t.Run("name_path_passes_validator", func(t *testing.T) {
		// The op staged through name is an ordinary ingest_source op: the
		// engine's validator accepts it and it is live in the open
		// changeset, ready for review and Append.
		reg, e := namingRegistry(t, nil, namingDoc("四元數簡介", "# 四元數簡介\n\nA body the validator must accept at the hinted path.\n"))
		nameOpen(t, reg)
		r := hintIngest(t, reg, `{"uri":"四元數.md","kind":"article","name":"Quaternion Introduction"}`)
		if r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, op := range cs.Live() {
			if op.Kind == "ingest_source" && op.Path == "raw/articles/quaternion-introduction.md" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no live ingest_source op at raw/articles/quaternion-introduction.md; ops = %+v", cs.Live())
		}
	})
}

// TestIngestSourceSchemaName pins the schema half of 009 idea 011: the
// optional name property exists, is not required, and the handler still
// rejects an argument object carrying a field the schema does not declare
// (additionalProperties: false is enforced, not decorative).
func TestIngestSourceSchemaName(t *testing.T) {
	t.Run("schema_accepts_name", func(t *testing.T) {
		var schema struct {
			Properties struct {
				Name struct {
					Type string `json:"type"`
				} `json:"name"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal([]byte(stageIngestSourceSchema), &schema); err != nil {
			t.Fatalf("schema does not decode: %v", err)
		}
		if schema.Properties.Name.Type != "string" {
			t.Fatalf("properties.name.type = %q, want \"string\"", schema.Properties.Name.Type)
		}
		if slices.Contains(schema.Required, "name") {
			t.Fatalf("required = %v, want name absent (it is optional)", schema.Required)
		}
	})

	t.Run("unknown_field_still_rejected", func(t *testing.T) {
		reg, _ := namingRegistry(t, nil, namingDoc("四元數簡介", "Body.\n"))
		r := hintIngest(t, reg, `{"uri":"x.md","nom":"y"}`)
		if !r.IsError {
			t.Fatalf("an undeclared argument field was accepted: %s", r.Content)
		}
		if !strings.Contains(r.Content, "arguments are not valid JSON for stage.ingest_source") {
			t.Errorf("result = %q, want the existing bad-args text", r.Content)
		}
	})
}

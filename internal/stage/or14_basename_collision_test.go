// or14_basename_collision_test.go pins MASTER §10 OR-14: validateCreatePage
// refuses a page whose basename already answers a bare wikilink elsewhere in
// the vault.
//
// Orchestrator-owned. The rule lives in validate.go (S2-T2's file) and the
// behaviour it guards is a property of vault.Resolve (S1's), so it is a
// cross-subtask contract MASTER §8 rule 3 reserves.
package stage

import (
	"strings"
	"testing"
)

// or14EntityPageContent is newConceptPageContent's wiki/entities twin —
// type: entity, so path.Dir matches PageType.Dir() — with the two outbound
// wikilinks validateCreatePage demands. Local to this file rather than added
// to helpers_test.go, which S2-T2 owns.
func or14EntityPageContent(title string) []byte {
	return []byte("---\n" +
		"title: " + title + "\n" +
		"created: 2026-08-29\n" +
		"updated: 2026-08-29\n" +
		"type: entity\n" +
		"tags: [llm]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# " + title + "\n" +
		"\n" +
		"See [[flash-attention]] and [[gpt-4]] for background.\n")
}

// TestCreatePageRefusesABasenameCollision measures the whole point: without
// the rule the create is ACCEPTED and the damage only shows up as broken
// links in files the model never touched.
func TestCreatePageRefusesABasenameCollision(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("collide", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	// wiki/concepts/kv-cache.md already exists in the minimal fixture.
	_, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/entities/kv-cache.md",
		Content:    or14EntityPageContent("KV Cache"),
		Rationale:  "regression fixture",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	})
	if err == nil {
		t.Fatal("Append accepted a basename collision; every [[kv-cache]] in the vault would resolve to nothing")
	}
	for _, want := range []string{"wiki/concepts/kv-cache.md", "kv-cache"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q, so the reviewer cannot act on it: %v", want, err)
		}
	}
}

// TestBasenameCollisionIsCaseInsensitive pins the helper's comparison
// directly, at the unit level, and the comment explains why it is not an
// Append test.
//
// Measured: Append can never exercise this branch, because requireValidPath
// rejects "wiki/entities/KV-Cache.md" first — "path … is not a
// vault-relative, slash-separated, lowercase-hyphen.md path". An earlier
// draft of this test asserted an Append refusal and passed WITHOUT the
// OR-14 rule at all, for that unrelated reason; it was rewritten rather
// than kept as false evidence.
//
// The lowercasing stays because it mirrors resolveByBasename exactly
// (backbone §2.9): if the path convention is ever relaxed, this rule must
// still agree with the Resolve behaviour it exists to protect. Unreachable
// today, like diff.go's cascadePathSet guard, and documented as such rather
// than "fixed".
func TestBasenameCollisionIsCaseInsensitive(t *testing.T) {
	e, _ := newTestEngine(t)

	other, ok := basenameCollision(e.vault, "wiki/entities/KV-Cache.md")
	if !ok {
		t.Fatal("basenameCollision missed a case-differing basename; it must match resolveByBasename, which lowercases both sides")
	}
	if other != "wiki/concepts/kv-cache.md" {
		t.Errorf("collided with %q, want wiki/concepts/kv-cache.md", other)
	}
}

// TestCreatePageAllowsADistinctBasename is the paired positive assertion:
// the rule must not refuse an ordinary create. Without this, OR-14 could be
// "satisfied" by refusing everything.
func TestCreatePageAllowsADistinctBasename(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("no collision", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/entities/or14-distinct-page.md",
		Content:    or14EntityPageContent("OR14 Distinct Page"),
		Rationale:  "regression fixture",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append refused a page with no basename collision: %v", err)
	}
}

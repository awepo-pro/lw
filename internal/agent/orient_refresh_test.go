package agent

import (
	"encoding/json"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
)

// TestOrientDigestRefreshesOnChangesetChange pins the agent-layer requirement
// of 067 S1: ContextBuilder digest for one session id refreshes after an Append,
// after a DropOp and after a commit; an unchanged state returns the cached
// digest without calling the tool (counted).
func TestOrientDigestRefreshesOnChangesetChange(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "agent", Model: "test"}
	reg := tools.NewRegistry(tools.Deps{
		Vault:   e.Vault(),
		Index:   e.Index(),
		Engine:  e,
		Author:  author,
		Extract: extract.NewFile(),
	})

	var orientCalls int
	cleanup := tools.SetTestOrientCallHook(func() {
		orientCalls++
	})
	defer cleanup()

	b := NewContextBuilder(e.Vault(), reg, 100_000)
	s := &Session{ID: "session-1", ChangesetID: "session-1"}

	// Open a changeset
	_, err = e.OpenChangeset("orient refresh test", author)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Initial Build fetches digest through vault.orient
	if orientCalls != 0 {
		t.Fatalf("orientCalls before initial Build = %d, want 0", orientCalls)
	}
	msgs1, err := b.Build(s, "initial user msg")
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	if orientCalls != 1 {
		t.Fatalf("orientCalls after initial Build = %d, want 1", orientCalls)
	}
	digest1 := msgs1[2].Content

	// 2. Unchanged state returns cached digest without calling vault.orient
	msgs2, err := b.Build(s, "second user msg")
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	if orientCalls != 1 {
		t.Fatalf("orientCalls after unchanged Build = %d, want 1 (cache hit)", orientCalls)
	}
	if msgs2[2].Content != digest1 {
		t.Fatalf("cached digest content changed unexpectedly")
	}

	// 3. Append op 1
	body1 := "# Staged Concept 1\n\nContent 1.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args1 := map[string]any{
		"path":       "wiki/concepts/staged-1.md",
		"title":      "Staged Concept 1",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body1,
		"rationale":  "create staged 1",
	}
	b1, _ := json.Marshal(args1)
	if res, err := reg.Call(t.Context(), "stage.create_page", b1); err != nil || res.IsError {
		t.Fatalf("stage.create_page 1: %v, content=%s", err, res.Content)
	}

	// Digest must refresh after Append
	msgs3, err := b.Build(s, "third user msg")
	if err != nil {
		t.Fatalf("Build 3: %v", err)
	}
	if orientCalls != 2 {
		t.Fatalf("orientCalls after Append = %d, want 2 (refetched)", orientCalls)
	}
	digest3 := msgs3[2].Content
	if digest3 == digest1 {
		t.Fatalf("digest after Append did not change")
	}

	// Unchanged again -> cached
	_, err = b.Build(s, "fourth user msg")
	if err != nil {
		t.Fatalf("Build 4: %v", err)
	}
	if orientCalls != 2 {
		t.Fatalf("orientCalls after unchanged = %d, want 2", orientCalls)
	}

	// Append op 2 so we can drop it
	body2 := "# Staged Concept 2\n\nContent 2.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args2 := map[string]any{
		"path":       "wiki/concepts/staged-2.md",
		"title":      "Staged Concept 2",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body2,
		"rationale":  "create staged 2",
	}
	b2, _ := json.Marshal(args2)
	if res, err := reg.Call(t.Context(), "stage.create_page", b2); err != nil || res.IsError {
		t.Fatalf("stage.create_page 2: %v, content=%s", err, res.Content)
	}

	// Build after Append op 2
	msgs3b, err := b.Build(s, "user msg after append 2")
	if err != nil {
		t.Fatalf("Build 3b: %v", err)
	}
	if orientCalls != 3 {
		t.Fatalf("orientCalls after Append 2 = %d, want 3", orientCalls)
	}
	digest3b := msgs3b[2].Content
	if digest3b == digest3 {
		t.Fatalf("digest after Append 2 did not change")
	}

	// 4. Drop op 2
	cs, err := e.Current()
	if err != nil || len(cs.Live()) < 2 {
		t.Fatalf("want at least 2 live ops")
	}
	op2ID := cs.Live()[1].ID
	if err := e.DropOp(op2ID); err != nil {
		t.Fatalf("DropOp: %v", err)
	}

	// Digest must refresh after DropOp
	msgs4, err := b.Build(s, "fifth user msg")
	if err != nil {
		t.Fatalf("Build 5: %v", err)
	}
	if orientCalls != 4 {
		t.Fatalf("orientCalls after DropOp = %d, want 4", orientCalls)
	}
	digest4 := msgs4[2].Content
	if digest4 == digest3b {
		t.Fatalf("digest after DropOp did not change from digest3b")
	}

	// Unchanged again -> cached
	_, err = b.Build(s, "sixth user msg")
	if err != nil {
		t.Fatalf("Build 6: %v", err)
	}
	if orientCalls != 4 {
		t.Fatalf("orientCalls after unchanged = %d, want 4", orientCalls)
	}

	// 5. Commit remaining op(s)
	if _, err := e.Commit("commit remaining op"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Digest must refresh after Commit
	msgs5, err := b.Build(s, "seventh user msg")
	if err != nil {
		t.Fatalf("Build 7: %v", err)
	}
	if orientCalls != 5 {
		t.Fatalf("orientCalls after Commit = %d, want 5", orientCalls)
	}
	digest5 := msgs5[2].Content
	if digest5 == digest4 {
		t.Fatalf("digest after Commit did not change")
	}

	// Unchanged again -> cached
	_, err = b.Build(s, "eighth user msg")
	if err != nil {
		t.Fatalf("Build 8: %v", err)
	}
	if orientCalls != 5 {
		t.Fatalf("orientCalls after commit unchanged = %d, want 5", orientCalls)
	}
}

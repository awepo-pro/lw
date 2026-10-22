package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
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

	askPlan := planFor(trace.VerbAsk)

	// 1. Initial Build fetches digest through vault.orient
	if orientCalls != 0 {
		t.Fatalf("orientCalls before initial Build = %d, want 0", orientCalls)
	}
	msgs1, err := b.buildFor(s, "initial user msg", askPlan)
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	if orientCalls != 1 {
		t.Fatalf("orientCalls after initial Build = %d, want 1", orientCalls)
	}
	digest1 := msgs1[2].Content

	// 2. Unchanged state returns cached digest without calling vault.orient
	msgs2, err := b.buildFor(s, "second user msg", askPlan)
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
	msgs3, err := b.buildFor(s, "third user msg", askPlan)
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
	_, err = b.buildFor(s, "fourth user msg", askPlan)
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
	msgs3b, err := b.buildFor(s, "user msg after append 2", askPlan)
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
	msgs4, err := b.buildFor(s, "fifth user msg", askPlan)
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
	_, err = b.buildFor(s, "sixth user msg", askPlan)
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
	msgs5, err := b.buildFor(s, "seventh user msg", askPlan)
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
	_, err = b.buildFor(s, "eighth user msg", askPlan)
	if err != nil {
		t.Fatalf("Build 8: %v", err)
	}
	if orientCalls != 5 {
		t.Fatalf("orientCalls after commit unchanged = %d, want 5", orientCalls)
	}
}

// TestOrientSectionOnlyInReadOnlyVerbs pins 067 FIX-W3 (W3-1):
// same split for vault.orient (ask, query, file show section; ingest, lint, "" do not);
// and a ContextBuilder digest built for an ingest-plan turn has no section while an ask-plan turn's has it.
func TestOrientSectionOnlyInReadOnlyVerbs(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
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

	cs, err := e.OpenChangeset("orient verbs test", author)
	if err != nil {
		t.Fatal(err)
	}

	// Stage a page
	body := "# Staged Orient Page\n\nBody.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args := map[string]any{
		"path":       "wiki/concepts/staged-orient.md",
		"title":      "Staged Orient Page",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body,
		"rationale":  "orient verb test page",
	}
	argBytes, _ := json.Marshal(args)
	if res, err := reg.Call(context.Background(), "stage.create_page", argBytes); err != nil || res.IsError {
		t.Fatalf("stage.create_page: %v, content=%s", err, res.Content)
	}

	const sectionHeader = "## Open changeset (unreviewed)"

	// 1. vault.orient tool split:
	// ask, query, file → section present
	for _, verb := range []string{trace.VerbAsk, trace.VerbQuery, trace.VerbFile} {
		t.Run("orient_tool_"+verb, func(t *testing.T) {
			ctx := trace.WithVerb(context.Background(), verb)
			res, err := reg.Call(ctx, "vault.orient", json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("vault.orient %s error: %v", verb, err)
			}
			if !strings.Contains(res.Content, sectionHeader) {
				t.Fatalf("vault.orient %s missing section header:\n%s", verb, res.Content)
			}
			if !strings.Contains(res.Content, cs.ID) {
				t.Fatalf("vault.orient %s missing changeset id %s:\n%s", verb, cs.ID, res.Content)
			}
			if !strings.Contains(res.Content, "wiki/concepts/staged-orient.md — Staged Orient Page") {
				t.Fatalf("vault.orient %s missing staged line:\n%s", verb, res.Content)
			}
		})
	}

	// ingest, lint, "" → section NOT present (output == committed-only baseline)
	resBaseline, err := reg.Call(context.Background(), "vault.orient", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resBaseline.Content, sectionHeader) {
		t.Fatalf("vault.orient under \"\" unexpectedly contains open changeset section")
	}

	for _, verb := range []string{trace.VerbIngest, trace.VerbLint, ""} {
		t.Run("orient_tool_"+verb, func(t *testing.T) {
			ctx := context.Background()
			if verb != "" {
				ctx = trace.WithVerb(ctx, verb)
			}
			res, err := reg.Call(ctx, "vault.orient", json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("vault.orient %q error: %v", verb, err)
			}
			if strings.Contains(res.Content, sectionHeader) {
				t.Fatalf("vault.orient %q unexpectedly contains open changeset section:\n%s", verb, res.Content)
			}
			if res.Content != resBaseline.Content {
				t.Fatalf("vault.orient %q output differs from baseline:\nGot:\n%s\nWant:\n%s", verb, res.Content, resBaseline.Content)
			}
		})
	}

	// 2. ContextBuilder digest built for an ingest-plan turn vs ask-plan turn
	b := NewContextBuilder(e.Vault(), reg, 100_000)
	s := &Session{ID: "session-orient-verbs", ChangesetID: cs.ID}

	// Ingest-plan turn: has no section
	ingestPlan := planFor(trace.VerbIngest)
	msgsIngest, err := b.buildFor(s, "ingest query", ingestPlan)
	if err != nil {
		t.Fatalf("buildFor ingest: %v", err)
	}
	digestIngest := msgsIngest[2].Content
	if strings.Contains(digestIngest, sectionHeader) {
		t.Fatalf("ContextBuilder digest for ingest plan unexpectedly contains open changeset section:\n%s", digestIngest)
	}

	// Ask-plan turn: has section
	askPlan := planFor(trace.VerbAsk)
	msgsAsk, err := b.buildFor(s, "ask query", askPlan)
	if err != nil {
		t.Fatalf("buildFor ask: %v", err)
	}
	digestAsk := msgsAsk[2].Content
	if !strings.Contains(digestAsk, sectionHeader) {
		t.Fatalf("ContextBuilder digest for ask plan missing open changeset section:\n%s", digestAsk)
	}
	if !strings.Contains(digestAsk, cs.ID) || !strings.Contains(digestAsk, "wiki/concepts/staged-orient.md") {
		t.Fatalf("ContextBuilder digest for ask plan missing staged page details:\n%s", digestAsk)
	}
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestCmdStageFromFixtureOpensChangeset exercises the exact invocation
// gate G2 runs: staging spec/fixtures/ops/create-page.json into a fresh
// vault with no open changeset. It must open one (Author.Kind == "human",
// intent "cli stage import") and print the new cs-… id.
func TestCmdStageFromFixtureOpensChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	fixture := stageFixturePath(t, "create-page.json")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"stage", "--vault", root, "--from", fixture})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "staged 1 op(s) into cs-") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "staged 1 op(s) into cs-")
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if c.Author.Kind != "human" {
		t.Errorf("Author.Kind = %q, want %q", c.Author.Kind, "human")
	}
	if c.Intent != "cli stage import" {
		t.Errorf("Intent = %q, want %q", c.Intent, "cli stage import")
	}
	if len(c.Ops) != 1 {
		t.Fatalf("len(Ops) = %d, want 1", len(c.Ops))
	}
	if c.Ops[0].Kind != stage.OpCreatePage {
		t.Errorf("Ops[0].Kind = %q, want %q", c.Ops[0].Kind, stage.OpCreatePage)
	}
	if c.Ops[0].Path != "wiki/concepts/paged-attention.md" {
		t.Errorf("Ops[0].Path = %q, want wiki/concepts/paged-attention.md", c.Ops[0].Path)
	}
	// Append assigns id/state even though the fixture omits both.
	if c.Ops[0].ID != "op1" {
		t.Errorf("Ops[0].ID = %q, want op1", c.Ops[0].ID)
	}
	if c.Ops[0].State != stage.StateProposed {
		t.Errorf("Ops[0].State = %q, want %q", c.Ops[0].State, stage.StateProposed)
	}
}

// TestCmdStageAppendsIntoExistingOpenChangeset runs the hidden verb twice
// against the same vault: the second call must append into the changeset
// the first call opened, not open (or refuse to open) a second one.
func TestCmdStageAppendsIntoExistingOpenChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	fixture := stageFixturePath(t, "create-page.json")

	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"stage", "--vault", root, "--from", fixture})
	}); code != 0 {
		t.Fatalf("first stage: exit = %d, stderr=%q", code, stderr)
	}

	second := writeStageOpsFile(t, []stageTestOp{{
		Op:         "create_page",
		Path:       "wiki/concepts/second-page.md",
		Rationale:  "second op in the same changeset",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
		Content:    newStagePageContent(t, "Second Page"),
	}})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"stage", "--vault", root, "--from", second})
	})
	if code != 0 {
		t.Fatalf("second stage: exit = %d, stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "staged 1 op(s) into cs-") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "staged 1 op(s) into cs-")
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(c.Ops) != 2 {
		t.Fatalf("len(Ops) = %d, want 2 (both stage calls in one changeset)", len(c.Ops))
	}
}

// TestCmdStageMissingFromIsUsageError covers backbone §13's Contract: a
// usage error prints its own usage and exits 2.
func TestCmdStageMissingFromIsUsageError(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"stage", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

// TestCmdStageBadFlagIsUsageError covers the flag.ContinueOnError path.
func TestCmdStageBadFlagIsUsageError(t *testing.T) {
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"stage", "--bogusflag"})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

// TestCmdStageIsHiddenFromUsage pins D-AL: the verb dispatches but never
// appears in --help output.
func TestCmdStageIsHiddenFromUsage(t *testing.T) {
	stdout, _, code := captureRun(t, func() int {
		return run([]string{"--help"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.Contains(stdout, "stage") {
		t.Errorf("usage output mentions %q, want the hidden stage verb absent:\n%s", "stage", stdout)
	}

	found := false
	for _, v := range verbs {
		if v.name == "stage" {
			found = true
		}
	}
	if !found {
		t.Error(`verbs table has no "stage" entry`)
	}
}

// TestLoadStageOpsRecoversNestedCascadeContent exercises the format pinned
// by backbone §13 D-AL / wave-8 entry item 4 directly: Op.Content is
// json:"-", so --from's decoder needs a second pass to recover "content"
// at both the top level and inside a nested "cascade" entry — the shape a
// hand-built patch_page-with-cascade fixture could legally carry (Append
// only recomputes Cascade itself for rename_page/merge_pages).
func TestLoadStageOpsRecoversNestedCascadeContent(t *testing.T) {
	raw := `[
		{
			"op": "patch_page",
			"path": "wiki/concepts/kv-cache.md",
			"section": "## Related",
			"before": "deadbeef",
			"content": "top-level content",
			"cascade": [
				{
					"op": "patch_page",
					"path": "index.md",
					"content": "nested content"
				}
			]
		}
	]`

	dir := t.TempDir()
	path := filepath.Join(dir, "ops.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ops, err := loadStageOps(path)
	if err != nil {
		t.Fatalf("loadStageOps: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("len(ops) = %d, want 1", len(ops))
	}
	if string(ops[0].Content) != "top-level content" {
		t.Errorf("top-level Content = %q, want %q", ops[0].Content, "top-level content")
	}
	if len(ops[0].Cascade) != 1 {
		t.Fatalf("len(Cascade) = %d, want 1", len(ops[0].Cascade))
	}
	if string(ops[0].Cascade[0].Content) != "nested content" {
		t.Errorf("Cascade[0].Content = %q, want %q", ops[0].Cascade[0].Content, "nested content")
	}
	// "content" must never leak onto the wire-format Op itself: it has no
	// json tag matching "content", so re-marshaling must not emit it.
	b, err := json.Marshal(ops[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "content") {
		t.Errorf("marshaled Op leaks a content key: %s", b)
	}
}

// --- fixtures -----------------------------------------------------------

// stageFixturePath resolves a file under this subtask's
// spec/fixtures/ops/ directory, relative to the module root testutil
// already knows how to find.
func stageFixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(testutil.FixtureRoot(t), "ops", name)
}

// stageTestOp is the --from input shape (backbone §13 D-AL): stage.Op's
// own JSON tags plus one extra "content" key. Defined locally, rather
// than imported, because it exists only for these tests to build
// well-formed fixture files with Go's own JSON encoder instead of
// hand-escaped literals.
type stageTestOp struct {
	Op         string   `json:"op"`
	Path       string   `json:"path"`
	Rationale  string   `json:"rationale"`
	Provenance []string `json:"provenance"`
	Content    string   `json:"content"`
}

// writeStageOpsFile marshals ops to a temp JSON file and returns its path.
func writeStageOpsFile(t *testing.T, ops []stageTestOp) string {
	t.Helper()
	b, err := json.MarshalIndent(ops, "", "  ")
	if err != nil {
		t.Fatalf("marshal ops: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ops.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// newStagePageContent returns a well-formed wiki/concepts page body, valid
// against the minimal fixture's schema, with >= 2 outbound wikilinks to
// pages that already exist there (the shape validateCreatePage demands).
func newStagePageContent(t *testing.T, title string) string {
	t.Helper()
	return "---\n" +
		"title: " + title + "\n" +
		"created: 2026-08-30\n" +
		"updated: 2026-08-30\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# " + title + "\n" +
		"\n" +
		"See [[kv-cache]] and [[flash-attention]] for background.\n"
}

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

const fbW = "wiki/concepts/fb-test.md"

// setupFBVault creates a test vault containing fbW with the given frontmatter
// and body, creates raw source files raw/articles/a.md and raw/articles/b.md,
// opens the engine and changeset, and returns the registry, engine, and root.
func setupFBVault(t *testing.T, fm, body string) (*Registry, *stage.Engine, string) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")

	rawA := "---\ntitle: Article A\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: article\nsources: []\nconfidence: high\n---\n\nRaw article A content.\n"
	rawB := "---\ntitle: Article B\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: article\nsources: []\nconfidence: high\n---\n\nRaw article B content.\n"
	if err := os.WriteFile(filepath.Join(dir, "raw", "articles", "a.md"), []byte(rawA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw", "articles", "b.md"), []byte(rawB), 0o644); err != nil {
		t.Fatal(err)
	}

	content := fm + body
	if err := os.WriteFile(filepath.Join(dir, fbW), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })

	v := e.Vault()
	reg := NewRegistry(Deps{Vault: v, Index: index.Build(v), Engine: e, Author: stage.Author{Kind: "agent", Model: "test"}})
	if r := callTool(t, reg, "stage.open", `{"intent":"fb test"}`); r.IsError {
		t.Fatalf("stage.open: %s", r.Content)
	}
	return reg, e, dir
}

// defaultFBMixin returns standard valid frontmatter for fbW.
func defaultFBMixin(sources []string) string {
	srcStr := "[]"
	if len(sources) > 0 {
		srcStr = "[" + strings.Join(sources, ", ") + "]"
	}
	return "---\ntitle: FB Test\ncreated: 2026-08-20\nupdated: 2026-08-29\ntype: concept\ntags: [inference, memory]\nsources: " + srcStr + "\nconfidence: high\n---\n\n"
}

// TestPatchPageRefusesFrontmatterBlock (workflow 081, FB.1) pins the refusal
// when any stage.patch_page op that writes content is passed content containing
// a frontmatter block.
func TestPatchPageRefusesFrontmatterBlock(t *testing.T) {
	fm := defaultFBMixin([]string{"raw/articles/a.md"})
	body := "Lead paragraph.\n\n## Section A\n\nContent A.\n\n## Section B\n\nContent B.\n"

	cases := []struct {
		name       string
		op         string
		section    string
		find       string
		content    string
		matchedKey string
	}{
		{
			name:       "replace_section lead with title key",
			op:         "replace_section",
			section:    "",
			content:    "---\ntitle: P\ncreated: 2026-09-14\n---\n\nLead.",
			matchedKey: "title",
		},
		{
			name:       "append_section with sources key",
			op:         "append_section",
			section:    "## Section A",
			content:    "x\n\n---\nsources: [raw/articles/a.md]\n---",
			matchedKey: "sources",
		},
		{
			name:       "replace_text with type key",
			op:         "replace_text",
			section:    "## Section A",
			find:       "Content A.",
			content:    "---\ntype: concept\n",
			matchedKey: "type",
		},
		{
			name:       "insert_after with tags key",
			op:         "insert_after",
			section:    "## Section A",
			content:    "## Section New\n\n---\ntags: [gpu]\n",
			matchedKey: "tags",
		},
		{
			name:       "insert_before with confidence key",
			op:         "insert_before",
			section:    "## Section B",
			content:    "## Section New\n\n---\nconfidence: high\n",
			matchedKey: "confidence",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, e, _ := setupFBVault(t, fm, body)
			payload := map[string]any{
				"path":      fbW,
				"section":   tc.section,
				"op":        tc.op,
				"content":   tc.content,
				"rationale": "test refusal",
			}
			if tc.find != "" {
				payload["find"] = tc.find
			}
			b, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			res := callTool(t, reg, "stage.patch_page", string(b))
			if !res.IsError {
				t.Fatalf("expected error result, got success: %s", res.Content)
			}
			wantMsg := "content contains a frontmatter block (a \"---\" line followed by \"" + tc.matchedKey + ":\"); frontmatter is not page text and no patch op writes it. To add raw sources the body cites to sources:, use op sync_sources. Send the content without the block."
			if res.Content != wantMsg {
				t.Fatalf("refusal content =\n%q\nwant =\n%q", res.Content, wantMsg)
			}
			cs, err := e.Current()
			if err != nil {
				t.Fatal(err)
			}
			if len(cs.Live()) != 0 {
				t.Fatalf("live ops = %d, want 0", len(cs.Live()))
			}
		})
	}
}

// TestPatchPageAllowsNonFrontmatterDashes (workflow 081, FB.2) pins that
// dashes not followed by a frontmatter key line stage normally.
func TestPatchPageAllowsNonFrontmatterDashes(t *testing.T) {
	fm := defaultFBMixin([]string{"raw/articles/a.md"})
	body := "Lead paragraph.\n\n## Section A\n\nContent A.\n"

	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "dashes between prose lines",
			content: "a\n\n---\n\nb",
		},
		{
			name:    "dashes followed by non key line",
			content: "---\nnot a key line",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, e, _ := setupFBVault(t, fm, body)
			payload := map[string]any{
				"path":      fbW,
				"section":   "## Section A",
				"op":        "replace_section",
				"content":   tc.content,
				"rationale": "test valid dashes",
			}
			b, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			res := callTool(t, reg, "stage.patch_page", string(b))
			if res.IsError {
				t.Fatalf("unexpected error for %q: %s", tc.content, res.Content)
			}
			cs, err := e.Current()
			if err != nil {
				t.Fatal(err)
			}
			if len(cs.Live()) != 1 {
				t.Fatalf("live ops = %d, want 1", len(cs.Live()))
			}
		})
	}
}

// TestCreatePageRefusesFrontmatterBlock (workflow 081, FB.3) pins that
// stage.create_page refuses a body that contains a frontmatter block.
func TestCreatePageRefusesFrontmatterBlock(t *testing.T) {
	reg, e, _ := setupFBVault(t, defaultFBMixin([]string{"raw/articles/a.md"}), "Lead.\n\n## Section A\n\nContent.\n")

	payload := map[string]any{
		"path":       "wiki/concepts/new-concept.md",
		"title":      "New Concept",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/a.md"},
		"confidence": "high",
		"contested":  false,
		"body":       "---\ntitle: P\ncreated: 2026-09-14\n---\n\nLead.\n\n## Abstract\n\nBody text.\n",
		"rationale":  "new page",
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	res := callTool(t, reg, "stage.create_page", string(b))
	if !res.IsError {
		t.Fatalf("expected error result, got: %s", res.Content)
	}
	wantMsg := "body contains a frontmatter block (a \"---\" line followed by \"title:\"); frontmatter comes from the create_page fields, not the body. Send the body without the block."
	if res.Content != wantMsg {
		t.Fatalf("refusal content =\n%q\nwant =\n%q", res.Content, wantMsg)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Live()) != 0 {
		t.Fatalf("live ops = %d, want 0", len(cs.Live()))
	}
}

// TestPatchPageSyncSources (workflow 081, FB.4) pins op sync_sources:
// adds raw sources cited in body to sources: without changing the body,
// carrying sourcesNote and composing against staged base.
func TestPatchPageSyncSources(t *testing.T) {
	fm := defaultFBMixin([]string{"raw/articles/a.md"})
	body := "Lead paragraph citing ^[raw/articles/a.md] and ^[raw/articles/b.md].\n\n## Abstract\n\nBody text.\n"

	reg, e, _ := setupFBVault(t, fm, body)

	payload := map[string]any{
		"path":      fbW,
		"section":   "",
		"op":        "sync_sources",
		"content":   "",
		"rationale": "sync cited sources",
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	res := callTool(t, reg, "stage.patch_page", string(b))
	if res.IsError {
		t.Fatalf("sync_sources failed: %s", res.Content)
	}

	wantNote := "note: added raw/articles/b.md to sources: (the body cites them)."
	if !strings.Contains(res.Content, wantNote) {
		t.Fatalf("res.Content does not contain sourcesNote %q:\n%s", wantNote, res.Content)
	}

	stagedBytes, ok, err := e.StagedFile(fbW)
	if err != nil || !ok {
		t.Fatalf("StagedFile ok=%v err=%v", ok, err)
	}
	page, err := vault.ParsePage(fbW, stagedBytes)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}

	wantSources := []string{"raw/articles/a.md", "raw/articles/b.md"}
	if !reflect.DeepEqual(page.FM.Sources, wantSources) {
		t.Fatalf("page.FM.Sources = %v, want %v", page.FM.Sources, wantSources)
	}
	if page.Body != body {
		t.Fatalf("page.Body =\n%q\nwant byte-identical base body =\n%q", page.Body, body)
	}

	// Composing with staged base: stage another patch on top of the sync_sources op.
	patchPayload := map[string]any{
		"path":      fbW,
		"section":   "## Abstract",
		"op":        "replace_text",
		"find":      "Body text.",
		"content":   "Updated body text.",
		"rationale": "update text",
	}
	pb, _ := json.Marshal(patchPayload)
	pres := callTool(t, reg, "stage.patch_page", string(pb))
	if pres.IsError {
		t.Fatalf("replace_text failed: %s", pres.Content)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	live := cs.Live()
	if len(live) != 2 {
		t.Fatalf("live ops = %d, want 2", len(live))
	}
	if live[1].Before != live[0].After {
		t.Fatalf("Op 2 Before = %s, want Op 1 After = %s", live[1].Before, live[0].After)
	}

	stagedBytes2, ok, err := e.StagedFile(fbW)
	if err != nil || !ok {
		t.Fatalf("StagedFile ok=%v err=%v", ok, err)
	}
	page2, err := vault.ParsePage(fbW, stagedBytes2)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if !reflect.DeepEqual(page2.FM.Sources, wantSources) {
		t.Fatalf("page2.FM.Sources = %v, want %v", page2.FM.Sources, wantSources)
	}
	wantBody2 := "Lead paragraph citing ^[raw/articles/a.md] and ^[raw/articles/b.md].\n\n## Abstract\n\nUpdated body text.\n"
	if page2.Body != wantBody2 {
		t.Fatalf("page2.Body =\n%q\nwant =\n%q", page2.Body, wantBody2)
	}
}

// TestPatchPageSyncSourcesNoChange (workflow 081, FB.5) pins that sync_sources
// with nothing to add returns the R2 no-change refusal with 0 ops staged.
func TestPatchPageSyncSourcesNoChange(t *testing.T) {
	fm := defaultFBMixin([]string{"raw/articles/a.md"})
	body := "Lead paragraph citing ^[raw/articles/a.md].\n\n## Abstract\n\nBody text.\n"

	reg, e, _ := setupFBVault(t, fm, body)

	payload := map[string]any{
		"path":      fbW,
		"section":   "",
		"op":        "sync_sources",
		"content":   "",
		"rationale": "sync cited sources",
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	res := callTool(t, reg, "stage.patch_page", string(b))
	if !res.IsError {
		t.Fatalf("expected error result, got: %s", res.Content)
	}
	wantMsg := "no change: this patch leaves " + fbW + " exactly as it already reads (staged edits included); nothing was staged."
	if res.Content != wantMsg {
		t.Fatalf("res.Content =\n%q\nwant =\n%q", res.Content, wantMsg)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Live()) != 0 {
		t.Fatalf("live ops = %d, want 0", len(cs.Live()))
	}
}

// TestPatchPageSyncSourcesMissingSourcesKey (workflow 081, FB.6) pins that
// sync_sources on a page with no sources: key returns a frozen refusal.
func TestPatchPageSyncSourcesMissingSourcesKey(t *testing.T) {
	fm := "---\ntitle: FB Test\ncreated: 2026-08-20\nupdated: 2026-08-29\ntype: concept\ntags: [inference, memory]\nconfidence: high\n---\n\n"
	body := "Lead paragraph citing ^[raw/articles/a.md].\n\n## Abstract\n\nBody text.\n"

	reg, e, _ := setupFBVault(t, fm, body)

	payload := map[string]any{
		"path":      fbW,
		"section":   "",
		"op":        "sync_sources",
		"content":   "",
		"rationale": "sync cited sources",
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	res := callTool(t, reg, "stage.patch_page", string(b))
	if !res.IsError {
		t.Fatalf("expected error result, got: %s", res.Content)
	}
	wantMsg := "sync_sources needs a sources: key; this page has none, so the reviewer adds it by hand"
	if res.Content != wantMsg {
		t.Fatalf("res.Content =\n%q\nwant =\n%q", res.Content, wantMsg)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Live()) != 0 {
		t.Fatalf("live ops = %d, want 0", len(cs.Live()))
	}
}

// TestPatchPageSchemaSyncSources (workflow 081, FB.7) pins that the stage.patch_page
// schema includes sync_sources in the op enum and description.
func TestPatchPageSchemaSyncSources(t *testing.T) {
	reg, _, _ := setupFBVault(t, defaultFBMixin([]string{"raw/articles/a.md"}), "Lead.\n")
	tool, ok := reg.Get("stage.patch_page")
	if !ok {
		t.Fatal("stage.patch_page not registered")
	}

	var schema struct {
		Properties map[string]struct {
			Type        string   `json:"type"`
			Description string   `json:"description"`
			Enum        []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatal(err)
	}

	wantEnum := []string{"replace_text", "replace_section", "append_section", "insert_after", "insert_before", "remove_section", "sync_sources"}
	if !reflect.DeepEqual(schema.Properties["op"].Enum, wantEnum) {
		t.Fatalf("op enum = %#v, want %#v", schema.Properties["op"].Enum, wantEnum)
	}

	wantSuffix := ` sync_sources: add every raw source the body cites to sources: (no text change; send section "" and content "").`
	if !strings.HasSuffix(schema.Properties["op"].Description, wantSuffix) {
		t.Fatalf("op description does not end with %q:\n%s", wantSuffix, schema.Properties["op"].Description)
	}
}

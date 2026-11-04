package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

const reachW = "wiki/concepts/reach.md"

// setupReachVault creates a test vault containing reachW with the given body
// and an open changeset.
func setupReachVault(t *testing.T, body string) (*Registry, *stage.Engine, string) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	fm := "---\ntitle: Reach\ncreated: 2026-08-20\nupdated: 2026-08-29\ntype: concept\ntags: [inference, memory]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: high\n---\n\n"
	content := fm + body
	if err := os.WriteFile(filepath.Join(dir, reachW), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v := e.Vault()
	reg := NewRegistry(Deps{Vault: v, Index: index.Build(v), Engine: e, Author: stage.Author{Kind: "agent", Model: "test"}})
	if r := callTool(t, reg, "stage.open", `{"intent":"reach test"}`); r.IsError {
		t.Fatalf("stage.open: %s", r.Content)
	}
	return reg, e, dir
}

// assertReachStaged verifies that the tool call succeeded and exactly one live op was staged
// whose parsed page body equals expectedBody.
func assertReachStaged(t *testing.T, e *stage.Engine, res Result, expectedBody string) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool call returned error: %s", res.Content)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("e.Current: %v", err)
	}
	liveOps := cs.Live()
	if len(liveOps) != 1 {
		t.Fatalf("live ops count = %d, want 1", len(liveOps))
	}
	b, ok, err := e.StagedFile(reachW)
	if err != nil || !ok {
		t.Fatalf("StagedFile ok=%v err=%v", ok, err)
	}
	p, err := vault.ParsePage(reachW, b)
	if err != nil {
		t.Fatalf("parse staged page %s: %v", reachW, err)
	}
	if p.Body != expectedBody {
		t.Fatalf("staged body =\n%q\nwant =\n%q", p.Body, expectedBody)
	}
}

// TestReach_F2_1_NoOpRefusal pins workflow 081, F2.1 (R2):
// body ## Related\n\n- [[a]]\n; replace_section ## Related content - [[a]] -> IsError, content exactly
// "no change: this patch leaves W exactly as it already reads (staged edits included); nothing was staged."
// live ops unchanged.
func TestReach_F2_1_NoOpRefusal(t *testing.T) {
	const body = "## Related\n\n- [[a]]\n"
	reg, e, _ := setupReachVault(t, body)

	args := `{"path":"` + reachW + `","section":"## Related","op":"replace_section","content":"- [[a]]","rationale":"same content"}`
	res := callTool(t, reg, "stage.patch_page", args)
	if !res.IsError {
		t.Fatalf("expected error result, got: %+v", res)
	}
	want := fmt.Sprintf("no change: this patch leaves %s exactly as it already reads (staged edits included); nothing was staged.", reachW)
	if res.Content != want {
		t.Fatalf("content =\n%q\nwant\n%q", res.Content, want)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	liveOps := cs.Live()
	if len(liveOps) != 0 {
		t.Fatalf("live ops = %d, want 0", len(liveOps))
	}
}

// TestReach_F2_2_NoOpRepeatRefusal pins workflow 081, F2.2 (R2):
// same page; two identical calls replace_section ## Related content - [[a]]\n- [[b]] ->
// the first stages, the second returns the F2.1 refusal; changeset has exactly 1 live op.
func TestReach_F2_2_NoOpRepeatRefusal(t *testing.T) {
	const body = "## Related\n\n- [[a]]\n"
	reg, e, _ := setupReachVault(t, body)

	args := `{"path":"` + reachW + `","section":"## Related","op":"replace_section","content":"- [[a]]\n- [[b]]","rationale":"add b"}`
	res1 := callTool(t, reg, "stage.patch_page", args)
	if res1.IsError {
		t.Fatalf("first call failed: %s", res1.Content)
	}

	res2 := callTool(t, reg, "stage.patch_page", args)
	if !res2.IsError {
		t.Fatalf("second call expected refusal, got: %+v", res2)
	}
	want := fmt.Sprintf("no change: this patch leaves %s exactly as it already reads (staged edits included); nothing was staged.", reachW)
	if res2.Content != want {
		t.Fatalf("content =\n%q\nwant\n%q", res2.Content, want)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	liveOps := cs.Live()
	if len(liveOps) != 1 {
		t.Fatalf("live ops = %d, want 1", len(liveOps))
	}
}

// TestReach_F2_3_LeadReplaceTextSpellings pins workflow 081, F2.3 (R3):
// body B1 = "Lead para.\n\n## Abstract\n\nX.\n"; for each section spelling "", preamble, (preamble),
// " Preamble ": replace_text find Lead para. content New lead. -> staged body "New lead.\n\n## Abstract\n\nX.\n",
// op.Section "(preamble)". (Fresh vault per spelling.)
func TestReach_F2_3_LeadReplaceTextSpellings(t *testing.T) {
	const b1 = "Lead para.\n\n## Abstract\n\nX.\n"
	spellings := []string{"", "preamble", "(preamble)", " Preamble "}

	for _, spelling := range spellings {
		t.Run("spelling "+spelling, func(t *testing.T) {
			reg, e, _ := setupReachVault(t, b1)
			m := map[string]any{
				"path":      reachW,
				"section":   spelling,
				"op":        "replace_text",
				"find":      "Lead para.",
				"content":   "New lead.",
				"rationale": "update lead",
			}
			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			res := callTool(t, reg, "stage.patch_page", string(b))
			assertReachStaged(t, e, res, "New lead.\n\n## Abstract\n\nX.\n")

			cs, err := e.Current()
			if err != nil {
				t.Fatal(err)
			}
			if cs.Ops[0].Section != "(preamble)" {
				t.Fatalf("op.Section = %q, want %q", cs.Ops[0].Section, "(preamble)")
			}
		})
	}
}

// TestReach_F2_4_LeadReplaceSection pins workflow 081, F2.4 (R3):
// B1, replace_section "" content Fresh lead. -> staged body Fresh lead.\n\n## Abstract\n\nX.\n.
func TestReach_F2_4_LeadReplaceSection(t *testing.T) {
	const b1 = "Lead para.\n\n## Abstract\n\nX.\n"
	reg, e, _ := setupReachVault(t, b1)

	args := `{"path":"` + reachW + `","section":"","op":"replace_section","content":"Fresh lead.","rationale":"replace lead"}`
	res := callTool(t, reg, "stage.patch_page", args)
	assertReachStaged(t, e, res, "Fresh lead.\n\n## Abstract\n\nX.\n")
}

// TestReach_F2_5_LeadAppendSection pins workflow 081, F2.5 (R3):
// B1, append_section "" content More. -> staged body Lead para.\n\nMore.\n\n## Abstract\n\nX.\n.
func TestReach_F2_5_LeadAppendSection(t *testing.T) {
	const b1 = "Lead para.\n\n## Abstract\n\nX.\n"
	reg, e, _ := setupReachVault(t, b1)

	args := `{"path":"` + reachW + `","section":"","op":"append_section","content":"More.","rationale":"append to lead"}`
	res := callTool(t, reg, "stage.patch_page", args)
	assertReachStaged(t, e, res, "Lead para.\n\nMore.\n\n## Abstract\n\nX.\n")
}

// TestReach_F2_6_LeadInsertAfter pins workflow 081, F2.6 (R3):
// B1, insert_after "" content ## Overview\n\nO. -> staged body
// Lead para.\n\n## Overview\n\nO.\n\n## Abstract\n\nX.\n.
func TestReach_F2_6_LeadInsertAfter(t *testing.T) {
	const b1 = "Lead para.\n\n## Abstract\n\nX.\n"
	reg, e, _ := setupReachVault(t, b1)

	args := `{"path":"` + reachW + `","section":"","op":"insert_after","content":"## Overview\n\nO.","rationale":"insert overview after lead"}`
	res := callTool(t, reg, "stage.patch_page", args)
	assertReachStaged(t, e, res, "Lead para.\n\n## Overview\n\nO.\n\n## Abstract\n\nX.\n")
}

// TestReach_F2_7_LeadRemoveSection pins workflow 081, F2.7 (R3):
// B1, remove_section "" -> staged body ## Abstract\n\nX.\n.
func TestReach_F2_7_LeadRemoveSection(t *testing.T) {
	const b1 = "Lead para.\n\n## Abstract\n\nX.\n"
	reg, e, _ := setupReachVault(t, b1)

	args := `{"path":"` + reachW + `","section":"","op":"remove_section","content":"","rationale":"remove lead"}`
	res := callTool(t, reg, "stage.patch_page", args)
	assertReachStaged(t, e, res, "## Abstract\n\nX.\n")
}

// TestReach_F2_8_LeadInsertBeforeRefusal pins workflow 081, F2.8 (R3):
// B1, insert_before "" -> IsError, exactly:
// "insert_before cannot target the lead: nothing comes before it. Use insert_after on the lead, or insert_before on the first heading."
func TestReach_F2_8_LeadInsertBeforeRefusal(t *testing.T) {
	const b1 = "Lead para.\n\n## Abstract\n\nX.\n"
	reg, _, _ := setupReachVault(t, b1)

	args := `{"path":"` + reachW + `","section":"","op":"insert_before","content":"## Intro\n\nI.","rationale":"insert before lead"}`
	res := callTool(t, reg, "stage.patch_page", args)
	if !res.IsError {
		t.Fatalf("expected error result, got: %+v", res)
	}
	const want = "insert_before cannot target the lead: nothing comes before it. Use insert_after on the lead, or insert_before on the first heading."
	if res.Content != want {
		t.Fatalf("content =\n%q\nwant\n%q", res.Content, want)
	}
}

// TestReach_F2_9_LeadDuplicateFrontmatter pins workflow 081, F2.9 (R3, the s66 shape):
// body B2 = ---\ntitle: X\n---\n\nLead.\n\n## Abstract\n\nX.\n; replace_text "" find
// ---\ntitle: X\n---\n\n content "" (empty) -> staged body Lead.\n\n## Abstract\n\nX.\n.
// If the parser makes title: X a setext heading so this cannot pass, STOP: BLOCKED with the evidence.
func TestReach_F2_9_LeadDuplicateFrontmatter(t *testing.T) {
	const b2 = "---\ntitle: X\n---\n\nLead.\n\n## Abstract\n\nX.\n"
	reg, e, _ := setupReachVault(t, b2)

	m := map[string]any{
		"path":      reachW,
		"section":   "",
		"op":        "replace_text",
		"find":      "---\ntitle: X\n---\n\n",
		"content":   "",
		"rationale": "strip duplicate frontmatter block",
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	res := callTool(t, reg, "stage.patch_page", string(b))
	assertReachStaged(t, e, res, "Lead.\n\n## Abstract\n\nX.\n")
}

// TestReach_F2_10_FrontmatterClauseRefusal pins workflow 081, F2.10 (R3):
// section frontmatter and --- -> IsError = the section-not-found message followed by the new clause (exact):
// ". Frontmatter is not a section and no patch op writes it; sources: follows the page's citations automatically. The text between the frontmatter and the first heading is section \"\" (the lead)."
func TestReach_F2_10_FrontmatterClauseRefusal(t *testing.T) {
	const body = "## Abstract\n\nX.\n"
	reg, _, _ := setupReachVault(t, body)

	const newClause = `. Frontmatter is not a section and no patch op writes it; sources: follows the page's citations automatically. The text between the frontmatter and the first heading is section "" (the lead).`

	for _, sec := range []string{"frontmatter", "---", "Frontmatter", " --- "} {
		t.Run("section "+sec, func(t *testing.T) {
			m := map[string]any{
				"path":      reachW,
				"section":   sec,
				"op":        "replace_section",
				"content":   "test",
				"rationale": "test",
			}
			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			res := callTool(t, reg, "stage.patch_page", string(b))
			if !res.IsError {
				t.Fatalf("expected error result, got: %+v", res)
			}
			if !strings.HasSuffix(res.Content, newClause) {
				t.Fatalf("res.Content does not end with newClause:\n%q\nwant suffix:\n%q", res.Content, newClause)
			}
		})
	}
}

// TestReach_F2_11_EngineCommitAndDropHunk pins workflow 081, F2.11 (R3 engine):
// commit of the F2.3 changeset writes W with body "New lead.\n\n## Abstract\n\nX.\n";
// and in a fresh copy, dropping that op's only hunk (the Review drop API) leaves the projected W equal to the base page.
func TestReach_F2_11_EngineCommitAndDropHunk(t *testing.T) {
	const b1 = "Lead para.\n\n## Abstract\n\nX.\n"

	t.Run("commit writes file to disk", func(t *testing.T) {
		reg, e, dir := setupReachVault(t, b1)
		m := map[string]any{
			"path":      reachW,
			"section":   "",
			"op":        "replace_text",
			"find":      "Lead para.",
			"content":   "New lead.",
			"rationale": "update lead",
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		res := callTool(t, reg, "stage.patch_page", string(b))
		if res.IsError {
			t.Fatalf("patch failed: %s", res.Content)
		}

		if _, err := e.Commit("commit F2.3 lead patch"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		diskBytes, err := os.ReadFile(filepath.Join(dir, reachW))
		if err != nil {
			t.Fatal(err)
		}
		p, err := vault.ParsePage(reachW, diskBytes)
		if err != nil {
			t.Fatalf("parse committed page: %v", err)
		}
		const want = "New lead.\n\n## Abstract\n\nX.\n"
		if p.Body != want {
			t.Fatalf("committed page body =\n%q\nwant\n%q", p.Body, want)
		}
	})

	t.Run("dropping hunk in fresh copy leaves projected page equal to base", func(t *testing.T) {
		reg, e, dir := setupReachVault(t, b1)
		baseBytes, err := os.ReadFile(filepath.Join(dir, reachW))
		if err != nil {
			t.Fatal(err)
		}
		basePage, err := vault.ParsePage(reachW, baseBytes)
		if err != nil {
			t.Fatal(err)
		}

		m := map[string]any{
			"path":      reachW,
			"section":   "",
			"op":        "replace_text",
			"find":      "Lead para.",
			"content":   "New lead.",
			"rationale": "update lead",
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		res := callTool(t, reg, "stage.patch_page", string(b))
		if res.IsError {
			t.Fatalf("patch failed: %s", res.Content)
		}

		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		if len(cs.Ops) != 1 || len(cs.Ops[0].Hunks) != 1 {
			t.Fatalf("expected 1 op with 1 hunk, got ops=%d hunks=%d", len(cs.Ops), len(cs.Ops[0].Hunks))
		}
		op := cs.Ops[0]
		hunk := op.Hunks[0]

		if err := e.DropHunk(op.ID, hunk.ID); err != nil {
			t.Fatalf("DropHunk: %v", err)
		}

		projectedBytes, ok, err := e.StagedFile(reachW)
		if err != nil || !ok {
			t.Fatalf("StagedFile ok=%v err=%v", ok, err)
		}
		projectedPage, err := vault.ParsePage(reachW, projectedBytes)
		if err != nil {
			t.Fatalf("parse projected page: %v", err)
		}
		if projectedPage.Body != basePage.Body {
			t.Fatalf("projected page body after drop =\n%q\nwant equal to base\n%q", projectedPage.Body, basePage.Body)
		}
	})
}

const b3 = "## What it is\n## What it is\nBody.\n\n## Related\n\n- a\n\n## Related\n\n- b\n"

// TestReach_F2_12_DuplicateHeadingRemoveSectionFirst pins workflow 081, F2.12 (R4):
// B3, remove_section ## What it is (no occurrence) -> staged body:
// "## What it is\nBody.\n\n## Related\n\n- a\n\n## Related\n\n- b\n"
func TestReach_F2_12_DuplicateHeadingRemoveSectionFirst(t *testing.T) {
	reg, e, _ := setupReachVault(t, b3)

	args := `{"path":"` + reachW + `","section":"## What it is","op":"remove_section","content":"","rationale":"remove first copy"}`
	res := callTool(t, reg, "stage.patch_page", args)
	want := "## What it is\nBody.\n\n## Related\n\n- a\n\n## Related\n\n- b\n"
	assertReachStaged(t, e, res, want)
}

// TestReach_F2_13_DuplicateHeadingRemoveSectionSecond pins workflow 081, F2.13 (R4):
// B3, remove_section ## Related occurrence 2 -> staged body:
// "## What it is\n## What it is\nBody.\n\n## Related\n\n- a\n"
func TestReach_F2_13_DuplicateHeadingRemoveSectionSecond(t *testing.T) {
	reg, e, _ := setupReachVault(t, b3)

	args := `{"path":"` + reachW + `","section":"## Related","occurrence":2,"op":"remove_section","content":"","rationale":"remove second copy"}`
	res := callTool(t, reg, "stage.patch_page", args)
	want := "## What it is\n## What it is\nBody.\n\n## Related\n\n- a\n"
	assertReachStaged(t, e, res, want)
}

// TestReach_F2_14_DuplicateHeadingReplaceTextSecond pins workflow 081, F2.14 (R4):
// B3, replace_text ## Related occurrence 2 find - b content - c -> staged body = B3 with the last - b replaced by - c.
func TestReach_F2_14_DuplicateHeadingReplaceTextSecond(t *testing.T) {
	reg, e, _ := setupReachVault(t, b3)

	m := map[string]any{
		"path":       reachW,
		"section":    "## Related",
		"occurrence": 2,
		"op":         "replace_text",
		"find":       "- b",
		"content":    "- c",
		"rationale":  "replace in second copy",
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	res := callTool(t, reg, "stage.patch_page", string(b))
	want := "## What it is\n## What it is\nBody.\n\n## Related\n\n- a\n\n## Related\n\n- c\n"
	assertReachStaged(t, e, res, want)
}

// TestReach_F2_15_OccurrenceOutOfRangeRefusal pins workflow 081, F2.15 (R4):
// B3, remove_section ## Related occurrence 3 -> IsError exactly
// "section \"## Related\" occurs 2 times on W; occurrence must be 1 to 2".
// occurrence -1 -> IsError exactly "occurrence must be 1 or more".
// occurrence 0 behaves as omitted (1).
func TestReach_F2_15_OccurrenceOutOfRangeRefusal(t *testing.T) {
	t.Run("occurrence 3 out of range", func(t *testing.T) {
		reg, _, _ := setupReachVault(t, b3)
		args := `{"path":"` + reachW + `","section":"## Related","occurrence":3,"op":"remove_section","content":"","rationale":"remove third copy"}`
		res := callTool(t, reg, "stage.patch_page", args)
		if !res.IsError {
			t.Fatalf("expected error result, got: %+v", res)
		}
		want := fmt.Sprintf(`section "## Related" occurs 2 times on %s; occurrence must be 1 to 2`, reachW)
		if res.Content != want {
			t.Fatalf("content =\n%q\nwant\n%q", res.Content, want)
		}
	})

	t.Run("occurrence -1 negative", func(t *testing.T) {
		reg, _, _ := setupReachVault(t, b3)
		args := `{"path":"` + reachW + `","section":"## Related","occurrence":-1,"op":"remove_section","content":"","rationale":"negative occurrence"}`
		res := callTool(t, reg, "stage.patch_page", args)
		if !res.IsError {
			t.Fatalf("expected error result, got: %+v", res)
		}
		const want = "occurrence must be 1 or more"
		if res.Content != want {
			t.Fatalf("content =\n%q\nwant\n%q", res.Content, want)
		}
	})

	t.Run("occurrence 0 behaves as omitted (1)", func(t *testing.T) {
		reg, e, _ := setupReachVault(t, b3)
		args := `{"path":"` + reachW + `","section":"## What it is","occurrence":0,"op":"remove_section","content":"","rationale":"remove first copy"}`
		res := callTool(t, reg, "stage.patch_page", args)
		want := "## What it is\nBody.\n\n## Related\n\n- a\n\n## Related\n\n- b\n"
		assertReachStaged(t, e, res, want)
	})
}

// TestReach_F2_16_OccurrenceAboveOneOpRefusal pins workflow 081, F2.16 (R4):
// B3, append_section ## Related occurrence 2 -> IsError exactly:
// "occurrence above 1 works with remove_section and replace_text only. To merge two copies, append the second copy's text to the first (occurrence 1), then remove_section the second (occurrence 2)."
func TestReach_F2_16_OccurrenceAboveOneOpRefusal(t *testing.T) {
	reg, _, _ := setupReachVault(t, b3)

	args := `{"path":"` + reachW + `","section":"## Related","occurrence":2,"op":"append_section","content":"- c","rationale":"append to second copy"}`
	res := callTool(t, reg, "stage.patch_page", args)
	if !res.IsError {
		t.Fatalf("expected error result, got: %+v", res)
	}
	const want = "occurrence above 1 works with remove_section and replace_text only. To merge two copies, append the second copy's text to the first (occurrence 1), then remove_section the second (occurrence 2)."
	if res.Content != want {
		t.Fatalf("content =\n%q\nwant\n%q", res.Content, want)
	}
}

// TestReach_F2_17_HeadingLineInFindHint pins workflow 081, F2.17 (R4 hint) and FE.4 (amendment A-081-6):
// B3, replace_text ## What it is find ## What it is\n## What it is content ## What it is ->
// IsError = today's find-not-found text followed by (exact)
// " (find includes the heading line \"## What it is\", which is not part of the section's text; to delete a duplicated heading, use op remove_section on the copy with no text under it; occurrence picks the copy, 1 is the first)".
// The hint is added whenever a line of find, trimmed of trailing spaces/tabs, equals the target section's heading line.
func TestReach_F2_17_HeadingLineInFindHint(t *testing.T) {
	reg, _, _ := setupReachVault(t, b3)

	m := map[string]any{
		"path":      reachW,
		"section":   "## What it is",
		"op":        "replace_text",
		"find":      "## What it is\n## What it is",
		"content":   "## What it is",
		"rationale": "dedupe heading",
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	res := callTool(t, reg, "stage.patch_page", string(b))
	if !res.IsError {
		t.Fatalf("expected error result, got: %+v", res)
	}
	todayPrefix := fmt.Sprintf("find text was not found in section %q of %s; copy it exactly from wiki.get (whitespace and punctuation included)", "## What it is", reachW)
	const wantHint = ` (find includes the heading line "## What it is", which is not part of the section's text; to delete a duplicated heading, use op remove_section on the copy with no text under it; occurrence picks the copy, 1 is the first)`
	want := todayPrefix + wantHint
	if res.Content != want {
		t.Fatalf("content =\n%q\nwant\n%q", res.Content, want)
	}
}

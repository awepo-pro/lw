// review_test.go drives the review screen's mutation path over a real
// engine: `y`/`n`/`A` against the changeset's hunk state, and the commit
// gate `C` runs (lint regression, stale refusal) — asserting engine state
// and the StatusReporter message, not view text.
package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestAcceptDropHunk drops one hunk and accepts another (a no-op success,
// since hunks are live by default — C-89/D-CL) over a two-hunk patch_page
// op, then asserts the *engine's* changeset state, not the view text
// (s4-tui.md S4-T3 item 7).
func TestAcceptDropHunk(t *testing.T) {
	d, e, _ := newTestDeps(t, "minimal")

	if _, err := e.OpenChangeset("two related edits", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	oldFlash := "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newFlash := "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
	oldSpec := "- [[speculative-decoding]] — both the draft and target model read the cache"
	newSpec := "- [[speculative-decoding]] — the draft model proposes; the target model verifies"
	if !strings.Contains(page.Body, oldFlash) || !strings.Contains(page.Body, oldSpec) {
		t.Fatalf("fixture body does not contain the expected lines:\n%s", page.Body)
	}

	newBody := strings.Replace(page.Body, oldFlash, newFlash, 1)
	newBody = strings.Replace(newBody, oldSpec, newSpec, 1)
	rewritten := *page
	rewritten.Body = newBody

	opID, err := e.Append(stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      page.Path,
		Section:   "## Related",
		Before:    page.SHA256(),
		Content:   rewritten.Serialize(),
		Rationale: "sharpen two related-links",
		Hunks: []stage.Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldFlash}, Add: []string{newFlash}},
			{ID: "h2", Path: page.Path, Del: []string{oldSpec}, Add: []string{newSpec}},
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	m := initModel(t, d)
	m = send(t, m, keyPress('n')) // drop h1 (cursor starts at the first stop)
	m = send(t, m, keyPress('y')) // accept h2: already live, a successful no-op

	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok := cs.Op(opID)
	if !ok {
		t.Fatal("op not found after review actions")
	}
	if len(op.Hunks) != 2 {
		t.Fatalf("op.Hunks = %v, want 2 entries", op.Hunks)
	}
	if !op.Hunks[0].Dropped {
		t.Error("h1 should be Dropped after `n`")
	}
	if op.Hunks[1].Dropped {
		t.Error("h2 should remain live after `y` on an already-live hunk")
	}

	fd := findFileDiff(t, mustDiff(t, e), opID)
	if strings.Contains(fd.New, newFlash) {
		t.Error("dropped hunk's replacement text must not appear in the projected content")
	}
	if !strings.Contains(fd.New, oldFlash) {
		t.Error("dropping h1 should leave the original flash-attention line in place")
	}
	if !strings.Contains(fd.New, newSpec) {
		t.Error("h2's replacement text should appear in the projected content")
	}
}

// TestDropThenAcceptRestoresHunk presses `n` then `y` on the same hunk and
// asserts the projected content is restored byte-exactly — comparing the
// Diff's rendered content before and after, not just the Dropped flag
// (s4-tui.md S4-T3 item 7).
func TestDropThenAcceptRestoresHunk(t *testing.T) {
	d, e, _ := newTestDeps(t, "minimal")

	if _, err := e.OpenChangeset("one edit", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	oldLine := "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newLine := "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
	if !strings.Contains(page.Body, oldLine) {
		t.Fatalf("fixture body does not contain %q", oldLine)
	}
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)

	opID, err := e.Append(stage.Op{
		Kind:    stage.OpPatchPage,
		Path:    page.Path,
		Section: "## Related",
		Before:  page.SHA256(),
		Content: rewritten.Serialize(),
		Hunks: []stage.Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldLine}, Add: []string{newLine}},
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	beforeFD := findFileDiff(t, mustDiff(t, e), opID)

	m := initModel(t, d)
	m = send(t, m, keyPress('n')) // drop h1
	m = send(t, m, keyPress('y')) // accept h1 again: must restore it exactly

	afterFD := findFileDiff(t, mustDiff(t, e), opID)
	if afterFD.New != beforeFD.New {
		t.Errorf("content after drop-then-accept is not byte-exact:\nbefore=%q\nafter =%q", beforeFD.New, afterFD.New)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok := cs.Op(opID)
	if !ok {
		t.Fatal("op not found")
	}
	if op.Hunks[0].Dropped {
		t.Error("h1 should be live again after drop-then-accept")
	}
}

// TestAcceptAllRefusedWhenLintDirty presses `A` over the dirty fixture
// (which lints error-dirty by construction) and asserts the refusal is
// visible in View and that no hunk changed state (s4-tui.md S4-T3 item 8;
// /docs/design.md §14's review-fatigue mitigation).
func TestAcceptAllRefusedWhenLintDirty(t *testing.T) {
	d, e, _ := newTestDeps(t, "dirty")

	if _, err := e.OpenChangeset("harmless patch", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/long-page.md")
	if !ok {
		t.Fatal("dirty fixture missing wiki/concepts/long-page.md")
	}
	oldLine := "- [[rogue-tag]] — another fixture page in this vault."
	newLine := "- [[rogue-tag]] — retitled for this test, still another fixture page."
	if !strings.Contains(page.Body, oldLine) {
		t.Fatalf("fixture body does not contain %q:\n%s", oldLine, page.Body)
	}
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)

	opID, err := e.Append(stage.Op{
		Kind:    stage.OpPatchPage,
		Path:    page.Path,
		Section: "## Related",
		Before:  page.SHA256(),
		Content: rewritten.Serialize(),
		Hunks: []stage.Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldLine}, Add: []string{newLine}},
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Drop the hunk up front so a refused accept-all has something
	// observable to leave untouched.
	if err := e.DropHunk(opID, "h1"); err != nil {
		t.Fatalf("DropHunk: %v", err)
	}

	m := initModel(t, d)
	m = send(t, m, keyPress('A'))

	// The refusal is the StatusReporter message now (contract §5): the
	// footer shows it styled by level, and the pane draws no status line
	// of its own.
	msg, level := statusOf(t, m)
	if !strings.Contains(msg, "accept-all refused") {
		t.Errorf("Status does not show the accept-all refusal: %q", msg)
	}
	if level != ui.StatusWarn {
		t.Errorf("accept-all refusal level = %v, want StatusWarn", level)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok := cs.Op(opID)
	if !ok {
		t.Fatal("op missing after A")
	}
	if !op.Hunks[0].Dropped {
		t.Error("a refused accept-all must not undrop a hunk that was dropped before it ran")
	}
}

// TestStaleOpBlocksCommit makes an op stale by writing its target path
// directly (bypassing the engine) and refreshing, presses `C`, and asserts
// the commit is refused with the refusal visible in View (s4-tui.md S4-T3
// item 7).
func TestStaleOpBlocksCommit(t *testing.T) {
	d, e, root := newTestDeps(t, "minimal")

	if _, err := e.OpenChangeset("will go stale", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	content := []byte("---\n" +
		"title: Will Go Stale\n" +
		"created: 2026-08-30\n" +
		"updated: 2026-08-30\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Will Go Stale\n" +
		"\n" +
		"See [[kv-cache]] and [[flash-attention]] for background.\n")
	if _, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/will-go-stale.md",
		Content:    content,
		Rationale:  "test fixture",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Race: the target path is written directly, out from under the open
	// changeset — create_page/ingest_source go stale when their target
	// path now exists (backbone §5.4 D-AJ).
	full := filepath.Join(root, filepath.FromSlash("wiki/concepts/will-go-stale.md"))
	if err := os.WriteFile(full, []byte("raced\n"), 0o644); err != nil {
		t.Fatalf("write raced file: %v", err)
	}
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	m := initModel(t, d)
	m = send(t, m, keyPress('C'))

	// The refusal surfaces through StatusReporter (contract §5); the old
	// assertion read it out of View, which no longer carries a status
	// line (00-conventions.md §5, MASTER §8).
	msg, level := statusOf(t, m)
	if !strings.Contains(msg, "commit refused") || !strings.Contains(msg, "stale") {
		t.Errorf("Status does not show the stale-commit refusal: %q", msg)
	}
	if level != ui.StatusWarn {
		t.Errorf("stale-commit refusal level = %v, want StatusWarn", level)
	}

	if _, err := e.Current(); err != nil {
		t.Errorf("Current after a refused commit: %v (the changeset should still be open)", err)
	}
}

// TestCommitRefusedOnFirstCommitLintRegression is S6-C127 fixed in the TUI:
// before this fix the review screen's `C` (like `lw commit`) had no gate
// at all on a vault's very first commit, because `lastCommitLintBaseline`
// found no commit_end and reported "no baseline". Here "minimal" (0
// errors) never had a prior commit, and the open changeset's one
// create_page links two pages that do not exist — a projected regression
// to 2 errors that must still be refused, exactly as it would be against
// a real prior commit_end, and must leave the changeset open.
//
// This is also the permanent regression test TD-3 flagged as missing
// (`grep -n 'Regress\|baseline' internal/ui/review/*_test.go` returned
// nothing before this subtask).
func TestCommitRefusedOnFirstCommitLintRegression(t *testing.T) {
	d, e, _ := newTestDeps(t, "minimal")

	if _, err := e.OpenChangeset("regressing changeset", stage.Author{Kind: "human"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	content := "---\n" +
		"title: Broken Links Page\n" +
		"created: 2026-08-30\n" +
		"updated: 2026-08-30\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Broken Links Page\n" +
		"\n" +
		"Links to [[does-not-exist-one]] and [[does-not-exist-two]].\n"
	if _, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/broken-links-page.md",
		Content:    []byte(content),
		Rationale:  "test fixture for the first-commit regression gate",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	m := initModel(t, d)
	m = send(t, m, keyPress('C'))

	msg, level := statusOf(t, m)
	if !strings.Contains(msg, "commit refused: lint regressed: 2 error(s) projected vs 0") {
		t.Errorf("Status does not show the first-commit regression refusal: %q", msg)
	}
	if level != ui.StatusWarn {
		t.Errorf("regression refusal level = %v, want StatusWarn", level)
	}

	if _, err := e.Current(); err != nil {
		t.Errorf("Current after a refused first commit: %v (the changeset should still be open)", err)
	}
}

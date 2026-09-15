package review

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// newTestDeps builds ui.Deps with a real Engine over a private copy of
// fixture, and lw's compiled-in Theme/KeyMap. XDG_CONFIG_HOME is pointed
// at an empty temp dir so these tests never pick up a real user config —
// mirrors internal/ui's own (unexported, different-package) testDeps
// helper.
func newTestDeps(t *testing.T, fixture string) (ui.Deps, *stage.Engine, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := testutil.CopyFixture(t, fixture)
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}

	return ui.Deps{Engine: e, Theme: theme, Keys: keys}, e, root
}

// keyPress builds a synthetic tea.KeyPressMsg for a single printable rune,
// the shape internal/ui's own tests use (app_test.go: Code and Text both
// set) — never via tea.Program.Run(), which does not return on EOF (C-83).
func keyPress(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// runCmd executes cmd, if non-nil, against m, expanding any tea.BatchMsg it
// returns into its constituent commands and feeding every resulting
// message back through m.Update, until nothing is left to run. Headless
// tests drive Update/View directly; this is the harness's stand-in for
// what tea.Program's event loop would otherwise do.
func runCmd(t *testing.T, m ui.Pane, cmd tea.Cmd) ui.Pane {
	t.Helper()
	if cmd == nil {
		return m
	}
	return feedMsg(t, m, cmd())
}

func feedMsg(t *testing.T, m ui.Pane, msg tea.Msg) ui.Pane {
	t.Helper()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = runCmd(t, m, c)
		}
		return m
	}
	updated, cmd := m.Update(msg)
	return runCmd(t, updated, cmd)
}

// send drives msg through m.Update and drains every tea.Cmd it (and
// whatever those in turn produce) returns, so a test can send one key and
// trust the model has fully settled — including its own reload — before
// asserting anything.
func send(t *testing.T, m ui.Pane, msg tea.Msg) ui.Pane {
	t.Helper()
	updated, cmd := m.Update(msg)
	return runCmd(t, updated, cmd)
}

// initModel constructs a review.Model over d and drives its Init() to
// completion (loading the open changeset and its diff, if any).
func initModel(t *testing.T, d ui.Deps) ui.Pane {
	t.Helper()
	m := New(d)
	return runCmd(t, m, m.Init())
}

// findFileDiff returns the FileDiff d carries for opID, failing t if there
// is none.
func findFileDiff(t *testing.T, d stage.Diff, opID string) stage.FileDiff {
	t.Helper()
	for _, f := range d.Files {
		if f.OpID == opID {
			return f
		}
	}
	t.Fatalf("Diff has no FileDiff for op %s (files: %+v)", opID, d.Files)
	return stage.FileDiff{}
}

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

// TestRationaleRendered checks that an op's Rationale and Provenance both
// show up in View(w, h) — the property that makes this a review of
// reasoning, not a diff viewer (/docs/design.md §9.2, s4-tui.md S4-T3 item 2).
func TestRationaleRendered(t *testing.T) {
	d, e, _ := newTestDeps(t, "minimal")

	if _, err := e.OpenChangeset("with rationale", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	oldLine := "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newLine := "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)

	const rationale = "tighten the flash-attention cross-reference for clarity"
	const provenance = "raw/articles/kv-cache-explained.md"

	if _, err := e.Append(stage.Op{
		Kind:       stage.OpPatchPage,
		Path:       page.Path,
		Section:    "## Related",
		Before:     page.SHA256(),
		Content:    rewritten.Serialize(),
		Rationale:  rationale,
		Provenance: []string{provenance},
		Hunks: []stage.Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldLine}, Add: []string{newLine}},
		},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	m := initModel(t, d)
	view := m.View(120, 30)

	if !strings.Contains(view, rationale) {
		t.Errorf("View does not show the op's Rationale:\n%s", view)
	}
	if !strings.Contains(view, provenance) {
		t.Errorf("View does not show the op's Provenance:\n%s", view)
	}
}

// mustDiff calls e.Diff(), failing t on error.
func mustDiff(t *testing.T, e *stage.Engine) stage.Diff {
	t.Helper()
	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	return d
}

// statusOf reads m's StatusReporter message, failing t when the pane
// implements none (contract §5: a refusal lives in the footer's status
// path, not in View — panes no longer draw a status line of their own).
func statusOf(t *testing.T, m ui.Pane) (string, ui.StatusLevel) {
	t.Helper()
	sr, ok := m.(ui.StatusReporter)
	if !ok {
		t.Fatal("review pane does not implement ui.StatusReporter")
		return "", 0
	}
	return sr.Status()
}

// appendTwoHunkPatch appends the two-hunk patch_page op on kv-cache.md the
// review-action tests walk, returning its op id.
func appendTwoHunkPatch(t *testing.T, e *stage.Engine) string {
	t.Helper()
	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	const (
		oldFlash = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
		newFlash = "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
		oldSpec  = "- [[speculative-decoding]] — both the draft and target model read the cache"
		newSpec  = "- [[speculative-decoding]] — the draft model proposes; the target model verifies"
	)
	if !strings.Contains(page.Body, oldFlash) || !strings.Contains(page.Body, oldSpec) {
		t.Fatalf("fixture body does not contain the expected lines:\n%s", page.Body)
	}
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldFlash, newFlash, 1)
	rewritten.Body = strings.Replace(rewritten.Body, oldSpec, newSpec, 1)

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
	return opID
}

// TestStaleOpRefusesReviewKeys is s2-screens.md T06's frozen refusal
// (MASTER §8 ORCH-9): `y`, `n` and `A` on a stale op set the warn status
// `op <id> is stale: refresh before reviewing its hunks` and call NO
// engine method — a stale op's OpDiff windows carry HunkID "" (contract
// §1 note 4), so a key here would act on content the reviewer cannot see
// as attributed, and DropHunk/UndropHunk have no stale guard to catch it.
// Each key's refusal is proven against engine state the forbidden call
// would have changed.
func TestStaleOpRefusesReviewKeys(t *testing.T) {
	d, e, root := newTestDeps(t, "minimal")

	if _, err := e.OpenChangeset("goes stale under review", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	opID := appendTwoHunkPatch(t, e)
	if err := e.DropHunk(opID, "h1"); err != nil {
		t.Fatalf("DropHunk: %v", err)
	}

	// Race: rewrite the target page out from under the open changeset —
	// a patch_page goes stale when the path's current sha no longer matches
	// Before (backbone §5.4).
	full := filepath.Join(root, filepath.FromSlash("wiki/concepts/kv-cache.md"))
	body, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read raced page: %v", err)
	}
	if err := os.WriteFile(full, append(body, []byte("\n<!-- raced under the changeset -->\n")...), 0o644); err != nil {
		t.Fatalf("write raced page: %v", err)
	}
	if err := e.Vault().Reload(); err != nil {
		t.Fatalf("Vault.Reload: %v", err)
	}
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	m := initModel(t, d)
	staleMsg := fmt.Sprintf("op %s is stale: refresh before reviewing its hunks", opID)

	// `y` is refused: h1 stays dropped, which UndropHunk would have undone.
	m = send(t, m, keyPress('y'))
	if msg, level := statusOf(t, m); msg != staleMsg || level != ui.StatusWarn {
		t.Errorf("after y: Status = (%q, %v), want (%q, StatusWarn)", msg, level, staleMsg)
	}
	if op, ok := mustOp(t, e, opID); !ok || !op.Hunks[0].Dropped {
		t.Error("y on a stale op undropped its hunk: an engine method ran")
	}

	// `n` is refused: h2 stays live, which DropHunk would have dropped.
	m = send(t, m, keyPress('n'))
	if msg, level := statusOf(t, m); msg != staleMsg || level != ui.StatusWarn {
		t.Errorf("after n: Status = (%q, %v), want (%q, StatusWarn)", msg, level, staleMsg)
	}
	if op, ok := mustOp(t, e, opID); !ok || op.Hunks[1].Dropped {
		t.Error("n on a stale op dropped its hunk: an engine method ran")
	}

	// `A` is refused before any undrop: h1 stays dropped.
	m = send(t, m, keyPress('A'))
	if msg, level := statusOf(t, m); msg != staleMsg || level != ui.StatusWarn {
		t.Errorf("after A: Status = (%q, %v), want (%q, StatusWarn)", msg, level, staleMsg)
	}
	if op, ok := mustOp(t, e, opID); !ok || !op.Hunks[0].Dropped {
		t.Error("A on a changeset with a stale op undropped a hunk: an engine method ran")
	}

	if _, err := e.Current(); err != nil {
		t.Errorf("Current after the refusals: %v (the changeset should still be open)", err)
	}
}

// TestOwnerlessWindowIsNeverYNCursorTarget pins s2-screens.md T06's
// second key rule: when the window under the cursor carries no HunkID —
// a create, an ingest, a derived index.md, or any window whose ownership
// cannot be proven (contract §1 note 4) — `y` and `n` refuse instead of
// calling the engine. The model is hand-built with a nil engine
// deliberately: proceeding past the refusal would nil-panic on the engine
// call, so the assertion is the proof that no engine method ran.
func TestOwnerlessWindowIsNeverYNCursorTarget(t *testing.T) {
	m := &Model{
		deps:         ui.Deps{Theme: testTheme(t), Keys: defaultTestKeys(t)}, // no engine
		theme:        testTheme(t),
		hasChangeset: true,
		changeset:    &stage.Changeset{ID: "cs-ownerless"},
		ops:          []stage.Op{{ID: "op1", Kind: stage.OpCreatePage, State: stage.StateProposed}},
		// The cursor walk has a stop on the op's file hunk...
		diff:  stage.Diff{Files: []stage.FileDiff{{OpID: "op1", Hunks: []stage.Hunk{{ID: "h1"}}}}},
		stops: buildCursorStops(stage.Diff{Files: []stage.FileDiff{{OpID: "op1", Hunks: []stage.Hunk{{ID: "h1"}}}}}),
		// ...but the displayed windows are ownerless, as OpDiff shows them
		// for an op that persists no hunks.
		opDiffs: map[string][]stage.FileOpDiff{
			"op1": {{Path: "wiki/concepts/ownerless.md", Hunks: []stage.DisplayHunk{{HunkID: "", Lines: []stage.DisplayLine{{Kind: '+', Text: "whole file"}}}}}},
		},
	}

	const want = "this window has no hunk id — it cannot be accepted or dropped individually"

	p := send(t, m, keyPress('y'))
	if msg, level := statusOf(t, p); msg != want || level != ui.StatusWarn {
		t.Errorf("after y: Status = (%q, %v), want (%q, StatusWarn)", msg, level, want)
	}
	p = send(t, p, keyPress('n'))
	if msg, level := statusOf(t, p); msg != want || level != ui.StatusWarn {
		t.Errorf("after n: Status = (%q, %v), want (%q, StatusWarn)", msg, level, want)
	}
}

// TestPreviewTogglesDetailMode presses `p` on the harness vault's patch
// op and asserts the frozen Detail swap: Diff → Preview with the `p diff`
// note and the staged page rendered, cursor and Ops footnote kept, and
// back (s2-screens.md T06).
func TestPreviewTogglesDetailMode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "review-preview-toggle")
	d := uitest.Deps(v, true, nil)

	m := initModel(t, d)
	// Walk the cursor to op3 (the autolyse patch), the way the conformance
	// script does: its head note is "op3 · 1 hunk".
	reached := false
	for i := 0; i < 10 && !reached; i++ {
		_, pl := uitest.PaneScreen(m, 100, 28)
		if strings.Contains(pl, "op3 · 1 hunk") {
			reached = true
			break
		}
		m = send(t, m, keyPress('j'))
	}
	if !reached {
		t.Fatal("cursor never reached op3 within 10 j presses")
	}

	m = send(t, m, keyPress('p'))
	_, pl := uitest.PaneScreen(m, 100, 28)
	if !strings.Contains(pl, "╭ Preview ") || !strings.Contains(pl, "p diff") {
		t.Errorf("p did not open Preview with the `p diff` note:\n%s", pl)
	}
	if !strings.Contains(pl, "op3 · staged page") {
		t.Errorf("Preview lacks the op head note:\n%s", pl)
	}
	if !strings.Contains(pl, "Autolyse") {
		t.Errorf("Preview does not render the staged page:\n%s", pl)
	}
	if !strings.Contains(pl, "3 of 4") {
		t.Errorf("toggling Preview moved the Ops cursor off op3:\n%s", pl)
	}

	m = send(t, m, keyPress('p'))
	_, pl = uitest.PaneScreen(m, 100, 28)
	if !strings.Contains(pl, "╭ Diff ") || !strings.Contains(pl, "p preview") {
		t.Errorf("p did not return to Diff with the `p preview` note:\n%s", pl)
	}
	if !strings.Contains(pl, "3 of 4") {
		t.Errorf("the round trip moved the Ops cursor:\n%s", pl)
	}
}

// mustOp fetches opID from the engine's current changeset.
func mustOp(t *testing.T, e *stage.Engine, opID string) (*stage.Op, bool) {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	return cs.Op(opID)
}

package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// TestDiffRender covers lw diff --render (003 contract §8): each changed
// page printed as it will read after commit — plain text at width 80 when
// stdout is not a TTY, styled at the terminal's width when it is — with
// --stat excluded, --op honoured, and the deleted / non-markdown
// substitutes in place of a body.
func TestDiffRender(t *testing.T) {
	t.Run("plain_when_not_tty", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		stageTwoOpChangeset(t, root)

		// captureRun's os.Stdout is a pipe: not a character device, so
		// this is the non-TTY path — plain text, width 80, no escapes.
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"diff", "--vault", root, "--render"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		if strings.Contains(stdout, "\x1b") {
			t.Errorf("non-TTY output contains an escape sequence:\n%q", stdout)
		}
		// "── <path> " + dashes fills W cells: for diff-target.md (28
		// chars) at width 80 that is 48 trailing dashes.
		rule := diffRuleLine(t, stdout, "wiki/concepts/diff-target.md")
		if got := strings.Count(rule, "─"); got != 50 {
			t.Errorf("rule line carries %d ─ runes, want 50 (80 cells): %q", got, rule)
		}
		// The rendered page ends with the format's trailing blank line;
		// testutil normalizes a golden file to exactly one trailing
		// newline, so the compared side is normalized the same way.
		testutil.GoldenString(t, "testdata/diff-render/plain-80.golden",
			strings.TrimRight(stdout, "\n")+"\n")

		// The same changeset through the TTY seam: injected 40-column
		// dark terminal, so the output is styled and the rule shrinks.
		orig := stdoutTerm
		stdoutTerm = termProbe{
			isTTY: func() bool { return true },
			width: func() int { return 40 },
			dark:  func() bool { return true },
		}
		t.Cleanup(func() { stdoutTerm = orig })
		stdout2, stderr2, code2 := captureRun(t, func() int {
			return run([]string{"diff", "--vault", root, "--render"})
		})
		if code2 != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code2, stderr2)
		}
		if !strings.Contains(stdout2, "\x1b[") {
			t.Errorf("TTY output carries no SGR sequence:\n%q", stdout2)
		}
		rule2 := diffRuleLine(t, stdout2, "wiki/concepts/diff-target.md")
		if got := strings.Count(rule2, "─"); got != 10 {
			t.Errorf("40-column rule line carries %d ─ runes, want 10 (40 cells): %q", got, rule2)
		}
	})

	t.Run("stat_and_render_exclusive", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"diff", "--vault", root, "--render", "--stat"})
		})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
		}
		if stderr != "lw diff: --render and --stat are mutually exclusive\n" {
			t.Errorf("stderr = %q, want the mutual-exclusion message", stderr)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty", stdout)
		}
	})

	t.Run("op_filter", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		createID, patchID := stageTwoOpChangeset(t, root)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"diff", "--vault", root, "--render", "--op", createID})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "── wiki/concepts/diff-target.md ") {
			t.Errorf("missing the create_page's own rule line:\n%s", stdout)
		}
		if strings.Contains(stdout, "kv-cache.md") {
			t.Errorf("contains the second op's page, which --op=%s should have filtered out:\n%s", createID, stdout)
		}

		stdout2, stderr2, code2 := captureRun(t, func() int {
			return run([]string{"diff", "--vault", root, "--render", "--op", patchID})
		})
		if code2 != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code2, stderr2)
		}
		if !strings.Contains(stdout2, "── wiki/concepts/kv-cache.md ") {
			t.Errorf("missing the patch_page's own rule line:\n%s", stdout2)
		}
		if strings.Contains(stdout2, "diff-target") {
			t.Errorf("contains the first op's page, which --op=%s should have filtered out:\n%s", patchID, stdout2)
		}

		// §8.2 de-duplicates by path, in order: a third op patching the
		// same kv-cache.md gives Diff().Files two entries for that path,
		// and the unfiltered render shows it once, at the first entry's
		// position, from the LAST entry — the write Commit materializes
		// (C25/D-3P: the body source is FileDiff.New of that last entry,
		// so "[x]" proves the last op's patch is what renders).
		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		kvCache, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("minimal fixture missing wiki/concepts/kv-cache.md")
		}
		oldKVCache := string(kvCache.Serialize())
		newKVCache := strings.Replace(oldKVCache,
			"- [[speculative-decoding]] — both the draft and target model read the cache",
			"- [[speculative-decoding]] — both the draft and target model read the cache [x]",
			1)
		if newKVCache == oldKVCache {
			t.Fatal("test setup: replacement did not match kv-cache.md's body")
		}
		hunks := stage.ComputeHunks(oldKVCache, newKVCache)
		for i := range hunks {
			hunks[i].Path = "wiki/concepts/kv-cache.md"
			hunks[i].Section = "## Related"
		}
		if _, err := e.Append(stage.Op{
			Kind:      stage.OpPatchPage,
			Path:      "wiki/concepts/kv-cache.md",
			Section:   "## Related",
			Before:    kvCache.SHA256(),
			Content:   []byte(newKVCache),
			Hunks:     hunks,
			Rationale: "test fixture",
		}); err != nil {
			t.Fatalf("Append second patch_page: %v", err)
		}
		if err := e.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		stdout3, stderr3, code3 := captureRun(t, func() int {
			return run([]string{"diff", "--vault", root, "--render"})
		})
		if code3 != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code3, stderr3)
		}
		if got := strings.Count(stdout3, "── wiki/concepts/kv-cache.md "); got != 1 {
			t.Errorf("kv-cache.md rendered %d times, want 1 (deduplicated):\n%s", got, stdout3)
		}
		if !strings.Contains(stdout3, "[x]") {
			t.Errorf("render does not show the last op's projected content:\n%s", stdout3)
		}
	})

	t.Run("deleted_and_non_markdown", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openCreatePageChangeset(t, root, "wiki/concepts/diff-target.md", "Diff Target")

		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		if _, err := e.Append(stage.Op{
			Kind:      stage.OpRenamePage,
			From:      "wiki/concepts/kv-cache.md",
			To:        "wiki/concepts/kv-cache-renamed.md",
			Rationale: "test fixture",
		}); err != nil {
			t.Fatalf("Append rename_page: %v", err)
		}
		if err := e.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"diff", "--vault", root, "--render"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "── wiki/concepts/kv-cache.md ") {
			t.Errorf("missing the renamed-from page's rule line:\n%s", stdout)
		}
		if !strings.Contains(stdout, "(deleted in this changeset)") {
			t.Errorf("missing the deleted-page substitute:\n%s", stdout)
		}

		// A non-.md path can never come out of a validated op (every
		// op path must be a lowercase-hyphen.md path), so the
		// substitute is exercised at diffFileBody directly.
		e, err = stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()
		o, err := renderOptions()
		if err != nil {
			t.Fatalf("renderOptions: %v", err)
		}
		lines, err := diffFileBody(e, markdown.NewRenderer(),
			stage.FileDiff{Path: "raw/articles/notes.txt", OpID: "op1"}, o)
		if err != nil {
			t.Fatalf("diffFileBody: %v", err)
		}
		if len(lines) != 1 || lines[0] != "(not markdown: see lw diff)" {
			t.Errorf("diffFileBody(non-markdown) = %q, want [(not markdown: see lw diff)]", lines)
		}

		// A derived index.md is the C25 case: Commit writes it for the
		// create_page, but no op targets it, so the body source must be
		// the entry's projected New — the rendered index, entry for the
		// new page included — never the deleted placeholder.
		root2 := testutil.CopyFixture(t, "minimal")
		openCreatePageChangeset(t, root2, "wiki/concepts/diff-target.md", "Diff Target")
		stdout2, stderr2, code2 := captureRun(t, func() int {
			return run([]string{"diff", "--vault", root2, "--render"})
		})
		if code2 != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code2, stderr2)
		}
		idxBody := renderSection(t, stdout2, "index.md")
		if strings.Contains(idxBody, "(deleted in this changeset)") {
			t.Errorf("derived index.md reported deleted; commit writes it:\n%s", idxBody)
		}
		if !strings.Contains(idxBody, "diff-target — Diff Target") {
			t.Errorf("index.md body missing the new page's index entry:\n%s", idxBody)
		}

		// Conversely, an entry with no projected content IS a deletion
		// (internal/stage/diff.go: New is "" for a deletion), even on a
		// path the changeset otherwise targets — the substitute is the
		// body whenever the entry's New is empty.
		e, err = stage.OpenEngine(root2)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		lines, err = diffFileBody(e, markdown.NewRenderer(),
			stage.FileDiff{Path: "wiki/concepts/diff-target.md", OpID: "op1", Kind: stage.OpCreatePage}, o)
		if err != nil {
			t.Fatalf("diffFileBody: %v", err)
		}
		if len(lines) != 1 || lines[0] != "(deleted in this changeset)" {
			t.Errorf("diffFileBody(New==%q) = %q, want [(deleted in this changeset)]", "", lines)
		}
	})

	t.Run("help_lists_render", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"diff", "--help"})
		})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, "-render") {
			t.Errorf("diff --help does not list -render:\n%s", stderr)
		}
		if !strings.Contains(stderr, "render each changed page as it will read after commit") {
			t.Errorf("diff --help does not list -render's usage text:\n%s", stderr)
		}
	})
}

// stageTwoOpChangeset stages the two-op changeset --render's tests share:
// openCreatePageChangeset's create_page at wiki/concepts/diff-target.md
// plus one patch_page of wiki/concepts/kv-cache.md's Related section, the
// shape of TestCmdDiffOpFilter's fixture. It returns the two op ids; the
// engine is closed before it returns, so callers reach the changeset
// through run().
func stageTwoOpChangeset(t *testing.T, root string) (createID, patchID string) {
	t.Helper()

	c := openCreatePageChangeset(t, root, "wiki/concepts/diff-target.md", "Diff Target")
	live := c.Live()
	if len(live) != 1 {
		t.Fatalf("test setup: want 1 live op after the create_page, got %d", len(live))
	}
	createID = live[0].ID

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	kvCache, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("minimal fixture missing wiki/concepts/kv-cache.md")
	}
	oldKVCache := string(kvCache.Serialize())
	newKVCache := strings.Replace(oldKVCache,
		"- [[flash-attention]]",
		"- [[gpt-4]] — an unrelated edit for TestDiffRender's fixture.\n- [[flash-attention]]",
		1)
	if newKVCache == oldKVCache {
		t.Fatal("test setup: replacement did not match kv-cache.md's body")
	}
	hunks := stage.ComputeHunks(oldKVCache, newKVCache)
	for i := range hunks {
		hunks[i].Path = "wiki/concepts/kv-cache.md"
		hunks[i].Section = "## Related"
	}
	patchID, err = e.Append(stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      "wiki/concepts/kv-cache.md",
		Section:   "## Related",
		Before:    kvCache.SHA256(),
		Content:   []byte(newKVCache),
		Hunks:     hunks,
		Rationale: "test fixture",
	})
	if err != nil {
		t.Fatalf("Append patch_page: %v", err)
	}
	return createID, patchID
}

// diffRuleLine returns the ── rule line out carries for path, failing the
// test when there is none.
func diffRuleLine(t *testing.T, out, path string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "── "+path+" ") {
			return line
		}
	}
	t.Fatalf("no ── rule line for %s in:\n%s", path, out)
	return ""
}

// renderSection returns one section of a --render output: the lines
// between path's ── rule line and the next rule line, exclusive of both,
// with the section's surrounding blank lines trimmed. It fails the test
// when out has no rule line for path.
func renderSection(t *testing.T, out, path string) string {
	t.Helper()
	lines := strings.Split(out, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "── "+path+" ") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("no ── rule line for %s in:\n%s", path, out)
		return ""
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "── ") {
			end = i
			break
		}
	}
	return strings.Trim(strings.Join(lines[start:end], "\n"), "\n")
}

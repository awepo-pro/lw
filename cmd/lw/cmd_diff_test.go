package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// TestCmdDiffNoChangeset covers the exit-code Contract (backbone §13
// D-AC): a bare failure — no open changeset — prints "lw: diff: ..." and
// exits 1.
func TestCmdDiffNoChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"diff", "--vault", root})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.HasPrefix(stderr, "lw: diff: ") {
		t.Errorf("stderr = %q, want it to start with %q", stderr, "lw: diff: ")
	}
}

// TestCmdDiffUnified stages one create_page op and checks the printed
// unified diff carries the new page's content under a /dev/null header.
func TestCmdDiffUnified(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openCreatePageChangeset(t, root, "wiki/concepts/diff-target.md", "Diff Target")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"diff", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "--- /dev/null") {
		t.Errorf("stdout missing /dev/null creation header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "+++ b/wiki/concepts/diff-target.md") {
		t.Errorf("stdout missing the new page's +++ header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "+title: Diff Target") {
		t.Errorf("stdout missing the new page's content:\n%s", stdout)
	}
}

// TestCmdDiffOpFilter checks --op narrows the unified diff to only the
// files a single op's FileDiff entries name — including index.md's
// derived line, which S2-T8's applyDerivedIndexDiff attributes to the
// create_page that produced it, but excluding a second, unrelated op's
// own entries (backbone §5.6, cmdDiff's own filterDiffByOp).
func TestCmdDiffOpFilter(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	if _, err := e.OpenChangeset("test changeset", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	content := "---\n" +
		"title: Diff Target\n" +
		"created: 2026-08-30\n" +
		"updated: 2026-08-30\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Diff Target\n" +
		"\n" +
		"See [[kv-cache]] and [[flash-attention]] for background.\n"
	createID, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/diff-target.md",
		Content:    []byte(content),
		Rationale:  "test fixture",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	})
	if err != nil {
		t.Fatalf("Append create_page: %v", err)
	}
	kvCache, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("minimal fixture missing wiki/concepts/kv-cache.md")
	}
	oldKVCache := string(kvCache.Serialize())
	newKVCache := strings.Replace(oldKVCache,
		"- [[flash-attention]]",
		"- [[gpt-4]] — an unrelated edit for TestCmdDiffOpFilter.\n- [[flash-attention]]",
		1)
	if newKVCache == oldKVCache {
		t.Fatal("test setup: replacement did not match kv-cache.md's body")
	}
	patchID, err := e.Append(stage.Op{
		Kind:    stage.OpPatchPage,
		Path:    "wiki/concepts/kv-cache.md",
		Section: "## Related",
		Before:  kvCache.SHA256(),
		Content: []byte(newKVCache),
	})
	if err != nil {
		t.Fatalf("Append patch_page: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"diff", "--vault", root, "--op", createID})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "+++ b/wiki/concepts/diff-target.md") {
		t.Errorf("stdout missing the create_page's own file:\n%s", stdout)
	}
	if !strings.Contains(stdout, "+- [[diff-target]] — Diff Target") {
		t.Errorf("stdout missing index.md's derived line, attributed to the same op:\n%s", stdout)
	}
	if strings.Contains(stdout, "kv-cache.md") {
		t.Errorf("stdout contains the second op's own file, which --op=%s should have filtered out:\n%s", createID, stdout)
	}

	stdout2, stderr2, code2 := captureRun(t, func() int {
		return run([]string{"diff", "--vault", root, "--op", patchID})
	})
	if code2 != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code2, stderr2)
	}
	if !strings.Contains(stdout2, "+++ b/wiki/concepts/kv-cache.md") {
		t.Errorf("stdout2 missing the patch_page's own file:\n%s", stdout2)
	}
	if strings.Contains(stdout2, "diff-target") {
		t.Errorf("stdout2 contains the first op's own paths, which --op=%s should have filtered out:\n%s", patchID, stdout2)
	}
}

// TestCmdDiffStat checks --stat prints a per-file summary instead of the
// full unified diff.
func TestCmdDiffStat(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openCreatePageChangeset(t, root, "wiki/concepts/diff-target.md", "Diff Target")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"diff", "--vault", root, "--stat"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "wiki/concepts/diff-target.md | +") {
		t.Errorf("stdout missing the per-file stat line:\n%s", stdout)
	}
	if strings.Contains(stdout, "--- ") {
		t.Errorf("--stat output should not contain a unified diff header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "file(s) changed") {
		t.Errorf("stdout missing the summary line:\n%s", stdout)
	}
}

// TestCmdDiffBadFlag covers the usage-error exit code.
func TestCmdDiffBadFlag(t *testing.T) {
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"diff", "--bogusflag"})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

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
		// same kv-cache.md gives Diff().Files two entries for that path
		// (StagedFile's last-op-wins content), and the unfiltered render
		// shows it once.
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

// openCreatePageChangeset opens a changeset over root and appends one
// well-formed create_page op at path with title, returning the resulting
// Changeset. Shared by cmd_diff_test.go and cmd_commit_test.go.
func openCreatePageChangeset(t *testing.T, root, path, title string) *stage.Changeset {
	t.Helper()

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	if _, err := e.OpenChangeset("test changeset", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	content := "---\n" +
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

	if _, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       path,
		Content:    []byte(content),
		Rationale:  "test fixture",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	return c
}

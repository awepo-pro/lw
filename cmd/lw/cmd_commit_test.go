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

// TestCmdCommitNoBaselinePasses is this subtask's goal (b): a vault with
// no prior commit_end has its baseline computed from the CURRENT
// committed working tree (S6-C127) rather than skipping the gate — the
// "minimal" fixture lints clean (0 errors), and a clean create_page whose
// links resolve to existing pages does not regress that, so the first
// commit still goes through.
func TestCmdCommitNoBaselinePasses(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openCreatePageChangeset(t, root, "wiki/concepts/first-commit.md", "First Commit")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "first commit, no baseline"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "committed 000001") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "committed 000001")
	}
}

// TestCmdCommitFirstCommitRefusesRegressionAndForceOverrides is S6-C127's
// central regression: the live defect had a vault's FIRST commit skip the
// gate entirely because there was no commit_end baseline yet. Here the
// "minimal" fixture (0 errors) gets a create_page whose body links two
// pages that do not exist — never committed before, so this is the
// changeset's first-ever commit — and the commit must still be refused
// exactly as it would be against a real prior baseline, then go through
// under --force with commit_end carrying "forced":true.
func TestCmdCommitFirstCommitRefusesRegressionAndForceOverrides(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openBrokenLinksChangeset(t, root)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "first commit, should refuse"})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.HasPrefix(stderr, "lw: commit: lint regressed: 2 error(s) projected vs 0") {
		t.Errorf("stderr = %q, want it to start with %q", stderr,
			"lw: commit: lint regressed: 2 error(s) projected vs 0")
	}

	// Nothing may have been committed: Current() must still return the
	// same open changeset, and no snapshot/commit directory exists yet.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	if _, err := e.Current(); err != nil {
		t.Fatalf("Current after refusal: %v (changeset should still be open)", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".llmwiki", "snapshots", "000001.tree")); err == nil {
		t.Fatalf("snapshots/000001.tree exists after a refused commit; nothing should have been written")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat snapshots/000001.tree: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(root, ".llmwiki", "changesets", "committed")); err != nil {
		t.Fatalf("read changesets/committed: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("changesets/committed has %d entries after a refused commit, want 0", len(entries))
	}

	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "first commit, forced", "--force"})
	})
	if code != 0 {
		t.Fatalf("forced commit: exit = %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "committed 000001") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "committed 000001")
	}

	e, err = stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	evs, err := e.Journal().Query(stage.Filter{Kinds: []stage.EventKind{stage.EvCommitEnd}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	forced := false
	for _, ev := range evs {
		if ev.Commit != "000001" {
			continue
		}
		var data struct {
			Forced     bool `json:"forced"`
			LintErrors int  `json:"lint_errors"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatalf("unmarshal commit_end data %s: %v", ev.Data, err)
		}
		if data.Forced {
			forced = true
			if data.LintErrors != 2 {
				t.Errorf("forced commit_end lint_errors = %d, want 2", data.LintErrors)
			}
		}
	}
	if !forced {
		t.Errorf("no commit_end event for 000001 carries \"forced\":true; events=%+v", evs)
	}
}

// TestCmdCommitFirstCommitToleratesExistingErrors is this subtask's goal
// (c): a vault whose working tree ALREADY has lint errors before its
// first commit (the "dirty" fixture, 7 errors by construction — see
// spec/fixtures/dirty/EXPECTED-LINT.md) must not have its first commit
// refused merely for carrying those pre-existing errors. The baseline is
// the CURRENT tree (7 errors), not zero, so a changeset that adds no NEW
// error commits fine.
func TestCmdCommitFirstCommitToleratesExistingErrors(t *testing.T) {
	root := testutil.CopyFixture(t, "dirty")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	if _, err := e.OpenChangeset("harmless addition", stage.Author{Kind: "human"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	content := "---\n" +
		"title: Harmless Addition\n" +
		"created: 2026-08-30\n" +
		"updated: 2026-08-30\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Harmless Addition\n" +
		"\n" +
		"See [[long-page]] and [[orphan-page]] for background.\n"
	if _, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/harmless-addition.md",
		Content:    []byte(content),
		Rationale:  "test fixture: adds no new lint error to an already-dirty vault",
		Provenance: []string{"raw/papers/valid-source.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "first commit over a pre-dirty vault"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "committed 000001") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "committed 000001")
	}
}

// TestCmdCommitRefusesRegressionAndForceOverrides is this subtask's
// central scenario: a clean baseline commit, then a changeset that
// regresses lint (two broken-link create_page's worth of errors), refused
// without --force, accepted with it, and journaled as forced:true.
func TestCmdCommitRefusesRegressionAndForceOverrides(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	openCreatePageChangeset(t, root, "wiki/concepts/baseline-page.md", "Baseline Page")
	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "baseline"})
	}); code != 0 {
		t.Fatalf("baseline commit: exit = %d, stderr=%q", code, stderr)
	}

	openBrokenLinksChangeset(t, root)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "should refuse"})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.HasPrefix(stderr, "lw: commit: lint regressed:") {
		t.Errorf("stderr = %q, want it to start with %q", stderr, "lw: commit: lint regressed:")
	}

	// The refused changeset must still be open — commit must not have
	// touched the vault.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	if _, err := e.Current(); err != nil {
		t.Fatalf("Current after refusal: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "forced", "--force"})
	})
	if code != 0 {
		t.Fatalf("forced commit: exit = %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "committed 000002") {
		t.Errorf("stdout = %q, want it to contain %q", stdout, "committed 000002")
	}

	e, err = stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	evs, err := e.Journal().Query(stage.Filter{
		Kinds: []stage.EventKind{stage.EvCommitEnd},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	forced := false
	for _, ev := range evs {
		if ev.Commit != "000002" {
			continue
		}
		var data struct {
			Forced     bool `json:"forced"`
			LintErrors int  `json:"lint_errors"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatalf("unmarshal commit_end data %s: %v", ev.Data, err)
		}
		if data.Forced {
			forced = true
			if data.LintErrors == 0 {
				t.Errorf("forced commit_end carries lint_errors=0, want it to record the actual regressed count")
			}
		}
	}
	if !forced {
		t.Errorf("no commit_end event for 000002 carries \"forced\":true; events=%+v", evs)
	}
}

// TestCmdCommitStaleOpIsActionable covers wave-8 entry item 5: Commit
// already refuses stale ops (ErrStale); the CLI turns that into an
// actionable message rather than re-implementing the check.
func TestCmdCommitStaleOpIsActionable(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	c := openCreatePageChangeset(t, root, "wiki/concepts/will-go-stale.md", "Will Go Stale")

	// A second Append with the SAME target path can't happen (create_page
	// rejects an existing path), so make the op stale the way §5.4's own
	// table does for create_page: create the target path out from under
	// it, on disk, directly.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = c

	writeFileDirect(t, root, "wiki/concepts/will-go-stale.md", "---\n"+
		"title: Raced\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\n"+
		"tags: [inference]\n---\n\nSee [[kv-cache]] and [[flash-attention]].\n")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "should refuse on stale"})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "stale") {
		t.Errorf("stderr = %q, want an actionable message mentioning staleness", stderr)
	}
}

// TestCmdCommitMissingMessageIsUsageError covers backbone §13's exit-code
// Contract: a usage error prints its own usage and exits 2.
func TestCmdCommitMissingMessageIsUsageError(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

// TestCmdCommitBadFlagIsUsageError covers the flag.ContinueOnError path.
func TestCmdCommitBadFlagIsUsageError(t *testing.T) {
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--bogusflag"})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

// TestCmdCommitNoChangeset covers the bare-error/exit-1 path when there
// is nothing to commit.
func TestCmdCommitNoChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "nothing to commit"})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.HasPrefix(stderr, "lw: commit: ") {
		t.Errorf("stderr = %q, want it to start with %q", stderr, "lw: commit: ")
	}
}

// openBrokenLinksChangeset opens a changeset over root and appends one
// create_page op whose body links to two pages that do not exist —
// syntactically valid (>= 2 outbound wikilinks, ValidateOp does not
// resolve link targets) but a link-broken SevError once linted, which is
// what makes it regress a clean baseline.
func openBrokenLinksChangeset(t *testing.T, root string) {
	t.Helper()

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

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
		Rationale:  "test fixture for the regression gate",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// writeFileDirect writes content to root/relPath directly, bypassing the
// staging engine entirely — the "working tree changed under it" shape
// Refresh detects (backbone §5.4 D-AJ: create_page/ingest_source go stale
// when their target path now exists).
func writeFileDirect(t *testing.T, root, relPath, content string) {
	t.Helper()

	full := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

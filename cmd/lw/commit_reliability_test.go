package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// stageRawOnlyChangeset opens a changeset over root holding exactly one
// live ingest_source op — the raw-only shape the commit warning keys on
// (008 contract §6) — and leaves it open, returning its id. Modeled on
// cmd_doctor_state_test.go's commitIngestSource, minus the commit.
func stageRawOnlyChangeset(t *testing.T, root, rawPath string) string {
	t.Helper()
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	body := "# Raw Only Test Source\n\nA body ingested so the changeset is raw-only.\n"
	fm := fmt.Sprintf("---\nsource_url: https://example.org/raw-only-test\ningested: 2026-09-09\nsha256: %s\n---\n\n", vault.BodySHA256(body))
	rs, err := vault.ParseRawSource(rawPath, []byte(fm+body))
	if err != nil {
		t.Fatalf("parse raw source: %v", err)
	}

	cs, err := e.OpenChangeset("raw-only ingest test", stage.Author{Kind: "human"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(stage.Op{
		Kind:      stage.OpIngestSource,
		Path:      rawPath,
		Extractor: "passthrough",
		Content:   rs.Serialize(),
	}); err != nil {
		t.Fatalf("append ingest_source: %v", err)
	}
	return cs.ID
}

// TestCmdCommitNothingToCommit pins the CLI half of stage.ErrNothingToCommit
// (008 contract §6): committing a changeset whose every op was dropped
// fails with the exact contract message and exit 1 — a plain failure, not
// a usage error.
func TestCmdCommitNothingToCommit(t *testing.T) {
	t.Run("all_dropped_exits_1_with_message", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		csID := stageRawOnlyChangeset(t, root, "raw/articles/nothing-to-commit-test.md")

		// Drop the only op, exactly as a curator's review can, then get out
		// of the engine so `lw commit` can take the lock itself.
		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		live := cs.Live()
		if len(live) != 1 {
			t.Fatalf("live ops = %d, want the one staged ingest_source", len(live))
		}
		if err := e.DropOp(live[0].ID); err != nil {
			t.Fatalf("DropOp: %v", err)
		}
		if err := e.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		const want = "nothing to commit: every op in %s was dropped"
		err = cmdCommit([]string{"--vault", root, "-m", "nothing live"})
		if err == nil {
			t.Fatalf("cmdCommit returned nil for an all-dropped changeset, want %q", fmt.Sprintf(want, csID))
		}
		if got := fmt.Sprintf(want, csID); err.Error() != got {
			t.Errorf("cmdCommit error = %q, want %q", err, got)
		}

		// Exit 1, not 2: the message is a failure, not a usage error, so
		// dispatch prints it with the ordinary "lw: commit: " prefix.
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"commit", "--vault", root, "-m", "nothing live"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
		}
		if !strings.HasPrefix(stderr, "lw: commit: nothing to commit: every op in "+csID+" was dropped") {
			t.Errorf("stderr = %q, want the prefixed nothing-to-commit message", stderr)
		}
	})
}

// TestCmdCommitRawOnlyWarns pins the raw-only commit notice: a changeset
// with live raw sources and no other live op (A-029-1) commits — the notice goes
// to stderr first, and the commit still lands.
func TestCmdCommitRawOnlyWarns(t *testing.T) {
	t.Run("warning_on_stderr_then_commits", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		const rawPath = "raw/articles/raw-only-warn-test.md"
		stageRawOnlyChangeset(t, root, rawPath)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"commit", "--vault", root, "-m", "raw only"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		wantWarn := fmt.Sprintf("warning: 0 pages proposed — committing raw source(s) only: %s", rawPath)
		if !strings.Contains(stderr, wantWarn) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, wantWarn)
		}
		if !strings.Contains(stdout, "committed ") {
			t.Errorf("stdout = %q, want the committed line", stdout)
		}

		// The commit actually landed: nothing open, one committed changeset,
		// and the raw file is in the working tree.
		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()
		if _, err := e.Current(); !errors.Is(err, stage.ErrNoChangeset) {
			t.Fatalf("Current after the raw-only commit = %v, want ErrNoChangeset", err)
		}
		if got := countChangesets(t, root, "committed"); got != 1 {
			t.Errorf("committed changesets = %d, want 1", got)
		}
		if !e.Vault().Exists(rawPath) {
			t.Errorf("committed raw source %s is not in the vault", rawPath)
		}
	})
}

// TestCmdCommitPatchNoWarning pins A-029-1's CLI half: one ingest_source
// plus a patch_page is page work — the old create-only rule counted the
// patch as nothing and printed the raw-only warning anyway. `lw commit`
// must print no warning and land the commit on stdout.
func TestCmdCommitPatchNoWarning(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	const rawPath = "raw/papers/tilelang-a-composable-tiled-programming-model-for-ai-systemsthanks-mathsection-equal-contributions.md"

	// Stage ingest + patch_page by engine calls, then close so `lw commit`
	// can take the vault lock itself.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	body := "# TileLang\n\nA body the patch below sits beside.\n"
	fm := fmt.Sprintf("---\nsource_url: https://example.org/tilelang\ningested: 2026-09-09\nsha256: %s\n---\n\n", vault.BodySHA256(body))
	rs, err := vault.ParseRawSource(rawPath, []byte(fm+body))
	if err != nil {
		t.Fatalf("parse raw source: %v", err)
	}
	if _, err := e.OpenChangeset("ingest plus patch", stage.Author{Kind: "human"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(stage.Op{
		Kind:      stage.OpIngestSource,
		Path:      rawPath,
		Extractor: "passthrough",
		Content:   rs.Serialize(),
	}); err != nil {
		t.Fatalf("append ingest_source: %v", err)
	}
	kv, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("minimal fixture missing wiki/concepts/kv-cache.md")
	}
	newKV := strings.Replace(string(kv.Serialize()),
		"- [[flash-attention]]",
		"- [[gpt-4]] — an unrelated edit for TestCmdCommitPatchNoWarning.\n- [[flash-attention]]", 1)
	if newKV == string(kv.Serialize()) {
		t.Fatal("test setup: replacement did not match kv-cache.md's body")
	}
	if _, err := e.Append(stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      kv.Path,
		Section:   "## Related",
		Before:    kv.SHA256(),
		Content:   []byte(newKV),
		Rationale: "one page edit beside the ingest",
	}); err != nil {
		t.Fatalf("Append patch_page: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "x"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if strings.Contains(stderr, "0 pages proposed") {
		t.Errorf("stderr carries the raw-only warning for ingest+patch:\n%s", stderr)
	}
	if !strings.Contains(stdout, "committed ") {
		t.Errorf("stdout = %q, want the committed line", stdout)
	}
}

// commit_log_trace_test.go is 029 T1's F2 evidence: Engine.Commit emits one
// slog record per call — success or refusal — so a commit is visible in
// lw.log where nothing was logged before. Records never carry page content
// or the commit message text.
package stage

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureDefaultLog points slog.Default() at a TextHandler over a buffer
// and restores the previous logger in t.Cleanup. Do not use t.Parallel in
// any test calling this: it swaps the global logger.
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestCommitLogsTrace(t *testing.T) {
	t.Run("success_logs_changeset_commit", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("commit trace", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		stageIngest(t, e, "raw/articles/trace-me.md", "# Trace Me\n\nOne live op.\n")

		buf := captureDefaultLog(t)
		commitID, err := e.Commit("trace commit")
		if err != nil {
			t.Fatalf("Commit: %v", err)
		}

		out := buf.String()
		for _, want := range []string{
			`msg="changeset commit"`,
			"changeset=cs-",
			"commit=" + commitID,
			"live_ops=1",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("commit trace does not contain %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("nothing_to_commit_logs_failure", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("commit trace, nothing live", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		opID := stageIngest(t, e, "raw/articles/drop-me.md", "# Drop Me\n\nEvery op dropped.\n")
		if err := e.DropOp(opID); err != nil {
			t.Fatalf("DropOp: %v", err)
		}

		buf := captureDefaultLog(t)
		if _, err := e.Commit("nothing live"); !errors.Is(err, ErrNothingToCommit) {
			t.Fatalf("Commit err = %v, want ErrNothingToCommit", err)
		}

		if out := buf.String(); !strings.Contains(out, `msg="changeset commit failed"`) {
			t.Errorf("failed commit trace does not contain %q; got:\n%s",
				`msg="changeset commit failed"`, out)
		}
	})

	// The stale refusal is the record most worth tracing — the working tree
	// moved under a proposed op, and lw.log must name the changeset it
	// happened to, not an empty id. Same hand-edit setup as
	// TestCommitRefusesStale (apply_test.go).
	t.Run("stale_refusal_logs_changeset", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("commit trace, stale", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		rewritten := *page
		rewritten.Body = page.Body + "\nAn appended sentence.\n"
		if _, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    "wiki/concepts/kv-cache.md",
			Before:  page.SHA256(),
			Content: rewritten.Serialize(),
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		handEdited := append(append([]byte{}, page.Serialize()...), []byte("\nSomeone edited this by hand.\n")...)
		if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "kv-cache.md"), handEdited, 0o644); err != nil {
			t.Fatalf("simulate hand-edit: %v", err)
		}
		if err := e.vault.Reload(); err != nil {
			t.Fatalf("reload vault: %v", err)
		}

		buf := captureDefaultLog(t)
		if _, err := e.Commit("should refuse"); !errors.Is(err, ErrStale) {
			t.Fatalf("Commit err = %v, want ErrStale", err)
		}

		out := buf.String()
		for _, want := range []string{
			`msg="changeset commit failed"`,
			"changeset=cs-",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stale refusal trace does not contain %q; got:\n%s", want, out)
			}
		}
	})
}

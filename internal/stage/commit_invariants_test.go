// commit_invariants_test.go pins the two commit-time invariants 008 adds
// (contract §3, MASTER §5 T-D): a changeset with no live op is refused
// before anything is written (ErrNothingToCommit, C-802), and a commit
// carrying live ingest_source ops names their raw paths in log.md, in op
// order, behind a U+2192 arrow.
package stage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitNothingToCommit(t *testing.T) {
	t.Run("all_ops_dropped_is_refused", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("one op, then none", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		// A changeset with no ops at all is refused.
		if _, err := e.Commit("empty"); !errors.Is(err, ErrNothingToCommit) {
			t.Fatalf("Commit with no ops = %v, want an error matching ErrNothingToCommit", err)
		}

		// So is one whose every op was dropped.
		opID := stageKVCachePatch(t, e)
		if err := e.DropOp(opID); err != nil {
			t.Fatalf("DropOp: %v", err)
		}
		if _, err := e.Commit("all dropped"); !errors.Is(err, ErrNothingToCommit) {
			t.Fatalf("Commit with every op dropped = %v, want an error matching ErrNothingToCommit", err)
		}
	})

	t.Run("refused_commit_journals_nothing", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("nothing journaled", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		before, err := e.Journal().Query(Filter{})
		if err != nil {
			t.Fatalf("Journal().Query before: %v", err)
		}

		if _, err := e.Commit("empty"); !errors.Is(err, ErrNothingToCommit) {
			t.Fatalf("Commit = %v, want an error matching ErrNothingToCommit", err)
		}

		after, err := e.Journal().Query(Filter{})
		if err != nil {
			t.Fatalf("Journal().Query after: %v", err)
		}
		if len(after) != len(before) {
			t.Fatalf("journal grew from %d to %d events across a refused commit", len(before), len(after))
		}
		for _, ev := range after {
			if ev.Kind == EvCommitBegin {
				t.Fatalf("refused commit journalled a commit_begin: %+v", ev)
			}
		}
	})

	t.Run("refused_commit_writes_nothing_to_vault", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("nothing to write", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		logBefore, err := os.ReadFile(filepath.Join(dir, "log.md"))
		if err != nil {
			t.Fatalf("read log.md: %v", err)
		}
		snapsBefore, err := os.ReadDir(filepath.Join(dir, ".llmwiki", "snapshots"))
		if err != nil {
			t.Fatalf("read snapshots/: %v", err)
		}

		if _, err := e.Commit("empty"); !errors.Is(err, ErrNothingToCommit) {
			t.Fatalf("Commit = %v, want an error matching ErrNothingToCommit", err)
		}

		logAfter, err := os.ReadFile(filepath.Join(dir, "log.md"))
		if err != nil {
			t.Fatalf("read log.md: %v", err)
		}
		if !bytes.Equal(logBefore, logAfter) {
			t.Fatalf("log.md changed across a refused commit:\nbefore: %q\nafter:  %q", logBefore, logAfter)
		}
		snapsAfter, err := os.ReadDir(filepath.Join(dir, ".llmwiki", "snapshots"))
		if err != nil {
			t.Fatalf("read snapshots/: %v", err)
		}
		if len(snapsAfter) != len(snapsBefore) {
			t.Fatalf("snapshots/ grew from %d to %d entries across a refused commit", len(snapsBefore), len(snapsAfter))
		}

		// The lock was released: a real commit succeeds straight after.
		stageKVCachePatch(t, e)
		if _, err := e.Commit("the follow-up commit"); err != nil {
			t.Fatalf("Commit after a refused one: %v", err)
		}
	})

	t.Run("changeset_stays_open", func(t *testing.T) {
		e, _ := newTestEngine(t)
		cs, err := e.OpenChangeset("still under review", testAuthor)
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		if _, err := e.Commit("empty"); !errors.Is(err, ErrNothingToCommit) {
			t.Fatalf("Commit = %v, want an error matching ErrNothingToCommit", err)
		}
		again, err := e.Current()
		if err != nil {
			t.Fatalf("Current after a refused commit: %v", err)
		}
		if again.ID != cs.ID {
			t.Fatalf("Current after a refused commit names %s, want the open %s", again.ID, cs.ID)
		}
	})
}

func TestLogRecordsRawPaths(t *testing.T) {
	// lastLogLine returns log.md's final "- " entry line.
	lastLogLine := func(t *testing.T, dir string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, "log.md"))
		if err != nil {
			t.Fatalf("read log.md: %v", err)
		}
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.HasPrefix(lines[i], "- ") {
				return lines[i]
			}
		}
		t.Fatalf("log.md has no entry lines:\n%s", b)
		return ""
	}

	t.Run("ingest_commit_names_raw_path", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("ingest a source", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		stageIngest(t, e, "raw/articles/staging-notes.md", "# Staging Notes\n\nA body no committed source carries.\n")
		if _, err := e.Append(Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/staging-notes.md",
			Content:    newConceptPageContent("Staging Notes"),
			Rationale:  "test",
			Provenance: []string{"raw/articles/staging-notes.md"},
		}); err != nil {
			t.Fatalf("Append create_page: %v", err)
		}
		if _, err := e.Commit("ingest and create"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		want := "- 2026-08-29 12:00 000001 ingest a source → raw/articles/staging-notes.md (+1 pages, ~0 edits)"
		if got := lastLogLine(t, dir); got != want {
			t.Fatalf("log line = %q, want %q", got, want)
		}
	})

	t.Run("two_ingests_in_op_order", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("two ingests", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		// Appended zeta first: op order (zeta, alpha) is the reverse of the
		// sorted order, so a sort would fail this.
		stageIngest(t, e, "raw/articles/zeta-source.md", "# Zeta\n\nA zeta body no committed source carries.\n")
		stageIngest(t, e, "raw/articles/alpha-source.md", "# Alpha\n\nAn alpha body no committed source carries.\n")
		if _, err := e.Commit("two ingests"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		want := "- 2026-08-29 12:00 000001 two ingests → raw/articles/zeta-source.md, raw/articles/alpha-source.md (+0 pages, ~0 edits)"
		if got := lastLogLine(t, dir); got != want {
			t.Fatalf("log line = %q, want %q", got, want)
		}
	})

	t.Run("dropped_ingest_not_listed", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("one kept, one dropped", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		gone := stageIngest(t, e, "raw/articles/gone-source.md", "# Gone\n\nA dropped body no committed source carries.\n")
		stageIngest(t, e, "raw/articles/kept-source.md", "# Kept\n\nA kept body no committed source carries.\n")
		if err := e.DropOp(gone); err != nil {
			t.Fatalf("DropOp: %v", err)
		}
		if _, err := e.Commit("one kept, one dropped"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		want := "- 2026-08-29 12:00 000001 one kept, one dropped → raw/articles/kept-source.md (+0 pages, ~0 edits)"
		if got := lastLogLine(t, dir); got != want {
			t.Fatalf("log line = %q, want %q", got, want)
		}
	})

	t.Run("line_without_ingest_unchanged", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("add a page", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		if _, err := e.Append(Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/unchanged-line.md",
			Content:    newConceptPageContent("Unchanged Line"),
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}); err != nil {
			t.Fatalf("Append create_page: %v", err)
		}
		if _, err := e.Commit("add a page"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		want := "- 2026-08-29 12:00 000001 add a page (+1 pages, ~0 edits)"
		if got := lastLogLine(t, dir); got != want {
			t.Fatalf("log line = %q, want the pre-008 format %q", got, want)
		}
	})
}

// commit_confirm_test.go is 008 T-G's review-side evidence (MASTER §5): the
// raw-only changeset commits only on a second, deliberate C, the warning
// names every raw path in live-op order, any other key disarms, a changeset
// carrying a create_page still commits on the first C, and an all-dropped
// changeset gets Engine's nothing-to-commit refusal as a StatusWarn.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// rawIngestContent renders body as a raw source whose frontmatter sha256 is
// the body's own hash — the shape src-integrity lints and validateIngestSource
// dedupes against (008 contract §3). Same shape uitest.rawSourceBytes builds.
func rawIngestContent(sourceURL, body string) []byte {
	sum := sha256.Sum256([]byte(body))
	return []byte(fmt.Sprintf("---\nsource_url: %s\ningested: 2026-09-17\nsha256: %s\n---\n\n%s",
		sourceURL, hex.EncodeToString(sum[:]), body))
}

// appendRawIngest appends one live ingest_source op for path carrying body,
// failing t on any validation error.
func appendRawIngest(t *testing.T, e *stage.Engine, path, body string) string {
	t.Helper()
	opID, err := e.Append(stage.Op{
		Kind:      stage.OpIngestSource,
		Path:      path,
		Extractor: "passthrough",
		Rationale: "raw source proposed for the raw-only confirmation",
		Content:   rawIngestContent("https://example.org/"+filepath.Base(path), body),
	})
	if err != nil {
		t.Fatalf("Append ingest_source %s: %v", path, err)
	}
	return opID
}

// appendPlainCreate appends one valid create_page op linking two existing
// minimal-fixture pages — the op that makes a changeset not raw-only.
func appendPlainCreate(t *testing.T, e *stage.Engine) string {
	t.Helper()
	content := []byte("---\n" +
		"title: Context Windows\n" +
		"created: 2026-09-17\n" +
		"updated: 2026-09-17\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Context Windows\n" +
		"\n" +
		"See [[kv-cache]] and [[flash-attention]] for background.\n")
	opID, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/context-windows.md",
		Content:    content,
		Rationale:  "the raw article's window section deserves its own page",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	})
	if err != nil {
		t.Fatalf("Append create_page: %v", err)
	}
	return opID
}

// journalHasCommitBegin reports whether the vault's journal already records
// a commit_begin — the proof a refused C wrote nothing.
func journalHasCommitBegin(t *testing.T, root string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".llmwiki", "journal.ndjson"))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return strings.Contains(string(b), "commit_begin")
}

// openChangesetID returns the engine's open changeset id.
func openChangesetID(t *testing.T, e *stage.Engine) string {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	return cs.ID
}

// swapRawOnlyChangeset replaces e's open changeset with a fresh raw-only
// one — engine calls only, no key and no pane message — the shape of a
// foreign process committing and re-opening stage under an armed pane.
// It returns the new changeset's id.
func swapRawOnlyChangeset(t *testing.T, e *stage.Engine, intent, path, body string) string {
	t.Helper()
	if err := e.Reject("swapped under the arm"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if _, err := e.OpenChangeset(intent, stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset (%s): %v", intent, err)
	}
	appendRawIngest(t, e, path, body)
	return openChangesetID(t, e)
}

// newRawOnlyModel stages the two-ingest changeset, loads the pane to
// quiescence, and returns it with its engine and vault root.
func newRawOnlyModel(t *testing.T) (ui.Pane, *stage.Engine, string) {
	t.Helper()
	d, e, root := newTestDeps(t, "minimal")
	if _, err := e.OpenChangeset("raw-only changeset", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	appendRawIngest(t, e, "raw/articles/agent-memory.md", "# Agent Memory\n\nNotes outlive the session.\n")
	appendRawIngest(t, e, "raw/articles/context-window.md", "# Context Windows\n\nThe window is finite.\n")
	return initModel(t, d), e, root
}

const wantRawOnlyWarn = "0 pages proposed — this commits raw source(s) only: " +
	"raw/articles/agent-memory.md, raw/articles/context-window.md · press C again to commit"

func TestRawOnlyCommitConfirm(t *testing.T) {
	t.Run("first_C_warns_and_does_not_commit", func(t *testing.T) {
		m, e, root := newRawOnlyModel(t)

		m = send(t, m, keyPress('C'))

		if msg, level := statusOf(t, m); msg != wantRawOnlyWarn || level != ui.StatusWarn {
			t.Errorf("Status after first C = (%q, %v), want the contract §5 warning at StatusWarn", msg, level)
		}
		if journalHasCommitBegin(t, root) {
			t.Error("journal recorded a commit_begin — the first C must not commit")
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current after the refused first C: %v (the changeset must stay open)", err)
		}
		if len(cs.Live()) != 2 {
			t.Errorf("live ops after first C = %d, want both ingests still live", len(cs.Live()))
		}
	})

	t.Run("warning_names_every_raw_path", func(t *testing.T) {
		m, _, _ := newRawOnlyModel(t)

		m = send(t, m, keyPress('C'))

		msg, _ := statusOf(t, m)
		for _, path := range []string{"raw/articles/agent-memory.md", "raw/articles/context-window.md"} {
			if !strings.Contains(msg, path) {
				t.Errorf("warning does not name %s: %q", path, msg)
			}
		}
		// Live-op order, not sorted: agent-memory was appended first.
		if strings.Index(msg, "agent-memory") > strings.Index(msg, "context-window") {
			t.Errorf("warning names the paths out of live-op order: %q", msg)
		}
	})

	t.Run("second_C_commits", func(t *testing.T) {
		m, e, _ := newRawOnlyModel(t)

		m = send(t, m, keyPress('C'))
		m = send(t, m, keyPress('C'))

		msg, level := statusOf(t, m)
		if !strings.HasPrefix(msg, "committed ") || level != ui.StatusGood {
			t.Errorf("Status after second C = (%q, %v), want \"committed <id>\" at StatusGood", msg, level)
		}
		if _, err := e.Current(); !errors.Is(err, stage.ErrNoChangeset) {
			t.Errorf("Current after the commit: %v, want ErrNoChangeset", err)
		}
	})

	t.Run("other_key_disarms", func(t *testing.T) {
		m, e, root := newRawOnlyModel(t)

		m = send(t, m, keyPress('C')) // arms
		m = send(t, m, keyPress('j')) // disarms
		m = send(t, m, keyPress('C')) // warns again, re-arms — still no commit

		if journalHasCommitBegin(t, root) {
			t.Fatal("journal recorded a commit_begin before the third C")
		}
		if msg, level := statusOf(t, m); msg != wantRawOnlyWarn || level != ui.StatusWarn {
			t.Errorf("Status after C,j,C = (%q, %v), want the warning shown again", msg, level)
		}

		m = send(t, m, keyPress('C')) // the third C commits

		if msg, _ := statusOf(t, m); !strings.HasPrefix(msg, "committed ") {
			t.Errorf("Status after the third C = %q, want the commit status", msg)
		}
		if _, err := e.Current(); !errors.Is(err, stage.ErrNoChangeset) {
			t.Errorf("Current after the third C: %v, want ErrNoChangeset", err)
		}

		// C-807: the arm is bound to the armed changeset's id, so a swap
		// with no key press in between cannot inherit it. Arm here, swap
		// the engine to a second raw-only changeset, and deliver the
		// reload the way the shell does — a StageChangedMsg, never a key.
		m, e, root = newRawOnlyModel(t)
		m = send(t, m, keyPress('C')) // arms on the first changeset
		firstID := openChangesetID(t, e)
		secondID := swapRawOnlyChangeset(t, e,
			"second raw-only changeset",
			"raw/articles/swap-target.md",
			"# Swap Target\n\nA body the first changeset never had.\n")
		if secondID == firstID {
			t.Fatalf("the swap kept the changeset id %q", secondID)
		}

		m = send(t, m, ui.StageChangedMsg{}) // the reload, with no key press

		// The load of a different id must itself have cleared the arm,
		// before any C gets a chance to consult the engine (C-807).
		mm, ok := m.(*Model)
		if !ok {
			t.Fatalf("pane is %T, want *review.Model", m)
		}
		if mm.commitArmedFor != "" {
			t.Errorf("the reload left the arm set to %q — a load of a different changeset id must disarm", mm.commitArmedFor)
		}

		m = send(t, m, keyPress('C')) // the NEW changeset's first C: warns, must not commit
		if journalHasCommitBegin(t, root) {
			t.Error("the swap inherited the arm: the new changeset committed without its warning")
		}
		const wantSwap = "0 pages proposed — this commits raw source(s) only: " +
			"raw/articles/swap-target.md · press C again to commit"
		if msg, level := statusOf(t, m); msg != wantSwap || level != ui.StatusWarn {
			t.Errorf("Status after the swapped-in changeset's first C = (%q, %v), want its own warning", msg, level)
		}
		m = send(t, m, keyPress('C')) // the new changeset's own second C commits
		if msg, _ := statusOf(t, m); !strings.HasPrefix(msg, "committed ") {
			t.Errorf("Status after the swapped-in changeset's second C = %q, want the commit status", msg)
		}

		// And when the C lands before the swap's reload does, the armed id
		// no longer matches the engine's open changeset: the id check is
		// the backstop, and this C must still warn instead of committing.
		m, e, root = newRawOnlyModel(t)
		m = send(t, m, keyPress('C')) // arms on the first changeset
		if thirdID := swapRawOnlyChangeset(t, e,
			"third raw-only changeset",
			"raw/articles/in-flight-swap.md",
			"# In-Flight Swap\n\nAnother body, never proposed before.\n"); thirdID == firstID {
			t.Fatalf("the in-flight swap kept the changeset id %q", thirdID)
		}

		m = send(t, m, keyPress('C')) // arrives ahead of any reload: warn, do not commit

		if journalHasCommitBegin(t, root) {
			t.Error("a C ahead of the swap's reload committed the new changeset without its warning")
		}
		const wantInFlight = "0 pages proposed — this commits raw source(s) only: " +
			"raw/articles/in-flight-swap.md · press C again to commit"
		if msg, level := statusOf(t, m); msg != wantInFlight || level != ui.StatusWarn {
			t.Errorf("Status after the in-flight swap's C = (%q, %v), want the new changeset's warning", msg, level)
		}
	})

	t.Run("page_changeset_commits_on_first_C", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("ingest plus create", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		appendRawIngest(t, e, "raw/articles/agent-memory.md", "# Agent Memory\n\nNotes outlive the session.\n")
		appendPlainCreate(t, e)
		m := initModel(t, d)

		m = send(t, m, keyPress('C'))

		if msg, level := statusOf(t, m); !strings.HasPrefix(msg, "committed ") || level != ui.StatusGood {
			t.Errorf("Status after first C = (%q, %v), want a commit on the first C", msg, level)
		}
	})
}

func TestNothingToCommitStatus(t *testing.T) {
	t.Run("all_dropped_shows_refusal", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("all dropped", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		opID := appendTwoHunkPatch(t, e)
		if err := e.DropOp(opID); err != nil {
			t.Fatalf("DropOp: %v", err)
		}
		m := initModel(t, d)

		m = send(t, m, keyPress('C'))

		const want = "commit refused: nothing to commit — every op was dropped"
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status = (%q, %v), want (%q, StatusWarn)", msg, level, want)
		}
		if _, err := e.Current(); err != nil {
			t.Errorf("Current after the refusal: %v (the changeset must stay open)", err)
		}
	})
}

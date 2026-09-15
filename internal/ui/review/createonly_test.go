// createonly_test.go is the C32/D-3U evidence (workflow 003 MASTER §8,
// s2-screens.md T06 keys amendment): in a changeset of only create_page ops
// — the shape of a first ingest — every op contributes zero hunks to
// Engine.Diff(), so the pre-T20 stop list was empty, the Ops panel drew no
// cursor row, and `j` moved nothing. The cursor stops now cover every
// Ops-panel row: one op-level stop per hunkless op, in panel order.
package review

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// stageTwoCreates appends the two create_page ops of the orchestrator's C32
// probe to the open changeset: valid pages (frontmatter title/created/
// updated/type/tags/confidence and a body linking two existing pages), no
// hunks, nothing else staged — a create-only changeset.
func stageTwoCreates(t *testing.T, e *stage.Engine) {
	t.Helper()
	for _, p := range []struct{ slug, title string }{
		{"kv-cache-eviction", "KV Cache Eviction"},
		{"speculative-prefetch", "Speculative Prefetch"},
	} {
		content := []byte("---\n" +
			"title: " + p.title + "\n" +
			"created: 2026-09-16\n" +
			"updated: 2026-09-16\n" +
			"type: concept\n" +
			"tags: [inference]\n" +
			"confidence: medium\n" +
			"---\n" +
			"\n" +
			"# " + p.title + "\n" +
			"\n" +
			"See [[kv-cache]] and [[flash-attention]] for background.\n")
		if _, err := e.Append(stage.Op{
			Kind:       stage.OpCreatePage,
			Path:       "wiki/concepts/" + p.slug + ".md",
			Content:    content,
			Rationale:  "first-ingest create: a whole new page, no hunks",
			Provenance: []string{"raw/articles/kv-cache-explained.md"},
		}); err != nil {
			t.Fatalf("Append %s: %v", p.slug, err)
		}
	}
}

// newCreateOnlyModel builds the C32 probe: the minimal fixture, a fresh
// changeset, two create_page ops, and the review pane loaded to quiescence.
func newCreateOnlyModel(t *testing.T) (ui.Pane, *stage.Engine) {
	t.Helper()
	d, e, _ := newTestDeps(t, "minimal")
	if _, err := e.OpenChangeset("create-only changeset", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	stageTwoCreates(t, e)
	return initModel(t, d), e
}

// currentOpID names the op the cursor stop addresses, and false when it
// addresses nothing at all — the pre-T20 state in a create-only changeset.
func currentOpID(t *testing.T, m ui.Pane) (string, bool) {
	t.Helper()
	mm, ok := m.(*Model)
	if !ok {
		t.Fatal("pane is not *review.Model")
	}
	id, _, ok := resolveCursor(mm.diff, mm.stops, mm.cursor)
	if !ok {
		return "", false
	}
	op, found := findOp(mm.ops, id)
	if !found {
		return "", false
	}
	return op.ID, true
}

// TestReviewCreateOnlyNavigation walks the create-only changeset of the
// C32 probe: `j`/`k` move between the two ops, the Ops panel always shows
// exactly one cursor row on the current op, Preview follows the op cursor,
// and `y`/`n` on an op-level stop take the ownerless-window refusal
// without calling the engine (s2-screens.md T06 keys, D-3U).
func TestReviewCreateOnlyNavigation(t *testing.T) {
	const ownerless = "this window has no hunk id — it cannot be accepted or dropped individually"

	t.Run("j_moves_between_ops_without_hunks", func(t *testing.T) {
		m, _ := newCreateOnlyModel(t)

		if id, ok := currentOpID(t, m); !ok || id != "op1" {
			t.Fatalf("current op after initModel = (%q, %v), want (op1, true)", id, ok)
		}
		m = send(t, m, keyPress('j'))
		if id, ok := currentOpID(t, m); !ok || id != "op2" {
			t.Fatalf("current op after j = (%q, %v), want (op2, true)", id, ok)
		}
		m = send(t, m, keyPress('k'))
		if id, ok := currentOpID(t, m); !ok || id != "op1" {
			t.Fatalf("current op after k = (%q, %v), want (op1, true)", id, ok)
		}
	})

	t.Run("ops_panel_shows_the_cursor", func(t *testing.T) {
		// At 120x38 the Ops panel is the left column, opsW = 40 cells wide
		// (mockgen.review); the cursor glyph sits at content column 0, well
		// inside it. Both basenames fit their row unclipped at this width.
		const opsW = 40

		check := func(stage string, wantBase string) {
			t.Helper()
			rows := strings.Split(stage, "\n")
			marked := -1
			for i, row := range rows {
				if !strings.Contains(ansi.Strip(row), "▌") {
					continue
				}
				if marked >= 0 {
					t.Fatalf("row %d also carries a cursor glyph: exactly one Ops row may", i+1)
				}
				marked = i
				cells := []rune(ansi.Strip(row))
				if len(cells) < opsW || !strings.Contains(string(cells[:opsW]), "▌") {
					t.Fatalf("cursor glyph at row %d is not inside the Ops panel: %q", i+1, string(cells))
				}
				if !strings.Contains(string(cells[:opsW]), wantBase) {
					t.Fatalf("cursor row %d is not %s's row: %q", i+1, wantBase, string(cells[:opsW]))
				}
			}
			if marked < 0 {
				t.Fatal("no Ops row carries the cursor glyph")
			}
		}

		m, _ := newCreateOnlyModel(t)
		check(m.View(120, 38), "kv-cache-eviction.md")
		m = send(t, m, keyPress('j'))
		check(m.View(120, 38), "speculative-prefetch.md")
	})

	t.Run("preview_follows_the_op_cursor", func(t *testing.T) {
		m, _ := newCreateOnlyModel(t)
		m = send(t, m, keyPress('j'))
		m = send(t, m, keyPress('p'))

		view := ansi.Strip(m.View(120, 38))
		if !strings.Contains(view, "╭ Preview ") {
			t.Fatalf("p did not open Preview:\n%s", view)
		}
		if !strings.Contains(view, "op2 · staged page") {
			t.Errorf("Preview lacks op2's head note:\n%s", view)
		}
		if !strings.Contains(view, "wiki/concepts/speculative-prefetch.md") {
			t.Errorf("Preview does not name op2's path:\n%s", view)
		}
		if strings.Contains(view, "wiki/concepts/kv-cache-eviction.md") {
			t.Errorf("Preview still names op1's path:\n%s", view)
		}
		if !strings.Contains(view, "2 of 2") {
			t.Errorf("Ops footnote does not put the cursor on op2:\n%s", view)
		}
	})

	t.Run("y_and_n_refused_on_an_op_without_hunks", func(t *testing.T) {
		m, e := newCreateOnlyModel(t)
		if id, ok := currentOpID(t, m); !ok || id != "op1" {
			t.Fatalf("cursor is not on op1's op-level stop: (%q, %v)", id, ok)
		}
		before := changesetSnapshot(t, e)

		m = send(t, m, keyPress('y'))
		if msg, level := statusOf(t, m); msg != ownerless || level != ui.StatusWarn {
			t.Errorf("after y: Status = (%q, %v), want (%q, StatusWarn)", msg, level, ownerless)
		}
		if after := changesetSnapshot(t, e); after != before {
			t.Errorf("y on an op-level stop changed the changeset:\nbefore:\n%s\nafter:\n%s", before, after)
		}

		m = send(t, m, keyPress('n'))
		if msg, level := statusOf(t, m); msg != ownerless || level != ui.StatusWarn {
			t.Errorf("after n: Status = (%q, %v), want (%q, StatusWarn)", msg, level, ownerless)
		}
		if after := changesetSnapshot(t, e); after != before {
			t.Errorf("n on an op-level stop changed the changeset:\nbefore:\n%s\nafter:\n%s", before, after)
		}

		if _, err := e.Current(); err != nil {
			t.Errorf("Current after the refusals: %v (the changeset should still be open)", err)
		}
	})
}

// changesetSnapshot renders the engine's open changeset — every op's id,
// state and hunk flags, cascades included — as one comparable string. The
// refusals must leave it byte-identical: that is the proof no engine
// method ran.
func changesetSnapshot(t *testing.T, e *stage.Engine) string {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cs=%s intent=%q", cs.ID, cs.Intent)
	var walk func(ops []stage.Op)
	walk = func(ops []stage.Op) {
		for _, op := range ops {
			fmt.Fprintf(&b, "\nop=%s kind=%s state=%s", op.ID, op.Kind, op.State)
			for _, h := range op.Hunks {
				fmt.Fprintf(&b, "\n  hunk=%s dropped=%v", h.ID, h.Dropped)
			}
			walk(op.Cascade)
		}
	}
	walk(cs.Ops)
	return b.String()
}

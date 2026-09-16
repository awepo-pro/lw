// scroll_render_test.go holds the scroll-side half of 005 T-B (contract §5
// note 5): every scroll quantity counts RENDERED lines now that assistant
// prose reflows through the shared markdown renderer, and the window still
// holds still when a rendered block changes height mid-stream (R2) —
// TestTranscriptHoldsStill's settled-block case. The render-and-measure
// plumbing it shares with the other scroll tests lives in
// scroll_stability_test.go.
package ask

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/markdown"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// renderFillerTurns overflows the 80×22 panel several times over: one
// answer line and one done boundary per turn is four to five rendered
// lines, so 15 of them put the pane far past inner + a page step.
const renderFillerTurns = 15

// renderFenceAnswer is a fenced answer whose SOURCE is three lines and
// whose RENDER is one: the fence markers are syntax, not content, so
// rendering them away is what makes a rendered-line count differ from a
// source-line count — the discriminating case for the note arithmetic.
const renderFenceAnswer = "```python\nclient = anthropic.AnthropicVertex(region=\"us-east5\", project_id=PROJECT)\n```"

// newRenderScrollModel builds an ask pane whose conversation is
// renderFillerTurns of filler plus, when fenceTurns is 1, one finished
// fenced-answer turn — applied directly, the builder style the tool-scroll
// tests use. The pane is rendered once so the scroll math sees the size
// the window really has.
func newRenderScrollModel(t *testing.T, fenceTurns int) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "render-scroll-vault")
	m := New(uitest.Deps(v, true, nil)).(*Model)
	applyFillerTurns(m, 1, renderFillerTurns)
	for i := 0; i < fenceTurns; i++ {
		m.applyEvent(agent.TextDelta{Text: renderFenceAnswer})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
	}
	_, _ = uitest.PaneScreen(m, scrollW, scrollH)
	return m
}

// renderedFenceWidth is the width conversationLines renders at for the
// scroll test's pane: min(scrollW-4, 100).
func renderedFenceWidth() int { return min(scrollW-4, 100) }

func TestScrollCountsRenderedLines(t *testing.T) {
	// earlier_note_counts_rendered_lines: with an answer tall enough to
	// overflow, `↑ N earlier` counts RENDERED lines — the note is computed
	// from the same rendered list the panel draws, where the fence answer
	// is one line, not its source's three.
	t.Run("earlier_note_counts_rendered_lines", func(t *testing.T) {
		m := newRenderScrollModel(t, 1)
		total := requireOverflow(t, m)

		// What the transcript SHOULD count: the same conversation with the
		// answer's lines as the shared renderer produces them, not as the
		// source carries them.
		frag, err := markdown.NewRenderer().RenderFragment([]byte(renderFenceAnswer),
			markdown.Options{Width: renderedFenceWidth(), Style: renderMDStyle(m)})
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		base := newRenderScrollModel(t, 0) // the same conversation, answerless
		// The answer turn renders as: blank, assistant marker, the fragment's
		// lines, blank, status.
		wantTotal := len(base.transcriptLines()) + 1 + 1 + len(frag) + 1 + 1
		if len(frag) >= strings.Count(renderFenceAnswer, "\n")+1 {
			t.Fatalf("precondition lost: the fenced answer no longer renders (%d) shorter than its source (%d lines), so this test can no longer tell rendered from source counts",
				len(frag), strings.Count(renderFenceAnswer, "\n")+1)
		}
		if total != wantTotal {
			t.Fatalf("the transcript holds %d lines, want %d — the %d-line answer must contribute its %d rendered lines plus blank, blank and status",
				total, wantTotal, strings.Count(renderFenceAnswer, "\n")+1, len(frag))
		}

		step := max(1, scrollInner-1)
		if _, cmd := m.Update(uitest.Key("pgup")); cmd != nil {
			t.Fatalf("pgup produced a command (%v), want nil", cmd)
		}
		styled, _ := renderPane(t, m)
		top, ok := noteNumber(t, 0, styled[0], "↑ ")
		if !ok {
			t.Fatalf("after pgup the top border has no `↑ N earlier`: %q", styled[0])
		}
		if want := total - scrollInner - step; top != want {
			t.Fatalf("the note reads `↑ %d earlier`, want %d (rendered total %d - inner %d - step %d)",
				top, want, total, scrollInner, step)
		}
	})

	// selected_tool_row_maps_to_its_rendered_line: with a rendered answer
	// above it, the selected tool call's cursor row is still its own head
	// line — on the model and on the drawn gutter.
	t.Run("selected_tool_row_maps_to_its_rendered_line", func(t *testing.T) {
		m := newRenderScrollModel(t, 1)
		m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"kv cache"}`})
		if _, cmd := m.Update(uitest.Key("down")); cmd != nil {
			t.Fatalf("down produced a command (%v), want nil", cmd)
		}
		if m.selected < 0 {
			t.Fatal("down selected no tool call")
		}
		_, _ = uitest.PaneScreen(m, scrollW, scrollH)

		lines, cursor := m.conversationLines(renderedFenceWidth())
		if head := toolHeadIndex(t, m); cursor != head {
			t.Fatalf("the cursor row is %d, want the tool call's head line %d among %d rendered lines",
				cursor, head, len(lines))
		}

		styled, plain := renderPane(t, m)
		gutters := 0
		for i, row := range plain {
			if !strings.Contains(row, "▌") {
				continue
			}
			gutters++
			content := contentRow(strings.Replace(row, "▌", " ", 1))
			if !strings.HasPrefix(content, "▸ ") {
				t.Fatalf("the cursor gutter sits on row %d with %q, want the tool call's ▸ head line", i, content)
			}
		}
		if gutters != 1 {
			t.Fatalf("%d rows carry the cursor gutter, want exactly one:\n%s", gutters, strings.Join(plain, "\n"))
		}
		_ = styled
	})
}

// TestTranscriptHoldsStill is W5d/T34's window-stability guarantee, carried
// into the rendered-prose world (005 R2): a rendered block changing height
// MID-STREAM — here a list that is one plain tail line until a blank line
// settles it into the renderer's shape — must leave every visible row
// byte-identical and the window's start pinned, with the accounting
// absorbing the growth below the window.
func TestTranscriptHoldsStill(t *testing.T) {
	m := newRenderScrollModel(t, 0)
	m.applyEvent(agent.TextDelta{Text: "alpha\n\n- kv only"}) // live: settled + open list in the tail
	_, _ = uitest.PaneScreen(m, scrollW, scrollH)

	if _, cmd := m.Update(uitest.Key("pgup")); cmd != nil {
		t.Fatalf("pgup produced a command (%v), want nil", cmd)
	}
	styledBefore, _ := renderPane(t, m)
	total0 := len(m.transcriptLines())
	start0 := total0 - scrollInner - m.back
	n0, ok := footnote(t, styledBefore)
	if !ok {
		t.Fatal("precondition failed: the pane is not scrolled up (no `↓ N newer`)")
	}
	top0, ok := noteNumber(t, 0, styledBefore[0], "↑ ")
	if !ok {
		t.Fatalf("precondition failed: the top border has no `↑ N earlier`: %q", styledBefore[0])
	}

	// A blank line closes the list: it leaves the plain tail and settles as
	// a rendered block, and the tail regrows below it. The rendered height
	// of the entry changes mid-stream — exactly the shape R2 warns about.
	pane, _ := m.Update(ui.EventMsg{Ev: agent.TextDelta{Text: "\n\n- second"}})
	m = pane.(*Model)
	total1 := len(m.transcriptLines())
	added := total1 - total0
	if added <= 0 {
		t.Fatal("settling the list added no rendered lines; the test is not exercising the height change")
	}

	styledAfter, _ := renderPane(t, m)
	for i := 0; i < scrollInner; i++ {
		if styledAfter[1+i] != styledBefore[1+i] {
			t.Fatalf("settling the list moved content row %d:\nbefore %q\nafter  %q",
				i+1, styledBefore[1+i], styledAfter[1+i])
		}
	}
	if start := total1 - scrollInner - m.back; start != start0 {
		t.Fatalf("the window start moved from %d to %d, want it pinned on the same conversation line", start0, start)
	}
	if top1, ok := noteNumber(t, 0, styledAfter[0], "↑ "); !ok || top1 != top0 {
		t.Fatalf("`↑ N earlier` = %d (ok=%v) after the settle, want the unchanged %d", top1, ok, top0)
	}
	if n1, ok := footnote(t, styledAfter); !ok || n1 != n0+added {
		t.Fatalf("`↓ N newer` = %d (ok=%v) after the settle, want %d (%d +%d rendered lines added)",
			n1, ok, n0+added, n0, added)
	}
}

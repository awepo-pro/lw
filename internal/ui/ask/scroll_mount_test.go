// scroll_mount_test.go is the 025 T2 fresh-context review's pin for the
// tail-mount split in mutateEntries (scroll.go): the conversation's tail
// chrome — 023's thinking rise, 025's sending row — must be accounted for
// separately from entry content, because a mount toggling in the same
// mutation as an above-window content change otherwise moves every visible
// row. The legs here table the mount combinations the delivered
// scroll_test.go does not: the rise replacing the sending row (1→4), the
// turn's end unmounting it (4→0), the partially-visible mount clamping to
// the tail, and the review's red check — a resolved tool wholly above the
// window while the rise is mounted (4→1), the shape that was latent since
// 023 and fails against the pre-025 single-number accounting.
package ask

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// assertRowsPinned fails when any of the scrollInner transcript rows moved
// between the two renders — the window-stability invariant every scrolled
// mutation is accounted against. The top border is asserted separately by
// each leg: its `↑ N earlier` note legitimately changes when content grows
// above the window (the rows ride that shift by design).
func assertRowsPinned(t *testing.T, before, after []string) {
	t.Helper()
	for i := 0; i < scrollInner; i++ {
		if after[1+i] != before[1+i] {
			t.Fatalf("transcript content row %d moved under a pinned window:\nbefore %q\nafter  %q",
				i+1, before[1+i], after[1+i])
		}
	}
}

// assertTopNote reads the `↑ N earlier` count off both top borders and
// fails unless it moved by exactly delta.
func assertTopNote(t *testing.T, before, after []string, delta int) {
	t.Helper()
	top0, ok0 := noteNumber(t, 0, before[0], "↑ ")
	top1, ok1 := noteNumber(t, 0, after[0], "↑ ")
	if !ok0 || !ok1 || top1 != top0+delta {
		t.Fatalf("`↑ N earlier` went from %d (ok=%v) to %d (ok=%v), want %+d", top0, ok0, top1, ok1, delta)
	}
}

// TestScrollAccountingTailMounts drives mount toggles through the real
// event fold while the pane is scrolled up, asserting the rows stay put
// and `↓ N newer` moves by exactly the mount's share.
func TestScrollAccountingTailMounts(t *testing.T) {
	// scrolled builds a pane over five finished turns and pages it up one
	// step — back > 0, so every following mutation goes through the
	// accounting rather than the follow-the-tail early return.
	scrolled := func(t *testing.T) *Model {
		t.Helper()
		m := newScrollModel(t, 5)
		if _, cmd := m.Update(uitest.Key("pgup")); cmd != nil {
			t.Fatalf("pgup produced a command (%v), want nil", cmd)
		}
		if m.back <= 0 {
			t.Fatal("precondition failed: the pane did not scroll up")
		}
		return m
	}

	t.Run("first_reasoning_replaces_row_with_rise_keeps_view", func(t *testing.T) {
		// The real flow's first mutation: a turn whose round has seen
		// nothing (the sending row mounted, 1 line) receives its first
		// ReasoningDelta — the rise (4 lines) replaces the row and no
		// entry changes. The mount's +3 must absorb into back alone.
		m := scrolled(t)
		m.turnActive = true // as beginTurn sets it; the sending row mounts with it
		styledBefore, _ := renderPane(t, m)
		n0, ok := footnote(t, styledBefore)
		if !ok {
			t.Fatal("precondition failed: the pane is not scrolled up")
		}
		if m.mountedTailLines() != 1 {
			t.Fatal("precondition failed: the sending row is not mounted")
		}
		total := len(m.transcriptLines())

		if _, cmd := m.Update(ui.EventMsg{Ev: agent.ReasoningDelta{Text: "hmm"}}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		if got := m.mountedTailLines(); got != 4 {
			t.Fatalf("mountedTailLines after the first reasoning = %d, want 4 (the rise + the bare line)", got)
		}
		if got := len(m.transcriptLines()); got != total+3 {
			t.Fatalf("the rise replacing the row took the list from %d to %d, want +3", total, got)
		}
		styledAfter, _ := renderPane(t, m)
		assertRowsPinned(t, styledBefore, styledAfter)
		assertTopNote(t, styledBefore, styledAfter, 0) // no content above the window moved
		if n1, ok := footnote(t, styledAfter); !ok || n1 != n0+3 {
			t.Fatalf("`↓ N newer` = %d (ok=%v), want %d (the rise's three extra hidden lines)", n1, ok, n0+3)
		}
	})

	t.Run("done_unmounts_rise_keeps_view", func(t *testing.T) {
		// The turn's end under the mounted rise: the rise (4 lines)
		// unmounts with the thinking phase while the boundary line (1)
		// appends as content. Net: the hidden-below count drops by three,
		// the rows on screen never move.
		m := scrolled(t)
		m.turnActive = true
		if _, cmd := m.Update(ui.EventMsg{Ev: agent.ReasoningDelta{Text: "hmm"}}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		styledBefore, _ := renderPane(t, m)
		n0, ok := footnote(t, styledBefore)
		if !ok {
			t.Fatal("precondition failed: the pane is not scrolled up")
		}
		total := len(m.transcriptLines())

		if _, cmd := m.Update(ui.EventMsg{Ev: agent.DoneEv{Reason: "stop", Rounds: 1}}); cmd != nil {
			t.Fatal("DoneEv produced a command")
		}
		if got := m.mountedTailLines(); got != 0 {
			t.Fatalf("mountedTailLines after the turn = %d, want 0", got)
		}
		if got := len(m.transcriptLines()); got != total-3 {
			t.Fatalf("the turn's end took the list from %d to %d, want -3 (rise gone, boundary added)", total, got)
		}
		styledAfter, _ := renderPane(t, m)
		assertRowsPinned(t, styledBefore, styledAfter)
		assertTopNote(t, styledBefore, styledAfter, 0) // the boundary appended below the window
		if n1, ok := footnote(t, styledAfter); !ok || n1 != n0-3 {
			t.Fatalf("`↓ N newer` = %d (ok=%v), want %d (the rise's four hidden lines gone, the boundary's one hidden)", n1, ok, n0-3)
		}
	})

	t.Run("partially_visible_mount_clamps_to_tail", func(t *testing.T) {
		// The edge the mount-split design names and no other leg pins:
		// back smaller than the mount, so part of the rise sits inside the
		// window. When the turn ends, the mount's -4 can only absorb down
		// to back 0 — the tail re-attaches and the boundary line is the
		// window's last row.
		m := newScrollModel(t, 5)
		m.turnActive = true
		if _, cmd := m.Update(ui.EventMsg{Ev: agent.ReasoningDelta{Text: "hmm"}}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		m.scrollBy(2) // back 2 < the rise's 4: its last two rows hidden
		styled, _ := renderPane(t, m)
		if n, ok := footnote(t, styled); !ok || n != 2 {
			t.Fatalf("precondition failed: `↓ N newer` = %d (ok=%v), want 2", n, ok)
		}
		if !strings.Contains(strings.Join(styled, "\n"), "▀███████▀") {
			t.Fatal("precondition failed: no rise row is visible — the mount is not partially visible")
		}

		if _, cmd := m.Update(ui.EventMsg{Ev: agent.DoneEv{Reason: "stop", Rounds: 1}}); cmd != nil {
			t.Fatal("DoneEv produced a command")
		}
		if m.back != 0 {
			t.Fatalf("back = %d after the mount collapsed, want 0 (the tail re-attaches)", m.back)
		}
		styled, plain := renderPane(t, m)
		if _, newer := footnote(t, styled); newer {
			t.Fatalf("the bottom border still reads %q, want the tail re-attached", styled[scrollInner+1])
		}
		if got := transcriptContent(plain)[scrollInner-1]; got != "done · 1 rounds" {
			t.Fatalf("the tail's last row = %q, want the turn boundary", got)
		}
	})

	t.Run("tool_result_under_the_rise_keeps_view", func(t *testing.T) {
		// The review's red check, and the shape latent since 023: a tool
		// RESOLVED wholly above the window while the thinking rise is
		// mounted — the in-place resolve is an above-window content
		// change, the rise handing back to the waiting row is a -3 mount
		// toggle, and the two land in one mutation. The single-number
		// accounting (pre-025) saw an above-window first change, skipped
		// the absorb, and let the window ride the mount delta: every row
		// moved. The split skips the content branch on content alone and
		// absorbs the mount: rows pinned, `↓ N newer` drops by exactly 3.
		// (The drive applies the events directly, tool_scroll_test's
		// style: these legs lean on the geometry, not on the stream pump.)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		v := uitest.PublicVault(t, "scroll-mount-vault")
		m := New(uitest.Deps(v, true, nil)).(*Model)
		applyFillerTurns(m, 1, 3) // above the tool turn
		m.applyEvent(agent.TextDelta{Text: "answer one"})
		m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"kv cache"}`})
		applyFillerTurns(m, 4, 6) // below the tool turn: the resolve lands above the window
		m.echoUser("why?")
		m.turnActive = true                             // the live turn, still waiting
		m.applyEvent(agent.ReasoningDelta{Text: "hmm"}) // the rise mounts over the live turn
		m.selected = 7                                  // the t1 entry: six filler entries, then the answer, then the call
		m.toggleSelectedExpand()
		_, _ = uitest.PaneScreen(m, scrollW, scrollH)

		m.scrollBy(pageStep(m.transcriptInner()))
		start := len(m.transcriptLines()) - scrollInner - m.back
		head := toolHeadIndex(t, m)
		if m.back <= 0 {
			t.Fatal("precondition failed: the pane did not scroll up")
		}
		if head+3 > start {
			t.Fatalf("precondition failed: the expanded tool call (head %d) is not wholly above the window start %d", head, start)
		}
		if m.mountedTailLines() != 4 {
			t.Fatal("precondition failed: the rise is not mounted")
		}
		styledBefore, _ := renderPane(t, m)
		n0, ok := footnote(t, styledBefore)
		if !ok {
			t.Fatal("precondition failed: no `↓ N newer`")
		}
		total := len(m.transcriptLines())

		pane, _ := m.Update(ui.EventMsg{Ev: agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "r1\nr2\nr3"}})
		m = pane.(*Model)
		if got := m.mountedTailLines(); got != 1 {
			t.Fatalf("mountedTailLines after the resolve = %d, want 1 (the sending row)", got)
		}
		// The resolve's own growth is +2 (three result lines replace the
		// `…` row); the mount gives back 3: 56-class totals net to -1.
		if got := len(m.transcriptLines()); got != total+2-3 {
			t.Fatalf("the resolve took the list from %d to %d, want +2 content -3 mount", total, got)
		}
		styledAfter, _ := renderPane(t, m)
		assertRowsPinned(t, styledBefore, styledAfter)
		assertTopNote(t, styledBefore, styledAfter, 2) // the resolve's own growth above the window
		if n1, ok := footnote(t, styledAfter); !ok || n1 != n0-3 {
			t.Fatalf("`↓ N newer` = %d (ok=%v), want %d (the rise's four lines out, the sending row in)", n1, ok, n0-3)
		}
	})
}

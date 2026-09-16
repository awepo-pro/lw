// scroll_test.go covers the transcript's tail-follow scrollback (W5 F2/C36,
// s2-screens.md T08 "Scroll"): `back` is the number of conversation lines
// hidden BELOW the panel, 0 means following the tail, and the six shell
// scroll bindings plus the wheel move it. Every assertion is on what the
// pane renders — the notes on the panel borders and the visible rows — the
// way the user actually sees a scroll. The render-and-measure plumbing
// lives in scroll_stability_test.go.
package ask

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

func TestAskTranscriptScroll(t *testing.T) {
	// pgup_leaves_tail is the C36 correction: after pgup the pane must
	// actually stay put — the bottom border says `↓ N newer` with N = the
	// page step max(1, inner-1), and the top note's `↑ N earlier` shrank by
	// that same N. (Written first, and shown failing on the pre-T28 code,
	// whose pgup did nothing.)
	t.Run("pgup_leaves_tail", func(t *testing.T) {
		m := newScrollModel(t, 5)
		total := requireOverflow(t, m)
		step := max(1, scrollInner-1)

		styled, _ := renderPane(t, m)
		top, ok := noteNumber(t, 0, styled[0], "↑ ")
		if !ok {
			t.Fatalf("full transcript has no `↑ N earlier` note on the top border: %q", styled[0])
		}
		if top != total-scrollInner {
			t.Fatalf("full transcript note = `↑ %d earlier`, want %d (total %d - inner %d)",
				top, total-scrollInner, total, scrollInner)
		}
		if _, newer := footnote(t, styled); newer {
			t.Fatalf("following the tail already shows `↓ N newer`: %q", styled[scrollInner+1])
		}

		if _, cmd := m.Update(uitest.Key("pgup")); cmd != nil {
			t.Fatalf("pgup produced a command (%v), want nil", cmd)
		}

		styled, _ = renderPane(t, m)
		got, ok := footnote(t, styled)
		if !ok {
			t.Fatalf("after pgup the bottom border has no `↓ N newer` (C36: the pane did not scroll): %q", styled[scrollInner+1])
		}
		if got != step {
			t.Fatalf("after pgup the bottom border reads `↓ %d newer`, want %d", got, step)
		}
		shrunk, ok := noteNumber(t, 0, styled[0], "↑ ")
		if !ok {
			t.Fatalf("after pgup the top border lost its `↑ N earlier` note: %q", styled[0])
		}
		if shrunk != top-step {
			t.Fatalf("after pgup the top note reads `↑ %d earlier`, want %d (was %d, shrank by %d)",
				shrunk, top-step, top, step)
		}
	})

	// new_event_keeps_view: while scrolled up, an event that appends lines
	// must not move the rows on screen — back absorbs the growth, so every
	// transcript content row stays byte-identical and `↓ N newer` grows by
	// exactly the number of lines added.
	t.Run("new_event_keeps_view", func(t *testing.T) {
		m := newScrollModel(t, 5)
		requireOverflow(t, m)
		if _, cmd := m.Update(uitest.Key("pgup")); cmd != nil {
			t.Fatalf("pgup produced a command (%v), want nil", cmd)
		}
		styledBefore, _ := renderPane(t, m)
		n0, ok := footnote(t, styledBefore)
		if !ok {
			t.Fatal("precondition failed: the pane is not scrolled up (no `↓ N newer`)")
		}

		before := len(m.transcriptLines())
		pane, _ := m.Update(ui.EventMsg{Ev: agent.TextDelta{Text: "fresh prose"}})
		m = pane.(*Model)
		added := len(m.transcriptLines()) - before
		if added <= 0 {
			t.Fatal("the delivered event added no lines; the test is not exercising the pin")
		}

		styledAfter, _ := renderPane(t, m)
		if styledAfter[0] != styledBefore[0] {
			t.Fatalf("the top border moved under a pinned window:\nbefore %q\nafter  %q", styledBefore[0], styledAfter[0])
		}
		for i := 0; i < scrollInner; i++ {
			if styledAfter[1+i] != styledBefore[1+i] {
				t.Fatalf("transcript content row %d moved under a pinned window:\nbefore %q\nafter  %q",
					i+1, styledBefore[1+i], styledAfter[1+i])
			}
		}
		n1, ok := footnote(t, styledAfter)
		if !ok {
			t.Fatal("after the event the bottom border lost its `↓ N newer`")
		}
		if n1 != n0+added {
			t.Fatalf("`↓ N newer` grew from %d to %d, want %d (%d +%d lines added)", n0, n1, n0+added, n0, added)
		}
	})

	// submit_reattaches: submitting re-attaches the tail — back 0, no
	// `↓ … newer`, the tail's last lines on screen.
	t.Run("submit_reattaches", func(t *testing.T) {
		m := newScrollModel(t, 5)
		requireOverflow(t, m)
		if _, cmd := m.Update(uitest.Key("pgup")); cmd != nil {
			t.Fatalf("pgup produced a command (%v), want nil", cmd)
		}

		m = runScriptedTurn(t, m, "again?")

		styled, plain := renderPane(t, m)
		if _, newer := footnote(t, styled); newer {
			t.Fatalf("after a submit the bottom border still reads %q, want the tail re-attached", styled[scrollInner+1])
		}
		// Tail-following means the window is exactly the last inner lines.
		for i, row := range transcriptContent(plain) {
			if want := tailLine(t, m, i); row != want {
				t.Fatalf("after a submit content row %d = %q, want the tail's %q", i+1, row, want)
			}
		}
	})

	// end_reattaches: the same, with the end key.
	t.Run("end_reattaches", func(t *testing.T) {
		m := newScrollModel(t, 5)
		requireOverflow(t, m)
		if _, cmd := m.Update(uitest.Key("pgup")); cmd != nil {
			t.Fatalf("pgup produced a command (%v), want nil", cmd)
		}
		if _, newer := mustFootnote(t, m); !newer {
			t.Fatal("precondition failed: pgup did not scroll up")
		}

		if _, cmd := m.Update(uitest.Key("end")); cmd != nil {
			t.Fatalf("end produced a command (%v), want nil", cmd)
		}

		styled, plain := renderPane(t, m)
		if _, newer := footnote(t, styled); newer {
			t.Fatalf("after end the bottom border still reads %q, want the tail re-attached", styled[scrollInner+1])
		}
		for i, row := range transcriptContent(plain) {
			if want := tailLine(t, m, i); row != want {
				t.Fatalf("after end content row %d = %q, want the tail's %q", i+1, row, want)
			}
		}
	})

	// home_shows_first_line: home goes to the conversation's first line,
	// with no `↑ … earlier` left above it.
	t.Run("home_shows_first_line", func(t *testing.T) {
		m := newScrollModel(t, 5)
		requireOverflow(t, m)

		if _, cmd := m.Update(uitest.Key("home")); cmd != nil {
			t.Fatalf("home produced a command (%v), want nil", cmd)
		}

		styled, plain := renderPane(t, m)
		if _, ok := noteNumber(t, 0, styled[0], "↑ "); ok {
			t.Fatalf("at the top the top border still reads %q, want no `↑ N earlier`", styled[0])
		}
		if first := transcriptContent(plain)[0]; first != "you" {
			t.Fatalf("at the top the first content row = %q, want the conversation's first line %q", first, "you")
		}
	})

	// wheel_over_message_ignored: a notch over the Message panel does
	// nothing; over the Transcript panel it scrolls three lines per notch.
	t.Run("wheel_over_message_ignored", func(t *testing.T) {
		m := newScrollModel(t, 5)
		requireOverflow(t, m)
		styledBefore, _ := renderPane(t, m)

		// Y = scrollH-3 is the Message panel's first row (view.go's layout:
		// the transcript's th rows, then the 3-row Message panel). Both
		// directions must leave the render untouched.
		for _, delta := range []int{-1, 1} {
			if _, cmd := m.Update(ui.WheelMsg{X: 40, Y: scrollH - 3, W: scrollW, H: scrollH, Delta: delta}); cmd != nil {
				t.Fatalf("the wheel produced a command (%v), want nil", cmd)
			}
			styledAfter, _ := renderPane(t, m)
			if strings.Join(styledAfter, "\n") != strings.Join(styledBefore, "\n") {
				t.Fatalf("a wheel notch over the Message panel (delta %+d) changed the render", delta)
			}
		}

		// Contrast: the same notch over the Transcript panel scrolls three
		// lines — Delta -1 is a wheel up, which hides newer lines below.
		if _, cmd := m.Update(ui.WheelMsg{X: 40, Y: 5, W: scrollW, H: scrollH, Delta: -1}); cmd != nil {
			t.Fatalf("the wheel produced a command (%v), want nil", cmd)
		}
		styledAfter, _ := renderPane(t, m)
		if n, ok := footnote(t, styledAfter); !ok || n != 3 {
			t.Fatalf("a wheel up over the Transcript reads `↓ %d newer` (ok=%v), want `↓ 3 newer`", n, ok)
		}
	})

	// empty_transcript_does_not_scroll: with no entries, pgdown (and pgup)
	// leave the empty state byte-identical.
	t.Run("empty_transcript_does_not_scroll", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		v := uitest.PublicVault(t, "scroll-vault")
		m := New(uitest.Deps(v, true, nil)).(*Model)
		styledBefore, _ := renderPane(t, m)

		for _, keyName := range []string{"pgdown", "pgup"} {
			if _, cmd := m.Update(uitest.Key(keyName)); cmd != nil {
				t.Fatalf("%s produced a command (%v), want nil", keyName, cmd)
			}
			styledAfter, _ := renderPane(t, m)
			if strings.Join(styledAfter, "\n") != strings.Join(styledBefore, "\n") {
				t.Fatalf("%s changed the empty transcript's render", keyName)
			}
		}
	})

	// tool_result_growth_above_window_keeps_view (W5d/T34): a ToolResEv
	// that grows an expanded tool call ABOVE the window must not move a
	// single visible row — the growth shifted every row the window names,
	// and a window named from the end rides that shift with back unchanged.
	// (Absorbing the growth into back pins the window's indexes over rows
	// that have moved — the pre-fix behaviour this is written against.) The
	// notes stay consistent with the window shown: `↓ N newer` is back,
	// unchanged; `↑ N earlier` is the window's start, grown by exactly the
	// lines the result added above it.
	t.Run("tool_result_growth_above_window_keeps_view", func(t *testing.T) {
		m := newToolScrollModel(t)
		if _, cmd := m.Update(uitest.Key("down")); cmd != nil {
			t.Fatalf("down produced a command (%v), want nil", cmd)
		}
		if _, cmd := m.Update(specialKey(tea.KeyEnter, 0)); cmd != nil {
			t.Fatalf("enter produced a command (%v), want nil", cmd)
		}
		if _, cmd := m.Update(uitest.Key("pgup")); cmd != nil {
			t.Fatalf("pgup produced a command (%v), want nil", cmd)
		}
		start := len(m.transcriptLines()) - scrollInner - m.back
		if head := toolHeadIndex(t, m); head+3 > start {
			t.Fatalf("precondition failed: the expanded tool call (head %d) is not wholly above the window start %d",
				head, start)
		}

		styledBefore, _ := renderPane(t, m)
		n0, ok := footnote(t, styledBefore)
		if !ok {
			t.Fatal("precondition failed: the pane is not scrolled up (no `↓ N newer`)")
		}
		top0, ok := noteNumber(t, 0, styledBefore[0], "↑ ")
		if !ok {
			t.Fatalf("precondition failed: the top border has no `↑ N earlier`: %q", styledBefore[0])
		}

		before := len(m.transcriptLines())
		pane, _ := m.Update(ui.EventMsg{Ev: agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "r1\nr2\nr3"}})
		m = pane.(*Model)
		added := len(m.transcriptLines()) - before
		if added <= 0 {
			t.Fatal("the delivered ToolResEv added no lines; the test is not exercising the growth")
		}

		styledAfter, _ := renderPane(t, m)
		for i := 0; i < scrollInner; i++ {
			if styledAfter[1+i] != styledBefore[1+i] {
				t.Fatalf("a result landing above the window moved content row %d:\nbefore %q\nafter  %q",
					i+1, styledBefore[1+i], styledAfter[1+i])
			}
		}
		if n1, ok := footnote(t, styledAfter); !ok || n1 != n0 {
			t.Fatalf("`↓ N newer` = %d (ok=%v) after the result, want the unchanged %d", n1, ok, n0)
		}
		if top1, ok := noteNumber(t, 0, styledAfter[0], "↑ "); !ok || top1 != top0+added {
			t.Fatalf("`↑ N earlier` = %d (ok=%v) after the result, want %d (%d +%d added above the window)",
				top1, ok, top0+added, top0, added)
		}
	})

	// expand_above_window_keeps_view (W5d/T34): enter on a tool call above
	// the window — expand, then collapse — must leave every visible row
	// byte-identical both times. The toggle changes the rendered line
	// count; the accounting every entries mutation goes through, not the
	// render-side clamp, is what decides what the window does about it.
	t.Run("expand_above_window_keeps_view", func(t *testing.T) {
		m := newToolScrollModel(t)
		if _, cmd := m.Update(uitest.Key("down")); cmd != nil {
			t.Fatalf("down produced a command (%v), want nil", cmd)
		}
		if _, cmd := m.Update(uitest.Key("pgup")); cmd != nil {
			t.Fatalf("pgup produced a command (%v), want nil", cmd)
		}
		start := len(m.transcriptLines()) - scrollInner - m.back
		if head := toolHeadIndex(t, m); head+3 > start {
			t.Fatalf("precondition failed: the tool call (head %d) is not wholly above the window start %d",
				head, start)
		}

		styledBefore, _ := renderPane(t, m)
		if _, ok := footnote(t, styledBefore); !ok {
			t.Fatal("precondition failed: the pane is not scrolled up (no `↓ N newer`)")
		}

		press := func(phase string) {
			t.Helper()
			if _, cmd := m.Update(specialKey(tea.KeyEnter, 0)); cmd != nil {
				t.Fatalf("%s produced a command (%v), want nil", phase, cmd)
			}
			styledAfter, _ := renderPane(t, m)
			for i := 0; i < scrollInner; i++ {
				if styledAfter[1+i] != styledBefore[1+i] {
					t.Fatalf("%s of the above-window tool call moved content row %d:\nbefore %q\nafter  %q",
						phase, i+1, styledBefore[1+i], styledAfter[1+i])
				}
			}
		}
		press("expanding")
		press("collapsing")
	})

	// expand_inside_window_keeps_view (W5d/T34, second pass): enter on a
	// tool call INSIDE a scrolled-up window — expand, then collapse — must
	// leave every window row above the call byte-identical and the window
	// starting on the same conversation line. This is the behaviour the
	// mutateEntries routing actually changed: the above-window case was
	// already safe by construction (its subtest passed before the fix),
	// while expanding in-window moved four of the seventeen visible rows —
	// measured by a probe that was deleted with the fix. This subtest is
	// that probe's permanent form.
	t.Run("expand_inside_window_keeps_view", func(t *testing.T) {
		m := newInsideToolScrollModel(t)
		if _, cmd := m.Update(uitest.Key("down")); cmd != nil {
			t.Fatalf("down produced a command (%v), want nil", cmd)
		}
		if _, cmd := m.Update(uitest.Key("pgup")); cmd != nil {
			t.Fatalf("pgup produced a command (%v), want nil", cmd)
		}
		total := len(m.transcriptLines())
		start := total - scrollInner - m.back
		head := toolHeadIndex(t, m)
		if m.back <= 0 {
			t.Fatal("precondition failed: the pane is not scrolled up (back = 0)")
		}
		if head <= start || head+3 > start+scrollInner {
			t.Fatalf("precondition failed: the tool call (head %d) is not wholly inside the window [%d,%d)",
				head, start, start+scrollInner)
		}

		styledBefore, _ := renderPane(t, m)
		n0, ok := footnote(t, styledBefore)
		if !ok {
			t.Fatal("precondition failed: the pane is not scrolled up (no `↓ N newer`)")
		}
		top0, ok := noteNumber(t, 0, styledBefore[0], "↑ ")
		if !ok {
			t.Fatalf("precondition failed: the top border has no `↑ N earlier`: %q", styledBefore[0])
		}
		above := head - start // window rows above the call's head line

		press := func(phase string, wantTotal, wantBack int) {
			t.Helper()
			if _, cmd := m.Update(specialKey(tea.KeyEnter, 0)); cmd != nil {
				t.Fatalf("%s produced a command (%v), want nil", phase, cmd)
			}
			if got := len(m.transcriptLines()); got != wantTotal {
				t.Fatalf("%s left the conversation at %d lines, want %d — the toggle did not land", phase, got, wantTotal)
			}
			if s := len(m.transcriptLines()) - scrollInner - m.back; s != start {
				t.Fatalf("%s moved the window start from %d to %d, want it pinned on the same conversation line", phase, start, s)
			}
			styledAfter, _ := renderPane(t, m)
			for i := 0; i < above; i++ {
				if styledAfter[1+i] != styledBefore[1+i] {
					t.Fatalf("%s the in-window tool call moved content row %d, above the call:\nbefore %q\nafter  %q",
						phase, i+1, styledBefore[1+i], styledAfter[1+i])
				}
			}
			if n1, ok := footnote(t, styledAfter); !ok || n1 != wantBack {
				t.Fatalf("%s: `↓ N newer` = %d (ok=%v), want %d", phase, n1, ok, wantBack)
			}
			if top1, ok := noteNumber(t, 0, styledAfter[0], "↑ "); !ok || top1 != top0 {
				t.Fatalf("%s: `↑ N earlier` = %d (ok=%v), want the unchanged %d", phase, top1, ok, top0)
			}
		}
		// Expanding the unresolved call adds two lines (args + `…`) that the
		// accounting absorbs into back; collapsing gives them back.
		press("expanding", total+2, n0+2)
		press("collapsing", total, n0)
	})
}

// scroll_test.go covers the transcript's tail-follow scrollback (W5 F2/C36,
// s2-screens.md T08 "Scroll"): `back` is the number of conversation lines
// hidden BELOW the panel, 0 means following the tail, and the six shell
// scroll bindings plus the wheel move it. Every assertion is on what the
// pane renders — the notes on the panel borders and the visible rows — the
// way the user actually sees a scroll.
package ask

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// scrollW/scrollH is the pane size every scroll test renders at. It is a
// pane size, not a terminal size: the shell would pass terminal h-2. At
// 80×22 the Transcript panel is 19 rows tall, so its inner height — the
// number of conversation lines a full window shows — is 17.
const (
	scrollW = 80
	scrollH = 22
	// scrollInner is the Transcript panel's inner height at scrollW×scrollH
	// (panel height h-3, minus the two borders).
	scrollInner = scrollH - 3 - 2
)

// newScrollModel builds an ask pane on the fixture vault with a FakeAgent
// and runs n scripted turns through it — each turn one typed question, one
// one-line answer, one done boundary — so the transcript overflows the
// panel. The pane is rendered once, the way the shell renders every frame,
// so the scroll math sees the size the window really has.
func newScrollModel(t *testing.T, n int) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "scroll-vault")
	ag := &uitest.FakeAgent{Events: scrollTurnEvents(n), Store: agent.NewFileSessions(v.Root)}
	m := New(uitest.Deps(v, true, ag)).(*Model)
	for i := 1; i <= n; i++ {
		m = runScriptedTurn(t, m, fmt.Sprintf("question %d?", i))
	}
	_, _ = uitest.PaneScreen(m, scrollW, scrollH)
	return m
}

// runScriptedTurn types q and submits it, draining every command the turn
// produces (the FakeAgent replays synchronously) — the same drive
// TestAskGolden uses.
func runScriptedTurn(t *testing.T, m *Model, q string) *Model {
	t.Helper()
	var pane ui.Pane = m
	for _, r := range q {
		var cmd tea.Cmd
		pane, cmd = pane.Update(keyPress(r))
		if cmd != nil {
			t.Fatalf("typing %q produced a command", string(r))
		}
	}
	pane, cmd := pane.Update(specialKey(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	return runCmd(t, pane, cmd, new([]tea.Msg)).(*Model)
}

// scrollTurnEvents is one turn's event script: a one-line answer and the
// done boundary. The questions come from the typing, not the events.
func scrollTurnEvents(n int) []agent.Event {
	evs := make([]agent.Event, 0, 2*n)
	for i := 1; i <= n; i++ {
		evs = append(evs,
			agent.TextDelta{Text: fmt.Sprintf("answer %d", i)},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)
	}
	return evs
}

// renderPane renders the pane at the test size and returns its styled and
// plain rows (failing if the grid invariant broke). Row 0 is the
// Transcript panel's top border; rows 1..scrollInner its content; row
// scrollInner+1 its bottom border; the last three rows are the Message
// panel.
func renderPane(t *testing.T, m *Model) (styled, plain []string) {
	t.Helper()
	styledText, plainText := uitest.PaneScreen(m, scrollW, scrollH)
	styled, plain = strings.Split(styledText, "\n"), strings.Split(plainText, "\n")
	if len(styled) != scrollH || len(plain) != scrollH {
		t.Fatalf("pane rendered %d/%d rows, want %d", len(styled), len(plain), scrollH)
	}
	return styled, plain
}

// contentRow strips one plain panel row down to its content: the two
// border cells and the gutter column off, trailing padding trimmed. Only
// meaningful for rows without a cursor gutter (no selection is live in
// these tests).
func contentRow(plainRow string) string {
	return strings.TrimRight(strings.TrimPrefix(strings.TrimSuffix(plainRow, " │"), "│ "), " ")
}

// transcriptContent is the transcript's scrollInner plain content rows.
func transcriptContent(plain []string) []string {
	rows := make([]string, scrollInner)
	for i := 0; i < scrollInner; i++ {
		rows[i] = contentRow(plain[1+i])
	}
	return rows
}

// footnote reads the `↓ N newer` count off the transcript's bottom border;
// ok is false when the border carries no footnote.
func footnote(t *testing.T, styled []string) (int, bool) {
	t.Helper()
	return noteNumber(t, scrollInner+1, styled[scrollInner+1], "↓ ")
}

// noteNumber reads the N out of a border note like `↑ 27 earlier` or
// `↓ 18 newer` on one rendered row; ok is false when the note is absent.
// The row is styled, so the digits are picked out past whatever SGR runs
// sit between the marker and the number.
func noteNumber(t *testing.T, row int, rowText, marker string) (int, bool) {
	t.Helper()
	i := strings.Index(rowText, marker)
	if i < 0 {
		return 0, false
	}
	rest := rowText[i+len(marker):]
	j := 0
	for j < len(rest) && !('0' <= rest[j] && rest[j] <= '9') {
		j++
	}
	k := j
	for k < len(rest) && '0' <= rest[k] && rest[k] <= '9' {
		k++
	}
	if j == k {
		t.Fatalf("row %d carries %q but no number after %q", row, rowText, marker)
	}
	n := 0
	for _, d := range rest[j:k] {
		n = n*10 + int(d-'0')
	}
	return n, true
}

// requireOverflow asserts the precondition every scrolled subtest leans on:
// the scripted conversation overflows the panel by more than a page, so a
// pgup neither pins the window at the top nor falls off it.
func requireOverflow(t *testing.T, m *Model) int {
	t.Helper()
	total := len(m.transcriptLines())
	if step := max(1, scrollInner-1); total <= scrollInner+step {
		t.Fatalf("test conversation is %d lines; want more than inner(%d) + a page step(%d) so pgup stays mid-transcript",
			total, scrollInner, step)
	}
	return total
}

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
}

// tailLine is content row i (0-based) of the tail-following window: the
// conversation's last inner lines, styles stripped, padding trimmed.
func tailLine(t *testing.T, m *Model, i int) string {
	t.Helper()
	lines := m.transcriptLines()
	return strings.TrimRight(ansi.Strip(lines[len(lines)-scrollInner+i]), " ")
}

// mustFootnote renders m and returns the `↓ N newer` count, failing when
// the pane is not scrolled up.
func mustFootnote(t *testing.T, m *Model) (int, bool) {
	t.Helper()
	styled, _ := renderPane(t, m)
	n, ok := footnote(t, styled)
	if !ok {
		t.Fatalf("the bottom border has no `↓ N newer`: %q", styled[scrollInner+1])
	}
	return n, true
}

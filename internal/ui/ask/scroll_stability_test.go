// scroll_stability_test.go holds the scrollback tests' render-and-measure
// plumbing — the pane renderer and the note readers — plus the scripted
// turn drive (newScrollModel, runScriptedTurn, scrollTurnEvents) and the
// tool-conversation builders the W5d/T34 window-stability subtests lean on.
// Split out of scroll_test.go so that file stays under the 400-line cap.
// No test lives here.
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

// newToolScrollModel builds an ask pane whose conversation is one
// tool-call turn high up followed by filler turns — enough lines that one
// pgup leaves the (expandable) tool call wholly above the window at
// 80×22. The events are applied directly, ask_test.go's style: these
// subtests lean on the geometry, not on the stream pump, and the pane is
// rendered once so the scroll math sees the size the window really has.
func newToolScrollModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "scroll-vault")
	m := New(uitest.Deps(v, true, nil)).(*Model)
	m.applyEvent(agent.TextDelta{Text: "answer one"})
	m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"kv cache"}`})
	m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
	applyFillerTurns(m, 1, fillerTurns)
	_, _ = uitest.PaneScreen(m, scrollW, scrollH)
	return m
}

// applyFillerTurns applies n filler turns — one assistant line and the
// done boundary each, numbered from `from` — the tool-scroll builders'
// padding, five rendered lines per turn.
func applyFillerTurns(m *Model, from, n int) {
	for i := from; i < from+n; i++ {
		m.applyEvent(agent.TextDelta{Text: fmt.Sprintf("filler %d", i)})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
	}
}

// newInsideToolScrollModel is newToolScrollModel's twin for the other half
// of the T34 contract: three filler turns ahead of the tool turn and four
// behind it, so one pgup leaves the pane scrolled up (back = the page
// step, 16) with the (expandable) call wholly INSIDE the window — rows
// pinned above its head, expansion room below it.
func newInsideToolScrollModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "scroll-vault")
	m := New(uitest.Deps(v, true, nil)).(*Model)
	applyFillerTurns(m, 1, 3)
	m.applyEvent(agent.TextDelta{Text: "answer one"})
	m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"kv cache"}`})
	m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
	applyFillerTurns(m, 4, 4)
	_, _ = uitest.PaneScreen(m, scrollW, scrollH)
	return m
}

// fillerTurns is newToolScrollModel's filler-turn count: five rendered
// lines per turn, on top of the tool turn's five, so the conversation
// overflows the panel by more than a page and a single pgup puts the tool
// call — three lines once expanded — above the window start.
const fillerTurns = 7

// toolHeadIndex returns the transcriptLines index of the conversation's
// one tool call's head row — the line starting with the faint `▸` marker.
// It fails the test when the scripted conversation has no tool call, or
// more than one.
func toolHeadIndex(t *testing.T, m *Model) int {
	t.Helper()
	found := -1
	for i, l := range m.transcriptLines() {
		if strings.HasPrefix(ansi.Strip(l), "▸ ") {
			if found >= 0 {
				t.Fatal("the scripted conversation has more than one tool call")
			}
			found = i
		}
	}
	if found < 0 {
		t.Fatal("the scripted conversation has no tool call")
	}
	return found
}

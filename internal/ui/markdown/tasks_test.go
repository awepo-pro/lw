package markdown

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// taskTwoItems is the R-508 regression case: two task items rendered as ONE
// merged line with the checkboxes gone (glamour routes a task item through
// its task element, so the item marker is never emitted, and the reflow
// then joins the marker-less lines).
const taskTwoItems = "- [ ] todo one\n- [x] done two"

// quoteInListSrc is the R-508 quote case: a blockquote inside a list item
// is one list block, so the reflow absorbed the bar line into the item's
// paragraph as literal text and the bar survived on the first line only.
const quoteInListSrc = "- outer item\n" +
	"  > quoted note that is long enough to wrap at thirty columns for sure"

// taskWrapSrc is a task item long enough to wrap at the widths below.
const taskWrapSrc = "- [ ] kneading works the dough hard and continuously, " +
	"building strength fast in a dough that has not yet rested"

// quoteInNestedSrc nests the quote under the INNER item of a nested list,
// so its lines must hang at the inner item's text column (4 cells).
const quoteInNestedSrc = "- outer item words alpha bravo charlie delta echo foxtrot golf hotel\n" +
	"  - inner item\n" +
	"    > quoted note that is long enough to wrap at thirty columns for sure"

func TestTasksAndQuotesInLists(t *testing.T) {
	t.Run("task_items_stay_separate_lines", func(t *testing.T) {
		lines := contentLines(renderFrag(t, taskTwoItems, 30, darkStyle, true))
		if len(lines) != 2 {
			t.Fatalf("got %d lines, want 2:\n%q", len(lines), lines)
		}
		if got := ansi.Strip(lines[0]); got != "[ ] todo one" {
			t.Fatalf("line 0 = %q, want %q", got, "[ ] todo one")
		}
		if got := ansi.Strip(lines[1]); got != "[x] done two" {
			t.Fatalf("line 1 = %q, want %q", got, "[x] done two")
		}
	})

	t.Run("ticked_and_unticked_differ", func(t *testing.T) {
		unt := contentLines(renderFrag(t, "- [ ] solo task", 40, darkStyle, true))
		tkd := contentLines(renderFrag(t, "- [x] solo task", 40, darkStyle, true))
		if len(unt) != 1 || len(tkd) != 1 {
			t.Fatalf("one-line renders expected, got %q and %q", unt, tkd)
		}
		if got := ansi.Strip(unt[0]); got != "[ ] solo task" {
			t.Fatalf("unticked = %q", got)
		}
		if got := ansi.Strip(tkd[0]); got != "[x] solo task" {
			t.Fatalf("ticked = %q", got)
		}
		// A task item whose TEXT begins with "[ ]": the leading checkbox is
		// the item's marker, the literal one stays text — words all kept.
		lines := contentLines(renderFrag(t, "- [ ] [ ] looks like a nested checkbox but is text", 60, darkStyle, true))
		if len(lines) != 1 || ansi.Strip(lines[0]) != "[ ] [ ] looks like a nested checkbox but is text" {
			t.Fatalf("literal-bracket item = %q", lines)
		}
	})

	t.Run("task_item_wraps_with_hang", func(t *testing.T) {
		// Derived from the same wrap the renderer uses: the checkbox takes
		// 4 cells, so the text budget at width 40 is 36.
		text := strings.Join(strings.Fields(strings.TrimPrefix(taskWrapSrc, "- [ ] ")), " ")
		var want []string
		for i, c := range strings.Split(lipgloss.Wrap(text, 36, ""), "\n") {
			if i == 0 {
				want = append(want, "[ ] "+c)
				continue
			}
			want = append(want, "    "+c)
		}
		lines := renderFrag(t, taskWrapSrc, 40, darkStyle, false)
		if len(lines) <= 1 {
			t.Fatalf("the case does not wrap: %q", lines)
		}
		for i, l := range lines {
			if got := ansi.Strip(l); got != want[i] {
				t.Fatalf("line %d = %q, want %q", i, got, want[i])
			}
		}
		// The checkbox carries the Accent SGR; no continuation line does.
		accent := ansi.Style{}.ForegroundColor(lipgloss.Color(darkStyle.Accent)).Styled("[ ]")
		if !strings.Contains(lines[0], accent) {
			t.Fatalf("line 0 carries no Accent-coloured checkbox: %q", lines[0])
		}
		for i, l := range lines[1:] {
			if strings.Contains(l, accent) {
				t.Fatalf("continuation %d carries the Accent SGR: %q", i+1, l)
			}
		}
	})

	t.Run("quote_inside_list_keeps_bar_on_every_line", func(t *testing.T) {
		lines := contentLines(renderFrag(t, quoteInListSrc, 30, darkStyle, true))
		if got := ansi.Strip(lines[0]); got != "• outer item" {
			t.Fatalf("line 0 = %q, want the item's own line %q", got, "• outer item")
		}
		// The quote is its own paragraph inside the item: hung under the
		// item's text column, the bar on every one of its lines, the text
		// re-wrapped at the room the indent and the bar leave (30 - 4).
		quote := quoteTextOf(quoteInListSrc)
		var want []string
		for _, c := range strings.Split(lipgloss.Wrap(quote, 26, ""), "\n") {
			want = append(want, "  │ "+c)
		}
		if len(lines) != 1+len(want) {
			t.Fatalf("got %d lines, want %d:\ngot  %q\nwant %q", len(lines), 1+len(want), lines, want)
		}
		for i, w := range want {
			if got := ansi.Strip(lines[i+1]); got != w {
				t.Fatalf("line %d = %q, want %q", i+1, got, w)
			}
		}

		// In a styled render every quote line's bar carries the Border
		// colour, exactly as a top-level quote's bars do — and the item's
		// own line never carries a bar at all.
		styled := contentLines(renderFrag(t, quoteInListSrc, 30, darkStyle, false))
		if strings.Contains(ansi.Strip(styled[0]), "│") {
			t.Fatalf("the item's line absorbed a bar: %q", ansi.Strip(styled[0]))
		}
		borderSGR := ansi.Style{}.ForegroundColor(lipgloss.Color(darkStyle.Border)).String()
		for i, l := range styled[1:] {
			if !strings.Contains(l, borderSGR+"│") {
				t.Fatalf("quote line %d carries no Border-coloured bar: %q", i+1, l)
			}
		}
	})

	t.Run("quote_inside_list_hangs_under_item", func(t *testing.T) {
		lines := contentLines(renderFrag(t, quoteInNestedSrc, 30, darkStyle, true))
		assertWordsKeptInList(t, quoteInNestedSrc, lines)
		// The quote hangs under the INNER item's text column: 2 (the
		// nested indent) + 1 marker + 1 separator, with the bar on every
		// line and the text budget at 30 - 4 - 2.
		quote := quoteTextOf(quoteInNestedSrc)
		var want []string
		for _, c := range strings.Split(lipgloss.Wrap(quote, 24, ""), "\n") {
			want = append(want, "    │ "+c)
		}
		found := -1
		for j := range lines {
			if strings.HasPrefix(ansi.Strip(lines[j]), "    │ ") {
				found = j
				break
			}
		}
		if found < 0 {
			t.Fatalf("no quote line hung at the inner item's text column:\n%q", lines)
		}
		if len(lines) < found+len(want) {
			t.Fatalf("got %d lines, want at least %d:\n%q", len(lines), found+len(want), lines)
		}
		for i, w := range want {
			if got := ansi.Strip(lines[found+i]); got != w {
				t.Fatalf("quote line %d = %q, want %q\nall: %q", i, got, w, lines)
			}
		}
		// The inner item itself and the outer item are intact above it.
		if !strings.HasPrefix(ansi.Strip(lines[0]), "• outer item words") {
			t.Fatalf("line 0 = %q, want the outer item", ansi.Strip(lines[0]))
		}
		var sawInner bool
		for _, l := range lines {
			if strings.HasPrefix(ansi.Strip(l), "  • inner item") {
				sawInner = true
			}
		}
		if !sawInner {
			t.Fatalf("no nested item line found:\n%q", lines)
		}
	})

	t.Run("comment_matches_the_escapes_glamour_emits", func(t *testing.T) {
		// The truth the inline.go comment must state: glamour emits OSC 8
		// hyperlink wrappers (BEL-terminated) around a real link's anchor
		// text and appended URL, and the re-wrap re-opens them on
		// continuation lines — every open closed on its own line, no text
		// moved against the unlimited render.
		src := "- see [text](https://example.com/a/long/path) for more words here to force a wrap around the width"
		r := NewRenderer()
		for _, width := range []int{40, 80} {
			lines, err := r.RenderFragment([]byte(src), Options{Width: width, Measure: width, Style: darkStyle})
			if err != nil {
				t.Fatalf("RenderFragment(width %d): %v", width, err)
			}
			total := 0
			for i, l := range lines {
				opens := strings.Count(l, "\x1b]8;")
				closes := strings.Count(l, "\x1b\\") + strings.Count(l, "\x07")
				if opens != closes {
					t.Fatalf("width %d line %d: %d OSC 8 opens, %d closes:\n%q", width, i, opens, closes, l)
				}
				total += opens
			}
			if total == 0 {
				t.Fatalf("width %d: no OSC 8 escapes at all — the comment's claim is stale", width)
			}
			wide, err := r.RenderFragment([]byte(src), Options{Width: 10000, Measure: 10000, Style: darkStyle})
			if err != nil {
				t.Fatalf("RenderFragment(width 10000): %v", err)
			}
			if have, want := flatQuoteText(lines), flatQuoteText(wide); have != want {
				t.Errorf("width %d: visible text changed against the unlimited render\n got %q\nwant %q", width, have, want)
			}
		}
	})

	t.Run("sweep_every_width", sweepTasksEveryWidth)
}

// quoteTextOf returns a list item's trailing nested quote as the one
// paragraph its source lines form: the ">" markers and their spaces
// dropped, the line breaks replaced by spaces.
func quoteTextOf(src string) string {
	var words []string
	for _, l := range strings.Split(src, "\n") {
		if i := strings.Index(l, ">"); i >= 0 {
			words = append(words, strings.Fields(l[i+1:])...)
		}
	}
	return strings.Join(words, " ")
}

// assertWordsKeptInList compares a list render's visible words (quote bars
// stripped per line) against the same source at an unlimited width.
func assertWordsKeptInList(t *testing.T, src string, lines []string) {
	t.Helper()
	wide := renderFrag(t, src, 10000, darkStyle, true)
	if have, want := flatQuoteText(lines), flatQuoteText(wide); have != want {
		t.Errorf("visible words changed against the unlimited render\n got %q\nwant %q", have, want)
	}
}

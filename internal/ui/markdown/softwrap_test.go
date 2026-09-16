package markdown

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// softQuoteSource is the R-507 regression case: a blockquote whose single
// paragraph is soft-wrapped across three source lines — the shape every
// T-I test corpus avoided. The paragraph's words must re-wrap as ONE
// paragraph with the bar on every line.
const softQuoteSource = "> alpha bravo charlie delta echo foxtrot golf\n" +
	"> hotel india juliet kilo lima mike november\n" +
	"> oscar papa quebec romeo sierra tango uniform"

// softListSource is the R-507 list case: one bullet item soft-wrapped
// across four source lines. Its words must join into ONE paragraph that
// re-wraps hung under the item's text column.
const softListSource = "- alpha bravo charlie delta echo foxtrot golf\n" +
	"  hotel india juliet kilo lima mike november\n" +
	"  oscar papa quebec romeo sierra tango uniform\n" +
	"  victor whiskey xray yankee zulu one two three\n" +
	"- second item"

func TestSoftWrappedSource(t *testing.T) {
	t.Run("quote_soft_breaks_keep_the_bar_on_every_line", func(t *testing.T) {
		lines := contentLines(renderFrag(t, softQuoteSource, 60, darkStyle, true))

		// The approved layout: the quote's whole text re-wrapped as one
		// paragraph at the text budget (60 minus the 2-cell bar), each line
		// carrying the bar — derived from the same wrap function the
		// renderer uses, not from the brief's illustrative shape.
		var want []string
		for _, c := range strings.Split(lipgloss.Wrap(softQuoteText(softQuoteSource), 58, ""), "\n") {
			want = append(want, "│ "+c)
		}
		if len(lines) != len(want) {
			t.Fatalf("got %d lines, want %d:\ngot  %q\nwant %q", len(lines), len(want), lines, want)
		}
		for i := range want {
			if got := ansi.Strip(lines[i]); got != want[i] {
				t.Fatalf("line %d = %q, want %q", i, got, want[i])
			}
			if n := ansi.StringWidth(lines[i]); n > 60 {
				t.Errorf("line %d is %d cells wide at width 60", i, n)
			}
		}

		// No word may move: the wrapped render's visible words equal the
		// same source rendered at an effectively unlimited width.
		wide := contentLines(renderFrag(t, softQuoteSource, 10000, darkStyle, true))
		if have, want := flatQuoteText(lines), flatQuoteText(wide); have != want {
			t.Errorf("visible words changed against the unlimited render\n got %q\nwant %q", have, want)
		}
	})

	t.Run("list_item_soft_breaks_join_and_hang", func(t *testing.T) {
		// The item's joined paragraph, re-wrapped at the text budget and
		// laid out with the marker on line 1 and a 2-cell hang after —
		// derived from lipgloss.Wrap, never hand-written.
		item := softListItemText(softListSource)
		for _, width := range []int{60, 100} {
			lines := contentLines(renderFrag(t, softListSource, width, darkStyle, true))
			var want []string
			for i, c := range strings.Split(lipgloss.Wrap(item, width-2, ""), "\n") {
				if i == 0 {
					want = append(want, "• "+c)
					continue
				}
				want = append(want, "  "+c)
			}
			want = append(want, "• second item")
			if len(lines) != len(want) {
				t.Fatalf("width %d: got %d lines, want %d:\ngot  %q\nwant %q", width, len(lines), len(want), lines, want)
			}
			for i := range want {
				if got := ansi.Strip(lines[i]); got != want[i] {
					t.Fatalf("width %d line %d = %q, want %q", width, i, got, want[i])
				}
			}
		}
	})

	t.Run("nested_item_soft_breaks_hang_under_its_text", func(t *testing.T) {
		src := "- outer item words alpha bravo charlie delta echo foxtrot golf hotel\n" +
			"  india juliet kilo lima mike november oscar papa quebec romeo sierra\n" +
			"  - inner item words one two three four five six seven eight nine\n" +
			"    ten eleven twelve thirteen fourteen fifteen sixteen seventeen\n" +
			"- second outer item"
		lines := contentLines(renderFrag(t, src, 60, darkStyle, true))
		assertWordsKept(t, src, lines, false)
		if n := checkListHangs(t, 60, lines); n == 0 {
			t.Fatalf("no continuation line to check — the case does not wrap: %q", lines)
		}
		inner := -1
		for j, l := range lines {
			if strings.HasPrefix(ansi.Strip(l), "  • ") {
				inner = j
				break
			}
		}
		if inner < 0 {
			t.Fatalf("no nested item line found: %q", lines)
		}
	})

	t.Run("lazy_continuation_hangs", func(t *testing.T) {
		src := "- item words alpha bravo charlie delta echo foxtrot golf hotel india\n" +
			"juliet kilo lima mike november oscar papa quebec romeo sierra tango\n" +
			"- second item"
		for _, width := range []int{60, 100} {
			lines := contentLines(renderFrag(t, src, width, darkStyle, true))
			assertWordsKept(t, src, lines, false)
			var want []string
			joined := softListItemText("- " + src[len("- "):])
			for i, c := range strings.Split(lipgloss.Wrap(joined, width-2, ""), "\n") {
				if i == 0 {
					want = append(want, "• "+c)
					continue
				}
				want = append(want, "  "+c)
			}
			want = append(want, "• second item")
			if len(lines) != len(want) {
				t.Fatalf("width %d: got %d lines, want %d:\ngot  %q\nwant %q", width, len(lines), len(want), lines, want)
			}
			for i := range want {
				if got := ansi.Strip(lines[i]); got != want[i] {
					t.Fatalf("width %d line %d = %q, want %q", width, i, got, want[i])
				}
			}
		}
	})

	t.Run("loose_list_paragraph_soft_breaks_hang", func(t *testing.T) {
		// A loose list: two items, each soft-wrapped, blank line between.
		// The blank is kept and each item's own continuation hangs under
		// that item's text.
		looseA := "- alpha bravo charlie delta echo foxtrot golf hotel india juliet\n" +
			"  kilo lima mike november oscar papa quebec romeo sierra tango uniform\n" +
			"\n" +
			"- victor whiskey xray yankee zulu one two three four five six seven\n" +
			"  eight nine ten eleven twelve thirteen fourteen fifteen sixteen"
		lines := renderFrag(t, looseA, 60, darkStyle, true)
		assertWordsKept(t, looseA, lines, false)
		blanks := 0
		for _, l := range lines {
			if strings.TrimSpace(ansi.Strip(l)) == "" {
				blanks++
			}
		}
		if blanks != 1 {
			t.Fatalf("got %d blank lines, want exactly 1 (the paragraph boundary kept):\n%q", blanks, lines)
		}
		if n := checkListHangs(t, 60, lines); n < 2 {
			t.Fatalf("expected continuations of both items, checked %d: %q", n, lines)
		}

		// One item whose second paragraph follows a blank line: the blank
		// is kept, the item's own paragraph re-wraps hung at 2, and the
		// second paragraph renders as its own reflowed paragraph — the
		// layout T-I's frozen loose_list_paragraph_keeps_indent pins.
		looseB := "- alpha bravo charlie delta echo foxtrot golf hotel india juliet\n" +
			"  kilo lima mike november oscar papa quebec romeo sierra tango uniform\n" +
			"\n" +
			"  victor whiskey xray yankee zulu one two three four five six seven\n" +
			"  eight nine ten eleven twelve thirteen fourteen fifteen sixteen"
		lines = renderFrag(t, looseB, 60, darkStyle, true)
		assertWordsKept(t, looseB, lines, false)
		blanks = 0
		boundary := -1
		for j, l := range lines {
			if strings.TrimSpace(ansi.Strip(l)) == "" {
				blanks++
				boundary = j
			}
		}
		if blanks != 1 {
			t.Fatalf("got %d blank lines, want exactly 1:\n%q", blanks, lines)
		}
		if n := checkListHangs(t, 60, lines[:boundary]); n == 0 {
			t.Fatalf("the item's own paragraph has no hung continuation: %q", lines)
		}
		for j, l := range lines[boundary+1:] {
			if s := ansi.Strip(l); hangOf(s) != 0 {
				t.Fatalf("second paragraph line %d hangs %d, want 0 (its own paragraph layout): %q", j, hangOf(s), s)
			}
		}
	})

	t.Run("nested_quote_keeps_both_bars", func(t *testing.T) {
		src := "> > alpha bravo charlie delta echo foxtrot golf hotel india juliet\n" +
			"> > kilo lima mike november oscar papa quebec romeo sierra tango uniform"
		lines := dropBareBarLines(contentLines(renderFrag(t, src, 60, darkStyle, true)))
		var want []string
		for _, c := range strings.Split(lipgloss.Wrap(softQuoteText(src), 56, ""), "\n") {
			want = append(want, "│ │ "+c)
		}
		if len(lines) != len(want) {
			t.Fatalf("got %d lines, want %d:\ngot  %q\nwant %q", len(lines), len(want), lines, want)
		}
		for i := range want {
			if got := ansi.Strip(lines[i]); got != want[i] {
				t.Fatalf("line %d = %q, want %q", i, got, want[i])
			}
		}
	})

	t.Run("quote_with_a_list_inside", func(t *testing.T) {
		src := "> - alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo\n" +
			">   lima mike november oscar papa quebec romeo sierra tango uniform victor\n" +
			"> - second item words here"
		lines := dropBareBarLines(contentLines(renderFrag(t, src, 60, darkStyle, true)))
		assertWordsKept(t, src, lines, true)
		for i, l := range lines {
			if s := ansi.Strip(l); !strings.HasPrefix(s, "│ ") {
				t.Fatalf("line %d lost the quote bar: %q", i, s)
			}
		}
		// The item's text joins across its soft break and re-wraps at the
		// room the bar and the item's own hang leave: 60 - 2 bars - 2 hang.
		item := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo " +
			"lima mike november oscar papa quebec romeo sierra tango uniform victor"
		var want []string
		for i, c := range strings.Split(lipgloss.Wrap(item, 56, ""), "\n") {
			if i == 0 {
				want = append(want, "│ • "+c)
				continue
			}
			want = append(want, "│   "+c)
		}
		want = append(want, "│ • second item words here")
		if len(lines) != len(want) {
			t.Fatalf("got %d lines, want %d:\ngot  %q\nwant %q", len(lines), len(want), lines, want)
		}
		for i := range want {
			if got := ansi.Strip(lines[i]); got != want[i] {
				t.Fatalf("line %d = %q, want %q", i, got, want[i])
			}
		}
	})

	t.Run("sweep_every_width", sweepEveryWidth)
}

// assertWordsKept compares a render's visible words against the same src
// at an effectively unlimited width.
func assertWordsKept(t *testing.T, src string, lines []string, quoted bool) {
	t.Helper()
	wide, err := NewRenderer().RenderFragment([]byte(src), Options{Width: 10000, Measure: 10000, Style: darkStyle, Plain: true})
	if err != nil {
		t.Fatalf("RenderFragment(width 10000): %v", err)
	}
	if have, want := sweepWords(lines, quot(quoted)), sweepWords(wide, quot(quoted)); have != want {
		t.Errorf("visible words changed against the unlimited render\n got %q\nwant %q", have, want)
	}
}

// itemHangOf returns the text column a stripped list marker line hangs its
// continuations at; ok is false when the line opens no marker.
func itemHangOf(stripped string) (int, bool) {
	ind := hangOf(stripped)
	s := stripped[ind:]
	if strings.HasPrefix(s, "•") {
		return ind + 2, true
	}
	d := 0
	for d < len(s) && s[d] >= '0' && s[d] <= '9' {
		d++
	}
	if d > 0 && d < len(s) && s[d] == '.' {
		return ind + d + 2, true
	}
	return 0, false
}

// checkListHangs walks a list render: every marker line sets the hang its
// item's continuations must carry, and every non-marker line after one
// must hang exactly there. Returns how many continuation lines it checked.
func checkListHangs(t *testing.T, w int, lines []string) int {
	t.Helper()
	hang, open, checked := 0, false, 0
	for i, l := range lines {
		s := ansi.Strip(l)
		if quotedLine(s) {
			s = stripQuoteBars(s)
		}
		if strings.TrimSpace(s) == "" {
			open = false
			continue
		}
		if h, ok := itemHangOf(s); ok {
			hang, open = h, true
			continue
		}
		if !open {
			continue // a standalone paragraph (the loose case's second block)
		}
		checked++
		if got := hangOf(s); got != hang || len(s) > hang && s[hang] == ' ' {
			t.Errorf("width %d line %d hangs %d, want %d: %q", w, i, got, hang, s)
		}
	}
	return checked
}

// quotedLine reports whether a stripped line opens with a quote bar.
func quotedLine(stripped string) bool {
	return strings.HasPrefix(stripped, "│")
}

// stripQuoteBars removes a stripped line's leading "│ " bar tokens.
func stripQuoteBars(s string) string {
	for strings.HasPrefix(s, "│") {
		s = strings.TrimPrefix(s[1:], " ")
	}
	return s
}

// dropBareBarLines removes lines that carry nothing but a bar (glamour's
// own margin line inside nested quotes): the layout tests derive the text
// lines only.
func dropBareBarLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if s := strings.TrimRight(ansi.Strip(l), " "); s == "│" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// softQuoteText joins a soft-wrapped blockquote source into the one
// paragraph CommonMark says it is: the ">" markers (both, for a nested
// quote) and their spaces dropped, the line breaks replaced by spaces.
func softQuoteText(src string) string {
	var words []string
	for _, l := range strings.Split(src, "\n") {
		words = append(words, strings.Fields(strings.TrimLeft(l, "> "))...)
	}
	return strings.Join(words, " ")
}

// softListItemText returns the first item of a soft-wrapped bullet source
// as the single paragraph its soft line breaks form: every line's marker
// and indent dropped, the breaks replaced by spaces.
func softListItemText(src string) string {
	var words []string
	for i, l := range strings.Split(src, "\n") {
		if i > 0 && strings.HasPrefix(l, "- ") {
			break // the next item: the first item's paragraph ends here
		}
		words = append(words, strings.Fields(strings.TrimPrefix(l, "- "))...)
	}
	return strings.Join(words, " ")
}

package markdown

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// renderFrag renders src as a fragment and fails t on error.
func renderFrag(t *testing.T, src string, width int, style Style, plain bool) []string {
	t.Helper()
	lines, err := NewRenderer().RenderFragment([]byte(src), Options{Width: width, Style: style, Plain: plain})
	if err != nil {
		t.Fatalf("RenderFragment(%q, width %d): %v", src, width, err)
	}
	return lines
}

// hangOf returns the number of leading spaces of a stripped line.
func hangOf(stripped string) int {
	n := 0
	for n < len(stripped) && stripped[n] == ' ' {
		n++
	}
	return n
}

// sgrKey is an order-insensitive identity for one SGR sequence: its
// parameters sorted and joined. The same style serializes differently
// depending on who emits it (glamour "38;2;...;1", the wrap writer's pen
// reopen "1;38;2;..."), so continuity checks compare keys, not bytes.
func sgrKey(seq string) string {
	p := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m")
	parts := strings.Split(p, ";")
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// contentLines drops blank lines.
func contentLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(ansi.Strip(l)) != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestHangingIndent(t *testing.T) {
	t.Run("bullet_continuation_aligns_with_text", func(t *testing.T) {
		lines := contentLines(renderFrag(t, "- alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima", 60, darkStyle, false))
		if got := ansi.Strip(lines[0]); !strings.HasPrefix(got, "• ") {
			t.Fatalf("first line = %q, want it to start with the bullet marker", got)
		}
		for i, l := range lines[1:] {
			got := ansi.Strip(l)
			if hang := hangOf(got); hang != 2 || len(got) > 2 && got[2] == ' ' {
				t.Fatalf("continuation %d hangs %d, want exactly 2 (the bullet's text column): %q", i, hang, got)
			}
		}
	})

	t.Run("ordered_multi_digit_aligns", func(t *testing.T) {
		src := ""
		for i := 1; i <= 9; i++ {
			src += fmt.Sprintf("%d. short item %d\n", i, i)
		}
		src += "10. the tenth item carries enough words that it certainly wraps around at this width yes\n"
		lines := contentLines(renderFrag(t, src, 50, darkStyle, false))
		i := -1
		for j, l := range lines {
			if strings.HasPrefix(ansi.Strip(l), "10. ") {
				i = j
				break
			}
		}
		if i < 0 {
			t.Fatalf("no rendered 10. marker found: %q", lines)
		}
		if i+1 >= len(lines) {
			t.Fatalf("the 10. item did not wrap: %q", lines)
		}
		got := ansi.Strip(lines[i+1])
		if hang := hangOf(got); hang != 4 || len(got) > 4 && got[4] == ' ' {
			t.Fatalf("the 10. item's continuation hangs %d, want exactly 4: %q", hang, got)
		}
	})

	t.Run("nested_item_aligns_under_its_own_text", func(t *testing.T) {
		src := "- outer item words alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo\n" +
			"  - inner item words one two three four five six seven eight nine ten eleven twelve\n"
		lines := contentLines(renderFrag(t, src, 55, darkStyle, false))
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
		if inner+1 >= len(lines) {
			t.Fatalf("the nested item did not wrap: %q", lines)
		}
		got := ansi.Strip(lines[inner+1])
		if hang := hangOf(got); hang != 4 || len(got) > 4 && got[4] == ' ' {
			t.Fatalf("nested continuation hangs %d, want exactly 4 (its own indent 2 + marker 2): %q", hang, got)
		}
		if outer := ansi.Strip(lines[inner-1]); hangOf(outer) != 2 {
			t.Errorf("the outer item's continuation hangs %d, want 2: %q", hangOf(outer), outer)
		}
	})

	t.Run("loose_list_paragraph_keeps_indent", func(t *testing.T) {
		src := "- item one with several words\n\n  a second paragraph of the same item with enough words to wrap around twice over here\n"
		lines := contentLines(renderFrag(t, src, 40, darkStyle, false))
		para := -1
		for j, l := range lines {
			if strings.HasPrefix(ansi.Strip(l), "a second paragraph") {
				para = j
				break
			}
		}
		if para < 0 {
			t.Fatalf("loose list paragraph not found: %q", lines)
		}
		if hang := hangOf(ansi.Strip(lines[para])); hang != 0 {
			t.Fatalf("the loose paragraph itself hangs %d, want its own indent 0: %q", hang, lines[para])
		}
		for j, l := range lines[para+1:] {
			got := ansi.Strip(l)
			if strings.HasPrefix(got, "• ") || strings.HasPrefix(got, "- ") {
				break // reached the next item
			}
			if hang := hangOf(got); hang != 0 {
				t.Fatalf("loose paragraph continuation %d hangs %d, want 0: %q", j, hang, got)
			}
		}
	})

	t.Run("hyphen_breaks_like_a_paragraph", func(t *testing.T) {
		text := "the anthropic-api-vs-vertex-ai token decides routing"
		for _, width := range []int{24, 30} {
			list := renderFrag(t, "- "+text, width, darkStyle, true)
			para := renderFrag(t, text, width-2, darkStyle, true) // the item text's available width
			if len(list) != len(para) {
				t.Fatalf("width %d: %d list lines vs %d paragraph lines:\nlist %q\npara %q", width, len(list), len(para), list, para)
			}
			for i := range list {
				lw, pw := ansi.Strip(list[i]), ansi.Strip(para[i])
				want := pw
				if i == 0 {
					want = "• " + pw
				} else {
					want = "  " + pw
				}
				if lw != want {
					t.Fatalf("width %d line %d: list %q, want %q (paragraph wrap)", width, i, lw, want)
				}
			}
		}
	})

	t.Run("colour_survives_the_rewrap", func(t *testing.T) {
		src := "- **alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo** then `code-one two three four five six` end"
		for _, style := range []Style{darkStyle, lightStyle} {
			// Width 50: the wrap falls inside the bold run; width 100:
			// inside the code span.
			for _, tc := range []struct {
				width       int
				crossing    string // a word inside the span, on line 1
				continuing  string // the span's first word on line 2
				spanOpening string // a word at the very start of the span
			}{
				{50, "golf", "hotel", "alpha"},
				{100, "four", "five", "code-one"},
			} {
				lines := contentLines(renderFrag(t, src, tc.width, style, false))
				if len(lines) < 2 {
					t.Fatalf("style %+v width %d: expected a wrap, got %q", style, tc.width, lines)
				}
				spanSGR := activeSGR(lines[0], tc.spanOpening)
				if spanSGR == "" {
					t.Fatalf("style %+v: the span's own SGR not found at %q: %q", style, tc.spanOpening, lines[0])
				}
				if got := activeSGR(lines[0], tc.crossing); sgrKey(got) != sgrKey(spanSGR) {
					t.Fatalf("style %+v width %d: the run open at %q is %q, want the span SGR %q", style, tc.width, tc.crossing, got, spanSGR)
				}
				if got := activeSGR(lines[1], tc.continuing); sgrKey(got) != sgrKey(spanSGR) {
					t.Fatalf("style %+v width %d: the continuation line's %q carries %q, want the span SGR %q reopened: %q", style, tc.width, tc.continuing, got, spanSGR, lines[1])
				}
				// The reopened run sits right after the hang spaces. Its
				// parameters may serialize in either order (glamour emits
				// "38;2;...;1", the wrap's pen reopens "1;38;2;...") — the
				// style, not the byte order, is the contract.
				if !strings.HasPrefix(lines[1], "  ") {
					t.Fatalf("style %+v width %d: continuation lost its hang: %q", style, tc.width, lines[1])
				}
				seq, _, _, ok := decodeSGR(lines[1][2:])
				if !ok || sgrKey(seq) != sgrKey(spanSGR) {
					t.Fatalf("style %+v width %d: continuation does not reopen the span SGR right after the hang: %q", style, tc.width, lines[1])
				}
			}
		}
	})

	t.Run("marker_keeps_accent", func(t *testing.T) {
		src := "- top item alpha bravo charlie delta echo foxtrot golf hotel\n" +
			"  - inner item one two three four five six seven eight nine ten\n" +
			"- second top item words alpha bravo charlie delta echo foxtrot golf\n"
		lines := contentLines(renderFrag(t, src, 45, darkStyle, false))
		accent := roleSGR(darkStyle.Accent, "")
		isMarker := func(stripped string) bool {
			s := stripped[strings.IndexFunc(stripped, func(r rune) bool { return r != ' ' }):]
			if strings.HasPrefix(s, "•") {
				return true
			}
			d := 0
			for d < len(s) && s[d] >= '0' && s[d] <= '9' {
				d++
			}
			return d > 0 && d < len(s) && s[d] == '.'
		}
		for i, l := range lines {
			stripped := ansi.Strip(l)
			if isMarker(stripped) {
				if !strings.Contains(l, accent) {
					t.Errorf("line %d marker is not Accent: %q", i, l)
				}
			} else if strings.Contains(l, accent) {
				t.Errorf("line %d is not a marker line yet carries the Accent SGR: %q", i, l)
			}
		}
	})

	t.Run("fragment_and_page_agree", func(t *testing.T) {
		src := "# Title\n\n- alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima\n\n```go\nfunc main() {}\n```\n\nTrailing paragraph words here for good measure.\n"
		frag := renderFrag(t, src, 98, darkStyle, true)                                                    // content width 98
		page, err := NewRenderer().Render([]byte(src), Options{Width: 100, Style: darkStyle, Plain: true}) // content width 98 too
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(frag) != len(page) {
			t.Fatalf("%d fragment lines vs %d page lines", len(frag), len(page))
		}
		for i := range page {
			got := strings.TrimRight(page[i], " ")
			if len(got) >= 2 {
				got = got[2:] // the page gutter
			}
			if want := strings.TrimRight(frag[i], " "); got != want {
				t.Fatalf("line %d: page body %q, fragment %q", i, got, want)
			}
		}
	})
}

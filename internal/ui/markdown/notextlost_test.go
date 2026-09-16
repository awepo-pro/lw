package markdown

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// flatVisible is a render's visible text with every whitespace rune
// removed: the C-508 property compares this between a wrapped render and
// the same source at an effectively unlimited width, so a moved word, a
// lost character or an invented one all show up as a difference no matter
// where the wrap points landed.
func flatVisible(lines []string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, ansi.Strip(strings.Join(lines, "\n")))
}

// maxVisibleWidth is the widest line in cells.
func maxVisibleWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		if n := ansi.StringWidth(l); n > w {
			w = n
		}
	}
	return w
}

// sweepEachWidth renders every word-prefix of a block at every sweep width
// and compares it against the same prefix rendered at an effectively
// unlimited width: same visible text (whitespace removed, per-line chrome
// dropped through flat when given) and no line wider than its width. Each
// prefix runs in its own goroutine — every render is independent and the
// render cache is mutex-guarded — and failures report in prefix order.
func sweepEachWidth(t *testing.T, build func(words int) string, total int, flat func([]string) string, check func(t *testing.T, w int, lines []string)) {
	t.Helper()
	if flat == nil {
		flat = flatVisible
	}
	r := NewRenderer()
	render := func(src string, width int) []string {
		t.Helper()
		lines, err := r.RenderFragment([]byte(src), Options{Width: width, Measure: width, Style: darkStyle})
		if err != nil {
			t.Errorf("RenderFragment(%q, width %d): %v", src, width, err)
			return nil
		}
		return lines
	}

	srcs := make([]string, total+1)
	for k := 1; k <= total; k++ {
		srcs[k] = build(k)
	}
	failures := make([]string, total+1)

	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for k := 1; k <= total; k++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(k int) {
			defer wg.Done()
			defer func() { <-sem }()
			src := srcs[k]
			want := flat(render(src, 10000)) // Measure=Width: effectively unlimited
			for w := 40; w <= 160; w++ {
				got := render(src, w)
				if got == nil {
					return
				}
				if n := maxVisibleWidth(got); n > w {
					failures[k] = fmt.Sprintf("prefix %d width %d: line of %d cells exceeds the width: %q", k, w, n, got)
					return
				}
				if have := flat(got); have != want {
					failures[k] = fmt.Sprintf("prefix %d width %d: visible text changed\n got %q\nwant %q\nsrc %q", k, w, have, want, src)
					return
				}
				if check != nil {
					// check only ever calls t.Errorf, which is safe from
					// multiple goroutines (never Fatalf).
					check(t, w, got)
				}
			}
		}(k)
	}
	wg.Wait()
	for k := 1; k <= total; k++ {
		if failures[k] != "" {
			t.Fatalf("%s", failures[k])
		}
	}
}

// buildMarked joins a marker ("- ", "> ", "") with the first n words of
// body and sweeps every prefix length.
func sweepMarked(t *testing.T, marker, body string, flat func([]string) string, check func(t *testing.T, w int, lines []string)) {
	t.Helper()
	words := strings.Fields(body)
	sweepEachWidth(t, func(n int) string {
		return marker + strings.Join(words[:n], " ")
	}, len(words), flat, check)
}

// flatQuoteText flattens quote lines with each line's leading bar tokens
// dropped first: a continuation line repeats its bar (that is the design),
// so the no-text-lost property compares the words, not the chrome.
func flatQuoteText(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(strings.TrimLeft(ansi.Strip(l), "│ "))
	}
	return flatVisible([]string{b.String()})
}

// c508Item is the measured C-508 case: at Width 100 the shipped renderer
// returned ONE line ending "(VPC-SC / PSC…" — the ")" and "." were gone.
const c508Item = "- Network — the public internet to api.anthropic.com, versus the private GCP backbone (VPC-SC / PSC)."

func TestNoTextLost(t *testing.T) {
	t.Run("c508_list_item_at_width_100", func(t *testing.T) {
		lines, err := NewRenderer().RenderFragment([]byte(c508Item), Options{Width: 100, Style: darkStyle})
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		if len(lines) != 2 {
			t.Fatalf("got %d lines, want 2:\n%q", len(lines), lines)
		}
		if got, want := ansi.Strip(lines[0]), "• Network — the public internet to api.anthropic.com, versus the private GCP backbone (VPC-SC /"; got != want {
			t.Errorf("line 1 = %q, want %q", got, want)
		}
		if got, want := ansi.Strip(lines[1]), "  PSC)."; got != want {
			t.Errorf("line 2 = %q, want %q (hang 2)", got, want)
		}
		if strings.Contains(flatVisible(lines), "…") {
			t.Errorf("an ellipsis clip dropped text: %q", lines)
		}
	})

	t.Run("lists_at_every_width", func(t *testing.T) {
		items := []string{
			"The container sees only `eth0` inside its own namespace.",
			c508Item[2:],
			"The **host adapter** must be rebuilt before the anthropic-api-vs-vertex-ai migration can land.",
			"See [[network-namespace-egress-isolation]] and [[veth-pairs|virtual ethernet pairs]] for background.",
			"Routing is decided per packet ^[raw/articles/netns-tutorial.md] then forwarded to the next hop gateway upstream.",
			"Wrap points land mid-token sometimes: anthropic-api-vs-vertex-ai plus a long-wiki-link-target-name here.",
		}
		for _, body := range items {
			sweepMarked(t, "- ", body, nil, nil)
		}
	})

	t.Run("ordered_and_nested_at_every_width", func(t *testing.T) {
		// A ten-item ordered list: only then does glamour's renumbering
		// produce a two-digit "10." marker, whose hang is 4.
		head := ""
		for i := 1; i <= 9; i++ {
			head += fmt.Sprintf("%d. ordered item number %d here\n", i, i)
		}
		tenth := []string{"the", "tenth", "item", "carries", "a", "anthropic-api-vs-vertex-ai", "token", "plus", "enough", "further", "words", "to", "wrap", "around"}
		sweepEachWidth(t, func(n int) string {
			return head + "10. " + strings.Join(tenth[:n], " ")
		}, len(tenth), nil, nil)

		nested := "- outer item words alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo\n" +
			"  - inner item words one two three four five six seven eight nine ten eleven twelve\n" +
			"    - deep item words "
		deep := strings.Fields("a b c d e f g h i j k l m n o p")
		sweepEachWidth(t, func(n int) string {
			return nested + strings.Join(deep[:n], " ")
		}, len(deep), nil, nil)
	})

	t.Run("quotes_at_every_width", func(t *testing.T) {
		bar := func(t *testing.T, w int, lines []string) {
			for i, l := range lines {
				if strings.TrimSpace(ansi.Strip(l)) == "" {
					continue
				}
				if !strings.HasPrefix(ansi.Strip(l), "│") {
					t.Errorf("quote line %d lost its bar: %q", i, l)
				}
			}
		}
		sweepMarked(t, "> ", "quoted prose with a hyphenated anthropic-api-vs-vertex-ai token and several further words", flatQuoteText, bar)
		sweepMarked(t, "> > ", "nested quote words alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike", flatQuoteText, bar)
	})

	t.Run("paragraphs_never_clipped", func(t *testing.T) {
		sweepMarked(t, "", "A plain paragraph with several words including a hyphenated anthropic-api-vs-vertex-ai token and `code spans` too.", nil, nil)
	})

	t.Run("unbreakable_token_may_clip", func(t *testing.T) {
		src := "- " + strings.Repeat("x", 60)
		lines, err := NewRenderer().RenderFragment([]byte(src), Options{Width: 40, Style: darkStyle})
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		if n := maxVisibleWidth(lines); n > 40 {
			t.Fatalf("a line is %d cells wide at width 40: %q", n, lines)
		}
		if have, want := flatVisible(lines), "•"+strings.Repeat("x", 60); have != want {
			t.Errorf("visible text = %q, want the 60 x's intact (%d chars)", have, len(want))
		}
	})
}

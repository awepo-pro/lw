package markdown

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// taskHangWalk walks a list render whose items may be bullets, ordered
// items or task items: every marker line sets the hang its continuations
// must carry, every other non-blank line must hang exactly there (a quote
// line hangs at the item's text column — the indent before its bars).
func taskHangWalk(t *testing.T, w int, lines []string) {
	t.Helper()
	hang, open := 0, false
	for i, l := range lines {
		s := ansi.Strip(l)
		if strings.TrimSpace(s) == "" {
			open = false
			continue
		}
		ind := hangOf(s)
		rest := s[ind:]
		switch {
		case strings.HasPrefix(rest, "•"):
			hang, open = ind+2, true
			continue
		case strings.HasPrefix(rest, "[ ]"), strings.HasPrefix(rest, "[x]"):
			hang, open = ind+4, true
			continue
		}
		if d := numPrefixLen(rest); d > 0 && d < len(rest) && rest[d] == '.' {
			hang, open = ind+d+2, true
			continue
		}
		if !open {
			continue
		}
		if got := hangOf(s); got != hang || len(s) > hang && s[hang] == ' ' {
			t.Errorf("width %d line %d hangs %d, want %d: %q", w, i, got, hang, s)
		}
	}
}

// numPrefixLen returns the length of s's leading digits.
func numPrefixLen(s string) int {
	d := 0
	for d < len(s) && s[d] >= '0' && s[d] <= '9' {
		d++
	}
	return d
}

// sweepTasksEveryWidth sweeps a corpus of task lists (ticked, unticked,
// nested under a bullet, mixed with plain items, soft-wrapped across
// source lines, one whose text opens with a literal "[ ]") and lists
// containing quotes (single-line, soft-wrapped, and with the item's text
// resuming after the quote) over every width 40…160: visible words equal
// the width-10000 render, no line exceeds its width, every task item
// begins a line with its checkbox, every quote line inside a list carries
// exactly its depth's bars, and every continuation hangs exactly at its
// item's text column. Each entry must wrap at width 40, so no check
// passes vacuously.
func sweepTasksEveryWidth(t *testing.T) {
	head := ""
	for i := 1; i <= 9; i++ {
		head += fmt.Sprintf("%d. ordered item number %d\n", i, i)
	}
	corpus := []struct {
		name     string
		src      string
		tasks    int // task items in the source
		quotes   bool
		quoteDep int
	}{
		{
			name: "ticked and unticked, soft-wrapped",
			src: "- [ ] kneading works the dough **hard and continuously**, building strength\n" +
				"  fast in a dough that has not yet rested — see [[windowpane-test]] for\n" +
				"  the shared finish line.^[raw/articles/loaf.md]\n" +
				"- [x] folding turns the dough gently instead",
			tasks: 2,
		},
		{
			name: "task nested under a bullet",
			src: "- outer bullets about proofing schedules and temperatures worth writing down\n" +
				"  - [ ] nested task with quite a lot of words to wrap around every width here\n" +
				"    continued softly across source lines too\n" +
				"- [x] ticked sibling item with enough further words to wrap around too",
			tasks: 2,
		},
		{
			name: "mixed task and plain items",
			src: "- [ ] task item one with enough words to wrap at the smallest width here\n" +
				"- plain bullet item with enough words to wrap at the smallest width too\n" +
				"- [x] task item two with further words so the line wraps around at small\n" +
				"  widths and stays joined across its soft break\n" +
				"- last plain item",
			tasks: 2,
		},
		{
			name:  "ordered then task",
			src:   head + "10. [ ] a task inside an ordered list with enough words to wrap around at the sweep widths here",
			tasks: 1,
		},
		{
			name: "literal checkbox in text",
			src: "- [ ] [ ] looks like a nested checkbox but is plain text with enough words\n" +
				"  to wrap around at every sweep width in this run",
			tasks: 1,
		},
		{
			name: "quote inside a list",
			src: "- outer item\n" +
				"  > quoted note that is long enough to wrap at thirty columns for sure\n" +
				"- following item",
			tasks: 0, quotes: true, quoteDep: 1,
		},
		{
			name: "quote inside a task item",
			src: "- [ ] task item holding a note\n" +
				"  > quoted note that is long enough to wrap at thirty columns for sure\n" +
				"- following item",
			tasks: 1, quotes: true, quoteDep: 1,
		},
		{
			name: "lazy line after the quote joins the quote paragraph",
			src: "- outer item\n" +
				"  > quoted note that is long enough to wrap at thirty columns for sure\n" +
				"  resumed item text after the quote with enough words to wrap around too\n" +
				"- next item",
			tasks: 0, quotes: true, quoteDep: 1,
		},
		{
			name: "quote soft-wrapped inside a nested item",
			src: "- outer item words alpha bravo charlie delta echo foxtrot golf hotel\n" +
				"  - inner item\n" +
				"    > quoted note that is long enough to wrap at thirty columns\n" +
				"    > for sure and then some more words to push the wrap further",
			tasks: 0, quotes: true, quoteDep: 1,
		},
	}

	r := NewRenderer()
	render := func(src string, width int) []string {
		t.Helper()
		lines, err := r.RenderFragment([]byte(src), Options{Width: width, Measure: width, Style: darkStyle})
		if err != nil {
			t.Errorf("RenderFragment(width %d): %v", width, err)
			return nil
		}
		return lines
	}

	failures := make([]string, len(corpus))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for k, tc := range corpus {
		wg.Add(1)
		sem <- struct{}{}
		go func(k int, tc struct {
			name     string
			src      string
			tasks    int
			quotes   bool
			quoteDep int
		}) {
			defer wg.Done()
			defer func() { <-sem }()
			wide := render(tc.src, 10000)
			if wide == nil {
				return
			}
			at40 := 0
			for w := 40; w <= 160; w++ {
				got := render(tc.src, w)
				if got == nil {
					return
				}
				if n := maxVisibleWidth(got); n > w {
					failures[k] = fmt.Sprintf("%s width %d: line of %d cells exceeds the width", tc.name, w, n)
					return
				}
				if have, want := flatQuoteText(got), flatQuoteText(wide); have != want {
					failures[k] = fmt.Sprintf("%s width %d: visible words changed\n got %q\nwant %q", tc.name, w, have, want)
					return
				}
				boxes := 0
				for _, l := range got {
					s := strings.TrimLeft(ansi.Strip(l), " ")
					switch {
					case strings.HasPrefix(s, "[ ]"), strings.HasPrefix(s, "[x]"):
						boxes++
					}
				}
				if boxes != tc.tasks {
					failures[k] = fmt.Sprintf("%s width %d: %d line-start checkboxes, want %d:\n%q", tc.name, w, boxes, tc.tasks, got)
					return
				}
				quoteLines := 0
				for _, l := range got {
					s := strings.TrimLeft(ansi.Strip(l), " ")
					if !strings.HasPrefix(s, "│") {
						if strings.Contains(s, "│") {
							failures[k] = fmt.Sprintf("%s width %d: a bar leaked into non-quote text: %q", tc.name, w, s)
							return
						}
						continue
					}
					quoteLines++
					bars, t := 0, s
					for strings.HasPrefix(t, "│") {
						bars++
						t = strings.TrimPrefix(t[1:], " ")
					}
					if bars != tc.quoteDep {
						failures[k] = fmt.Sprintf("%s width %d: quote line carries %d bars, want %d: %q", tc.name, w, bars, tc.quoteDep, s)
						return
					}
				}
				if tc.quotes && quoteLines == 0 {
					failures[k] = fmt.Sprintf("%s width %d: no quote line carried a bar:\n%q", tc.name, w, got)
					return
				}
				taskHangWalk(t, w, got)
				if w == 40 {
					at40 = len(got)
					if at40 < 2 {
						failures[k] = fmt.Sprintf("%s: a one-line render at width 40 — the sweep would pass vacuously", tc.name)
						return
					}
				}
				if w == 160 && len(got) >= at40 {
					failures[k] = fmt.Sprintf("%s: %d lines at width 40 vs %d at 160 — nothing wraps", tc.name, at40, len(got))
					return
				}
			}
		}(k, tc)
	}
	wg.Wait()
	for k := range corpus {
		if failures[k] != "" {
			t.Fatalf("%s", failures[k])
		}
	}
}

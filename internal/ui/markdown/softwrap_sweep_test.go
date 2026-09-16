package markdown

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// quot marks a sweep corpus entry whose rendered lines lead with quote
// bars (the word comparison strips them first).
type quot bool

// sweepWords flattens a render's visible words, dropping quote bars per
// line when the source is quoted.
func sweepWords(lines []string, quoted quot) string {
	if quoted {
		return flatQuoteText(lines)
	}
	return flatVisible(lines)
}

// sweepEveryWidth sweeps a corpus of MULTI-LINE sources — soft-wrapped
// bullet, ordered (with a 10. item), nested, lazy, loose, quote, nested
// quote and a quote containing a list, each with realistic tokens — over
// every width 40…160: visible words equal the width-10000 render (quote
// bars stripped per line), no line exceeds its width, every quote line
// keeps its bar, and every list continuation hangs exactly at its item's
// text column. The width-40 render must wrap (more lines than at 160), so
// no check passes vacuously.
func sweepEveryWidth(t *testing.T) {
	head := ""
	for i := 1; i <= 9; i++ {
		head += fmt.Sprintf("%d. ordered item number %d\n", i, i)
	}
	corpus := []struct {
		name   string
		src    string
		quoted bool // lines lead with quote bars; strip them before comparing
	}{
		{
			name: "bullet",
			src: "- kneading works the dough **hard and continuously**, building strength\n" +
				"  fast in a dough that has not yet rested — see [[windowpane-test]] for\n" +
				"  the shared finish line.^[raw/articles/loaf.md]\n" +
				"- folding turns the dough gently instead",
		},
		{
			name: "ordered",
			src: head +
				"10. the tenth item carries an anthropic-api-vs-vertex-ai token plus\n" +
				"    enough further words to wrap around at every sweep width here",
		},
		{
			name: "nested",
			src: "- outer item words alpha bravo charlie delta echo foxtrot golf hotel\n" +
				"  india juliet kilo lima mike november oscar papa quebec romeo sierra\n" +
				"  - inner item words one two three four five six seven eight nine ten\n" +
				"    eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen",
		},
		{
			name: "lazy",
			src: "- lazy item words with a `code span` and an em dash — plus the\n" +
				"anthropic-api-vs-vertex-ai token and several further words to wrap\n" +
				"- second item",
		},
		{
			name: "loose",
			src: "- alpha bravo charlie delta echo foxtrot golf hotel india juliet\n" +
				"  kilo lima mike november oscar papa quebec romeo sierra tango uniform\n" +
				"\n" +
				"- victor whiskey xray yankee zulu one two three four five six seven\n" +
				"  eight nine ten eleven twelve thirteen fourteen fifteen sixteen",
		},
		{
			name: "quote",
			src: "> quoted prose with a `code span` and an em dash — plus a\n" +
				"> hyphenated anthropic-api-vs-vertex-ai token and several\n" +
				"> further words to force wraps at nearly every sweep width",
			quoted: true,
		},
		{
			name: "nested quote",
			src: "> > nested quote words alpha bravo charlie delta echo foxtrot golf\n" +
				"> > hotel india juliet kilo lima mike november oscar papa quebec",
			quoted: true,
		},
		{
			name: "quote with a list",
			src: "> - quoted item words alpha bravo charlie delta echo foxtrot golf hotel\n" +
				">   india juliet kilo lima mike november oscar papa quebec romeo sierra\n" +
				"> - second quoted item with a [[wikilink]] and ^[raw/notes/x.md] marker",
			quoted: true,
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
			name   string
			src    string
			quoted bool
		}) {
			defer wg.Done()
			defer func() { <-sem }()
			wide := render(tc.src, 10000)
			if wide == nil {
				return
			}
			wideLines := 0
			for w := 40; w <= 160; w++ {
				got := render(tc.src, w)
				if got == nil {
					return
				}
				if n := maxVisibleWidth(got); n > w {
					failures[k] = fmt.Sprintf("%s width %d: line of %d cells exceeds the width", tc.name, w, n)
					return
				}
				if have, want := sweepWords(got, quot(tc.quoted)), sweepWords(wide, quot(tc.quoted)); have != want {
					failures[k] = fmt.Sprintf("%s width %d: visible words changed\n got %q\nwant %q", tc.name, w, have, want)
					return
				}
				if tc.quoted {
					for i, l := range got {
						if s := ansi.Strip(l); strings.TrimSpace(s) != "" && !strings.HasPrefix(s, "│") {
							failures[k] = fmt.Sprintf("%s width %d line %d lost its bar: %q", tc.name, w, i, s)
							return
						}
					}
				}
				checkListHangs(t, w, got)
				if w == 40 {
					wideLines = len(got)
					if wideLines < 2 {
						failures[k] = fmt.Sprintf("%s: a one-line render at width 40 — the sweep would pass vacuously", tc.name)
						return
					}
				}
				if w == 160 && len(got) >= wideLines {
					failures[k] = fmt.Sprintf("%s: %d lines at width 40 vs %d at 160 — nothing wraps", tc.name, wideLines, len(got))
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

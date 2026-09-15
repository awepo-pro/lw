// text.go implements contract §5's cell-based text primitives — Clip, Pad
// and Wrap — the vocabulary every panel and the frame itself measure and cut
// text with (00-conventions.md §4.1: cells, not bytes; §4.5: word-wrap is
// ui.Wrap). Clip and Pad never import an ANSI package directly: they lean on
// lipgloss.Style.MaxWidth, whose own truncation machinery is already
// SGR-aware, so an escape sequence is never split. Wrap operates on plain
// text one rune at a time, the same way mockgen.wrap operates on mockgen's
// per-character cell list — a direct port, not a cell-width approximation.
package ui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// cellLen returns s's visible width in terminal cells, ANSI-aware.
func cellLen(s string) int {
	return lipgloss.Width(s)
}

// Clip returns s cut to at most w cells, with "…" in the last cell when cut
// (mockgen.clip). Styled input is clipped by visible cells, preserving SGR
// sequences: lipgloss.Style.MaxWidth truncates through its own
// escape-aware machinery, so the cut never lands inside an escape sequence.
func Clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if cellLen(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return lipgloss.NewStyle().MaxWidth(w-1).Render(s) + "…"
}

// Pad returns s padded with spaces to exactly w cells, clipping first if
// wider.
func Pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = Clip(s, w)
	if cur := cellLen(s); cur < w {
		s += strings.Repeat(" ", w-cur)
	}
	return s
}

// Wrap greedy-wraps plain text into lines of at most w cells; continuation
// lines get hang spaces (mockgen.wrap). Words longer than the line are
// hard-split. It is a direct, rune-by-rune port of mockgen.wrap, which
// treats every character as one cell — so this does too, rather than
// measuring display width, matching the reference exactly instead of only
// approximating it.
func Wrap(s string, w, hang int) []string {
	var words [][]rune
	var word []rune
	for _, r := range s {
		if r == ' ' {
			if len(word) > 0 {
				words = append(words, word)
				word = nil
			}
			continue
		}
		word = append(word, r)
	}
	if len(word) > 0 {
		words = append(words, word)
	}

	var lines [][]rune
	var line []rune

	cap := func() int {
		c := w
		if len(lines) > 0 {
			c -= hang
		}
		if c < 1 {
			c = 1
		}
		return c
	}

	for _, word := range words {
		sep := 0
		if len(line) > 0 {
			sep = 1
		}
		if len(line)+sep+len(word) <= cap() {
			if len(line) > 0 {
				line = append(line, ' ')
			}
			line = append(line, word...)
			continue
		}
		if len(line) > 0 {
			lines = append(lines, line)
			line = nil
		}
		for len(word) > cap() {
			n := cap()
			lines = append(lines, word[:n])
			word = word[n:]
		}
		line = word
	}
	if len(line) > 0 || len(lines) == 0 {
		lines = append(lines, line)
	}

	out := make([]string, len(lines))
	for i, l := range lines {
		if i == 0 {
			out[i] = string(l)
		} else {
			out[i] = strings.Repeat(" ", hang) + string(l)
		}
	}
	return out
}

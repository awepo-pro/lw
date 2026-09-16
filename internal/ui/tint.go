// tint.go is the cursor-tint machinery behind contract §5 frame note 9
// (correction C35): parsing escape sequences out of a pre-styled content
// line so the tint can survive the resets that content carries.
package ui

import (
	"image/color"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// tintSpan re-renders s — a run of whole cells, already padded to its
// final width — with bg forced onto every one of its cells.
//
// Wrapping pre-styled content in one Background(...).Render is what C35
// measured: the content's first styled run ends with its own reset
// ("\x1b[39m\x1b[49m"), and the 49 switches the tint off for every cell
// after it. So instead the background sequence is asserted before the
// first cell and re-asserted after every SGR sequence s contains — SGR is
// additive, so re-asserting a background never disturbs a foreground the
// same sequence set — and a final reset stops the tint at s's last cell,
// so the caller's right border stays untinted.
func tintSpan(bg color.Color, s string) string {
	if bg == nil {
		return s
	}
	seq := bgSequence(bg)
	if seq == "" {
		return s
	}

	var b strings.Builder
	b.WriteString(seq)
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			j := ansiSeqEnd(s, i)
			b.WriteString(s[i:j])
			if strings.HasSuffix(s[i:j], "m") {
				b.WriteString(seq)
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// bgSequence returns the SGR sequence that sets bg as the background —
// what lipgloss emits for it, so the sequence downsamples with the theme
// exactly as every other render does.
func bgSequence(bg color.Color) string {
	r := lipgloss.NewStyle().Background(bg).Render(" ")
	if i := strings.IndexByte(r, 'm'); i >= 0 {
		return r[:i+1]
	}
	return ""
}

// ansiSeqEnd returns the index just past the escape sequence starting at
// s[i] (which must be ESC). CSI sequences run to the first final byte in
// 0x40–0x7e; string-terminated sequences (OSC, DCS, SOS, PM, APC) run to
// BEL or ESC \; anything else is a two-byte escape. A sequence cut short
// by the end of the string ends at the end of the string.
func ansiSeqEnd(s string, i int) int {
	if i+1 >= len(s) {
		return i + 1
	}
	switch s[i+1] {
	case '[': // CSI: parameter and intermediate bytes, then one final byte
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		if j < len(s) {
			return j + 1
		}
		return len(s)
	case ']', 'P', 'X', '^', '_': // terminated by BEL or ESC \
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	default:
		return i + 2
	}
}

// sgrParams reports whether seq is an SGR sequence (CSI … "m") and returns
// its parameters, with each empty parameter normalized to "0" the way a
// terminal reads "\x1b[m" as a reset.
func sgrParams(seq string) ([]string, bool) {
	if !strings.HasPrefix(seq, "\x1b[") || !strings.HasSuffix(seq, "m") || len(seq) < 3 {
		return nil, false
	}
	middle := seq[2 : len(seq)-1]
	params := strings.Split(middle, ";")
	for i, p := range params {
		if p == "" {
			params[i] = "0"
		}
	}
	return params, true
}

package markdown

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// span is one sentinel-delimited run restyleSpans looks for.
type span struct {
	open, close string
	openSGR     string // "" means: leave the content exactly as glamour rendered it
}

// fgSGR returns the SGR "set" sequence (no reset) for hex, optionally
// underlined; "" if hex is empty (nothing to set — restyleSpans then passes
// the span's content through unchanged instead of wrapping it).
func fgSGR(hex string, underline bool) string {
	if hex == "" && !underline {
		return ""
	}
	st := ansi.Style{}
	if hex != "" {
		st = st.ForegroundColor(lipgloss.Color(hex))
	}
	if underline {
		st = st.Underline(true)
	}
	return st.String()
}

// restyleSpans finds provOpen/provClose and wikiOpen/wikiClose sentinel
// pairs insertMarkers left in rendered, word-wrapped block text, and
// replaces each with its own styled span, restoring whatever SGR state was
// active immediately before it (repair-1, Critical 1's root-cause fix: no
// carrier AST node, so nothing can collide with real markdown; this is the
// counterpart post-processing pass, the same pattern the review accepted
// for table borders in block.go).
//
// It is a single linear scan over the whole block's rendered output
// (before splitting into lines), with one piece of state beyond the
// output builder: which span, if any, is currently open. While inside a
// span:
//   - every SGR sequence glamour's own line-wrap re-styling emits (its
//     reset-then-reopen pair around a "\n", so each wrapped line is
//     independently self-styled) is swallowed rather than copied through,
//     so it can never override the span's own style mid-content
//     (repair-2, R1: without this, a span's continuation segment on a
//     later line silently reverted to the paragraph's ambient style).
//   - a literal "\n" gets its own reset-then-reopen pair *of the span's
//     style*, so every line the span occupies is independently correct —
//     including a gutter later prepended to that line in render.go, since
//     the gutter is plain text concatenated in front of a line that
//     already starts with its own valid SGR state.
//   - the close sentinel restores the SGR state that was active
//     immediately before the span opened (captured once, at open, in
//     restoreActive), regardless of how many lines the span spanned or
//     what glamour's own embedded resets did in between.
//
// ansiOptions never use OSC hyperlinks any more (repair-1), so every
// escape this pass can see is a plain SGR "\x1b[...m".
//
// Known cosmetic caveat, deliberately not special-cased: when a marker's
// content word-wraps INSIDE a padded table cell (the only wrap that inserts
// row padding and a column separator before the close sentinel), the span
// stays open across that padding and border, and glamour's per-cell SGRs
// inside the window are swallowed — the neighbouring cell's text can lose
// its Fg until the border recolour's reset. Plain output is byte-correct;
// the paragraph/list/headings cases — every wrap restyleSpans exists for —
// are unaffected, and distinguishing a wrap-reopen from another cell's
// style would be guesswork.
func restyleSpans(s string, spans []span) string {
	var b strings.Builder
	ambient := ""    // last SGR "set" sequence seen outside any span
	var active *span // the span currently open, if any
	var restoreActive string

	i := 0
	for i < len(s) {
		if seq, n, isReset, ok := decodeSGR(s[i:]); ok {
			if active == nil {
				b.WriteString(seq)
			}
			if isReset {
				ambient = ""
			} else {
				ambient = seq
			}
			i += n
			continue
		}

		if active != nil {
			if active.close != "" && strings.HasPrefix(s[i:], active.close) {
				if active.openSGR != "" {
					b.WriteString(ansi.ResetStyle)
					b.WriteString(restoreActive)
				}
				i += len(active.close)
				active = nil
				continue
			}
			if s[i] == '\n' {
				if active.openSGR != "" {
					b.WriteString(ansi.ResetStyle)
				}
				b.WriteByte('\n')
				if active.openSGR != "" {
					b.WriteString(active.openSGR)
				}
				i++
				continue
			}
			r, size := utf8.DecodeRuneInString(s[i:])
			b.WriteRune(r)
			i += size
			continue
		}

		if sp, ok := openingSpan(s[i:], spans); ok {
			active = sp
			restoreActive = ambient
			if sp.openSGR != "" {
				b.WriteString(sp.openSGR)
			}
			i += len(sp.open)
			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// openingSpan returns the span whose open sentinel starts s, if any.
func openingSpan(s string, spans []span) (*span, bool) {
	for i := range spans {
		if spans[i].open != "" && strings.HasPrefix(s, spans[i].open) {
			return &spans[i], true
		}
	}
	return nil, false
}

// decodeSGR reports whether s starts with a SGR escape sequence
// ("\x1b[...m"), returning the sequence itself, its byte length, and
// whether it is a reset (bare "\x1b[m" / "\x1b[0m").
func decodeSGR(s string) (seq string, n int, isReset bool, ok bool) {
	if len(s) < 3 || s[0] != 0x1b || s[1] != '[' {
		return "", 0, false, false
	}
	j := 2
	for j < len(s) && s[j] != 'm' {
		j++
	}
	if j >= len(s) {
		return "", 0, false, false
	}
	seq = s[:j+1]
	isReset = seq == "\x1b[m" || seq == "\x1b[0m"
	return seq, j + 1, isReset, true
}

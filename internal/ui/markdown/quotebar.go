package markdown

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// colorizeQuoteBar recolours a rendered blockquote's "│" indent bars to
// borderHex (contract §2 note 2). glamour draws the outer indent token
// with the parent (document) style, but each deeper nesting level with
// that level's own BlockQuote primitive — so on a nested quote the inner
// bar arrives Muted+italic while the outer bar is Border: two-tone. Every
// indent bar at the start of the line is therefore recoloured Border, so
// nested bars match, and the quote text after the bars keeps exactly the
// styling glamour gave it.
//
// The bytes decide what counts as a bar: one indent token "│ " — always a
// "│", a plain space, then an escape sequence (the token run's reset, or
// the next styling run). A "│" the quote's text opens with has prose
// after it, so that lookahead is what keeps quote text untouched.
func colorizeQuoteBar(lines []string, borderHex string) []string {
	if borderHex == "" {
		return lines
	}
	st := ansi.Style{}.ForegroundColor(lipgloss.Color(borderHex))
	out := make([]string, len(lines))
	for i, l := range lines {
		rest := l
		var b strings.Builder
		for {
			// A bar sits inside a styling run that ends right after the
			// token's space; collect the run first, then decide on the
			// leading "│".
			var sgrs string
			for {
				seq, n, _, ok := decodeSGR(rest)
				if !ok {
					break
				}
				sgrs += seq
				rest = rest[n:]
			}
			if !isIndentBar(rest) {
				// Quote text (or the end of the line): restore the
				// collected styling and hand the rest through verbatim.
				b.WriteString(sgrs)
				break
			}
			b.WriteString(st.Styled("│"))
			rest = rest[len("│"):]
			// Keep the indent token's separating space plain, then loop:
			// a nested bar's styling runs are collected at the top.
			if strings.HasPrefix(rest, " ") {
				b.WriteByte(' ')
				rest = rest[1:]
			}
		}
		b.WriteString(rest)
		out[i] = b.String()
	}
	return out
}

// isIndentBar reports whether rest opens with a "│" indent token — a bar
// is always "│ " with an escape sequence (or the line's end) after the
// space, never prose.
func isIndentBar(rest string) bool {
	if !strings.HasPrefix(rest, "│") {
		return false
	}
	tail := rest[len("│"):]
	if tail == "" {
		return true
	}
	if tail[0] != ' ' {
		return false
	}
	return len(tail) == 1 || tail[1] == '\x1b'
}

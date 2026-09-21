package markdown

import "strings"

// MathToUnicodeInline rewrites only inline $…$ math in src, through the
// same macro and glyph tables the page converter uses: a candidate converts
// only when it holds a recognized TeX construct, code spans are masked, and
// display $$…$$ regions pass through byte-identical.
//
// It exists because the ask screen's live answer tail renders through the
// ask package's own inline cell renderer, not through this package's
// glamour pipeline — the unexported mathToUnicode is out of its reach, so
// this is the one narrow function the seam exports.
func MathToUnicodeInline(src string) string {
	if !strings.Contains(src, "$") {
		return src
	}
	segs := splitCodeSpans(src)
	changed := false
	var b strings.Builder
	b.Grow(len(src))
	for _, seg := range segs {
		if seg.code {
			b.WriteString(seg.text)
			continue
		}
		out := mathScanInline(seg.text)
		if out != seg.text {
			changed = true
		}
		b.WriteString(out)
	}
	if !changed {
		return src
	}
	return b.String()
}

// mathScanInline is mathScan with the display branch demoted to verbatim
// passthrough: a $$…$$ region is copied out untouched — its interior never
// becomes an inline candidate — and only $…$ converts.
func mathScanInline(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && s[i+1] == '$': // escaped dollar
			b.WriteString(s[i : i+2])
			i += 2
		case c == '$' && i+1 < len(s) && s[i+1] == '$': // display $$…$$: passthrough
			rest := s[i+2:]
			end := strings.Index(rest, "$$")
			if end < 0 {
				b.WriteString(s[i : i+2])
				i += 2
				break
			}
			b.WriteString(s[i : i+2+end+2])
			i = i + 2 + end + 2
		case c == '$': // inline $…$
			if j := inlineMathClose(s, i); j >= 0 {
				content := s[i+1 : j]
				if hasTeXConstruct(content) {
					convertMathInto(&b, content)
				} else {
					b.WriteString(s[i : j+1])
				}
				i = j + 1
			} else {
				b.WriteByte(c)
				i++
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

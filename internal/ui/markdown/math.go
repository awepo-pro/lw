package markdown

import (
	"strings"
	"unicode/utf8"
)

// mathToUnicode rewrites TeX math — $…$, $$…$$, \(…\) and \[…\] — into
// unicode-markdown inside src. Everything outside math delimiters (and
// inside code spans) is byte-identical; a $-delimited candidate is only
// converted when its content holds at least one recognized TeX construct,
// so money and shell dollars pass through untouched.
func mathToUnicode(src string) string {
	if !strings.Contains(src, "$") &&
		!strings.Contains(src, "\\(") && !strings.Contains(src, "\\[") {
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
		out := mathScan(seg.text)
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

// mathSegment is one piece of a source line: either raw text that math
// scanning may look at, or a code span carried through verbatim.
type mathSegment struct {
	text string
	code bool
}

// splitCodeSpans cuts s into text and code-span segments. A span opens at a
// run of n backticks and closes at the next run of exactly n backticks; an
// unmatched run stays ordinary text.
func splitCodeSpans(s string) []mathSegment {
	var segs []mathSegment
	flush := func(start, end int) {
		if end > start {
			segs = append(segs, mathSegment{text: s[start:end]})
		}
	}
	i, textStart := 0, 0
	for i < len(s) {
		if s[i] != '`' {
			i++
			continue
		}
		n := 0
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		close := findCodeClose(s, i+n, n)
		if close < 0 {
			i += n
			continue
		}
		flush(textStart, i)
		segs = append(segs, mathSegment{text: s[i : close+n], code: true})
		i = close + n
		textStart = i
	}
	flush(textStart, len(s))
	return segs
}

// findCodeClose returns the index of a run of exactly n backticks at or
// after from, or -1.
func findCodeClose(s string, from, n int) int {
	for j := from; j < len(s); {
		if s[j] != '`' {
			j++
			continue
		}
		m := 0
		for j+m < len(s) && s[j+m] == '`' {
			m++
		}
		if m == n {
			return j
		}
		if m > n {
			return -1
		}
		j += m
	}
	return -1
}

// mathScan walks one code-span-free segment, replacing each delimited math
// candidate with its conversion and copying everything else verbatim.
func mathScan(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && s[i+1] == '$': // escaped dollar
			b.WriteString(s[i : i+2])
			i += 2
		case c == '$' && i+1 < len(s) && s[i+1] == '$': // display $$
			rest := s[i+2:]
			end := strings.Index(rest, "$$")
			if end < 0 {
				b.WriteString(s[i : i+2])
				i += 2
				break
			}
			content := rest[:end]
			if hasTeXConstruct(content) {
				convertMathInto(&b, content)
			} else {
				b.WriteString(s[i : i+2+end+2])
			}
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
		case strings.HasPrefix(s[i:], "\\("): // inline alias
			end := strings.Index(s[i+2:], "\\)")
			if end < 0 {
				b.WriteString(s[i : i+2])
				i += 2
				break
			}
			convertMathInto(&b, s[i+2:i+2+end])
			i = i + 2 + end + 2
		case strings.HasPrefix(s[i:], "\\["): // display alias
			end := strings.Index(s[i+2:], "\\]")
			if end < 0 {
				b.WriteString(s[i : i+2])
				i += 2
				break
			}
			convertMathInto(&b, s[i+2:i+2+end])
			i = i + 2 + end + 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// inlineMathClose finds the closing $ for the candidate opening at open:
// the opener must be followed by a non-space rune, the closer must be
// preceded by a non-space rune, and no $ may appear inside the content
// (\$ escapes). The first unescaped $ decides — an invalid one means no
// candidate. Returns the closer index or -1.
func inlineMathClose(s string, open int) int {
	if open+1 >= len(s) {
		return -1
	}
	r, _ := utf8.DecodeRuneInString(s[open+1:])
	if r == ' ' || r == '\t' || r == '\n' {
		return -1
	}
	for j := open + 1; j < len(s); {
		switch s[j] {
		case '\\':
			j += 2
		case '$':
			p, _ := utf8.DecodeLastRuneInString(s[:j])
			if p == ' ' || p == '\t' || p == '\n' {
				return -1
			}
			return j
		default:
			j++
		}
	}
	return -1
}

// hasTeXConstruct reports whether s holds at least one recognized TeX
// construct — a script marker or a known macro. Used to gate $-delimited
// candidates; apostrophes and bare braces deliberately do not count.
func hasTeXConstruct(s string) bool {
	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case '^', '_':
			return true
		case '\\':
			name, after := readMacroName(s, i)
			if mathMacroRecognized(name) {
				return true
			}
			i = after
		default:
			i++
		}
	}
	return false
}

// mathMacroRecognized reports whether name is any macro the converter
// handles structurally or resolves through the glyph table.
func mathMacroRecognized(name string) bool {
	if _, ok := mathMacroGlyphs[name]; ok {
		return true
	}
	switch name {
	case "frac", "sqrt", "left", "right", "begin", "end",
		"mathbf", "mathit", "mathsf", "mathrm", "mathbb",
		"quad", "qquad", ",", ";", ":", "\\":
		return true
	}
	return false
}

// convertMath converts the content of a math candidate.
func convertMath(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	convertMathInto(&b, s)
	return b.String()
}

// convertMathInto renders TeX content s into b: groups, scripts, the
// structural macros, table macros, and the bare-rune rules (- → −,
// ' → ′). Unknown macros pass through literally, their braced group
// included and unconverted.
func convertMathInto(b *strings.Builder, s string) {
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\':
			start := i
			name, after := readMacroName(s, i)
			i = after
			applyMathMacro(b, s, start, name, &i)
		case c == '_' || c == '^':
			applyMathScript(b, s, &i, c)
		case c == '&': // alignment marker: dropped
			i++
		case c == '{':
			content, end, _ := readGroup(s, i)
			convertMathInto(b, content)
			i = end
		case c == '}': // stray close: dropped
			i++
		case c == '-':
			b.WriteRune('−')
			i++
		case c == '\'':
			b.WriteRune('′')
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
}

// applyMathMacro renders the macro whose name starts at start (already
// consumed; i sits just after the name) and advances *i past whatever it
// absorbed.
func applyMathMacro(b *strings.Builder, s string, start int, name string, i *int) {
	switch name {
	case "frac":
		num, e1, ok := readGroupOrToken(s, *i)
		if !ok {
			b.WriteString(s[start:*i])
			return
		}
		den, e2, ok := readGroupOrToken(s, skipHorizSpace(s, e1))
		if !ok {
			b.WriteString(s[start:*i])
			return
		}
		n, d := convertMath(num), convertMath(den)
		if g, ok := mathFractionGlyphs[n+"/"+d]; ok {
			b.WriteString(g)
		} else {
			b.WriteString("(" + n + ")/(" + d + ")")
		}
		*i = e2
	case "sqrt":
		j := *i
		if j < len(s) && s[j] == '[' { // optional root index: ignored
			for j < len(s) && s[j] != ']' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(s) {
				j++
			}
		}
		operand, end, _ := readGroupOrToken(s, j)
		conv := convertMath(operand)
		if utf8.RuneCountInString(conv) == 1 {
			b.WriteString("√" + conv)
		} else {
			b.WriteString("√(" + conv + ")")
		}
		*i = end
	case "left", "right":
		j := skipHorizSpace(s, *i)
		if j >= len(s) {
			*i = j
			return
		}
		if s[j] == '\\' {
			dname, after := readMacroName(s, j)
			if g, ok := mathMacroGlyphs[dname]; ok {
				b.WriteString(g)
			} else {
				b.WriteString(s[j:after])
			}
			*i = after
			return
		}
		if s[j] == '.' { // \left. — invisible delimiter
			*i = j + 1
			return
		}
		b.WriteByte(s[j])
		*i = j + 1
	case "begin", "end":
		if _, e, ok := readGroup(s, *i); ok {
			*i = e
		}
		trimTrailingSpace(b)
		*i = skipHorizSpace(s, *i)
	case "mathbf":
		renderMathStyled(b, s, i, "**", "**")
	case "mathit", "mathsf":
		renderMathStyled(b, s, i, "*", "*")
	case "mathrm":
		renderMathStyled(b, s, i, "", "")
	case "mathbb":
		operand, end, _ := readGroupOrToken(s, *i)
		for _, r := range convertMath(operand) {
			if g, ok := mathbbGlyphs[r]; ok {
				b.WriteRune(g)
			} else {
				b.WriteRune(r)
			}
		}
		*i = end
	case "qquad":
		trimTrailingSpace(b)
		b.WriteString("  ")
		*i = skipHorizSpace(s, *i)
	case "quad", ",", ";", ":":
		trimTrailingSpace(b)
		b.WriteByte(' ')
		*i = skipHorizSpace(s, *i)
	case "\\": // line break
		trimTrailingSpace(b)
		b.WriteByte('\n')
		*i = skipHorizSpace(s, *i)
	default:
		if g, ok := mathMacroGlyphs[name]; ok {
			b.WriteString(g)
			return
		}
		// Unknown macro: pass through literally, with its braced group
		// (if any) raw and unconverted.
		end := *i
		if *i < len(s) && s[*i] == '{' {
			if _, e, ok := readGroup(s, *i); ok {
				end = e
			}
		}
		b.WriteString(s[start:end])
		*i = end
	}
}

// renderMathStyled converts the macro's operand and wraps it in prefix/suffix.
func renderMathStyled(b *strings.Builder, s string, i *int, prefix, suffix string) {
	operand, end, _ := readGroupOrToken(s, *i)
	if operand == "" {
		*i = end
		return
	}
	b.WriteString(prefix + convertMath(operand) + suffix)
	*i = end
}

// applyMathScript renders a _ or ^ script whose marker sits at *i. The
// glyph path is all-or-nothing: every character of the raw operand must
// have a glyph, else the whole operand falls back to bracket form around
// its converted content.
func applyMathScript(b *strings.Builder, s string, i *int, marker byte) {
	glyphs := mathSubGlyphs
	open, close := "_(", ")"
	if marker == '^' {
		glyphs, open, close = mathSupGlyphs, "^(", ")"
	}
	raw, end, _ := readGroupOrToken(s, *i+1)
	if raw == "" {
		b.WriteByte(marker)
		*i = end
		return
	}
	all := true
	for _, r := range raw {
		if _, ok := glyphs[r]; !ok {
			all = false
			break
		}
	}
	if all {
		for _, r := range raw {
			b.WriteRune(glyphs[r])
		}
	} else {
		b.WriteString(open + convertMath(raw) + close)
	}
	*i = end
}

// readMacroName reads the macro token at i (s[i] == '\\'): a run of
// letters, or a single non-letter character. Returns the token without the
// backslash and the index just past it.
func readMacroName(s string, i int) (string, int) {
	if i+1 >= len(s) {
		return "\\", len(s)
	}
	c := s[i+1]
	if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
		j := i + 2
		for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
			j++
		}
		return s[i+1 : j], j
	}
	_, size := utf8.DecodeRuneInString(s[i+1:])
	return s[i+1 : i+1+size], i + 1 + size
}

// readGroup reads a balanced {...} group at i (skipping \-escaped bytes)
// and returns its content without the braces. An unterminated group
// extends to the end of the string.
func readGroup(s string, i int) (string, int, bool) {
	if i >= len(s) || s[i] != '{' {
		return "", i, false
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[i+1 : j], j + 1, true
			}
		}
	}
	return s[i+1:], len(s), true
}

// readGroupOrToken reads one operand: a braced group if present, else a
// macro token, else a single rune.
func readGroupOrToken(s string, i int) (string, int, bool) {
	if i >= len(s) {
		return "", i, false
	}
	if s[i] == '{' {
		return readGroup(s, i)
	}
	if s[i] == '\\' {
		_, after := readMacroName(s, i)
		return s[i:after], after, true
	}
	_, size := utf8.DecodeRuneInString(s[i:])
	return s[i : i+size], i + size, true
}

// skipHorizSpace advances past spaces and tabs.
func skipHorizSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// trimTrailingSpace drops trailing horizontal whitespace already written
// to b, so spacing macros and line breaks consume the whitespace before
// them.
func trimTrailingSpace(b *strings.Builder) {
	s := b.String()
	t := strings.TrimRight(s, " \t")
	if len(t) != len(s) {
		b.Reset()
		b.WriteString(t)
	}
}

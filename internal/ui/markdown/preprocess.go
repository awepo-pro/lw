package markdown

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Sentinel delimiters wrap a provenance/wikilink's literal replacement text
// after insertMarkers rewrites it into the block's markdown. They are the
// four Unicode "invisible operator" code points (category Cf, zero cell
// width per x/ansi's wcwidth table \u2014 verified: a Private-Use-Area
// alternative measured width 1 and shifted lipgloss's word-wrap decision by
// those 2 extra phantom cells per marker, hard-splitting an unrelated word
// mid-hyphen and moving the plain-text goldens). Being zero-width, and
// never markdown-special, they flow through goldmark as ordinary text and
// never split, trigger, merge with a real link/image/emphasis parse, or
// perturb the wrap width the way raw ANSI or "[["/"~~" carriers did
// (repair-1: Critical 1). inline.go's restyleSpans finds and consumes them
// after glamour has rendered and word-wrapped the block.
const (
	provOpen  = "\u2060"
	provClose = "\u2061"
	wikiOpen  = "\u2062"
	wikiClose = "\u2063"

	// escMark is a fifth zero-width "invisible operator" code point (same
	// category Cf, same zero cell width). It is the fixed first half of
	// every escaped pair the package produces: escapeSourceSentinels hides
	// source occurrences of the five runes above behind "escMark + code"
	// pairs (repair-2, R2), and escapeMarkdown hides '~'/'&' inside marker
	// content the same way. unescapeMarkers collapses the pairs back after
	// glamour has rendered the block.
	escMark = "\u2064"
)

// sentinelRunes is every code point escapeSourceSentinels/unescapeMarkers
// treat specially: the four functional sentinels plus the escape marker
// itself (so a source that already contains escMark is handled too).
const sentinelRunes = provOpen + provClose + wikiOpen + wikiClose + escMark

// escapeCode maps each of sentinelRunes to the zero-width variation
// selector that identifies it as the second half of its "escMark + code"
// escaped pair. Variation selectors (U+FE00+) are combining marks: zero
// cells wide in x/ansi, never a line-break or word-join opportunity, and
// passed through untouched by goldmark and glamour \u2014 so an escaped source
// sentinel measures exactly as wide as the rune it stands for (nothing)
// and cannot shift glamour's word-wrap. The first encoding used an ASCII
// digit here; a digit is one cell wide, so every escaped source sentinel
// shifted the wrap decision one cell earlier
// (escaped_source_sentinels_do_not_shift_wrap).
//
// The code runes are disjoint from sentinelRunes on purpose: the escaped
// form must contain *no* bare functional sentinel rune at all, or
// restyleSpans (which only ever looks for those four bare runes, with no
// knowledge of escaping) would match it as a real marker boundary \u2014
// exactly the bug a first attempt at this had, caught by
// source_sentinel_code_points_preserved during repair-2.
var escapeCode = map[rune]rune{
	[]rune(provOpen)[0]:  '\ufe00',
	[]rune(provClose)[0]: '\ufe01',
	[]rune(wikiOpen)[0]:  '\ufe02',
	[]rune(wikiClose)[0]: '\ufe03',
	[]rune(escMark)[0]:   '\ufe04',
}

// contentCode encodes the two ASCII runes goldmark would otherwise consume
// as inline SYNTAX inside marker content, silently dropping the source
// bytes: '~' opens a GFM strikethrough delimiter run ("[[a~~b~~c]]"
// rendered as "abc") and '&' opens an HTML entity reference ("[[Tom
// &amp; Jerry]]" rendered as "Tom & Jerry"). glamour's escapeReplacer only
// un-escapes its own 18 backslash pairs (mdEscapable), so "\~"/"\&" print
// a literal backslash instead \u2014 verified against glamour v2.0.1. Encoding
// them as "escMark + code" hides the byte from goldmark entirely (there is
// no tilde and no "&name;" to parse) and unescapeMarkers restores it after
// render. A variation selector in the source itself is never touched: it
// is only consumed here as the second half of a pair.
var contentCode = map[rune]rune{
	'~': '\ufe05',
	'&': '\ufe06',
}

// unescapeCode is the inverse of escapeCode and contentCode: every code
// rune back to the rune it stands for.
var unescapeCode = func() map[rune]rune {
	m := make(map[rune]rune, len(escapeCode)+len(contentCode))
	for r, code := range escapeCode {
		m[code] = r
	}
	for r, code := range contentCode {
		m[code] = r
	}
	return m
}()

// inlineTokenRe matches, in priority order: a single-line code span
// (left untouched — contract §2 note 2's "protect code spans"), a
// provenance marker "^[path]", or a wikilink "[[x]]" / "[[x|label]]".
var inlineTokenRe = regexp.MustCompile("`[^`\n]*`" + `|\^\[[^\]\n]+\]` + `|\[\[[^\]\n]+\]\]`)

// mdEscapable is exactly glamour's own escapeReplacer's input set
// (baseelement.go): the 18 backslash pairs glamour un-escapes back to
// their literal rune when finally rendering a text token. That set is
// narrower than CommonMark's (goldmark honours "\" before any ASCII
// punctuation but leaves the pair in the text token for the renderer to
// resolve), so it is the only set that can be backslash-escaped here
// without printing a literal backslash — '~' and '&' are handled by
// contentCode instead.
const mdEscapable = "\\`*_{}[]<>()#+-.!|"

// insertMarkers rewrites provenance markers and wikilinks in one non-fence
// block's text (repair-1, replacing the rejected carrier design):
//
//   - "^[raw/articles/gemini.md]" becomes sentinel-wrapped, markdown-escaped
//     literal text: provOpen + "\[gemini.md\]" + provClose.
//   - "[[x|label]]" (or "[[x]]") becomes wikiOpen + "\x" (target only,
//     escaped) + wikiClose.
//
// Backslash-escaping every CommonMark-special byte in the literal text
// (escapeMarkdown) stops goldmark's link/image/list parsers from
// triggering on a literal "[", "]", "-" or "." inside a basename or
// wikilink target — verified empirically: without it, "[notes.md]" alone
// (no markup at all, just literal brackets) is enough to make goldmark
// split the surrounding text into extra runs at the "[". '~' and '&' are
// encoded rather than backslash-escaped (escapeMarkdown/contentCode), so a
// GFM strikethrough run or an "&amp;"-style entity inside marker content
// cannot eat those bytes either. Both replacements are plain text with no
// markdown syntax at all, so they cannot collide with a real
// strikethrough, image or link the way the rejected `~~x~~`/`![[x]]()`
// carriers did — those dispatch on AST node kind alone, and real vault
// syntax reaches the exact same node kind.
//
// Code spans match first in the alternation and are returned unchanged:
// a "^[" or "[[" that happens to sit inside backticks is never rewritten.
// insertMarkers first escapes any of the five sentinel code points already
// present in s (escapeSourceSentinels), so every provOpen/provClose/
// wikiOpen/wikiClose it goes on to insert is unambiguous, then rewrites
// provenance markers and wikilinks into sentinel-wrapped literal text.
func insertMarkers(s string) string {
	s = escapeSourceSentinels(s)
	return inlineTokenRe.ReplaceAllStringFunc(s, func(m string) string {
		switch {
		case strings.HasPrefix(m, "`"):
			return m
		case strings.HasPrefix(m, "^["):
			path := m[2 : len(m)-1]
			return provOpen + escapeMarkdown("["+baseName(path)+"]") + provClose
		default: // "[["
			inner := m[2 : len(m)-2]
			target := inner
			if i := strings.IndexByte(inner, '|'); i >= 0 {
				target = inner[:i]
			}
			return wikiOpen + escapeMarkdown(target) + wikiClose
		}
	})
}

// escapeSourceSentinels turns every occurrence in s of one of the five
// sentinelRunes into its escaped form, escMark + a zero-width code rune
// identifying which rune it was (escapeCode) — never the bare rune itself,
// so it can never be mistaken for a marker boundary insertMarkers goes on
// to add, or matched by restyleSpans, which only ever looks for a bare
// sentinel rune (repair-2, R2). The pair is zero cells wide in total, so
// it measures exactly like the rune it replaced and glamour's word-wrap
// decision cannot see it. A no-op, allocation-free fast path handles the
// overwhelming common case (no sentinel-like rune present at all).
func escapeSourceSentinels(s string) string {
	if !strings.ContainsAny(s, sentinelRunes) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if code, ok := escapeCode[r]; ok {
			b.WriteString(escMark)
			b.WriteRune(code)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unescapeMarkers reverses escapeSourceSentinels and escapeMarkdown on a
// block's final, already-restyled output: every "escMark + code rune" pair
// collapses back to the rune it stood for (unescapeCode), restoring the
// source byte for byte outside any marker. It runs after restyleSpans has
// already consumed every *functional* (bare, non-escaped) sentinel pair,
// so nothing here can be mistaken for one of those — the escaped form was
// never visible to it as a bare sentinel rune in the first place. A bare
// variation selector from the source itself is never preceded by escMark
// (source escMark was escaped first), so it passes through untouched.
func unescapeMarkers(s string) string {
	if !strings.Contains(s, escMark) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], escMark) {
			if r, size := utf8.DecodeRuneInString(s[i+len(escMark):]); size > 0 {
				if orig, ok := unescapeCode[r]; ok {
					b.WriteRune(orig)
					i += len(escMark) + size
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// escapeMarkdown neutralizes every byte of s (a marker's replacement text)
// that goldmark's inline parsers would otherwise consume as syntax:
//
//   - the 18 mdEscapable runes are backslash-escaped — glamour's own
//     escapeReplacer (baseelement.go) un-escapes exactly those pairs back
//     to the literal rune at render time;
//   - '~' and '&' have no working backslash form (glamour's replacer does
//     not know "\~"/"\&", so the backslash would print) and are hidden as
//     "escMark + code" pairs (contentCode) for unescapeMarkers to restore.
func escapeMarkdown(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 && strings.ContainsRune(mdEscapable, r) {
			b.WriteByte('\\')
			b.WriteRune(r)
			continue
		}
		if code, ok := contentCode[r]; ok {
			b.WriteString(escMark)
			b.WriteRune(code)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// baseName returns the last "/"-separated component of a vault-relative
// path (provenance paths are always slash-separated, per project
// convention, regardless of host OS).
func baseName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// isTableBlock reports whether every line of blk opens a markdown table row
// (mockgen.render_block: "all(l.lstrip().startswith('|') for l in blk)").
func isTableBlock(blk []string) bool {
	if len(blk) == 0 {
		return false
	}
	for _, l := range blk {
		if !strings.HasPrefix(strings.TrimLeft(l, " \t"), "|") {
			return false
		}
	}
	return true
}

// isFenceBlock reports whether blk is a fenced code block (mockgen.blocks:
// a closed fence is one block on its own). Fence blocks are never run
// through insertMarkers: their content is verbatim code, including any
// literal "^[" or "[[" text.
func isFenceBlock(blk []string) bool {
	return len(blk) > 0 && strings.HasPrefix(strings.TrimLeft(blk[0], " \t"), "```")
}

// splitBlocks splits body into top-level blocks exactly as mockgen.blocks
// does: consecutive non-blank lines form one block; a fenced code block
// (open "```" to matching close) is one block on its own, blank lines
// inside it included; blank lines outside a fence separate blocks and are
// themselves dropped.
func splitBlocks(body string) [][]string {
	var out [][]string
	var cur []string
	fence := false

	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
	}

	for _, l := range strings.Split(body, "\n") {
		trimmed := strings.TrimLeft(l, " \t")
		if strings.HasPrefix(trimmed, "```") {
			if !fence {
				flush()
			}
			cur = append(cur, l)
			fence = !fence
			if !fence {
				flush()
			}
			continue
		}
		switch {
		case fence:
			cur = append(cur, l)
		case strings.TrimSpace(l) == "":
			flush()
		default:
			cur = append(cur, l)
		}
	}
	flush()
	return out
}

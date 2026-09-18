// Package slug turns arbitrary user text into the lowercase ASCII path
// segments the staging engine's validator accepts — the one slug rule for
// the whole tree. Before A-805 three helpers slugified with two different
// letter rules, and the two that kept every unicode.IsLetter rune turned a
// CJK title into a path the validator rejected outright, so the source
// could not be ingested at all (008 contract §12, amendment A-805).
//
// The rule: lowercase, Unicode NFKD-decompose, drop nonspacing marks
// (é→e, and NFKD also folds full-width Latin Ｑ→q), transliterate the nine
// Latin letters NFKD cannot decompose (ß→ss, æ→ae, …), keep [a-z0-9], fold
// every other run of runes — spaces, punctuation, CJK, emoji — into a
// single "-", then trim "-". Make returns "" when nothing survives; callers
// own the fallback.
package slug

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Make returns s as a lowercase ASCII path segment that stage's
// validator accepts — ^[a-z0-9]+(-[a-z0-9]+)*$ — or "" when nothing
// of s survives. Callers own the fallback.
func Make(s string) string {
	var b strings.Builder
	dash := false // only one "-" per run of dropped runes
	for _, r := range norm.NFKD.String(strings.ToLower(s)) {
		if t, ok := translit(r); ok {
			b.WriteString(t)
			dash = false
			continue
		}
		switch {
		case unicode.Is(unicode.Mn, r):
			// A dropped combining mark joins no run: "é" decomposes to
			// "e" + U+0301, and the mark must not turn "e" into "e-".
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// translit spells out the Latin letters NFKD does not decompose, so they
// survive as their ASCII readings (008 contract §12): ß→ss, æ→ae, œ→oe,
// ø→o, đ→d, ð→d, ł→l, þ→th, ı→i.
func translit(r rune) (string, bool) {
	switch r {
	case 'ß':
		return "ss", true
	case 'æ':
		return "ae", true
	case 'œ':
		return "oe", true
	case 'ø':
		return "o", true
	case 'đ':
		return "d", true
	case 'ð':
		return "d", true
	case 'ł':
		return "l", true
	case 'þ':
		return "th", true
	case 'ı':
		return "i", true
	}
	return "", false
}

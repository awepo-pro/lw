package score

import (
	"sort"
	"strings"

	"github.com/kljensen/snowball/english"
)

// slugStopWords are dropped from a slug before two slugs are compared: a page
// called "the-http-versions" and one called "http-versions" are the same
// topic, and "vs" or "on" shared by two unrelated pages is not evidence.
var slugStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "the": true, "of": true, "in": true,
	"on": true, "for": true, "to": true, "vs": true, "with": true,
}

// SlugTokens is the token set a page slug (its file name without ".md") is
// compared by: the slug lowercased and split on "-", empty tokens and stop
// words dropped, every remaining token that is all ASCII letters reduced by
// the snowball English stemmer ("versions" and "version" meet) and any other
// token — a digit, "m3", a CJK word — kept as it is. The result is sorted and
// has no duplicates; a slug with no token left gives none. (049.)
func SlugTokens(slug string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.Split(strings.ToLower(slug), "-") {
		if tok == "" || slugStopWords[tok] {
			continue
		}
		if isASCIILetters(tok) {
			tok = english.Stem(tok, false)
		}
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	sort.Strings(out)
	return out
}

// isASCIILetters reports whether s is non-empty and all ASCII letters, the
// only tokens the English stemmer is meant for.
func isASCIILetters(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// NearDuplicate reports whether two page slugs name the same topic: the
// Jaccard index of their SlugTokens sets is at least 0.5. The test is
// 2·|A∩B| ≥ |A∪B|, which is the same inequality without a float to round at
// exactly 0.5. A slug with no tokens is a duplicate of nothing. (049.)
func NearDuplicate(a, b string) bool {
	ta, tb := SlugTokens(a), SlugTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	in := make(map[string]bool, len(ta))
	for _, tok := range ta {
		in[tok] = true
	}
	inter := 0
	for _, tok := range tb {
		if in[tok] {
			inter++
		}
	}
	return 2*inter >= len(ta)+len(tb)-inter
}

// LinesKept reports whether after still holds every non-blank line of
// before: a multiset check, so a line that was there k times must be there at
// least k times, and lines are compared after their trailing spaces and tabs
// are trimmed. It answers "did this edit of a page lose anything" — added
// lines, moved lines and blank-line changes are free; a dropped, shortened or
// reworded line is not. (049.)
func LinesKept(before, after string) bool {
	have := map[string]int{}
	for _, l := range strings.Split(after, "\n") {
		have[strings.TrimRight(l, " \t")]++
	}
	for _, l := range strings.Split(before, "\n") {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			continue
		}
		if have[l] == 0 {
			return false
		}
		have[l]--
	}
	return true
}

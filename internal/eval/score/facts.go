package score

import (
	"fmt"
	"regexp"
	"strings"
)

// regexpPrefix marks an alternative that is a regular expression, not a
// literal: "re:\bv[34]\b".
const regexpPrefix = "re:"

// apostropheFolder straightens the typographic apostrophes a model emits
// ("doesn’t") so they compare equal to the straight one a fact is written
// with. Only apostrophes: no other punctuation is ever folded, because a
// dash or quote in a fact is usually part of the fact.
var apostropheFolder = strings.NewReplacer("’", "'", "‘", "'")

// Normalize is the one fold every comparison in this package rides on:
// lower-cased, every run of whitespace (newlines, tabs, non-breaking spaces,
// wrapped lines) collapsed to one space, the ends trimmed, typographic
// apostrophes straightened. A fact is a claim about content, not about how a
// model wrapped its line or capitalised a heading, and folding both sides
// the same way keeps that the only thing the match is blind to. (037 T2.)
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(apostropheFolder.Replace(s))), " ")
}

// MatchFact reports whether text satisfies one fact: true when ANY of alts
// matches. An alternative is a literal substring of the normalised text —
// both sides go through Normalize, so "1.6 trillion" matches "1.6 Trillion
// params" — sitting on word boundaries where its edges are letters or digits
// (containsBounded: "IAM" is not in "diameter"), or, with a "re:" prefix, a
// regular expression run over the normalised text, case-insensitively (the text is already lower-cased, so a
// case-sensitive pattern with a capital could never hit; and the whitespace
// is already collapsed, so a pattern spells a gap as one space or \s).
//
// The error is only ever a "re:" alternative that does not compile (or is
// empty), and every regexp is compiled BEFORE the first match is tried: an
// eval set with a typo in its seventh alternative must fail the run the first
// time it is scored, not only on the answers where the earlier alternatives
// happen to miss. An empty literal alternative never matches — a stray ""
// must not turn a fact into a free point. (037 T2.)
func MatchFact(text string, alts []string) (bool, error) {
	res := make([]*regexp.Regexp, len(alts)) // res[i] != nil for a "re:" alternative
	for i, alt := range alts {
		pat, ok := strings.CutPrefix(alt, regexpPrefix)
		if !ok {
			continue
		}
		if strings.TrimSpace(pat) == "" {
			return false, fmt.Errorf("score: empty regexp alternative %q", alt)
		}
		re, err := regexp.Compile("(?i)" + pat)
		if err != nil {
			return false, fmt.Errorf("score: bad regexp alternative %q: %w", alt, err)
		}
		res[i] = re
	}
	norm := Normalize(text)
	for i, alt := range alts {
		if res[i] != nil {
			if res[i].MatchString(norm) {
				return true, nil
			}
			continue
		}
		if lit := Normalize(alt); lit != "" && containsBounded(norm, lit) {
			return true, nil
		}
	}
	return false, nil
}

// isWordByte reports whether b is an ASCII letter or digit. Only ASCII: a
// non-ASCII neighbour (CJK prose written without spaces, "使用IAM角色") is not
// a word character here, which is also Go's own \b. (037 T2, A-037-6.)
func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// containsBounded reports whether lit occurs in norm on word boundaries: an
// edge of lit that is a letter or digit must not touch another letter or
// digit of norm. A plain substring test lets "IAM" hit inside "diameter",
// "1.50" inside "11.50" and "62.1" inside "162.1" — the fact reads as found
// when the text says something else. An edge that is punctuation (".net",
// "c++") needs no boundary. The price is the suffix: "daemonset" no longer
// hits "daemonsets", so a fact that should accept the plural lists it or
// uses a "re:" alternative. Both strings are already Normalised; every
// occurrence is tried, not just the first. (037 T2, A-037-6.)
func containsBounded(norm, lit string) bool {
	checkLeft, checkRight := isWordByte(lit[0]), isWordByte(lit[len(lit)-1])
	for from := 0; from < len(norm); {
		i := strings.Index(norm[from:], lit)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(lit)
		if (!checkLeft || i == 0 || !isWordByte(norm[i-1])) && (!checkRight || end == len(norm) || !isWordByte(norm[end])) {
			return true
		}
		from = i + 1
	}
	return false
}

// FactRecall scores text against a set of facts, each a list of
// alternatives (see MatchFact): hit is how many facts matched, and missed is
// the alternatives of every fact that did not, exactly as the eval set wrote
// them and in set order — the report prints them, so a normalised or
// re-ordered copy would misdescribe the set. A bad "re:" alternative fails
// the whole recall (naming the fact) rather than scoring that fact as a
// miss: a silent miss would read as the model's fault. (037 T2.)
func FactRecall(text string, facts [][]string) (hit int, missed [][]string, err error) {
	for i, alts := range facts {
		ok, err := MatchFact(text, alts)
		if err != nil {
			return 0, nil, fmt.Errorf("fact %d: %w", i, err)
		}
		if ok {
			hit++
		} else {
			missed = append(missed, alts)
		}
	}
	return hit, missed, nil
}

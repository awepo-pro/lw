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
// params" — or, with a "re:" prefix, a regular expression run over the
// normalised text, case-insensitively (the text is already lower-cased, so a
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
		if lit := Normalize(alt); lit != "" && strings.Contains(norm, lit) {
			return true, nil
		}
	}
	return false, nil
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

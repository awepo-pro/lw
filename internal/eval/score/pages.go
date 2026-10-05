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
// before. A line is kept when, with trailing spaces and tabs trimmed, a line
// of after is it or is it with text INSERTED at one point: a "See also" line
// that gained ", [[b]]" at its end, a sentence that gained a link in its
// middle, a phrase that gained a word at its front. The check is a multiset,
// so each line of after holds at most one line of before — a line that was
// there k times needs k lines in after that are it or extend it. It answers
// "did this edit of a page lose anything": added lines, moved lines and
// blank-line changes are free; a dropped, shortened, reworded or
// twice-edited line (two separate insertions are two edits) is not.
// (049, A-049-4, A-049-6.)
//
// Which line of after holds which line of before is a bipartite MATCHING, and
// no scan order can be trusted to find it. With only appends the candidates of
// a line were nested or disjoint — two lines are both prefixes of one only
// when one is a prefix of the other — so longest-first was safe. Insertions
// break that: "ab" and "cd" can both be held by "abcd", neither is a prefix of
// the other, and "ab" can also be held by "abzz", so giving "abcd" to whichever
// is tried first strands the other. An exact twin is no longer final either:
// "ac" fits "abc" and "abc" fits "Xabc" but "ac" does not fit "Xabc", so "abc"
// must give up its twin. Exact twins are therefore only the starting matching;
// each line still unmatched then looks for an augmenting path (Kuhn's
// algorithm), moving already-matched lines to other candidates when it must.
// The answer is the same in every order of either side's lines.
func LinesKept(before, after string) bool {
	return len(lostLines(before, after)) == 0
}

// lostLines returns the non-blank lines of before that no line of after can
// be given to once the lines are matched as LinesKept describes — a smallest
// such set, in the order the lines appear in before. Which of several
// competing lines is reported is a choice, how many are is not.
func lostLines(before, after string) []string {
	old, cur := nonBlankLines(before), nonBlankLines(after)
	owner := make([]int, len(cur)) // the line of old that cur[j] holds; -1 for none
	twins := map[string][]int{}    // the cur lines with this text that are still unheld
	for j, l := range cur {
		owner[j] = -1
		twins[l] = append(twins[l], j)
	}
	var open []int // the lines of old with no exact twin
	for i, l := range old {
		if js := twins[l]; len(js) > 0 {
			owner[js[0]], twins[l] = i, js[1:]
			continue
		}
		open = append(open, i)
	}
	var lost []string
	for _, i := range open {
		if !holdLine(i, old, cur, owner, make([]bool, len(cur))) {
			lost = append(lost, old[i])
		}
	}
	return lost
}

// holdLine finds a line of cur for old[i], taking one from the line that holds
// it now when that line can move to another; seen is the lines of cur this
// search has already tried. It reports whether old[i] is now held.
func holdLine(i int, old, cur []string, owner []int, seen []bool) bool {
	for j, m := range cur {
		if seen[j] || !insertedInto(old[i], m) {
			continue
		}
		seen[j] = true
		if owner[j] < 0 || holdLine(owner[j], old, cur, owner, seen) {
			owner[j] = i
			return true
		}
	}
	return false
}

// insertedInto reports whether m is l with text inserted at one point, which
// includes inserting nothing: m = P + X + S for a split l = P + S, P or S
// empty. m therefore starts with P, ends with S and is at least as long as l;
// the length is what stops the two from overlapping ("a" is not "aa" with
// something inserted). Such a split exists exactly when the longest common
// prefix and the longest common suffix of l and m together cover l. The
// comparison is on bytes, which for valid UTF-8 text finds the same splits.
func insertedInto(l, m string) bool {
	if len(m) < len(l) {
		return false
	}
	head := 0
	for head < len(l) && l[head] == m[head] {
		head++
	}
	tail := 0
	for tail < len(l) && l[len(l)-1-tail] == m[len(m)-1-tail] {
		tail++
	}
	return head+tail >= len(l)
}

// nonBlankLines splits text into its lines, trailing spaces and tabs trimmed,
// and drops the blank ones.
func nonBlankLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimRight(l, " \t"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

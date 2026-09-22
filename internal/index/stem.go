// stem.go is 028's morphological layer. Tokens reaching the BM25 fields and
// Search's query terms are reduced to their stems, so "decoder" finds a page
// whose abstract says "decoding". Tokenize itself stays byte-for-byte the
// backbone §3 surface contract (correction log #1) and its non-index
// consumers — tools/raw.list's filter and the snippet's literal matcher —
// keep surface tokens (correction log #2), so stemming lives strictly
// downstream of Tokenize: here and in analyze.
package index

import (
	"strings"

	"github.com/kljensen/snowball/english"
)

// stem returns tok's morphological stem. A token made only of ASCII letters
// goes through the snowball English stemmer ("decoding" and "decoder" both
// collapse to "decod"). A hyphenated token whose every segment is ASCII
// letters has each segment stemmed and the parts rejoined
// ("speculative-decoding" → "specul-decod"), so a compound contributes the
// same stems its parts would contribute standing alone. Anything else — a
// token containing a digit ("gpt4", "2024") or a non-ASCII rune ("café",
// "量子") — is returned unchanged: the stemmer is English-only, and
// identifiers and non-English words must pass through intact.
func stem(tok string) string {
	if !strings.Contains(tok, "-") {
		if isASCIILetters(tok) {
			return english.Stem(tok, false)
		}
		return tok
	}
	segs := strings.Split(tok, "-")
	for _, s := range segs {
		if !isASCIILetters(s) {
			return tok
		}
	}
	for i, s := range segs {
		segs[i] = english.Stem(s, false)
	}
	return strings.Join(segs, "-")
}

// isASCIILetters reports whether s is non-empty and every byte is an ASCII
// letter. Tokens arrive lowercased from Tokenize, so 'A'-'Z' cannot occur
// in practice, but the predicate stays honest on its own.
func isASCIILetters(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// analyze is 028's one indexing step for a plain string field or query:
// Tokenize, then stem every token. It replaces Tokenize for the title field
// at Build and for Search's query terms; Tokenize itself keeps its surface
// consumers (backbone §3).
func analyze(s string) []string {
	return stemmed(Tokenize(s))
}

// stemmed applies stem to every token, in order. Split from analyze because
// the body, abstract and tag fields are not single strings: their tokens are
// assembled by bodyTokens (whose wikilink extra-segment rule must run BEFORE
// stemming — 028 contract F.S2), abstractTokens and tagTokens, and the
// assembled list is stemmed in one pass afterwards.
func stemmed(tokens []string) []string {
	if tokens == nil {
		return nil
	}
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = stem(t)
	}
	return out
}

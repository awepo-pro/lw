package index

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/vault"
)

// stopwords is the package-level English stopword list. It is a true
// constant-ish var (00-conventions.md §2): never mutated after init, just a
// map literal that cannot be a Go const.
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "been": true, "but": true, "by": true, "for": true,
	"from": true, "has": true, "have": true, "if": true, "in": true,
	"into": true, "is": true, "it": true, "its": true, "no": true,
	"not": true, "of": true, "on": true, "or": true, "so": true,
	"such": true, "than": true, "that": true, "the": true, "their": true,
	"then": true, "there": true, "these": true, "they": true, "this": true,
	"to": true, "was": true, "were": true, "will": true, "with": true,
}

// Tokenize lowercases s and splits it into terms on every non-alphanumeric
// character, except a hyphen that sits between two alphanumeric characters
// ("inside a word"), which is kept as part of the term. Terms of length 1
// (by rune count) and stopwords are dropped.
//
// Contract (backbone §3): wikilink targets get additional treatment beyond
// this function — see wikilinkTokens — so that [[kv-cache]] contributes
// "kv-cache", "kv" and "cache" to the body field, not just "kv-cache".
func Tokenize(s string) []string {
	lower := strings.ToLower(s)
	runes := []rune(lower)
	n := len(runes)

	var tokens []string
	var cur []rune

	flush := func() {
		if len(cur) == 0 {
			return
		}
		if tok, ok := filterToken(string(cur)); ok {
			tokens = append(tokens, tok)
		}
		cur = cur[:0]
	}

	for i := 0; i < n; i++ {
		r := runes[i]
		switch {
		case isAlnum(r):
			cur = append(cur, r)
		case r == '-' && len(cur) > 0 && i+1 < n && isAlnum(runes[i+1]):
			// A hyphen "inside a word": the character before it is already
			// in cur (so it was alnum), and the one after it is alnum too.
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()

	return tokens
}

// isAlnum reports whether r is a letter or digit, under Unicode's
// definition rather than ASCII's, so an accented word in a page body still
// tokenizes sensibly.
func isAlnum(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// filterToken applies Tokenize's length and stopword rules to a single
// already-lowercased candidate token. Split out so wikilinkTokens can reuse
// the exact same filter when it manufactures extra tokens by hand.
func filterToken(s string) (string, bool) {
	if utf8.RuneCountInString(s) <= 1 {
		return "", false
	}
	if stopwords[s] {
		return "", false
	}
	return s, true
}

// wikilinkTokens returns the tokens a wikilink target contributes to the
// body field: the whole target (tokenized like any other text, so a
// non-hyphenated target such as "gpt4" or a multi-segment one still comes
// through unchanged) plus, when the target contains a hyphen, each
// hyphen-separated part, filtered the same way as any other token.
//
// Contract (backbone §3): [[kv-cache]] must yield "kv-cache", "kv" and
// "cache".
func wikilinkTokens(target string) []string {
	lower := strings.ToLower(target)

	tokens := Tokenize(lower)
	if !strings.Contains(lower, "-") {
		return tokens
	}

	for _, part := range strings.Split(lower, "-") {
		if tok, ok := filterToken(part); ok {
			tokens = append(tokens, tok)
		}
	}
	return tokens
}

// bodyTokens tokenizes p.Body for the body field. Wikilink spans are
// excluded from the plain-text pass — the raw "[[...]]" bytes are never
// tokenized directly — and replaced with wikilinkTokens(target) plus the
// alias's own words (backbone §3): a wikilink's alias is indexed as body
// prose, because it is what the reader actually sees rendered. [[kv-cache|the
// KV cache]] contributes "kv-cache", "kv", "cache" (from the target) and
// "the"-filtered "kv", "cache" (from the alias, tokenized the same as any
// other text) — a token reachable from both target and alias is genuinely
// double the evidence and is deliberately not deduplicated. A link with no
// alias contributes nothing extra, since Tokenize("") is nil.
//
// Contract (backbone §2.5): ParseWikilinks returns p.Links in ascending
// Start order, which this walk relies on.
func bodyTokens(p *vault.Page) []string {
	body := p.Body

	var tokens []string
	last := 0
	for _, link := range p.Links {
		if link.Start < last || link.End > len(body) || link.Start > link.End {
			// Defensive: malformed offsets should never happen given the
			// §2.5 contract, but a corrupt link must not corrupt indexing.
			continue
		}
		if link.Start > last {
			tokens = append(tokens, Tokenize(body[last:link.Start])...)
		}
		tokens = append(tokens, wikilinkTokens(link.Target)...)
		tokens = append(tokens, Tokenize(link.Alias)...)
		last = link.End
	}
	if last < len(body) {
		tokens = append(tokens, Tokenize(body[last:])...)
	}
	return tokens
}

// tagTokens tokenizes every tag in tags for the tag field.
func tagTokens(tags []string) []string {
	var tokens []string
	for _, tag := range tags {
		tokens = append(tokens, Tokenize(tag)...)
	}
	return tokens
}

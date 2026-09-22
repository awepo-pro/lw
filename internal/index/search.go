// search.go is Index.Search and its candidate selection (split out of
// index.go, Tier-1 review's file-split rule, so index.go holds the type and
// its mutators while this file holds the query path). Every function here
// runs over the docs map the entry point loaded once (008 A-802): a
// concurrent Rebuild or Update can never swap a map out from under a
// scoring pass in flight.
package index

import (
	"sort"
	"strings"
)

// Search stems q (028 analyze — the same step Build ran over every field),
// applies o's filters structurally, scores the surviving candidates with
// BM25 over the four weighted fields, and returns the results sorted by
// Score descending, ties broken by Path ascending (backbone §3) — never by
// map iteration order.
func (ix *Index) Search(q string, o Options) []Hit {
	terms := uniqueSortedTokens(analyze(q))
	if len(terms) == 0 {
		return nil
	}
	// Snippets stay literal (correction log #2): the surface tokens —
	// Tokenize's own output, unstemmed — are what the snippet matcher looks
	// for in the text, ordered by the rarity of their stems.
	surface := uniqueSortedTokens(Tokenize(q))

	limit := o.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 0 {
		limit = 0
	}
	if limit == 0 {
		return nil
	}

	candidates := ix.filteredCandidates(o)
	if len(candidates) == 0 {
		return nil
	}

	n := len(candidates)
	lens := make(map[string]int, n)
	var totalLen int
	for _, d := range candidates {
		l := combinedLen(d)
		lens[d.Path] = l
		totalLen += l
	}
	avgLen := float64(totalLen) / float64(n)
	if avgLen == 0 {
		avgLen = 1 // an all-empty corpus; avoids a division by zero below
	}

	df := documentFrequencies(candidates, terms)
	rarest := orderedByRarity(terms, df)
	rarestSurface := orderedByStemRarity(surface, df)

	var hits []Hit
	for _, d := range candidates {
		var score float64
		for _, t := range terms {
			score += termScore(d, t, n, df[t], avgLen)
		}
		if score <= 0 {
			continue
		}
		hits = append(hits, Hit{
			Path:    d.Path,
			Title:   d.Title,
			Score:   score,
			Snippet: buildSnippetStemmed(d.Abstract, d.Body, rarestSurface, rarest),
		})
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})

	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// filteredCandidates returns every doc matching o's Type/Tags/date filters,
// in sorted Path order — collected before any map is ranged for scoring, so
// map iteration order never reaches Search's output. It loads the docs map
// once, so the whole scoring pass runs over one coherent map (008 A-802).
func (ix *Index) filteredCandidates(o Options) []*docEntry {
	var docs map[string]*docEntry
	if p := ix.docs.Load(); p != nil {
		docs = *p
	}
	paths := make([]string, 0, len(docs))
	for p := range docs {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var out []*docEntry
	for _, p := range paths {
		d := docs[p]
		if matchesOptions(d, o) {
			out = append(out, d)
		}
	}
	return out
}

// matchesOptions applies o's structural filters to d.
func matchesOptions(d *docEntry, o Options) bool {
	if o.Type != "" && d.Type != o.Type {
		return false
	}
	for _, tag := range o.Tags {
		if !containsString(d.Tags, tag) {
			return false
		}
	}
	if !o.After.IsZero() && d.Updated.Time.Before(o.After.Time) {
		return false
	}
	if !o.Before.IsZero() && d.Updated.Time.After(o.Before.Time) {
		return false
	}
	return true
}

// containsString reports whether s is present in list.
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// documentFrequencies counts, for each term, how many of candidates contain
// it at all (in any of the four fields).
func documentFrequencies(candidates []*docEntry, terms []string) map[string]int {
	df := make(map[string]int, len(terms))
	for _, t := range terms {
		for _, d := range candidates {
			if combinedFreq(d, t) > 0 {
				df[t]++
			}
		}
	}
	return df
}

// orderedByRarity returns terms sorted by ascending document frequency
// (rarest, most distinguishing term first), ties broken alphabetically so
// the order — and therefore the snippet centred on it — is deterministic.
func orderedByRarity(terms []string, df map[string]int) []string {
	out := append([]string(nil), terms...)
	sort.Slice(out, func(i, j int) bool {
		if df[out[i]] != df[out[j]] {
			return df[out[i]] < df[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// uniqueSortedTokens deduplicates and sorts tokens, so summing per-term BM25
// contributions always happens in the same order regardless of map
// iteration (00-conventions.md §3).
func uniqueSortedTokens(tokens []string) []string {
	set := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		set[t] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// orderedByStemRarity returns the query's surface tokens sorted by the
// document frequency of their stem (rarest, most distinguishing word
// first), ties broken alphabetically on the surface form, so the snippet —
// which looks for the surface tokens literally — is centred on the same
// word BM25 found most distinguishing.
func orderedByStemRarity(surface []string, df map[string]int) []string {
	out := append([]string(nil), surface...)
	sort.Slice(out, func(i, j int) bool {
		if df[stem(out[i])] != df[stem(out[j])] {
			return df[stem(out[i])] < df[stem(out[j])]
		}
		return out[i] < out[j]
	})
	return out
}

// buildSnippet extracts Hit.Snippet from a hit's abstract and body: a
// window of at most snippetMaxRunes runes centred on the first literal
// occurrence of the earliest term (by rarity) that actually appears — in
// the abstract first (014: the abstract is the summary a reader scans
// first), else in the body — elided with "…" on whichever side was cut.
// When no query term occurs in either field literally (e.g. every match
// came from the title or tag fields only), it falls back to the start of
// the abstract when the abstract is non-empty, else the start of body.
// An empty abstract leaves exactly the pre-014 behavior.
func buildSnippet(abstract, body string, orderedTerms []string) string {
	return buildSnippetStemmed(abstract, body, orderedTerms, nil)
}

// buildSnippetStemmed is Search's 028 snippet rule. The query's SURFACE
// tokens (ordered by the rarity of their stems) are looked for literally —
// byte-for-byte today's rule, preferred wherever it hits, because a stem
// substring usually stops mid-word and the text's own spelling is what a
// reader should see. Only when no surface token occurs anywhere does the
// stem pass run: the stems, in the same rarity order, are tried literally
// (a stem is a substring of the inflected forms it came from, so "decod"
// still centres the window on "decoding") before the existing
// abstract/body-start fallback.
func buildSnippetStemmed(abstract, body string, orderedSurface, orderedStems []string) string {
	if idx, ok := firstTermIndex(abstract, orderedSurface); ok {
		return runeWindow(abstract, idx)
	}
	if idx, ok := firstTermIndex(body, orderedSurface); ok {
		return runeWindow(body, idx)
	}
	if idx, ok := firstTermIndex(abstract, orderedStems); ok {
		return runeWindow(abstract, idx)
	}
	if idx, ok := firstTermIndex(body, orderedStems); ok {
		return runeWindow(body, idx)
	}
	if abstract != "" {
		return runeWindow(abstract, 0)
	}
	return runeWindow(body, 0)
}

// firstTermIndex returns the byte offset of the first literal,
// case-insensitive occurrence of the earliest term (by rarity) that appears
// in s, and whether any term does.
func firstTermIndex(s string, orderedTerms []string) (int, bool) {
	lower := strings.ToLower(s)
	for _, t := range orderedTerms {
		if idx := strings.Index(lower, t); idx >= 0 {
			return idx, true
		}
	}
	return 0, false
}

// runeWindow returns the at-most-snippetMaxRunes-rune window of body
// centred on the rune containing byteOffset, never splitting a UTF-8 rune,
// with "…" prepended/appended when the window is a strict sub-range of
// body. Whitespace inside the window is collapsed to single spaces.
func runeWindow(body string, byteOffset int) string {
	if byteOffset < 0 {
		byteOffset = 0
	}
	if byteOffset > len(body) {
		byteOffset = len(body)
	}

	runes := []rune(body)
	total := len(runes)
	centerIdx := len([]rune(body[:byteOffset]))

	budget := snippetMaxRunes
	half := budget / 2
	start := centerIdx - half
	end := start + budget
	if start < 0 {
		end -= start
		start = 0
	}
	if end > total {
		start -= end - total
		end = total
	}
	if start < 0 {
		start = 0
	}

	prefix := start > 0
	suffix := end < total
	// Reserve one rune of budget for each elision mark actually used, so
	// the finished snippet — marks included — never exceeds
	// snippetMaxRunes runes.
	if prefix && start < end {
		start++
	}
	if suffix && start < end {
		end--
	}

	text := collapseWhitespace(string(runes[start:end]))

	var b strings.Builder
	if prefix {
		b.WriteString("…")
	}
	b.WriteString(text)
	if suffix {
		b.WriteString("…")
	}
	return b.String()
}

// collapseWhitespace folds every run of whitespace (including newlines) in
// s into a single space and trims the ends, so a snippet reads as one line
// regardless of where in the body it was cut from.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Package index is the in-memory inverted word index over a vault: tokenizing
// page bodies, titles and tags, scoring matches with BM25, and persisting the
// result to disk for reuse across runs. It will hold index.go, tokenize.go,
// bm25.go and persist.go.
package index

import (
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// defaultLimit is what Options.Limit == 0 means.
const defaultLimit = 20

// snippetMaxRunes is Hit.Snippet's maximum length, in runes, including any
// elision marks.
const snippetMaxRunes = 200

// Options filters a Search. Filters are applied structurally, before any
// term is scored — never folded into the query itself (backbone §3).
type Options struct {
	Type   string     // "" = any
	Tags   []string   // AND across tags
	After  vault.Date // zero = unbounded
	Before vault.Date
	Limit  int // 0 => 20
}

// Hit is one search result.
type Hit struct {
	Path    string
	Title   string
	Score   float64
	Snippet string // <= 200 runes around the best match, "…" elided
}

// docEntry is everything the index keeps about one page, derived once at
// Build/Update time from a *vault.Page. Every field here is a pure function
// of that one page, so Update never needs to look at any other document —
// which is what makes an incrementally built index identical to a fully
// rebuilt one (S1-T4 TestIncrementalMatchesFull).
type docEntry struct {
	Path    string
	Title   string
	Type    string
	Tags    []string
	Created vault.Date
	Updated vault.Date
	SHA256  string
	Body    string // raw body text, kept for snippet extraction

	BodyTermFreq  map[string]int
	TitleTermFreq map[string]int
	TagTermFreq   map[string]int
	BodyLen       int
	TitleLen      int
	TagLen        int
}

// Index is the in-memory inverted word index over a vault's pages.
type Index struct {
	docs map[string]*docEntry // keyed by Path
}

// Build constructs a fresh Index over every page in v.
func Build(v *vault.Vault) *Index {
	ix := &Index{docs: make(map[string]*docEntry)}
	for _, p := range v.Pages() {
		ix.docs[p.Path] = buildDocEntry(p)
	}
	return ix
}

// buildDocEntry derives a docEntry from p. It touches nothing but p, so the
// result is identical regardless of whether it is produced by Build or by
// Update.
func buildDocEntry(p *vault.Page) *docEntry {
	body := bodyTokens(p)
	title := Tokenize(p.FM.Title)
	tags := tagTokens(p.FM.Tags)

	return &docEntry{
		Path:    p.Path,
		Title:   p.FM.Title,
		Type:    string(p.FM.Type),
		Tags:    append([]string(nil), p.FM.Tags...),
		Created: p.FM.Created,
		Updated: p.FM.Updated,
		SHA256:  p.SHA256(),
		Body:    p.Body,

		BodyTermFreq:  freqMap(body),
		TitleTermFreq: freqMap(title),
		TagTermFreq:   freqMap(tags),
		BodyLen:       len(body),
		TitleLen:      len(title),
		TagLen:        len(tags),
	}
}

// freqMap counts occurrences of each token.
func freqMap(tokens []string) map[string]int {
	m := make(map[string]int, len(tokens))
	for _, t := range tokens {
		m[t]++
	}
	return m
}

// Update re-indexes the pages named by paths: a path still present in v is
// (re-)built from the vault's current content, a path no longer present in
// v is dropped from the index entirely.
func (ix *Index) Update(v *vault.Vault, paths []string) {
	if ix.docs == nil {
		ix.docs = make(map[string]*docEntry)
	}
	for _, path := range paths {
		if p, ok := v.Page(path); ok {
			ix.docs[path] = buildDocEntry(p)
		} else {
			delete(ix.docs, path)
		}
	}
}

// Len returns the number of pages currently in the index.
func (ix *Index) Len() int {
	return len(ix.docs)
}

// StaleAgainst reports whether ix no longer matches v: a different set of
// pages, or a page whose SHA256 has changed since it was indexed.
func (ix *Index) StaleAgainst(v *vault.Vault) bool {
	pages := v.Pages()
	if len(pages) != len(ix.docs) {
		return true
	}
	for _, p := range pages {
		d, ok := ix.docs[p.Path]
		if !ok || d.SHA256 != p.SHA256() {
			return true
		}
	}
	return false
}

// Search tokenizes q, applies o's filters structurally, scores the
// surviving candidates with BM25 over the three weighted fields, and
// returns the results sorted by Score descending, ties broken by Path
// ascending (backbone §3) — never by map iteration order.
func (ix *Index) Search(q string, o Options) []Hit {
	terms := uniqueSortedTokens(Tokenize(q))
	if len(terms) == 0 {
		return nil
	}

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
			Snippet: buildSnippet(d.Body, rarest),
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
// map iteration order never reaches Search's output.
func (ix *Index) filteredCandidates(o Options) []*docEntry {
	paths := make([]string, 0, len(ix.docs))
	for p := range ix.docs {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var out []*docEntry
	for _, p := range paths {
		d := ix.docs[p]
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
// it at all (in any of the three fields).
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

// buildSnippet extracts Hit.Snippet from body: a window of at most
// snippetMaxRunes runes centred on the first literal occurrence of the
// earliest term (by rarity) that actually appears in body, elided with "…"
// on whichever side was cut. Falls back to the start of body when none of
// the query terms occurs there literally (e.g. every match came from the
// title or tag fields only).
func buildSnippet(body string, orderedTerms []string) string {
	lowerBody := strings.ToLower(body)

	center := 0
	for _, t := range orderedTerms {
		if idx := strings.Index(lowerBody, t); idx >= 0 {
			center = idx
			break
		}
	}
	return runeWindow(body, center)
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

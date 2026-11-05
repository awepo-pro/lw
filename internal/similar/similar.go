// Package similar provides deterministic, embedding-free similarity and
// duplicate detection across wiki pages in a vault.
package similar

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/cite"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/vault"
)

// stopWords holds the built-in English stopword list (~60 words) used to
// filter tokens for similarity scoring.
var stopWords = map[string]bool{
	"about": true, "after": true, "all": true, "also": true,
	"and": true, "any": true, "are": true, "been": true,
	"before": true, "being": true, "between": true, "both": true,
	"but": true, "can": true, "could": true, "did": true,
	"does": true, "each": true, "for": true, "from": true,
	"had": true, "has": true, "have": true, "having": true,
	"her": true, "here": true, "him": true, "his": true,
	"how": true, "into": true, "its": true, "just": true,
	"like": true, "make": true, "many": true, "more": true,
	"most": true, "much": true, "not": true, "now": true,
	"only": true, "other": true, "our": true, "out": true,
	"over": true, "same": true, "should": true, "some": true,
	"such": true, "than": true, "that": true, "the": true,
	"their": true, "them": true, "then": true, "there": true,
	"these": true, "they": true, "this": true, "through": true,
	"under": true, "very": true, "was": true, "were": true,
	"what": true, "when": true, "where": true, "which": true,
	"who": true, "will": true, "with": true, "would": true,
	"you": true, "your": true,
}

// Signals holds the individual similarity components between two pages,
// each in the range [0, 1].
type Signals struct {
	Links   float64 `json:"links"`
	Sources float64 `json:"sources"`
	Tags    float64 `json:"tags"`
	Title   float64 `json:"title"`
	Body    float64 `json:"body"`
	Direct  bool    `json:"direct"`
}

// Shared lists the shared items between two pages for explanation,
// each sorted ascending and capped at 5 elements.
type Shared struct {
	Links   []string `json:"links"`
	Sources []string `json:"sources"`
	Tags    []string `json:"tags"`
}

// Match represents a page related to a query page, ranked by Related score.
type Match struct {
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
	Signals Signals `json:"signals"`
	Shared  Shared  `json:"shared"`
}

// Pair represents a candidate duplicate pair of pages, ranked by Dupe score.
type Pair struct {
	A       string  `json:"a"`
	B       string  `json:"b"`
	ATitle  string  `json:"a_title"`
	BTitle  string  `json:"b_title"`
	Score   float64 `json:"score"`
	Signals Signals `json:"signals"`
	Linked  bool    `json:"linked"`
}

// pageRecord precomputes similarity features for a single page.
type pageRecord struct {
	path        string
	title       string
	neighbors   map[string]bool    // resolved neighbor page paths, excluding self
	sources     map[string]bool    // normalised sources
	tags        map[string]bool    // lowercased tags
	titleTokens map[string]bool    // filtered title tokens
	bodyVec     map[string]float64 // term -> tf * idf
	bodyNorm    float64            // L2 norm of bodyVec
}

// DupeOptions controls duplicate pair generation.
type DupeOptions struct {
	IncludeLinked bool
}

// Corpus holds precomputed features for all pages in a vault.
type Corpus struct {
	pages      []*pageRecord
	byPath     map[string]*pageRecord
	tagWeights map[string]float64
}

// tokenizeAndFilter lowercases, drops tokens shorter than 3 runes,
// and drops stopwords.
func tokenizeAndFilter(text string) []string {
	raw := index.Tokenize(text)
	out := make([]string, 0, len(raw))
	for _, tok := range raw {
		tok = strings.ToLower(tok)
		if utf8.RuneCountInString(tok) < 3 {
			continue
		}
		if stopWords[tok] {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// normaliseSource standardises a source path by trimming whitespace,
// stripping wikilink brackets, dropping leading "./", and ensuring a ".md" suffix.
func normaliseSource(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[[") && strings.HasSuffix(s, "]]") {
		s = strings.TrimPrefix(s, "[[")
		s = strings.TrimSuffix(s, "]]")
		s = strings.TrimSpace(s)
	}
	s = strings.TrimPrefix(s, "[[")
	s = strings.TrimSuffix(s, "]]")
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "./") {
		s = strings.TrimPrefix(s, "./")
	}
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '#'); idx != -1 {
		s = s[:idx]
	}
	if s == "" {
		return ""
	}
	if !strings.HasSuffix(s, ".md") {
		s = s + ".md"
	}
	return s
}

// jaccard calculates the Jaccard similarity of two sets and returns the
// score along with their sorted intersection.
func jaccard(setA, setB map[string]bool) (float64, []string) {
	if len(setA) == 0 && len(setB) == 0 {
		return 0, []string{}
	}
	var shared []string
	small, big := setA, setB
	if len(setB) < len(setA) {
		small, big = setB, setA
	}
	for k := range small {
		if big[k] {
			shared = append(shared, k)
		}
	}
	unionSize := len(setA) + len(setB) - len(shared)
	if unionSize == 0 {
		return 0, []string{}
	}
	sort.Strings(shared)
	if shared == nil {
		shared = []string{}
	}
	return float64(len(shared)) / float64(unionSize), shared
}

// weightedJaccard calculates idf-weighted Jaccard similarity:
// weight(tag) = ln(N / df(tag)) (0 when df == N).
// score = sum of weights of shared tags / sum of weights of the union (0 when the union weight is 0).
func weightedJaccard(setA, setB map[string]bool, weights map[string]float64) (float64, []string) {
	if len(setA) == 0 && len(setB) == 0 {
		return 0, []string{}
	}
	var shared []string
	var sharedWeight float64
	var unionWeight float64

	for k := range setA {
		w := weights[k]
		unionWeight += w
		if setB[k] {
			shared = append(shared, k)
			sharedWeight += w
		}
	}
	for k := range setB {
		if !setA[k] {
			unionWeight += weights[k]
		}
	}

	sort.Strings(shared)
	if shared == nil {
		shared = []string{}
	}

	if unionWeight <= 0 {
		return 0, shared
	}

	score := sharedWeight / unionWeight
	if score < 0 {
		return 0, shared
	}
	if score > 1 {
		return 1, shared
	}
	return score, shared
}

// cosineSimilarity calculates the cosine similarity between two tf-idf vectors.
func cosineSimilarity(vecA, vecB map[string]float64, normA, normB float64) float64 {
	if normA == 0 || normB == 0 {
		return 0
	}
	small, big := vecA, vecB
	if len(vecB) < len(vecA) {
		small, big = vecB, vecA
	}
	var common []string
	for term := range small {
		if _, ok := big[term]; ok {
			common = append(common, term)
		}
	}
	if len(common) == 0 {
		return 0
	}
	sort.Strings(common)
	var dot float64
	for _, term := range common {
		dot += small[term] * big[term]
	}
	sim := dot / (normA * normB)
	if sim < 0 {
		return 0
	}
	if sim > 1 {
		return 1
	}
	return sim
}

// NewCorpus precomputes similarity features across all pages in v:
// resolved neighbor sets, normalised sources, lowercased tags, title token sets,
// and body tf-idf vectors using idf = ln(1 + N/df).
func NewCorpus(v *vault.Vault) *Corpus {
	vaultPages := v.Pages()
	nPages := len(vaultPages)
	c := &Corpus{
		pages:  make([]*pageRecord, 0, nPages),
		byPath: make(map[string]*pageRecord, nPages),
	}
	if nPages == 0 {
		return c
	}

	graph := v.Graph()

	// Phase 1: gather sets, body term frequencies, and document frequencies.
	type rawPage struct {
		path        string
		title       string
		neighbors   map[string]bool
		sources     map[string]bool
		tags        map[string]bool
		titleTokens map[string]bool
		tf          map[string]int
	}

	rawPages := make([]rawPage, 0, nPages)
	df := make(map[string]int)
	tagDF := make(map[string]int)

	for _, p := range vaultPages {
		// Neighbors: resolved outbound + backlinks, excluding self
		nbSlice := graph.Neighbors(p.Path, 1)
		nbMap := make(map[string]bool, len(nbSlice))
		for _, nb := range nbSlice {
			if nb != p.Path {
				nbMap[nb] = true
			}
		}

		// Sources: FM.Sources plus inline citations with Err == ""
		srcMap := make(map[string]bool)
		for _, src := range p.FM.Sources {
			if norm := normaliseSource(src); norm != "" {
				srcMap[norm] = true
			}
		}
		for _, citeItem := range cite.Scan(p.Body) {
			if citeItem.Err == "" {
				if norm := normaliseSource(citeItem.Source); norm != "" {
					srcMap[norm] = true
				}
			}
		}

		// Tags: lowercased
		tagMap := make(map[string]bool, len(p.FM.Tags))
		for _, tag := range p.FM.Tags {
			cleanTag := strings.ToLower(strings.TrimSpace(tag))
			if cleanTag != "" {
				tagMap[cleanTag] = true
			}
		}
		for tag := range tagMap {
			tagDF[tag]++
		}

		// Title tokens
		titleToks := tokenizeAndFilter(p.FM.Title)
		titleMap := make(map[string]bool, len(titleToks))
		for _, tok := range titleToks {
			titleMap[tok] = true
		}

		// Body tokens & TF
		bodyToks := tokenizeAndFilter(p.Body)
		bodyTF := make(map[string]int)
		for _, tok := range bodyToks {
			bodyTF[tok]++
		}
		for term := range bodyTF {
			df[term]++
		}

		rawPages = append(rawPages, rawPage{
			path:        p.Path,
			title:       p.FM.Title,
			neighbors:   nbMap,
			sources:     srcMap,
			tags:        tagMap,
			titleTokens: titleMap,
			tf:          bodyTF,
		})
	}

	// Phase 2: compute tf-idf vectors and norms.
	numPagesFloat := float64(nPages)
	for _, rp := range rawPages {
		vec := make(map[string]float64, len(rp.tf))
		terms := make([]string, 0, len(rp.tf))
		for term := range rp.tf {
			terms = append(terms, term)
		}
		sort.Strings(terms)
		var sumSq float64
		for _, term := range terms {
			count := rp.tf[term]
			idf := math.Log(1.0 + numPagesFloat/float64(df[term]))
			weight := float64(count) * idf
			vec[term] = weight
			sumSq += weight * weight
		}
		rec := &pageRecord{
			path:        rp.path,
			title:       rp.title,
			neighbors:   rp.neighbors,
			sources:     rp.sources,
			tags:        rp.tags,
			titleTokens: rp.titleTokens,
			bodyVec:     vec,
			bodyNorm:    math.Sqrt(sumSq),
		}
		c.pages = append(c.pages, rec)
		c.byPath[rec.path] = rec
	}

	tagWeights := make(map[string]float64, len(tagDF))
	for tag, count := range tagDF {
		if count >= nPages {
			tagWeights[tag] = 0
		} else {
			tagWeights[tag] = math.Log(numPagesFloat / float64(count))
		}
	}
	c.tagWeights = tagWeights

	return c
}

// computeSignals calculates the full Signals breakdown between page A and page B,
// along with the shared items for explanation.
func (c *Corpus) computeSignals(pA, pB *pageRecord) (Signals, Shared) {
	linksSim, sharedLinks := jaccard(pA.neighbors, pB.neighbors)
	sourcesSim, sharedSources := jaccard(pA.sources, pB.sources)
	tagsSim, sharedTags := weightedJaccard(pA.tags, pB.tags, c.tagWeights)
	titleSim, _ := jaccard(pA.titleTokens, pB.titleTokens)
	bodySim := cosineSimilarity(pA.bodyVec, pB.bodyVec, pA.bodyNorm, pB.bodyNorm)
	direct := pA.neighbors[pB.path] || pB.neighbors[pA.path]

	if len(sharedLinks) > 5 {
		sharedLinks = sharedLinks[:5]
	}
	if len(sharedSources) > 5 {
		sharedSources = sharedSources[:5]
	}
	if len(sharedTags) > 5 {
		sharedTags = sharedTags[:5]
	}

	signals := Signals{
		Links:   linksSim,
		Sources: sourcesSim,
		Tags:    tagsSim,
		Title:   titleSim,
		Body:    bodySim,
		Direct:  direct,
	}

	shared := Shared{
		Links:   sharedLinks,
		Sources: sharedSources,
		Tags:    sharedTags,
	}

	return signals, shared
}

// Related finds all other pages in the corpus related to path with Score > 0.05.
// Results are sorted by Score descending, then Path ascending, returning at most n matches.
// A page is never related to itself.
func (c *Corpus) Related(path string, n int) []Match {
	target := c.byPath[path]
	if target == nil {
		return []Match{}
	}

	var matches []Match
	for _, other := range c.pages {
		if other.path == path {
			continue
		}
		signals, shared := c.computeSignals(target, other)

		var directBonus float64
		if signals.Direct {
			directBonus = 0.10
		}
		score := 0.30*signals.Links + 0.20*signals.Sources + 0.15*signals.Tags + 0.15*signals.Title + 0.20*signals.Body + directBonus
		if score > 1.0 {
			score = 1.0
		}
		if score > 0.05 {
			matches = append(matches, Match{
				Path:    other.path,
				Title:   other.title,
				Score:   score,
				Signals: signals,
				Shared:  shared,
			})
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if matches[i].Signals.Direct != matches[j].Signals.Direct {
			return matches[i].Signals.Direct
		}
		return matches[i].Path < matches[j].Path
	})

	if n > 0 && len(matches) > n {
		matches = matches[:n]
	}
	if matches == nil {
		matches = []Match{}
	}
	return matches
}

// Dupes computes near-duplicate page pairs across all unordered pairs (A < B by path)
// whose duplicate score is >= threshold.
// Results are sorted by Score descending, then A ascending, then B ascending, returning at most n pairs.
//
// Dupes runs in O(N^2) comparisons where N is the number of pages. For vaults
// with N > 3000 pages, this executes roughly N*(N-1)/2 vector and set comparisons.
// Precomputed vectors and maps keep each comparison fast, but callers should
// account for quadratic scaling.
func (c *Corpus) Dupes(threshold float64, n int, opts ...DupeOptions) []Pair {
	var opt DupeOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	pairs := make([]Pair, 0)
	nPages := len(c.pages)

	for i := 0; i < nPages; i++ {
		pA := c.pages[i]
		for j := i + 1; j < nPages; j++ {
			pB := c.pages[j]
			signals, _ := c.computeSignals(pA, pB)

			if !opt.IncludeLinked && signals.Direct {
				continue
			}

			maxTB := math.Max(signals.Title, signals.Body)
			sourceTerm := signals.Sources * maxTB
			score := 0.45*signals.Title + 0.35*signals.Body + 0.20*sourceTerm
			if score >= threshold {
				pairs = append(pairs, Pair{
					A:       pA.path,
					B:       pB.path,
					ATitle:  pA.title,
					BTitle:  pB.title,
					Score:   score,
					Signals: signals,
					Linked:  signals.Direct,
				})
			}
		}
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Score != pairs[j].Score {
			return pairs[i].Score > pairs[j].Score
		}
		if pairs[i].A != pairs[j].A {
			return pairs[i].A < pairs[j].A
		}
		return pairs[i].B < pairs[j].B
	})

	if n > 0 && len(pairs) > n {
		pairs = pairs[:n]
	}
	return pairs
}

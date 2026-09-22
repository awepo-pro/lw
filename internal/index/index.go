// Package index is the in-memory inverted word index over a vault: tokenizing
// page bodies, titles and tags, scoring matches with BM25, and persisting the
// result to disk for reuse across runs. It will hold index.go, tokenize.go,
// bm25.go and persist.go.
package index

import (
	"sync/atomic"

	"github.com/awepo-pro/lw/internal/vault"
)

// defaultLimit is what Options.Limit == 0 means.
const defaultLimit = 20

// indexSchema is the current index schema version. 028 (schema 2) stems
// every BM25 field's tokens (stem.go), which silently changes what the
// saved term frequencies mean — a pre-028 file's surface-token index must
// not be reused against a stemmed query side. Version 1 is intentionally
// unused headroom between the unversioned files and 028's stemming schema.
// gobIndex carries the version on disk; pre-028 files have no Schema field
// at all and therefore decode as 0, which never equals indexSchema, so
// StaleAgainst reports them stale and the engine's existing open path
// rebuilds and re-saves them (correction log #4/#5 — no new error path).
const indexSchema = 2

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
	// Abstract is the raw body slice of the page's "## Abstract" section,
	// "" when the page has none (014: fourth weighted field, above title).
	Abstract string

	BodyTermFreq     map[string]int
	TitleTermFreq    map[string]int
	TagTermFreq      map[string]int
	AbstractTermFreq map[string]int
	BodyLen          int
	TitleLen         int
	TagLen           int
	AbstractLen      int
}

// Index is the in-memory inverted word index over a vault's pages.
//
// The docs map lives behind an atomic pointer as an immutable, copy-on-write
// value (008 A-802): Build, Rebuild, Update and GobDecode build a fresh map
// and store it in one step, and Search, Len and StaleAgainst load it once
// per call, so a search concurrent with a rebuild or an update always sees
// one whole map. An entry is never mutated after insertion, which is what
// makes sharing unchanged entries between the old and new maps safe.
type Index struct {
	docs atomic.Pointer[map[string]*docEntry] // keyed by Path
	// schema is the layout version this in-memory index was built or
	// decoded with (indexSchema). Atomic for the same reason docs is: a
	// concurrent StaleAgainst must never catch Rebuild mid-update.
	schema atomic.Int32
}

// Build constructs a fresh Index over every page in v.
func Build(v *vault.Vault) *Index {
	docs := make(map[string]*docEntry)
	for _, p := range v.Pages() {
		docs[p.Path] = buildDocEntry(p)
	}
	ix := &Index{}
	ix.docs.Store(&docs)
	ix.schema.Store(indexSchema)
	return ix
}

// buildDocEntry derives a docEntry from p. It touches nothing but p, so the
// result is identical regardless of whether it is produced by Build or by
// Update.
func buildDocEntry(p *vault.Page) *docEntry {
	// 028: every BM25 field is stemmed after tokenizing (analyze/stemmed),
	// so the query side — stemmed through the same step — meets an
	// identically-stemmed index. The assembler helpers stay surface
	// (backbone §3): bodyTokens applies the wikilink extra-segment rule
	// BEFORE stemming, and the assembled lists pass through stemmed() here.
	body := stemmed(bodyTokens(p))
	title := analyze(p.FM.Title)
	tags := stemmed(tagTokens(p.FM.Tags))
	absTokens, absText := abstractTokens(p)
	absTokens = stemmed(absTokens)

	return &docEntry{
		Path:    p.Path,
		Title:   p.FM.Title,
		Type:    string(p.FM.Type),
		Tags:    append([]string(nil), p.FM.Tags...),
		Created: p.FM.Created,
		Updated: p.FM.Updated,
		SHA256:  p.SHA256(),
		Body:    p.Body,

		BodyTermFreq:     freqMap(body),
		TitleTermFreq:    freqMap(title),
		TagTermFreq:      freqMap(tags),
		AbstractTermFreq: freqMap(absTokens),
		BodyLen:          len(body),
		TitleLen:         len(title),
		TagLen:           len(tags),
		AbstractLen:      len(absTokens),

		Abstract: absText,
	}
}

// abstractTokens returns p's "## Abstract" section text — the first Section
// whose Slug is "abstract" — and its tokens, tokenized with the exact same
// rule bodyTokens applies to p.Body: wikilink spans are excluded from the
// plain-text pass, and each wikilink inside the abstract contributes
// wikilinkTokens(target) plus Tokenize(alias). The abstract is a body
// slice, so the tokens derived from it must equal what the body field
// derives from the same bytes; delegating to bodyTokens over the rebased
// slice is what keeps the two rules from drifting apart (014).
//
// A page with no "## Abstract" section yields ("", nil) and therefore zero
// contribution from the abstract field — today's pre-014 behavior,
// unchanged.
func abstractTokens(p *vault.Page) (tokens []string, text string) {
	for _, sec := range p.Sections {
		if sec.Slug != "abstract" {
			continue
		}
		// Defensive: offsets index into Body per backbone §2.4, but a
		// corrupt section must not corrupt indexing (same stance as
		// bodyTokens' malformed-link guard).
		if sec.Body < 0 || sec.Body > sec.End || sec.End > len(p.Body) {
			return nil, ""
		}
		text = p.Body[sec.Body:sec.End]

		// Rebase the wikilinks fully inside the abstract onto the slice's
		// own coordinates, then let bodyTokens run its exact walk. Links
		// are in ascending Start order (backbone §2.5), which the walk
		// relies on; rebasing preserves that order.
		var links []vault.Wikilink
		for _, l := range p.Links {
			if l.Start >= sec.Body && l.End <= sec.End && l.Start <= l.End {
				links = append(links, vault.Wikilink{
					Target:   l.Target,
					Fragment: l.Fragment,
					Alias:    l.Alias,
					Start:    l.Start - sec.Body,
					End:      l.End - sec.Body,
					Line:     l.Line,
				})
			}
		}
		return bodyTokens(&vault.Page{Body: text, Links: links}), text
	}
	return nil, ""
}

// freqMap counts occurrences of each token.
func freqMap(tokens []string) map[string]int {
	m := make(map[string]int, len(tokens))
	for _, t := range tokens {
		m[t]++
	}
	return m
}

// Rebuild replaces ix's contents with a fresh index over every page in v,
// keeping ix's identity: a pointer to ix captured before the rebuild — the
// agent tool registry holds one for the process lifetime — keeps answering,
// now for v (008 A-801, Engine.ReloadIfChanged). The replacement is one
// atomic store (008 A-802).
func (ix *Index) Rebuild(v *vault.Vault) {
	fresh := Build(v)
	ix.docs.Store(fresh.docs.Load())
	ix.schema.Store(indexSchema)
}

// Update re-indexes the pages named by paths: a path still present in v is
// (re-)built from the vault's current content, a path no longer present in
// v is dropped from the index entirely. It deliberately leaves ix's schema
// alone: the unchanged entries it carries over keep whatever layout they
// were built with, so a schema-old index stays stale (and will be rebuilt
// on the next open) rather than masquerading as current.
//
// Copy-on-write (008 A-802): the unchanged entries are carried over by
// pointer into a fresh map, which is then stored in one step — a concurrent
// Search keeps iterating the map it loaded, never a half-updated one.
func (ix *Index) Update(v *vault.Vault, paths []string) {
	var old map[string]*docEntry
	if p := ix.docs.Load(); p != nil {
		old = *p
	}
	docs := make(map[string]*docEntry, len(old)+len(paths))
	for k, d := range old {
		docs[k] = d
	}
	for _, path := range paths {
		if p, ok := v.Page(path); ok {
			docs[path] = buildDocEntry(p)
		} else {
			delete(docs, path)
		}
	}
	ix.docs.Store(&docs)
}

// Len returns the number of pages currently in the index.
func (ix *Index) Len() int {
	if p := ix.docs.Load(); p != nil {
		return len(*p)
	}
	return 0
}

// StaleAgainst reports whether ix no longer matches v: a schema version
// other than the current one (028: a pre-028 file decodes as 0 and can
// never be current), a different set of pages, or a page whose SHA256 has
// changed since it was indexed.
func (ix *Index) StaleAgainst(v *vault.Vault) bool {
	if ix.schema.Load() != indexSchema {
		return true
	}
	pages := v.Pages()
	var docs map[string]*docEntry
	if p := ix.docs.Load(); p != nil {
		docs = *p
	}
	if len(pages) != len(docs) {
		return true
	}
	for _, p := range pages {
		d, ok := docs[p.Path]
		if !ok || d.SHA256 != p.SHA256() {
			return true
		}
	}
	return false
}

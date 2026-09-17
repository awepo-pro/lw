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
//
// The docs map lives behind an atomic pointer as an immutable, copy-on-write
// value (008 A-802): Build, Rebuild, Update and GobDecode build a fresh map
// and store it in one step, and Search, Len and StaleAgainst load it once
// per call, so a search concurrent with a rebuild or an update always sees
// one whole map. An entry is never mutated after insertion, which is what
// makes sharing unchanged entries between the old and new maps safe.
type Index struct {
	docs atomic.Pointer[map[string]*docEntry] // keyed by Path
}

// Build constructs a fresh Index over every page in v.
func Build(v *vault.Vault) *Index {
	docs := make(map[string]*docEntry)
	for _, p := range v.Pages() {
		docs[p.Path] = buildDocEntry(p)
	}
	ix := &Index{}
	ix.docs.Store(&docs)
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

// Rebuild replaces ix's contents with a fresh index over every page in v,
// keeping ix's identity: a pointer to ix captured before the rebuild — the
// agent tool registry holds one for the process lifetime — keeps answering,
// now for v (008 A-801, Engine.ReloadIfChanged). The replacement is one
// atomic store (008 A-802).
func (ix *Index) Rebuild(v *vault.Vault) {
	fresh := Build(v)
	ix.docs.Store(fresh.docs.Load())
}

// Update re-indexes the pages named by paths: a path still present in v is
// (re-)built from the vault's current content, a path no longer present in
// v is dropped from the index entirely.
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

// StaleAgainst reports whether ix no longer matches v: a different set of
// pages, or a page whose SHA256 has changed since it was indexed.
func (ix *Index) StaleAgainst(v *vault.Vault) bool {
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

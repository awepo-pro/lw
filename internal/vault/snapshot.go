// snapshot.go holds the vault's immutable content snapshot and the
// accessors that read it (008 A-802): Open and Reload build one complete
// snapshot and swap it in behind Vault's atomic pointer with a single
// store, so every accessor below loads the pointer once per call and never
// sees a half-applied reload. A stored snapshot is never mutated; the
// *Schema, *Page, *RawSource and *Graph values it holds are immutable too,
// which is why the values handed out stay valid after any number of
// reloads.
package vault

// snapshot is one complete, immutable view of a vault's content. The
// path-keyed maps serve Page and RawSource lookups; the sorted slices are
// the order Pages and RawSources return, built once per reload so a reader
// never sorts.
type snapshot struct {
	schema      *Schema
	pages       map[string]*Page
	pagesSorted []*Page
	rawSources  map[string]*RawSource
	rawSorted   []*RawSource
	parseErrors []ParseError
	graph       *Graph
}

// Pages returns s's pages sorted by Path — the slice Vault.Pages copies
// from. Package-internal callers iterate it and never retain it.
func (s *snapshot) Pages() []*Page {
	return s.pagesSorted
}

// Schema returns the vault's parsed SCHEMA.md. Each call loads the snapshot
// once; the returned *Schema belongs to that snapshot and never changes.
func (v *Vault) Schema() *Schema {
	if s := v.snap.Load(); s != nil {
		return s.schema
	}
	return nil
}

// Pages returns every successfully parsed wiki page, sorted by Path.
func (v *Vault) Pages() []*Page {
	s := v.snap.Load()
	if s == nil {
		return []*Page{}
	}
	out := make([]*Page, len(s.pagesSorted))
	copy(out, s.pagesSorted)
	return out
}

// Page returns the page at the given vault-relative path, and whether it
// was found.
func (v *Vault) Page(path string) (*Page, bool) {
	s := v.snap.Load()
	if s == nil {
		return nil, false
	}
	p, ok := s.pages[path]
	return p, ok
}

// RawSources returns every successfully parsed raw source, sorted by Path.
func (v *Vault) RawSources() []*RawSource {
	s := v.snap.Load()
	if s == nil {
		return []*RawSource{}
	}
	out := make([]*RawSource, len(s.rawSorted))
	copy(out, s.rawSorted)
	return out
}

// RawSource returns the raw source at the given vault-relative path, and
// whether it was found.
func (v *Vault) RawSource(path string) (*RawSource, bool) {
	s := v.snap.Load()
	if s == nil {
		return nil, false
	}
	r, ok := s.rawSources[path]
	return r, ok
}

// ParseErrors returns one entry per file under wiki/ or raw/ that Open
// could not parse, sorted by Path. A file in ParseErrors is in neither
// Pages() nor RawSources() and contributes no graph edges (backbone §2.8,
// MASTER §9 D-W).
func (v *Vault) ParseErrors() []ParseError {
	s := v.snap.Load()
	if s == nil {
		return nil
	}
	return append([]ParseError(nil), s.parseErrors...)
}

// Graph returns the vault's wikilink graph, built from Pages() only.
func (v *Vault) Graph() *Graph {
	if s := v.snap.Load(); s != nil {
		return s.graph
	}
	return nil
}

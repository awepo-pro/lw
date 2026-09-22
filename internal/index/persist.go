package index

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/awepo-pro/lw/internal/vault"
)

// gobIndex is the exported shape Index (de)serializes through.
// encoding/gob only ever sees exported struct fields; Index itself keeps
// its internals unexported per backbone §3 ("type Index struct { /*
// unexported */ }"), so Index implements gob.GobEncoder/GobDecoder by hand
// and marshals through this shadow struct instead of letting gob reflect
// over Index directly. Docs are stored as a Path-sorted slice, never a
// map, so document order is the same every time for the same index
// content. The file's BYTES are not reproducible: the per-doc
// term-frequency fields are gob maps, and gob walks a map in Go's
// randomized order, so two saves of the same index differ in map-entry
// order (measured, pre-028 behaviour). That is accepted for a disposable
// cache nothing byte-compares; making the bytes reproducible would take a
// wire change and a schema bump, because every format older than this one
// carries map-typed fields that must keep decoding (F.S4).
type gobIndex struct {
	// Schema is the layout version the docs were built with (indexSchema).
	// It is what makes a tokenizer change visible to StaleAgainst: pre-028
	// files have no Schema field on the wire, which decodes as 0 — never
	// equal to indexSchema — so the engine rebuilds them through its
	// existing stale path (028 F.S4).
	Schema int
	Docs   []gobDoc
}

// gobDoc is the exported, gob-encodable mirror of docEntry. It must change
// in lockstep with docEntry — gob encodes exported fields, so a field
// present on docEntry but missing here is silently dropped by Save/Load.
type gobDoc struct {
	Path     string
	Title    string
	Type     string
	Tags     []string
	Created  vault.Date
	Updated  vault.Date
	SHA256   string
	Body     string
	Abstract string

	BodyTermFreq     map[string]int
	TitleTermFreq    map[string]int
	TagTermFreq      map[string]int
	AbstractTermFreq map[string]int
	BodyLen          int
	TitleLen         int
	TagLen           int
	AbstractLen      int

	// Surface is docEntry.SurfaceSet's wire form (A-028-2): the set's tokens
	// as one sorted slice, not a map — a set has no order to preserve, so a
	// sorted slice is its canonical wire form and the same vocabulary always
	// encodes to the same bytes. This field alone does not make the file
	// byte-reproducible; the term-frequency fields above are gob maps (see
	// gobIndex's comment). A schema-2 file has no Surface field at all; it
	// decodes as nil and the schema check rebuilds the index before any
	// query can score against the missing sets.
	Surface []string
}

// GobEncode implements gob.GobEncoder so Save can gob.Encode an *Index
// directly while keeping its fields unexported. It loads the docs map once
// (008 A-802), so the encoded bytes describe one whole map even if a
// concurrent Update lands mid-encode.
func (ix *Index) GobEncode() ([]byte, error) {
	// Schema is loaded BEFORE the docs map. Every writer stores docs first
	// and schema second (Rebuild, GobDecode), so a reader that has seen the
	// current schema can only see docs that schema describes. Loading in the
	// other order let an encode racing a legacy→current rebuild emit a file
	// claiming the current schema over pre-stemming term frequencies — a
	// file StaleAgainst would trust forever, because only the version could
	// have said otherwise (schema_concurrency_test.go).
	schema := int(ix.schema.Load())
	var docs map[string]*docEntry
	if p := ix.docs.Load(); p != nil {
		docs = *p
	}
	paths := make([]string, 0, len(docs))
	for p := range docs {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	g := gobIndex{Schema: schema, Docs: make([]gobDoc, 0, len(paths))}
	for _, p := range paths {
		d := docs[p]
		g.Docs = append(g.Docs, gobDoc{
			Path:     d.Path,
			Title:    d.Title,
			Type:     d.Type,
			Tags:     d.Tags,
			Created:  d.Created,
			Updated:  d.Updated,
			SHA256:   d.SHA256,
			Body:     d.Body,
			Abstract: d.Abstract,

			BodyTermFreq:     d.BodyTermFreq,
			TitleTermFreq:    d.TitleTermFreq,
			TagTermFreq:      d.TagTermFreq,
			AbstractTermFreq: d.AbstractTermFreq,
			BodyLen:          d.BodyLen,
			TitleLen:         d.TitleLen,
			TagLen:           d.TagLen,
			AbstractLen:      d.AbstractLen,

			Surface: sortedSurfaceKeys(d.SurfaceSet),
		})
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(g); err != nil {
		return nil, fmt.Errorf("index: gob encode: %w", err)
	}
	return buf.Bytes(), nil
}

// GobDecode implements gob.GobDecoder, the inverse of GobEncode.
func (ix *Index) GobDecode(data []byte) error {
	var g gobIndex
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&g); err != nil {
		return fmt.Errorf("index: gob decode: %w", err)
	}

	docs := make(map[string]*docEntry, len(g.Docs))
	for _, gd := range g.Docs {
		docs[gd.Path] = &docEntry{
			Path:     gd.Path,
			Title:    gd.Title,
			Type:     gd.Type,
			Tags:     gd.Tags,
			Created:  gd.Created,
			Updated:  gd.Updated,
			SHA256:   gd.SHA256,
			Body:     gd.Body,
			Abstract: gd.Abstract,

			BodyTermFreq:     gd.BodyTermFreq,
			TitleTermFreq:    gd.TitleTermFreq,
			TagTermFreq:      gd.TagTermFreq,
			AbstractTermFreq: gd.AbstractTermFreq,
			BodyLen:          gd.BodyLen,
			TitleLen:         gd.TitleLen,
			TagLen:           gd.TagLen,
			AbstractLen:      gd.AbstractLen,

			SurfaceSet: surfaceSetFrom(gd.Surface),
		}
	}
	ix.docs.Store(&docs) // one store: a concurrent reader sees old or new, never a partial map (008 A-802)
	ix.schema.Store(int32(g.Schema))
	return nil
}

// sortedSurfaceKeys flattens a surface set into its token-sorted slice, the
// wire form gobDoc.Surface carries.
func sortedSurfaceKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for tok := range set {
		out = append(out, tok)
	}
	sort.Strings(out)
	return out
}

// surfaceSetFrom rebuilds a SurfaceSet from its encoded slice. A schema-2
// file has no Surface field, which decodes as an empty slice and therefore
// a nil set — safe for Search (matched 0, factor 1) and irrelevant in
// practice, because the schema check rebuilds such a file before it serves.
func surfaceSetFrom(toks []string) map[string]struct{} {
	if len(toks) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(toks))
	for _, t := range toks {
		set[t] = struct{}{}
	}
	return set
}

// Save writes ix to path via encoding/gob, atomically: it encodes into a
// temp file created in path's own directory, syncs and closes it, then
// renames it over path. This package touches the filesystem for writing
// only here, and only at the caller-supplied path — never inside a vault.
func (ix *Index) Save(path string) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return fmt.Errorf("index: create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if err := gob.NewEncoder(tmp).Encode(ix); err != nil {
		tmp.Close()
		return fmt.Errorf("index: encode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("index: sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("index: close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("index: rename %s to %s: %w", tmpPath, path, err)
	}
	return nil
}

// Load reads an Index previously written by Save.
func Load(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("index: read %s: %w", path, err)
	}

	ix := &Index{}
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(ix); err != nil {
		return nil, fmt.Errorf("index: decode %s: %w", path, err)
	}
	return ix, nil
}

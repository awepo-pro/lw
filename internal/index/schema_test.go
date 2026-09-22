package index

import (
	"bytes"
	"encoding/gob"
	"path/filepath"
	"testing"
)

// TestSchemaVersionStales pins F.S4: the schema version rides the gob file
// (gobIndex.Schema), a freshly built index round-trips it and is not stale,
// and a file written by a pre-028 binary — whose gobIndex had no Schema
// field at all, here simulated by a hand-built gobIndex with Schema 0 —
// loads OK but is stale against the very same, unmodified vault, because
// only the version differs.
func TestSchemaVersionStales(t *testing.T) {
	v := openMinimal(t)

	if Build(v).StaleAgainst(v) {
		t.Fatal("a freshly built index is stale against its own vault")
	}

	path := filepath.Join(t.TempDir(), "index.gob")
	fresh := Build(v)
	if err := fresh.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.StaleAgainst(v) {
		t.Fatal("a freshly built index is stale after Save/Load — the schema did not round-trip")
	}

	// Hand-build the pre-028 wire shape: GobEncode — the exact bytes Save
	// wraps — decodes into the shadow struct, whose schema is confirmed to
	// be the current one, then rewritten with the pre-028 zero value.
	// Every page SHA still matches v — the version is the only thing that
	// can make this index stale.
	blob, err := loaded.GobEncode()
	if err != nil {
		t.Fatalf("GobEncode: %v", err)
	}
	var g gobIndex
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&g); err != nil {
		t.Fatalf("decode gobIndex: %v", err)
	}
	if g.Schema != indexSchema {
		t.Fatalf("saved gobIndex.Schema = %d, want %d", g.Schema, indexSchema)
	}
	g.Schema = 0
	var inner bytes.Buffer
	if err := gob.NewEncoder(&inner).Encode(&g); err != nil {
		t.Fatalf("encode legacy gobIndex: %v", err)
	}

	// Make the pre-028 file the way a pre-028 binary would have: an Index
	// holding the legacy docs and zero schema, written by the real Save.
	// A pre-028 gobIndex had no Schema field, whose encoding is identical
	// to this one with Schema 0 — the zero value gob decodes it to.
	legacy := &Index{}
	if err := legacy.GobDecode(inner.Bytes()); err != nil {
		t.Fatalf("GobDecode legacy: %v", err)
	}
	if err := legacy.Save(path); err != nil {
		t.Fatalf("Save legacy: %v", err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load(pre-028 file): %v", err)
	}
	if !reloaded.StaleAgainst(v) {
		t.Fatal("a Schema-0 index with every page SHA matching must be stale — the tokenizer changed under it")
	}
}

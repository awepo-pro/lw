// stem_schema_test.go pins 028's F.S5 engine claim: an index.gob written by
// a pre-028 binary (whose wire shape had no Schema field, decoded as 0) is
// classified stale by the engine's existing open path — the SHAs all match,
// so only the version can say so — and is rebuilt and re-saved in place on
// first open, with no new error path (correction log #5).
//
// A Save'd file is Index's opaque GobEncoder blob around a gobIndex stream,
// so every read here goes Load → GobEncode (the inner bytes) → decode, and
// the legacy file is produced the way a pre-028 binary produced it: an
// Index holding schema 0 and the real docs, written by the real Save.
package stage

import (
	"bytes"
	"encoding/gob"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/vault"
)

// legacyIndexDoc mirrors index's gobDoc field-for-field, so gob decodes the
// inner stream into it by field name. legacyIndexGob drops the Schema field:
// its encoding decodes into gobIndex as the pre-028 zero.
type legacyIndexGob struct {
	Docs []legacyIndexDoc
}

type legacyIndexDoc struct {
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
}

// indexGobSchema decodes only the schema field of a gobIndex inner stream.
type indexGobSchema struct {
	Schema int
}

// innerIndexBytes returns the gobIndex stream wrapped by the index.gob at
// path: Load, then the Index's own GobEncode.
func innerIndexBytes(t *testing.T, path string) []byte {
	t.Helper()
	ix, err := index.Load(path)
	if err != nil {
		t.Fatalf("Load %s: %v", path, err)
	}
	blob, err := ix.GobEncode()
	if err != nil {
		t.Fatalf("GobEncode %s: %v", path, err)
	}
	return blob
}

func readIndexGobSchema(t *testing.T, path string) int {
	t.Helper()
	var s indexGobSchema
	if err := gob.NewDecoder(bytes.NewReader(innerIndexBytes(t, path))).Decode(&s); err != nil {
		t.Fatalf("decode %s schema: %v", path, err)
	}
	return s.Schema
}

// TestEngineRebuildsOldSchemaIndex: after rewriting the fixture's freshly
// saved index.gob into the pre-028 shape (docs identical, Schema absent —
// i.e. 0) and reopening an engine over the vault, the file on disk carries
// the current schema again: the open path detected it, rebuilt and re-saved.
func TestEngineRebuildsOldSchemaIndex(t *testing.T) {
	// The first engine exists to create and save the fixture's index.gob.
	_, dir := newTestEngine(t)
	indexPath := filepath.Join(dir, ".llmwiki", "index.gob")

	if got := readIndexGobSchema(t, indexPath); got != 2 {
		t.Fatalf("freshly saved index.gob has schema %d, want 2 (test setup is wrong)", got)
	}

	// Harvest the real docs so the legacy file is a fully valid index for
	// this vault — every SHA matches, and only the missing schema can make
	// the reopened engine rebuild it.
	var current legacyIndexGob
	if err := gob.NewDecoder(bytes.NewReader(innerIndexBytes(t, indexPath))).Decode(&current); err != nil {
		t.Fatalf("decode current index docs: %v", err)
	}
	if len(current.Docs) == 0 {
		t.Fatal("harvested no docs from the fixture index (test setup is wrong)")
	}
	var inner bytes.Buffer
	if err := gob.NewEncoder(&inner).Encode(legacyIndexGob{Docs: current.Docs}); err != nil {
		t.Fatalf("encode legacy inner stream: %v", err)
	}
	legacy := &index.Index{}
	if err := legacy.GobDecode(inner.Bytes()); err != nil {
		t.Fatalf("GobDecode legacy stream: %v", err)
	}
	if err := legacy.Save(indexPath); err != nil {
		t.Fatalf("Save legacy: %v", err)
	}
	if got := readIndexGobSchema(t, indexPath); got != 0 {
		t.Fatalf("rewritten index.gob has schema %d, want 0 (the pre-028 simulation leaked the field)", got)
	}

	// Reopen: no page changed, so the SHA check alone would keep the legacy
	// file; the schema check must rebuild and re-save it.
	e2, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("re-OpenEngine over a Schema-0 index: %v", err)
	}
	t.Cleanup(func() { e2.Close() })

	if got := readIndexGobSchema(t, indexPath); got != 2 {
		t.Fatalf("index.gob after reopen has schema %d, want 2 — the engine did not rebuild the pre-028 index", got)
	}
}

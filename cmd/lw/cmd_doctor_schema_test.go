// cmd_doctor_schema_test.go pins A-028-2's doctor truth-telling (F.B4): an
// index.gob written by an older lw — a format version behind the running
// binary's — is reported as exactly that, "written by an older lw (index
// format N, want M)", and never as "stale against the vault", which would
// blame the vault's content for what is a layout change on lw's side. The
// remedy stays the same --rebuild-index it has always been.
package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// olderFormatDoc mirrors index's gobDoc field-for-field as a PREVIOUS lw
// release knew it — without the Surface field A-028-2 added — so gob decodes
// a current file's inner stream into it by field name and an encode of it
// back is byte-honest about what the older binary wrote (the same harvest →
// rewrite technique internal/stage/stem_schema_test.go established).
type olderFormatDoc struct {
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

// olderFormatIndex is the older release's gobIndex shape: Schema carried,
// Docs without Surface.
type olderFormatIndex struct {
	Schema int
	Docs   []olderFormatDoc
}

// rewriteIndexAsOlderFormat rewrites the index.gob at path in the older
// lw's wire shape: the fixture's real docs (every SHA still matches the
// vault, harvested from a freshly saved index) with the format version
// pinned to 2 — T1's stemming-only layout, one behind SchemaVersion. The
// file is written the way the older binary wrote it: an Index envelope
// (Save → GobEncode) around the older gobIndex stream.
func rewriteIndexAsOlderFormat(t *testing.T, path string) {
	t.Helper()
	ix, err := index.Load(path)
	if err != nil {
		t.Fatalf("Load fresh index: %v", err)
	}
	blob, err := ix.GobEncode()
	if err != nil {
		t.Fatalf("GobEncode fresh index: %v", err)
	}
	var current olderFormatIndex
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&current); err != nil {
		t.Fatalf("harvest docs: %v", err)
	}
	if len(current.Docs) == 0 {
		t.Fatal("harvested no docs from the fresh index (test setup is wrong)")
	}
	current.Schema = 2
	var inner bytes.Buffer
	if err := gob.NewEncoder(&inner).Encode(&current); err != nil {
		t.Fatalf("encode older-format index: %v", err)
	}
	older := &index.Index{}
	if err := older.GobDecode(inner.Bytes()); err != nil {
		t.Fatalf("GobDecode older-format stream: %v", err)
	}
	if err := older.Save(path); err != nil {
		t.Fatalf("Save older-format index: %v", err)
	}
}

func TestDoctorIndexFormatMismatchSaysOlderLW(t *testing.T) {
	doctorTestEnv(t)
	root := testutil.CopyFixture(t, "minimal")
	indexPath := filepath.Join(root, stateDirName, indexFileName)

	// Setup sanity: a freshly saved index is current and healthy, so the
	// failure below can only come from the format version.
	if err := os.MkdirAll(filepath.Join(root, stateDirName), 0o755); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	if err := index.Build(openVaultOnly(t, root)).Save(indexPath); err != nil {
		t.Fatalf("Save fresh index: %v", err)
	}
	if c := checkIndex(root, openVaultOnly(t, root)); !c.OK {
		t.Fatalf("freshly built index reported a failure — setup is wrong: %+v", c)
	}

	rewriteIndexAsOlderFormat(t, indexPath)

	c := checkIndex(root, openVaultOnly(t, root))
	want := fmt.Sprintf("written by an older lw (index format 2, want %d)", index.SchemaVersion)
	if c.OK {
		t.Fatalf("check reported OK, want the format-mismatch failure: %+v", c)
	}
	if !strings.Contains(c.Detail, want) {
		t.Errorf("detail = %q, want it to say %q", c.Detail, want)
	}
	if strings.Contains(c.Detail, "stale against the vault") {
		t.Errorf("detail = %q — a format mismatch must not be worded as vault staleness", c.Detail)
	}
	// The remedy is unchanged: the same rebuild fixes a version mismatch.
	wantFailed(t, c, "lw doctor --rebuild-index")
}

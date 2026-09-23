package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

// originalExtractor serves one fixed Doc — the 033 seam under test: the
// extractor chain (PDF sidecar) is what sets Doc.Original; the agent's
// tool arguments never carry it (backbone invariant 1).
type originalExtractor struct{ doc *extract.Doc }

func (f originalExtractor) CanHandle(string) bool                                 { return true }
func (f originalExtractor) Extract(context.Context, string) (*extract.Doc, error) { return f.doc, nil }

// TestIngestSourceCarriesOriginal drives stage.ingest_source with an
// extractor whose Doc.Original names a real file: the staged op must carry
// OriginalPath beside the raw md (same slug, lowercased extension), the
// original's sha, and the bytes themselves in the CAS channel — and the
// proposed raw md's frontmatter must declare the same pair. None of it
// comes from the model's arguments.
func TestIngestSourceCarriesOriginal(t *testing.T) {
	blob := make([]byte, 3072)
	copy(blob, "%PDF-1.6 fake pdf for TestIngestSourceCarriesOriginal\n")
	for i := range blob {
		blob[i] = byte('a' + i%26)
	}
	copy(blob, "%PDF-1.6 fake pdf for TestIngestSourceCarriesOriginal\n")
	sum := sha256.Sum256(blob)
	wantSHA := hex.EncodeToString(sum[:])

	pdf := filepath.Join(t.TempDir(), "quaternion-introduction.PDF")
	if err := os.WriteFile(pdf, blob, 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}

	doc := &extract.Doc{
		Title:     "Quaternion Introduction",
		SourceURL: "https://example.test/quaternion",
		Markdown:  "# Quaternion Introduction\n\nThe extracted body.\n",
		Kind:      "paper",
		Extractor: "docling/pdf",
		Original:  pdf,
	}
	reg, e, _ := engineRegistry(t, originalExtractor{doc: doc})

	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest a pdf with its original"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}
	r, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"`+pdf+`","kind":"paper"}`))
	if err != nil {
		t.Fatalf("stage.ingest_source: %v", err)
	}
	if r.IsError {
		t.Fatalf("ingest rejected: %s", r.Content)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 1 {
		t.Fatalf("staged %d ops, want 1", len(cs.Ops))
	}
	op := cs.Ops[0]

	// The original travels beside the raw md, extension lowercased.
	if want := "raw/papers/quaternion-introduction.pdf"; op.OriginalPath != want {
		t.Errorf("op.OriginalPath = %q, want %q", op.OriginalPath, want)
	}
	if op.Original != wantSHA {
		t.Errorf("op.Original = %q, want the original's sha256 %q", op.Original, wantSHA)
	}
	// Append has already cleared the op's byte channel (D-AY: bytes in, sha
	// out) — so the bytes must be recoverable from the CAS under that sha.
	store, err := stage.OpenStore(filepath.Join(e.Vault().Root(), ".llmwiki", "objects"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	got, err := store.Get(op.Original)
	if err != nil {
		t.Fatalf("CAS Get(%s): %v", op.Original, err)
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("CAS holds %d bytes under the original's sha, want the file's %d bytes", len(got), len(blob))
	}
	if !strings.HasSuffix(op.Path, ".md") || strings.TrimSuffix(op.Path, ".md") != strings.TrimSuffix(op.OriginalPath, ".pdf") {
		t.Errorf("op.Path %q and op.OriginalPath %q are not a md/pdf pair", op.Path, op.OriginalPath)
	}

	// The staged raw md declares the same pair, in its frontmatter.
	proposed := proposedRawDiff(t, e).New
	rs, err := vault.ParseRawSource(op.Path, []byte(proposed))
	if err != nil {
		t.Fatalf("ParseRawSource: %v", err)
	}
	if rs.Original != op.OriginalPath {
		t.Errorf("frontmatter original = %q, want %q", rs.Original, op.OriginalPath)
	}
	if rs.OriginalSHA256 != wantSHA {
		t.Errorf("frontmatter original_sha256 = %q, want %q", rs.OriginalSHA256, wantSHA)
	}
	if body := strings.TrimLeft(doc.Markdown, "\n"); rs.Body != normalizeToolBody(body) {
		t.Errorf("the extracted body changed behind the frontmatter")
	}
}

// TestIngestSourceOriginalUnreadableIsRecoverable: a Doc.Original naming a
// file the tool cannot read is an IsError result the model can act on —
// never a staged op with a dangling original, and never a turn abort.
func TestIngestSourceOriginalUnreadableIsRecoverable(t *testing.T) {
	doc := &extract.Doc{
		Title:     "Ghost Original",
		SourceURL: "https://example.test/ghost",
		Markdown:  "# Ghost Original\n\nBody.\n",
		Kind:      "paper",
		Extractor: "docling/pdf",
		Original:  filepath.Join(t.TempDir(), "gone.pdf"),
	}
	reg, e, _ := engineRegistry(t, originalExtractor{doc: doc})

	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest a ghost"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}
	r, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"ghost.md","kind":"paper"}`))
	if err != nil {
		t.Fatalf("stage.ingest_source returned a Go error: %v", err)
	}
	if !r.IsError {
		t.Fatalf("ingest with an unreadable original succeeded: %+v", r)
	}
	if !strings.Contains(r.Content, "ghost.pdf") && !strings.Contains(r.Content, "original") {
		t.Errorf("result = %q, want it to name the unreadable original", r.Content)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 0 {
		t.Errorf("a refused ingest staged %d ops", len(cs.Ops))
	}
	if _, err := os.Stat(filepath.Join(e.Vault().Root(), "raw", "papers", "ghost-original.pdf")); !os.IsNotExist(err) {
		t.Errorf("a file appeared in the vault from a refused ingest (err = %v)", err)
	}
}

// TestIngestSourceWithoutOriginalHasNoOriginalFields pins the
// original-less path staying exactly as it was: no OriginalPath, no
// Original, and no original keys in the frontmatter.
func TestIngestSourceWithoutOriginalHasNoOriginalFields(t *testing.T) {
	doc := &extract.Doc{
		Title:     "Plain HTML Source",
		SourceURL: "https://example.test/plain",
		Markdown:  "# Plain HTML Source\n\nBody.\n",
		Kind:      "article",
		Extractor: "go/html",
	}
	reg, e, _ := engineRegistry(t, originalExtractor{doc: doc})

	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest plain"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}
	r, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"plain.html","kind":"article"}`))
	if err != nil || r.IsError {
		t.Fatalf("stage.ingest_source: %+v %v", r, err)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	op := cs.Ops[0]
	if op.OriginalPath != "" || op.Original != "" {
		t.Errorf("original-less ingest staged original fields: %q / %q", op.OriginalPath, op.Original)
	}
	proposed := proposedRawDiff(t, e).New
	if strings.Contains(proposed, "original") {
		t.Errorf("original-less proposal carries an original key:\n%q", proposed)
	}
}

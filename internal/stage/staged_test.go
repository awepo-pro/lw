package stage

import (
	"errors"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// TestStagedFileIngestSource pins the S6-C121 fix: immediately after
// Append(ingest_source) — before Commit — StagedFile returns the exact
// bytes that were proposed.
func TestStagedFileIngestSource(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("ingest", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	body := "# X\n\nBody.\n"
	content := []byte("---\nsource_url: https://example.com/x\ningested: 2026-08-29\nsha256: " +
		vault.BodySHA256(body) + "\n---\n\n" + body)
	_, err := e.Append(Op{
		Kind:      OpIngestSource,
		Path:      "raw/articles/staged-file.md",
		Extractor: "go/html",
		Content:   content,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, ok, err := e.StagedFile("raw/articles/staged-file.md")
	if err != nil {
		t.Fatalf("StagedFile: %v", err)
	}
	if !ok {
		t.Fatal("StagedFile ok = false, want true")
	}
	if string(got) != string(content) {
		t.Fatalf("StagedFile bytes = %q, want %q", got, content)
	}
}

// TestStagedFileCreatePage pins the create_page half of the contract.
func TestStagedFileCreatePage(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("create", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	content := newConceptPageContent("Staged Concept")
	_, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/staged-concept.md",
		Content:    content,
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, ok, err := e.StagedFile("wiki/concepts/staged-concept.md")
	if err != nil {
		t.Fatalf("StagedFile: %v", err)
	}
	if !ok {
		t.Fatal("StagedFile ok = false, want true")
	}
	if string(got) != string(content) {
		t.Fatalf("StagedFile bytes differ from the proposed content")
	}
}

// TestStagedFileNoOpenChangeset pins "no changeset -> (nil, false, nil)",
// never an error a tool handler would have to special-case.
func TestStagedFileNoOpenChangeset(t *testing.T) {
	e, _ := newTestEngine(t)

	got, ok, err := e.StagedFile("raw/articles/anything.md")
	if err != nil {
		t.Fatalf("StagedFile: %v", err)
	}
	if ok {
		t.Fatal("StagedFile ok = true, want false with no open changeset")
	}
	if got != nil {
		t.Fatalf("StagedFile bytes = %v, want nil", got)
	}
}

// TestStagedFileUnrelatedPath pins "no matching op -> (nil, false, nil)".
func TestStagedFileUnrelatedPath(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("ingest", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:      OpIngestSource,
		Path:      "raw/articles/staged-file.md",
		Extractor: "go/html",
		Content:   []byte("body\n"),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, ok, err := e.StagedFile("raw/articles/some-other-path.md")
	if err != nil {
		t.Fatalf("StagedFile: %v", err)
	}
	if ok {
		t.Fatal("StagedFile ok = true, want false for an unrelated path")
	}
	if got != nil {
		t.Fatalf("StagedFile bytes = %v, want nil", got)
	}
}

// TestStagedFileDroppedOp pins "dropped op -> false": DropOp excludes the
// op from Changeset.Live(), so StagedFile must stop seeing its path.
func TestStagedFileDroppedOp(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("ingest", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	id, err := e.Append(Op{
		Kind:      OpIngestSource,
		Path:      "raw/articles/staged-file.md",
		Extractor: "go/html",
		Content:   []byte("body\n"),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, ok, err := e.StagedFile("raw/articles/staged-file.md"); err != nil || !ok {
		t.Fatalf("StagedFile before drop: ok=%v err=%v, want ok=true", ok, err)
	}

	if err := e.DropOp(id); err != nil {
		t.Fatalf("DropOp: %v", err)
	}

	got, ok, err := e.StagedFile("raw/articles/staged-file.md")
	if err != nil {
		t.Fatalf("StagedFile after drop: %v", err)
	}
	if ok {
		t.Fatal("StagedFile ok = true after DropOp, want false")
	}
	if got != nil {
		t.Fatalf("StagedFile bytes = %v after drop, want nil", got)
	}
}

// TestStagedFileAfterCommit pins "after Commit -> false (no open
// changeset)": Commit moves the changeset out of changesets/open/, so
// Current (and therefore StagedFile) sees ErrNoChangeset again.
func TestStagedFileAfterCommit(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("ingest", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:      OpIngestSource,
		Path:      "raw/articles/staged-file.md",
		Extractor: "go/html",
		Content:   []byte("body\n"),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, err := e.Commit("commit staged file"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, ok, err := e.StagedFile("raw/articles/staged-file.md")
	if err != nil {
		t.Fatalf("StagedFile after commit: %v", err)
	}
	if ok {
		t.Fatal("StagedFile ok = true after Commit, want false")
	}
	if got != nil {
		t.Fatalf("StagedFile bytes = %v after commit, want nil", got)
	}
}

// TestStagedFileRealEngineErrorPropagates pins "real CAS/engine read
// failure -> error": an op whose After sha the store does not hold (an
// impossible state through the public API, forced here to prove the error
// path is wired) must surface as an error, not a silent false.
func TestStagedFileRealEngineErrorPropagates(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("ingest", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	id, err := e.Append(Op{
		Kind:      OpIngestSource,
		Path:      "raw/articles/staged-file.md",
		Extractor: "go/html",
		Content:   []byte("body\n"),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok := c.Op(id)
	if !ok {
		t.Fatalf("op %s not found", id)
	}
	op.After = strings.Repeat("0", 64)
	op.SHA256 = op.After
	e.open = c

	_, _, err = e.StagedFile("raw/articles/staged-file.md")
	if err == nil {
		t.Fatal("StagedFile err = nil, want a Store.Get error for a sha the CAS does not hold")
	}
	if errors.Is(err, ErrNoChangeset) {
		t.Fatalf("StagedFile err = %v, want a CAS error, not ErrNoChangeset", err)
	}
}

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

type customFakeExtractor struct {
	doc *extract.Doc
}

func (f customFakeExtractor) CanHandle(string) bool { return true }
func (f customFakeExtractor) Extract(context.Context, string) (*extract.Doc, error) {
	return f.doc, nil
}

// helper to set up a test vault directory with given raw and wiki files.
func setupTestVault(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	schema := "# SCHEMA\n\n## Domain\nTest domain.\n\n## Tags\n- `test`\n"
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// T1 stage.ingest_source detection.
func TestT1_StageIngestSourceDetection(t *testing.T) {
	bodyA := "# X\n\nOriginal body A.\n"
	bodyB := "# X\n\nEdited body B.\n"
	shaA := vault.BodySHA256(bodyA)

	rawX := "---\nsource_url: /n/x.md\ningested: 2026-10-01\nsha256: " + shaA + "\n---\n\n" + bodyA

	// Case 1: Page cites raw/articles/x.md
	t.Run("with citing page", func(t *testing.T) {
		pageP := "---\ntype: concept\ntitle: Concept P\ntags: [test]\nconfidence: high\nsources:\n  - raw/articles/x.md\n---\n\n# Concept P\n\nClaims about X ^[raw/articles/x.md].\n"
		dir := setupTestVault(t, map[string]string{
			"raw/articles/x.md":  rawX,
			"wiki/concepts/p.md": pageP,
		})

		e, err := stage.OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()

		doc := &extract.Doc{
			Title:     "X",
			SourceURL: "/n/x.md",
			Markdown:  bodyB,
			Kind:      "article",
		}
		reg := NewRegistry(Deps{
			Vault:   e.Vault(),
			Index:   index.Build(e.Vault()),
			Engine:  e,
			Extract: customFakeExtractor{doc: doc},
			Author:  stage.Author{Kind: "agent", Model: "test"},
		})

		ctx := context.Background()
		if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest revision"}`)); err != nil || r.IsError {
			t.Fatalf("stage.open: %+v %v", r, err)
		}

		res, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"/n/x.md","kind":"article"}`))
		if err != nil {
			t.Fatalf("stage.ingest_source err: %v", err)
		}
		if res.IsError {
			t.Fatalf("stage.ingest_source returned error: %s", res.Content)
		}

		// Op staged at raw/articles/x-2.md
		d, err := e.Diff()
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		var stagedOp *stage.FileDiff
		for i := range d.Files {
			if d.Files[i].Path == "raw/articles/x-2.md" {
				stagedOp = &d.Files[i]
				break
			}
		}
		if stagedOp == nil {
			t.Fatalf("no staged op for raw/articles/x-2.md; diff files: %+v", d.Files)
		}

		// Content starts with frontmatter containing supersedes: raw/articles/x.md and source_url: /n/x.md
		if !strings.Contains(stagedOp.New, "supersedes: raw/articles/x.md\n") {
			t.Errorf("staged op content missing supersedes: raw/articles/x.md\ncontent:\n%s", stagedOp.New)
		}
		if !strings.Contains(stagedOp.New, "source_url: /n/x.md\n") {
			t.Errorf("staged op content missing source_url: /n/x.md\ncontent:\n%s", stagedOp.New)
		}

		// Result text contains revises raw/articles/x.md and pages citing it: wiki/concepts/p.md
		if !strings.Contains(res.Content, "revises raw/articles/x.md") {
			t.Errorf("res.Content does not contain 'revises raw/articles/x.md': %q", res.Content)
		}
		if !strings.Contains(res.Content, "pages citing it: wiki/concepts/p.md") {
			t.Errorf("res.Content does not contain 'pages citing it: wiki/concepts/p.md': %q", res.Content)
		}

		// Old raw file is untouched on disk
		oldDiskBytes, err := os.ReadFile(filepath.Join(dir, "raw/articles/x.md"))
		if err != nil {
			t.Fatalf("ReadFile old raw: %v", err)
		}
		if string(oldDiskBytes) != rawX {
			t.Errorf("old raw file was modified on disk!")
		}
	})

	// Case 2: No page cites raw/articles/x.md
	t.Run("no citing pages", func(t *testing.T) {
		dir := setupTestVault(t, map[string]string{
			"raw/articles/x.md": rawX,
		})

		e, err := stage.OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()

		doc := &extract.Doc{
			Title:     "X",
			SourceURL: "/n/x.md",
			Markdown:  bodyB,
			Kind:      "article",
		}
		reg := NewRegistry(Deps{
			Vault:   e.Vault(),
			Index:   index.Build(e.Vault()),
			Engine:  e,
			Extract: customFakeExtractor{doc: doc},
			Author:  stage.Author{Kind: "agent", Model: "test"},
		})

		ctx := context.Background()
		if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest revision"}`)); err != nil || r.IsError {
			t.Fatalf("stage.open: %+v %v", r, err)
		}

		res, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"/n/x.md","kind":"article"}`))
		if err != nil {
			t.Fatalf("stage.ingest_source err: %v", err)
		}
		if res.IsError {
			t.Fatalf("stage.ingest_source returned error: %s", res.Content)
		}

		if !strings.Contains(res.Content, "revises raw/articles/x.md") {
			t.Errorf("res.Content does not contain 'revises raw/articles/x.md': %q", res.Content)
		}
		if !strings.Contains(res.Content, "no page cites it") {
			t.Errorf("res.Content does not contain 'no page cites it': %q", res.Content)
		}
	})
}

// T2 no false link.
func TestT2_NoFalseLink(t *testing.T) {
	bodyA := "# Title X\n\nOriginal body A.\n"
	shaA := vault.BodySHA256(bodyA)
	rawX := "---\nsource_url: /n/x.md\ningested: 2026-10-01\nsha256: " + shaA + "\n---\n\n" + bodyA

	// Subtest 1: Same body as committed raw -> still refused (already ingested, dedupe unchanged).
	t.Run("same body refused", func(t *testing.T) {
		dir := setupTestVault(t, map[string]string{
			"raw/articles/x.md": rawX,
		})
		e, err := stage.OpenEngine(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()

		doc := &extract.Doc{
			Title:     "Title X",
			SourceURL: "/n/x.md",
			Markdown:  bodyA, // same body
			Kind:      "article",
		}
		reg := NewRegistry(Deps{
			Vault:   e.Vault(),
			Index:   index.Build(e.Vault()),
			Engine:  e,
			Extract: customFakeExtractor{doc: doc},
			Author:  stage.Author{Kind: "agent", Model: "test"},
		})

		ctx := context.Background()
		_ = proposeOpen(ctx, reg)
		res, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"/n/x.md","kind":"article"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("expected error for same body, got success: %s", res.Content)
		}
		if !strings.Contains(res.Content, "already ingested") {
			t.Errorf("error %q does not contain 'already ingested'", res.Content)
		}
	})

	// Subtest 2: Different source_url with similar title -> no supersedes:, result text has no revises.
	t.Run("different source_url no link", func(t *testing.T) {
		dir := setupTestVault(t, map[string]string{
			"raw/articles/x.md": rawX,
		})
		e, err := stage.OpenEngine(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()

		bodyOther := "# Title X\n\nDifferent source with same title.\n"
		doc := &extract.Doc{
			Title:     "Title X",
			SourceURL: "/n/unrelated.md",
			Markdown:  bodyOther,
			Kind:      "article",
		}
		reg := NewRegistry(Deps{
			Vault:   e.Vault(),
			Index:   index.Build(e.Vault()),
			Engine:  e,
			Extract: customFakeExtractor{doc: doc},
			Author:  stage.Author{Kind: "agent", Model: "test"},
		})

		ctx := context.Background()
		_ = proposeOpen(ctx, reg)
		res, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"/n/unrelated.md","kind":"article"}`))
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("unexpected error: %s", res.Content)
		}
		if strings.Contains(res.Content, "revises") {
			t.Errorf("res.Content unexpectedly contains 'revises': %s", res.Content)
		}
		d, err := e.Diff()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range d.Files {
			if strings.Contains(f.New, "supersedes:") {
				t.Errorf("op %s contains supersedes: when source_url differed", f.Path)
			}
		}
	})

	// Subtest 3: Relative uri x.md whose absolute form equals a committed raw's source_url -> detected.
	t.Run("relative uri matching absolute source_url detected", func(t *testing.T) {
		absCurrent, err := filepath.Abs("relative-target.md")
		if err != nil {
			t.Fatal(err)
		}
		committedRaw := "---\nsource_url: " + absCurrent + "\ningested: 2026-10-01\nsha256: " + shaA + "\n---\n\n" + bodyA

		dir := setupTestVault(t, map[string]string{
			"raw/articles/relative-target.md": committedRaw,
		})
		e, err := stage.OpenEngine(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()

		editedBody := "# Title Target\n\nEdited body.\n"
		doc := &extract.Doc{
			Title:     "Relative Target",
			SourceURL: "relative-target.md", // relative source_url
			Markdown:  editedBody,
			Kind:      "article",
		}
		reg := NewRegistry(Deps{
			Vault:   e.Vault(),
			Index:   index.Build(e.Vault()),
			Engine:  e,
			Extract: customFakeExtractor{doc: doc},
			Author:  stage.Author{Kind: "agent", Model: "test"},
		})

		ctx := context.Background()
		_ = proposeOpen(ctx, reg)
		res, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"relative-target.md","kind":"article"}`))
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("unexpected error: %s", res.Content)
		}
		if !strings.Contains(res.Content, "revises raw/articles/relative-target.md") {
			t.Errorf("res.Content does not report revises raw/articles/relative-target.md: %s", res.Content)
		}
		d, err := e.Diff()
		if err != nil {
			t.Fatal(err)
		}
		foundSupersedes := false
		for _, f := range d.Files {
			if strings.Contains(f.New, "supersedes: raw/articles/relative-target.md") {
				foundSupersedes = true
				// Stored source_url stays what it is today (relative)
				if !strings.Contains(f.New, "source_url: relative-target.md\n") {
					t.Errorf("stored source_url should stay relative 'relative-target.md':\n%s", f.New)
				}
			}
		}
		if !foundSupersedes {
			t.Errorf("diff files did not contain supersedes: raw/articles/relative-target.md")
		}
	})

	// Subtest 4: Committed raw with a RELATIVE stored source_url -> never matched.
	t.Run("committed relative source_url never matched", func(t *testing.T) {
		rawRel := "---\nsource_url: relative.md\ningested: 2026-10-01\nsha256: " + shaA + "\n---\n\n" + bodyA
		dir := setupTestVault(t, map[string]string{
			"raw/articles/relative.md": rawRel,
		})
		e, err := stage.OpenEngine(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()

		editedBody := "# Title Relative\n\nEdited body.\n"
		doc := &extract.Doc{
			Title:     "Relative",
			SourceURL: "relative.md",
			Markdown:  editedBody,
			Kind:      "article",
		}
		reg := NewRegistry(Deps{
			Vault:   e.Vault(),
			Index:   index.Build(e.Vault()),
			Engine:  e,
			Extract: customFakeExtractor{doc: doc},
			Author:  stage.Author{Kind: "agent", Model: "test"},
		})

		ctx := context.Background()
		_ = proposeOpen(ctx, reg)
		res, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"relative.md","kind":"article"}`))
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("unexpected error: %s", res.Content)
		}
		if strings.Contains(res.Content, "revises") {
			t.Errorf("committed relative source_url must never match, but got revises: %s", res.Content)
		}
	})
}

// T3 chain. Vault R1, R2(supersedes R1), edited ingest of the same url -> new raw supersedes: R2 (the tip), never R1.
func TestT3_Chain(t *testing.T) {
	url := "https://example.com/chain"
	body1 := "# Chain\n\nBody 1.\n"
	body2 := "# Chain\n\nBody 2.\n"
	body3 := "# Chain\n\nBody 3.\n"

	raw1 := "---\nsource_url: " + url + "\ningested: 2026-10-01\nsha256: " + vault.BodySHA256(body1) + "\n---\n\n" + body1
	raw2 := "---\nsource_url: " + url + "\ningested: 2026-10-02\nsha256: " + vault.BodySHA256(body2) + "\nsupersedes: raw/articles/chain.md\n---\n\n" + body2

	dir := setupTestVault(t, map[string]string{
		"raw/articles/chain.md":   raw1,
		"raw/articles/chain-2.md": raw2,
	})

	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	doc := &extract.Doc{
		Title:     "Chain",
		SourceURL: url,
		Markdown:  body3,
		Kind:      "article",
	}
	reg := NewRegistry(Deps{
		Vault:   e.Vault(),
		Index:   index.Build(e.Vault()),
		Engine:  e,
		Extract: customFakeExtractor{doc: doc},
		Author:  stage.Author{Kind: "agent", Model: "test"},
	})

	ctx := context.Background()
	_ = proposeOpen(ctx, reg)
	res, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(`{"uri":"`+url+`","kind":"article"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}

	if !strings.Contains(res.Content, "revises raw/articles/chain-2.md") {
		t.Errorf("res.Content does not name tip R2: %s", res.Content)
	}
	if strings.Contains(res.Content, "revises raw/articles/chain.md") {
		t.Errorf("res.Content unexpectedly named R1 instead of tip R2: %s", res.Content)
	}

	d, err := e.Diff()
	if err != nil {
		t.Fatal(err)
	}
	var newRawContent string
	for _, f := range d.Files {
		if f.Path == "raw/articles/chain-3.md" {
			newRawContent = f.New
			break
		}
	}
	if newRawContent == "" {
		t.Fatalf("raw/articles/chain-3.md not staged; diff files: %+v", d.Files)
	}
	if !strings.Contains(newRawContent, "supersedes: raw/articles/chain-2.md\n") {
		t.Errorf("new raw supersedes must be raw/articles/chain-2.md, got:\n%s", newRawContent)
	}
}

// T4 defect check. rawDocumentDefect returns non-empty when the built file parses back with a different Supersedes
// (unit test builds a file whose supersedes: value contains  #), and "" for a normal one.
func TestT4_DefectCheck(t *testing.T) {
	d, _ := vault.ParseDate("2026-10-29")
	body := "# Test\n\nBody.\n"

	// Normal one -> returns ""
	normalDoc := rawSourceDocumentFull("https://example.com/test", d, body, "", "", "raw/articles/old.md")
	defectNormal := rawDocumentDefect("https://example.com/test", body, "", "", "raw/articles/old.md", normalDoc)
	if defectNormal != "" {
		t.Errorf("defectNormal = %q, want empty", defectNormal)
	}

	// Broken one with " #" in supersedes
	badSupersedes := "raw/articles/old.md # comment"
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.WriteString("source_url: https://example.com/test\n")
	buf.WriteString("ingested: 2026-10-29\n")
	buf.WriteString("sha256: " + vault.BodySHA256(body) + "\n")
	buf.WriteString("supersedes: " + badSupersedes + "\n")
	buf.WriteString("---\n\n")
	buf.WriteString(body)

	defectBad := rawDocumentDefect("https://example.com/test", body, "", "", badSupersedes, buf.Bytes())
	if defectBad == "" {
		t.Errorf("defectBad = \"\", want non-empty defect error when supersedes contains ' #'")
	}
}

// T5 byte identity. With no revision, the proposal bytes and the result text are identical to v2.55.0 for the existing ingest
// fixtures (the existing stage_source tests pass unmodified).
func TestT5_ByteIdentity(t *testing.T) {
	doc := ingestDoc{
		Title:     "New Paper",
		SourceURL: "https://example.test/brand-new-paper",
		Markdown:  "# New Paper\n\nA fresh body with no revision.\n",
		Kind:      "paper",
	}
	reg, e := ingestRegistry(t, doc)
	r := proposeIngest(t, reg, "new-paper.md")
	if r.IsError {
		t.Fatalf("ingest rejected: %s", r.Content)
	}

	if strings.Contains(r.Content, "revises") {
		t.Errorf("result text for non-revision contains 'revises': %s", r.Content)
	}

	proposed := proposedRawDiff(t, e).New
	if strings.Contains(proposed, "supersedes:") {
		t.Errorf("proposed raw for non-revision contains 'supersedes:': %s", proposed)
	}
}

func proposeOpen(ctx context.Context, reg *Registry) Result {
	r, _ := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"test"}`))
	return r
}

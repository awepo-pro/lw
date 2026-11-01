package tools

// stage_source_notes_074_test.go pins F11 of workflow 074 subtask N:
// a source originating inside <vault>/notes/ stages under raw/notes/
// instead of raw/articles/ (decision D-5: placement by origin, not by a new kind).
// Prevents the 047 notes/ directory leakage into raw/articles/.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestNotes074_SchemaPinned pins the stage.ingest_source schema byte-for-byte against v2.62.1.
// Subtask N must not change the schema string or enum (I6, D-5).
func TestNotes074_SchemaPinned(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	const wantSchema = "{\n  \"type\":\"object\",\n  \"properties\":{\"uri\":{\"type\":\"string\"},\"kind\":{\"type\":\"string\",\"enum\":[\"article\",\"paper\",\"transcript\"]},\"name\":{\"type\":\"string\"}},\n  \"required\":[\"uri\"],\"additionalProperties\":false\n}"

	if stageIngestSourceSchema != wantSchema {
		t.Fatalf("stageIngestSourceSchema constant does not match v2.62.1 pinned literal:\ngot:  %q\nwant: %q", stageIngestSourceSchema, wantSchema)
	}

	reg, _, _ := engineRegistry(t, realExtractors())
	tool, ok := reg.Get("stage.ingest_source")
	if !ok {
		t.Fatalf("stage.ingest_source tool not registered")
	}
	if got := string(tool.Schema); got != wantSchema {
		t.Fatalf("tool.Schema does not match v2.62.1 pinned literal:\ngot:  %q\nwant: %q", got, wantSchema)
	}
}

// TestNotes074_LocalSourceNotePlacement proves that a local source under <vault>/notes/
// stages at raw/notes/<name>.md with original at raw/notes/<name>.md.orig and
// frontmatter pointing to the original, across all kind values (article, paper, transcript, "").
func TestNotes074_LocalSourceNotePlacement(t *testing.T) {
	kinds := []struct {
		name string
		kind string
	}{
		{"article", "article"},
		{"paper", "paper"},
		{"transcript", "transcript"},
		{"empty", ""},
	}

	for _, tc := range kinds {
		t.Run("kind_"+tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("LW_VAULT", "")

			reg, e, vaultDir := engineRegistry(t, realExtractors())
			notesDir := filepath.Join(vaultDir, vault.NotesDir)
			if err := os.MkdirAll(notesDir, 0o755); err != nil {
				t.Fatalf("mkdir notes: %v", err)
			}

			notePath := filepath.Join(notesDir, "20261101-x.md")
			body := "Note content without header for exact filename match.\n"
			if err := os.WriteFile(notePath, []byte(body), 0o644); err != nil {
				t.Fatalf("write note: %v", err)
			}

			ctx := context.Background()
			if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest note"}`)); err != nil || r.IsError {
				t.Fatalf("stage.open: %+v %v", r, err)
			}

			argMap := map[string]string{"uri": notePath}
			if tc.kind != "" {
				argMap["kind"] = tc.kind
			}
			args, _ := json.Marshal(argMap)

			res, err := reg.Call(ctx, "stage.ingest_source", args)
			if err != nil {
				t.Fatalf("stage.ingest_source err: %v", err)
			}
			if res.IsError {
				t.Fatalf("stage.ingest_source returned error: %s", res.Content)
			}

			op := onlyOp(t, e)
			wantPath := "raw/notes/20261101-x.md"
			if op.Path != wantPath {
				t.Errorf("op.Path = %q, want %q", op.Path, wantPath)
			}

			wantOrig := "raw/notes/20261101-x.md.orig"
			if op.OriginalPath != wantOrig {
				t.Errorf("op.OriginalPath = %q, want %q", op.OriginalPath, wantOrig)
			}

			b, staged, err := e.StagedFile(op.Path)
			if err != nil || !staged {
				t.Fatalf("StagedFile(%q) failed: staged=%v, err=%v", op.Path, staged, err)
			}

			rs, err := vault.ParseRawSource(op.Path, b)
			if err != nil {
				t.Fatalf("ParseRawSource(%q): %v", op.Path, err)
			}
			if rs.Original != wantOrig {
				t.Errorf("frontmatter Original = %q, want %q", rs.Original, wantOrig)
			}

			if !strings.Contains(string(b), "original: "+wantOrig) {
				t.Errorf("raw content missing frontmatter original line; got:\n%s", string(b))
			}
		})
	}
}

// TestNotes074_SubdirAndBoundaryCases verifies that <vault>/notes/sub/y.md stages under
// raw/notes/, while boundary cases (<vault>/deep/notes/z.md, <vault>/notesx/a.md, outside vault,
// http URL) remain under raw/articles/ (unchanged placement).
func TestNotes074_SubdirAndBoundaryCases(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	t.Run("notes_subdir", func(t *testing.T) {
		reg, e, vaultDir := engineRegistry(t, realExtractors())
		subDir := filepath.Join(vaultDir, vault.NotesDir, "sub")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(subDir, "y.md")
		if err := os.WriteFile(p, []byte("Sub note body.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest sub note"}`)); err != nil || r.IsError {
			t.Fatalf("stage.open: %+v %v", r, err)
		}
		args, _ := json.Marshal(map[string]string{"uri": p, "kind": "article"})
		res, err := reg.Call(ctx, "stage.ingest_source", args)
		if err != nil || res.IsError {
			t.Fatalf("stage.ingest_source: %+v %v", res, err)
		}

		op := onlyOp(t, e)
		if want := "raw/notes/y.md"; op.Path != want {
			t.Errorf("op.Path = %q, want %q", op.Path, want)
		}
		if want := "raw/notes/y.md.orig"; op.OriginalPath != want {
			t.Errorf("op.OriginalPath = %q, want %q", op.OriginalPath, want)
		}
	})

	t.Run("deep_notes_boundary", func(t *testing.T) {
		reg, e, vaultDir := engineRegistry(t, realExtractors())
		deepDir := filepath.Join(vaultDir, "deep", "notes")
		if err := os.MkdirAll(deepDir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(deepDir, "z.md")
		if err := os.WriteFile(p, []byte("Deep note body.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest deep note"}`)); err != nil || r.IsError {
			t.Fatalf("stage.open: %+v %v", r, err)
		}
		args, _ := json.Marshal(map[string]string{"uri": p, "kind": "article"})
		res, err := reg.Call(ctx, "stage.ingest_source", args)
		if err != nil || res.IsError {
			t.Fatalf("stage.ingest_source: %+v %v", res, err)
		}

		op := onlyOp(t, e)
		if want := "raw/articles/z.md"; op.Path != want {
			t.Errorf("op.Path = %q, want %q", op.Path, want)
		}
	})

	t.Run("notesx_boundary", func(t *testing.T) {
		reg, e, vaultDir := engineRegistry(t, realExtractors())
		notesxDir := filepath.Join(vaultDir, "notesx")
		if err := os.MkdirAll(notesxDir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(notesxDir, "a.md")
		if err := os.WriteFile(p, []byte("Notesx body.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest notesx"}`)); err != nil || r.IsError {
			t.Fatalf("stage.open: %+v %v", r, err)
		}
		args, _ := json.Marshal(map[string]string{"uri": p, "kind": "article"})
		res, err := reg.Call(ctx, "stage.ingest_source", args)
		if err != nil || res.IsError {
			t.Fatalf("stage.ingest_source: %+v %v", res, err)
		}

		op := onlyOp(t, e)
		if want := "raw/articles/a.md"; op.Path != want {
			t.Errorf("op.Path = %q, want %q", op.Path, want)
		}
	})

	t.Run("outside_vault", func(t *testing.T) {
		reg, e, _ := engineRegistry(t, realExtractors())
		outsideDir := t.TempDir()
		p := filepath.Join(outsideDir, "outside.md")
		if err := os.WriteFile(p, []byte("Outside vault body.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest outside"}`)); err != nil || r.IsError {
			t.Fatalf("stage.open: %+v %v", r, err)
		}
		args, _ := json.Marshal(map[string]string{"uri": p, "kind": "article"})
		res, err := reg.Call(ctx, "stage.ingest_source", args)
		if err != nil || res.IsError {
			t.Fatalf("stage.ingest_source: %+v %v", res, err)
		}

		op := onlyOp(t, e)
		if want := "raw/articles/outside.md"; op.Path != want {
			t.Errorf("op.Path = %q, want %q", op.Path, want)
		}
	})

	t.Run("http_url", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<!DOCTYPE html><html><head><title>Web Source</title></head><body><p>Web note body.</p></body></html>"))
		}))
		defer srv.Close()

		dir := testutil.CopyFixture(t, "minimal")
		e, err := stage.OpenEngine(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()

		reg := NewRegistry(Deps{
			Vault:  e.Vault(),
			Index:  index.Build(e.Vault()),
			Engine: e,
			Extract: extract.Chain(
				extract.NewHTML(extract.NewHTTPClient(10*time.Second)),
				extract.NewFile(),
			),
			Author: stage.Author{Kind: "agent", Model: "test"},
		})

		ctx := context.Background()
		if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest url"}`)); err != nil || r.IsError {
			t.Fatalf("stage.open: %+v %v", r, err)
		}

		urlPath := srv.URL + "/notes/web.md"
		args, _ := json.Marshal(map[string]string{"uri": urlPath, "kind": "article"})
		res, err := reg.Call(ctx, "stage.ingest_source", args)
		if err != nil || res.IsError {
			t.Fatalf("stage.ingest_source: %+v %v", res, err)
		}

		op := onlyOp(t, e)
		if want := "raw/articles/web-source.md"; op.Path != want {
			t.Errorf("op.Path = %q, want %q", op.Path, want)
		}
	})
}

// TestNotes074_SupersedesAcrossDirs tests that re-ingesting a note whose earlier revision sits at
// raw/articles/x.md (pre-S3) records `supersedes: raw/articles/x.md` in the new raw/notes/... raw.
func TestNotes074_SupersedesAcrossDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	dir := testutil.CopyFixture(t, "minimal")
	noteDir := filepath.Join(dir, vault.NotesDir)
	if err := os.MkdirAll(noteDir, 0o755); err != nil {
		t.Fatal(err)
	}

	notePath := filepath.Join(noteDir, "x.md")
	bodyA := "# X\n\nOriginal body A in note.\n"
	bodyB := "# X\n\nEdited body B in note.\n"
	shaA := vault.BodySHA256(bodyA)

	rawX := "---\nsource_url: " + notePath + "\ningested: 2026-10-01\nsha256: " + shaA + "\noriginal: raw/articles/x.md.orig\noriginal_sha256: " + shaA + "\n---\n\n" + bodyA

	rawArticlesDir := filepath.Join(dir, "raw", "articles")
	if err := os.MkdirAll(rawArticlesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rawArticlesDir, "x.md"), []byte(rawX), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write new edited body to notePath
	if err := os.WriteFile(notePath, []byte(bodyB), 0o644); err != nil {
		t.Fatal(err)
	}

	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	reg := NewRegistry(Deps{
		Vault:   e.Vault(),
		Index:   index.Build(e.Vault()),
		Engine:  e,
		Extract: realExtractors(),
		Author:  stage.Author{Kind: "agent", Model: "test"},
	})

	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"re-ingest note revision"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}

	args, _ := json.Marshal(map[string]string{"uri": notePath, "kind": "article"})
	res, err := reg.Call(ctx, "stage.ingest_source", args)
	if err != nil {
		t.Fatalf("stage.ingest_source err: %v", err)
	}
	if res.IsError {
		t.Fatalf("stage.ingest_source error: %s", res.Content)
	}

	if !strings.Contains(res.Content, "revises raw/articles/x.md") {
		t.Errorf("result content missing revision note; got: %q", res.Content)
	}

	op := onlyOp(t, e)
	wantPath := "raw/notes/x.md"
	if op.Path != wantPath {
		t.Errorf("op.Path = %q, want %q", op.Path, wantPath)
	}

	b, staged, err := e.StagedFile(op.Path)
	if err != nil || !staged {
		t.Fatalf("StagedFile(%q) failed: %v", op.Path, err)
	}

	rs, err := vault.ParseRawSource(op.Path, b)
	if err != nil {
		t.Fatalf("ParseRawSource(%q): %v", op.Path, err)
	}

	if rs.Supersedes != "raw/articles/x.md" {
		t.Errorf("rs.Supersedes = %q, want %q", rs.Supersedes, "raw/articles/x.md")
	}

	if !strings.Contains(string(b), "supersedes: raw/articles/x.md") {
		t.Errorf("raw content missing supersedes frontmatter; got:\n%s", string(b))
	}
}

// TestNotes074_PreExtractedScratchPath tests that when stage.ingest_source is called
// with a scratch temporary path under uri but doc.SourceURL has the original note path
// (the CLI preExtracted and --prepare behavior), the source stages under raw/notes/.
func TestNotes074_PreExtractedScratchPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	reg, e, vaultDir := engineRegistry(t, nil)
	notesDir := filepath.Join(vaultDir, vault.NotesDir)
	origNotePath := filepath.Join(notesDir, "scratch-note.md")

	scratchDir := t.TempDir()
	scratchPath := filepath.Join(scratchDir, "scratch.md")
	scratchBody := "# Scratch Note\n\nExtracted markdown from scratch.\n"
	if err := os.WriteFile(scratchPath, []byte(scratchBody), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeDoc := &extract.Doc{
		Title:         "Scratch Note",
		SourceURL:     origNotePath,
		Markdown:      scratchBody,
		Kind:          "article",
		OriginalBytes: []byte(scratchBody),
		OriginalExt:   ".md",
	}

	reg = NewRegistry(Deps{
		Vault:   e.Vault(),
		Index:   index.Build(e.Vault()),
		Engine:  e,
		Extract: customFakeExtractor{doc: fakeDoc},
		Author:  stage.Author{Kind: "agent", Model: "test"},
	})

	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest pre-extracted"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}

	args, _ := json.Marshal(map[string]string{"uri": scratchPath, "kind": "article"})
	res, err := reg.Call(ctx, "stage.ingest_source", args)
	if err != nil || res.IsError {
		t.Fatalf("stage.ingest_source: %+v %v", res, err)
	}

	op := onlyOp(t, e)
	if want := "raw/notes/scratch-note.md"; op.Path != want {
		t.Errorf("op.Path = %q, want %q", op.Path, want)
	}
	if want := "raw/notes/scratch-note.md.orig"; op.OriginalPath != want {
		t.Errorf("op.OriginalPath = %q, want %q", op.OriginalPath, want)
	}
}

// TestNotes074_SuffixAndDedupe proves that note sources follow the same dedupe-by-sha
// and -2 suffixing rules as other sources, all under raw/notes/.
func TestNotes074_SuffixAndDedupe(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	reg, e, vaultDir := engineRegistry(t, realExtractors())
	notesDir := filepath.Join(vaultDir, vault.NotesDir)
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	noteA := filepath.Join(notesDir, "item.md")
	bodyA := "# Item\n\nBody A.\n"
	if err := os.WriteFile(noteA, []byte(bodyA), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest item"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}

	argsA, _ := json.Marshal(map[string]string{"uri": noteA, "kind": "article"})
	resA, err := reg.Call(ctx, "stage.ingest_source", argsA)
	if err != nil || resA.IsError {
		t.Fatalf("stage.ingest_source A: %+v %v", resA, err)
	}

	// 1. Dedupe check: ingesting the exact same body in the same changeset is refused
	resDup, err := reg.Call(ctx, "stage.ingest_source", argsA)
	if err != nil {
		t.Fatalf("stage.ingest_source duplicate err: %v", err)
	}
	if !resDup.IsError || !strings.Contains(resDup.Content, "already ingested or proposed") {
		t.Fatalf("expected duplicate rejection; got: %+v", resDup)
	}

	// Commit changeset so raw/notes/item.md exists in the committed vault
	if _, err := e.Commit("commit item"); err != nil {
		t.Fatalf("e.Commit: %v", err)
	}

	// Refresh vault and registry after commit
	v, err := vault.Open(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	e, err = stage.OpenEngine(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	reg = NewRegistry(Deps{
		Vault:   v,
		Index:   index.Build(v),
		Engine:  e,
		Extract: realExtractors(),
		Author:  stage.Author{Kind: "agent", Model: "test"},
	})

	// 2. Suffix check: another source with same title/slug "Item" but different body stages at raw/notes/item-2.md
	noteB := filepath.Join(notesDir, "item-different.md")
	bodyB := "# Item\n\nDifferent body B.\n"
	if err := os.WriteFile(noteB, []byte(bodyB), 0o644); err != nil {
		t.Fatal(err)
	}

	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest second item"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open 2: %+v %v", r, err)
	}

	argsB, _ := json.Marshal(map[string]string{"uri": noteB, "kind": "article"})
	resB, err := reg.Call(ctx, "stage.ingest_source", argsB)
	if err != nil || resB.IsError {
		t.Fatalf("stage.ingest_source B: %+v %v", resB, err)
	}

	op := onlyOp(t, e)
	wantSuffixed := "raw/notes/item-2.md"
	if op.Path != wantSuffixed {
		t.Errorf("op.Path = %q, want %q", op.Path, wantSuffixed)
	}
	wantOrig := "raw/notes/item-2.md.orig"
	if op.OriginalPath != wantOrig {
		t.Errorf("op.OriginalPath = %q, want %q", op.OriginalPath, wantOrig)
	}
}

package extract

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// extractFile writes content to a temp .md file and runs the real
// fileExtractor over it, so every subtest exercises the public entry point
// exactly as stage.ingest_source does.
func extractFile(t *testing.T, content string) *Doc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	doc, err := NewFile().Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	return doc
}

// TestFileExtractFrontmatterTitle pins contract §2: a document that starts
// with a `---` line and has a closing `---` line lends its frontmatter
// `title:` to Doc.Title, trimmed and with one level of matching quotes
// removed; everything else still falls back to firstATXH1. U6's real input
// shape is the first case — a clipping with frontmatter and no H1.
func TestFileExtractFrontmatterTitle(t *testing.T) {
	t.Run("frontmatter_title_without_heading", func(t *testing.T) {
		doc := extractFile(t, "---\ntitle: Quaternion 四元數簡介\nsource: x\n---\n\nbody\n")
		if doc.Title != "Quaternion 四元數簡介" {
			t.Errorf("Title = %q, want %q", doc.Title, "Quaternion 四元數簡介")
		}
	})

	t.Run("frontmatter_title_wins_over_h1", func(t *testing.T) {
		doc := extractFile(t, "---\ntitle: A\n---\n\n# B\n\nbody\n")
		if doc.Title != "A" {
			t.Errorf("Title = %q, want %q", doc.Title, "A")
		}
	})

	t.Run("quoted_title_is_unquoted", func(t *testing.T) {
		doc := extractFile(t, "---\ntitle: \"Q: a\"\n---\n\nbody\n")
		if doc.Title != "Q: a" {
			t.Errorf("double-quoted Title = %q, want %q", doc.Title, "Q: a")
		}
		doc = extractFile(t, "---\ntitle: 'x'\n---\n\nbody\n")
		if doc.Title != "x" {
			t.Errorf("single-quoted Title = %q, want %q", doc.Title, "x")
		}
	})

	t.Run("empty_title_falls_back_to_h1", func(t *testing.T) {
		doc := extractFile(t, "---\ntitle:\nsource: x\n---\n\n# H\n\nbody\n")
		if doc.Title != "H" {
			t.Errorf("Title = %q, want %q from the H1", doc.Title, "H")
		}
	})

	t.Run("unclosed_frontmatter_is_body", func(t *testing.T) {
		doc := extractFile(t, "---\ntitle: A\n# H\n")
		if doc.Title != "H" {
			t.Errorf("Title = %q, want %q — frontmatter without a closing line is body", doc.Title, "H")
		}
	})

	t.Run("frontmatter_not_at_start_is_ignored", func(t *testing.T) {
		doc := extractFile(t, "intro\n---\ntitle: A\n---\n\n# H\n")
		if doc.Title != "H" {
			t.Errorf("Title = %q, want %q — the block does not start the document", doc.Title, "H")
		}
	})

	t.Run("markdown_bytes_unchanged", func(t *testing.T) {
		contents := []string{
			"---\ntitle: Quaternion 四元數簡介\nsource: x\n---\n\nbody\n",
			"---\ntitle: A\n---\n\n# B\n\nbody\n",
			"---\ntitle: \"Q: a\"\n---\n\nbody\n",
			"---\ntitle:\nsource: x\n---\n\n# H\n\nbody\n",
			"---\ntitle: A\n# H\n",
			"intro\n---\ntitle: A\n---\n\n# H\n",
		}
		for _, content := range contents {
			doc := extractFile(t, content)
			if doc.Markdown != content {
				t.Errorf("Markdown = %q, want the file bytes %q", doc.Markdown, content)
			}
		}
	})
}

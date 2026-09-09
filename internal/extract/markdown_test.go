package extract

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileCanHandle(t *testing.T) {
	ex := NewFile()
	cases := []struct {
		uri  string
		want bool
	}{
		{"notes.md", true},
		{"notes.MD", true},
		{"notes.txt", true},
		{"notes.html", false},
		{"https://example.org/notes.md", false},
	}
	for _, tc := range cases {
		if got := ex.CanHandle(tc.uri); got != tc.want {
			t.Errorf("CanHandle(%q) = %v, want %v", tc.uri, got, tc.want)
		}
	}
}

func TestFileExtract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	content := "# My Note\r\n\r\nSome body text.\r\n\r\n\r\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ex := NewFile()
	doc, err := ex.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}

	if doc.Title != "My Note" {
		t.Errorf("Title = %q, want %q", doc.Title, "My Note")
	}
	if doc.Kind != "article" {
		t.Errorf("Kind = %q, want %q", doc.Kind, "article")
	}
	if doc.Extractor != "passthrough" {
		t.Errorf("Extractor = %q, want %q", doc.Extractor, "passthrough")
	}
	if doc.SourceURL != path {
		t.Errorf("SourceURL = %q, want %q", doc.SourceURL, path)
	}
	want := "# My Note\n\nSome body text.\n"
	if doc.Markdown != want {
		t.Errorf("Markdown = %q, want %q", doc.Markdown, want)
	}
}

func TestFileExtractNoHeading(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("just plain text, no heading\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ex := NewFile()
	doc, err := ex.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if doc.Title != "" {
		t.Errorf("Title = %q, want empty", doc.Title)
	}
}

func TestFileExtractSkipsHeadingInFence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	content := "```\n# not a heading\n```\n\n# Real Heading\n\nbody\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ex := NewFile()
	doc, err := ex.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if doc.Title != "Real Heading" {
		t.Errorf("Title = %q, want %q", doc.Title, "Real Heading")
	}
}

func TestFileExtractMissing(t *testing.T) {
	ex := NewFile()
	if _, err := ex.Extract(context.Background(), filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Fatal("Extract() error = nil, want a non-nil error for a missing file")
	}
}

func TestFileExtractDeterministic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("# Title\n\nBody.\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ex := NewFile()
	doc1, err := ex.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("Extract #1 error: %v", err)
	}
	doc2, err := ex.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("Extract #2 error: %v", err)
	}
	if doc1.Markdown != doc2.Markdown {
		t.Fatalf("markdown drifted across two extractions of the same input")
	}
}

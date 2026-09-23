package extract

import (
	"bytes"
	"context"
	"errors"
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

func TestFileCanHandleMarkdownExtension(t *testing.T) {
	ex := NewFile()
	cases := []struct {
		uri  string
		want bool
	}{
		{"notes.markdown", true},
		{"notes.MARKDOWN", true},
		{"notes.Markdown", true},
		{"notes.md", true},
		{"notes.txt", true},
		{"notes.html", false},
		{"notes.htm", false},
		{"https://example.org/notes.markdown", false},
	}
	for _, tc := range cases {
		if got := ex.CanHandle(tc.uri); got != tc.want {
			t.Errorf("CanHandle(%q) = %v, want %v", tc.uri, got, tc.want)
		}
	}
}

// TestFileExtractNotText is the frozen F.E3 pin: a file that is not valid
// UTF-8 or carries a NUL in its first 8 KiB refuses with ErrNotText.
func TestFileExtractNotText(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content []byte
	}{
		{"nul.txt", []byte("hello\x00world")},
		{"invalid.md", []byte("\xff\xfe")},
	}
	for _, tc := range cases {
		path := filepath.Join(dir, tc.name)
		if err := os.WriteFile(path, tc.content, 0o644); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
		_, err := NewFile().Extract(context.Background(), path)
		if err == nil {
			t.Fatalf("%s: Extract error = nil, want ErrNotText", tc.name)
		}
		if !errors.Is(err, ErrNotText) {
			t.Errorf("%s: Extract error %v does not wrap ErrNotText", tc.name, err)
		}
	}
}

// A multi-byte rune that straddles the sniff window edge is a truncation
// artifact of the window, not a property of the file: a valid UTF-8 file
// whose é (2 B) or 量 (3 B) crosses byte 8192 must still extract. Invalid
// bytes fully inside the window are still rejected — but the pin below
// puts one at byte 8190 of the é case, so only the cut sequence may be
// forgiven.
func TestFileExtractRuneStraddlesSniffWindow(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content []byte
	}{
		{"two-byte.md", append(bytes.Repeat([]byte("a"), 8191), "é after the edge\n"...)},
		// 量 = E9 87 8F, start byte at 8190: the window keeps E9 87.
		{"three-byte.md", append(bytes.Repeat([]byte("a"), 8190), "量 after the edge\n"...)},
		// Same straddle, but a genuinely invalid byte sits fully inside
		// the window — the straddle must not launder it through.
		{"straddle-and-garbage.md", append(append(bytes.Repeat([]byte("a"), 8191), "\xff"...), "é after the edge\n"...)},
	}
	for _, tc := range cases {
		path := filepath.Join(dir, tc.name)
		if err := os.WriteFile(path, tc.content, 0o644); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
		_, err := NewFile().Extract(context.Background(), path)
		if tc.name == "straddle-and-garbage.md" {
			if !errors.Is(err, ErrNotText) {
				t.Errorf("%s: Extract error %v, want ErrNotText (garbage inside the window)", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: Extract error %v, want nil (the rune cut by the window edge is not the file's fault)", tc.name, err)
		}
	}
}

// A NUL at byte 8192 is past the sniff window — the file is extracted.
func TestFileExtractNULPastWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "late-nul.txt")
	content := bytes.Repeat([]byte("a"), 8193)
	copy(content, "first line\n")
	content[8192] = 0
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	doc, err := NewFile().Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if len(doc.Markdown) == 0 {
		t.Fatal("Extract returned empty markdown for a text file with a late NUL")
	}
}

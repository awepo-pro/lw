package extract

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestDocSlug(t *testing.T) {
	cases := []struct {
		title string
		uri   string
		want  string
	}{
		{"System Design", "/notes/arch.md", "system-design"},
		{"", "/notes/My-Note.md", "my-note"},
		{"", "/notes/Another-Note.markdown", "another-note"},
		{"", "/notes/UPPER.MARKDOWN", "upper"},
		{"", "/notes/plain.txt", "plain-txt"},
		{"", "/notes/!!!.md", "untitled"},
		{"", "", "untitled"},
	}
	for _, tc := range cases {
		got := DocSlug(tc.title, tc.uri)
		if got != tc.want {
			t.Errorf("DocSlug(%q, %q) = %q, want %q", tc.title, tc.uri, got, tc.want)
		}
	}
}

func TestMarkdownExtractsAndRewritesLocalImages(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, data []byte) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(rel), err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	write("img/arch.png", []byte("arch-bytes"))
	write("sub/flow.jpg", []byte("flow-bytes"))
	write("icons/logo.svg", []byte("<svg>logo</svg>"))
	write("diagram.webp", []byte("diagram-bytes"))

	rawContent := `# System Design

Here is arch: ![arch](./img/arch.png)
And flow: ![flow](sub/flow.jpg "Flow")
And logo: <img src="icons/logo.svg" width="100">
And obsidian: ![[diagram.webp|Overview]]
`
	notePath := filepath.Join(dir, "note.md")
	if err := os.WriteFile(notePath, []byte(rawContent), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	doc, err := NewFile().Extract(context.Background(), notePath)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if doc.Title != "System Design" {
		t.Errorf("Title = %q, want %q", doc.Title, "System Design")
	}

	if len(doc.Assets) != 4 {
		t.Fatalf("len(Assets) = %d, want 4", len(doc.Assets))
	}

	wantAssets := []struct {
		name       string
		sourcePath string
		bytes      string
	}{
		{"system-design-arch.png", filepath.Join(dir, "img/arch.png"), "arch-bytes"},
		{"system-design-flow.jpg", filepath.Join(dir, "sub/flow.jpg"), "flow-bytes"},
		{"system-design-logo.svg", filepath.Join(dir, "icons/logo.svg"), "<svg>logo</svg>"},
		{"system-design-diagram.webp", filepath.Join(dir, "diagram.webp"), "diagram-bytes"},
	}

	for i, want := range wantAssets {
		got := doc.Assets[i]
		if got.Name != want.name {
			t.Errorf("Assets[%d].Name = %q, want %q", i, got.Name, want.name)
		}
		if got.SourcePath != want.sourcePath {
			t.Errorf("Assets[%d].SourcePath = %q, want %q", i, got.SourcePath, want.sourcePath)
		}
		if string(got.Bytes) != want.bytes {
			t.Errorf("Assets[%d].Bytes = %q, want %q", i, string(got.Bytes), want.bytes)
		}
	}

	wantSnippets := []string{
		"![arch](../../raw/assets/system-design-arch.png)",
		"![flow](../../raw/assets/system-design-flow.jpg \"Flow\")",
		`<img src="../../raw/assets/system-design-logo.svg" width="100">`,
		"![Overview](../../raw/assets/system-design-diagram.webp)",
	}

	for _, s := range wantSnippets {
		if !strings.Contains(doc.Markdown, s) {
			t.Errorf("Markdown missing rewritten snippet %q:\n%s", s, doc.Markdown)
		}
	}

	if string(doc.OriginalBytes) != rawContent {
		t.Errorf("OriginalBytes modified: got %q, want %q", string(doc.OriginalBytes), rawContent)
	}
}

func TestMarkdownImageCodeAndFrontmatterUntouched(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, data []byte) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(rel), err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	write("cover.png", []byte("cover"))
	write("fence.png", []byte("fence"))
	write("tilde.png", []byte("tilde"))
	write("inline.png", []byte("inline"))
	write("inline2.png", []byte("inline2"))

	content := `---
title: "Untouched Test"
cover: "![fm](cover.png)"
---
# Header

` + "```go\n![in_fence](fence.png)\n```" + `

~~~bash
![in_tilde](tilde.png)
~~~

Inline code: ` + "`![inline](inline.png)`" + ` and ` + "`` `![inline2](inline2.png)` ``" + `
`
	notePath := filepath.Join(dir, "note.md")
	if err := os.WriteFile(notePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	doc, err := NewFile().Extract(context.Background(), notePath)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if len(doc.Assets) != 0 {
		t.Fatalf("len(Assets) = %d, want 0 (all images inside protected regions)", len(doc.Assets))
	}

	if !strings.Contains(doc.Markdown, `cover: "![fm](cover.png)"`) {
		t.Errorf("frontmatter was modified:\n%s", doc.Markdown)
	}
	if !strings.Contains(doc.Markdown, "![in_fence](fence.png)") {
		t.Errorf("fence was modified:\n%s", doc.Markdown)
	}
	if !strings.Contains(doc.Markdown, "![in_tilde](tilde.png)") {
		t.Errorf("tilde fence was modified:\n%s", doc.Markdown)
	}
	if !strings.Contains(doc.Markdown, "`![inline](inline.png)`") {
		t.Errorf("inline code was modified:\n%s", doc.Markdown)
	}
	if !strings.Contains(doc.Markdown, "`` `![inline2](inline2.png)` ``") {
		t.Errorf("multi-backtick inline code was modified:\n%s", doc.Markdown)
	}
}

func TestMarkdownImageRemoteAndMissingUntouched(t *testing.T) {
	dir := t.TempDir()
	realImg := filepath.Join(dir, "real.png")
	if err := os.WriteFile(realImg, []byte("real-img"), 0o644); err != nil {
		t.Fatalf("write real.png: %v", err)
	}
	symlinkImg := filepath.Join(dir, "symlink.png")
	if err := os.Symlink(realImg, symlinkImg); err != nil {
		t.Fatalf("symlink symlink.png: %v", err)
	}
	zeroImg := filepath.Join(dir, "zero.png")
	if err := os.WriteFile(zeroImg, []byte{}, 0o644); err != nil {
		t.Fatalf("write zero.png: %v", err)
	}

	content := `# Remote and Missing

Remote: ![remote](https://example.com/a.png)
Data: ![data](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==)
Missing: ![missing](nope.png)
Symlink: ![symlink](symlink.png)
Zero: ![zero](zero.png)
`
	notePath := filepath.Join(dir, "note.md")
	if err := os.WriteFile(notePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	doc, err := NewFile().Extract(context.Background(), notePath)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if len(doc.Assets) != 0 {
		t.Fatalf("len(Assets) = %d, want 0", len(doc.Assets))
	}

	checks := []string{
		"![remote](https://example.com/a.png)",
		"![data](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==)",
		"![missing](nope.png)",
		"![symlink](symlink.png)",
		"![zero](zero.png)",
	}
	for _, c := range checks {
		if !strings.Contains(doc.Markdown, c) {
			t.Errorf("Markdown missing untouched string %q:\n%s", c, doc.Markdown)
		}
	}
}

func TestMarkdownImageCollisionAndDedupeWithinDoc(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, data []byte) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(rel), err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	write("img/a.png", []byte("bytes-a"))
	write("part1/fig.png", []byte("bytes-fig1"))
	write("part2/fig.png", []byte("bytes-fig2-different"))

	content := `# Collisions and Dedupe

First a: ![first](img/a.png)
Second a: ![second](img/a.png)

Fig 1: ![fig1](part1/fig.png)
Fig 2: ![fig2](part2/fig.png)
Fig 2 again: ![fig2-repeat](part2/fig.png)
`
	notePath := filepath.Join(dir, "note.md")
	if err := os.WriteFile(notePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	doc, err := NewFile().Extract(context.Background(), notePath)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if len(doc.Assets) != 3 {
		t.Fatalf("len(Assets) = %d, want 3", len(doc.Assets))
	}

	wantNames := []string{
		"collisions-and-dedupe-a.png",
		"collisions-and-dedupe-fig.png",
		"collisions-and-dedupe-fig-2.png",
	}

	for i, want := range wantNames {
		if doc.Assets[i].Name != want {
			t.Errorf("Assets[%d].Name = %q, want %q", i, doc.Assets[i].Name, want)
		}
	}

	if !strings.Contains(doc.Markdown, "![first](../../raw/assets/collisions-and-dedupe-a.png)") {
		t.Errorf("Markdown missing first a rewrite:\n%s", doc.Markdown)
	}
	if !strings.Contains(doc.Markdown, "![second](../../raw/assets/collisions-and-dedupe-a.png)") {
		t.Errorf("Markdown missing second a rewrite:\n%s", doc.Markdown)
	}

	if !strings.Contains(doc.Markdown, "![fig1](../../raw/assets/collisions-and-dedupe-fig.png)") {
		t.Errorf("Markdown missing fig1 rewrite:\n%s", doc.Markdown)
	}
	if !strings.Contains(doc.Markdown, "![fig2](../../raw/assets/collisions-and-dedupe-fig-2.png)") {
		t.Errorf("Markdown missing fig2 rewrite:\n%s", doc.Markdown)
	}
	if !strings.Contains(doc.Markdown, "![fig2-repeat](../../raw/assets/collisions-and-dedupe-fig-2.png)") {
		t.Errorf("Markdown missing fig2-repeat rewrite:\n%s", doc.Markdown)
	}
}

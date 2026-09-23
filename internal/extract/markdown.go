package extract

// markdown.go implements backbone §10's NewFile: a passthrough Extractor
// for local .md/.txt sources — a file that is already markdown (or plain
// text close enough to it) needs no DOM walk, only a Title recovered from
// its first ATX "# " heading and a normalized trailing newline
// (00-conventions.md §2, "every file ends with exactly one newline").

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ErrNotText is wrapped by an Extractor that refuses a file because its
// content is not text — invalid UTF-8, or a NUL byte in the first 8 KiB
// (004 F.E3). The walker never reads contents, so this sentinel is how the
// backend reports the verdict back to the folder-ingest caller.
var ErrNotText = errors.New("not text")

// sniffWindow is how many leading bytes the text sniff inspects: enough
// for any real magic-number signature (PNG's is 8 bytes), small enough
// that the verdict is cheap. A NUL or invalid UTF-8 past the window does
// not disqualify a file (004 F.E3).
const sniffWindow = 8 * 1024

// fileExtractor is the Extractor NewFile returns.
type fileExtractor struct{}

// NewFile returns an Extractor for local .md and .txt files: the content
// is used verbatim as Markdown, never reformatted (backbone §10).
func NewFile() Extractor {
	return fileExtractor{}
}

// CanHandle reports whether uri is a local path (not http/https) with a
// .md, .markdown or .txt extension (004 F.E2 added .markdown — the
// canonical extension of the format this extractor passes through —
// case-insensitive like .md always was).
func (fileExtractor) CanHandle(uri string) bool {
	if isRemoteURL(uri) {
		return false
	}
	ext := strings.ToLower(filepath.Ext(uri))
	return ext == ".md" || ext == ".markdown" || ext == ".txt"
}

// Extract reads uri and returns it as a Doc: Markdown is the file's
// content with line endings normalized to "\n" and exactly one trailing
// newline, Title is the document's YAML frontmatter `title:` when the file
// starts with a `---` line closed by another one (008 contract §2 — a
// frontmatter title wins over a heading), else the text of the first "# "
// ATX heading (or "" if neither exists — the caller, e.g.
// stage.ingest_source's tool, falls back to the source's own basename),
// Kind defaults to "article" (backbone §10 rules out any readability
// heuristic that would guess "paper" or "transcript"), and Extractor is
// "passthrough". The markdown itself is passthrough: frontmatter included,
// byte for byte.
func (fileExtractor) Extract(ctx context.Context, uri string) (*Doc, error) {
	b, err := os.ReadFile(uri)
	if err != nil {
		return nil, fmt.Errorf("extract: read %s: %w", uri, err)
	}

	// 004 F.E3: whether a file is text is decided here, in the backend,
	// not in the walker — Walk is stat-only and selects by CanHandle, so
	// the sniff must happen where the bytes are already in hand. Only the
	// first sniffWindow bytes are examined (see sniffWindow).
	window := b
	if len(window) > sniffWindow {
		window = window[:sniffWindow]
	}
	if !utf8.Valid(window) || bytes.IndexByte(window, 0) >= 0 {
		return nil, fmt.Errorf("extract: %s: %w", uri, ErrNotText)
	}

	body := normalizeNewlines(string(b))
	body = strings.TrimRight(body, "\n")
	if body != "" {
		body += "\n"
	}

	title := frontmatterTitle(body)
	if title == "" {
		title = firstATXH1(body)
	}

	return &Doc{
		Title:     title,
		SourceURL: uri,
		Markdown:  body,
		Kind:      "article",
		Extractor: "passthrough",
	}, nil
}

// frontmatterTitle returns the top-level `title:` value from body's YAML
// frontmatter — the block between an opening `---` as the very first line
// and a closing `---` — trimmed, with one level of matching surrounding
// double or single quotes removed. It returns "" when the document does
// not start with `---`, when no closing line exists (the block is then
// just body text), or when the key is absent or empty. A real YAML parser
// is deliberately not used: one top-level scalar key is all passthrough
// content needs, and a line scan cannot mis-parse the rest of the file.
func frontmatterTitle(body string) string {
	lines := strings.Split(body, "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return ""
	}

	title := ""
	closed := false
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			closed = true
			break
		}
		if title == "" && strings.HasPrefix(line, "title:") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "title:"))
		}
	}
	if !closed || title == "" {
		return ""
	}
	if len(title) >= 2 && (title[0] == '"' || title[0] == '\'') && title[len(title)-1] == title[0] {
		title = title[1 : len(title)-1]
	}
	return title
}

// normalizeNewlines rewrites "\r\n" and lone "\r" to "\n", so a file
// saved on a different platform still extracts to byte-identical
// markdown on every run.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// firstATXH1 returns the text of the first line of body that is a
// level-1 ATX heading ("# " followed by non-whitespace), trimmed, or ""
// if body has none. This is deliberately simpler than a full markdown
// parse — passthrough content is trusted to already be well-formed
// markdown — and never looks inside a fenced code block, since a line
// beginning with "# " but preceded by an open "```" fence is source code,
// not a heading; body is scanned top-down so the fence state is exact.
func firstATXH1(body string) string {
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

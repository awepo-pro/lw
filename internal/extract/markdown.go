package extract

// markdown.go implements backbone §10's NewFile: a passthrough Extractor
// for local .md/.txt sources — a file that is already markdown (or plain
// text close enough to it) needs no DOM walk, only a Title recovered from
// its first ATX "# " heading and a normalized trailing newline
// (00-conventions.md §2, "every file ends with exactly one newline").

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fileExtractor is the Extractor NewFile returns.
type fileExtractor struct{}

// NewFile returns an Extractor for local .md and .txt files: the content
// is used verbatim as Markdown, never reformatted (backbone §10).
func NewFile() Extractor {
	return fileExtractor{}
}

// CanHandle reports whether uri is a local path (not http/https) with a
// .md or .txt extension.
func (fileExtractor) CanHandle(uri string) bool {
	if isRemoteURL(uri) {
		return false
	}
	ext := strings.ToLower(filepath.Ext(uri))
	return ext == ".md" || ext == ".txt"
}

// Extract reads uri and returns it as a Doc: Markdown is the file's
// content with line endings normalized to "\n" and exactly one trailing
// newline, Title is the text of the first "# " ATX heading (or "" if none
// exists — the caller, e.g. stage.ingest_source's tool, falls back to the
// source's own basename), Kind defaults to "article" (backbone §10 rules
// out any readability heuristic that would guess "paper" or
// "transcript"), and Extractor is "passthrough".
func (fileExtractor) Extract(ctx context.Context, uri string) (*Doc, error) {
	b, err := os.ReadFile(uri)
	if err != nil {
		return nil, fmt.Errorf("extract: read %s: %w", uri, err)
	}

	body := normalizeNewlines(string(b))
	body = strings.TrimRight(body, "\n")
	if body != "" {
		body += "\n"
	}

	return &Doc{
		Title:     firstATXH1(body),
		SourceURL: uri,
		Markdown:  body,
		Kind:      "article",
		Extractor: "passthrough",
	}, nil
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

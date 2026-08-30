package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"gopkg.in/yaml.v3"
)

// RawSource is one ingested, write-once source document under raw/.
type RawSource struct {
	Path      string // "raw/papers/leviathan-2023.md"
	SourceURL string
	Ingested  Date
	SHA256    string // hex sha256 of Body — drives dedupe and drift detection
	Title     string
	Body      string
}

// rawFrontmatter is the exact three-key YAML shape of a raw source's
// frontmatter block (backbone §2.7, S1 correction C-3): source_url,
// ingested, sha256 — no title key.
type rawFrontmatter struct {
	SourceURL string `yaml:"source_url"`
	Ingested  Date   `yaml:"ingested"`
	SHA256    string `yaml:"sha256"`
}

// ParseRawSource parses b — the full contents of a raw source file at
// path — into a RawSource.
//
// Contract (backbone §2.7): the frontmatter carries exactly source_url,
// ingested, sha256. Body is everything after the closing "---" line with
// leading blank lines stripped and the trailing newline kept — reusing the
// same delimiter scan ParseFrontmatter uses, so the two never disagree on
// where a body starts. Title is derived from the body afterward (S1
// correction C-3), never read from the frontmatter block.
func ParseRawSource(path string, b []byte) (*RawSource, error) {
	if !bytes.HasPrefix(b, []byte(openDelim)) {
		return nil, fmt.Errorf("vault: raw source %s: must open with %q", path, openDelim)
	}
	rest := b[len(openDelim):]

	yamlEnd, bodyStart, ok := findClosingDelimiter(rest)
	if !ok {
		return nil, fmt.Errorf("vault: raw source %s: frontmatter has no closing %q line", path, "---")
	}
	yamlBytes := rest[:yamlEnd]
	body := string(stripLeadingBlankLines(rest[bodyStart:]))

	var raw rawFrontmatter
	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		return nil, fmt.Errorf("vault: raw source %s: parse frontmatter yaml: %w", path, err)
	}

	return &RawSource{
		Path:      path,
		SourceURL: raw.SourceURL,
		Ingested:  raw.Ingested,
		SHA256:    raw.SHA256,
		Title:     firstH1Title(body),
		Body:      body,
	}, nil
}

// firstH1Title returns the text of the first level-1 ATX heading in body —
// the "#" and following space stripped — or "" if body has none.
//
// Contract (backbone §2.7): "the text of the first ATX `# ` heading". This
// reuses ParseSections (backbone §2.4), which already walks the goldmark AST
// and strips the "#"s into Section.Title, rather than re-deriving the same
// heading-detection logic with a second, independent implementation that
// could disagree with it about what counts as a heading (e.g. one inside a
// fenced code block).
func firstH1Title(body string) string {
	for _, s := range ParseSections(body) {
		if s.Level == 1 {
			return s.Title
		}
	}
	return ""
}

// Serialize renders r as canonical bytes: the three frontmatter keys in
// source_url, ingested, sha256 order, then "---\n", a blank line, then Body.
//
// Contract (backbone §2.7): no title key is ever emitted. Unlike
// Frontmatter.Encode (§2.2), these three scalars are never quoted — a
// source_url is a bare URL and quoting on the colon in "https://" would
// itself break byte-stability against the fixtures, none of which quote it.
func (r *RawSource) Serialize() []byte {
	var buf bytes.Buffer
	buf.WriteString(openDelim)
	fmt.Fprintf(&buf, "source_url: %s\n", r.SourceURL)
	fmt.Fprintf(&buf, "ingested: %s\n", r.Ingested.String())
	fmt.Fprintf(&buf, "sha256: %s\n", r.SHA256)
	buf.WriteString("---\n")
	buf.WriteString("\n")
	buf.WriteString(r.Body)
	return buf.Bytes()
}

// BodySHA256 returns the hex sha256 of body — the same hash a correctly
// ingested RawSource carries in its SHA256 field, and the definition
// src-integrity's drift check recomputes to compare against it.
//
// Contract (backbone §2.7, S1 correction C-2): bare lowercase hex, 64
// characters, no "sha256:" prefix.
func BodySHA256(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

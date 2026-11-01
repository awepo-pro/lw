package digest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/vault"
)

func setupTestVault(t *testing.T) (string, *vault.Vault, string) {
	t.Helper()
	dir := t.TempDir()

	// SCHEMA.md
	schemaContent := `# SCHEMA

## Domain
Machine learning systems and inference optimizations.

## Page types
- entity: systems, models, hardware
- concept: techniques, algorithms, abstractions
- comparison: tradeoffs between two or more approaches
- query: open questions and synthesis
- summary: distillations of multiple sources

## Tags
- decoding: speculative and draft decoding
- inference: serving and throughput
- memory: kv cache and weights
- transformers: architectures and attention
`
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schemaContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create directories
	dirs := []string{
		filepath.Join(dir, "wiki", "concepts"),
		filepath.Join(dir, "wiki", "entities"),
		filepath.Join(dir, "raw", "papers"),
		filepath.Join(dir, "raw", "articles"),
		filepath.Join(dir, ".llmwiki"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// 1. p1: created inside window
	p1Content := `---
title: New Concept
created: 2026-10-12
updated: 2026-10-12
type: concept
tags: [inference, decoding]
sources: [raw/papers/r1.md]
confidence: high
---

# New Concept

New concept explains [[target-page|Target Page]] efficiently.^[raw/papers/r1.md p.2]

## Details
- item
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "new-concept.md"), []byte(p1Content), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2. p2: created before window, updated inside window
	p2Content := `---
title: Updated Entity
created: 2026-09-01
updated: 2026-10-15
type: entity
tags: [transformers]
sources: [raw/articles/r2.md]
confidence: medium
---

# Updated Entity

Updated entity description with [[transformer]].
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "entities", "updated-entity.md"), []byte(p2Content), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. p3: untouched in window
	p3Content := `---
title: Untouched Concept
created: 2026-09-01
updated: 2026-09-01
type: concept
tags: [memory]
confidence: low
---

# Untouched Concept

Never touched in this window.
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "untouched.md"), []byte(p3Content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Note: p4 is "wiki/entities/deleted.md" which is NOT written to disk (deleted page).

	// 2 raws
	r1Content := `---
source_url: https://example.com/r1.pdf
ingested: 2026-10-12
sha256: 0000000000000000000000000000000000000000000000000000000000000001
---

# Paper R1

Paper body content.
`
	if err := os.WriteFile(filepath.Join(dir, "raw", "papers", "r1.md"), []byte(r1Content), 0o644); err != nil {
		t.Fatal(err)
	}

	r2Content := `---
source_url: https://example.com/r2.html
ingested: 2026-10-14
sha256: 0000000000000000000000000000000000000000000000000000000000000002
---

# Article R2

Article body content.
`
	if err := os.WriteFile(filepath.Join(dir, "raw", "articles", "r2.md"), []byte(r2Content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Journal with:
	// - 2 complete commits inside window (cs-01 and cs-02)
	// - 1 commit_begin without commit_end (cs-03)
	// - 1 complete commit before window (cs-00)
	// - 1 rejected changeset (cs-bad)
	// - 1 malformed line
	journalLines := []string{
		// Commit 0 (before window)
		`{"ts":"2026-09-15T09:00:00Z","kind":"commit_begin","changeset":"cs-00","commit":"c000","paths":["wiki/concepts/untouched.md"],"actor":{"kind":"human"},"message":"initial setup"}`,
		`{"ts":"2026-09-15T09:00:00Z","kind":"commit_end","changeset":"cs-00","commit":"c000"}`,
		// Malformed line
		`{"ts":"invalid-json`,
		// Commit 1 (in window)
		`{"ts":"2026-10-12T10:00:00Z","kind":"commit_begin","changeset":"cs-01","commit":"c001","paths":["wiki/concepts/new-concept.md","raw/papers/r1.md"],"actor":{"kind":"agent","model":"claude-3-5-sonnet"},"message":"add new concept"}`,
		`{"ts":"2026-10-12T10:00:00Z","kind":"commit_end","changeset":"cs-01","commit":"c001"}`,
		// Rejected changeset (in window)
		`{"ts":"2026-10-13T12:00:00Z","kind":"changeset_rejected","changeset":"cs-bad","actor":{"kind":"agent","model":"test"}}`,
		// Commit 2 (in window)
		`{"ts":"2026-10-15T14:30:00Z","kind":"commit_begin","changeset":"cs-02","commit":"c002","paths":["wiki/entities/updated-entity.md","wiki/entities/deleted.md","raw/articles/r2.md"],"actor":{"kind":"human"},"message":"update entity and delete obsolete"}`,
		`{"ts":"2026-10-15T14:30:00Z","kind":"commit_end","changeset":"cs-02","commit":"c002"}`,
		// Incomplete commit (in window, no commit_end)
		`{"ts":"2026-10-16T11:00:00Z","kind":"commit_begin","changeset":"cs-03","commit":"c003","paths":["wiki/concepts/uncommitted.md"],"actor":{"kind":"human"},"message":"uncommitted work"}`,
	}
	journalPath := filepath.Join(dir, ".llmwiki", "journal.ndjson")
	if err := os.WriteFile(journalPath, []byte(strings.Join(journalLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	return dir, v, journalPath
}

// D1 commit counting and windowing exact
func TestDigest_D1_CommitCountingAndWindowing(t *testing.T) {
	_, v, jpath := setupTestVault(t)
	opts := Options{
		Since: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
	}
	d, err := Build(v, jpath, opts)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Exact 2 commits counted
	if len(d.Commits) != 2 {
		t.Fatalf("len(d.Commits) = %d, want 2", len(d.Commits))
	}
	if d.Commits[0].ID != "c001" || d.Commits[0].Actor != "agent:claude-3-5-sonnet" || d.Commits[0].Paths != 2 {
		t.Errorf("commit[0] mismatch: %+v", d.Commits[0])
	}
	if d.Commits[1].ID != "c002" || d.Commits[1].Actor != "human" || d.Commits[1].Paths != 3 {
		t.Errorf("commit[1] mismatch: %+v", d.Commits[1])
	}

	if d.Rejected != 1 {
		t.Errorf("d.Rejected = %d, want 1", d.Rejected)
	}
	if d.Reverts != 0 {
		t.Errorf("d.Reverts = %d, want 0", d.Reverts)
	}
}

// D2 New/Updated/Deleted classification exact
func TestDigest_D2_Classification(t *testing.T) {
	_, v, jpath := setupTestVault(t)
	opts := Options{
		Since: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
	}
	d, err := Build(v, jpath, opts)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if len(d.New) != 1 {
		t.Fatalf("len(d.New) = %d, want 1", len(d.New))
	}
	if d.New[0].Path != "wiki/concepts/new-concept.md" {
		t.Errorf("d.New[0].Path = %q, want wiki/concepts/new-concept.md", d.New[0].Path)
	}

	if len(d.Updated) != 1 {
		t.Fatalf("len(d.Updated) = %d, want 1", len(d.Updated))
	}
	if d.Updated[0].Path != "wiki/entities/updated-entity.md" {
		t.Errorf("d.Updated[0].Path = %q, want wiki/entities/updated-entity.md", d.Updated[0].Path)
	}

	if len(d.Deleted) != 1 {
		t.Fatalf("len(d.Deleted) = %d, want 1", len(d.Deleted))
	}
	if d.Deleted[0] != "wiki/entities/deleted.md" {
		t.Errorf("d.Deleted[0] = %q, want wiki/entities/deleted.md", d.Deleted[0])
	}

	if len(d.Raws) != 2 {
		t.Fatalf("len(d.Raws) = %d, want 2", len(d.Raws))
	}
	if d.Raws[0].Path != "raw/articles/r2.md" || d.Raws[1].Path != "raw/papers/r1.md" {
		t.Errorf("d.Raws mismatch: %+v", d.Raws)
	}
}

// D3 gist strips citations and wikilinks, skips headings/lists, and truncates with \u2026 at a word boundary
func TestDigest_D3_ExtractGist(t *testing.T) {
	body := `# Heading 1

## Overview

> A blockquote that should be skipped.

- list item 1
- list item 2

1. ordered 1
2. ordered 2

| Table | Header |
|---|---|
| A | B |

` + "```go\nfunc Code() {}\n```" + `

This is the first prose paragraph of the page body. It has multiple sentences and cites ^[raw/papers/x.md p.5] as well as [[kv-cache|KV cache]] and [[flash-attention]]. It contains enough text to test word boundary truncation when the paragraph extends well past two hundred and twenty runes in total length for proper testing.

## Next Section
More text here.
`
	gist := ExtractGist(body)

	// Verify citations stripped
	if strings.Contains(gist, "^[") || strings.Contains(gist, "raw/papers/x.md") {
		t.Errorf("gist contains citation marker: %q", gist)
	}

	// Verify wikilinks replaced
	if strings.Contains(gist, "[[") || strings.Contains(gist, "]]") {
		t.Errorf("gist contains raw wikilink: %q", gist)
	}
	if !strings.Contains(gist, "KV cache") || !strings.Contains(gist, "flash-attention") {
		t.Errorf("gist missing replaced link labels: %q", gist)
	}

	// Verify skipped headings, lists, quotes, tables, fences
	if strings.Contains(gist, "Heading") || strings.Contains(gist, "blockquote") || strings.Contains(gist, "list item") || strings.Contains(gist, "Table") || strings.Contains(gist, "Code") {
		t.Errorf("gist contains non-prose elements: %q", gist)
	}

	// Verify truncation at word boundary with \u2026
	if !strings.HasSuffix(gist, "\u2026") {
		t.Errorf("gist does not end with \\u2026: %q", gist)
	}
	runeCount := utf8.RuneCountInString(gist)
	if runeCount > 221 {
		t.Errorf("gist rune count %d > 221 runes: %q", runeCount, gist)
	}
	// Check word boundary: character before ellipsis must not be a space
	beforeEllipsis := strings.TrimSuffix(gist, "\u2026")
	if strings.HasSuffix(beforeEllipsis, " ") {
		t.Errorf("gist has trailing space before ellipsis: %q", gist)
	}
}

// D4 WriteMarkdown golden (exact string) for that vault
func TestDigest_D4_WriteMarkdown_Golden(t *testing.T) {
	_, v, jpath := setupTestVault(t)
	opts := Options{
		Since: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
	}
	d, err := Build(v, jpath, opts)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, d); err != nil {
		t.Fatalf("WriteMarkdown failed: %v", err)
	}

	want := `# Wiki digest 2026-10-10 .. 2026-10-17
2 commits - 1 new pages - 1 updated - 2 new sources - 1 rejected

## New pages

### Concepts
- **New Concept** (` + "`wiki/concepts/new-concept.md`" + `) - New concept explains Target Page efficiently. *decoding, inference*

## Updated pages
- **Updated Entity** (` + "`wiki/entities/updated-entity.md`" + `)

## New sources
- Article R2 - https://example.com/r2.html (` + "`raw/articles/r2.md`" + `)
- Paper R1 - https://example.com/r1.pdf (` + "`raw/papers/r1.md`" + `)

## Top tags
- decoding: 1 new, 0 updated
- inference: 1 new, 0 updated
- transformers: 0 new, 1 updated

## Commits
- 2026-10-12 c001 add new concept (agent:claude-3-5-sonnet)
- 2026-10-15 c002 update entity and delete obsolete (human)
`
	if buf.String() != want {
		t.Errorf("WriteMarkdown output mismatch:\nGOT:\n%s\nWANT:\n%s", buf.String(), want)
	}
}

// D5 empty window message
func TestDigest_D5_EmptyWindow(t *testing.T) {
	_, v, jpath := setupTestVault(t)
	opts := Options{
		Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC),
	}
	d, err := Build(v, jpath, opts)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, d); err != nil {
		t.Fatalf("WriteMarkdown failed: %v", err)
	}

	want := `# Wiki digest 2026-11-01 .. 2026-11-07

nothing was committed in this window
`
	if buf.String() != want {
		t.Errorf("empty window output mismatch:\nGOT:\n%s\nWANT:\n%s", buf.String(), want)
	}
}

// D6 missing journal -> empty digest, no error
func TestDigest_D6_MissingJournal(t *testing.T) {
	dir, v, _ := setupTestVault(t)
	opts := Options{
		Since: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
	}
	d, err := Build(v, filepath.Join(dir, "no-such-journal.ndjson"), opts)
	if err != nil {
		t.Fatalf("Build returned error for missing journal: %v", err)
	}
	if len(d.Commits) != 0 || len(d.New) != 0 || len(d.Updated) != 0 || len(d.Raws) != 0 {
		t.Errorf("digest not empty: %+v", d)
	}
}

// D7 WriteHTML escapes a title containing <script> and contains no http(s) resource reference except vault source links
func TestDigest_D7_WriteHTML(t *testing.T) {
	_, v, jpath := setupTestVault(t)
	opts := Options{
		Since: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
	}
	d, err := Build(v, jpath, opts)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Add a page with <script> in title
	d.New = append(d.New, PageInfo{
		Title: `<script>alert("xss")</script>`,
		Path:  "wiki/concepts/xss.md",
		Type:  "concept",
	})

	var buf bytes.Buffer
	if err := WriteHTML(&buf, d); err != nil {
		t.Fatalf("WriteHTML failed: %v", err)
	}
	html := buf.String()

	// Check script is escaped
	if strings.Contains(html, `<script>`) {
		t.Errorf("HTML contains unescaped <script> tag")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("HTML does not contain escaped &lt;script&gt;")
	}

	// Check that there are no external resource references (no <link rel=..., <script src=..., <img src=...)
	if strings.Contains(html, "<link") || strings.Contains(html, "<script src") || strings.Contains(html, "<img") {
		t.Errorf("HTML contains external resource tags: %s", html)
	}

	// All http:// or https:// must be in <a href=...
	for _, line := range strings.Split(html, "\n") {
		if strings.Contains(line, "http://") || strings.Contains(line, "https://") {
			if !strings.Contains(line, `<a href="http`) {
				t.Errorf("line contains http/https outside <a href=: %q", line)
			}
		}
	}
}

// D8 deterministic output
func TestDigest_D8_DeterministicOutput(t *testing.T) {
	_, v, jpath := setupTestVault(t)
	opts := Options{
		Since: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
	}

	var firstMD string
	var firstHTML string
	for i := 0; i < 5; i++ {
		d, err := Build(v, jpath, opts)
		if err != nil {
			t.Fatalf("run %d Build failed: %v", i, err)
		}
		var bufMD bytes.Buffer
		if err := WriteMarkdown(&bufMD, d); err != nil {
			t.Fatalf("run %d WriteMarkdown failed: %v", i, err)
		}
		var bufHTML bytes.Buffer
		if err := WriteHTML(&bufHTML, d); err != nil {
			t.Fatalf("run %d WriteHTML failed: %v", i, err)
		}

		if i == 0 {
			firstMD = bufMD.String()
			firstHTML = bufHTML.String()
		} else {
			if bufMD.String() != firstMD {
				t.Fatalf("run %d MD output differed from run 0", i)
			}
			if bufHTML.String() != firstHTML {
				t.Fatalf("run %d HTML output differed from run 0", i)
			}
		}
	}
}

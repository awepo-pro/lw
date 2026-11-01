package vaultstats_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/vault"
	"github.com/awepo-pro/lw/internal/vaultstats"
)

func setupTestVault(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()

	// Copy SCHEMA.md from spec/fixtures/minimal
	schemaSrc, err := os.ReadFile("/home/anton/learning/llmwiki-078-x3-stats/spec/fixtures/minimal/SCHEMA.md")
	if err != nil {
		t.Fatalf("read minimal SCHEMA.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), schemaSrc, 0o644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}

	// Create directories
	dirs := []string{
		"wiki/concepts",
		"wiki/entities",
		"wiki/comparisons",
		"raw/papers",
		"raw/articles",
		"raw/assets",
		".llmwiki",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// 4 raws in two kinds:
	// r1 in papers: cited only inline by c1, supersedes r4
	r1 := `---
source_url: https://example.com/r1
ingested: 2026-10-01
sha256: 1111111111111111111111111111111111111111111111111111111111111111
supersedes: raw/papers/r4.md
---
Paper 1 body.
`
	// r2 in articles: cited only via sources: by c2
	r2 := `---
source_url: https://example.com/r2
ingested: 2026-10-02
sha256: 2222222222222222222222222222222222222222222222222222222222222222
---
Article 2 body.
`
	// r3 in articles: cited by nobody
	r3 := `---
source_url: https://example.com/r3
ingested: 2026-10-03
sha256: 3333333333333333333333333333333333333333333333333333333333333333
---
Article 3 body.
`
	// r4 in papers: superseded by r1
	r4 := `---
source_url: https://example.com/r4
ingested: 2026-09-01
sha256: 4444444444444444444444444444444444444444444444444444444444444444
---
Paper 4 body (older).
`
	if err := os.WriteFile(filepath.Join(dir, "raw/papers/r1.md"), []byte(r1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw/articles/r2.md"), []byte(r2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw/articles/r3.md"), []byte(r3), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw/papers/r4.md"), []byte(r4), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1 asset file under raw/assets/
	if err := os.WriteFile(filepath.Join(dir, "raw/assets/fig1.png"), []byte("PNG data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 5 pages of 3 types:
	// c1 (concept): updated 2026-11-01 (1d old, <7d), tags: tagA, tagB, cites r1 inline
	c1 := `---
title: Concept One
created: 2026-10-01
updated: 2026-11-01
type: concept
tags: [tagA, tagB]
sources: []
confidence: high
---
Cites r1 inline: ^[raw/papers/r1.md] and links to [[c2]] and [[e1]].
`
	// c2 (concept): updated 2026-10-20 (13d old, 7-30d), tags: tagA, tagC, sources: [raw/articles/r2.md]
	c2 := `---
title: Concept Two
created: 2026-10-01
updated: 2026-10-20
type: concept
tags: [tagA, tagC]
sources: [raw/articles/r2.md]
confidence: medium
---
Concept two body with links to [[e1]].
`
	// e1 (entity): updated 2026-09-15 (48d old, 30-90d), tags: tagA, tagB, contested: true
	e1 := `---
title: Entity One
created: 2026-09-01
updated: 2026-09-15
type: entity
tags: [tagA, tagB]
sources: []
confidence: low
contested: true
---
Entity one body linking to [[c1]].
`
	// e2 (entity): updated 2026-07-01 (124d old, >90d), tags: tagB, unset confidence
	e2 := `---
title: Entity Two
created: 2026-07-01
updated: 2026-07-01
type: entity
tags: [tagB]
sources: []
---
Entity two body linking to [[c1]].
`
	// comp1 (comparison): no updated date, tags: tagD
	comp1 := `---
title: Comparison One
created: 2026-06-01
type: comparison
tags: [tagD]
sources: []
confidence: high
---
Comparison body with no outbound links.
`
	if err := os.WriteFile(filepath.Join(dir, "wiki/concepts/c1.md"), []byte(c1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wiki/concepts/c2.md"), []byte(c2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wiki/entities/e1.md"), []byte(e1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wiki/entities/e2.md"), []byte(e2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wiki/comparisons/comp1.md"), []byte(comp1), 0o644); err != nil {
		t.Fatal(err)
	}

	// Journal file with 3 commit_end (in 2 different weeks), 1 rejected, 1 malformed
	journal := `{"ts":"2026-11-02T10:00:00Z","kind":"commit_end"}
{"ts":"2026-11-02T11:00:00Z","kind":"commit_end"}
{"ts":"2026-10-26T10:00:00Z","kind":"commit_end"}
{"ts":"2026-11-01T10:00:00Z","kind":"changeset_rejected"}
{malformed json line
`
	journalPath := filepath.Join(dir, ".llmwiki", "journal.ndjson")
	if err := os.WriteFile(journalPath, []byte(journal), 0o644); err != nil {
		t.Fatal(err)
	}

	return dir, journalPath
}

func TestVaultStats_S1_CountsByTypeAndKind(t *testing.T) {
	dir, journalPath := setupTestVault(t)
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("Open vault: %v", err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s := vaultstats.Compute(v, journalPath, now, 90)

	if s.Pages != 5 {
		t.Errorf("Pages = %d, want 5", s.Pages)
	}
	if s.ByType["concept"] != 2 || s.ByType["entity"] != 2 || s.ByType["comparison"] != 1 {
		t.Errorf("ByType = %+v, want concept:2, entity:2, comparison:1", s.ByType)
	}
	if s.Raws != 4 {
		t.Errorf("Raws = %d, want 4", s.Raws)
	}
	if s.RawsByKind["papers"] != 2 || s.RawsByKind["articles"] != 2 {
		t.Errorf("RawsByKind = %+v, want papers:2, articles:2", s.RawsByKind)
	}
	if s.Assets != 1 {
		t.Errorf("Assets = %d, want 1", s.Assets)
	}
}

func TestVaultStats_S2_TopTagsOrder(t *testing.T) {
	dir, journalPath := setupTestVault(t)
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("Open vault: %v", err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s := vaultstats.Compute(v, journalPath, now, 90)

	if s.DistinctTags != 4 {
		t.Errorf("DistinctTags = %d, want 4", s.DistinctTags)
	}
	if len(s.TopTags) != 4 {
		t.Fatalf("len(TopTags) = %d, want 4", len(s.TopTags))
	}
	wantOrder := []vaultstats.TagCount{
		{Tag: "tagA", Pages: 3},
		{Tag: "tagB", Pages: 3},
		{Tag: "tagC", Pages: 1},
		{Tag: "tagD", Pages: 1},
	}
	for i, want := range wantOrder {
		if s.TopTags[i] != want {
			t.Errorf("TopTags[%d] = %+v, want %+v", i, s.TopTags[i], want)
		}
	}
}

func TestVaultStats_S3_UncitedAndSupersededRaws(t *testing.T) {
	dir, journalPath := setupTestVault(t)
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("Open vault: %v", err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s := vaultstats.Compute(v, journalPath, now, 90)

	if s.SupersededRaws != 1 {
		t.Errorf("SupersededRaws = %d, want 1", s.SupersededRaws)
	}
	if len(s.UncitedRaws) != 1 || s.UncitedRaws[0] != "raw/articles/r3.md" {
		t.Errorf("UncitedRaws = %v, want exactly [raw/articles/r3.md]", s.UncitedRaws)
	}
}

func TestVaultStats_S4_FreshnessAndStale(t *testing.T) {
	dir, journalPath := setupTestVault(t)
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("Open vault: %v", err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s := vaultstats.Compute(v, journalPath, now, 90)

	if s.Freshness["<7d"] != 1 {
		t.Errorf("Freshness[<7d] = %d, want 1", s.Freshness["<7d"])
	}
	if s.Freshness["7-30d"] != 1 {
		t.Errorf("Freshness[7-30d] = %d, want 1", s.Freshness["7-30d"])
	}
	if s.Freshness["30-90d"] != 1 {
		t.Errorf("Freshness[30-90d] = %d, want 1", s.Freshness["30-90d"])
	}
	if s.Freshness[">90d"] != 1 {
		t.Errorf("Freshness[>90d] = %d, want 1", s.Freshness[">90d"])
	}
	if s.Freshness["no date"] != 1 {
		t.Errorf("Freshness[no date] = %d, want 1", s.Freshness["no date"])
	}

	if len(s.Stale) != 1 {
		t.Fatalf("len(Stale) = %d, want 1", len(s.Stale))
	}
	if s.Stale[0].Path != "wiki/entities/e2.md" {
		t.Errorf("Stale[0].Path = %q, want wiki/entities/e2.md", s.Stale[0].Path)
	}
	if s.Stale[0].Days <= 90 {
		t.Errorf("Stale[0].Days = %d, want > 90", s.Stale[0].Days)
	}
}

func TestVaultStats_S5_Activity(t *testing.T) {
	dir, journalPath := setupTestVault(t)
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("Open vault: %v", err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s := vaultstats.Compute(v, journalPath, now, 90)

	if s.Commits != 3 {
		t.Errorf("Commits = %d, want 3", s.Commits)
	}
	if s.Rejected != 1 {
		t.Errorf("Rejected = %d, want 1", s.Rejected)
	}
	if s.LastCommit != "2026-11-02" {
		t.Errorf("LastCommit = %q, want 2026-11-02", s.LastCommit)
	}
	if len(s.Weekly) != 8 {
		t.Fatalf("len(Weekly) = %d, want 8", len(s.Weekly))
	}
	// 2026-11-02 is in 2026-W45 (week 7)
	// 2026-10-26 is in 2026-W44 (week 6)
	if s.Weekly[7].Commits != 2 {
		t.Errorf("Weekly[7] (%s) commits = %d, want 2", s.Weekly[7].Week, s.Weekly[7].Commits)
	}
	if s.Weekly[6].Commits != 1 {
		t.Errorf("Weekly[6] (%s) commits = %d, want 1", s.Weekly[6].Week, s.Weekly[6].Commits)
	}
	for i := 0; i < 6; i++ {
		if s.Weekly[i].Commits != 0 {
			t.Errorf("Weekly[%d] (%s) commits = %d, want 0", i, s.Weekly[i].Week, s.Weekly[i].Commits)
		}
	}
}

func TestVaultStats_S6_MissingJournal(t *testing.T) {
	dir, _ := setupTestVault(t)
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("Open vault: %v", err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s := vaultstats.Compute(v, filepath.Join(dir, "no-such-journal.ndjson"), now, 90)

	if s.Commits != 0 || s.Rejected != 0 || s.LastCommit != "" {
		t.Errorf("Commits=%d, Rejected=%d, LastCommit=%q; want 0, 0, empty", s.Commits, s.Rejected, s.LastCommit)
	}
	for i, w := range s.Weekly {
		if w.Commits != 0 {
			t.Errorf("Weekly[%d].Commits = %d, want 0", i, w.Commits)
		}
	}
}

func TestVaultStats_S7_JSONEmptySlices(t *testing.T) {
	// Create empty vault
	dir := t.TempDir()
	schemaSrc, err := os.ReadFile("/home/anton/learning/llmwiki-078-x3-stats/spec/fixtures/minimal/SCHEMA.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), schemaSrc, 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := vaultstats.Compute(v, "", time.Now(), 90)

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	js := string(b)
	for _, field := range []string{"top_tags", "uncited_raws", "stale", "largest"} {
		needle := fmt.Sprintf("%q:[]", field)
		if !strings.Contains(js, needle) {
			t.Errorf("JSON output does not contain %s (got %s)", needle, js)
		}
	}
	if strings.Contains(js, "null") {
		t.Errorf("JSON output from Compute contains null: %s", js)
	}
}

func TestVaultStats_S8_WriteTextHeadingsAndLineWidth(t *testing.T) {
	dir, journalPath := setupTestVault(t)
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("Open vault: %v", err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s := vaultstats.Compute(v, journalPath, now, 90)

	var buf bytes.Buffer
	if err := vaultstats.WriteText(&buf, s); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()

	headings := []string{
		"pages",
		"raw sources",
		"tags",
		"confidence",
		"sourcing & citations",
		"coverage",
		"links",
		"freshness",
		"largest pages",
		"activity",
	}
	for _, h := range headings {
		if !strings.Contains(out, h) {
			t.Errorf("WriteText output missing heading %q\nFull output:\n%s", h, out)
		}
	}

	lines := strings.Split(out, "\n")
	for i, line := range lines {
		runeCount := utf8.RuneCountInString(line)
		if runeCount > 100 {
			t.Errorf("line %d has %d runes (> 100):\n%s", i+1, runeCount, line)
		}
	}
}

// TestVaultStats_DistinctEdges tests that distinct page pairs are counted separately from link occurrences,
// self-links are excluded, and the formatted text line is correct.
func TestVaultStats_DistinctEdges(t *testing.T) {
	dir := t.TempDir()
	schema := "# SCHEMA\n\n## Domain\ntest\n\n## Tags\n- a\n"
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schema), 0644); err != nil {
		t.Fatal(err)
	}
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Page a links b twice, links self once, and links broken once
	pageA := `---
title: Page A
type: concept
tags: [a]
---
# Page A
Link one to [[page-b]].
Link two to [[page-b]].
Self link to [[page-a]].
Broken link to [[non-existent]].
`
	pageB := `---
title: Page B
type: concept
tags: [a]
---
# Page B
No outbound links.
`
	pageC := `---
title: Page C
type: concept
tags: [a]
---
# Page C
Orphan page with no links.
`
	if err := os.WriteFile(filepath.Join(conceptsDir, "page-a.md"), []byte(pageA), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conceptsDir, "page-b.md"), []byte(pageB), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conceptsDir, "page-c.md"), []byte(pageC), 0644); err != nil {
		t.Fatal(err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	s := vaultstats.Compute(v, filepath.Join(dir, "nonexistent-journal"), time.Now(), 90)

	// Edges (resolved link occurrences, including self): [[page-b]] x2 + [[page-a]] x1 = 3
	if s.Edges != 3 {
		t.Errorf("s.Edges = %d, want 3", s.Edges)
	}
	// DistinctEdges (distinct (from, to) pairs excluding self): only (page-a -> page-b) = 1
	if s.DistinctEdges != 1 {
		t.Errorf("s.DistinctEdges = %d, want 1", s.DistinctEdges)
	}
	if s.Broken != 1 {
		t.Errorf("s.Broken = %d, want 1", s.Broken)
	}

	var buf bytes.Buffer
	if err := vaultstats.WriteText(&buf, s); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	wantLine := "links: 3 refs (1 distinct page pairs), broken: 1, orphans: 1"
	if !strings.Contains(out, wantLine) {
		t.Errorf("WriteText missing expected line %q\nFull output:\n%s", wantLine, out)
	}
}

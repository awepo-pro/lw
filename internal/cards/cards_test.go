package cards

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/vault"
)

func setupTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()

	schemaPath := filepath.Join("..", "..", "spec", "fixtures", "minimal", "SCHEMA.md")
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read fixture SCHEMA.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), schemaBytes, 0o644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}

	for _, sub := range []string{"wiki/concepts", "wiki/entities", "wiki/comparisons"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	kvCacheMD := `---
title: KV Cache
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [inference, memory]
sources: [raw/articles/kv-cache-explained.md]
confidence: high
---

# KV Cache

The key/value cache stores per-layer attention projections from previous decoding steps so tokens attend to cached keys and values.^[raw/articles/kv-cache-explained.md] See [[transformer-arch|Transformer Architecture]] for details.

## Mechanism

During generation, keys and values are computed once and stored in GPU memory to avoid recomputation on future tokens.

## Variants

- Dense attention
- Sparse attention
- Flash attention

## Sources

- Vaswani et al., 2017

## Short

1234567890
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "kv-cache.md"), []byte(kvCacheMD), 0o644); err != nil {
		t.Fatalf("write kv-cache.md: %v", err)
	}

	gpuMD := `---
title: GPU
created: 2026-08-20
updated: 2026-08-29
type: entity
tags: [hardware]
sources: [raw/articles/gpu.md]
confidence: high
---

# GPU

A graphics processing unit is a specialized electronic circuit designed to manipulate and alter memory to accelerate parallel image and tensor processing.
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "entities", "gpu.md"), []byte(gpuMD), 0o644); err != nil {
		t.Fatalf("write gpu.md: %v", err)
	}

	mqaMD := `---
title: MQA vs GQA
created: 2026-08-20
updated: 2026-08-29
type: comparison
tags: [inference]
sources: [raw/articles/mqa-vs-gqa.md]
confidence: high
---

# MQA vs GQA

Multi-Query Attention shares a single key/value head across all query heads, whereas Grouped-Query Attention groups multiple query heads to share key/value heads.
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "comparisons", "mqa-vs-gqa.md"), []byte(mqaMD), 0o644); err != nil {
		t.Fatalf("write mqa-vs-gqa.md: %v", err)
	}

	fencedMD := `---
title: Fenced Lead
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [inference]
sources: [raw/articles/test.md]
confidence: high
---

# Fenced Lead

` + "```python\ndef compute():\n    return 42\n```" + `

This is the actual prose lead paragraph describing the system mechanism and implementation details.
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "fenced-lead.md"), []byte(fencedMD), 0o644); err != nil {
		t.Fatalf("write fenced-lead.md: %v", err)
	}

	transformerMD := `---
title: Transformer Architecture
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [transformers]
sources: [raw/articles/transformer.md]
confidence: high
---

# Transformer Architecture

The transformer architecture is based on self-attention mechanisms without recurrence.
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "transformer-arch.md"), []byte(transformerMD), 0o644); err != nil {
		t.Fatalf("write transformer-arch.md: %v", err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("open vault %s: %v", dir, err)
	}
	return v
}

// C1: exact cards (Front, Back, Section) for the concept page, citations removed, link label kept, skipped sections absent.
func TestC1ExactCards(t *testing.T) {
	v := setupTestVault(t)
	cards := Extract(v, Options{Type: "concept"})

	var kvCards []Card
	for _, c := range cards {
		if c.Path == "wiki/concepts/kv-cache.md" {
			kvCards = append(kvCards, c)
		}
	}

	if len(kvCards) != 3 {
		t.Fatalf("got %d cards for kv-cache.md, want 3", len(kvCards))
	}

	// Lead card
	wantLeadFront := "What is KV Cache?"
	wantLeadBack := "The key/value cache stores per-layer attention projections from previous decoding steps so tokens attend to cached keys and values. See Transformer Architecture for details."
	if kvCards[0].Front != wantLeadFront {
		t.Errorf("lead Front = %q, want %q", kvCards[0].Front, wantLeadFront)
	}
	if kvCards[0].Back != wantLeadBack {
		t.Errorf("lead Back = %q, want %q", kvCards[0].Back, wantLeadBack)
	}
	if kvCards[0].Section != "" {
		t.Errorf("lead Section = %q, want empty", kvCards[0].Section)
	}

	// Mechanism card
	wantMechFront := "KV Cache \u2014 Mechanism"
	wantMechBack := "During generation, keys and values are computed once and stored in GPU memory to avoid recomputation on future tokens."
	if kvCards[1].Front != wantMechFront {
		t.Errorf("mech Front = %q, want %q", kvCards[1].Front, wantMechFront)
	}
	if kvCards[1].Back != wantMechBack {
		t.Errorf("mech Back = %q, want %q", kvCards[1].Back, wantMechBack)
	}
	if kvCards[1].Section != "Mechanism" {
		t.Errorf("mech Section = %q, want %q", kvCards[1].Section, "Mechanism")
	}

	// Variants card
	wantVarFront := "KV Cache \u2014 Variants"
	wantVarBack := "- Dense attention\n- Sparse attention\n- Flash attention"
	if kvCards[2].Front != wantVarFront {
		t.Errorf("var Front = %q, want %q", kvCards[2].Front, wantVarFront)
	}
	if kvCards[2].Back != wantVarBack {
		t.Errorf("var Back = %q, want %q", kvCards[2].Back, wantVarBack)
	}
	if kvCards[2].Section != "Variants" {
		t.Errorf("var Section = %q, want %q", kvCards[2].Section, "Variants")
	}

	// Skipped sections absent
	for _, c := range kvCards {
		if strings.EqualFold(c.Section, "sources") || strings.EqualFold(c.Section, "short") {
			t.Errorf("found card for skipped section: %+v", c)
		}
	}
}

// C2: the list-only section produces a back made of "- item" lines via the MinBack rule.
func TestC2ListOnlySection(t *testing.T) {
	v := setupTestVault(t)
	cards := Extract(v, Options{})

	var varCard *Card
	for i := range cards {
		if cards[i].Path == "wiki/concepts/kv-cache.md" && cards[i].Section == "Variants" {
			varCard = &cards[i]
			break
		}
	}
	if varCard == nil {
		t.Fatal("Variants card not found")
	}

	lines := strings.Split(varCard.Back, "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines in back, want 3: %q", len(lines), varCard.Back)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "- ") {
			t.Errorf("line does not start with '- ': %q", l)
		}
	}
}

// C3: the code fence is skipped for the lead.
func TestC3CodeFenceSkippedForLead(t *testing.T) {
	v := setupTestVault(t)
	cards := Extract(v, Options{})

	var leadCard *Card
	for i := range cards {
		if cards[i].Path == "wiki/concepts/fenced-lead.md" && cards[i].Section == "" {
			leadCard = &cards[i]
			break
		}
	}
	if leadCard == nil {
		t.Fatal("fenced-lead lead card not found")
	}

	wantBack := "This is the actual prose lead paragraph describing the system mechanism and implementation details."
	if leadCard.Back != wantBack {
		t.Errorf("got Back %q, want %q", leadCard.Back, wantBack)
	}
	if strings.Contains(leadCard.Back, "python") || strings.Contains(leadCard.Back, "compute") {
		t.Errorf("code fence leaked into back: %q", leadCard.Back)
	}
}

// C4: IDs are stable across two Extract calls and differ per section.
func TestC4IDsStableAndUnique(t *testing.T) {
	v := setupTestVault(t)
	run1 := Extract(v, Options{})
	run2 := Extract(v, Options{})

	if len(run1) != len(run2) {
		t.Fatalf("card count mismatch: %d vs %d", len(run1), len(run2))
	}

	for i := range run1 {
		if run1[i].ID != run2[i].ID {
			t.Errorf("ID mismatch at index %d: %q vs %q", i, run1[i].ID, run2[i].ID)
		}
		if !strings.HasPrefix(run1[i].ID, "lw-") {
			t.Errorf("ID missing lw- prefix: %q", run1[i].ID)
		}
		if len(run1[i].ID) != 15 {
			t.Errorf("ID length = %d, want 15: %q", len(run1[i].ID), run1[i].ID)
		}
	}

	// Distinct IDs per section within kv-cache
	seen := make(map[string]bool)
	for _, c := range run1 {
		if c.Path == "wiki/concepts/kv-cache.md" {
			if seen[c.ID] {
				t.Errorf("duplicate ID on same page: %q", c.ID)
			}
			seen[c.ID] = true
		}
	}
}

// C5: truncation at MaxBack with " \u2026" on a word boundary.
func TestC5TruncationWordBoundary(t *testing.T) {
	v := setupTestVault(t)
	cards := Extract(v, Options{MaxBack: 50, MinBack: 20})

	var leadCard *Card
	for i := range cards {
		if cards[i].Path == "wiki/concepts/kv-cache.md" && cards[i].Section == "" {
			leadCard = &cards[i]
			break
		}
	}
	if leadCard == nil {
		t.Fatal("kv-cache lead card not found")
	}

	if !strings.HasSuffix(leadCard.Back, " \u2026") {
		t.Fatalf("truncated text does not end with ' \\u2026': %q", leadCard.Back)
	}

	prefix := strings.TrimSuffix(leadCard.Back, " \u2026")
	if utf8.RuneCountInString(prefix) > 50 {
		t.Errorf("prefix rune count %d > MaxBack 50: %q", utf8.RuneCountInString(prefix), prefix)
	}

	// Verify word boundary: full words only
	lastWord := prefix[strings.LastIndex(prefix, " ")+1:]
	if lastWord != "attention" {
		t.Errorf("last word before cut = %q, want 'attention'", lastWord)
	}
}

// C6: WriteAnkiTSV golden: the 6 header lines, 4 tab-separated fields per line, no raw newline or tab inside a field, <br> for newlines, HTML escaped.
func TestC6WriteAnkiTSVGolden(t *testing.T) {
	testCards := []Card{
		{
			ID:      "lw-1234567890ab",
			Front:   "What is <Special & Rare>?",
			Back:    "Line one\nLine two with <tags>",
			Path:    "wiki/concepts/special.md",
			Title:   "Special",
			Section: "",
			Tags:    []string{"lw", "concept", "ml-systems"},
		},
	}

	var buf bytes.Buffer
	if err := WriteAnkiTSV(&buf, testCards, "lw::testdeck"); err != nil {
		t.Fatalf("WriteAnkiTSV: %v", err)
	}

	content := buf.String()
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("got %d lines, want 7 (6 headers + 1 card)", len(lines))
	}

	wantHeaders := []string{
		"#separator:tab",
		"#html:true",
		"#notetype:Basic",
		"#deck:lw::testdeck",
		"#guid column:1",
		"#tags column:4",
	}
	for i, h := range wantHeaders {
		if lines[i] != h {
			t.Errorf("header line %d = %q, want %q", i, lines[i], h)
		}
	}

	cardLine := lines[6]
	fields := strings.Split(cardLine, "\t")
	if len(fields) != 4 {
		t.Fatalf("got %d fields, want 4 tab-separated fields in %q", len(fields), cardLine)
	}

	if fields[0] != "lw-1234567890ab" {
		t.Errorf("field 0 ID = %q", fields[0])
	}
	wantFront := "What is &lt;Special &amp; Rare&gt;?"
	if fields[1] != wantFront {
		t.Errorf("field 1 Front = %q, want %q", fields[1], wantFront)
	}
	wantBack := "Line one<br>Line two with &lt;tags&gt;<br><small>lw: wiki/concepts/special.md</small>"
	if fields[2] != wantBack {
		t.Errorf("field 2 Back = %q, want %q", fields[2], wantBack)
	}
	wantTags := "lw concept ml-systems"
	if fields[3] != wantTags {
		t.Errorf("field 3 Tags = %q, want %q", fields[3], wantTags)
	}

	// Verify no raw tab or newline inside fields
	for i, f := range fields {
		if strings.Contains(f, "\t") {
			t.Errorf("field %d contains raw tab: %q", i, f)
		}
		if strings.Contains(f, "\n") {
			t.Errorf("field %d contains raw newline: %q", i, f)
		}
	}
}

// C7: WriteMarkdown golden for one page.
func TestC7WriteMarkdownGolden(t *testing.T) {
	v := setupTestVault(t)
	allCards := Extract(v, Options{Type: "concept"})

	var kvCards []Card
	for _, c := range allCards {
		if c.Path == "wiki/concepts/kv-cache.md" {
			kvCards = append(kvCards, c)
		}
	}
	if len(kvCards) != 3 {
		t.Fatalf("got %d cards for kv-cache.md, want 3", len(kvCards))
	}

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, kvCards); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	wantMD := "#flashcards/lw/concept\n## KV Cache\n" +
		"What is KV Cache?\n?\n" +
		"The key/value cache stores per-layer attention projections from previous decoding steps so tokens attend to cached keys and values. See Transformer Architecture for details.\n" +
		"<!-- lw:" + kvCards[0].ID + " -->\n\n" +
		"KV Cache \u2014 Mechanism\n?\n" +
		"During generation, keys and values are computed once and stored in GPU memory to avoid recomputation on future tokens.\n" +
		"<!-- lw:" + kvCards[1].ID + " -->\n\n" +
		"KV Cache \u2014 Variants\n?\n" +
		"- Dense attention\n- Sparse attention\n- Flash attention\n" +
		"<!-- lw:" + kvCards[2].ID + " -->\n"

	if got := buf.String(); got != wantMD {
		t.Fatalf("WriteMarkdown mismatch:\nGOT:\n%s\nWANT:\n%s", got, wantMD)
	}
}

// C8: --type and --tag filters.
func TestC8Filters(t *testing.T) {
	v := setupTestVault(t)

	// Filter by Type
	entityCards := Extract(v, Options{Type: "entity"})
	if len(entityCards) != 1 {
		t.Fatalf("got %d entity cards, want 1", len(entityCards))
	}
	if entityCards[0].Title != "GPU" {
		t.Errorf("entity card Title = %q, want GPU", entityCards[0].Title)
	}

	comparisonCards := Extract(v, Options{Type: "comparison"})
	if len(comparisonCards) != 1 {
		t.Fatalf("got %d comparison cards, want 1", len(comparisonCards))
	}
	if comparisonCards[0].Title != "MQA vs GQA" {
		t.Errorf("comparison card Title = %q, want MQA vs GQA", comparisonCards[0].Title)
	}

	// Filter by single Tag
	hwCards := Extract(v, Options{Tags: []string{"hardware"}})
	if len(hwCards) != 1 || hwCards[0].Title != "GPU" {
		t.Errorf("tag hardware returned %+v, want GPU card", hwCards)
	}

	// Filter by multiple Tags (AND logic)
	memInfCards := Extract(v, Options{Tags: []string{"memory", "inference"}})
	for _, c := range memInfCards {
		if c.Path != "wiki/concepts/kv-cache.md" {
			t.Errorf("unexpected card from memory+inference: %s", c.Path)
		}
	}
	if len(memInfCards) != 3 {
		t.Errorf("got %d cards for memory+inference, want 3", len(memInfCards))
	}

	// Non-matching AND filter
	none := Extract(v, Options{Tags: []string{"hardware", "memory"}})
	if len(none) != 0 {
		t.Errorf("got %d cards for conflicting tags, want 0", len(none))
	}

	if comparisonCards[0].Front != "Compare: MQA vs GQA" {
		t.Errorf("comparison card Front = %q, want 'Compare: MQA vs GQA'", comparisonCards[0].Front)
	}
}

func TestCards_TableFirstSection(t *testing.T) {
	dir := t.TempDir()
	schema := `---
version: 1
---
# Schema
## Types
- concept
## Tags
- test
`
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755); err != nil {
		t.Fatal(err)
	}

	page := `---
title: Table Feature
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [test]
sources: []
confidence: high
---

# Table Feature

A feature concept with tables.

## Comparison Table

| Feature | Option A | Option B |
|---|---|---|
| Speed | 10ms | 50ms |
| Memory | 2GB | 8GB |

After the table note that should not displace the table.
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "table-feature.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	cards := Extract(v, Options{})
	var secCard *Card
	for i := range cards {
		if cards[i].Section == "Comparison Table" {
			secCard = &cards[i]
			break
		}
	}
	if secCard == nil {
		t.Fatalf("no card generated for section 'Comparison Table'")
	}
	wantBack := "Feature | Option A | Option B\nSpeed | 10ms | 50ms\nMemory | 2GB | 8GB"
	if secCard.Back != wantBack {
		t.Errorf("table-first card Back =\n%q\nwant:\n%q", secCard.Back, wantBack)
	}
}

func TestCards_ColonLeadParagraphWithTable(t *testing.T) {
	dir := t.TempDir()
	schema := `---
version: 1
---
# Schema
## Types
- concept
## Tags
- test
`
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755); err != nil {
		t.Fatal(err)
	}

	page := `---
title: Colon Feature
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [test]
sources: []
confidence: high
---

# Colon Feature

Lead description here.

## Options

The available configuration options are as follows:

| Setting | Default | Description |
| --- | --- | --- |
| timeout | 30s | Request timeout |
| retries | 3 | Max retry count |
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "colon-feature.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	cards := Extract(v, Options{})
	var secCard *Card
	for i := range cards {
		if cards[i].Section == "Options" {
			secCard = &cards[i]
			break
		}
	}
	if secCard == nil {
		t.Fatalf("no card generated for section 'Options'")
	}
	wantBack := "The available configuration options are as follows:\n\nSetting | Default | Description\ntimeout | 30s | Request timeout\nretries | 3 | Max retry count"
	if secCard.Back != wantBack {
		t.Errorf("colon-lead card Back =\n%q\nwant:\n%q", secCard.Back, wantBack)
	}
}

func TestCards_QueryFrontQuestionMark(t *testing.T) {
	dir := t.TempDir()
	schema := `---
version: 1
---
# Schema
## Types
- query
## Tags
- test
`
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "wiki", "queries"), 0o755); err != nil {
		t.Fatal(err)
	}

	page1 := `---
title: What is the optimal batch size?
created: 2026-08-20
updated: 2026-08-29
type: query
tags: [test]
sources: []
confidence: high
---

# What is the optimal batch size?

Optimal batch size depends on hardware memory and gradient stability.
`
	page2 := `---
title: How to tune learning rate
created: 2026-08-20
updated: 2026-08-29
type: query
tags: [test]
sources: []
confidence: high
---

# How to tune learning rate

Learning rate can be tuned using grid search or cosine annealing schedule.
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "queries", "q1.md"), []byte(page1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wiki", "queries", "q2.md"), []byte(page2), 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	cards := Extract(v, Options{Type: "query"})
	if len(cards) != 2 {
		t.Fatalf("got %d query cards, want 2", len(cards))
	}
	for _, c := range cards {
		if c.Title == "What is the optimal batch size?" {
			if c.Front != "What is the optimal batch size?" {
				t.Errorf("already ends with ?, got Front = %q, want no double ?", c.Front)
			}
		} else if c.Title == "How to tune learning rate" {
			if c.Front != "How to tune learning rate?" {
				t.Errorf("does not end with ?, got Front = %q, want single ?", c.Front)
			}
		}
	}
}

// TestR3NestedCodeFence verifies finding R-3: 4-backtick code fences do not prematurely close on nested 3-backtick lines.
func TestR3NestedCodeFence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte("# SCHEMA\n## Domain\ntest\n## Tags\n- test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := "---\ntitle: Markdown Fences\ntype: concept\ntags: [test]\n---\n# Markdown Fences\n\n" +
		"````markdown\n```go\nfunc secretInsideFence() string { return \"should not be in card\" }\n```\n````\n\n" +
		"This is the real prose that should become the card back.\n"
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "fences.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cards := Extract(v, Options{})
	if len(cards) == 0 {
		t.Fatalf("expected at least 1 card, got 0")
	}
	for _, c := range cards {
		if strings.Contains(c.Back, "secretInsideFence") {
			t.Errorf("card back leaked code inside fence: %q", c.Back)
		}
		if !strings.Contains(c.Back, "This is the real prose") {
			t.Errorf("card back missing real prose: %q", c.Back)
		}
	}
}

// TestR4TruncateRunesCJK verifies finding R-4: 650 CJK runes with one space at index 2
// yields a card whose back has >= 600 runes before the ellipsis.
func TestR4TruncateRunesCJK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte("# SCHEMA\n## Domain\ntest\n## Tags\n- test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755); err != nil {
		t.Fatal(err)
	}
	cjkBody := "\u6982\u5ff5 " + strings.Repeat("\u4e00", 647)
	page := "---\ntitle: \u91cf\u5b50\u8ba1\u7b97\ntype: concept\ntags: [test]\n---\n# \u91cf\u5b50\u8ba1\u7b97\n\n" + cjkBody + "\n"
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "quantum.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cards := Extract(v, Options{MaxBack: 600})
	if len(cards) == 0 {
		t.Fatalf("expected at least 1 card, got 0 (card was dropped)")
	}
	back := cards[0].Back
	ellipsisIdx := strings.Index(back, "\u2026")
	if ellipsisIdx == -1 {
		t.Fatalf("expected ellipsis in back: %q", back)
	}
	beforeEllipsis := strings.TrimRight(back[:ellipsisIdx], " ")
	runesCount := utf8.RuneCountInString(beforeEllipsis)
	if runesCount < 600 {
		t.Errorf("expected >= 600 runes before ellipsis, got %d", runesCount)
	}
}

// TestR6EmptyTitleUsesBasename verifies finding R-6: when FM.Title is empty,
// use the page basename without .md everywhere a title is shown in a card.
func TestR6EmptyTitleUsesBasename(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte("# SCHEMA\n## Domain\ntest\n## Tags\n- test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := "---\ntype: concept\ntags: [test]\n---\n# Heading Not Title\n\n" +
		"This is body of no-title page with enough runes to meet the min back requirement.\n\n" +
		"## Section One\n\n" +
		"This is section one body with enough runes to meet the min back requirement.\n"
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "no-title.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cards := Extract(v, Options{})
	if len(cards) < 2 {
		t.Fatalf("expected at least 2 cards, got %d", len(cards))
	}
	lead := cards[0]
	if lead.Title != "no-title" {
		t.Errorf("lead card Title = %q, want 'no-title'", lead.Title)
	}
	if lead.Front != "What is no-title?" {
		t.Errorf("lead card Front = %q, want 'What is no-title?'", lead.Front)
	}
	sec := cards[1]
	if sec.Title != "no-title" {
		t.Errorf("section card Title = %q, want 'no-title'", sec.Title)
	}
	if sec.Front != "no-title \u2014 Section One" {
		t.Errorf("section card Front = %q, want 'no-title \u2014 Section One'", sec.Front)
	}
}

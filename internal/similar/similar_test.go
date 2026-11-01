package similar

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// setupHandVault creates a test vault with the hand-crafted pages
// specified in the subtask requirements.
func setupHandVault(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	dir := t.TempDir()

	schemaContent := `# SCHEMA

## Domain
ml-systems

## Tags
- inference
- memory
- decoding
- quantization
- hardware
- culinary
`
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}

	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatalf("mkdir concepts: %v", err)
	}

	pages := map[string]string{
		"kv-cache.md": `---
title: KV Cache System
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [inference, memory]
sources: [raw/articles/kv-cache-explained.md]
confidence: high
---

# KV Cache System

The key value cache stores attention projections from earlier decoding steps so a model avoids recomputing activations.
Caching turns per token work into constant computation while memory scales with context length.
`,
		"key-value-cache.md": `---
title: KV Cache System
created: 2026-08-21
updated: 2026-08-29
type: concept
tags: [decoding]
sources: [raw/articles/kv-cache-explained.md]
confidence: high
---

# KV Cache System

The key value cache stores attention projections from earlier decoding steps so a model avoids recomputing activations.
Caching turns per token work into constant computation while memory scales with context length.
`,
		"paged-attention.md": `---
title: Paged Attention
created: 2026-08-22
updated: 2026-08-29
type: concept
tags: [inference, memory]
sources: [raw/papers/vllm.md]
confidence: high
---

# Paged Attention

Paged attention allocates physical memory in non-contiguous blocks for [[kv-cache]].
Dynamic memory management reduces fragmentation during continuous batching.
`,
		"cooking-pasta.md": `---
title: Cooking Italian Pasta
created: 2026-08-23
updated: 2026-08-29
type: concept
tags: [culinary]
sources: [raw/articles/pasta-recipe.md]
confidence: high
---

# Cooking Italian Pasta

Boil salted water vigorously in a large pot before adding dried pasta.
Cook until al dente then toss with fresh basil tomato sauce and olive oil.
`,
		"filler-a.md": `---
title: Quantization Methods
created: 2026-08-24
updated: 2026-08-29
type: concept
tags: [quantization]
sources: [raw/papers/quant.md]
confidence: high
---

# Quantization Methods

Quantization reduces floating point numerical precision down to integer representations.
Lower bit width reduces weights bandwidth across memory buses.
`,
		"filler-b.md": `---
title: Hardware Accelerators
created: 2026-08-25
updated: 2026-08-29
type: concept
tags: [hardware]
sources: [raw/papers/gpu.md]
confidence: high
---

# Hardware Accelerators

Hardware accelerators provide high tensor throughput and massive parallelism for matrix multiplication.
Memory bandwidth frequently limits systolic array utilization.
`,
	}

	for filename, content := range pages {
		p := filepath.Join(conceptsDir, filename)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write page %s: %v", filename, err)
		}
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	return v, dir
}

// TestSimilarR1Related tests R1:
// Related(kv-cache) ranks key-value-cache or paged-attention first and
// the cooking page last or absent; the Shared fields list the shared source/tag.
func TestSimilarR1Related(t *testing.T) {
	v, _ := setupHandVault(t)
	corp := NewCorpus(v)

	matches := corp.Related("wiki/concepts/kv-cache.md", 10)
	if len(matches) == 0 {
		t.Fatalf("expected matches, got none")
	}

	first := matches[0]
	if first.Path != "wiki/concepts/key-value-cache.md" && first.Path != "wiki/concepts/paged-attention.md" {
		t.Errorf("first match = %s, want key-value-cache or paged-attention", first.Path)
	}

	// Verify cooking page is absent or last
	for i, m := range matches {
		if m.Path == "wiki/concepts/cooking-pasta.md" && i != len(matches)-1 {
			t.Errorf("cooking page at rank %d, want last or absent", i)
		}
	}

	// Verify Shared fields
	var foundDupe, foundPaged bool
	for _, m := range matches {
		if m.Path == "wiki/concepts/key-value-cache.md" {
			foundDupe = true
			if len(m.Shared.Sources) == 0 || m.Shared.Sources[0] != "raw/articles/kv-cache-explained.md" {
				t.Errorf("expected shared source raw/articles/kv-cache-explained.md, got %v", m.Shared.Sources)
			}
		}
		if m.Path == "wiki/concepts/paged-attention.md" {
			foundPaged = true
			hasTag := false
			for _, tg := range m.Shared.Tags {
				if tg == "inference" || tg == "memory" {
					hasTag = true
					break
				}
			}
			if !hasTag {
				t.Errorf("expected shared tag inference or memory, got %v", m.Shared.Tags)
			}
		}
	}
	if !foundDupe {
		t.Errorf("expected key-value-cache in matches")
	}
	if !foundPaged {
		t.Errorf("expected paged-attention in matches")
	}
}

// TestSimilarR2Dupes tests R2:
// Dupes(0.45) returns exactly the kv-cache/key-value-cache pair first, Linked=false;
// the cooking page appears in no pair.
func TestSimilarR2Dupes(t *testing.T) {
	v, _ := setupHandVault(t)
	corp := NewCorpus(v)

	pairs := corp.Dupes(0.45, 10)
	if len(pairs) == 0 {
		t.Fatalf("expected at least one dupe pair, got none")
	}

	first := pairs[0]
	pairMatches := (first.A == "wiki/concepts/key-value-cache.md" && first.B == "wiki/concepts/kv-cache.md") ||
		(first.A == "wiki/concepts/kv-cache.md" && first.B == "wiki/concepts/key-value-cache.md")
	if !pairMatches {
		t.Errorf("first dupe pair = (%s, %s), want kv-cache and key-value-cache", first.A, first.B)
	}
	if first.Linked {
		t.Errorf("first dupe pair Linked = true, want false")
	}

	for _, p := range pairs {
		if p.A == "wiki/concepts/cooking-pasta.md" || p.B == "wiki/concepts/cooking-pasta.md" {
			t.Errorf("cooking page appeared in dupe pair: %+v", p)
		}
	}
}

// TestSimilarR3Symmetry tests R3:
// Related scores are symmetric: score(a->b) == score(b->a) within 1e-9.
func TestSimilarR3Symmetry(t *testing.T) {
	v, _ := setupHandVault(t)
	corp := NewCorpus(v)

	for _, pA := range corp.pages {
		matchesA := corp.Related(pA.path, len(corp.pages))
		scoreMapA := make(map[string]float64)
		for _, m := range matchesA {
			scoreMapA[m.Path] = m.Score
		}

		for _, pB := range corp.pages {
			if pA.path == pB.path {
				continue
			}
			matchesB := corp.Related(pB.path, len(corp.pages))
			scoreMapB := make(map[string]float64)
			for _, m := range matchesB {
				scoreMapB[m.Path] = m.Score
			}

			scoreAB := scoreMapA[pB.path]
			scoreBA := scoreMapB[pA.path]

			diff := math.Abs(scoreAB - scoreBA)
			if diff > 1e-9 {
				t.Errorf("asymmetric score between %s and %s: %v vs %v (diff %e)",
					pA.path, pB.path, scoreAB, scoreBA, diff)
			}
		}
	}
}

// TestSimilarR4SignalsAndNoSelf tests R4:
// every signal in [0,1]; a page is never related to itself.
func TestSimilarR4SignalsAndNoSelf(t *testing.T) {
	v, _ := setupHandVault(t)
	corp := NewCorpus(v)

	checkSignals := func(s Signals, context string) {
		if s.Links < 0 || s.Links > 1 {
			t.Errorf("%s Links signal out of bounds: %v", context, s.Links)
		}
		if s.Sources < 0 || s.Sources > 1 {
			t.Errorf("%s Sources signal out of bounds: %v", context, s.Sources)
		}
		if s.Tags < 0 || s.Tags > 1 {
			t.Errorf("%s Tags signal out of bounds: %v", context, s.Tags)
		}
		if s.Title < 0 || s.Title > 1 {
			t.Errorf("%s Title signal out of bounds: %v", context, s.Title)
		}
		if s.Body < 0 || s.Body > 1 {
			t.Errorf("%s Body signal out of bounds: %v", context, s.Body)
		}
	}

	for _, p := range corp.pages {
		matches := corp.Related(p.path, len(corp.pages))
		for _, m := range matches {
			if m.Path == p.path {
				t.Errorf("page %s is related to itself", p.path)
			}
			checkSignals(m.Signals, p.path+" -> "+m.Path)
		}
	}

	pairs := corp.Dupes(0.0, 100)
	for _, pair := range pairs {
		if pair.A == pair.B {
			t.Errorf("dupe pair with identical pages: %s", pair.A)
		}
		checkSignals(pair.Signals, pair.A+" <-> "+pair.B)
	}
}

// TestSimilarR5Determinism tests R5:
// two NewCorpus runs give identical Related and Dupes output.
func TestSimilarR5Determinism(t *testing.T) {
	v, _ := setupHandVault(t)
	corp1 := NewCorpus(v)
	corp2 := NewCorpus(v)

	for _, p := range corp1.pages {
		m1 := corp1.Related(p.path, 10)
		m2 := corp2.Related(p.path, 10)
		if !reflect.DeepEqual(m1, m2) {
			t.Errorf("Related mismatch for %s:\n%+v\nvs\n%+v", p.path, m1, m2)
		}
	}

	p1 := corp1.Dupes(0.2, 50)
	p2 := corp2.Dupes(0.2, 50)
	if !reflect.DeepEqual(p1, p2) {
		t.Errorf("Dupes mismatch:\n%+v\nvs\n%+v", p1, p2)
	}
}

// TestSimilarR6Stopwords tests R6:
// stopwords and short tokens do not create similarity:
// two pages whose bodies share only stopwords have Body == 0.
func TestSimilarR6Stopwords(t *testing.T) {
	dir := t.TempDir()
	schemaContent := "# SCHEMA\n\n## Domain\ntest\n\n## Tags\n- a\n"
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatalf("mkdir concepts: %v", err)
	}

	page1 := `---
title: Alpha
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [a]
---

# Alpha

The and for with that this from are was were has have not but its into an is at by.
Alpha unique content.
`
	page2 := `---
title: Beta
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [a]
---

# Beta

The and for with that this from are was were has have not but its into an is at by.
Beta completely different text.
`
	if err := os.WriteFile(filepath.Join(conceptsDir, "p1.md"), []byte(page1), 0644); err != nil {
		t.Fatalf("write p1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(conceptsDir, "p2.md"), []byte(page2), 0644); err != nil {
		t.Fatalf("write p2: %v", err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	corp := NewCorpus(v)
	matches := corp.Related("wiki/concepts/p1.md", 10)
	for _, m := range matches {
		if m.Path == "wiki/concepts/p2.md" {
			if m.Signals.Body != 0 {
				t.Errorf("expected Body == 0 for stopword-only overlap, got %v", m.Signals.Body)
			}
		}
	}
}

// TestSimilarR7InlineCitations tests R7:
// inline citations count as sources (a page citing ^[raw/papers/p.md p.3] with empty FM.Sources
// shares the source with a page listing raw/papers/p.md in sources:).
func TestSimilarR7InlineCitations(t *testing.T) {
	dir := t.TempDir()
	schemaContent := "# SCHEMA\n\n## Domain\ntest\n\n## Tags\n- a\n"
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatalf("mkdir concepts: %v", err)
	}

	pageInline := `---
title: Inline Citer
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [a]
---

# Inline Citer

This is supported by evidence.^[raw/papers/p.md p.3]
`
	pageFrontmatter := `---
title: FM Lister
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [a]
sources: [raw/papers/p.md]
---

# FM Lister

Different body text here.
`
	if err := os.WriteFile(filepath.Join(conceptsDir, "inline.md"), []byte(pageInline), 0644); err != nil {
		t.Fatalf("write inline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(conceptsDir, "fm.md"), []byte(pageFrontmatter), 0644); err != nil {
		t.Fatalf("write fm: %v", err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	corp := NewCorpus(v)
	matches := corp.Related("wiki/concepts/inline.md", 10)
	var found bool
	for _, m := range matches {
		if m.Path == "wiki/concepts/fm.md" {
			found = true
			if m.Signals.Sources != 1.0 {
				t.Errorf("expected Sources == 1.0, got %v", m.Signals.Sources)
			}
			if len(m.Shared.Sources) == 0 || m.Shared.Sources[0] != "raw/papers/p.md" {
				t.Errorf("expected shared source raw/papers/p.md, got %v", m.Shared.Sources)
			}
		}
	}
	if !found {
		t.Errorf("expected fm.md to be related to inline.md via shared source")
	}
}

// TestSimilarLinkedPairHiddenByDefault tests that a linked pair is excluded by default
// and shown when IncludeLinked is true.
func TestSimilarLinkedPairHiddenByDefault(t *testing.T) {
	dir := t.TempDir()
	schemaContent := "# SCHEMA\n\n## Domain\ntest\n\n## Tags\n- a\n"
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatalf("mkdir concepts: %v", err)
	}

	page1 := `---
title: Alpha Page
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [a]
---
# Alpha Page
Alpha content linking [[beta-page]].
`
	page2 := `---
title: Beta Page
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [a]
---
# Beta Page
Alpha content linking beta.
`
	if err := os.WriteFile(filepath.Join(conceptsDir, "alpha-page.md"), []byte(page1), 0644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(conceptsDir, "beta-page.md"), []byte(page2), 0644); err != nil {
		t.Fatalf("write beta: %v", err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	corp := NewCorpus(v)

	// Default: excludes linked pairs
	pairsDefault := corp.Dupes(0.1, 10)
	if len(pairsDefault) != 0 {
		t.Errorf("expected 0 pairs by default for linked pages, got %d", len(pairsDefault))
	}

	// With IncludeLinked: pair is returned
	pairsLinked := corp.Dupes(0.1, 10, DupeOptions{IncludeLinked: true})
	if len(pairsLinked) != 1 {
		t.Fatalf("expected 1 pair with IncludeLinked=true, got %d", len(pairsLinked))
	}
	if !pairsLinked[0].Linked {
		t.Errorf("expected Linked == true for linked pair")
	}
}

// TestSimilarTagOnAllPagesZeroScore tests that a tag present on all pages has weight 0
// and yields Tags == 0 similarity.
func TestSimilarTagOnAllPagesZeroScore(t *testing.T) {
	dir := t.TempDir()
	schemaContent := "# SCHEMA\n\n## Domain\ntest\n\n## Tags\n- universal\n- special\n"
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatalf("mkdir concepts: %v", err)
	}

	p1 := `---
title: Page One
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [universal]
---
# Page One
Apples and oranges are different fruits.
`
	p2 := `---
title: Page Two
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [universal]
---
# Page Two
Cars and trucks are motor vehicles.
`
	p3 := `---
title: Page Three
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [universal, special]
---
# Page Three
Quantum computing uses qubits and entanglement.
`
	if err := os.WriteFile(filepath.Join(conceptsDir, "p1.md"), []byte(p1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conceptsDir, "p2.md"), []byte(p2), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conceptsDir, "p3.md"), []byte(p3), 0644); err != nil {
		t.Fatal(err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	corp := NewCorpus(v)

	matches := corp.Related("wiki/concepts/p1.md", 10)
	for _, m := range matches {
		if m.Path == "wiki/concepts/p2.md" {
			if m.Signals.Tags != 0 {
				t.Errorf("expected Tags == 0 for universal tag on all pages, got %v", m.Signals.Tags)
			}
			// Shared tags should still list "universal" for human explanation
			foundShared := false
			for _, st := range m.Shared.Tags {
				if st == "universal" {
					foundShared = true
				}
			}
			if !foundShared {
				t.Errorf("expected 'universal' in Shared.Tags, got %v", m.Shared.Tags)
			}
		}
	}
}

// TestSimilarSharedSourceOnlyDupeScoreZero tests that two pages sharing only a source
// and having no title or body similarity get a dupe score of 0.
func TestSimilarSharedSourceOnlyDupeScoreZero(t *testing.T) {
	dir := t.TempDir()
	schemaContent := "# SCHEMA\n\n## Domain\ntest\n\n## Tags\n- a\n"
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(schemaContent), 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatalf("mkdir concepts: %v", err)
	}

	p1 := `---
title: Quantum Cryptography
created: 2026-08-20
updated: 2026-08-29
type: concept
sources: [raw/papers/shared-source.md]
---
# Quantum Cryptography
Photons transmit quantum key distribution signals securely across optical fiber networks.
`
	p2 := `---
title: Culinary Baking Techniques
created: 2026-08-20
updated: 2026-08-29
type: concept
sources: [raw/papers/shared-source.md]
---
# Culinary Baking Techniques
Yeast fermentation creates carbon dioxide bubbles inside sourdough bread dough.
`
	if err := os.WriteFile(filepath.Join(conceptsDir, "crypto.md"), []byte(p1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conceptsDir, "baking.md"), []byte(p2), 0644); err != nil {
		t.Fatal(err)
	}

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	corp := NewCorpus(v)

	// Threshold 0.0001: dupe score must be exactly 0 because max(Title, Body) == 0
	pairs := corp.Dupes(0.0001, 10, DupeOptions{IncludeLinked: true})
	if len(pairs) != 0 {
		t.Errorf("expected 0 dupe pairs, got %d (pairs: %+v)", len(pairs), pairs)
	}

	// Double check that Sources signal is 1.0, but Title and Body are 0
	signals, _ := corp.computeSignals(corp.byPath["wiki/concepts/crypto.md"], corp.byPath["wiki/concepts/baking.md"])
	if signals.Sources != 1.0 {
		t.Errorf("expected Sources == 1.0, got %v", signals.Sources)
	}
	if signals.Title != 0 {
		t.Errorf("expected Title == 0, got %v", signals.Title)
	}
	if signals.Body != 0 {
		t.Errorf("expected Body == 0, got %v", signals.Body)
	}
}
